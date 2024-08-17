package gb28181

import (
	"crypto/rand"

	"github.com/pion/rtp"
)

const (
	rtpVersion            = 2
	defaultPayloadMaxSize = 1460 // 1500 (UDP MTU) - 20 (IP header) - 8 (UDP header) - 12 (RTP header)
)

func randUint32() (uint32, error) {
	var b [4]byte
	_, err := rand.Read(b[:])
	if err != nil {
		return 0, err
	}
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]), nil
}

func packetCount(avail, le int) int {
	n := le / avail
	if (le % avail) != 0 {
		n++
	}
	return n
}

// Encoder is a RTP/MPEG-PS encoder.
// Specification: https://datatracker.ietf.org/doc/html/rfc6184
type RtpPacketizer struct {
	// payload type of packets.
	PayloadType uint8

	// SSRC of packets (optional).
	// It defaults to a random value.
	SSRC *uint32

	// initial sequence number of packets (optional).
	// It defaults to a random value.
	InitialSequenceNumber *uint16

	// maximum size of packet payloads (optional).
	// It defaults to 1460.
	PayloadMaxSize int

	sequenceNumber uint16
}

// Init initializes the encoder.
func (e *RtpPacketizer) Init() error {
	if e.SSRC == nil {
		v, err := randUint32()
		if err != nil {
			return err
		}
		e.SSRC = &v
	}
	if e.InitialSequenceNumber == nil {
		v, err := randUint32()
		if err != nil {
			return err
		}
		v2 := uint16(v)
		e.InitialSequenceNumber = &v2
	}
	if e.PayloadMaxSize == 0 {
		e.PayloadMaxSize = defaultPayloadMaxSize
	}

	e.sequenceNumber = *e.InitialSequenceNumber
	return nil
}

// Encode encodes an access unit into RTP/H264 packets.
func (e *RtpPacketizer) Encode(nalu []byte, ts uint32) ([]*rtp.Packet, error) {
	var rets []*rtp.Packet
	packetCount := packetCount(e.PayloadMaxSize, len(nalu))

	rets = make([]*rtp.Packet, packetCount)

	le := e.PayloadMaxSize
	for i := range rets {
		if len(nalu) < le {
			le = len(nalu)
		}

		data := make([]byte, le)
		copy(data, nalu)
		nalu = nalu[le:]

		rets[i] = &rtp.Packet{
			Header: rtp.Header{
				Version:        rtpVersion,
				PayloadType:    e.PayloadType,
				SequenceNumber: e.sequenceNumber,
				SSRC:           *e.SSRC,
				Marker:         i == (packetCount - 1),
				Timestamp:      ts,
			},
			Payload: data,
		}

		e.sequenceNumber++
	}

	return rets, nil
}
