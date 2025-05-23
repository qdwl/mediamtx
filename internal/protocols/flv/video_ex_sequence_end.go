package flv

import (
	"time"

	"github.com/bluenviron/mediamtx/internal/protocols/rtmp/message"
)

// VideoExSequenceEnd is a sequence end extended message.
type VideoExSequenceEnd struct {
	DTS    time.Duration
	FourCC message.FourCC
}

func (m VideoExSequenceEnd) marshalBodySize() int {
	return 5
}

func (m VideoExSequenceEnd) marshal() (*Tag, error) {
	body := make([]byte, m.marshalBodySize())

	body[0] = 0b10000000 | byte(message.VideoExTypeSequenceEnd)
	body[1] = uint8(m.FourCC >> 24)
	body[2] = uint8(m.FourCC >> 16)
	body[3] = uint8(m.FourCC >> 8)
	body[4] = uint8(m.FourCC)

	return &Tag{
		Type: TagTypeVideo,
		DTS:  m.DTS,
		Data: body,
	}, nil
}
