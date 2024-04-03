package flv

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/protocols/httpp"
	"github.com/bluenviron/mediamtx/internal/restrictnetwork"
	"github.com/notedit/rtmp/format/flv/flvio"
)

type httpServer struct {
	address        string
	encryption     bool
	serverKey      string
	serverCert     string
	allowOrigin    string
	trustedProxies conf.IPNetworks
	readTimeout    conf.StringDuration
	parent         *Server
	inner          *httpp.WrappedServer
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

	network, address := restrictnetwork.Restrict("tcp", s.address)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.handleConn(w, r)
	})

	var err error
	s.inner, err = httpp.NewWrappedServer(
		network,
		address,
		time.Duration(s.readTimeout),
		s.serverCert,
		s.serverKey,
		mux,
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

func (s *httpServer) handleConn(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", s.allowOrigin)
	w.Header().Set("Access-Control-Allow-Credentials", "true")

	// remove leading prefix
	pa := r.URL.Path

	var path string

	switch {
	case pa == "", pa == "favicon.ico":
		return

	case strings.HasSuffix(pa, ".flv"):
		path = strings.TrimSuffix(strings.TrimLeft(pa, "/"), ".flv")

	default:
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	buff := make([]byte, 256)
	conn := newConn()

	err := s.parent.newMuxer(newMuxerReq{
		remoteAddr: r.RemoteAddr,
		path:       path,
		conn:       conn,
	})
	if err != nil {
		http.Error(w, "invalid path", http.StatusNotFound)

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
			_, err := w.Write(buff[:flvio.FileHeaderLength])
			if err != nil {
				conn.close()
				return
			}
		case tag := <-conn.flvTags:
			err := flvio.WriteTag(w, tag, buff)
			if err != nil {
				conn.close()
				return
			}
		}
	}
}
