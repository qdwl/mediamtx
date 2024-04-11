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
	"github.com/bluenviron/mediacommon/pkg/codecs/mpeg4audio"
	"github.com/bluenviron/mediamtx/internal/auth"
	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/protocols/gb28181"
	"github.com/bluenviron/mediamtx/internal/protocols/gb28181/mpegps"
	"github.com/bluenviron/mediamtx/internal/stream"
	"github.com/bluenviron/mediamtx/internal/unit"
	"github.com/google/uuid"
)

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

	chNew chan gb28181NewSessionReq
}

func (s *session) initialize() {
	ctx, ctxCancel := context.WithCancel(s.parentCtx)

	s.ctx = ctx
	s.ctxCancel = ctxCancel
	s.created = time.Now()
	s.uuid = uuid.New()
	s.conn = gb28181.NewConn(ctx, uint16(s.portPair.RTPPort), "RTP/AVP")
	s.chNew = make(chan gb28181NewSessionReq)

	s.Log(logger.Info, "created by %s", s.req.pathName)

	s.wg.Add(1)
	go s.run()
}

func (s *session) Log(level logger.Level, format string, args ...interface{}) {
	id := hex.EncodeToString(s.uuid[:4])
	s.parent.Log(level, "[session %v] "+format, append([]interface{}{id}, args...)...)
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
	if s.req.publish {
		return s.runPublish()
	}
	return s.runRead()
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
	return 0, nil
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
