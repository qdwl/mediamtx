package gb28181

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/bluenviron/mediacommon/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/pkg/codecs/h265"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/bluenviron/mediamtx/internal/protocols/gb28181/mpegps"
	"github.com/bluenviron/mediamtx/internal/protocols/gb28181/transport"
	"github.com/pion/rtp"
	mpeg2 "github.com/qdwl/mpegps"
)

const (
	// 音频编码类型
	PayloadTypePCMU     = 0  // G.711 μ-law
	PayloadTypeGSM      = 3  // GSM 6.10
	PayloadTypeG723     = 4  // G.723.1
	PayloadTypeDVI4_8K  = 5  // DVI4 8kHz
	PayloadTypeDVI4_16K = 6  // DVI4 16kHz
	PayloadTypeLPC      = 7  // LPC
	PayloadTypePCMA     = 8  // G.711 A-law
	PayloadTypeG722     = 9  // G.722
	PayloadTypeL16_2    = 10 // L16 2通道
	PayloadTypeL16_1    = 11 // L16 单通道
	PayloadTypeQCELP    = 12 // QCELP
	PayloadTypeCN       = 13 // Comfort Noise
	PayloadTypeMPA      = 14 // MPEG Audio
	PayloadTypeG728     = 15 // G.728
	PayloadTypeDVI4_11K = 16 // DVI4 11.025kHz
	PayloadTypeDVI4_22K = 17 // DVI4 22.05kHz
	PayloadTypeG729     = 18 // G.729

	// 视频编码类型
	PayloadTypeCelB   = 25 // CelB
	PayloadTypeJPEG   = 26 // JPEG
	PayloadTypeNV     = 28 // nv
	PayloadTypeH261   = 31 // H.261
	PayloadTypeMPV    = 32 // MPEG Video
	PayloadTypeMP2T   = 33 // MPEG2 Transport
	PayloadTypeH263   = 34 // H.263
	PayloadTypeMepgPs = 96
)

// OnFrameFunc is the prototype of the callback passed to OnFrameFunc().
type OnFrameFunc func(pts time.Duration, data []byte)

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
	trackGatherComplete atomic.Bool
	trackProbe          chan trackProbeReq
	OnFrameFuncMap      map[uint8]OnFrameFunc
	buf                 []byte
	timebase            int64
	payloadType         uint8

	vcid uint8
	acid uint8

	IDRPresent bool
	startRead  atomic.Bool

	// out
	frameCache []*PsFrame

	// in
	packetChan chan mpeg2.Display
	done       chan struct{}
}

func NewConn(
	parentCtx context.Context,
	port int,
	remoteIp string,
	remotePort int,
	protocol int,
	payloadType uint8,
) *Conn {
	ctx, ctxCancel := context.WithCancel(parentCtx)

	c := &Conn{
		port:           port,
		protocol:       protocol,
		ctx:            ctx,
		ctxCancel:      ctxCancel,
		muxer:          mpeg2.NewPsMuxer(),
		demuxer:        mpeg2.NewPSDemuxer(),
		tracks:         make(map[uint8]*mpegps.Track),
		trackProbe:     make(chan trackProbeReq),
		OnFrameFuncMap: make(map[uint8]OnFrameFunc),
		buf:            make([]byte, 1500),
		payloadType:    payloadType,
		// timebase:       time.Now().UnixMilli(),
		frameCache: make([]*PsFrame, 0),
		packetChan: make(chan mpeg2.Display),
		done:       make(chan struct{}),
	}

	c.rtpPacketizer = &RtpPacketizer{
		PayloadType: c.payloadType,
	}
	c.rtpPacketizer.Init()

	localAddr := fmt.Sprintf(":%d", port)
	remoteAddr := fmt.Sprintf("%s:%d", remoteIp, remotePort)

	if protocol == UdpSocket {
		c.transport, _ = transport.NewUdpSocket(c, localAddr, remoteAddr)
	} else if protocol == TcpClient {
		c.transport, _ = transport.NewTcpClient(c, localAddr, remoteAddr)
	} else if protocol == TcpServer {
		c.transport, _ = transport.NewTcpServer(c, localAddr, remoteAddr)
	}

	c.muxer.OnPacket = c.OnMuxPacket
	c.demuxer.OnPacket = c.OnDemuxPacket
	c.demuxer.OnFrame = c.OnFrame

	go c.run()

	return c
}

func (c *Conn) Close() {
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

func (c *Conn) SetPayloadType(pl uint8) {
	c.payloadType = pl
	c.rtpPacketizer.PayloadType = pl
}

func (c *Conn) Port() int {
	return c.port
}

// Tracks returns detected tracks.
func (c *Conn) Tracks() []*mpegps.Track {
	tracks := make([]*mpegps.Track, 0)
	for _, track := range c.tracks {
		if track.Complete {
			tracks = append(tracks, track)
		}
	}
	return tracks
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

func (c *Conn) AddVideoStream(cid mpeg2.PS_STREAM_TYPE) {
	c.vcid = c.muxer.AddStream(cid)
}

func (c *Conn) AddAudioStream(cid mpeg2.PS_STREAM_TYPE) {
	c.acid = c.muxer.AddStream(cid)
}

func (c *Conn) OnDemuxPacket(pkg mpeg2.Display, decodeResult error) {
	c.ProcessPsPacket(pkg)
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

func (c *Conn) onFrameFunc(cid uint8, cb OnFrameFunc) {
	c.OnFrameFuncMap[cid] = cb
}

func (c *Conn) StartRead() {
	c.startRead.Store(true)
}

func (c *Conn) OnFrame(frame []byte, cid mpeg2.PS_STREAM_TYPE, pts uint64, dts uint64) {
	if c.timebase == 0 {
		// c.timebase = int64(pts)
		c.timebase = time.Now().UnixMilli()
	}
	// ts := time.Duration(pts - uint64(c.timebase))
	ts := time.Duration(time.Now().UnixMilli() - c.timebase)

	if !c.startRead.Load() {
		f := &PsFrame{
			Frame: make([]byte, 0),
			CID:   cid,
			PTS:   uint64(ts),
			DTS:   uint64(ts),
		}

		f.Frame = append(f.Frame, frame...)
		c.frameCache = append(c.frameCache, f)
	} else {
		if len(c.frameCache) > 0 {
			for _, val := range c.frameCache {
				cb, ok := c.OnFrameFuncMap[uint8(val.CID)]
				if ok {
					cb(time.Duration(val.PTS), val.Frame)
				}
			}
			c.frameCache = c.frameCache[:0]
		}

		cb, ok := c.OnFrameFuncMap[uint8(cid)]
		if ok {
			cb(ts, frame)
		}
	}
}

func (c *Conn) run() {
	defer close(c.done)

	func() {
		for {
			select {
			// case pkt := <-c.packetChan:
			// 	c.ProcessPsPacket(pkt)
			case req := <-c.trackProbe:
				res := trackProbeRes{
					tracks: make([]*mpegps.Track, 0),
				}
				if c.trackGatherComplete.Load() {
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
}

func (c *Conn) ProcessRtpPacket(pkt *rtp.Packet) {
	c.demuxer.Input(pkt.Payload)
}

func (c *Conn) ProcessPsPacket(pkt mpeg2.Display) {
	if c.trackGatherComplete.Load() {
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
						Complete:   false,
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
						Complete:   false,
						Updated:    time.Now(),
					}
					c.tracks[uint8(mpeg2.PS_STREAM_H265)] = track
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
				if mpeg2.PS_STREAM_TYPE(track.StreamType) == mpeg2.PS_STREAM_H265 {
					if track, ok := c.tracks[uint8(mpeg2.PS_STREAM_H265)]; ok {
						var dec h264.AnnexB
						err := dec.Unmarshal(value.Pes_payload)
						if err != nil {
							return
						}

						codec := track.Codec.(*mpegps.CodecH265)

						for _, nalu := range dec {
							typ := h265.NALUType((nalu[0] >> 1) & 0b111111)
							switch typ {
							case h265.NALUType_VPS_NUT:
								codec.VPS = append(codec.VPS, nalu...)
							case h265.NALUType_SPS_NUT:
								codec.SPS = append(codec.SPS, nalu...)
							case h265.NALUType_PPS_NUT:
								codec.PPS = append(codec.PPS, nalu...)
							}
						}

						if len(codec.VPS) > 0 && len(codec.PPS) > 0 && len(codec.SPS) > 0 {
							track.Complete = true
						}
					}
				}
				if mpeg2.PS_STREAM_TYPE(track.StreamType) == mpeg2.PS_STREAM_H264 {
					if track, ok := c.tracks[uint8(mpeg2.PS_STREAM_H264)]; ok {
						var dec h264.AnnexB
						err := dec.Unmarshal(value.Pes_payload)
						if err != nil {
							return
						}

						codec := track.Codec.(*mpegps.CodecH264)

						for _, nalu := range dec {
							typ := h264.NALUType(nalu[0] & 0x1F)
							switch typ {
							case h264.NALUTypeSPS:
								codec.SPS = append(codec.SPS, nalu...)
							case h264.NALUTypePPS:
								codec.PPS = append(codec.PPS, nalu...)
							}
						}

						if len(codec.SPS) > 0 && len(codec.PPS) > 0 {
							track.Complete = true
						}
					}
				}
				track.Updated = time.Now()
			}

			if track.Complete || track.Updated.Add(time.Second).Before(time.Now()) {
				count++
			}
		}
		if count == len(c.tracks) && count > 0 {
			c.trackGatherComplete.Store(true)
		}
	}
}

func (c *Conn) WriteVideo(frame []byte, pts uint64, dts uint64) {
	if err := c.muxer.Write(c.vcid, frame, pts, dts); err != nil {
		fmt.Printf("write video frame error %v\n", err)
	}
}

func (c *Conn) WriteAudio(frame []byte, pts uint64, dts uint64) {
	if c.payloadType == PayloadTypeMepgPs {
		if err := c.muxer.Write(c.acid, frame, pts, dts); err != nil {
			fmt.Printf("write audio frame error %v\n", err)
		}
	} else {
		c.OnMuxPacket(frame, pts)
	}
}

func (c *Conn) write(buf []byte) error {
	if c.transport != nil {
		return c.transport.Write(buf)
	}
	return nil
}
