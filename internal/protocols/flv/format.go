package flv

import (
	"time"
)

// TagType is the type of a flv tag.
type TagType uint8

const (
	TagTypeAudio  TagType = 8
	TagTypeVideo  TagType = 9
	TagTypeScript TagType = 18
)

const (
	// TypeFlagsReserved UB[5]
	// TypeFlagsAudio    UB[1] Audio tags are present
	// TypeFlagsReserved UB[1] Must be 0
	// TypeFlagsVideo    UB[1] Video tags are present
	TypeFlagsAudio = 0x4
	TypeFlagsVideo = 0x1
)

const FileHeaderLength = 9
const TagHeaderLength = 11
const TagTrailerLength = 4

// TagData is a data in tag.
type TagData interface {
	marshal() (*Tag, error)
}

type Header struct {
	// Audio tags are present
	AudioFlag bool
	// Video tags are present
	VideoFlag bool
}

func (h Header) Marshal() []byte {
	body := make([]byte, FileHeaderLength)

	body[0] = 'F'
	body[1] = 'L'
	body[2] = 'V'
	body[3] = 0x01

	var flags uint8
	if h.AudioFlag {
		flags |= TypeFlagsAudio
	}
	if h.VideoFlag {
		flags |= TypeFlagsVideo
	}

	body[4] = flags

	// DataOffset: UI32 Offset in bytes from start of file to start of body (that is, size of header)
	// The DataOffset field usually has a value of 9 for FLV version 1.
	body[5] = uint8(FileHeaderLength >> 24)
	body[6] = uint8(FileHeaderLength >> 16)
	body[7] = uint8(FileHeaderLength >> 8)
	body[8] = uint8(FileHeaderLength)

	return body
}

type Tag struct {
	/**
	 * Type of this tag. Values are:
	 * 8: audio
	 * 9: video
	 * 18: script data
	 * all others: reserved
	**/
	Type TagType

	/**
	 * Time in milliseconds at which
	 * the data in this tag applies.
	**/
	DTS time.Duration
	/**
	 * Always 0
	**/
	StreamID uint32

	/**
	 * Body of the tag
	**/
	Data []byte
}

func (m Tag) marshalBodySize() int {
	return TagHeaderLength + len(m.Data)
}

func (m Tag) Marshal() []byte {
	body := make([]byte, m.marshalBodySize())

	body[0] = byte(m.Type)
	dataSize := len(m.Data)
	body[1] = uint8(dataSize >> 16)
	body[2] = uint8(dataSize >> 8)
	body[3] = uint8(dataSize)

	// Timestamp
	ts := uint32(m.DTS / time.Millisecond)
	body[4] = uint8(ts >> 16)
	body[5] = uint8(ts >> 8)
	body[6] = uint8(ts)

	// TimestampExtended
	body[7] = uint8(ts >> 24)

	// StreamID Always 0
	body[8] = 0x00
	body[9] = 0x00
	body[10] = 0x00

	// Data
	copy(body[TagHeaderLength:], m.Data)

	return body
}

func MarshalTagSize(len int) []byte {
	body := make([]byte, TagTrailerLength)
	body[0] = uint8(len >> 24)
	body[1] = uint8(len >> 16)
	body[2] = uint8(len >> 8)
	body[3] = uint8(len)

	return body
}
