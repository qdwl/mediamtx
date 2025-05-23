package flv

import (
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/bluenviron/mediamtx/internal/protocols/rtmp/message"
)

// AudioExSequenceStart is a sequence start extended message.
type AudioExSequenceStart struct {
	DTS       time.Duration
	FourCC    message.FourCC
	AACHeader *mpeg4audio.AudioSpecificConfig
}

func (m AudioExSequenceStart) marshal() (*Tag, error) {
	var addBody []byte

	switch m.FourCC {
	case message.FourCCMP4A:
		buf, err := m.AACHeader.Marshal()
		if err != nil {
			return nil, err
		}
		addBody = buf
	}

	body := make([]byte, 5+len(addBody))

	body[0] = (9 << 4) | byte(message.AudioExTypeSequenceStart)
	body[1] = uint8(m.FourCC >> 24)
	body[2] = uint8(m.FourCC >> 16)
	body[3] = uint8(m.FourCC >> 8)
	body[4] = uint8(m.FourCC)
	copy(body[5:], addBody)

	return &Tag{
		Type: TagTypeAudio,
		DTS:  m.DTS,
		Data: body,
	}, nil
}
