package flv

import (
	"time"

	"github.com/bluenviron/mediamtx/internal/protocols/rtmp/amf0"
	"github.com/bluenviron/mediamtx/internal/protocols/rtmp/message"
)

// VideoExMetadata is a metadata extended message.
type VideoExMetadata struct {
	DTS     time.Duration
	FourCC  message.FourCC
	Payload amf0.Data
}

func (m VideoExMetadata) marshalBodySize() (int, error) {
	ms, err := m.Payload.MarshalSize()
	if err != nil {
		return 0, err
	}
	return 5 + ms, nil
}

func (m VideoExMetadata) marshal() (*Tag, error) {
	mbs, err := m.marshalBodySize()
	if err != nil {
		return nil, err
	}
	body := make([]byte, mbs)

	body[0] = 0b10000000 | byte(message.VideoExTypeMetadata)
	body[1] = uint8(m.FourCC >> 24)
	body[2] = uint8(m.FourCC >> 16)
	body[3] = uint8(m.FourCC >> 8)
	body[4] = uint8(m.FourCC)

	_, err = m.Payload.MarshalTo(body[5:])
	if err != nil {
		return nil, err
	}

	return &Tag{
		DTS:  m.DTS,
		Type: TagTypeVideo,
		Data: body,
	}, nil
}
