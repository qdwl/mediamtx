package gb28181

import (
	"testing"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/stretchr/testify/require"
)

func TestAUAggregatorH265(t *testing.T) {
	vps := []byte{0x40, 0x01, 0x0c}
	sps := []byte{0x42, 0x01, 0x01}
	pps := []byte{0x44, 0x01, 0xc0}
	slice1 := []byte{0x26, 0x01, 0xaf, 0x11}
	slice2 := []byte{0x26, 0x01, 0x2a, 0x22}

	var aus []h264.AnnexB
	var ausPTS []time.Duration

	agg := &auAggregator{
		h265: true,
		onAccessUnit: func(pts time.Duration, au h264.AnnexB) {
			aus = append(aus, au)
			ausPTS = append(ausPTS, pts)
		},
	}

	// following slices of a picture started before this reader are dropped
	agg.push(slice2, 0*time.Millisecond)
	require.Len(t, aus, 0)

	// first picture, split in two slices with different timestamps
	agg.push(vps, 0*time.Millisecond)
	agg.push(sps, 0*time.Millisecond)
	agg.push(pps, 0*time.Millisecond)
	agg.push(slice1, 0*time.Millisecond)
	agg.push(slice2, 40*time.Millisecond)
	require.Len(t, aus, 0)

	// second picture
	agg.push(slice1, 40*time.Millisecond)
	agg.push(slice2, 80*time.Millisecond)
	require.Len(t, aus, 1)
	require.Equal(t, 0*time.Millisecond, ausPTS[0])
	require.Equal(t, h264.AnnexB{vps, sps, pps, slice1, slice2}, aus[0])

	// third picture
	agg.push(slice1, 80*time.Millisecond)
	require.Len(t, aus, 2)
	require.Equal(t, 40*time.Millisecond, ausPTS[1])
	require.Equal(t, h264.AnnexB{slice1, slice2}, aus[1])
}

func TestAUAggregatorH265ParameterSetDedup(t *testing.T) {
	vps := []byte{0x40, 0x01, 0x0c}
	slice1 := []byte{0x26, 0x01, 0xaf}
	slice2 := []byte{0x26, 0x01, 0x2a}

	var aus []h264.AnnexB

	agg := &auAggregator{
		h265: true,
		onAccessUnit: func(_ time.Duration, au h264.AnnexB) {
			aus = append(aus, au)
		},
	}

	agg.push(vps, 0)
	agg.push(slice1, 0)
	agg.push(vps, 0)
	agg.push(slice2, 0)
	agg.push(slice1, 40*time.Millisecond)

	require.Len(t, aus, 1)
	require.Equal(t, h264.AnnexB{vps, slice1, slice2}, aus[0])
}

func TestAUAggregatorH265CopiesNALUs(t *testing.T) {
	slice1 := []byte{0x26, 0x01, 0xaf, 0x11}
	slice2 := []byte{0x26, 0x01, 0x2a, 0x22}

	var aus []h264.AnnexB

	agg := &auAggregator{
		h265: true,
		onAccessUnit: func(_ time.Duration, au h264.AnnexB) {
			aus = append(aus, au)
		},
	}

	agg.push(slice1, 0)
	agg.push(slice2, 0)

	// the source buffer is reused by the PS demuxer
	slice1[3] = 0xff

	agg.push([]byte{0x26, 0x01, 0xaf, 0x11}, 40*time.Millisecond)

	require.Len(t, aus, 1)
	require.Equal(t, byte(0x11), aus[0][0][3])
}

func TestAUAggregatorH265AccessUnitDelimiter(t *testing.T) {
	aud := []byte{0x46, 0x01, 0x50}
	slice1 := []byte{0x26, 0x01, 0xaf}

	var aus []h264.AnnexB

	agg := &auAggregator{
		h265: true,
		onAccessUnit: func(_ time.Duration, au h264.AnnexB) {
			aus = append(aus, au)
		},
	}

	agg.push(slice1, 0)
	agg.push(aud, 40*time.Millisecond)

	require.Len(t, aus, 1)
	require.Equal(t, h264.AnnexB{slice1}, aus[0])
}

func TestAUAggregatorH264(t *testing.T) {
	sps := []byte{0x67, 0x42, 0xc0}
	pps := []byte{0x68, 0xce}
	idr := []byte{0x65, 0x88, 0x84}   // first_mb_in_slice = 0
	inter := []byte{0x41, 0x0f, 0x80} // first_mb_in_slice = 30
	nextIDR := []byte{0x65, 0x88, 0x84}

	var aus []h264.AnnexB

	agg := &auAggregator{
		onAccessUnit: func(_ time.Duration, au h264.AnnexB) {
			aus = append(aus, au)
		},
	}

	agg.push(sps, 0)
	agg.push(pps, 0)
	agg.push(idr, 0)
	agg.push(inter, 40*time.Millisecond)
	require.Len(t, aus, 0)

	agg.push(nextIDR, 80*time.Millisecond)
	require.Len(t, aus, 1)
	require.Equal(t, h264.AnnexB{sps, pps, idr, inter}, aus[0])
}
