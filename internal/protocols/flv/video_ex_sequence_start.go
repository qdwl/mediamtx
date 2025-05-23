package flv

import (
	"bytes"
	"time"

	"github.com/abema/go-mp4"

	"github.com/bluenviron/mediamtx/internal/protocols/rtmp/message"
)

// VideoExSequenceStart is a sequence start extended message.
type VideoExSequenceStart struct {
	DTS        time.Duration
	FourCC     message.FourCC
	AV1Header  *mp4.Av1C
	VP9Header  *mp4.VpcC
	HEVCHeader *mp4.HvcC
	AVCHeader  *mp4.AVCDecoderConfiguration
}

func (m VideoExSequenceStart) marshal() (*Tag, error) {
	var addBody []byte

	switch m.FourCC {
	case message.FourCCAV1:
		var buf bytes.Buffer
		_, err := mp4.Marshal(&buf, m.AV1Header, mp4.Context{})
		if err != nil {
			return nil, err
		}
		addBody = buf.Bytes()

	case message.FourCCVP9:
		var buf bytes.Buffer
		_, err := mp4.Marshal(&buf, m.VP9Header, mp4.Context{})
		if err != nil {
			return nil, err
		}
		addBody = buf.Bytes()

	case message.FourCCHEVC:
		var buf bytes.Buffer
		_, err := mp4.Marshal(&buf, m.HEVCHeader, mp4.Context{})
		if err != nil {
			return nil, err
		}
		addBody = buf.Bytes()

	case message.FourCCAVC:
		var buf bytes.Buffer
		_, err := mp4.Marshal(&buf, m.AVCHeader, mp4.Context{})
		if err != nil {
			return nil, err
		}
		addBody = buf.Bytes()
	}

	body := make([]byte, 5+len(addBody))

	body[0] = 0b10000000 | byte(message.VideoExTypeSequenceStart)
	body[1] = uint8(m.FourCC >> 24)
	body[2] = uint8(m.FourCC >> 16)
	body[3] = uint8(m.FourCC >> 8)
	body[4] = uint8(m.FourCC)
	copy(body[5:], addBody)

	return &Tag{
		Type: TagTypeVideo,
		Data: body,
	}, nil
}
