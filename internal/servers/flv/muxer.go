package flv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/abema/go-mp4"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/bluenviron/mediacommon/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/pkg/codecs/h265"
	"github.com/bluenviron/mediacommon/pkg/codecs/mpeg4audio"
	"github.com/bluenviron/mediamtx/internal/asyncwriter"
	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/protocols/rtmp/h264conf"
	"github.com/bluenviron/mediamtx/internal/stream"
	"github.com/bluenviron/mediamtx/internal/unit"
	"github.com/notedit/rtmp/format/flv/flvio"
)

var errNoSupportedCodecs = errors.New(
	"the stream doesn't contain any supported codec, which are currently H264, MPEG-4 Audio, MPEG-1/2 Audio")

type muxer struct {
	parentCtx      context.Context
	remoteAddr     string
	writeQueueSize int
	wg             *sync.WaitGroup
	pathName       string
	pathManager    serverPathManager
	parent         *Server
	conn           *conn

	ctx       context.Context
	ctxCancel func()
	path      defs.Path

	// out
	done chan struct{}
}

func (m *muxer) initialize() {
	ctx, ctxCancel := context.WithCancel(m.parentCtx)

	m.ctx = ctx
	m.ctxCancel = ctxCancel

	m.Log(logger.Info, "created requested by %s", m.remoteAddr)

	m.wg.Add(1)
	go m.run()
}

func (m *muxer) Close() {
	m.ctxCancel()
	<-m.done
}

// Log implements logger.Writer.
func (m *muxer) Log(level logger.Level, format string, args ...interface{}) {
	m.parent.Log(level, "[muxer %s] "+format, append([]interface{}{m.pathName}, args...)...)
}

// PathName returns the path name.
func (m *muxer) PathName() string {
	return m.pathName
}

func (m *muxer) run() {
	defer m.wg.Done()

	err := m.runInner()

	m.ctxCancel()

	m.parent.closeMuxer(m)

	m.Log(logger.Info, "destroyed: %v", err)
}

func (m *muxer) runInner() error {
	path, stream, err := m.pathManager.AddReader(defs.PathAddReaderReq{
		Author: m,
		AccessRequest: defs.PathAccessRequest{
			Name:     m.pathName,
			SkipAuth: true,
		},
	})
	if err != nil {
		return err
	}

	m.path = path

	defer m.path.RemoveReader(defs.PathRemoveReaderReq{Author: m})

	writer := asyncwriter.New(m.writeQueueSize, m)

	defer stream.RemoveReader(writer)

	videoFormat := m.setupVideo(stream, writer)
	audioFormat := m.setupAudio(stream, writer)

	if videoFormat == nil && audioFormat == nil {
		return errNoSupportedCodecs
	}

	m.Log(logger.Info, "is reading from path '%s', %s",
		path.Name(), defs.FormatsInfo(stream.FormatsForReader(writer)))

	err = m.writeTracks(videoFormat, audioFormat)
	if err != nil {
		return err
	}

	writer.Start()

	select {
	case <-m.ctx.Done():
		writer.Stop()
		return fmt.Errorf("terminated")

	case err := <-writer.Error():
		return err
	}
}

func (m *muxer) setupVideo(
	stream *stream.Stream,
	writer *asyncwriter.Writer,
) format.Format {
	var videoFormatH264 *format.H264
	videoMedia := stream.Desc().FindFormat(&videoFormatH264)

	if videoFormatH264 != nil {
		var videoDTSExtractor *h264.DTSExtractor

		stream.AddReader(writer, videoMedia, videoFormatH264, func(u unit.Unit) error {
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

			var dts time.Duration

			// wait until we receive an IDR
			if videoDTSExtractor == nil {
				if !idrPresent {
					return nil
				}

				videoDTSExtractor = h264.NewDTSExtractor()

				var err error
				dts, err = videoDTSExtractor.Extract(tunit.AU, tunit.PTS)
				if err != nil {
					return err
				}
			} else {
				if !idrPresent && !nonIDRPresent {
					return nil
				}

				var err error
				dts, err = videoDTSExtractor.Extract(tunit.AU, tunit.PTS)
				if err != nil {
					return err
				}
			}

			return m.writeH264(tunit.PTS, dts, idrPresent, tunit.AU)
		})

		return videoFormatH264
	}

	var videoFormatH265 *format.H265
	videoMedia = stream.Desc().FindFormat(&videoFormatH265)
	if videoFormatH265 != nil {
		var videoDTSExtractor *h265.DTSExtractor

		stream.AddReader(writer, videoMedia, videoFormatH265, func(u unit.Unit) error {
			tunit := u.(*unit.H265)

			if tunit.AU == nil {
				return nil
			}

			idrPresent := false

			for _, nalu := range tunit.AU {
				typ := h265.NALUType((nalu[0] >> 1) & 0b111111)

				switch typ {
				case h265.NALUType_IDR_W_RADL, h265.NALUType_IDR_N_LP, h265.NALUType_CRA_NUT:
					idrPresent = true
				}
			}

			var dts time.Duration

			// wait until we receive an IDR
			if videoDTSExtractor == nil {
				if !idrPresent {
					return nil
				}

				videoDTSExtractor = h265.NewDTSExtractor()

				var err error
				dts, err = videoDTSExtractor.Extract(tunit.AU, tunit.PTS)
				if err != nil {
					return err
				}
			} else {
				var err error
				dts, err = videoDTSExtractor.Extract(tunit.AU, tunit.PTS)
				if err != nil {
					return err
				}
			}

			return m.writeH265(tunit.PTS, dts, idrPresent, tunit.AU)
		})

		return videoFormatH265
	}

	return nil
}

func (m *muxer) setupAudio(
	stream *stream.Stream,
	writer *asyncwriter.Writer,
) format.Format {
	var audioFormatMPEG4Audio *format.MPEG4Audio
	audioMedia := stream.Desc().FindFormat(&audioFormatMPEG4Audio)

	if audioMedia != nil {
		stream.AddReader(writer, audioMedia, audioFormatMPEG4Audio, func(u unit.Unit) error {
			tunit := u.(*unit.MPEG4Audio)

			if tunit.AUs == nil {
				return nil
			}

			for i, au := range tunit.AUs {
				err := m.WriteMPEG4Audio(
					tunit.PTS+time.Duration(i)*mpeg4audio.SamplesPerAccessUnit*
						time.Second/time.Duration(audioFormatMPEG4Audio.ClockRate()),
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

	return nil
}

func (m *muxer) writeTracks(videoTrack format.Format, audioTrack format.Format) error {
	// narr := []interface{}{}
	// arr := flvio.AMFMap{}
	// header := flvHeader{}

	// if videoTrack != nil {
	// 	header.hasVideo = true
	// 	varr := flvio.AMFMap{
	// 		{
	// 			K: "videodatarate",
	// 			V: float64(0),
	// 		},
	// 		{
	// 			K: "videocodecid",
	// 			V: func() float64 {
	// 				switch videoTrack.(type) {
	// 				case *format.H264:
	// 					return message.CodecH264
	// 				case *format.H265:
	// 					return message.CodecH265

	// 				default:
	// 					return 0
	// 				}
	// 			}(),
	// 		},
	// 	}
	// 	arr = append(arr, varr...)
	// }

	// if audioTrack != nil {
	// 	header.hasAudio = true
	// 	aarr := flvio.AMFMap{
	// 		{
	// 			K: "audiodatarate",
	// 			V: float64(0),
	// 		},
	// 		{
	// 			K: "audiocodecid",
	// 			V: func() float64 {
	// 				switch audioTrack.(type) {
	// 				case *format.MPEG1Audio:
	// 					return message.CodecMPEG1Audio

	// 				case *format.MPEG4Audio:
	// 					return message.CodecMPEG4Audio

	// 				default:
	// 					return 0
	// 				}
	// 			}(),
	// 		},
	// 	}
	// 	arr = append(arr, aarr...)
	// }

	// // narr = append(narr, "onMetaData")
	// // narr = append(narr, arr)
	// // tagdata := flvio.FillAMF0ValsMalloc(narr)
	// // tag := flvio.Tag{
	// // 	Type: flvio.TAG_AMF0,
	// // 	Data: tagdata,
	// // 	Time: uint32(flvio.TimeToTs(0)),
	// // }
	// // if err := m.conn.writeTag(tag); err != nil {
	// // 	return err
	// // }
	header := flvHeader{}
	if videoTrack != nil {
		header.hasVideo = true
	}
	if audioTrack != nil {
		header.hasAudio = true
	}

	if err := m.conn.writeHeader(header); err != nil {
		return err
	}

	var count int = 0

	if videoTrack, ok := videoTrack.(*format.H264); ok {
		count = 0
		for {
			// write decoder config only if SPS and PPS are available.
			if sps, pps := videoTrack.SafeParams(); sps != nil && pps != nil {
				buf, _ := h264conf.Conf{
					SPS: sps,
					PPS: pps,
				}.Marshal()

				tag := flvio.Tag{
					Type:          flvio.TAG_VIDEO,
					FrameType:     flvio.FRAME_KEY,
					AVCPacketType: flvio.AVC_SEQHDR,
					VideoFormat:   flvio.VIDEO_H264,
					Data:          buf,
					Time:          uint32(flvio.TimeToTs(0)),
				}

				if err := m.conn.writeTag(tag); err != nil {
					return err
				} else {
					break
				}
			} else {
				count++
				time.Sleep(10 * time.Millisecond)
				if count > 300 {
					return errors.New("vps sps or pps are not avaiable")
				}
			}
		}

	}

	if videoTrack, ok := videoTrack.(*format.H265); ok {
		// write decoder config only if VPS SPS and PPS are available.
		count = 0
		for {
			if vps, sps, pps := videoTrack.SafeParams(); vps != nil && sps != nil && pps != nil {
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

				var buf bytes.Buffer
				_, err = mp4.Marshal(&buf, hvcc, mp4.Context{})
				if err != nil {
					return errors.New("vps sps or pps are not avaiable")
				}

				tag := flvio.Tag{
					Type:          flvio.TAG_VIDEO,
					FrameType:     flvio.FRAME_KEY,
					AVCPacketType: flvio.AVC_SEQHDR,
					VideoFormat:   flvio.VIDEO_H265,
					Data:          buf.Bytes(),
					Time:          uint32(flvio.TimeToTs(0)),
				}

				m.Log(logger.Info, "write video track ts:%d", tag.Time)

				if err := m.conn.writeTag(tag); err != nil {
					return err
				} else {
					break
				}
			} else {
				count++
				time.Sleep(10 * time.Millisecond)
				if count > 300 {
					return errors.New("vps sps or pps are not avaiable")
				}
			}
		}
	}

	if audioTrack != nil {
		var audioConfig *mpeg4audio.AudioSpecificConfig

		switch track := audioTrack.(type) {
		case *format.MPEG4Audio:
			audioConfig = track.Config

			// case *format.MPEG4AudioLATM:
			// 	audioConfig = track.Config.Programs[0].Layers[0].AudioSpecificConfig
		}

		if audioConfig != nil {
			enc, err := audioConfig.Marshal()
			if err != nil {
				return err
			}

			tag := flvio.Tag{
				Type:          flvio.TAG_AUDIO,
				SoundFormat:   flvio.SOUND_AAC,
				SoundRate:     flvio.SOUND_44Khz,
				SoundSize:     flvio.SOUND_16BIT,
				AACPacketType: flvio.AAC_SEQHDR,
				Data:          enc,
				Time:          uint32(flvio.TimeToTs(0)),
			}
			switch audioConfig.ChannelCount {
			case 1:
				tag.SoundType = flvio.SOUND_MONO
			default:
				tag.SoundType = flvio.SOUND_STEREO
			}

			m.Log(logger.Info, "write audio track ts:%d", tag.Time)

			if err := m.conn.writeTag(tag); err != nil {
				return err
			}
		} else {
			return errors.New("AudioSpecificConfig are not avaiable")
		}
	}

	return nil
}

func (m *muxer) writeH264(pts time.Duration, dts time.Duration, idrPresent bool, au [][]byte) error {
	avcc, err := h264.AVCCMarshal(au)
	if err != nil {
		return err
	}

	frameType := flvio.FRAME_INTER
	if idrPresent {
		frameType = flvio.FRAME_KEY
	}

	tag := flvio.Tag{
		Type:          flvio.TAG_VIDEO,
		AVCPacketType: flvio.AVC_NALU,
		FrameType:     uint8(frameType),
		VideoFormat:   flvio.VIDEO_H264,
		CTime:         int32(flvio.TimeToTs(0)),
		Time:          uint32(flvio.TimeToTs(pts)),
		Data:          avcc,
	}

	if err := m.conn.writeTag(tag); err != nil {
		return err
	}

	return nil
}

func (m *muxer) writeH265(pts time.Duration, dts time.Duration, idrPresent bool, au [][]byte) error {
	avcc, err := h264.AVCCMarshal(au)
	if err != nil {
		return err
	}

	frameType := flvio.FRAME_INTER
	if idrPresent {
		frameType = flvio.FRAME_KEY
	}

	tag := flvio.Tag{
		Type:          flvio.TAG_VIDEO,
		AVCPacketType: flvio.AVC_NALU,
		FrameType:     uint8(frameType),
		VideoFormat:   flvio.VIDEO_H265,
		CTime:         int32(flvio.TimeToTs(0)),
		Time:          uint32(flvio.TimeToTs(pts)),
		Data:          avcc,
	}

	if err := m.conn.writeTag(tag); err != nil {
		return err
	}

	return nil
}

func (m *muxer) WriteMPEG4Audio(pts time.Duration, au []byte) error {
	tag := flvio.Tag{
		Type:          flvio.TAG_AUDIO,
		SoundFormat:   flvio.SOUND_AAC,
		SoundRate:     flvio.SOUND_44Khz,
		SoundSize:     flvio.SOUND_16BIT,
		SoundType:     flvio.SOUND_MONO,
		AACPacketType: flvio.AAC_RAW,
		Data:          au,
		Time:          uint32(flvio.TimeToTs(pts)),
	}

	if err := m.conn.writeTag(tag); err != nil {
		return err
	}

	return nil
}

// APIReaderDescribe implements reader.
func (m *muxer) APIReaderDescribe() defs.APIPathSourceOrReader {
	return defs.APIPathSourceOrReader{
		Type: "flvMuxer",
		ID:   "",
	}
}
