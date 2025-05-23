package gb28181

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	vcid       uint8
	acid       uint8
	timebase   int64
}

func (s *session) initialize() {
	ctx, ctxCancel := context.WithCancel(s.parentCtx)

	s.ctx = ctx
	s.ctxCancel = ctxCancel
	s.created = time.Now()
	s.uuid = uuid.New()
	s.conn = gb28181.NewConn(ctx, s.portPair.RTPPort, s.req.remoteIp, s.req.remotePort, s.req.transport)

	s.Log(logger.Info, "gb28181 session created by %s, port:%d, transport:%d",
		s.req.pathName, s.portPair.RTPPort, s.req.transport)

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
}

func (s *session) Close() {
	s.ctxCancel()
}

func (s *session) run() {
	defer s.wg.Done()

	errStatusCode, err := s.runInner()

	if !s.answerSent {
		s.Log(logger.Info, "s.answerSent (%t)", s.answerSent)

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
		rv, err := s.runPublish()
		return rv, err
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
			SkipAuth: false,
		},
	})
	if err != nil {
		s.Log(logger.Error, "runPublish failed %v", err)
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

	_, err = s.conn.ProbeTracks()
	if err != nil {
		return http.StatusBadRequest, err
	}

	var stream *stream.Stream

	medias, err := gb28181.ToStream(s.conn, &stream)
	if err != nil {
		return 0, err
	}

	stream, err = path.StartPublisher(defs.PathStartPublisherReq{
		Author:             s,
		Desc:               &description.Session{Medias: medias},
		GenerateRTPPackets: true,
	})
	if err != nil {
		return 0, err
	}
	s.conn.StartRead()

	select {
	case <-s.ctx.Done():
		return 0, nil
	}
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
