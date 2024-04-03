package mpegps

import "github.com/bluenviron/mediacommon/pkg/codecs/mpeg4audio"

// Codec is a MPEG-TS codec.
type Codec interface {
	isCodec()
}

// CodecH264 is a H264 codec.
type CodecH264 struct{}

func (*CodecH264) isCodec() {
}

// CodecH265 is a H265 codec.
type CodecH265 struct{}

func (*CodecH265) isCodec() {
}

// CodecMPEG4Audio is a MPEG4-Audio codec.
type CodecMPEG4Audio struct {
	mpeg4audio.Config
}

func (*CodecMPEG4Audio) isCodec() {
}

// CodecOpus is a Opus codec.
type CodecOpus struct {
	Channels int
}

func (*CodecOpus) isCodec() {
}

// CodecG711A is a G711A codec.
type CodecG711A struct{}

func (*CodecG711A) isCodec() {
}

// CodecG711U is a G711U codec.
type CodecG711U struct{}

func (*CodecG711U) isCodec() {
}
