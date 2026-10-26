package gb28181

import (
	"bytes"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
)

// maxNALUsPerAccessUnit prevents an unbounded growth of the access unit
// in case the stream doesn't contain valid access unit boundaries.
const maxNALUsPerAccessUnit = 512

// auAggregator groups the NALUs emitted by the PS demuxer, which outputs one
// NALU at a time, into complete access units.
// Readers (FLV, HLS, WebRTC, RTSP) expect one unit to contain a whole picture;
// a picture split into multiple slices must not be sent as multiple units,
// otherwise only the first slice is decoded and the rest of the picture is
// rendered as green.
type auAggregator struct {
	h265         bool
	onAccessUnit func(pts time.Duration, au h264.AnnexB)

	au       h264.AnnexB
	auPTS    time.Duration
	hasFirst bool
}

func (a *auAggregator) push(nalu []byte, pts time.Duration) {
	if len(nalu) < 2 {
		return
	}

	typ := naluType(nalu, a.h265)
	isVCL := isVCL(typ, a.h265)

	if isVCL {
		firstSlice := isFirstSlice(nalu, typ, a.h265)

		if firstSlice {
			// parameter sets received so far belong to the new picture
			if a.hasFirst && len(a.au) != 0 {
				a.emit()
			}
		} else if !a.hasFirst {
			// the first slice of this picture has not been received (i.e. the
			// reader joined in the middle of a picture): drop it, otherwise a
			// truncated picture is sent downstream.
			a.au = a.au[:0]
			return
		}
	} else if isAccessUnitDelimiter(typ, a.h265) && a.hasFirst {
		a.emit()
	}

	if len(a.au) == 0 {
		a.auPTS = pts
		a.hasFirst = false
	}

	if isParameterSet(typ, a.h265) && containsNALU(a.au, nalu) {
		return
	}

	a.au = append(a.au, append([]byte(nil), nalu...))

	if isVCL {
		a.hasFirst = true
	}

	if len(a.au) >= maxNALUsPerAccessUnit {
		a.emit()
	}
}

func (a *auAggregator) emit() {
	if len(a.au) == 0 {
		return
	}

	au := a.au
	pts := a.auPTS

	// allocations are not reused, so that the emitted access unit is not
	// modified by subsequent writes.
	a.au = nil
	a.hasFirst = false

	a.onAccessUnit(pts, au)
}

func naluType(nalu []byte, h265 bool) uint8 {
	if h265 {
		return (nalu[0] >> 1) & 0b111111
	}
	return nalu[0] & 0b11111
}

func isVCL(typ uint8, h265 bool) bool {
	if h265 {
		return typ <= 31
	}
	return typ >= 1 && typ <= 5
}

func isParameterSet(typ uint8, h265 bool) bool {
	if h265 {
		return typ == 32 || typ == 33 || typ == 34
	}
	return typ == 7 || typ == 8
}

func isAccessUnitDelimiter(typ uint8, h265 bool) bool {
	if h265 {
		return typ == 35
	}
	return typ == 9
}

func isFirstSlice(nalu []byte, typ uint8, h265 bool) bool {
	if h265 {
		// first_slice_segment_in_pic_flag
		return len(nalu) >= 3 && nalu[2]&0b10000000 != 0
	}

	switch typ {
	case 1, 2, 5:
		// first_mb_in_slice
		firstMB, ok := readUE(nalu, 8)
		return ok && firstMB == 0
	}

	return false
}

func containsNALU(au h264.AnnexB, nalu []byte) bool {
	for _, other := range au {
		if bytes.Equal(other, nalu) {
			return true
		}
	}
	return false
}

// readUE reads an unsigned exp-golomb value starting at the given bit position.
func readUE(byts []byte, bitPos int) (uint64, bool) {
	readBit := func() (uint64, bool) {
		if bitPos >= len(byts)*8 {
			return 0, false
		}
		v := uint64((byts[bitPos/8] >> uint(7-bitPos%8)) & 1)
		bitPos++
		return v, true
	}

	zeros := 0
	for {
		v, ok := readBit()
		if !ok {
			return 0, false
		}
		if v == 1 {
			break
		}
		zeros++
		if zeros > 31 {
			return 0, false
		}
	}

	var val uint64
	for i := 0; i < zeros; i++ {
		v, ok := readBit()
		if !ok {
			return 0, false
		}
		val = (val << 1) | v
	}

	return (uint64(1) << uint(zeros)) - 1 + val, true
}
