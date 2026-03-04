package playback

import (
	"os"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v4/pkg/description"
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
	desc            *description.Session
	tracks          []*muxerStreamTrack
	muxer           *muxerStream
	server          *Server
	startMutex      sync.Mutex
	started         bool
	publishing      bool
	publishingInit  bool
}

// Close implements defs.Publisher.
func (ps *playbackSession) Close() {
	ps.startMutex.Lock()
	ps.started = false
	ps.startMutex.Unlock()

	// Close the done channel to signal playback stop
	if ps.muxer != nil {
		ps.muxer.Close()
	}

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

func (ps *playbackSession) StartPlayback(s *stream.Stream, tracks []*muxerStreamTrack) error {
	ps.startMutex.Lock()
	defer ps.startMutex.Unlock()

	if ps.started {
		return nil
	}
	if s == nil || len(tracks) == 0 {
		return os.ErrInvalid
	}

	ps.stream = s
	ps.tracks = tracks
	ps.started = true
	ps.status = "playing"

	go ps.playback()
	return nil
}

func (ps *playbackSession) IsStarted() bool {
	ps.startMutex.Lock()
	defer ps.startMutex.Unlock()
	return ps.started
}

func (ps *playbackSession) PrepareDescription(desc *description.Session, tracks []*muxerStreamTrack) {
	ps.startMutex.Lock()
	defer ps.startMutex.Unlock()
	ps.desc = desc
	ps.tracks = tracks
}

func (ps *playbackSession) EnsurePublisherStarted() error {
	ps.startMutex.Lock()
	if ps.publishing || ps.publishingInit {
		ps.startMutex.Unlock()
		return nil
	}
	if ps.path == nil || ps.desc == nil {
		ps.startMutex.Unlock()
		return os.ErrInvalid
	}
	ps.publishingInit = true
	ps.startMutex.Unlock()

	stream, err := ps.path.StartPublisher(defs.PathStartPublisherReq{
		Author:             ps,
		Desc:               ps.desc,
		GenerateRTPPackets: true,
	})
	if err != nil {
		ps.startMutex.Lock()
		ps.publishingInit = false
		ps.startMutex.Unlock()
		return err
	}

	ps.startMutex.Lock()
	ps.stream = stream
	ps.publishing = true
	ps.publishingInit = false
	ps.startMutex.Unlock()

	return nil
}

func (ps *playbackSession) SeekPosition(pos time.Duration) {
	ps.currentPosition = pos
}

func (ps *playbackSession) PlaybackSpeed(speed float64) {
	ps.playbackSpeed = speed
	if ps.muxer != nil {
		ps.muxer.playbackSpeed = speed
	}
}

// Pause pauses playback.
func (ps *playbackSession) Pause() {
	ps.status = "paused"
	if ps.muxer != nil {
		ps.muxer.Pause()
	}
	ps.Log(logger.Info, "playback paused")
}

// Resume resumes playback.
func (ps *playbackSession) Resume() {
	ps.status = "playing"
	if ps.muxer != nil {
		ps.muxer.Resume()
	}
	ps.Log(logger.Info, "playback resumed")
}

// IsPaused returns whether playback is paused.
func (ps *playbackSession) IsPaused() bool {
	return ps.status == "paused" || (ps.muxer != nil && ps.muxer.IsPaused())
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
	// Calculate total playback duration
	totalDuration := ps.endTime.Sub(ps.startTime)

	// Find the segment containing the current playback position
	// and calculate the offset within that segment
	var currentSegmentStart time.Time
	var segmentOffset time.Duration
	var foundStartSegment bool

	for i, segment := range segments {
		// Calculate segment end time (next segment start or session end time)
		var segmentEnd time.Time
		if i < len(segments)-1 {
			segmentEnd = segments[i+1].Start
		} else {
			// Last segment, use session end time
			segmentEnd = ps.endTime
		}

		// Check if current playback position is within this segment
		if ps.currentPosition >= (segment.Start.Sub(ps.startTime)) &&
			ps.currentPosition < (segmentEnd.Sub(ps.startTime)) {
			// Found the segment containing the current playback position
			currentSegmentStart = segment.Start
			segmentOffset = ps.currentPosition - (segment.Start.Sub(ps.startTime))
			foundStartSegment = true
			ps.Log(logger.Info, "found start segment %s, offset %v", segment.Fpath, segmentOffset)
			break
		}
	}

	// If no start segment found, start from the first segment
	if !foundStartSegment && len(segments) > 0 {
		currentSegmentStart = segments[0].Start
		segmentOffset = ps.currentPosition
		ps.Log(logger.Info, "no start segment found, using first segment %s, offset %v", segments[0].Fpath, segmentOffset)
	}

	// Process segments from the start segment
	var accumulatedDuration time.Duration
	var firstSegmentProcessed bool

	for i, segment := range segments {
		// Skip segments before the start segment
		if segment.Start.Before(currentSegmentStart) {
			ps.Log(logger.Info, "skipping segment %s (before start position)", segment.Fpath)
			continue
		}

		// Calculate segment end time
		var segmentEnd time.Time
		if i < len(segments)-1 {
			segmentEnd = segments[i+1].Start
		} else {
			segmentEnd = ps.endTime
		}

		// Calculate segment duration
		segmentDuration := segmentEnd.Sub(segment.Start)

		// Calculate duration to play from this segment
		var playDuration time.Duration
		if !firstSegmentProcessed {
			// First segment: play from offset to end of segment or session end
			playDuration = segmentDuration - segmentOffset
			if accumulatedDuration+playDuration > totalDuration {
				playDuration = totalDuration - accumulatedDuration
			}
		} else {
			// Subsequent segments: play entire segment or remaining duration
			if accumulatedDuration+segmentDuration > totalDuration {
				playDuration = totalDuration - accumulatedDuration
			} else {
				playDuration = segmentDuration
			}
		}

		// Check if we've played enough
		if accumulatedDuration >= totalDuration {
			ps.Log(logger.Info, "reached total playback duration, stopping")
			break
		}

		// Open segment file
		file, err := os.Open(segment.Fpath)
		if err != nil {
			ps.Log(logger.Error, "open segment file failed %v", err)
			continue
		}

		segmentStartOffset := time.Duration(0)
		if !firstSegmentProcessed {
			segmentStartOffset = segmentOffset
		}

		_, err = segmentFMP4SeekAndMuxParts(
			file,
			segmentStartOffset,
			playDuration,
			init,
			muxer,
		)
		ps.Log(logger.Info, "processed segment %s, offset %v, segmentStartOffset %v duration %v, err %v",
			segment.Fpath, segmentOffset, segmentStartOffset, playDuration, err)

		file.Close()

		if err != nil {
			// If playback was stopped, exit immediately
			if err.Error() == "playback stopped" {
				return
			}
			ps.Log(logger.Error, "process segment failed %v, continuing", err)
			continue
		}

		// Update tracking variables
		accumulatedDuration += playDuration
		firstSegmentProcessed = true
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
	if ps.muxer == nil {
		ps.muxer = &muxerStream{
			parent:        ps.server,
			stream:        ps.stream,
			tracks:        ps.tracks,
			playbackSpeed: ps.playbackSpeed,
			done:          make(chan struct{}),
		}
	}

	ps.muxer.resetTimeBase(ps.currentPosition > 0)

	// Process segments and write samples
	ps.processSegments(segments, init, ps.muxer)

	// Mark session as completed
	ps.status = "completed"

	ps.Log(logger.Error, "playback completed %v", ps)

}
