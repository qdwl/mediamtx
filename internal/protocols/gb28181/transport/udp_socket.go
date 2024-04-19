package transport

import (
	"net"
	"time"

	"github.com/pion/rtp"
)

type UdpSocket struct {
	pc           *net.UDPConn
	listenIP     net.IP
	writeTimeout time.Duration
	reader       PacketProcessor

	done chan struct{}
}

func NewUdpSocket(
	reader PacketProcessor,
	address string,
) (*UdpSocket, error) {
	var pc *net.UDPConn
	var listenIP net.IP

	tmp, err := net.ListenPacket("udp", address)
	if err != nil {
		return nil, err
	}

	pc = tmp.(*net.UDPConn)
	listenIP = tmp.LocalAddr().(*net.UDPAddr).IP

	err = pc.SetReadBuffer(udpKernelReadBufferSize)
	if err != nil {
		return nil, err
	}

	u := &UdpSocket{
		pc:           pc,
		listenIP:     listenIP,
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

func (u *UdpSocket) IP() net.IP {
	return u.listenIP
}

func (u *UdpSocket) Port() int {
	return u.pc.LocalAddr().(*net.UDPAddr).Port
}

func (u *UdpSocket) runReader() {
	defer close(u.done)

	for {
		buf := make([]byte, maxPacketSize)
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

func (u *UdpSocket) Write(buf []byte, addr *net.UDPAddr) error {
	// no mutex is needed here since Write() has an internal lock.
	// https://github.com/golang/go/issues/27203#issuecomment-534386117
	u.pc.SetWriteDeadline(time.Now().Add(u.writeTimeout))
	_, err := u.pc.WriteTo(buf, addr)
	return err
}
