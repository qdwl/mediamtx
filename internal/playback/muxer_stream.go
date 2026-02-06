package playback

import (
	"time"

	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/pmp4"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/stream"
	"github.com/bluenviron/mediamtx/internal/unit"
)

// muxerStreamTrack represents a track in the muxer stream.
type muxerStreamTrack struct {
	pmp4.Track
	media *description.Media
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
	parent        logger.Writer
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
	m.parent.Log(level, "[muxerStream] "+format, args...)
}

func (m *muxerStream) writeInit(init *fmp4.Init) {

}

func (m *muxerStream) writeSample(dts int64, ptsOffset int32, isNonSyncSample bool, payloadSize uint32, getPayload func() ([]byte, error)) error {
	m.Log(logger.Error, "writeSample dts:%d ptsOffset:%d\n", dts, ptsOffset)

	// Get payload
	data, err := getPayload()
	if err != nil {
		m.Log(logger.Error, "get payload err %v\n", err)
		return err
	}

	m.Log(logger.Info, "stream %+v, curTrack %+v, curTrack media %+v",
		m.stream, m.curTrack, m.curTrack.media)

	// Calculate PTS
	pts := dts + int64(ptsOffset)

	// Apply playback speed control
	if !m.baseSet {
		// Set base time for the first sample
		m.baseDTS = dts
		m.baseTime = time.Now()
		m.baseSet = true
	} else {
		// Calculate expected real time for current sample
		timeDiff := dts - m.baseDTS
		m.Log(logger.Info, "+++++++time diff %d", timeDiff)
		if timeDiff > 0 {
			expectedTime := m.baseTime.Add(time.Duration(float64(timeDiff)/m.playbackSpeed/90) * time.Millisecond)
			// Calculate actual time passed
			actualTime := time.Now()
			m.Log(logger.Info, "----------actualTime %d, expectedTime %d", actualTime.UnixMilli(), expectedTime.UnixMilli())
			// If actual time is less than expected time, sleep
			if actualTime.Before(expectedTime) {
				m.Log(logger.Info, "++++++++ actualTime %d before expected time %d", actualTime.UnixMilli(), expectedTime.UnixMilli())
				time.Sleep(expectedTime.Sub(actualTime))
			}
		}
	}

	// Write sample to stream
	if m.stream != nil && m.curTrack != nil && m.curTrack.media != nil {
		switch m.curTrack.media.Formats[0].(type) {
		case *format.H264:
			var au h264.AVCC
			if err := au.Unmarshal(data); err != nil {
				m.Log(logger.Error, "write sample %v", err)
				return err
			}

			// Create H264 unit
			u := &unit.H264{
				Base: unit.Base{
					NTP: time.Now(),
					PTS: pts,
				},
				AU: au,
			}

			// Write unit to stream
			m.stream.WriteUnit(m.curTrack.media, m.curTrack.media.Formats[0], u)

		case *format.H265:
			var au h264.AVCC
			if err := au.Unmarshal(data); err != nil {
				m.Log(logger.Error, "write sample %v", err)
				return err
			}

			// Create H264 unit
			u := &unit.H265{
				Base: unit.Base{
					NTP: time.Now(),
					PTS: pts,
				},
				AU: au,
			}

			m.Log(logger.Error, "send h265 track pts:%d\n", pts)

			// Write unit to stream
			m.stream.WriteUnit(m.curTrack.media, m.curTrack.media.Formats[0], u)

		case *format.MPEG4Audio:
			u := &unit.MPEG4Audio{
				Base: unit.Base{
					NTP: time.Now(),
					PTS: pts,
				},
				AUs: [][]byte{data},
			}

			m.Log(logger.Error, "send aac track pts:%d\n", pts)

			// Write unit to stream
			m.stream.WriteUnit(m.curTrack.media, m.curTrack.media.Formats[0], u)

		default:
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
