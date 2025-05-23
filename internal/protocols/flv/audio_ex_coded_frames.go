package flv

import (
	"time"

	"github.com/bluenviron/mediamtx/internal/protocols/rtmp/message"
)

// AudioExCodedFrames is a CodedFrames extended message.
type AudioExCodedFrames struct {
	DTS     time.Duration
	FourCC  message.FourCC
	Payload []byte
}

func (m AudioExCodedFrames) marshalBodySize() int {
	return 5 + len(m.Payload)
}

func (m AudioExCodedFrames) marshal() (*Tag, error) {
	body := make([]byte, m.marshalBodySize())

	body[0] = (9 << 4) | byte(message.AudioExTypeCodedFrames)
	body[1] = uint8(m.FourCC >> 24)
	body[2] = uint8(m.FourCC >> 16)
	body[3] = uint8(m.FourCC >> 8)
	body[4] = uint8(m.FourCC)
	copy(body[5:], m.Payload)

	return &Tag{
		DTS:  m.DTS,
		Type: TypeFlagsAudio,
		Data: body,
	}, nil
}
