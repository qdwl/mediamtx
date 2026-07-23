//go:build linux && arm64

package codec

// #cgo linux,arm64 LDFLAGS: -L${SRCDIR}/../../thirdparty/ffmpeg-n6.1.2-linux-arm64/lib -lavcodec -lavutil -lswscale -lswresample
// #cgo linux,arm64 CFLAGS: -I${SRCDIR}/../../thirdparty/ffmpeg-n6.1.2-linux-arm64/include
// #include <libavcodec/avcodec.h>
// #include <libavutil/imgutils.h>
// #include <libswscale/swscale.h>
// #include <libswresample/swresample.h>
// #include <libavutil/audio_fifo.h>
// #include <libavutil/samplefmt.h>
import "C"
import (
	"fmt"
	"time"
	"unsafe"

	"github.com/bluenviron/gortsplib/v4/pkg/format"
)

type AudioPacket struct {
	PTS int64
	DTS int64
	Buf []uint8
}

type AudioTranscoder struct {
	decCtx    *C.AVCodecContext
	decFrame  *C.AVFrame
	decPacket *C.AVPacket

	encCtx    *C.AVCodecContext
	encFrame  *C.AVFrame
	encPacket *C.AVPacket

	swrCtx *C.SwrContext
	//buffer for swr output
	swrBuf [][]byte
	fifo   *C.AVAudioFifo

	newPktPts  int64
	nextOutPts int64
}

func getCodecInfo(forma format.Format) (codecId C.enum_AVCodecID, sampleRate int, channels int, err error) {
	switch typ := forma.(type) {
	case *format.G711:
		if typ.MULaw {
			codecId = C.AV_CODEC_ID_PCM_MULAW
		} else {
			codecId = C.AV_CODEC_ID_PCM_ALAW
		}
		sampleRate = typ.SampleRate
		channels = typ.ChannelCount
		return
	case *format.MPEG4Audio:
		codecId = C.AV_CODEC_ID_AAC
		sampleRate = typ.Config.SampleRate
		channels = typ.Config.ChannelCount
		return
	}

	return 0, 0, 0, fmt.Errorf("not support")
}

func (t *AudioTranscoder) Initialize(src format.Format, dst format.Format) error {
	if err := t.initDecoder(src); err != nil {
		return err
	}

	if err := t.initEncoder(dst); err != nil {
		return err
	}

	if err := t.initFifo(); err != nil {
		return err
	}

	return nil
}

// VideoTranscoder decodes H.264/H.265 video and encodes to JPEG.
type VideoTranscoder struct {
	decCtx   *C.AVCodecContext
	decFrame *C.AVFrame
	decPkt   *C.AVPacket

	encCtx *C.AVCodecContext
	encPkt *C.AVPacket

	swsCtx   *C.struct_SwsContext
	swsFrame *C.AVFrame
	swsBuf   []byte

	initialized bool
	ptsCounter  int64
}

func (t *VideoTranscoder) closeVideoTranscoder() {
	if t.decCtx != nil {
		C.avcodec_close(t.decCtx)
		t.decCtx = nil
	}
	if t.decFrame != nil {
		C.av_frame_free(&t.decFrame)
		t.decFrame = nil
	}
	if t.decPkt != nil {
		C.av_packet_free(&t.decPkt)
		t.decPkt = nil
	}
	if t.encCtx != nil {
		C.avcodec_close(t.encCtx)
		t.encCtx = nil
	}
	if t.encPkt != nil {
		C.av_packet_free(&t.encPkt)
		t.encPkt = nil
	}
	if t.swsCtx != nil {
		C.sws_freeContext(t.swsCtx)
		t.swsCtx = nil
	}
	if t.swsFrame != nil {
		C.av_frame_free(&t.swsFrame)
		t.swsFrame = nil
	}
	t.swsBuf = nil
	t.initialized = false
}

func mapCodecID(goID VideoCodecID) C.enum_AVCodecID {
	switch goID {
	case VideoCodecH264:
		return C.AV_CODEC_ID_H264
	case VideoCodecH265:
		return C.AV_CODEC_ID_HEVC
	default:
		return C.AV_CODEC_ID_NONE
	}
}

// Initialize initializes the video transcoder with optional codec extradata (SPS/PPS).
func (t *VideoTranscoder) Initialize(codecID VideoCodecID, extradata []byte) error {
	avCodecID := mapCodecID(codecID)
	if avCodecID == C.AV_CODEC_ID_NONE {
		return fmt.Errorf("unsupported codec")
	}

	// init decoder
	codec := C.avcodec_find_decoder(avCodecID)
	if codec == nil {
		return fmt.Errorf("avcodec_find_decoder() failed")
	}

	t.decCtx = C.avcodec_alloc_context3(codec)
	if t.decCtx == nil {
		return fmt.Errorf("avcodec_alloc_context3() failed")
	}

	// set extradata (e.g. SPS/PPS for H.264, VPS/SPS/PPS for H.265)
	if len(extradata) > 0 {
		t.decCtx.extradata_size = C.int(len(extradata))
		t.decCtx.extradata = (*C.uint8_t)(C.av_malloc(C.size_t(len(extradata))))
		if t.decCtx.extradata == nil {
			return fmt.Errorf("failed to allocate extradata")
		}
		C.memcpy(unsafe.Pointer(t.decCtx.extradata), unsafe.Pointer(&extradata[0]), C.size_t(len(extradata)))
	}

	res := C.avcodec_open2(t.decCtx, codec, nil)
	if res < 0 {
		return fmt.Errorf("avcodec_open2() failed for decoder")
	}

	t.decFrame = C.av_frame_alloc()
	if t.decFrame == nil {
		return fmt.Errorf("av_frame_alloc() failed")
	}

	t.decPkt = C.av_packet_alloc()
	if t.decPkt == nil {
		return fmt.Errorf("av_packet_alloc() failed")
	}

	t.initialized = true
	return nil
}

func (t *VideoTranscoder) initEncoder(width, height int) error {
	codec := C.avcodec_find_encoder(C.AV_CODEC_ID_MJPEG)
	if codec == nil {
		return fmt.Errorf("avcodec_find_encoder(MJPEG) failed")
	}

	t.encCtx = C.avcodec_alloc_context3(codec)
	if t.encCtx == nil {
		return fmt.Errorf("avcodec_alloc_context3() failed")
	}

	t.encCtx.width = C.int(width)
	t.encCtx.height = C.int(height)
	t.encCtx.pix_fmt = C.AV_PIX_FMT_YUV420P
	t.encCtx.color_range = C.AVCOL_RANGE_JPEG
	t.encCtx.time_base.num = 1
	t.encCtx.time_base.den = 1

	res := C.avcodec_open2(t.encCtx, codec, nil)
	if res < 0 {
		return fmt.Errorf("avcodec_open2() failed for encoder")
	}

	t.encPkt = C.av_packet_alloc()
	if t.encPkt == nil {
		return fmt.Errorf("av_packet_alloc() failed")
	}

	return nil
}

func (t *VideoTranscoder) initSws(width, height int) error {
	w := C.int(width)
	h := C.int(height)

	t.swsCtx = C.sws_getContext(
		w, h, C.AV_PIX_FMT_YUV420P,
		w, h, C.AV_PIX_FMT_YUV420P,
		C.SWS_BILINEAR, nil, nil, nil,
	)
	if t.swsCtx == nil {
		return fmt.Errorf("sws_getContext() failed")
	}

	t.swsFrame = C.av_frame_alloc()
	if t.swsFrame == nil {
		return fmt.Errorf("av_frame_alloc() failed")
	}
	t.swsFrame.width = w
	t.swsFrame.height = h
	t.swsFrame.format = C.AV_PIX_FMT_YUV420P
	t.swsFrame.color_range = C.AVCOL_RANGE_JPEG

	res := C.av_frame_get_buffer(t.swsFrame, 0)
	if res < 0 {
		return fmt.Errorf("av_frame_get_buffer() failed")
	}

	return nil
}

// DecodeAndEncode feeds H.264/H.265 NAL data and returns JPEG bytes on success.
func (t *VideoTranscoder) DecodeAndEncode(nalData []byte) ([]byte, error) {
	if !t.initialized {
		return nil, fmt.Errorf("transcoder not initialized")
	}
	if len(nalData) == 0 {
		return nil, nil
	}

	cdata := (*C.uint8_t)(unsafe.Pointer(&nalData[0]))
	clen := C.int(len(nalData))

	t.decPkt.data = cdata
	t.decPkt.size = clen

	res := C.avcodec_send_packet(t.decCtx, t.decPkt)
	if res < 0 {
		return nil, nil // need more data / not yet decoded
	}

	res = C.avcodec_receive_frame(t.decCtx, t.decFrame)
	if res == -C.EAGAIN {
		return nil, nil
	}
	if res < 0 {
		return nil, nil
	}

	// initialize encoder and scaler on first frame
	dw := int(t.decFrame.width)
	dh := int(t.decFrame.height)

	if t.encCtx == nil {
		if err := t.initEncoder(dw, dh); err != nil {
			return nil, err
		}
		if err := t.initSws(dw, dh); err != nil {
			return nil, err
		}
	}

	// scale YUV420P to YUV420P (JPEG range) if needed
	frameToEncode := t.decFrame
	if t.swsCtx != nil {
		C.sws_scale(t.swsCtx,
			&t.decFrame.data[0], &t.decFrame.linesize[0], 0, C.int(dh),
			&t.swsFrame.data[0], &t.swsFrame.linesize[0],
		)
		frameToEncode = t.swsFrame
	}

	t.ptsCounter++
	frameToEncode.pts = C.int64_t(t.ptsCounter)

	res = C.avcodec_send_frame(t.encCtx, frameToEncode)
	if res < 0 {
		return nil, fmt.Errorf("encoder send frame failed")
	}

	res = C.avcodec_receive_packet(t.encCtx, t.encPkt)
	if res == -C.EAGAIN {
		return nil, nil
	}
	if res < 0 {
		return nil, fmt.Errorf("encoder receive packet failed")
	}

	jpeg := C.GoBytes(unsafe.Pointer(t.encPkt.data), C.int(t.encPkt.size))
	C.av_packet_unref(t.encPkt)

	return jpeg, nil
}

// Close releases all resources.
func (t *VideoTranscoder) Close() {
	t.closeVideoTranscoder()
}

func (t *AudioTranscoder) Close() {
	if t.decCtx != nil {
		C.avcodec_close(t.decCtx)
		// C.avcodec_free_context(&t.decCtx)
		t.decCtx = nil
	}

	if t.decFrame != nil {
		C.av_frame_free(&t.decFrame)
		t.decFrame = nil
	}

	if t.decPacket != nil {
		C.av_packet_free(&t.decPacket)
		t.decPacket = nil
	}

	if t.swrCtx != nil {
		C.swr_free(&t.swrCtx)
		t.swrCtx = nil
	}

	if t.encCtx != nil {
		C.avcodec_close(t.encCtx)
		// C.avcodec_free_context(&t.encCtx)
		t.encCtx = nil
	}

	if t.encFrame != nil {
		C.av_frame_free(&t.encFrame)
		t.encFrame = nil
	}

	if t.encPacket != nil {
		C.av_packet_free(&t.encPacket)
		t.encPacket = nil
	}

	if t.fifo != nil {
		C.av_audio_fifo_free(t.fifo)
		t.fifo = nil
	}
}

func (t *AudioTranscoder) initDecoder(forma format.Format) error {
	codecId, sampleRate, channels, err := getCodecInfo(forma)
	if err != nil {
		return err
	}

	codec := C.avcodec_find_decoder(codecId)
	if codec == nil {
		return fmt.Errorf("avcodec_find_decoder() failed")
	}

	t.decCtx = C.avcodec_alloc_context3(codec)
	if t.decCtx == nil {
		return fmt.Errorf("avcodec_alloc_context3() failed")
	}

	t.decCtx.sample_rate = C.int(sampleRate)
	t.decCtx.channels = C.int(channels)

	res := C.avcodec_open2(t.decCtx, codec, nil)
	if res < 0 {
		// C.avcodec_close(t.decCtx)
		return fmt.Errorf("avcodec_open2() failed")
	}

	t.decCtx.channel_layout = C.ulong(C.av_get_default_channel_layout(t.decCtx.channels))

	t.decFrame = C.av_frame_alloc()
	if t.decFrame == nil {
		// C.avcodec_close(t.decCtx)
		return fmt.Errorf("av_frame_alloc() failed")
	}

	t.decPacket = C.av_packet_alloc()
	if t.decPacket == nil {
		// C.av_frame_free(&t.decFrame)
		// C.avcodec_close(t.decCtx)
		return fmt.Errorf("av_packet_alloc() failed")
	}

	return nil
}

func (t *AudioTranscoder) initEncoder(forma format.Format) error {
	codecId, sampleRate, channels, err := getCodecInfo(forma)
	if err != nil {
		return err
	}

	codec := C.avcodec_find_encoder(codecId)
	if codec == nil {
		return fmt.Errorf("avcodec_find_encoder() failed")
	}

	t.encCtx = C.avcodec_alloc_context3(codec)
	if t.encCtx == nil {
		return fmt.Errorf("avcodec_alloc_context3() failed")
	}

	t.encCtx.sample_rate = C.int(sampleRate)
	t.encCtx.channels = C.int(channels)
	t.encCtx.channel_layout = C.ulong(C.av_get_default_channel_layout(t.encCtx.channels))
	t.encCtx.bit_rate = C.long(32000)
	t.encCtx.sample_fmt = *codec.sample_fmts
	t.encCtx.time_base.num = 1
	t.encCtx.time_base.den = 1000
	t.encCtx.strict_std_compliance = C.FF_COMPLIANCE_EXPERIMENTAL

	res := C.avcodec_open2(t.encCtx, codec, nil)
	if res < 0 {
		// C.avcodec_close(t.encCtx)
		return fmt.Errorf("avcodec_open2() failed")
	}

	t.encFrame = C.av_frame_alloc()
	if t.encFrame == nil {
		// C.avcodec_close(t.decCtx)
		return fmt.Errorf("av_frame_alloc() failed")
	}

	t.encFrame.format = C.int(t.encCtx.sample_fmt)
	t.encFrame.nb_samples = t.encCtx.frame_size
	t.encFrame.channel_layout = t.encCtx.channel_layout

	if res := C.av_frame_get_buffer(t.encFrame, 0); res < 0 {
		// C.av_frame_free(&t.encFrame)
		// C.avcodec_close(t.encCtx)
		return fmt.Errorf("Could not get audio frame buffer")
	}

	t.encPacket = C.av_packet_alloc()
	if t.encPacket == nil {
		// C.av_frame_free(&t.encFrame)
		// C.avcodec_close(t.encCtx)
		return fmt.Errorf("av_packet_alloc() failed")
	}

	return nil
}

func (t *AudioTranscoder) initSwr(decCtx *C.AVCodecContext, encCtx *C.AVCodecContext) error {
	t.swrCtx = C.swr_alloc_set_opts(nil,
		C.long(encCtx.channel_layout),
		int32(encCtx.sample_fmt),
		C.int(encCtx.sample_rate),
		C.long(decCtx.channel_layout),
		int32(decCtx.sample_fmt),
		C.int(decCtx.sample_rate),
		0,
		nil,
	)
	if t.swrCtx == nil {
		return fmt.Errorf("alloc swr failed")
	}

	if res := C.swr_init(t.swrCtx); res < 0 {
		return fmt.Errorf("init swr failed")
	}

	/* Allocate as many pointers as there are audio channels.
	 * Each pointer will later point to the audio samples of the corresponding
	 * channels (although it may be NULL for interleaved formats).
	 */
	t.swrBuf = make([][]byte, encCtx.channels)

	/* Allocate memory for the samples of all channels in one consecutive
	 * block for convenience. */
	res := C.av_samples_alloc((**C.uint8_t)(unsafe.Pointer(&t.swrBuf[0])),
		nil,
		encCtx.channels,
		encCtx.frame_size,
		encCtx.sample_fmt,
		0,
	)
	if res < 0 {
		return fmt.Errorf("alloc swr buffer failed")
	}

	return nil
}

func (t *AudioTranscoder) initFifo() error {
	t.fifo = C.av_audio_fifo_alloc(t.encCtx.sample_fmt, t.encCtx.channels, 1)
	if t.fifo == nil {
		return fmt.Errorf("could not allocate FIFO")
	}

	return nil
}

func (t *AudioTranscoder) Transcode(pts time.Duration, au []byte) ([]AudioPacket, error) {
	if len(au) == 0 {
		return nil, nil
	}
	if t.decCtx == nil || t.encCtx == nil {
		return nil, fmt.Errorf("terminated")
	}

	err := t.decodeAndResample(au, pts.Milliseconds())
	if err != nil {
		return nil, err
	}

	return t.encode()
}

func (t *AudioTranscoder) decodeAndResample(data []byte, pts int64) error {
	t.decPacket.data = (*C.uint8_t)(unsafe.Pointer(&data[0]))
	t.decPacket.size = C.int(len(data))

	res := C.avcodec_send_packet(t.decCtx, t.decPacket)
	if res < 0 {
		return fmt.Errorf("submit to decoder failed %d", res)
	}

	t.newPktPts = pts
	for {
		res = C.avcodec_receive_frame(t.decCtx, t.decFrame)
		if res == -11 {
			return nil
		}
		if res < 0 {
			return fmt.Errorf("decoding error %d", res)
		}

		if t.decCtx.channel_layout == 0 {
			if t.decFrame.channel_layout != 0 {
				t.decCtx.channel_layout = t.decFrame.channel_layout
			} else if t.decFrame.channels > 0 {
				t.decCtx.channel_layout = C.ulong(C.av_get_default_channel_layout(t.decFrame.channels))
			}
		}

		// Decoder is OK now, try to init swr if not initialized.
		if t.swrCtx == nil {
			if err := t.initSwr(t.decCtx, t.encCtx); err != nil {
				return fmt.Errorf("resample init error %d", res)
			}
		}

		inSamples := t.decFrame.nb_samples
		inData := t.decFrame.extended_data

		for {
			/* Convert the samples using the resampler. */
			frameSize := C.swr_convert(t.swrCtx,
				(**C.uint8_t)(unsafe.Pointer(&t.swrBuf[0])),
				t.encCtx.frame_size,
				inData,
				inSamples)
			if frameSize < 0 {
				return fmt.Errorf("Could not convert input samples")
			}

			inData = nil
			inSamples = 0
			if err := t.addSamplesToFifo(t.swrBuf, int(frameSize)); err != nil {
				return err
			}

			res := C.swr_get_out_samples(t.swrCtx, inSamples)
			if res < t.encCtx.frame_size {
				break
			}
		}
	}
}

func (t *AudioTranscoder) encode() ([]AudioPacket, error) {
	if t.nextOutPts == 0 {
		t.nextOutPts = t.newPktPts * 1000
	} else {
		if t.newPktPts-t.nextOutPts/1000 > 1000 {
			t.nextOutPts = t.newPktPts * 1000
		}
	}

	pkts := make([]AudioPacket, 0)

	frameCount := 0
	for {
		if C.av_audio_fifo_size(t.fifo) < t.encCtx.frame_size {
			break
		}

		/* Read as many samples from the FIFO buffer as required to fill the frame.
		 * The samples are stored in the frame temporarily. */
		res := C.av_audio_fifo_read(t.fifo, (*unsafe.Pointer)(unsafe.Pointer(&t.encFrame.data[0])), t.encCtx.frame_size)
		if res < t.encCtx.frame_size {
			return pkts, fmt.Errorf("Could not read data from FIFO")
		}
		/* send the frame for encoding */
		//enc_frame_->pts = (next_out_pts_ + av_rescale(enc_->frame_size * frame_cnt, 1000 * 1000, enc_->sample_rate))/1000;
		t.encFrame.pts = (C.long(t.nextOutPts) + C.av_rescale(C.long(t.encCtx.frame_size)*C.long(frameCount), 1000*1000, C.long(t.encCtx.sample_rate))) / 1000
		frameCount++
		res = C.avcodec_send_frame(t.encCtx, t.encFrame)
		if res < 0 {
			return pkts, fmt.Errorf("Error sending the frame to the encoder")
		}

		C.av_init_packet(t.encPacket)
		t.encPacket.data = nil
		t.encPacket.size = 0
		/* read all the available output packets (in general there may be any
		 * number of them */
		for {
			res = C.avcodec_receive_packet(t.encCtx, t.encPacket)
			if res == -11 {
				break
			}
			if res < 0 {
				return pkts, fmt.Errorf("Error during encoding %d", res)
			}

			pkt := AudioPacket{
				PTS: int64(t.encPacket.pts),
				DTS: int64(t.encPacket.dts),
				Buf: C.GoBytes(unsafe.Pointer(t.encPacket.data), C.int(t.encPacket.size)),
			}

			pkts = append(pkts, pkt)
		}
	}

	t.nextOutPts += int64(C.av_rescale(C.long(t.encCtx.frame_size)*C.long(frameCount), 1000*1000, C.long(t.encCtx.sample_rate)))

	return pkts, nil
}

func (t *AudioTranscoder) addSamplesToFifo(samples [][]byte, frameSize int) error {
	/* Make the FIFO as large as it needs to be to hold both,
	 * the old and the new samples. */
	res := C.av_audio_fifo_realloc(t.fifo, C.av_audio_fifo_size(t.fifo)+C.int(frameSize))
	if res < 0 {
		return fmt.Errorf("Could not reallocate FIFO")
	}

	/* Store the new samples in the FIFO buffer. */
	res = C.av_audio_fifo_write(t.fifo, (*unsafe.Pointer)(unsafe.Pointer(&t.swrBuf[0])), C.int(frameSize))
	if res < 0 {
		return fmt.Errorf("Could not write data to FIFO")
	}

	return nil
}
