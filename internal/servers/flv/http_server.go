package flv

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/protocols/flv"
	"github.com/gorilla/websocket"
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
	httpSrv        *http.Server
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

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleConn)

	httpSrv := &http.Server{
		Addr:         s.address,
		Handler:      mux,
		ReadTimeout:  time.Duration(s.readTimeout),
		WriteTimeout: 0,
	}

	go func() {
		var err error
		if s.encryption {
			err = httpSrv.ListenAndServeTLS(s.serverCert, s.serverKey)
		} else {
			err = httpSrv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			s.Log(logger.Error, "server error: %v", err)
		}
	}()

	return nil
}

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:   1024,
	WriteBufferSize:  1024,
	HandshakeTimeout: 10 * time.Second,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// Log implements logger.Writer.
func (s *httpServer) Log(level logger.Level, format string, args ...interface{}) {
	s.parent.Log(level, format, args...)
}

func (s *httpServer) close() {
	if s.httpSrv != nil {
		s.httpSrv.Close()
	}
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

	if strings.ToLower(r.Header.Get("Upgrade")) == "websocket" {
		s.handleWebSocketFLV(w, r, path)
	} else {
		s.handleHTTPFLV(w, r, path)
	}
}

func (s *httpServer) handleHTTPFLV(w http.ResponseWriter, r *http.Request, path string) {
	flvConn := flv.NewConn()
	muxer, err := s.parent.newMuxer(newMuxerReq{
		remoteAddr: r.RemoteAddr,
		path:       path,
		query:      r.URL.RawQuery,
		flvConn:    flvConn,
	})
	if err != nil {
		http.Error(w, "invalid path", http.StatusNotFound)
		return
	}
	defer muxer.Close()

	w.Header().Set("Content-Type", "video/x-flv")
	flusher := w.(http.Flusher)

	for {
		select {
		case header := <-flvConn.FlvHeader:
			data := header.Marshal()
			if _, err := w.Write(data); err != nil {
				s.Log(logger.Error, "write http flv header failed %v", err)
				return
			}

			data = flv.MarshalTagSize(0)
			if _, err := w.Write(data); err != nil {
				s.Log(logger.Error, "write http flv tag size failed %v", err)
				return
			}
			flusher.Flush()

		case tag := <-flvConn.FlvTags:
			data := tag.Marshal()
			if _, err := w.Write(data); err != nil {
				s.Log(logger.Error, "write http flv tag failed %v", err)
				return
			}

			data = flv.MarshalTagSize(len(data))
			if _, err := w.Write(data); err != nil {
				s.Log(logger.Error, "write http flv tag size failed %v", err)
				return
			}
			flusher.Flush()

		case <-muxer.Context().Done():
			s.Log(logger.Info, "flv conn closed")
			return

		case <-r.Context().Done():
			s.Log(logger.Info, "http flv client disconnected")
			return
		}
	}
}

func (s *httpServer) handleWebSocketFLV(w http.ResponseWriter, r *http.Request, path string) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		s.Log(logger.Error, "websocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()
	s.Log(logger.Info, "handle websocket flv path:%s", path)

	flvConn := flv.NewConn()
	muxer, err := s.parent.newMuxer(newMuxerReq{
		remoteAddr: r.RemoteAddr,
		path:       path,
		query:      r.URL.RawQuery,
		flvConn:    flvConn,
	})
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("invalid path"))
		return
	}
	defer muxer.Close()

	const (
		pongWait   = 30 * time.Second // must receive pong in this time
		pingPeriod = 10 * time.Second // send ping every X sec
	)

	// 初始 ReadDeadline
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		// 收到 pong → 延长 ReadDeadline
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	// goroutine：必须读消息，否则 WS 会卡死
	go func() {
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				s.Log(logger.Error, "ws read message failed %s", err.Error())
				return
			}

			messageType, p, err := conn.ReadMessage()
			if err != nil {
				// 检查是否是正常关闭
				if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
					s.Log(logger.Error, "client close connection %s", err.Error())
				} else {
					s.Log(logger.Error, "read message faield: %s", err.Error())
				}
				return
			}

			switch messageType {
			case websocket.CloseMessage:
				s.Log(logger.Info, "receive client close message")
				return

			case websocket.PingMessage:
				s.Log(logger.Info, "receive client ping message")
				conn.WriteMessage(websocket.PongMessage, p)

			case websocket.TextMessage:
				s.Log(logger.Info, "receive client text message")

			case websocket.BinaryMessage:
				s.Log(logger.Info, "receive client binary message")
			}
		}
	}()

	// goroutine：定期发送 ping
	pingTicker := time.NewTicker(pingPeriod)
	defer pingTicker.Stop()

	for {
		select {
		case <-pingTicker.C:
			// 发送 ping，WriteControl 会自动带 timeout
			if err := conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(10*time.Second)); err != nil {
				s.Log(logger.Info, "ws send ping failed: %v", err)
				return
			}

		case header := <-flvConn.FlvHeader:
			data := header.Marshal()
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
				s.Log(logger.Info, "ws flv header send failed: %v", err)
				return
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, flv.MarshalTagSize(0)); err != nil {
				s.Log(logger.Info, "ws pre-tag send failed: %v", err)
				return
			}

		case tag := <-flvConn.FlvTags:
			data := tag.Marshal()
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
				s.Log(logger.Info, "ws tag send failed: %v", err)
				return
			}

			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.BinaryMessage, flv.MarshalTagSize(len(data))); err != nil {
				s.Log(logger.Info, "ws pre-tag send failed: %v", err)
				return
			}

		case <-muxer.Context().Done():
			s.Log(logger.Info, "ws flv muxer closed")
			return
		}
	}
}
