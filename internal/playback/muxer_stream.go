package playback

import (
	"time"

	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4"
	"github.com/bluenviron/mediamtx/internal/stream"
	"github.com/bluenviron/mediamtx/internal/unit"
)

// muxerStreamTrack represents a track in the muxer stream.
type muxerStreamTrack struct {
	media     *description.Media
	format    format.Format
	codecType string
}

// findStreamTrack finds a track by ID.
func findStreamTrack(tracks []*muxerStreamTrack, id int) *muxerStreamTrack {
	// Simple index-based lookup for now
	// In a real implementation, you might want to map track IDs to indexes
	if id < len(tracks) {
		return tracks[id]
	}
	return nil
}

// muxerStream is a muxer that writes samples to a stream.
type muxerStream struct {
	stream        *stream.Stream
	tracks        []*muxerStreamTrack
	curTrack      *muxerStreamTrack
	playbackSpeed float64
	baseDTS       int64
	baseTime      time.Time
	baseSet       bool
}

func (m *muxerStream) writeInit(init *fmp4.Init) {
	// Init data is already included in the stream description
	// Initialize tracks
	m.tracks = make([]*muxerStreamTrack, len(init.Tracks))
}

func (m *muxerStream) writeSample(dts int64, ptsOffset int32, isNonSyncSample bool, payloadSize uint32, getPayload func() ([]byte, error)) error {
	// Get payload
	data, err := getPayload()
	if err != nil {
		return err
	}

	// Write sample to stream
	if m.stream != nil && m.curTrack != nil && m.curTrack.media != nil && m.curTrack.format != nil {
		// Calculate PTS
		pts := dts + int64(ptsOffset)

		switch m.curTrack.format.(type) {
		case *format.H264:
			// Unmarshal H264 data
			var dec h264.AnnexB
			err := dec.Unmarshal(data)
			if err != nil {
				return err
			}

			// Create H264 unit
			u := &unit.H264{
				Base: unit.Base{
					NTP: time.Now(),
					PTS: pts,
				},
				AU: dec,
			}

			// Write unit to stream
			m.stream.WriteUnit(m.curTrack.media, m.curTrack.format, u)

		case *format.H265:
			// Unmarshal H264 data
			var dec h264.AnnexB
			err := dec.Unmarshal(data)
			if err != nil {
				return err
			}

			// Create H264 unit
			u := &unit.H265{
				Base: unit.Base{
					NTP: time.Now(),
					PTS: pts,
				},
				AU: dec,
			}

			// Write unit to stream
			m.stream.WriteUnit(m.curTrack.media, m.curTrack.format, u)

		case *format.MPEG4Audio:
			// Unmarshal AAC data
			var pkts mpeg4audio.ADTSPackets
			err := pkts.Unmarshal(data)
			if err != nil {
				return err
			}

			// Create AAC unit
			aus := make([][]byte, len(pkts))
			for i, pkt := range pkts {
				aus[i] = append(aus[i], pkt.AU...)
			}

			u := &unit.MPEG4Audio{
				Base: unit.Base{
					NTP: time.Now(),
					PTS: pts,
				},
				AUs: aus,
			}

			// Write unit to stream
			m.stream.WriteUnit(m.curTrack.media, m.curTrack.format, u)

		default:
		}
	}

	// Apply playback speed control
	if !m.baseSet {
		// Set base time for the first sample
		m.baseDTS = dts
		m.baseTime = time.Now()
		m.baseSet = true
	} else {
		// Calculate expected real time for current sample
		timeDiff := dts - m.baseDTS
		if timeDiff > 0 {
			expectedTime := m.baseTime.Add(time.Duration(float64(timeDiff) / m.playbackSpeed))
			// Calculate actual time passed
			actualTime := time.Now()
			// If actual time is less than expected time, sleep
			if actualTime.Before(expectedTime) {
				time.Sleep(expectedTime.Sub(actualTime))
			}
		}
	}

	return nil
}

func (m *muxerStream) writeFinalDTS(dts int64) {
	// Not needed for stream muxer
}

func (m *muxerStream) flush() error {
	// Not needed for stream muxer
	return nil
}

func (m *muxerStream) setTrack(trackID int) {
	// Set current track
	m.curTrack = findStreamTrack(m.tracks, trackID)
}

// // resetTimeBase resets the time base for all tracks.
// // This should be called when a seek operation occurs.
// func (m *muxerStream) resetTimeBase() {
// 	m.baseSet = false
// 	m.baseDTS = 0
// 	m.baseTime = time.Time{}
// }

// setTrackMedia sets the media and format for a track.
func (m *muxerStream) setTrackMedia(trackID int, media *description.Media, fmt format.Format) {
	if trackID < len(m.tracks) {
		// Determine codec type
		codecType := ""
		switch f := fmt.(type) {
		case *format.H264:
			codecType = "H264"
		case *format.H265:
			codecType = "H265"
		case *format.MPEG4Audio:
			codecType = "AAC"
		case *format.G711:
			if f.MULaw {
				codecType = "G711U"
			} else {
				codecType = "G711A"
			}
		}

		m.tracks[trackID] = &muxerStreamTrack{
			media:     media,
			format:    fmt,
			codecType: codecType,
		}
	}
}
