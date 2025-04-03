package gb28181

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/bluenviron/mediacommon/pkg/codecs/mpeg4audio"
	"github.com/bluenviron/mediamtx/internal/protocols/gb28181/mpegps"
	"github.com/bluenviron/mediamtx/internal/protocols/gb28181/transport"
	"github.com/pion/rtp"
	mpeg2 "github.com/qdwl/mpegps"
)

const (
	UdpSocket int = 1
	TcpClient int = 3
	TcpServer int = 4
)

type PsFrame struct {
	Frame []byte
	CID   mpeg2.PS_STREAM_TYPE
	PTS   uint64
	DTS   uint64
}

type trackProbeRes struct {
	tracks []*mpegps.Track
}

type trackProbeReq struct {
	resChan chan trackProbeRes
}

type Conn struct {
	port                int
	protocol            int
	transport           transport.Transport
	ctx                 context.Context
	ctxCancel           func()
	muxer               *mpeg2.PSMuxer
	demuxer             *mpeg2.PSDemuxer
	rtpPacketizer       *RtpPacketizer
	tracks              map[uint8]*mpegps.Track
	trackGatherComplete bool
	trackProbe          chan trackProbeReq
	pts                 uint64
	dts                 uint64
	buf                 []byte
	file                *os.File

	// in
	packetChan chan mpeg2.Display
	frameChan  chan *PsFrame
	done       chan struct{}

	// out
	demuxerChan chan *PsFrame
}

func NewConn(
	parnteCtx context.Context,
	port int,
	remoteIp string,
	remotePort int,
	protocol int,
) *Conn {
	ctx, ctxCancel := context.WithCancel(parnteCtx)

	c := &Conn{
		port:                port,
		protocol:            protocol,
		ctx:                 ctx,
		ctxCancel:           ctxCancel,
		muxer:               mpeg2.NewPsMuxer(),
		demuxer:             mpeg2.NewPSDemuxer(),
		tracks:              make(map[uint8]*mpegps.Track),
		trackGatherComplete: false,
		trackProbe:          make(chan trackProbeReq),
		pts:                 0,
		dts:                 0,
		buf:                 make([]byte, 1500),
		packetChan:          make(chan mpeg2.Display),
		frameChan:           make(chan *PsFrame),
		demuxerChan:         make(chan *PsFrame, 30),
		done:                make(chan struct{}),
	}

	c.rtpPacketizer = &RtpPacketizer{
		PayloadType: 96,
	}
	c.rtpPacketizer.Init()

	localAddr := fmt.Sprintf(":%d", port)
	remoteAddr := fmt.Sprintf("%s:%d", remoteIp, remotePort)

	if protocol == UdpSocket {
		c.transport, _ = transport.NewUdpSocket(c, localAddr, remoteAddr)
	} else if protocol == TcpClient {
		c.transport, _ = transport.NewTcpServer(c, localAddr, remoteAddr)
	} else if protocol == TcpServer {
		c.transport = nil
	}

	c.muxer.OnPacket = c.OnMuxPacket
	c.demuxer.OnPacket = c.OnDemuxPacket

	go c.run()

	return c
}

func (c *Conn) Close() {
	c.file.Close()
	c.ctxCancel()
	<-c.done
}

func (c *Conn) SetRemoteAddr(remoteIp string, remotePort int) {
	if c.protocol == TcpServer {
		localAddr := fmt.Sprintf(":%d", c.port)
		remoteAddr := fmt.Sprintf("%s:%d", remoteIp, remotePort)

		c.transport, _ = transport.NewTcpClient(c, localAddr, remoteAddr)
	}
}

func (c *Conn) Port() int {
	return c.port
}

func (c *Conn) ProbeTracks() (tracks []*mpegps.Track, err error) {
	for {
		req := trackProbeReq{
			resChan: make(chan trackProbeRes),
		}

		select {
		case c.trackProbe <- req:
			select {
			case res := <-req.resChan:
				if len(res.tracks) > 0 {
					return res.tracks, nil
				}
				time.Sleep(10 * time.Millisecond)
				continue
			case <-c.ctx.Done():
				return nil, errors.New("GB28181 connection closed")
			}

		case <-time.After(10000 * time.Millisecond):
			return nil, errors.New("probe tracks timeout")

		case <-c.ctx.Done():
			return nil, errors.New("GB28181 connection closed")
		}
	}
}

func (c *Conn) ReadPsFrame() (frame *PsFrame, err error) {
	select {
	case frame = <-c.demuxerChan:
		return frame, nil
	case <-c.ctx.Done():
		return nil, errors.New("GB28181 connection closed")
	}
}

func (c *Conn) AddStream(cid mpeg2.PS_STREAM_TYPE) uint8 {
	return c.muxer.AddStream(cid)
}

func (c *Conn) OnDemuxPacket(pkg mpeg2.Display, decodeResult error) {
	select {
	case c.packetChan <- pkg:
	case <-c.ctx.Done():
		return
	}
}

func (c *Conn) OnMuxPacket(pkg []byte, ts uint64) {
	pkts, err := c.rtpPacketizer.Encode(pkg, uint32(ts))
	if err != nil {
		return
	}

	for _, pkt := range pkts {
		n, err := pkt.MarshalTo(c.buf)
		if err != nil {
			continue
		}
		c.write(c.buf[:n])
	}
}

func (c *Conn) OnFrame(frame []byte, cid mpeg2.PS_STREAM_TYPE, pts uint64, dts uint64) {
	f := &PsFrame{
		Frame: make([]byte, 0),
		CID:   cid,
		PTS:   pts,
		DTS:   dts,
	}

	f.Frame = append(f.Frame, frame...)

	select {
	case c.frameChan <- f:
	case <-c.ctx.Done():
		return
	}
}

func (c *Conn) run() {
	defer close(c.done)
	fmt.Println("GB281818 conn run")

	func() {
		for {
			select {
			case pkt := <-c.packetChan:
				c.ProcessPsPacket(pkt)

			case frame := <-c.frameChan:
				c.ProcessPsFrame(frame)

			case req := <-c.trackProbe:
				res := trackProbeRes{
					tracks: make([]*mpegps.Track, 0),
				}
				if c.trackGatherComplete {
					for _, track := range c.tracks {
						if track.Complete {
							res.tracks = append(res.tracks, track)
						}
					}
				}
				req.resChan <- res

			case <-c.ctx.Done():
				return
			}
		}
	}()

	if c.transport != nil {
		c.transport.Close()
	}

	fmt.Println("GB281818 conn exit")
}

func (c *Conn) ProcessRtpPacket(pkt *rtp.Packet) {
	c.demuxer.Input(pkt.Payload)
}

func (c *Conn) ProcessPsPacket(pkt mpeg2.Display) {
	if c.trackGatherComplete {
		return
	}

	switch value := pkt.(type) {
	case *mpeg2.Program_stream_map:
		for _, es := range value.Stream_map {
			switch es.Stream_type {
			case uint8(mpeg2.PS_STREAM_H264):
				if _, ok := c.tracks[uint8(mpeg2.PS_STREAM_H264)]; !ok {
					track := &mpegps.Track{
						StreamId:   es.Elementary_stream_id,
						StreamType: uint8(mpeg2.PS_STREAM_H264),
						Codec:      &mpegps.CodecH264{},
						Complete:   true,
						Updated:    time.Now(),
					}
					c.tracks[uint8(mpeg2.PS_STREAM_H264)] = track
				}

			case uint8(mpeg2.PS_STREAM_H265):
				if _, ok := c.tracks[uint8(mpeg2.PS_STREAM_H265)]; !ok {
					track := &mpegps.Track{
						StreamId:   es.Elementary_stream_id,
						StreamType: uint8(mpeg2.PS_STREAM_H265),
						Codec:      &mpegps.CodecH265{},
						Complete:   true,
						Updated:    time.Now(),
					}
					c.tracks[uint8(mpeg2.PS_STREAM_H264)] = track
				}

			case uint8(mpeg2.PS_STREAM_AAC):
				if _, ok := c.tracks[uint8(mpeg2.PS_STREAM_AAC)]; !ok {
					track := &mpegps.Track{
						StreamId:   es.Elementary_stream_id,
						StreamType: uint8(mpeg2.PS_STREAM_AAC),
						Complete:   false,
						Updated:    time.Now(),
					}
					c.tracks[uint8(mpeg2.PS_STREAM_AAC)] = track
				}

			case uint8(mpeg2.PS_STREAM_G711A):
				if _, ok := c.tracks[uint8(mpeg2.PS_STREAM_G711A)]; !ok {
					track := &mpegps.Track{
						StreamId:   es.Elementary_stream_id,
						StreamType: uint8(mpeg2.PS_STREAM_G711A),
						Codec:      &mpegps.CodecG711A{},
						Complete:   true,
						Updated:    time.Now(),
					}
					c.tracks[uint8(mpeg2.PS_STREAM_G711A)] = track
				}

			case uint8(mpeg2.PS_STREAM_G711U):
				if _, ok := c.tracks[uint8(mpeg2.PS_STREAM_G711U)]; !ok {
					track := &mpegps.Track{
						StreamId:   es.Elementary_stream_id,
						StreamType: uint8(mpeg2.PS_STREAM_G711U),
						Codec:      &mpegps.CodecG711U{},
						Complete:   true,
						Updated:    time.Now(),
					}
					c.tracks[uint8(mpeg2.PS_STREAM_G711U)] = track
				}
			}

		}
	case *mpeg2.PesPacket:
		count := 0
		for _, track := range c.tracks {
			if track.StreamId == value.Stream_id {
				if mpeg2.PS_STREAM_TYPE(track.StreamType) == mpeg2.PS_STREAM_AAC {
					if track, ok := c.tracks[uint8(mpeg2.PS_STREAM_AAC)]; ok {
						var adtsPkts mpeg4audio.ADTSPackets
						err := adtsPkts.Unmarshal(value.Pes_payload)
						if err != nil {
							continue
						}

						pkt := adtsPkts[0]
						conf := &mpeg4audio.Config{
							Type:         pkt.Type,
							SampleRate:   pkt.SampleRate,
							ChannelCount: pkt.ChannelCount,
						}

						track.Codec = &mpegps.CodecMPEG4Audio{
							Config: *conf,
						}
						track.Complete = true
					}
				}
				track.Updated = time.Now()
			}

			if track.Complete || track.Updated.Add(time.Second).Before(time.Now()) {
				count++
			}
		}
		if count == len(c.tracks) && count > 0 {
			c.trackGatherComplete = true
		}
	}

	if c.trackGatherComplete {
		c.demuxer.OnFrame = c.OnFrame
	}
}

func (c *Conn) ProcessPsFrame(frame *PsFrame) {
	select {
	case c.demuxerChan <- frame:
	case <-c.ctx.Done():
		return
	}
}

func (c *Conn) Write(sid uint8, frame []byte, pts uint64, dts uint64) {
	c.pts = pts
	c.dts = dts

	c.muxer.Write(sid, frame, pts, dts)
}

func (c *Conn) write(buf []byte) error {
	if c.transport != nil {
		return c.transport.Write(buf)
	}
	return nil
}
