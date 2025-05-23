package flv

import (
	"time"
)

const (
	// VideoChunkStreamID is the chunk stream ID that is usually used to send Video{}
	VideoChunkStreamID = 6
)

// supported video codecs
const (
	CodecH264 = 7
)

// VideoType is the type of a video message.
type VideoType uint8

// VideoType values.
const (
	VideoTypeConfig VideoType = 0
	VideoTypeAU     VideoType = 1
	VideoTypeEOS    VideoType = 2
)

// Video is a video tag.
type Video struct {
	DTS        time.Duration
	Codec      uint8
	IsKeyFrame bool
	Type       VideoType
	PTSDelta   time.Duration
	Payload    []byte
}

func (m Video) marshalBodySize() int {
	return 5 + len(m.Payload)
}

func (m Video) marshal() (*Tag, error) {
	body := make([]byte, m.marshalBodySize())

	if m.IsKeyFrame {
		body[0] = 1 << 4
	} else {
		body[0] = 2 << 4
	}
	body[0] |= m.Codec
	body[1] = uint8(m.Type)

	tmp := uint32(m.PTSDelta / time.Millisecond)
	body[2] = uint8(tmp >> 16)
	body[3] = uint8(tmp >> 8)
	body[4] = uint8(tmp)

	copy(body[5:], m.Payload)

	return &Tag{
		Type: TagTypeVideo,
		DTS:  m.DTS,
		Data: body,
	}, nil
}
