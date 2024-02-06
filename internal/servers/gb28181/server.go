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
)

type GB28181PublishReq struct {
	PathName string `json:"pathName"`
	SSRC     string `json:"ssrc"`
}

type GB28181PublishRes struct {
	PathName  string `json:"pathName"`
	UUID      string `json:"uuid"`
	LocalPort uint16 `json:"localPort"`
}

type GB28181PlayReq struct {
	PathName   string `json:"pathName"`
	SSRC       string `json:"ssrc"`
	RemoteIP   string `json:"remoteIp"`
	RemotePort uint16 `json:"remotePort"`
}

type GB28181PlayRes struct {
	PathName  string `json:"pathName"`
	SessionID string `json:"sessionId"`
	LocalPort uint16 `json:"localPort"`
}

type gb28181NewSessionRes struct {
	sx            *session
	err           error
	errStatusCode int
}

type gb28181NewSessionReq struct {
	pathName   string
	ssrc       string
	remoteIp   string
	remotePort uint16
	publish    bool
	res        chan gb28181NewSessionRes
}

type gb28181DeleteSessionRes struct {
	err           error
	errStatusCode int
}

type gb28181DeleteSessionReq struct {
	uuid string
	res  chan gb28181DeleteSessionRes
}

type PortPair struct {
	RTPPort  int
	RTCPPort int
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
	TrustedProxies conf.IPsOrCIDRs
	ReadTimeout    conf.StringDuration
	MinRTPPort     int
	MaxRTPPort     int
	PathManager    defs.PathManager
	Parent         serverParent

	ctx        context.Context
	ctxCancel  func()
	httpServer *httpServer
	sessions   map[string]*session
	PortPairs  []PortPair

	// in
	chSessionNew    chan gb28181NewSessionReq
	chSessionDelete chan gb28181DeleteSessionReq
	chSessionClose  chan *session

	// out
	done chan struct{}
}

// Initialize initializes the server.
func (s *Server) Initialize() error {
	ctx, ctxCancel := context.WithCancel(context.Background())

	s.ctx = ctx
	s.ctxCancel = ctxCancel
	s.sessions = make(map[string]*session)
	s.PortPairs = make([]PortPair, 0)
	s.chSessionNew = make(chan gb28181NewSessionReq)
	s.chSessionDelete = make(chan gb28181DeleteSessionReq)
	s.done = make(chan struct{})

	for i := s.MinRTPPort; i < s.MaxRTPPort; i += 2 {
		pair := PortPair{
			RTPPort:  i,
			RTCPPort: i + 1,
		}
		s.PortPairs = append(s.PortPairs, pair)
	}
	s.httpServer = &httpServer{
		address:        s.Address,
		encryption:     s.Encryption,
		serverKey:      s.ServerKey,
		serverCert:     s.ServerCert,
		allowOrigin:    s.AllowOrigin,
		trustedProxies: s.TrustedProxies,
		readTimeout:    s.ReadTimeout,
		pathManager:    s.PathManager,
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

// Log implements logger.Writer.
func (s *Server) Log(level logger.Level, format string, args ...interface{}) {
	s.Parent.Log(level, "[WebRTC] "+format, args...)
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
		case req := <-s.chSessionNew:
			if len(s.PortPairs) > 0 {
				portPair := s.PortPairs[0]
				s.PortPairs = s.PortPairs[1:]

				sx := &session{
					parentCtx:   s.ctx,
					req:         req,
					wg:          &wg,
					portPair:    portPair,
					pathManager: s.PathManager,
					parent:      s,
				}
				sx.initialize()
				s.sessions[sx.uuid.String()] = sx
				req.res <- gb28181NewSessionRes{sx: sx}
			} else {
				req.res <- gb28181NewSessionRes{
					err: errors.New("port resource not available"),
				}
			}

		case req := <-s.chSessionDelete:
			sx, ok := s.sessions[req.uuid]
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

		case sx := <-s.chSessionClose:
			s.PortPairs = append(s.PortPairs, sx.portPair)
			delete(s.sessions, sx.uuid.String())

		case <-s.ctx.Done():
			break outer
		}
	}

	s.ctxCancel()

	wg.Wait()

	s.httpServer.close()
}

// sessionNew is called by gb28181HTTPServer.
func (s *Server) sessionNew(req gb28181NewSessionReq) gb28181NewSessionRes {
	req.res = make(chan gb28181NewSessionRes)

	select {
	case s.chSessionNew <- req:
		res1 := <-req.res

		select {
		case res2 := <-req.res:
			return res2

		case <-res1.sx.ctx.Done():
			return gb28181NewSessionRes{err: fmt.Errorf("terminated"), errStatusCode: http.StatusInternalServerError}
		}

	case <-s.ctx.Done():
		return gb28181NewSessionRes{err: fmt.Errorf("terminated"), errStatusCode: http.StatusInternalServerError}
	}
}

// sessionNew is called by gb28181HTTPServer.
func (s *Server) sessionDelete(req gb28181DeleteSessionReq) gb28181DeleteSessionRes {
	req.res = make(chan gb28181DeleteSessionRes)

	select {
	case s.chSessionDelete <- req:
		res := <-req.res
		return res

	case <-s.ctx.Done():
		return gb28181DeleteSessionRes{err: fmt.Errorf("terminated"), errStatusCode: http.StatusInternalServerError}
	}
}

// sessionClose is called by gb28181Session.
func (s *Server) sessionClose(sx *session) {
	select {
	case s.chSessionClose <- sx:
	case <-s.ctx.Done():
	}
}
