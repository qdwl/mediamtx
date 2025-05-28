package gb28181

import (
	"errors"
	"time"

	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/bluenviron/mediamtx/internal/protocols/gb28181/mpegps"
	"github.com/bluenviron/mediamtx/internal/stream"
	"github.com/bluenviron/mediamtx/internal/unit"
)

var errNoSupportedCodecsTo = errors.New(
	"the stream doesn't contain any supported codec, which are currently " +
		"H265, H264, MPEG-4 Audio, MPEG-1/2 Audio, G711, LPCM")

// ToStream maps a GB28181 stream to a MediaMTX stream.
func ToStream(conn *Conn, stream **stream.Stream) ([]*description.Media, error) {
	var medias []*description.Media //nolint:prealloc

	for _, track := range conn.Tracks() {
		switch codec := track.Codec.(type) {
		case *mpegps.CodecH264:
			medi := &description.Media{
				Type: description.MediaTypeVideo,
				Formats: []format.Format{
					&format.H264{
						PayloadTyp:        96,
						PacketizationMode: 1,
						SPS:               codec.SPS,
						PPS:               codec.PPS,
					}},
			}
			medias = append(medias, medi)

			conn.onFrameFunc(track.StreamType, func(pts time.Duration, data []byte) {
				var dec h264.AnnexB
				err := dec.Unmarshal(data)
				if err != nil {
					return
				}

				(*stream).WriteUnit(medi, medi.Formats[0], &unit.H264{
					Base: unit.Base{
						NTP: time.Now(),
						PTS: int64(pts) * 90,
					},
					AU: dec,
				})
			})

		case *mpegps.CodecH265:
			medi := &description.Media{
				Type: description.MediaTypeVideo,
				Formats: []format.Format{
					&format.H265{
						PayloadTyp: 96,
						VPS:        codec.VPS,
						SPS:        codec.SPS,
						PPS:        codec.PPS,
					}},
			}
			medias = append(medias, medi)

			conn.onFrameFunc(track.StreamType, func(pts time.Duration, data []byte) {
				var dec h264.AnnexB
				err := dec.Unmarshal(data)
				if err != nil {
					return
				}

				(*stream).WriteUnit(medi, medi.Formats[0], &unit.H265{
					Base: unit.Base{
						NTP: time.Now(),
						PTS: int64(pts) * 90,
					},
					AU: dec,
				})
			})

		case *mpegps.CodecMPEG4Audio:
			medi := &description.Media{
				Type: description.MediaTypeAudio,
				Formats: []format.Format{
					&format.MPEG4Audio{
						PayloadTyp:       96,
						SizeLength:       13,
						IndexLength:      3,
						IndexDeltaLength: 3,
						Config:           &codec.Config,
					}},
			}
			medias = append(medias, medi)

			conn.onFrameFunc(track.StreamType, func(pts time.Duration, data []byte) {
				var pkts mpeg4audio.ADTSPackets
				err := pkts.Unmarshal(data)
				if err != nil {
					return
				}

				aus := make([][]byte, len(pkts))
				for i, pkt := range pkts {
					aus[i] = pkt.AU
				}

				(*stream).WriteUnit(medi, medi.Formats[0], &unit.MPEG4Audio{
					Base: unit.Base{
						NTP: time.Now(),
						PTS: int64(pts),
					},
					AUs: aus,
				})
			})

		case *mpegps.CodecG711A:
			medi := &description.Media{
				Type: description.MediaTypeAudio,
				Formats: []format.Format{&format.G711{
					PayloadTyp:   98,
					MULaw:        true,
					SampleRate:   8000,
					ChannelCount: 1,
				}},
			}

			medias = append(medias, medi)

			conn.onFrameFunc(track.StreamType, func(pts time.Duration, data []byte) {
				(*stream).WriteUnit(medi, medi.Formats[0], &unit.G711{
					Base: unit.Base{
						NTP: time.Now(),
						PTS: int64(pts),
					},
					Samples: data,
				})
			})

		case *mpegps.CodecG711U:
			medi := &description.Media{
				Type: description.MediaTypeAudio,
				Formats: []format.Format{&format.G711{
					PayloadTyp:   98,
					MULaw:        false,
					SampleRate:   8000,
					ChannelCount: 1,
				}},
			}

			medias = append(medias, medi)

			conn.onFrameFunc(track.StreamType, func(pts time.Duration, data []byte) {
				(*stream).WriteUnit(medi, medi.Formats[0], &unit.G711{
					Base: unit.Base{
						NTP: time.Now(),
						PTS: int64(pts),
					},
					Samples: data,
				})
			})

		default:
			panic("should not happen")
		}
	}

	if len(medias) == 0 {
		return nil, errNoSupportedCodecsTo
	}

	return medias, nil
}
