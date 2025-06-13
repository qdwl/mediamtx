package flv

import (
	"errors"
	"time"

	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/stream"
	"github.com/bluenviron/mediamtx/internal/unit"
)

var errNoSupportedCodecsFrom = errors.New(
	"the stream doesn't contain any supported codec, which are currently H264, HEVC, MPEG-4 Audio, MPEG-1/2 Audio")

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
	w **Writer,
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

				return (*w).WriteH264(
					timestampToDuration(tunit.PTS, videoFormatH264.ClockRate()),
					timestampToDuration(dts, videoFormatH264.ClockRate()),
					tunit.AU)
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

				return (*w).WriteH265(
					timestampToDuration(tunit.PTS, videoFormatH265.ClockRate()),
					timestampToDuration(dts, videoFormatH265.ClockRate()),
					tunit.AU)
			})

		return videoFormatH265
	}

	return nil

}

func setupAudio(
	str *stream.Stream,
	reader stream.Reader,
	w **Writer,
	transcoder *AudioTranscoder,
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
					err := (*w).WriteMPEG4Audio(
						timestampToDuration(pts, audioFormatMPEG4Audio.ClockRate()),
						au,
					)
					if err != nil {
						return err
					}
				}

				return nil
			})

		return audioFormatMPEG4Audio
	}

	var g711Format *format.G711
	audioMedia = str.Desc.FindFormat(&g711Format)

	if g711Format != nil {
		audioFormatMPEG4Audio = &format.MPEG4Audio{
			PayloadTyp:       96,
			LATM:             false,
			SizeLength:       13,
			IndexLength:      3,
			IndexDeltaLength: 3,
			Config: &mpeg4audio.Config{
				Type:         mpeg4audio.ObjectTypeAACLC,
				SampleRate:   g711Format.SampleRate,
				ChannelCount: 2,
			},
		}
		err := transcoder.Initialize(g711Format, audioFormatMPEG4Audio)
		if err != nil {
			return nil
		}

		str.AddReader(
			reader,
			audioMedia,
			g711Format,
			func(u unit.Unit) error {
				tunit := u.(*unit.G711)

				pts := timestampToDuration(tunit.PTS, g711Format.ClockRate())
				pkts, err := transcoder.Transcode(pts, tunit.Samples)
				if err != nil {
					return err
				}

				for _, pkt := range pkts {
					err = (*w).WriteMPEG4Audio(
						time.Duration(pkt.pts)*time.Millisecond,
						pkt.buf,
					)
					if err != nil {
						return err
					}
				}

				return nil
			})

		return audioFormatMPEG4Audio
	}

	return nil

}

// FromStream maps a MediaMTX stream to a FLV stream.
func FromStream(
	str *stream.Stream,
	reader stream.Reader,
	conn *Conn,
	transcoder *AudioTranscoder,
) error {
	var w *Writer

	videoFormat := setupVideo(
		str,
		reader,
		&w,
	)

	audioFormat := setupAudio(
		str,
		reader,
		&w,
		transcoder,
	)

	if videoFormat == nil && audioFormat == nil {
		return errNoSupportedCodecsFrom
	}

	w = &Writer{
		Conn:       conn,
		VideoTrack: videoFormat,
		AudioTrack: audioFormat,
	}

	err := w.Initialize()
	if err != nil {
		return err
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
