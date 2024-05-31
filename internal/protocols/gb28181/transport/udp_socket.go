package transport

import (
	"fmt"
	"net"
	"time"

	"github.com/bluenviron/mediamtx/internal/restrictnetwork"
	"github.com/pion/rtp"
)

type UdpSocket struct {
	pc           *net.UDPConn
	remoteAddr   *net.UDPAddr
	writeTimeout time.Duration
	reader       PacketProcessor

	done chan struct{}
}

func NewUdpSocket(
	reader PacketProcessor,
	localAddr string,
	remoteAddr string,
) (*UdpSocket, error) {
	var pc *net.UDPConn

	addr, err := net.ResolveUDPAddr("udp", remoteAddr)
	if err != nil {
		fmt.Println("ResolveUDPAddr failed:", err)
		return nil, fmt.Errorf("remote address fmt error")
	}

	tmp, err := net.ListenPacket(restrictnetwork.Restrict("udp", localAddr))
	if err != nil {
		return nil, fmt.Errorf("listen udp server %s failed", localAddr)
	}

	pc = tmp.(*net.UDPConn)
	err = pc.SetReadBuffer(kernelReadBufferSize)
	if err != nil {
		return nil, err
	}

	u := &UdpSocket{
		pc:           pc,
		remoteAddr:   addr,
		writeTimeout: 10 * time.Second,
		reader:       reader,
		done:         make(chan struct{}),
	}

	go u.runReader()

	return u, nil
}

func (u *UdpSocket) Close() {
	u.pc.Close()
	<-u.done
}

func (u *UdpSocket) runReader() {
	defer close(u.done)

	buf := make([]byte, maxPacketSize)
	for {
		n, _, err := u.pc.ReadFromUDP(buf)
		if err != nil {
			break
		}

		func() {
			pkt := &rtp.Packet{}
			err := pkt.Unmarshal(buf[:n])
			if err != nil {
				return
			}

			u.reader.ProcessRtpPacket(pkt)
		}()
	}
}

func (u *UdpSocket) Write(buf []byte) error {
	// no mutex is needed here since Write() has an internal lock.
	// https://github.com/golang/go/issues/27203#issuecomment-534386117
	u.pc.SetWriteDeadline(time.Now().Add(u.writeTimeout))
	_, err := u.pc.WriteTo(buf, u.remoteAddr)
	return err
}
