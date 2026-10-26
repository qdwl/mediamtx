package flv

import (
	"testing"

	"github.com/bluenviron/mediamtx/internal/protocols/rtmp/message"
	"github.com/stretchr/testify/require"
)

func TestVideoExFramesXFrameType(t *testing.T) {
	for _, ca := range []struct {
		name       string
		isKeyFrame bool
		expected   byte
	}{
		{"key frame", true, 0x93},    // 1 (ex header) | 1 (key frame) << 4 | 3 (CodedFramesX)
		{"inter frame", false, 0xa3}, /* 1 (ex header) | 2 (inter frame) << 4 | 3 (CodedFramesX) */
	} {
		t.Run(ca.name, func(t *testing.T) {
			tag, err := VideoExFramesX{
				FourCC:     message.FourCCHEVC,
				IsKeyFrame: ca.isKeyFrame,
				Payload:    []byte{0x00, 0x00, 0x00, 0x01},
			}.marshal()
			require.NoError(t, err)
			require.Equal(t, []byte{ca.expected, 'h', 'v', 'c', '1', 0x00, 0x00, 0x00, 0x01}, tag.Data)
		})
	}
}

func TestVideoExCodedFramesFrameType(t *testing.T) {
	for _, ca := range []struct {
		name       string
		isKeyFrame bool
		expected   byte
	}{
		{"key frame", true, 0x91},    // 1 (ex header) | 1 (key frame) << 4 | 1 (CodedFrames)
		{"inter frame", false, 0xa1}, /* 1 (ex header) | 2 (inter frame) << 4 | 1 (CodedFrames) */
	} {
		t.Run(ca.name, func(t *testing.T) {
			tag, err := VideoExCodedFrames{
				FourCC:     message.FourCCHEVC,
				IsKeyFrame: ca.isKeyFrame,
				Payload:    []byte{0x00, 0x00, 0x00, 0x01},
			}.marshal()
			require.NoError(t, err)
			require.Equal(t, []byte{ca.expected, 'h', 'v', 'c', '1', 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01}, tag.Data)
		})
	}
}
