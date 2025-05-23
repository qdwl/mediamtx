package flv

import (
	"time"

	"github.com/bluenviron/mediamtx/internal/protocols/rtmp/message"
)

// VideoExCodedFrames is a CodedFrames extended message.
type VideoExCodedFrames struct {
	DTS      time.Duration
	FourCC   message.FourCC
	PTSDelta time.Duration
	Payload  []byte
}

func (m VideoExCodedFrames) marshalBodySize() int {
	switch m.FourCC {
	case message.FourCCAVC, message.FourCCHEVC:
		return 8 + len(m.Payload)
	}
	return 5 + len(m.Payload)
}

func (m VideoExCodedFrames) marshal() (*Tag, error) {
	body := make([]byte, m.marshalBodySize())

	body[0] = 0b10000000 | byte(message.VideoExTypeCodedFrames)
	body[1] = uint8(m.FourCC >> 24)
	body[2] = uint8(m.FourCC >> 16)
	body[3] = uint8(m.FourCC >> 8)
	body[4] = uint8(m.FourCC)

	if m.FourCC == message.FourCCHEVC {
		tmp := uint32(m.PTSDelta / time.Millisecond)
		body[5] = uint8(tmp >> 16)
		body[6] = uint8(tmp >> 8)
		body[7] = uint8(tmp)
		copy(body[8:], m.Payload)
	} else {
		copy(body[5:], m.Payload)
	}

	return &Tag{
		DTS:  m.DTS,
		Type: TagTypeVideo,
		Data: body,
	}, nil
}
