package pusher

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/stream"
)

type CreatePushReq struct {
	PathName string `json:"pathName"`
	PushAddr string `json:"pushAddr"`
}

type DeletePushReq struct {
	PathName string `json:"pathName"`
	PushAddr string `json:"pushAddr"`
}

type newPushReq struct {
	pathName string
	pushAddr string
	res      chan newPushRes
}

type newPushRes struct {
	pusher        *pusher
	err           error
	errStatusCode int
}

type deletePushReq struct {
	pathName string
	pushAddr string
	res      chan deletePushRes
}

type deletePushRes struct {
	err           error
	errStatusCode int
}

type serverPathManager interface {
	FindPathConf(req defs.PathFindPathConfReq) (*conf.Path, error)
	AddReader(req defs.PathAddReaderReq) (defs.Path, *stream.Stream, error)
}

type serverParent interface {
	logger.Writer
}

// Server is a Push Server.
type Server struct {
	Address        string
	Encryption     bool
	ServerKey      string
	ServerCert     string
	AllowOrigin    string
	TrustedProxies conf.IPNetworks
	ReadTimeout    conf.Duration
	WriteTimeout   conf.Duration
	PathManager    serverPathManager
	Parent         serverParent

	ctx        context.Context
	ctxCancel  func()
	httpServer *httpServer
	pushers    map[string]*pusher

	// in
	chNewPush    chan newPushReq
	chDeletePush chan deletePushReq
	chClosePush  chan *pusher

	// out
	done chan struct{}
}

// Initialize initializes the server.
func (s *Server) Initialize() error {
	ctx, ctxCancel := context.WithCancel(context.Background())

	s.ctx = ctx
	s.ctxCancel = ctxCancel
	s.pushers = make(map[string]*pusher)
	s.chNewPush = make(chan newPushReq)
	s.chDeletePush = make(chan deletePushReq)
	s.chClosePush = make(chan *pusher)
	s.done = make(chan struct{})

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

// Log implements logger.Writer.
func (s *Server) Log(level logger.Level, format string, args ...interface{}) {
	s.Parent.Log(level, "[PUSH] "+format, args...)
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
		case req := <-s.chNewPush:
			pusher := &pusher{
				parentCtx:    s.ctx,
				pathName:     req.pathName,
				pushAddr:     req.pushAddr,
				wg:           &wg,
				pathManager:  s.PathManager,
				parent:       s,
				writeTimeout: s.WriteTimeout,
				readTimeout:  s.ReadTimeout,
			}
			pusher.initialize()
			s.pushers[pusher.ID()] = pusher
			req.res <- newPushRes{
				pusher: pusher,
			}
		case req := <-s.chDeletePush:
			hash := md5.Sum([]byte(req.pathName + req.pushAddr))
			id := hex.EncodeToString(hash[:])
			pusher, ok := s.pushers[id]
			if !ok {
				req.res <- deletePushRes{
					err:           errors.New("pusher not exist"),
					errStatusCode: http.StatusNotFound,
				}
			} else {
				pusher.Close()
				req.res <- deletePushRes{
					errStatusCode: http.StatusOK,
				}
			}

		case pusher := <-s.chClosePush:
			delete(s.pushers, pusher.ID())

		case <-s.ctx.Done():
			break outer
		}
	}

	s.ctxCancel()
	wg.Wait()

	s.httpServer.close()
}

// newPush is called by pushHTTPServer
func (s *Server) newPush(req newPushReq) newPushRes {
	req.res = make(chan newPushRes)

	select {
	case s.chNewPush <- req:
		res1 := <-req.res

		select {
		case res2 := <-req.res:
			return res2

		case <-res1.pusher.ctx.Done():
			return newPushRes{
				err:           fmt.Errorf("terminated"),
				errStatusCode: http.StatusInternalServerError,
			}
		}

	case <-s.ctx.Done():
		return newPushRes{
			err:           fmt.Errorf("terminated"),
			errStatusCode: http.StatusInternalServerError,
		}
	}
}

// deletePush is called by pushHTTPServer.
func (s *Server) deletePush(req deletePushReq) deletePushRes {
	req.res = make(chan deletePushRes)

	select {
	case s.chDeletePush <- req:
		res := <-req.res
		return res

	case <-s.ctx.Done():
		return deletePushRes{
			err:           fmt.Errorf("terminated"),
			errStatusCode: http.StatusInternalServerError,
		}
	}
}

// closePush is called by pusher.
func (m *Server) closePush(pusher *pusher) {
	select {
	case m.chClosePush <- pusher:
	case <-m.ctx.Done():
	}
}
