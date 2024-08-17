package gb28181

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/bluenviron/mediacommon/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/pkg/codecs/h265"
	"github.com/bluenviron/mediacommon/pkg/codecs/mpeg4audio"
	"github.com/bluenviron/mediamtx/internal/asyncwriter"
	"github.com/bluenviron/mediamtx/internal/auth"
	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/protocols/gb28181"
	"github.com/bluenviron/mediamtx/internal/protocols/gb28181/mpegps"
	"github.com/bluenviron/mediamtx/internal/stream"
	"github.com/bluenviron/mediamtx/internal/unit"
	"github.com/google/uuid"
	mpeg2 "github.com/qdwl/mpegps"
)

var errNoSupportedCodecs = errors.New(
	"the stream doesn't contain any supported codec, which are currently H264, MPEG-4 Audio, MPEG-1/2 Audio")

type session struct {
	parentCtx      context.Context
	writeQueueSize int
	req            gb28181NewSessionReq
	wg             *sync.WaitGroup
	portPair       PortPair
	pathManager    serverPathManager
	parent         *Server

	ctx        context.Context
	ctxCancel  func()
	created    time.Time
	uuid       uuid.UUID
	answerSent bool
	conn       *gb28181.Conn
	vcid       uint8
	acid       uint8
}

func (s *session) initialize() {
	ctx, ctxCancel := context.WithCancel(s.parentCtx)

	s.ctx = ctx
	s.ctxCancel = ctxCancel
	s.created = time.Now()
	s.uuid = uuid.New()
	s.conn = gb28181.NewConn(ctx, s.portPair.RTPPort, s.req.remoteIp, s.req.remotePort, s.req.transport)

	s.Log(logger.Info, "created by %s", s.req.pathName)

	s.wg.Add(1)
	go s.run()
}

func (s *session) Log(level logger.Level, format string, args ...interface{}) {
	id := hex.EncodeToString(s.uuid[:4])
	s.parent.Log(level, "[session %v] "+format, append([]interface{}{id}, args...)...)
}

func (s *session) Update(req gb28181UpdateSessionReq) {
	s.conn.SetRemoteAddr(req.remoteIp, req.remotePort)
}

func (s *session) Close() {
	s.ctxCancel()
}

func (s *session) run() {
	defer s.wg.Done()

	errStatusCode, err := s.runInner()

	if !s.answerSent {
		select {
		case s.req.res <- gb28181NewSessionRes{
			err:           err,
			errStatusCode: errStatusCode,
		}:
		case <-s.ctx.Done():
		}
	}

	s.ctxCancel()

	s.parent.closeSession(s)

	s.Log(logger.Info, "closed (%v)", err)
}

func (s *session) runInner() (int, error) {
	if s.req.direction == "recvonly" {
		return s.runPublish()
	} else if s.req.direction == "sendonly" {
		return s.runRead()
	} else {
		return 0, fmt.Errorf("unsupport direction")
	}
}

func (s *session) runPublish() (int, error) {
	path, err := s.pathManager.AddPublisher(defs.PathAddPublisherReq{
		Author: s,
		AccessRequest: defs.PathAccessRequest{
			Name:     s.req.pathName,
			Publish:  true,
			SkipAuth: true,
		},
	})
	if err != nil {
		var terr auth.Error
		if errors.As(err, &terr) {
			// wait some seconds to mitigate brute force attacks
			<-time.After(auth.PauseAfterError)

			return http.StatusUnauthorized, err
		}

		return http.StatusBadRequest, err
	}

	defer path.RemovePublisher(defs.PathRemovePublisherReq{Author: s})

	s.writeAnswer()

	tracks, err := s.conn.ProbeTracks()
	if err != nil {
		return 0, err
	}

	medias := make([]*description.Media, 0)
	mediaCallbacks := make(map[uint8]func(time.Duration, []byte), len(tracks))
	var stream *stream.Stream

	for _, track := range tracks {
		var medi *description.Media

		switch codec := track.Codec.(type) {
		case *mpegps.CodecH264:
			medi = &description.Media{
				Type: description.MediaTypeVideo,
				Formats: []format.Format{
					&format.H264{
						PayloadTyp:        96,
						PacketizationMode: 1,
					}},
			}

			mediaCallbacks[track.StreamType] = func(pts time.Duration, data []byte) {
				au, err := h264.AnnexBUnmarshal(data)
				if err != nil {
					s.Log(logger.Warn, "%v", err)
					return
				}

				stream.WriteUnit(medi, medi.Formats[0], &unit.H264{
					Base: unit.Base{
						NTP: time.Now(),
						PTS: pts,
					},
					AU: au,
				})
			}

		case *mpegps.CodecH265:
			medi = &description.Media{
				Type: description.MediaTypeVideo,
				Formats: []format.Format{
					&format.H265{
						PayloadTyp: 96,
					}},
			}

			mediaCallbacks[track.StreamType] = func(pts time.Duration, data []byte) {
				au, err := h264.AnnexBUnmarshal(data)
				if err != nil {
					s.Log(logger.Warn, "%v", err)
					return
				}

				stream.WriteUnit(medi, medi.Formats[0], &unit.H265{
					Base: unit.Base{
						NTP: time.Now(),
						PTS: pts,
					},
					AU: au,
				})
			}

		case *mpegps.CodecMPEG4Audio:
			medi = &description.Media{
				Type: description.MediaTypeAudio,
				Formats: []format.Format{
					&format.MPEG4Audio{
						PayloadTyp:       96,
						SizeLength:       13,
						IndexLength:      3,
						IndexDeltaLength: 3,
						Config:           &codec.Config,
					}},
			}

			mediaCallbacks[track.StreamType] = func(pts time.Duration, data []byte) {
				var pkts mpeg4audio.ADTSPackets
				err := pkts.Unmarshal(data)
				if err != nil {
					s.Log(logger.Warn, "%v", err)
				}

				aus := make([][]byte, len(pkts))
				for i, pkt := range pkts {
					aus[i] = pkt.AU
				}

				stream.WriteUnit(medi, medi.Formats[0], &unit.MPEG4Audio{
					Base: unit.Base{
						NTP: time.Now(),
						PTS: pts,
					},
					AUs: aus,
				})
			}

		case *mpegps.CodecG711A:
			medi = &description.Media{
				Type: description.MediaTypeAudio,
				Formats: []format.Format{&format.G711{
					PayloadTyp:   98,
					MULaw:        true,
					SampleRate:   8000,
					ChannelCount: 1,
				}},
			}

			mediaCallbacks[track.StreamType] = func(pts time.Duration, data []byte) {
				stream.WriteUnit(medi, medi.Formats[0], &unit.G711{
					Base: unit.Base{
						NTP: time.Now(),
						PTS: pts,
					},
					Samples: data,
				})
			}

		case *mpegps.CodecG711U:
			medi = &description.Media{
				Type: description.MediaTypeAudio,
				Formats: []format.Format{&format.G711{
					PayloadTyp:   98,
					MULaw:        false,
					SampleRate:   8000,
					ChannelCount: 1,
				}},
			}

			mediaCallbacks[track.StreamType] = func(pts time.Duration, data []byte) {
				stream.WriteUnit(medi, medi.Formats[0], &unit.G711{
					Base: unit.Base{
						NTP: time.Now(),
						PTS: pts,
					},
					Samples: data,
				})
			}

		default:
			continue
		}
		medias = append(medias, medi)
	}

	stream, err = path.StartPublisher(defs.PathStartPublisherReq{
		Author:             s,
		Desc:               &description.Session{Medias: medias},
		GenerateRTPPackets: true,
	})
	if err != nil {
		return 0, err
	}

	for {
		frame, err := s.conn.ReadPsFrame()
		if err != nil {
			break
		}

		cb, ok := mediaCallbacks[uint8(frame.CID)]
		if !ok {
			continue
		}

		pts := time.Duration(frame.PTS) * time.Millisecond

		cb(pts, frame.Frame)
	}

	return 0, nil
}

func (s *session) runRead() (int, error) {
	_, err := s.pathManager.FindPathConf(defs.PathFindPathConfReq{
		AccessRequest: defs.PathAccessRequest{
			Name:     s.req.pathName,
			SkipAuth: true,
		},
	})
	if err != nil {
		return 0, err
	}

	s.writeAnswer()

	var path defs.Path
	var stream *stream.Stream

	ctx, ctxCancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer ctxCancel()

	ticker := time.NewTicker(time.Duration(10) * time.Millisecond)
	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-ticker.C:
		}
		path, stream, err = s.pathManager.AddReader(defs.PathAddReaderReq{
			Author: s,
			AccessRequest: defs.PathAccessRequest{
				Name:     s.req.pathName,
				SkipAuth: true,
			},
		})
		if err == nil {
			break
		}
		if !errors.Is(err, defs.PathNoOnePublishingError{
			PathName: s.req.pathName,
		}) {
			return 0, err
		}
	}
	defer path.RemoveReader(defs.PathRemoveReaderReq{Author: s})

	writer := asyncwriter.New(s.writeQueueSize, s)

	defer stream.RemoveReader(writer)

	videoFormat := s.setupVideo(stream, writer)
	audioFormat := s.setupAudio(stream, writer)

	if videoFormat == nil && audioFormat == nil {
		return 0, errNoSupportedCodecs
	}

	s.Log(logger.Info, "is reading from path '%s', %s",
		path.Name(), defs.FormatsInfo(stream.FormatsForReader(writer)))

	if videoFormat != nil {
		s.vcid = s.conn.AddStream(mpeg2.PS_STREAM_H264)
	}

	if audioFormat != nil {
		s.acid = s.conn.AddStream(mpeg2.PS_STREAM_AAC)
	}

	writer.Start()

	select {
	case <-s.ctx.Done():
		writer.Stop()
		return 0, fmt.Errorf("terminated")

	case err := <-writer.Error():
		return 0, err
	}
}

func (s *session) setupVideo(
	stream *stream.Stream,
	writer *asyncwriter.Writer,
) format.Format {
	var videoFormatH264 *format.H264
	videoMedia := stream.Desc().FindFormat(&videoFormatH264)

	if videoFormatH264 != nil {
		var videoDTSExtractor *h264.DTSExtractor

		stream.AddReader(writer, videoMedia, videoFormatH264, func(u unit.Unit) error {
			tunit := u.(*unit.H264)

			if tunit.AU == nil {
				return nil
			}

			idrPresent := false
			nonIDRPresent := false

			for _, nalu := range tunit.AU {
				typ := h264.NALUType(nalu[0] & 0x1F)
				switch typ {
				case h264.NALUTypeIDR:
					idrPresent = true

				case h264.NALUTypeNonIDR:
					nonIDRPresent = true
				}
			}

			var dts time.Duration

			// wait until we receive an IDR
			if videoDTSExtractor == nil {
				if !idrPresent {
					return nil
				}

				videoDTSExtractor = h264.NewDTSExtractor()

				var err error
				dts, err = videoDTSExtractor.Extract(tunit.AU, tunit.PTS)
				if err != nil {
					return err
				}
			} else {
				if !idrPresent && !nonIDRPresent {
					return nil
				}

				var err error
				dts, err = videoDTSExtractor.Extract(tunit.AU, tunit.PTS)
				if err != nil {
					return err
				}
			}

			return s.writeH264(tunit.PTS, dts, idrPresent, tunit.AU)
		})

		return videoFormatH264
	}

	var videoFormatH265 *format.H265
	videoMedia = stream.Desc().FindFormat(&videoFormatH265)
	if videoFormatH265 != nil {
		var videoDTSExtractor *h265.DTSExtractor

		stream.AddReader(writer, videoMedia, videoFormatH265, func(u unit.Unit) error {
			tunit := u.(*unit.H265)

			if tunit.AU == nil {
				return nil
			}

			idrPresent := false

			for _, nalu := range tunit.AU {
				typ := h265.NALUType((nalu[0] >> 1) & 0b111111)

				switch typ {
				case h265.NALUType_IDR_W_RADL, h265.NALUType_IDR_N_LP, h265.NALUType_CRA_NUT:
					idrPresent = true
				}
			}

			var dts time.Duration

			// wait until we receive an IDR
			if videoDTSExtractor == nil {
				if !idrPresent {
					return nil
				}

				videoDTSExtractor = h265.NewDTSExtractor()

				var err error
				dts, err = videoDTSExtractor.Extract(tunit.AU, tunit.PTS)
				if err != nil {
					return err
				}
			} else {
				var err error
				dts, err = videoDTSExtractor.Extract(tunit.AU, tunit.PTS)
				if err != nil {
					return err
				}
			}

			return s.writeH265(tunit.PTS, dts, idrPresent, tunit.AU)
		})

		return videoFormatH265
	}

	return nil
}

func (s *session) setupAudio(
	stream *stream.Stream,
	writer *asyncwriter.Writer,
) format.Format {
	var audioFormatMPEG4Audio *format.MPEG4Audio
	audioMedia := stream.Desc().FindFormat(&audioFormatMPEG4Audio)

	if audioMedia != nil {
		stream.AddReader(writer, audioMedia, audioFormatMPEG4Audio, func(u unit.Unit) error {
			tunit := u.(*unit.MPEG4Audio)

			if tunit.AUs == nil {
				return nil
			}

			for i, au := range tunit.AUs {
				err := s.WriteMPEG4Audio(
					tunit.PTS+time.Duration(i)*mpeg4audio.SamplesPerAccessUnit*
						time.Second/time.Duration(audioFormatMPEG4Audio.ClockRate()),
					au,
				)
				if err != nil {
					return err
				}
			}

			return nil
		})

		return audioFormatMPEG4Audio
	}

	return nil
}

func (s *session) writeH264(pts time.Duration, dts time.Duration, idrPresent bool, au [][]byte) error {
	enc, err := h264.AnnexBMarshal(au)
	if err != nil {
		return err
	}

	s.conn.Write(s.vcid, enc, uint64(pts.Milliseconds()), uint64(dts.Milliseconds()))

	return nil
}

func (s *session) writeH265(pts time.Duration, dts time.Duration, idrPresent bool, au [][]byte) error {
	enc, err := h264.AnnexBMarshal(au)
	if err != nil {
		return err
	}

	s.conn.Write(s.vcid, enc, uint64(pts.Milliseconds()), uint64(dts.Milliseconds()))

	return nil
}

func (s *session) WriteMPEG4Audio(pts time.Duration, au []byte) error {
	s.conn.Write(s.acid, au, uint64(pts.Milliseconds()), uint64(pts.Milliseconds()))
	return nil
}

func (s *session) writeAnswer() error {
	select {
	case s.req.res <- gb28181NewSessionRes{
		sx: s,
	}:
		s.answerSent = true
	case <-s.ctx.Done():
		return fmt.Errorf("terminated")
	}

	return nil
}

// apiReaderDescribe implements reader.
func (s *session) APIReaderDescribe() defs.APIPathSourceOrReader {
	return defs.APIPathSourceOrReader{
		Type: "gb28181Session",
		ID:   s.uuid.String(),
	}
}

// APISourceDescribe implements source.
func (s *session) APISourceDescribe() defs.APIPathSourceOrReader {
	return s.APIReaderDescribe()
}
