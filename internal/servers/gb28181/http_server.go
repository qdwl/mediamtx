package gb28181

import (
	"fmt"
	"net/http"
	"strings"
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
	readTimeout    conf.StringDuration
	pathManager    serverPathManager
	parent         *Server

	inner *httpp.WrappedServer
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
	router.POST("/gb28181", s.onCreateStream)
	router.PUT("/gb28181/:id", s.onUpdateStream)
	router.DELETE("/gb28181/:id", s.onDeleteStream)
	network, address := restrictnetwork.Restrict("tcp", s.address)

	var err error
	s.inner, err = httpp.NewWrappedServer(
		network,
		address,
		time.Duration(s.readTimeout),
		s.serverCert,
		s.serverKey,
		router,
		s,
	)
	if err != nil {
		return err
	}

	return nil
}

func (s *httpServer) Log(level logger.Level, format string, args ...interface{}) {
	s.parent.Log(level, format, args...)
}

func (s *httpServer) close() {
	s.inner.Close()
}

func (s *httpServer) onCreateStream(ctx *gin.Context) {
	req := GB28181CreateReq{}

	if err := ctx.ShouldBind(&req); err != nil {
		ctx.Writer.WriteHeader(http.StatusBadRequest)
		return
	}

	res := s.parent.newSession(gb28181NewSessionReq{
		pathName:   req.PathName,
		ssrc:       req.SSRC,
		remoteIp:   req.RemoteIP,
		remotePort: req.RemotePort,
		direction:  strings.ToLower(req.Direction),
		transport:  req.Transport,
	})
	if res.err != nil {
		if res.errStatusCode != 0 {
			ctx.Writer.WriteHeader(res.errStatusCode)
		}
		return
	}

	res1 := GB28181CreateRes{
		PathName:  res.sx.req.pathName,
		SessionID: res.sx.uuid.String(),
		LocalPort: res.sx.conn.Port(),
	}

	ctx.JSON(http.StatusOK, &res1)
}

func (s *httpServer) onUpdateStream(ctx *gin.Context) {
	sessionId := ctx.Param("id")

	req := GB28181UpdateReq{}
	if err := ctx.ShouldBind(&req); err != nil {
		ctx.Writer.WriteHeader(http.StatusBadRequest)
		return
	}

	res := s.parent.updateSession(gb28181UpdateSessionReq{
		pathName:   req.PathName,
		ssrc:       req.SSRC,
		sessionId:  sessionId,
		remoteIp:   req.RemoteIP,
		remotePort: req.RemotePort,
	})
	if res.err != nil {
		if res.errStatusCode != 0 {
			ctx.Writer.WriteHeader(res.errStatusCode)
		}
		return
	}

	ctx.Writer.WriteHeader(http.StatusOK)
}

func (s *httpServer) onDeleteStream(ctx *gin.Context) {
	sessionId := ctx.Param("id")

	res := s.parent.deleteSession(gb28181DeleteSessionReq{
		sessionId: sessionId,
	})
	if res.err != nil {
		if res.errStatusCode != 0 {
			ctx.Writer.WriteHeader(res.errStatusCode)
		}
		return
	}
	ctx.Writer.WriteHeader(http.StatusOK)
}
