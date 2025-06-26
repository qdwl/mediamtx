package flv

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/protocols/flv"
	"github.com/bluenviron/mediamtx/internal/stream"
)

var errNoSupportedCodecs = errors.New(
	"the stream doesn't contain any supported codec, which are currently H264, MPEG-4 Audio, MPEG-1/2 Audio")

type muxer struct {
	parentCtx   context.Context
	remoteAddr  string
	wg          *sync.WaitGroup
	pathName    string
	pathManager serverPathManager
	parent      *Server
	flvConn     *flv.Conn
	transcoder  flv.AudioTranscoder

	ctx       context.Context
	ctxCancel func()
	path      defs.Path
}

func (m *muxer) initialize() {
	ctx, ctxCancel := context.WithCancel(m.parentCtx)

	m.ctx = ctx
	m.ctxCancel = ctxCancel

	m.Log(logger.Info, "opened")

	m.wg.Add(1)
	go m.run()
}

func (m *muxer) Context() context.Context {
	return m.ctx
}

func (m *muxer) Close() {
	m.Log(logger.Info, "close muxer")
	m.flvConn.Close()
	m.ctxCancel()
}

// Log implements logger.Writer.
func (m *muxer) Log(level logger.Level, format string, args ...interface{}) {
	m.parent.Log(level, "[muxer %s] "+format, append([]interface{}{m.pathName}, args...)...)
}

// PathName returns the path name.
func (m *muxer) PathName() string {
	return m.pathName
}

func (m *muxer) run() {
	defer m.wg.Done()

	err := m.runInner()
	if err != nil {
		m.flvConn.Close()
		m.Log(logger.Info, "flv conn close")
	}

	m.Log(logger.Info, "run exit")

	m.ctxCancel()

	m.parent.closeMuxer(m)

	m.Log(logger.Info, "closed: %v", err)
}

func (m *muxer) runInner() error {
	var path defs.Path
	var stream *stream.Stream
	var err error
	var count int = 0
	for {
		<-time.After(100 * time.Millisecond)
		path, stream, err = m.pathManager.AddReader(defs.PathAddReaderReq{
			Author: m,
			AccessRequest: defs.PathAccessRequest{
				Name:     m.pathName,
				SkipAuth: true,
			},
		})
		count++
		if err == nil || count > 100 {
			break
		} else {
			m.Log(logger.Debug, "find stream failed, %v", err)
			continue
		}
	}
	if err != nil {
		return err
	}

	m.path = path

	defer m.path.RemoveReader(defs.PathRemoveReaderReq{Author: m})

	err = flv.FromStream(stream, m, m.flvConn, &m.transcoder)
	if err != nil {
		return err
	}

	m.Log(logger.Info, "is reading from path '%s', %s",
		path.Name(), defs.FormatsInfo(stream.ReaderFormats(m)))

	stream.StartReader(m)
	defer m.transcoder.Close()
	defer stream.RemoveReader(m)

	select {
	case <-m.ctx.Done():
		m.Log(logger.Info, "terminated")
		return fmt.Errorf("terminated")

	case err := <-stream.ReaderError(m):
		m.Log(logger.Error, "read stream error %v", err)
		return err
	}
}

// APIReaderDescribe implements reader.
func (m *muxer) APIReaderDescribe() defs.APIPathSourceOrReader {
	return defs.APIPathSourceOrReader{
		Type: "flvMuxer",
		ID:   "",
	}
}
