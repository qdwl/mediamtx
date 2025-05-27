package gb28181

import (
	"errors"

	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/stream"
	"github.com/bluenviron/mediamtx/internal/unit"
	mpeg2 "github.com/qdwl/mpegps"
)

var errNoSupportedCodecsFrom = errors.New(
	"the stream doesn't contain any supported codec, which are currently H264, HEVC, MPEG-4 Audio, MPEG-1/2 Audio")

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
					conn.WriteAudio(au, uint64(pts), uint64(pts))
				}

				return nil
			})

		return audioFormatMPEG4Audio
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
