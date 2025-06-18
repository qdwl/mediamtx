package gb28181

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/stream"
)

type GB28181CreateReq struct {
	SSRC        string `json:"ssrc"`
	PayloadType uint8  `json:"payloadType"`
	RemoteIP    string `json:"remoteIp"`
	RemotePort  int    `json:"remotePort"`
	Transport   int    `json:"transport"`
	Direction   string `json:"direction"`
}

type GB28181CreateRes struct {
	SessionID string `json:"sessionId"`
	LocalPort int    `json:"localPort"`
}

type GB28181UpdateReq struct {
	SessionID   string `json:"sessionId"`
	SSRC        string `json:"ssrc"`
	PayloadType uint8  `json:"payloadType"`
	RemoteIP    string `json:"remoteIp"`
	RemotePort  int    `json:"remotePort"`
}

type GB28181DeleteReq struct {
	SessionID string `json:"sessionId"`
}

type gb28181NewSessionRes struct {
	sx            *session
	err           error
	errStatusCode int
}

type gb28181NewSessionReq struct {
	pathName    string
	ssrc        string
	payloadType uint8
	remoteIp    string
	remotePort  int
	transport   int
	direction   string
	res         chan gb28181NewSessionRes
}

type gb28181UpdateSessionRes struct {
	err           error
	errStatusCode int
}

type gb28181UpdateSessionReq struct {
	pathName    string
	sessionId   string
	ssrc        string
	payloadType uint8
	remoteIp    string
	remotePort  int
	res         chan gb28181UpdateSessionRes
}

type gb28181DeleteSessionRes struct {
	err           error
	errStatusCode int
}

type gb28181DeleteSessionReq struct {
	pathName  string
	sessionId string
	res       chan gb28181DeleteSessionRes
}

type PortPair struct {
	RTPPort  int
	RTCPPort int
}

type serverPathManager interface {
	FindPathConf(req defs.PathFindPathConfReq) (*conf.Path, error)
	AddPublisher(req defs.PathAddPublisherReq) (defs.Path, error)
	AddReader(req defs.PathAddReaderReq) (defs.Path, *stream.Stream, error)
}

type serverParent interface {
	logger.Writer
}

// Server is a GB28181 server.
type Server struct {
	Address        string
	Encryption     bool
	ServerKey      string
	ServerCert     string
	AllowOrigin    string
	TrustedProxies conf.IPNetworks
	ReadTimeout    conf.Duration
	WriteQueueSize int
	MinRTPPort     int
	MaxRTPPort     int
	PathManager    serverPathManager
	Parent         serverParent

	ctx        context.Context
	ctxCancel  func()
	httpServer *httpServer
	sessions   map[string]*session
	portPairs  []PortPair

	// in
	chNewSession    chan gb28181NewSessionReq
	chUpdateSession chan gb28181UpdateSessionReq
	chDeleteSession chan gb28181DeleteSessionReq
	chCloseSession  chan *session

	// out
	done chan struct{}
}

// Initialize initializes the server.
func (s *Server) Initialize() error {
	ctx, ctxCancel := context.WithCancel(context.Background())

	s.ctx = ctx
	s.ctxCancel = ctxCancel
	s.sessions = make(map[string]*session)
	s.portPairs = make([]PortPair, 0)
	s.chNewSession = make(chan gb28181NewSessionReq)
	s.chUpdateSession = make(chan gb28181UpdateSessionReq)
	s.chDeleteSession = make(chan gb28181DeleteSessionReq)
	s.chCloseSession = make(chan *session)
	s.done = make(chan struct{})

	for i := s.MinRTPPort; i < s.MaxRTPPort; i += 2 {
		pair := PortPair{
			RTPPort:  i,
			RTCPPort: i + 1,
		}
		s.portPairs = append(s.portPairs, pair)
	}

	s.httpServer = &httpServer{
		address:        s.Address,
		encryption:     s.Encryption,
		serverKey:      s.ServerKey,
		serverCert:     s.ServerCert,
		allowOrigin:    s.AllowOrigin,
		trustedProxies: s.TrustedProxies,
		readTimeout:    s.ReadTimeout,
		parent:         s,
	}
	err := s.httpServer.initialize()
	if err != nil {
		ctxCancel()
		return err
	}

	str := "listener opened on " + s.Address + " (HTTP)"
	s.Log(logger.Info, str)

	go s.run()

	return nil
}

// Log is the main logging function.
func (s *Server) Log(level logger.Level, format string, args ...interface{}) {
	s.Parent.Log(level, "[GB28181] "+format, args...)
}

// Close closes the server.
func (s *Server) Close() {
	s.Log(logger.Info, "listener is closing")
	s.ctxCancel()
	<-s.done
}

func (s *Server) run() {
	defer close(s.done)

	var wg sync.WaitGroup

outer:
	for {
		select {
		case req := <-s.chNewSession:
			if len(s.portPairs) > 0 {
				portPair := s.portPairs[0]
				s.portPairs = s.portPairs[1:]

				sx := &session{
					parentCtx:      s.ctx,
					writeQueueSize: s.WriteQueueSize,
					req:            req,
					wg:             &wg,
					portPair:       portPair,
					pathManager:    s.PathManager,
					parent:         s,
				}
				sx.initialize()
				s.sessions[sx.uuid.String()] = sx
				req.res <- gb28181NewSessionRes{sx: sx}
			} else {
				req.res <- gb28181NewSessionRes{
					err: errors.New("port resource not available"),
				}
			}

		case req := <-s.chUpdateSession:
			sx, ok := s.sessions[req.sessionId]
			if !ok {
				req.res <- gb28181UpdateSessionRes{
					err:           errors.New("session not exist"),
					errStatusCode: http.StatusNotFound,
				}
			} else {
				sx.Update(req)
				req.res <- gb28181UpdateSessionRes{
					errStatusCode: http.StatusOK,
				}
			}

		case req := <-s.chDeleteSession:
			sx, ok := s.sessions[req.sessionId]
			if !ok {
				req.res <- gb28181DeleteSessionRes{
					err:           errors.New("session not exist"),
					errStatusCode: http.StatusNotFound,
				}
			} else {
				sx.Close()
				req.res <- gb28181DeleteSessionRes{
					errStatusCode: http.StatusOK,
				}
			}

		case sx := <-s.chCloseSession:
			s.portPairs = append(s.portPairs, sx.portPair)
			delete(s.sessions, sx.uuid.String())

		case <-s.ctx.Done():
			break outer
		}
	}

	s.ctxCancel()
	s.Log(logger.Info, "wait close")

	wg.Wait()

	s.httpServer.close()
}

// newSession is called by gb28181HTTPServer.
func (s *Server) newSession(req gb28181NewSessionReq) gb28181NewSessionRes {
	req.res = make(chan gb28181NewSessionRes)

	select {
	case s.chNewSession <- req:
		res1 := <-req.res
		return res1

		// select {
		// case res2 := <-req.res:
		// 	return res2

		// case <-res1.sx.ctx.Done():
		// 	return gb28181NewSessionRes{
		// 		err:           fmt.Errorf("terminated"),
		// 		errStatusCode: http.StatusInternalServerError,
		// 	}
		// }

	case <-s.ctx.Done():
		return gb28181NewSessionRes{
			err:           fmt.Errorf("terminated"),
			errStatusCode: http.StatusInternalServerError,
		}
	}
}

// updateSession is called by gb28181HTTPServer.
func (s *Server) updateSession(req gb28181UpdateSessionReq) gb28181UpdateSessionRes {
	req.res = make(chan gb28181UpdateSessionRes)

	select {
	case s.chUpdateSession <- req:
		res := <-req.res
		return res

	case <-s.ctx.Done():
		return gb28181UpdateSessionRes{
			err:           fmt.Errorf("terminated"),
			errStatusCode: http.StatusInternalServerError,
		}
	}
}

// deleteSession is called by gb28181HTTPServer.
func (s *Server) deleteSession(req gb28181DeleteSessionReq) gb28181DeleteSessionRes {
	req.res = make(chan gb28181DeleteSessionRes)

	select {
	case s.chDeleteSession <- req:
		res := <-req.res
		return res

	case <-s.ctx.Done():
		return gb28181DeleteSessionRes{
			err:           fmt.Errorf("terminated"),
			errStatusCode: http.StatusInternalServerError,
		}
	}
}

// sessionClose is called by gb28181Session.
func (m *Server) closeSession(sx *session) {
	select {
	case m.chCloseSession <- sx:
	case <-m.ctx.Done():
	}
}
