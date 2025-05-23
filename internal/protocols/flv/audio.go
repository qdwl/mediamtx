package flv

import (
	"time"
)

// audio codecs
const (
	CodecMPEG1Audio = 2
	CodecLPCM       = 3
	CodecPCMA       = 7
	CodecPCMU       = 8
	CodecMPEG4Audio = 10
)

// audio rates
const (
	Rate5512  = 0
	Rate11025 = 1
	Rate22050 = 2
	Rate44100 = 3
)

// audio depths
const (
	Depth8  = 0
	Depth16 = 1
)

// AudioAACType is the AAC type of a Audio.
type AudioAACType uint8

// AudioAACType values.
const (
	AudioAACTypeConfig AudioAACType = 0
	AudioAACTypeAU     AudioAACType = 1
)

// Audio is an audio message.
type Audio struct {
	DTS      time.Duration
	Codec    uint8
	Rate     uint8
	Depth    uint8
	IsStereo bool
	AACType  AudioAACType // only for CodecMPEG4Audio
	Payload  []byte
}

func (m Audio) marshalBodySize() int {
	var l int
	if m.Codec == CodecMPEG1Audio {
		l = 1 + len(m.Payload)
	} else {
		l = 2 + len(m.Payload)
	}
	return l
}

func (m Audio) marshal() (*Tag, error) {
	body := make([]byte, m.marshalBodySize())

	body[0] = m.Codec<<4 | m.Rate<<2 | m.Depth<<1

	if m.IsStereo {
		body[0] |= 1
	}

	if m.Codec == CodecMPEG4Audio {
		body[1] = uint8(m.AACType)
		copy(body[2:], m.Payload)
	} else {
		copy(body[1:], m.Payload)
	}

	return &Tag{
		DTS:  m.DTS,
		Type: TagTypeAudio,
		Data: body,
	}, nil
}
