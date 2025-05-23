package flv

import (
	"time"

	"github.com/bluenviron/mediamtx/internal/protocols/rtmp/message"
)

// AudioExChannelOrder is an audio channel order.
type AudioExChannelOrder uint8

// audio channel orders.
const (
	AudioExChannelOrderUnspecified AudioExChannelOrder = 0
	AudioExChannelOrderNative      AudioExChannelOrder = 1
	AudioExChannelOrderCustom      AudioExChannelOrder = 2
)

// AudioExMultichannelConfig is a multichannel config extended message.
type AudioExMultichannelConfig struct {
	DTS                 time.Duration
	FourCC              message.FourCC
	AudioChannelOrder   AudioExChannelOrder
	ChannelCount        uint8
	AudioChannelMapping uint8  // if AudioChannelOrder == AudioExChannelOrderCustom
	AudioChannelFlags   uint32 // if AudioChannelOrder == AudioExChannelOrderNative
}

func (m AudioExMultichannelConfig) marshal() (*Tag, error) {
	var addBody []byte

	switch m.AudioChannelOrder {
	case AudioExChannelOrderCustom:
		addBody = []byte{m.AudioChannelMapping}

	case AudioExChannelOrderNative:
		addBody = []byte{
			byte(m.AudioChannelFlags >> 24),
			byte(m.AudioChannelFlags >> 16),
			byte(m.AudioChannelFlags >> 8),
			byte(m.AudioChannelFlags),
		}
	}

	body := make([]byte, 7+len(addBody))

	body[0] = (9 << 4) | byte(message.AudioExTypeMultichannelConfig)
	body[1] = uint8(m.FourCC >> 24)
	body[2] = uint8(m.FourCC >> 16)
	body[3] = uint8(m.FourCC >> 8)
	body[4] = uint8(m.FourCC)
	body[5] = uint8(m.AudioChannelOrder)
	body[6] = m.ChannelCount
	copy(body[7:], addBody)

	return &Tag{
		DTS:  m.DTS,
		Type: TagTypeAudio,
		Data: body,
	}, nil
}
