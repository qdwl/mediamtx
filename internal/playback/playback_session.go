package playback

import (
	"os"
	"time"

	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
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

// StartPublisher implements defs.Publisher.
func (ps *playbackSession) StartPublisher(req defs.PathStartPublisherReq) (*stream.Stream, error) {
	// Use the provided session description
	str := &stream.Stream{
		Desc: req.Desc,
	}

	// Start playback in a goroutine
	go ps.playback()

	ps.stream = str
	return str, nil
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

// createStreamDescriptionFromInit creates a stream description from fmp4 init data.
func createStreamDescriptionFromInit(init *fmp4.Init) *description.Session {
	// Create session
	sess := &description.Session{}

	// Add tracks to session
	for _, track := range init.Tracks {
		// Create media for track
		media := &description.Media{}

		// Set media type and format based on codec type
		switch codec := track.Codec.(type) {
		case *fmp4.CodecH264:
			// Set video media type
			media.Type = description.MediaTypeVideo
			// Add H264 format
			media.Formats = []format.Format{
				&format.H264{
					PayloadTyp:        96,
					PacketizationMode: 1,
					SPS:               codec.SPS,
					PPS:               codec.PPS,
				},
			}
			sess.Medias = append(sess.Medias, media)

		case *fmp4.CodecH265:
			// Set video media type
			media.Type = description.MediaTypeVideo
			// Add H265 format
			media.Formats = []format.Format{
				&format.H265{
					PayloadTyp: 96,
					VPS:        codec.VPS,
					SPS:        codec.SPS,
					PPS:        codec.PPS,
				},
			}
			sess.Medias = append(sess.Medias, media)

		case *fmp4.CodecMPEG4Audio:
			// Set audio media type
			media.Type = description.MediaTypeAudio
			// Add AAC format
			media.Formats = []format.Format{
				&format.MPEG4Audio{
					PayloadTyp:       96,
					SizeLength:       13,
					IndexLength:      3,
					IndexDeltaLength: 3,
					Config:           &codec.Config,
				},
			}
			sess.Medias = append(sess.Medias, media)

		default:
		}

	}

	return sess
}

// processSegments processes recording segments and writes samples to the muxer.
func (ps *playbackSession) processSegments(segments []*recordstore.Segment, init *fmp4.Init, muxer muxer) {
	for _, segment := range segments {
		// Open segment file
		file, err := os.Open(segment.Fpath)
		if err != nil {
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
		ps.status = "error"
		return
	}

	// Find recording segments
	segments, err := recordstore.FindSegments(pathConf, ps.sourcePath, &ps.startTime, &ps.endTime)
	if err != nil {
		ps.status = "error"
		return
	}

	if len(segments) == 0 {
		ps.status = "completed"
		return
	}

	// Read init data from first segment
	init, err := readInitFromSegment(segments[0])
	if err != nil {
		ps.status = "error"
		return
	}

	// Create stream description from init data
	desc := createStreamDescriptionFromInit(init)

	// Update stream description
	if ps.stream != nil {
		ps.stream.Desc = desc
	}

	// Create muxer to write samples to stream
	muxer := &muxerStream{
		stream:        ps.stream,
		playbackSpeed: ps.playbackSpeed,
	}

	// Initialize tracks and set media/format for each track
	if len(desc.Medias) > 0 {
		for i, media := range desc.Medias {
			if len(media.Formats) > 0 {
				forma := media.Formats[0]
				muxer.setTrackMedia(i, media, forma)
			}
		}
	}

	// Process segments and write samples
	ps.processSegments(segments, init, muxer)

	// Mark session as completed
	ps.status = "completed"
}
