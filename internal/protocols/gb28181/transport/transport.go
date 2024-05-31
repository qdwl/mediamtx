package transport

import (
	"github.com/pion/rtp"
)

type PacketProcessor interface {
	ProcessRtpPacket(pkt *rtp.Packet)
}

type Transport interface {
	Close()
	Write(buf []byte) error
}

const (
	// same size as GStreamer's rtspsrc
	kernelReadBufferSize = 0x80000

	// 1500 (UDP MTU) - 20 (IP header) - 8 (UDP header)
	maxPacketSize = 1472
)
