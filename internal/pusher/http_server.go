package pusher

import (
	"fmt"
	"net/http"
	"time"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/protocols/httpp"
	"github.com/bluenviron/mediamtx/internal/restrictnetwork"
	"github.com/gin-gonic/gin"
)

type httpServer struct {
	address        string
	encryption     bool
	serverKey      string
	serverCert     string
	allowOrigin    string
	trustedProxies conf.IPNetworks
	readTimeout    conf.Duration
	parent         *Server
	inner          *httpp.Server
}

// Log implements logger.Writer.
func (s *httpServer) Log(level logger.Level, format string, args ...interface{}) {
	s.parent.Log(level, format, args...)
}

func (s *httpServer) initialize() error {
	if s.encryption {
		if s.serverCert == "" {
			return fmt.Errorf("server cert is missing")
		}
	} else {
		s.serverKey = ""
		s.serverCert = ""
	}

	router := gin.New()
	router.SetTrustedProxies(s.trustedProxies.ToTrustedProxies()) //nolint:errcheck
	router.POST("/push", s.onCreatePush)
	router.DELETE("/push", s.onDeletePush)
	network, address := restrictnetwork.Restrict("tcp", s.address)

	s.inner = &httpp.Server{
		Network:     network,
		Address:     address,
		ReadTimeout: time.Duration(s.readTimeout),
		Encryption:  s.encryption,
		ServerCert:  s.serverCert,
		ServerKey:   s.serverKey,
		Handler:     router,
		Parent:      s,
	}
	err := s.inner.Initialize()
	if err != nil {
		return err
	}

	return nil
}

func (s *httpServer) close() {
	s.inner.Close()
}

func (s *httpServer) onCreatePush(ctx *gin.Context) {
	req := CreatePushReq{}

	if err := ctx.ShouldBind(&req); err != nil {
		ctx.Writer.WriteHeader(http.StatusBadRequest)
		return
	}

	res := s.parent.newPush(newPushReq{
		pathName: req.PathName,
		pushAddr: req.PushAddr,
	})
	if res.err != nil {
		if res.errStatusCode != 0 {
			ctx.Writer.WriteHeader(res.errStatusCode)
		}
		return
	}

	ctx.Writer.WriteHeader(http.StatusOK)
}

func (s *httpServer) onDeletePush(ctx *gin.Context) {
	req := DeletePushReq{}

	if err := ctx.ShouldBind(&req); err != nil {
		ctx.Writer.WriteHeader(http.StatusBadRequest)
		return
	}

	res := s.parent.deletePush(deletePushReq{
		pathName: req.PathName,
		pushAddr: req.PushAddr,
	})
	if res.err != nil {
		if res.errStatusCode != 0 {
			ctx.Writer.WriteHeader(res.errStatusCode)
		}
		return
	}

	ctx.Writer.WriteHeader(http.StatusOK)
}
