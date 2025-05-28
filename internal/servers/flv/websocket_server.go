package flv

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/protocols/flv"

	"golang.org/x/net/websocket"
)

type websocketServer struct {
	address        string
	encryption     bool
	serverKey      string
	serverCert     string
	allowOrigin    string
	trustedProxies conf.IPNetworks
	readTimeout    conf.Duration
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

	flvConn := flv.NewConn()
	muxer, err := s.parent.newMuxer(newMuxerReq{
		remoteAddr: r.RemoteAddr,
		path:       path,
		flvConn:    flvConn,
	})
	if err != nil {
		return
	}
	defer muxer.Close()

	s.Log(logger.Info, "websocket-flv pull")

	// c.SetReadDeadline(time.Now().Add(60 * time.Second))
	// c.SetWriteDeadline(time.Now().Add(60 * time.Second))

	readerErr := make(chan error)
	go func() {
		for {
			var msg string
			err := websocket.Message.Receive(c, &msg)
			if err != nil {
				readerErr <- errors.New("terminated")
				s.Log(logger.Info, "websocket connection read error %v", err)
				return
			}
		}
	}()

	for {
		select {
		case header := <-flvConn.FlvHeader:
			data := header.Marshal()
			if err := websocket.Message.Send(c, data); err != nil {
				s.Log(logger.Error, "websocket conn send failed %v", err)
				return
			}

			data = flv.MarshalTagSize(0)
			if err := websocket.Message.Send(c, data); err != nil {
				s.Log(logger.Error, "websocket conn send failed %v", err)
				return
			}
		case tag := <-flvConn.FlvTags:
			data := tag.Marshal()
			if err := websocket.Message.Send(c, data); err != nil {
				s.Log(logger.Error, "websocket conn send failed %v", err)
				return
			}

			data = flv.MarshalTagSize(len(data))
			if err := websocket.Message.Send(c, data); err != nil {
				s.Log(logger.Error, "websocket conn send failed %v", err)
				return
			}
		case <-flvConn.Done:
			s.Log(logger.Info, "flv conn closed")
			return

		case <-r.Context().Done():
			s.Log(logger.Info, "websocket conn done")
			return

		case err := <-readerErr:
			s.Log(logger.Info, "websocket conn error %v", err)
			return
		}
	}
}
