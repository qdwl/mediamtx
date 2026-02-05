package playback

import (
	"time"

	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/pmp4"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/stream"
	"github.com/bluenviron/mediamtx/internal/unit"
)

// muxerStreamTrack represents a track in the muxer stream.
type muxerStreamTrack struct {
	pmp4.Track
	media  *description.Media
	format format.Format
}

func findStreamTrack(tracks []*muxerStreamTrack, id int) *muxerStreamTrack {
	for _, track := range tracks {
		if track.ID == id {
			return track
		}
	}
	return nil
}

// muxerStream is a muxer that writes samples to a stream.
type muxerStream struct {
	Parent        logger.Writer
	stream        *stream.Stream
	tracks        []*muxerStreamTrack
	curTrack      *muxerStreamTrack
	playbackSpeed float64
	baseDTS       int64
	baseTime      time.Time
	baseSet       bool
}

// Log implements logger.Writer.
func (m *muxerStream) Log(level logger.Level, format string, args ...interface{}) {
	m.Parent.Log(level, "[muxerStream] "+format, args...)
}

func (m *muxerStream) writeInit(init *fmp4.Init) {
	m.tracks = make([]*muxerStreamTrack, 0)

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
			track := &muxerStreamTrack{
				Track: pmp4.Track{
					ID:        track.ID,
					TimeScale: track.TimeScale,
					Codec:     track.Codec,
				},
				media:  media,
				format: media.Formats[0],
			}
			m.tracks = append(m.tracks, track)
			m.Log(logger.Info, "write init create track %+v", track)

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
			track := &muxerStreamTrack{
				Track: pmp4.Track{
					ID:        track.ID,
					TimeScale: track.TimeScale,
					Codec:     track.Codec,
				},
				media:  media,
				format: media.Formats[0],
			}
			m.tracks = append(m.tracks, track)
			m.Log(logger.Info, "write init create track %+v", track)

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
			track := &muxerStreamTrack{
				Track: pmp4.Track{
					ID:        track.ID,
					TimeScale: track.TimeScale,
					Codec:     track.Codec,
				},
				media:  media,
				format: media.Formats[0],
			}
			m.tracks = append(m.tracks, track)
			m.Log(logger.Info, "write init create track %+v", track)

		default:
		}
	}
}

func (m *muxerStream) writeSample(dts int64, ptsOffset int32, isNonSyncSample bool, payloadSize uint32, getPayload func() ([]byte, error)) error {
	m.Log(logger.Error, "writeSample dts:%d ptsOffset:%d\n", dts, ptsOffset)

	// Get payload
	data, err := getPayload()
	if err != nil {
		m.Log(logger.Error, "get payload err %v\n", err)
		return err
	}

	m.Log(logger.Info, "stream %+v, curTrack %+v, curTrack media %+v curTrack format %+v",
		m.stream, m.curTrack, m.curTrack.media, m.curTrack.format)

	// Write sample to stream
	if m.stream != nil && m.curTrack != nil && m.curTrack.media != nil && m.curTrack.format != nil {
		// Calculate PTS
		pts := dts + int64(ptsOffset)

		m.Log(logger.Info, "write sample pts %d, data:% x", pts, data)

		switch m.curTrack.format.(type) {
		case *format.H264:
			// Unmarshal H264 data
			var dec h264.AnnexB
			err := dec.Unmarshal(data)
			if err != nil {
				m.Log(logger.Error, "write sample %v", err)
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

			m.Log(logger.Error, "send h264 track pts:%d\n", pts)

			// Write unit to stream
			m.stream.WriteUnit(m.curTrack.media, m.curTrack.format, u)

		case *format.H265:
			// Unmarshal H264 data
			var dec h264.AnnexB
			err := dec.Unmarshal(data)
			if err != nil {
				m.Log(logger.Error, "write sample %v", err)
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

			m.Log(logger.Error, "send h265 track pts:%d\n", pts)

			// Write unit to stream
			m.stream.WriteUnit(m.curTrack.media, m.curTrack.format, u)

		case *format.MPEG4Audio:
			// Unmarshal AAC data
			var pkts mpeg4audio.ADTSPackets
			err := pkts.Unmarshal(data)
			if err != nil {
				m.Log(logger.Error, "write sample %v", err)
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

			m.Log(logger.Error, "send aac track pts:%d\n", pts)

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
	m.Log(logger.Info, "set track %d curTrack %+v", trackID, m.curTrack)
	for _, val := range m.tracks {
		m.Log(logger.Info, "track %+v", val)
	}
}

// // resetTimeBase resets the time base for all tracks.
// // This should be called when a seek operation occurs.
// func (m *muxerStream) resetTimeBase() {
// 	m.baseSet = false
// 	m.baseDTS = 0
// 	m.baseTime = time.Time{}
// }
