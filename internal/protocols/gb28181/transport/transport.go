package transport

import (
	"net"

	"github.com/pion/rtp"
)

type PacketProcessor interface {
	ProcessRtpPacket(pkt *rtp.Packet)
}

type Transport interface {
	Close()
	IP() net.IP
	Port() int
	Write(buf []byte, addr *net.UDPAddr) error
}

const (
	// same size as GStreamer's rtspsrc
	udpKernelReadBufferSize = 0x80000

	// 1500 (UDP MTU) - 20 (IP header) - 8 (UDP header)
	maxPacketSize = 1472
)
