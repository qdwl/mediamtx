package flv

import (
	"errors"
	"time"

	"github.com/abema/go-mp4"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/bluenviron/mediamtx/internal/protocols/rtmp/h264conf"
	"github.com/bluenviron/mediamtx/internal/protocols/rtmp/message"
)

// Writer is a wrapper around Conn that provides utilities to mux outgoing data.
type Writer struct {
	Conn       *Conn
	VideoTrack format.Format
	AudioTrack format.Format
}

// Initialize initializes a Writer.
func (w *Writer) Initialize() error {
	err := w.writeTracks()
	if err != nil {
		return err
	}

	return nil
}

func (w *Writer) writeTracks() error {
	header := &Header{
		VideoFlag: w.VideoTrack != nil,
		AudioFlag: w.AudioTrack != nil,
	}

	if err := w.Conn.WriteHeader(header); err != nil {
		return err
	}

	if videoTrack, ok := w.VideoTrack.(*format.H264); ok {
		// write decoder config only if SPS and PPS are available.
		// if they're not available yet, they're sent later.
		if sps, pps := videoTrack.SafeParams(); sps != nil && pps != nil {
			buf, _ := h264conf.Conf{
				SPS: sps,
				PPS: pps,
			}.Marshal()

			err := w.Conn.Write(&Video{
				Codec:      CodecH264,
				IsKeyFrame: true,
				Type:       VideoTypeConfig,
				Payload:    buf,
			})

			if err != nil {
				return err
			}
		}
	}

	if videoTrack, ok := w.VideoTrack.(*format.H265); ok {
		vps, sps, pps := videoTrack.SafeParams()
		if vps != nil && sps != nil && pps != nil {
			var spsp h265.SPS
			err := spsp.Unmarshal(sps)
			if err != nil {
				return errors.New("vps sps or pps are not avaiable")
			}

			hvcc := &mp4.HvcC{
				ConfigurationVersion:        1,
				GeneralProfileIdc:           spsp.ProfileTierLevel.GeneralProfileIdc,
				GeneralProfileCompatibility: spsp.ProfileTierLevel.GeneralProfileCompatibilityFlag,
				GeneralConstraintIndicator: [6]uint8{
					sps[7], sps[8], sps[9], sps[10], sps[11], sps[12],
				},
				GeneralLevelIdc: spsp.ProfileTierLevel.GeneralLevelIdc,
				// MinSpatialSegmentationIdc
				// ParallelismType
				ChromaFormatIdc:      uint8(spsp.ChromaFormatIdc),
				BitDepthLumaMinus8:   uint8(spsp.BitDepthLumaMinus8),
				BitDepthChromaMinus8: uint8(spsp.BitDepthChromaMinus8),
				// AvgFrameRate
				// ConstantFrameRate
				NumTemporalLayers: 1,
				// TemporalIdNested
				LengthSizeMinusOne: 3,
				NumOfNaluArrays:    3,
				NaluArrays: []mp4.HEVCNaluArray{
					{
						NaluType: byte(h265.NALUType_VPS_NUT),
						NumNalus: 1,
						Nalus: []mp4.HEVCNalu{{
							Length:  uint16(len(vps)),
							NALUnit: vps,
						}},
					},
					{
						NaluType: byte(h265.NALUType_SPS_NUT),
						NumNalus: 1,
						Nalus: []mp4.HEVCNalu{{
							Length:  uint16(len(sps)),
							NALUnit: sps,
						}},
					},
					{
						NaluType: byte(h265.NALUType_PPS_NUT),
						NumNalus: 1,
						Nalus: []mp4.HEVCNalu{{
							Length:  uint16(len(pps)),
							NALUnit: pps,
						}},
					},
				},
			}

			err = w.Conn.Write(&VideoExSequenceStart{
				FourCC:     message.FourCCHEVC,
				HEVCHeader: hvcc,
			})
			if err != nil {
				return err
			}
		}

	}

	var audioConfig *mpeg4audio.AudioSpecificConfig

	if track, ok := w.AudioTrack.(*format.MPEG4Audio); ok {
		audioConfig = track.GetConfig()
	}

	if audioConfig != nil {
		enc, err := audioConfig.Marshal()
		if err != nil {
			return err
		}

		err = w.Conn.Write(&Audio{
			Codec:    message.CodecMPEG4Audio,
			Rate:     message.Rate44100,
			Depth:    message.Depth16,
			IsStereo: true,
			AACType:  AudioAACTypeConfig,
			Payload:  enc,
		})
		if err != nil {
			return err
		}
	}

	return nil
}

// WriteH264 writes H264 data.
func (w *Writer) WriteH264(pts time.Duration, dts time.Duration, au [][]byte) error {
	avcc, err := h264.AVCC(au).Marshal()
	if err != nil {
		return err
	}

	return w.Conn.Write(&Video{
		DTS:        dts,
		Codec:      CodecH264,
		IsKeyFrame: h264.IsRandomAccess(au),
		Type:       VideoTypeAU,
		Payload:    avcc,
		PTSDelta:   pts - dts,
	})
}

// WriteH265 writes H265 data.
func (w *Writer) WriteH265(pts time.Duration, dts time.Duration, au [][]byte) error {
	avcc, err := h264.AVCC(au).Marshal()
	if err != nil {
		return err
	}

	if pts != dts {
		return w.Conn.Write(&VideoExCodedFrames{
			Payload:  avcc,
			FourCC:   message.FourCCHEVC,
			DTS:      dts,
			PTSDelta: pts - dts,
		})
	} else {
		return w.Conn.Write(&VideoExFramesX{
			Payload: avcc,
			FourCC:  message.FourCCHEVC,
			DTS:     dts,
		})
	}
}

// WriteMPEG4Audio writes MPEG-4 Audio data.
func (w *Writer) WriteMPEG4Audio(pts time.Duration, au []byte) error {
	return w.Conn.Write(&Audio{
		Codec:    CodecMPEG4Audio,
		Rate:     Rate44100,
		Depth:    Depth16,
		IsStereo: true,
		AACType:  AudioAACTypeAU,
		Payload:  au,
		DTS:      pts,
	})
}
