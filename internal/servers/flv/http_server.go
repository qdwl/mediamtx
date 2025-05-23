package flv

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/protocols/flv"
	"github.com/bluenviron/mediamtx/internal/protocols/httpp"
	"github.com/bluenviron/mediamtx/internal/restrictnetwork"
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

	network, address := restrictnetwork.Restrict("tcp", s.address)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.handleConn(w, r)
	})

	s.inner = &httpp.Server{
		Network:     network,
		Address:     address,
		ReadTimeout: time.Duration(s.readTimeout),
		Encryption:  s.encryption,
		ServerCert:  s.serverCert,
		ServerKey:   s.serverKey,
		Handler:     mux,
		Parent:      s,
	}
	err := s.inner.Initialize()
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

	flvConn := flv.NewConn()
	muxer, err := s.parent.newMuxer(newMuxerReq{
		remoteAddr: r.RemoteAddr,
		path:       path,
		flvConn:    flvConn,
	})
	if err != nil {
		http.Error(w, "invalid path", http.StatusNotFound)
		return
	}
	defer muxer.Close()

	for {
		select {
		case header := <-flvConn.FlvHeader:
			data := header.Marshal()
			if _, err := w.Write(data); err != nil {
				s.Log(logger.Error, "write flv header failed %v", err)
				return
			}

			data = flv.MarshalTagSize(0)
			if _, err := w.Write(data); err != nil {
				s.Log(logger.Error, "write flv tag size failed %v", err)
				return
			}

		case tag := <-flvConn.FlvTags:
			data := tag.Marshal()
			if _, err := w.Write(data); err != nil {
				s.Log(logger.Error, "write flv tag failed %v", err)
				return
			}

			data = flv.MarshalTagSize(len(data))
			if _, err := w.Write(data); err != nil {
				s.Log(logger.Error, "write flv tag size failed %v", err)
				return
			}

		case <-flvConn.Done:
			s.Log(logger.Info, "flv conn closed")
			return

		case <-r.Context().Done():
			s.Log(logger.Info, "http flv client disconnected")
			return
		}
	}
}
