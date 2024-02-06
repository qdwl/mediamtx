package gb28181

import (
	"fmt"
	"net/http"
	"time"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/protocols/httpserv"
	"github.com/bluenviron/mediamtx/internal/restrictnetwork"
	"github.com/gin-gonic/gin"
)

type httpServer struct {
	address        string
	encryption     bool
	serverKey      string
	serverCert     string
	allowOrigin    string
	trustedProxies conf.IPsOrCIDRs
	readTimeout    conf.StringDuration
	pathManager    defs.PathManager
	parent         *Server

	inner *httpserv.WrappedServer
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
	router.POST("/gb28181/publish", s.onPublish)
	router.POST("/gb28181/play", s.onPlay)
	router.DELETE("/gb28181/:id", s.onDelete)

	network, address := restrictnetwork.Restrict("tcp", s.address)

	var err error
	s.inner, err = httpserv.NewWrappedServer(
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

// Log implements logger.Writer.
func (s *httpServer) Log(level logger.Level, format string, args ...interface{}) {
	s.parent.Log(level, format, args...)
}

func (s *httpServer) close() {
	s.inner.Close()
}

func (s *httpServer) onPublish(ctx *gin.Context) {
	req := GB28181PublishReq{}

	if err := ctx.ShouldBind(&req); err != nil {
		ctx.Writer.WriteHeader(http.StatusBadRequest)
		return
	}

	res := s.parent.sessionNew(gb28181NewSessionReq{
		pathName: req.PathName,
		ssrc:     req.SSRC,
		publish:  true,
	})
	if res.err != nil {
		if res.errStatusCode != 0 {
			ctx.Writer.WriteHeader(res.errStatusCode)
		}
		return
	}

	res1 := GB28181PublishRes{
		PathName:  res.sx.req.pathName,
		UUID:      res.sx.uuid.String(),
		LocalPort: uint16(res.sx.conn.Port()),
	}

	ctx.JSON(http.StatusOK, &res1)
}

func (s *httpServer) onPlay(ctx *gin.Context) {

}

func (s *httpServer) onDelete(ctx *gin.Context) {
	uuid := ctx.Param("id")

	res := s.parent.sessionDelete(gb28181DeleteSessionReq{
		uuid: uuid,
	})
	if res.err != nil {
		if res.errStatusCode != 0 {
			ctx.Writer.WriteHeader(res.errStatusCode)
		}
		return
	}
	ctx.Writer.WriteHeader(http.StatusOK)
}
