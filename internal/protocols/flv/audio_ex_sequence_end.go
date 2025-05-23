package flv

import (
	"time"

	"github.com/bluenviron/mediamtx/internal/protocols/rtmp/message"
)

// AudioExSequenceEnd is a sequence end extended message.
type AudioExSequenceEnd struct {
	DTS    time.Duration
	FourCC message.FourCC
}

func (m AudioExSequenceEnd) marshal() (*Tag, error) {
	body := make([]byte, 5)

	body[0] = (9 << 4) | byte(message.AudioExTypeSequenceEnd)
	body[1] = uint8(m.FourCC >> 24)
	body[2] = uint8(m.FourCC >> 16)
	body[3] = uint8(m.FourCC >> 8)
	body[4] = uint8(m.FourCC)

	return &Tag{
		Type: TagTypeAudio,
		DTS:  m.DTS,
		Data: body,
	}, nil
}
