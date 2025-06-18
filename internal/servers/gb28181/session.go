package gb28181

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/mediamtx/internal/auth"
	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/protocols/gb28181"
	"github.com/bluenviron/mediamtx/internal/stream"
	"github.com/google/uuid"
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
	timebase   int64
	path       defs.Path
}

func (s *session) initialize() {
	ctx, ctxCancel := context.WithCancel(s.parentCtx)

	s.ctx = ctx
	s.ctxCancel = ctxCancel
	s.created = time.Now()
	s.uuid = uuid.New()
	s.conn = gb28181.NewConn(ctx, s.portPair.RTPPort, s.req.remoteIp, s.req.remotePort, s.req.transport, s.req.payloadType)

	s.Log(logger.Info, "gb28181 session created by %s, port:%d, transport:%d, remoteIp:%s, remotePort:%d",
		s.req.pathName, s.portPair.RTPPort, s.req.transport, s.req.remoteIp, s.req.remotePort)

	s.wg.Add(1)
	go s.run()
}

func (s *session) Log(level logger.Level, format string, args ...interface{}) {
	// id := hex.EncodeToString(s.uuid[:4])
	id := s.uuid.String()
	s.parent.Log(level, "[session %v] "+format, append([]interface{}{id}, args...)...)
}

func (s *session) Update(req gb28181UpdateSessionReq) {
	s.conn.SetRemoteAddr(req.remoteIp, req.remotePort)
	s.conn.SetPayloadType(req.payloadType)
}

func (s *session) Close() {
	s.ctxCancel()
}

func (s *session) run() {
	defer s.wg.Done()

	err := s.runInner()

	s.ctxCancel()

	s.parent.closeSession(s)

	s.Log(logger.Info, "closed (%v)", err)
}

func (s *session) runInner() error {
	if s.req.direction == "recvonly" {
		return s.runPublish()
	} else if s.req.direction == "sendonly" {
		return s.runRead()
	} else {
		return fmt.Errorf("unsupport direction")
	}
}

func (s *session) runPublish() error {
	path, err := s.pathManager.AddPublisher(defs.PathAddPublisherReq{
		Author: s,
		AccessRequest: defs.PathAccessRequest{
			Name:     s.req.pathName,
			Publish:  true,
			SkipAuth: false,
		},
	})
	if err != nil {
		s.Log(logger.Error, "runPublish failed %v", err)
		var terr auth.Error
		if errors.As(err, &terr) {
			// wait some seconds to mitigate brute force attacks
			<-time.After(auth.PauseAfterError)

			return err
		}

		return err
	}

	defer path.RemovePublisher(defs.PathRemovePublisherReq{Author: s})

	_, err = s.conn.ProbeTracks()
	if err != nil {
		return err
	}

	var stream *stream.Stream

	medias, err := gb28181.ToStream(s.conn, &stream)
	if err != nil {
		return err
	}

	stream, err = path.StartPublisher(defs.PathStartPublisherReq{
		Author:             s,
		Desc:               &description.Session{Medias: medias},
		GenerateRTPPackets: true,
	})
	if err != nil {
		return err
	}
	s.conn.StartRead()

	select {
	case <-s.ctx.Done():
		return nil
	}
}

func (s *session) runRead() error {
	var path defs.Path
	var stream *stream.Stream
	var err error
	var count int = 0
	for {
		<-time.After(100 * time.Millisecond)
		path, stream, err = s.pathManager.AddReader(defs.PathAddReaderReq{
			Author: s,
			AccessRequest: defs.PathAccessRequest{
				Name:     s.req.pathName,
				SkipAuth: true,
			},
		})
		count++
		if err == nil || count > 100 {
			break
		} else {
			s.Log(logger.Error, "find stream failed, %v", err)
			continue
		}
	}
	if err != nil {
		return err
	}
	s.path = path

	defer s.path.RemoveReader(defs.PathRemoveReaderReq{Author: s})

	err = gb28181.FromStream(stream, s, s.conn)
	if err != nil {
		return err
	}

	s.Log(logger.Info, "is reading from path '%s', %s",
		path.Name(), defs.FormatsInfo(stream.ReaderFormats(s)))

	stream.StartReader(s)
	defer stream.RemoveReader(s)

	select {
	case <-s.ctx.Done():
		return fmt.Errorf("terminated")

	case err := <-stream.ReaderError(s):
		return err
	}
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
