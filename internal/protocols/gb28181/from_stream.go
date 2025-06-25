package gb28181

import (
	"errors"
	"time"

	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/g711"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/stream"
	"github.com/bluenviron/mediamtx/internal/unit"
	mpeg2 "github.com/qdwl/mpegps"
)

var errNoSupportedCodecsFrom = errors.New(
	"the stream doesn't contain any supported codec, which are currently H264, HEVC, MPEG-4 Audio, G711")

func multiplyAndDivide2(v, m, d time.Duration) time.Duration {
	secs := v / d
	dec := v % d
	return (secs*m + dec*m/d)
}

func timestampToDuration(t int64, clockRate int) time.Duration {
	return multiplyAndDivide2(time.Duration(t), time.Second, time.Duration(clockRate))
}

func setupVideo(
	str *stream.Stream,
	reader stream.Reader,
	conn *Conn,
) format.Format {
	var videoFormatH264 *format.H264
	videoMedia := str.Desc.FindFormat(&videoFormatH264)

	if videoFormatH264 != nil {
		var videoDTSExtractor *h264.DTSExtractor

		str.AddReader(
			reader,
			videoMedia,
			videoFormatH264,
			func(u unit.Unit) error {
				tunit := u.(*unit.H264)

				if tunit.AU == nil {
					return nil
				}

				idrPresent := false
				nonIDRPresent := false

				for _, nalu := range tunit.AU {
					typ := h264.NALUType(nalu[0] & 0x1F)
					switch typ {
					case h264.NALUTypeIDR:
						idrPresent = true

					case h264.NALUTypeNonIDR:
						nonIDRPresent = true
					}
				}

				// wait until we receive an IDR
				if videoDTSExtractor == nil {
					if !idrPresent {
						return nil
					}

					videoDTSExtractor = &h264.DTSExtractor{}
					videoDTSExtractor.Initialize()
				} else if !idrPresent && !nonIDRPresent {
					return nil
				}

				dts, err := videoDTSExtractor.Extract(tunit.AU, tunit.PTS)
				if err != nil {
					return err
				}

				var dec h264.AnnexB = tunit.AU
				enc, err := dec.Marshal()
				if err != nil {
					return err
				}
				conn.WriteVideo(enc, uint64(tunit.PTS), uint64(dts))

				return nil
			})

		return videoFormatH264
	}

	var videoFormatH265 *format.H265
	videoMedia = str.Desc.FindFormat(&videoFormatH265)

	if videoFormatH265 != nil {
		var videoDTSExtractor *h265.DTSExtractor

		str.AddReader(
			reader,
			videoMedia,
			videoFormatH265,
			func(u unit.Unit) error {
				tunit := u.(*unit.H265)

				if tunit.AU == nil {
					return nil
				}

				idrPresent := false
				nonIDRPresent := false

				for _, nalu := range tunit.AU {
					typ := h265.NALUType((nalu[0] >> 1) & 0x3F)

					switch typ {
					case h265.NALUType_BLA_W_LP:
						fallthrough
					case h265.NALUType_BLA_W_RADL:
						fallthrough
					case h265.NALUType_BLA_N_LP:
						fallthrough
					case h265.NALUType_IDR_W_RADL:
						fallthrough
					case h265.NALUType_IDR_N_LP:
						fallthrough
					case h265.NALUType_CRA_NUT:
						idrPresent = true
					case h265.NALUType_TRAIL_N:
						fallthrough
					case h265.NALUType_TRAIL_R:
						fallthrough
					case h265.NALUType_TSA_N:
						fallthrough
					case h265.NALUType_TSA_R:
						fallthrough
					case h265.NALUType_STSA_N:
						fallthrough
					case h265.NALUType_STSA_R:
						nonIDRPresent = true
					}
				}

				// wait until we receive an IDR
				if videoDTSExtractor == nil {
					if !idrPresent {
						return nil
					}

					videoDTSExtractor = &h265.DTSExtractor{}
					videoDTSExtractor.Initialize()
				} else if !idrPresent && !nonIDRPresent {
					return nil
				}

				dts, err := videoDTSExtractor.Extract(tunit.AU, tunit.PTS)
				if err != nil {
					return err
				}

				var dec h264.AnnexB = tunit.AU
				enc, err := dec.Marshal()
				if err != nil {
					return err
				}
				conn.WriteVideo(enc, uint64(tunit.PTS), uint64(dts))

				return nil
			})

		return videoFormatH265
	}

	return nil

}

func setupAudio(
	str *stream.Stream,
	reader stream.Reader,
	conn *Conn,
) format.Format {

	var audioFormatMPEG4Audio *format.MPEG4Audio
	audioMedia := str.Desc.FindFormat(&audioFormatMPEG4Audio)

	if audioMedia != nil {
		str.AddReader(
			reader,
			audioMedia,
			audioFormatMPEG4Audio,
			func(u unit.Unit) error {
				tunit := u.(*unit.MPEG4Audio)

				if tunit.AUs == nil {
					return nil
				}

				for i, au := range tunit.AUs {
					pts := tunit.PTS + int64(i)*mpeg4audio.SamplesPerAccessUnit
					ts := timestampToDuration(pts, audioFormatMPEG4Audio.ClockRate())

					conn.WriteAudio(au, uint64(ts), uint64(ts))
				}

				return nil
			})

		return audioFormatMPEG4Audio
	}

	var g711Format *format.G711
	audioMedia = str.Desc.FindFormat(&g711Format)
	if audioMedia != nil {
		str.AddReader(
			reader,
			audioMedia,
			g711Format,
			func(u unit.Unit) error {
				tunit := u.(*unit.G711)

				if tunit.Samples == nil {
					return nil
				}

				ts := timestampToDuration(tunit.PTS, g711Format.ClockRate())
				ts = ts / time.Millisecond

				if conn.GetPayloadType() != g711Format.PayloadTyp {
					var lpcm []byte
					if g711Format.MULaw {
						var mu g711.Mulaw
						mu.Unmarshal(tunit.Samples)
						lpcm = mu

						al := g711.Alaw(lpcm)
						samples, err := al.Marshal()
						if err != nil {
							conn.WriteAudio(samples, uint64(ts), uint64(ts))
						}
					} else {
						var al g711.Alaw
						al.Unmarshal(tunit.Samples)
						lpcm = al

						mu := g711.Mulaw(lpcm)
						samples, err := mu.Marshal()
						if err != nil {
							conn.WriteAudio(samples, uint64(ts), uint64(ts))
						}
					}
				} else {
					conn.WriteAudio(tunit.Samples, uint64(ts), uint64(ts))
				}

				return nil
			})

		return g711Format
	}

	return nil

}

func FromStream(
	str *stream.Stream,
	reader stream.Reader,
	conn *Conn,
) error {
	videoFormat := setupVideo(
		str,
		reader,
		conn,
	)

	audioFormat := setupAudio(
		str,
		reader,
		conn,
	)

	if videoFormat == nil && audioFormat == nil {
		return errNoSupportedCodecsFrom
	}

	if _, ok := videoFormat.(*format.H264); ok {
		conn.AddVideoStream(mpeg2.PS_STREAM_H264)
	}
	if _, ok := videoFormat.(*format.H265); ok {
		conn.AddVideoStream(mpeg2.PS_STREAM_H265)
	}
	if _, ok := audioFormat.(*format.MPEG4Audio); ok {
		conn.AddAudioStream(mpeg2.PS_STREAM_AAC)
	}
	if forma, ok := audioFormat.(*format.G711); ok {
		if forma.MULaw {
			conn.AddAudioStream(mpeg2.PS_STREAM_G711U)
		} else {
			conn.AddAudioStream(mpeg2.PS_STREAM_G711A)
		}
	}

	n := 1
	for _, media := range str.Desc.Medias {
		for _, forma := range media.Formats {
			if forma != videoFormat && forma != audioFormat {
				reader.Log(logger.Warn, "skipping track %d (%s)", n, forma.Codec())
			}
			n++
		}
	}

	return nil
}
