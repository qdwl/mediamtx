package playback

import (
	"fmt"
	"sync"
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
	parent            logger.Writer
	stream            *stream.Stream
	tracks            []*muxerStreamTrack
	curTrack          *muxerStreamTrack
	playbackSpeed     float64
	lastPlaybackSpeed float64
	baseDTS           int64
	baseTime          time.Time
	baseSet           bool
	basePTS           int64
	basePTSTime       time.Time
	basePTSSet        bool
	wg                sync.WaitGroup
	done              chan struct{}
	paused            bool
	pauseMutex        sync.Mutex
	pauseCond         *sync.Cond
}

// Log implements logger.Writer.
func (m *muxerStream) Log(level logger.Level, format string, args ...interface{}) {
	m.parent.Log(level, "[muxerStream] "+format, args...)
}

func (m *muxerStream) Close() {
	m.pauseMutex.Lock()
	m.paused = false
	m.pauseCond.Broadcast()
	m.pauseMutex.Unlock()

	close(m.done)
	m.wg.Wait()
}

func (m *muxerStream) writeInit(init *fmp4.Init) {

}

func (m *muxerStream) writeSample(dts int64, ptsOffset int32, isNonSyncSample bool, payloadSize uint32, getPayload func() ([]byte, error)) error {
	m.wg.Add(1)
	defer m.wg.Done()

	// Check if playback is stopped
	select {
	case <-m.done:
		m.Log(logger.Info, "write sample terminate")
		return fmt.Errorf("playback stopped")
	default:
	}

	//目前仅支持回放视频
	if m.curTrack == nil || m.curTrack.media.Type != description.MediaTypeVideo {
		return nil
	}

	// Check if playback is paused
	m.pauseMutex.Lock()
	for m.paused {
		m.pauseCond.Wait()
		// Check if done while waiting
		select {
		case <-m.done:
			m.pauseMutex.Unlock()
			return fmt.Errorf("playback stopped")
		default:
		}
	}
	m.pauseMutex.Unlock()

	// Handle GOPs before GOP of first frame when not starting from beginning
	if dts < 0 {
		return nil
	}

	// Get payload
	data, err := getPayload()
	if err != nil {
		m.Log(logger.Error, "get payload err %v\n", err)
		return err
	}

	// Apply playback speed control
	if !m.baseSet {
		// Set base time for the first sample
		m.baseDTS = dts
		m.baseTime = time.Now()
		m.baseSet = true
		m.lastPlaybackSpeed = m.playbackSpeed
	} else {
		// Check if playback speed has changed
		if m.playbackSpeed != m.lastPlaybackSpeed {
			// Adjust base time to account for speed change
			// Calculate how much real time has passed since the last baseTime
			timePassed := time.Since(m.baseTime)
			// Calculate how much media time has passed
			mediaTimePassed := int64(float64(timePassed.Milliseconds()) * float64(90) * m.lastPlaybackSpeed)
			// Update baseDTS and baseTime
			m.baseDTS += mediaTimePassed
			m.baseTime = time.Now()
			m.Log(logger.Info, "playback speed changed from %f to %f, adjusting base time, base dts %d",
				m.lastPlaybackSpeed, m.playbackSpeed, m.baseDTS)

			m.lastPlaybackSpeed = m.playbackSpeed
		}

		// Calculate expected real time for current sample
		// Use absolute time difference to handle non-monotonic dts
		timeDiff := dts - m.baseDTS
		// Only proceed if timeDiff is positive
		// If dts is not monotonic, skip time control
		if timeDiff > 0 {
			expectedTime := m.baseTime.Add(time.Duration(float64(timeDiff)/m.playbackSpeed/90) * time.Millisecond)
			// Calculate actual time passed
			actualTime := time.Now()
			// If actual time is less than expected time, sleep
			if actualTime.Before(expectedTime) {
				m.Log(logger.Info, "++++++++ actualTime %d before expected time %d, diff %d, dts:%d, baseDts:%d",
					actualTime.UnixMilli(), expectedTime.UnixMilli(), timeDiff, dts, m.baseDTS)
				time.Sleep(expectedTime.Sub(actualTime))
			}
		} else if timeDiff < 0 {
			// dts is not monotonic, reset base time
			m.Log(logger.Info, "dts is not monotonic, resetting base time: %d -> %d", m.baseDTS, dts)
			m.baseDTS = dts
			m.baseTime = time.Now()
			m.lastPlaybackSpeed = m.playbackSpeed
		}
	}

	if !m.basePTSSet {
		m.basePTSTime = time.Now()
		m.basePTS = dts + int64(ptsOffset)
		m.basePTSSet = true
	} else {
		timeSince := time.Since(m.basePTSTime)
		m.basePTS = m.basePTS + int64(timeSince.Milliseconds()*90)
		m.basePTSTime = time.Now()

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
					PTS: m.basePTS,
				},
				AU: au,
			}

			// Write unit to stream
			m.stream.WriteUnit(m.curTrack.media, m.curTrack.media.Formats[0], u)
			m.Log(logger.Info, "write h264 pts:%d", u.PTS)

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
					PTS: m.basePTS,
				},
				AU: au,
			}

			// Write unit to stream
			m.stream.WriteUnit(m.curTrack.media, m.curTrack.media.Formats[0], u)

		case *format.MPEG4Audio:
			u := &unit.MPEG4Audio{
				Base: unit.Base{
					NTP: time.Now(),
					PTS: m.basePTS,
				},
				AUs: [][]byte{data},
			}

			m.Log(logger.Error, "send aac track pts:%d\n", m.basePTS)

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
}

// resetTimeBase resets the time base for all tracks.
// This should be called when a seek operation occurs.
func (m *muxerStream) resetTimeBase() {
	m.baseSet = false
	m.baseDTS = 0
	m.baseTime = time.Time{}
	m.lastPlaybackSpeed = m.playbackSpeed
	m.done = make(chan struct{})
	m.paused = false
	m.pauseCond = sync.NewCond(&m.pauseMutex)
}

// Pause pauses playback.
func (m *muxerStream) Pause() {
	m.pauseMutex.Lock()
	defer m.pauseMutex.Unlock()
	m.paused = true
	m.Log(logger.Info, "playback paused")
}

// Resume resumes playback.
func (m *muxerStream) Resume() {
	m.pauseMutex.Lock()
	defer m.pauseMutex.Unlock()
	m.paused = false
	m.pauseCond.Broadcast()
	m.Log(logger.Info, "playback resumed")
}

// IsPaused returns whether playback is paused.
func (m *muxerStream) IsPaused() bool {
	m.pauseMutex.Lock()
	defer m.pauseMutex.Unlock()
	return m.paused
}
