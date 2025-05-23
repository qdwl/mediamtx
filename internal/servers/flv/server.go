package flv

import (
	"context"
	"fmt"
	"sync"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/protocols/flv"
	"github.com/bluenviron/mediamtx/internal/stream"
)

type newMuxerReq struct {
	remoteAddr string
	path       string
	flvConn    *flv.Conn
	res        chan newMuxerRes
}

type newMuxerRes struct {
	err   error
	muxer *muxer
}

type serverPathManager interface {
	FindPathConf(req defs.PathFindPathConfReq) (*conf.Path, error)
	AddReader(req defs.PathAddReaderReq) (defs.Path, *stream.Stream, error)
}

type serverParent interface {
	logger.Writer
}

// Server is a FLV server.
type Server struct {
	HttpAddress      string
	WebsocketAddress string
	Encryption       bool
	ServerKey        string
	ServerCert       string
	AllowOrigin      string
	TrustedProxies   conf.IPNetworks
	ReadTimeout      conf.Duration
	WriteQueueSize   int
	PathManager      serverPathManager
	Parent           serverParent

	ctx             context.Context
	ctxCancel       func()
	wg              sync.WaitGroup
	httpServer      *httpServer
	websocketServer *websocketServer
	muxers          map[*muxer]struct{}

	// in
	chNewMuxer   chan newMuxerReq
	chCloseMuxer chan *muxer
}

// Initialize initializes the server.
func (s *Server) Initialize() error {
	ctx, ctxCancel := context.WithCancel(context.Background())

	s.ctx = ctx
	s.ctxCancel = ctxCancel
	s.muxers = make(map[*muxer]struct{})
	s.chNewMuxer = make(chan newMuxerReq)
	s.chCloseMuxer = make(chan *muxer)

	s.httpServer = &httpServer{
		address:        s.HttpAddress,
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
	s.Log(logger.Info, "http-flv listener opened on "+s.HttpAddress)

	s.websocketServer = &websocketServer{
		address:        s.WebsocketAddress,
		encryption:     s.Encryption,
		serverKey:      s.ServerKey,
		serverCert:     s.ServerCert,
		allowOrigin:    s.AllowOrigin,
		trustedProxies: s.TrustedProxies,
		readTimeout:    s.ReadTimeout,
		parent:         s,
	}
	err = s.websocketServer.initialize()
	if err != nil {
		ctxCancel()
		return err
	}
	s.Log(logger.Info, "websocket-flv listener opened on "+s.WebsocketAddress)

	s.wg.Add(1)
	go s.run()

	return nil
}

// Log implements logger.Writer.
func (s *Server) Log(level logger.Level, format string, args ...interface{}) {
	s.Parent.Log(level, "[FLV] "+format, args...)
}

// Close closes the server.
func (s *Server) Close() {
	s.Log(logger.Info, "listener is closing")
	s.ctxCancel()
	s.wg.Wait()
}

func (s *Server) run() {
	defer s.wg.Done()

outer:
	for {
		select {
		case req := <-s.chNewMuxer:
			m, err := s.createMuxer(req.path, req.remoteAddr, req.flvConn)
			req.res <- newMuxerRes{
				muxer: m,
				err:   err,
			}
		case c := <-s.chCloseMuxer:
			if _, ok := s.muxers[c]; !ok {
				continue
			}
			delete(s.muxers, c)
		case <-s.ctx.Done():
			break outer
		}
	}
	s.ctxCancel()

	s.httpServer.close()
	s.websocketServer.close()
}

func (s *Server) createMuxer(path string, remoteAddr string, conn *flv.Conn) (*muxer, error) {
	r := &muxer{
		parentCtx:   s.ctx,
		remoteAddr:  remoteAddr,
		wg:          &s.wg,
		pathName:    path,
		pathManager: s.PathManager,
		parent:      s,
		flvConn:     conn,
	}
	r.initialize()
	s.muxers[r] = struct{}{}
	return r, nil
}

// closeMuxer is called by muxer.
func (s *Server) closeMuxer(c *muxer) {
	select {
	case s.chCloseMuxer <- c:
	case <-s.ctx.Done():
	}
}

func (s *Server) newMuxer(req newMuxerReq) (*muxer, error) {
	req.res = make(chan newMuxerRes)

	select {
	case s.chNewMuxer <- req:
		res := <-req.res
		return res.muxer, res.err
	case <-s.ctx.Done():
		return nil, fmt.Errorf("terminated")
	}
}
