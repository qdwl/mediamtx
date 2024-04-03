package flv

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/notedit/rtmp/format/flv/flvio"
	"golang.org/x/net/websocket"
)

type websocketServer struct {
	address        string
	encryption     bool
	serverKey      string
	serverCert     string
	allowOrigin    string
	trustedProxies conf.IPNetworks
	readTimeout    conf.StringDuration
	parent         *Server
	listener       net.Listener
}

func (s *websocketServer) initialize() error {
	if s.encryption {
		if s.serverCert == "" {
			return fmt.Errorf("server cert is missing")
		}
	} else {
		s.serverKey = ""
		s.serverCert = ""
	}

	listener, err := net.Listen("tcp", s.address)
	if err != nil {
		log.Fatal(err)
	}
	s.listener = listener

	go func() error {
		mux := http.NewServeMux()
		mux.Handle("/", websocket.Handler(s.handleConn))
		if err := http.Serve(s.listener, mux); err != nil {
			return err
		}

		return nil
	}()

	return nil
}

// Log implements logger.Writer.
func (s *websocketServer) Log(level logger.Level, format string, args ...interface{}) {
	s.parent.Log(level, format, args...)
}

func (s *websocketServer) close() {
	s.listener.Close()
}

func (s *websocketServer) handleConn(c *websocket.Conn) {
	r := c.Request()
	defer c.Close()
	s.Log(logger.Info, r.URL.Path)

	// remove leading prefix
	pa := r.URL.Path

	var path string

	switch {
	case pa == "", pa == "favicon.ico":
		return

	case strings.HasSuffix(pa, ".flv"):
		path = strings.TrimSuffix(strings.TrimLeft(pa, "/"), ".flv")

	default:
		return
	}

	buff := make([]byte, 256)
	conn := newConn()
	defer conn.close()

	err := s.parent.newMuxer(newMuxerReq{
		remoteAddr: r.RemoteAddr,
		path:       path,
		conn:       conn,
	})
	if err != nil {
		return
	}

	for {
		select {
		case header := <-conn.flvHeader:
			var flags uint8
			if header.hasAudio {
				flags |= flvio.FILE_HAS_AUDIO
			}
			if header.hasVideo {
				flags |= flvio.FILE_HAS_VIDEO
			}
			flvio.FillFileHeader(buff, flags)
			err := websocket.Message.Send(c, buff[:flvio.FileHeaderLength])
			if err != nil {
				return
			}
		case tag := <-conn.flvTags:
			data := tag.Data

			n := tag.FillHeader(buff[flvio.TagHeaderLength:])
			datalen := len(data) + n

			flvio.FillTagHeader(buff, tag, datalen)
			n += flvio.TagHeaderLength

			err := websocket.Message.Send(c, buff[:n])
			if err != nil {
				return
			}

			err = websocket.Message.Send(c, data)
			if err != nil {
				return
			}

			flvio.FillTagTrailer(buff, datalen)
			err = websocket.Message.Send(c, buff[:flvio.TagTrailerLength])
			if err != nil {
				return
			}
		}
	}
}
