package transport

import (
	"fmt"
	"io"
	"net"
	"time"

	"github.com/bluenviron/mediamtx/internal/restrictnetwork"
	"github.com/pion/rtp"
)

type TcpClient struct {
	conn         net.Conn
	writeTimeout time.Duration
	reader       PacketProcessor

	done chan struct{}
}

func NewTcpClient(
	reader PacketProcessor,
	localAddr string,
	remoteAddr string,
) (*TcpClient, error) {
	laddr, err := net.ResolveTCPAddr(restrictnetwork.Restrict("tcp", localAddr))
	if err != nil {
		fmt.Println("ResolveTCPAddr failed:", err)
		return nil, fmt.Errorf("remote address fmt error")
	}

	raddr, err := net.ResolveTCPAddr("tcp", remoteAddr)
	if err != nil {
		fmt.Println("ResolveTCPAddr failed:", err)
		return nil, fmt.Errorf("remote address fmt error")
	}

	conn, err := net.DialTCP("tcp", laddr, raddr)
	if err != nil {
		return nil, fmt.Errorf("dail tcp server %s failed", remoteAddr)
	}

	err = conn.SetReadBuffer(kernelReadBufferSize)
	if err != nil {
		return nil, err
	}

	c := &TcpClient{
		conn:         conn,
		writeTimeout: 10 * time.Second,
		reader:       reader,
		done:         make(chan struct{}),
	}

	go c.runReader()

	return c, nil
}

func (c *TcpClient) Close() {
	c.conn.Close()
	<-c.done
}

func (c *TcpClient) runReader() {
	defer close(c.done)
	defer c.conn.Close()

	for {
		lengthBytes := make([]byte, 2)
		_, err := io.ReadFull(c.conn, lengthBytes)
		if err != nil {
			break
		}

		length := int(lengthBytes[0])<<8 | int(lengthBytes[1])

		buf := make([]byte, length)
		_, err = io.ReadFull(c.conn, buf)
		if err != nil {
			return
		}

		func() {
			pkt := &rtp.Packet{}
			err := pkt.Unmarshal(buf)
			if err != nil {
				return
			}

			c.reader.ProcessRtpPacket(pkt)
		}()
	}
}

func (c *TcpClient) Write(buf []byte) error {
	if c.conn != nil {
		c.conn.SetWriteDeadline(time.Now().Add(c.writeTimeout))

		length := len(buf)
		lengthBytes := []byte{byte(length >> 8), byte(length & 0xFF)}
		_, err := c.conn.Write(lengthBytes)
		if err != nil {
			return err
		}

		_, err = c.conn.Write(buf)
		if err != nil {
			return err
		}
	}

	return nil
}
