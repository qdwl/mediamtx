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
	sourcePath      string
	playbackPath    string
	startTime       time.Time
	endTime         time.Time
	status          string
	currentPosition time.Duration
	playbackSpeed   float64
	path            defs.Path
	stream          *stream.Stream
	tracks          []*muxerStreamTrack
	done            chan struct{}
	server          *Server
}

// Close implements defs.Publisher.
func (ps *playbackSession) Close() {
	// Close the done channel to signal playback stop
	close(ps.done)

	// Update session status
	ps.status = "stopped"

	ps.Log(logger.Info, "playback session stopped")
}

// Log implements logger.Writer.
func (ps *playbackSession) Log(level logger.Level, format string, args ...interface{}) {
	ps.server.Log(level, "[session "+ps.playbackPath+"] "+format, args...)
}

// APISourceDescribe implements Source.
func (ps *playbackSession) APISourceDescribe() defs.APIPathSourceOrReader {
	return defs.APIPathSourceOrReader{
		Type: "playback",
		ID:   ps.playbackPath,
	}
}

func (ps *playbackSession) StartPlayback(s *stream.Stream, tracks []*muxerStreamTrack) {
	ps.stream = s
	ps.tracks = tracks
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
			// If playback was stopped, exit immediately
			if err.Error() == "playback stopped" {
				return
			}
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
		parent:        ps.server,
		stream:        ps.stream,
		tracks:        ps.tracks,
		playbackSpeed: ps.playbackSpeed,
		done:          ps.done,
	}

	// Process segments and write samples
	ps.processSegments(segments, init, muxer)

	// Mark session as completed
	ps.status = "completed"

	ps.Log(logger.Error, "playback completed %v", ps)

}
