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
	readTimeout    conf.Duration
	parent         *Server
	inner          *httpp.Server
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
	router.POST("/gb28181/:path", s.onCreateStream)
	router.PUT("/gb28181/:path", s.onUpdateStream)
	router.DELETE("/gb28181/:path", s.onDeleteStream)
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

func (s *httpServer) Log(level logger.Level, format string, args ...interface{}) {
	s.parent.Log(level, format, args...)
}

func (s *httpServer) close() {
	s.inner.Close()
}

func (s *httpServer) onCreateStream(ctx *gin.Context) {
	pathName := ctx.Param("path")
	req := GB28181CreateReq{}

	if err := ctx.ShouldBind(&req); err != nil {
		ctx.Writer.WriteHeader(http.StatusBadRequest)
		return
	}

	res := s.parent.newSession(gb28181NewSessionReq{
		pathName:    pathName,
		ssrc:        req.SSRC,
		payloadType: req.PayloadType,
		remoteIp:    req.RemoteIP,
		remotePort:  req.RemotePort,
		direction:   strings.ToLower(req.Direction),
		transport:   req.Transport,
	})
	if res.err != nil {
		if res.errStatusCode != 0 {
			ctx.Writer.WriteHeader(res.errStatusCode)
		}
		return
	}

	res1 := GB28181CreateRes{
		SessionID: res.sx.uuid.String(),
		LocalPort: res.sx.conn.Port(),
	}

	ctx.JSON(http.StatusOK, &res1)
}

func (s *httpServer) onUpdateStream(ctx *gin.Context) {
	pathName := ctx.Param("path")
	req := GB28181UpdateReq{}

	if err := ctx.ShouldBind(&req); err != nil {
		ctx.Writer.WriteHeader(http.StatusBadRequest)
		return
	}

	res := s.parent.updateSession(gb28181UpdateSessionReq{
		pathName:    pathName,
		ssrc:        req.SSRC,
		payloadType: req.PayloadType,
		sessionId:   req.SessionID,
		remoteIp:    req.RemoteIP,
		remotePort:  req.RemotePort,
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
	pathName := ctx.Param("path")

	req := GB28181DeleteReq{}
	if err := ctx.ShouldBind(&req); err != nil {
		ctx.Writer.WriteHeader(http.StatusBadRequest)
		return
	}

	res := s.parent.deleteSession(gb28181DeleteSessionReq{
		pathName:  pathName,
		sessionId: req.SessionID,
	})
	if res.err != nil {
		if res.errStatusCode != 0 {
			ctx.Writer.WriteHeader(res.errStatusCode)
		}
		return
	}
	ctx.Writer.WriteHeader(http.StatusOK)
}
