// Package playback contains the playback server.
package playback

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/pmp4"

	"github.com/bluenviron/mediamtx/internal/auth"
	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/protocols/httpp"
	"github.com/bluenviron/mediamtx/internal/recordstore"
	"github.com/bluenviron/mediamtx/internal/restrictnetwork"
	"github.com/bluenviron/mediamtx/internal/stream"
	"github.com/gin-gonic/gin"
)

type serverAuthManager interface {
	Authenticate(req *auth.Request) error
}

type serverPathManager interface {
	AddPublisher(req defs.PathAddPublisherReq) (defs.Path, error)
	AddReader(req defs.PathAddReaderReq) (defs.Path, *stream.Stream, error)
}

// Server is the playback server.
type Server struct {
	Address        string
	Encryption     bool
	ServerKey      string
	ServerCert     string
	AllowOrigin    string
	TrustedProxies conf.IPNetworks
	ReadTimeout    conf.Duration
	PathConfs      map[string]*conf.Path
	AuthManager    serverAuthManager
	PathManager    serverPathManager
	Parent         logger.Writer

	httpServer       *httpp.Server
	mutex            sync.RWMutex
	playbackSessions map[string]*playbackSession
	playbackMutex    sync.RWMutex
}

// Initialize initializes Server.
func (s *Server) Initialize() error {
	router := gin.New()
	router.SetTrustedProxies(s.TrustedProxies.ToTrustedProxies()) //nolint:errcheck

	router.Use(s.middlewareOrigin)

	router.GET("/list", s.onList)
	router.GET("/get", s.onGet)
	router.POST("/start", s.onStart)
	router.POST("/stop", s.onStop)
	router.POST("/control", s.onControl)

	network, address := restrictnetwork.Restrict("tcp", s.Address)

	s.httpServer = &httpp.Server{
		Network:     network,
		Address:     address,
		ReadTimeout: time.Duration(s.ReadTimeout),
		Encryption:  s.Encryption,
		ServerCert:  s.ServerCert,
		ServerKey:   s.ServerKey,
		Handler:     router,
		Parent:      s,
	}
	err := s.httpServer.Initialize()
	if err != nil {
		return err
	}

	s.playbackSessions = make(map[string]*playbackSession)

	s.Log(logger.Info, "listener opened on "+address)

	return nil
}

// Close closes Server.
func (s *Server) Close() {
	s.Log(logger.Info, "listener is closing")
	s.httpServer.Close()
}

// Log implements logger.Writer.
func (s *Server) Log(level logger.Level, format string, args ...interface{}) {
	s.Parent.Log(level, "[playback] "+format, args...)
}

// ReloadPathConfs is called by core.Core.
func (s *Server) ReloadPathConfs(pathConfs map[string]*conf.Path) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.PathConfs = pathConfs
}

func (s *Server) writeError(ctx *gin.Context, status int, err error) {
	// show error in logs
	s.Log(logger.Error, err.Error())

	// add error to response
	ctx.String(status, err.Error())
}

func (s *Server) safeFindPathConf(name string) (*conf.Path, []string, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return conf.FindPathConf(s.PathConfs, name)
}

func (s *Server) middlewareOrigin(ctx *gin.Context) {
	ctx.Header("Access-Control-Allow-Origin", s.AllowOrigin)
	ctx.Header("Access-Control-Allow-Credentials", "true")

	// preflight requests
	if ctx.Request.Method == http.MethodOptions &&
		ctx.Request.Header.Get("Access-Control-Request-Method") != "" {
		ctx.Header("Access-Control-Allow-Methods", "OPTIONS, GET")
		ctx.Header("Access-Control-Allow-Headers", "Authorization")
		ctx.AbortWithStatus(http.StatusNoContent)
		return
	}
}

func (s *Server) doAuth(ctx *gin.Context, pathName string) bool {
	req := &auth.Request{
		Action:      conf.AuthActionPlayback,
		Path:        pathName,
		Query:       ctx.Request.URL.RawQuery,
		Credentials: httpp.Credentials(ctx.Request),
		IP:          net.ParseIP(ctx.ClientIP()),
	}

	err := s.AuthManager.Authenticate(req)
	if err != nil {
		if err.(auth.Error).AskCredentials { //nolint:errorlint
			ctx.Header("WWW-Authenticate", `Basic realm="mediamtx"`)
			ctx.Writer.WriteHeader(http.StatusUnauthorized)
			return false
		}

		s.Log(logger.Info, "connection %v failed to authenticate: %v",
			httpp.RemoteAddr(ctx), err.(*auth.Error).Message) //nolint:errorlint

		// wait some seconds to mitigate brute force attacks
		<-time.After(auth.PauseAfterError)

		ctx.Writer.WriteHeader(http.StatusUnauthorized)
		return false
	}

	return true
}

// onStart handles the start playback request.
func (s *Server) onStart(ctx *gin.Context) {
	// Parse parameters
	sourcePath := ctx.PostForm("sourcePath")
	playbackPath := ctx.PostForm("playbackPath")
	startTimeStr := ctx.PostForm("startTime")
	endTimeStr := ctx.PostForm("endTime")

	if sourcePath == "" || playbackPath == "" || startTimeStr == "" || endTimeStr == "" {
		s.writeError(ctx, http.StatusBadRequest, fmt.Errorf("missing required parameters"))
		return
	}

	// Authenticate
	if !s.doAuth(ctx, sourcePath) {
		return
	}

	// Parse time parameters
	startTime, err := time.Parse(time.RFC3339, startTimeStr)
	if err != nil {
		s.writeError(ctx, http.StatusBadRequest, fmt.Errorf("invalid startTime: %w", err))
		return
	}

	endTime, err := time.Parse(time.RFC3339, endTimeStr)
	if err != nil {
		s.writeError(ctx, http.StatusBadRequest, fmt.Errorf("invalid endTime: %w", err))
		return
	}

	if endTime.Before(startTime) {
		s.writeError(ctx, http.StatusBadRequest, fmt.Errorf("endTime must be after startTime"))
		return
	}

	// Find path configuration
	_, _, err = s.safeFindPathConf(sourcePath)
	if err != nil {
		s.writeError(ctx, http.StatusBadRequest, err)
		return
	}

	// Create playback session
	session := &playbackSession{
		sourcePath:      sourcePath,
		playbackPath:    playbackPath,
		startTime:       startTime,
		endTime:         endTime,
		status:          "starting",
		currentPosition: 0,
		playbackSpeed:   1.0,
		server:          s,
	}

	// Add session to map
	s.playbackMutex.Lock()
	s.playbackSessions[playbackPath] = session
	s.playbackMutex.Unlock()

	// Create access request
	accessReq := defs.PathAccessRequest{
		Name:    playbackPath,
		Query:   "",
		Publish: true,
		Proto:   auth.ProtocolRTSP, // Use RTSP as fallback since there's no HTTP protocol defined
		IP:      net.ParseIP(ctx.ClientIP()),
	}

	// Add publisher
	path, err := s.PathManager.AddPublisher(defs.PathAddPublisherReq{
		Author:        session,
		AccessRequest: accessReq,
	})
	if err != nil {
		s.playbackMutex.Lock()
		delete(s.playbackSessions, playbackPath)
		s.playbackMutex.Unlock()
		s.writeError(ctx, http.StatusInternalServerError, fmt.Errorf("failed to add publisher: %w", err))
		return
	}

	session.path = path
	session.status = "playing"

	// Start publisher to begin playback
	// Generate stream description from recording's init data
	var desc *description.Session

	// Find path configuration
	pathConf, _, err := s.safeFindPathConf(session.sourcePath)
	if err != nil {
		s.playbackMutex.Lock()
		delete(s.playbackSessions, playbackPath)
		s.playbackMutex.Unlock()
		s.writeError(ctx, http.StatusInternalServerError, fmt.Errorf("failed to find path configuration: %w", err))
		return
	}

	// Find recording segments
	segments, err := recordstore.FindSegments(pathConf, session.sourcePath, &session.startTime, &session.endTime)
	if err != nil {
		s.playbackMutex.Lock()
		delete(s.playbackSessions, playbackPath)
		s.playbackMutex.Unlock()
		s.writeError(ctx, http.StatusInternalServerError, fmt.Errorf("failed to find recording segments: %w", err))
		return
	}

	if len(segments) == 0 {
		s.playbackMutex.Lock()
		delete(s.playbackSessions, playbackPath)
		s.playbackMutex.Unlock()
		s.writeError(ctx, http.StatusInternalServerError, fmt.Errorf("no recording segments found"))
		return
	}

	// Read init data from first segment
	file, err := os.Open(segments[0].Fpath)
	if err != nil {
		s.playbackMutex.Lock()
		delete(s.playbackSessions, playbackPath)
		s.playbackMutex.Unlock()
		s.writeError(ctx, http.StatusInternalServerError, fmt.Errorf("failed to open segment file: %w", err))
		return
	}
	init, _, err := segmentFMP4ReadHeader(file)
	file.Close()
	if err != nil {
		s.playbackMutex.Lock()
		delete(s.playbackSessions, playbackPath)
		s.playbackMutex.Unlock()
		s.writeError(ctx, http.StatusInternalServerError, fmt.Errorf("failed to read init data: %w", err))
		return
	}

	// 目前仅支持回放视频
	var tracks []*muxerStreamTrack
	// Create stream description from init data
	desc = &description.Session{}
	for _, track := range init.Tracks {
		media := &description.Media{}
		switch codec := track.Codec.(type) {
		case *fmp4.CodecH264:
			media.Type = description.MediaTypeVideo
			media.Formats = []format.Format{
				&format.H264{
					PayloadTyp:        96,
					PacketizationMode: 1,
					SPS:               codec.SPS,
					PPS:               codec.PPS,
				},
			}
			track := &muxerStreamTrack{
				Track: pmp4.Track{
					ID:        track.ID,
					TimeScale: track.TimeScale,
					Codec:     track.Codec,
				},
				media: media,
			}
			tracks = append(tracks, track)

		case *fmp4.CodecH265:
			media.Type = description.MediaTypeVideo
			media.Formats = []format.Format{
				&format.H265{
					PayloadTyp: 96,
					VPS:        codec.VPS,
					SPS:        codec.SPS,
					PPS:        codec.PPS,
				},
			}
			track := &muxerStreamTrack{
				Track: pmp4.Track{
					ID:        track.ID,
					TimeScale: track.TimeScale,
					Codec:     track.Codec,
				},
				media: media,
			}
			tracks = append(tracks, track)

		case *fmp4.CodecMPEG4Audio:
			media.Type = description.MediaTypeAudio
			media.Formats = []format.Format{
				&format.MPEG4Audio{
					PayloadTyp:       96,
					SizeLength:       13,
					IndexLength:      3,
					IndexDeltaLength: 3,
					Config:           &codec.Config,
				},
			}
			track := &muxerStreamTrack{
				Track: pmp4.Track{
					ID:        track.ID,
					TimeScale: track.TimeScale,
					Codec:     track.Codec,
				},
				media: media,
			}
			tracks = append(tracks, track)

		default:
		}
		desc.Medias = append(desc.Medias, media)
	}

	stream, err := path.StartPublisher(defs.PathStartPublisherReq{
		Author:             session,
		Desc:               desc,
		GenerateRTPPackets: true,
	})
	if err != nil {
		s.playbackMutex.Lock()
		delete(s.playbackSessions, playbackPath)
		s.playbackMutex.Unlock()
		s.writeError(ctx, http.StatusInternalServerError, fmt.Errorf("failed to start publisher: %w", err))
		return
	}
	session.StartPlayback(stream, tracks)

	// Return session information
	ctx.JSON(http.StatusOK, gin.H{
		"sourcePath":      sourcePath,
		"playbackPath":    playbackPath,
		"startTime":       startTime,
		"endTime":         endTime,
		"status":          session.status,
		"currentPosition": 0,
		"playbackSpeed":   1.0,
	})
}

// onStop handles the stop playback request.
func (s *Server) onStop(ctx *gin.Context) {
	// Parse playbackPath ID
	playbackPath := ctx.PostForm("playbackPath")
	if playbackPath == "" {
		s.writeError(ctx, http.StatusBadRequest, fmt.Errorf("missing playbackPath"))
		return
	}

	// Find session
	s.playbackMutex.Lock()
	session, ok := s.playbackSessions[playbackPath]
	if !ok {
		s.playbackMutex.Unlock()
		s.writeError(ctx, http.StatusNotFound, fmt.Errorf("session not found"))
		return
	}

	// Remove session from map
	delete(s.playbackSessions, playbackPath)
	s.playbackMutex.Unlock()

	// Stop playback
	session.Close()

	// Remove publisher
	if session.path != nil {
		session.path.RemovePublisher(defs.PathRemovePublisherReq{Author: session})
	}

	// Return success
	ctx.JSON(http.StatusOK, gin.H{
		"success":      true,
		"playbackPath": playbackPath,
	})
}

// onControl handles the playback control request.
func (s *Server) onControl(ctx *gin.Context) {
	// Parse playbackPath ID
	playbackPath := ctx.PostForm("playbackPath")
	if playbackPath == "" {
		s.writeError(ctx, http.StatusBadRequest, fmt.Errorf("missing playbackPath"))
		return
	}

	// Find session
	s.playbackMutex.RLock()
	session, ok := s.playbackSessions[playbackPath]
	if !ok {
		s.playbackMutex.RUnlock()
		s.writeError(ctx, http.StatusNotFound, fmt.Errorf("session not found"))
		return
	}
	s.playbackMutex.RUnlock()

	// Parse control parameters
	seekPosStr := ctx.PostForm("seekPosition")
	speedStr := ctx.PostForm("playbackSpeed")
	playbackCmd := ctx.PostForm("command")

	// Handle playback command (pause/resume)
	if playbackCmd != "" {
		switch playbackCmd {
		case "pause":
			session.Pause()
		case "resume":
			session.Resume()
		case "play":
			if session.IsPaused() {
				session.Resume()
			}
			// Handle playback speed
			if speedStr != "" {
				speed, err := strconv.ParseFloat(speedStr, 64)
				if err != nil {
					s.writeError(ctx, http.StatusBadRequest, fmt.Errorf("invalid playbackSpeed: %w", err))
					return
				}
				if speed <= 0 {
					s.writeError(ctx, http.StatusBadRequest, fmt.Errorf("playbackSpeed must be positive"))
					return
				}
				session.PlaybackSpeed(speed)
				session.Log(logger.Info, "playback speed %f", session.playbackSpeed)
			}

			// Handle seek
			if seekPosStr != "" {
				seekPos, err := time.ParseDuration(seekPosStr)
				if err != nil {
					s.writeError(ctx, http.StatusBadRequest, fmt.Errorf("invalid seekPosition: %w", err))
					return
				}

				// Stop current playback
				session.Close()

				// Update seek position
				session.currentPosition = seekPos
				session.status = "seeking"

				// Restart playback from new position
				go func() {
					session.status = "playing"
					session.playback()
				}()
				session.Log(logger.Info, "seek to position %v", seekPos)
			}
		default:
			s.writeError(ctx, http.StatusBadRequest, fmt.Errorf("invalid command: %s", playbackCmd))
			return
		}
	}

	// Return current status
	ctx.JSON(http.StatusOK, gin.H{
		"status":          session.status,
		"playbackPath":    playbackPath,
		"currentPosition": session.currentPosition,
		"playbackSpeed":   session.playbackSpeed,
	})
}
