package flv

import (
	"time"

	"github.com/bluenviron/mediamtx/internal/protocols/rtmp/message"
)

// VideoExFramesX is a FramesX extended message.
type VideoExFramesX struct {
	DTS     time.Duration
	FourCC  message.FourCC
	Payload []byte
}

func (m VideoExFramesX) marshalBodySize() int {
	return 5 + len(m.Payload)
}

func (m VideoExFramesX) marshal() (*Tag, error) {
	body := make([]byte, m.marshalBodySize())

	body[0] = 0b10000000 | byte(message.VideoExTypeFramesX)
	body[1] = uint8(m.FourCC >> 24)
	body[2] = uint8(m.FourCC >> 16)
	body[3] = uint8(m.FourCC >> 8)
	body[4] = uint8(m.FourCC)
	copy(body[5:], m.Payload)

	return &Tag{
		DTS:  m.DTS,
		Type: TagTypeVideo,
		Data: body,
	}, nil
}
