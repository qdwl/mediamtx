package flv

// #cgo pkg-config: libavcodec libavutil libswscale libswresample
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
	pts int64
	dts int64
	buf []uint8
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
				pts: int64(t.encPacket.pts),
				dts: int64(t.encPacket.dts),
				buf: C.GoBytes(unsafe.Pointer(t.encPacket.data), C.int(t.encPacket.size)),
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
