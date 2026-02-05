package playback

import (
	"os"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4"

	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/recordstore"
	"github.com/bluenviron/mediamtx/internal/stream"
)

// playbackSession represents a playback session.
type playbackSession struct {
	id              string
	sourcePath      string
	playbackPath    string
	startTime       time.Time
	endTime         time.Time
	status          string
	currentPosition time.Duration
	playbackSpeed   float64
	path            defs.Path
	stream          *stream.Stream
	done            chan struct{}
	server          *Server
}

// Close implements defs.Publisher.
func (ps *playbackSession) Close() {
	close(ps.done)
}

// Log implements logger.Writer.
func (ps *playbackSession) Log(level logger.Level, format string, args ...interface{}) {
	ps.server.Log(level, "[session "+ps.id+"] "+format, args...)
}

// APISourceDescribe implements Source.
func (ps *playbackSession) APISourceDescribe() defs.APIPathSourceOrReader {
	return defs.APIPathSourceOrReader{
		Type: "playback",
		ID:   ps.id,
	}
}

func (ps *playbackSession) StartReadFile(s *stream.Stream) {
	ps.stream = s
	go ps.playback()
}

// readInitFromSegment reads the init data from a recording segment.
func readInitFromSegment(segment *recordstore.Segment) (*fmp4.Init, error) {
	// Open segment file
	file, err := os.Open(segment.Fpath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Read init data
	init, _, err := segmentFMP4ReadHeader(file)
	if err != nil {
		return nil, err
	}

	return init, nil
}

// processSegments processes recording segments and writes samples to the muxer.
func (ps *playbackSession) processSegments(segments []*recordstore.Segment, init *fmp4.Init, muxer muxer) {
	for _, segment := range segments {
		// Open segment file
		file, err := os.Open(segment.Fpath)
		if err != nil {
			ps.Log(logger.Error, "open segment file failed %v", err)
			continue
		}

		// Process segment
		_, err = segmentFMP4SeekAndMuxParts(
			file,
			ps.currentPosition,
			ps.endTime.Sub(ps.startTime),
			init,
			muxer,
		)
		ps.Log(logger.Info, "segment seek and mux parts %v", err)

		file.Close()

		if err != nil {
			continue
		}
	}
}

// playback starts the playback of recording segments.
func (ps *playbackSession) playback() {
	// Find path configuration
	server := ps.server
	pathConf, _, err := server.safeFindPathConf(ps.sourcePath)
	if err != nil {
		ps.Log(logger.Error, "find path conf failed %v", err)
		ps.status = "error"
		return
	}

	// Find recording segments
	segments, err := recordstore.FindSegments(pathConf, ps.sourcePath, &ps.startTime, &ps.endTime)
	if err != nil {
		ps.Log(logger.Error, "find segments failed %v", err)
		ps.status = "error"
		return
	}

	if len(segments) == 0 {
		ps.Log(logger.Info, "no record segments found")
		ps.status = "completed"
		return
	}

	// Read init data from first segment
	init, err := readInitFromSegment(segments[0])
	if err != nil {
		ps.Log(logger.Error, "read init from segment failed %v", err)
		ps.status = "error"
		return
	}

	// Create muxer to write samples to stream
	muxer := &muxerStream{
		Parent:        ps.server,
		stream:        ps.stream,
		playbackSpeed: ps.playbackSpeed,
	}
	muxer.writeInit(init)

	// Process segments and write samples
	ps.processSegments(segments, init, muxer)

	// Mark session as completed
	ps.status = "completed"

	ps.Log(logger.Error, "playback completed %v", ps)

}
