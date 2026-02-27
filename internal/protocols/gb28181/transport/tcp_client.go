package transport

import (
	"errors"
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
		return nil, fmt.Errorf("remote address fmt error")
	}

	raddr, err := net.ResolveTCPAddr("tcp", remoteAddr)
	if err != nil {
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
	if c.conn != nil {
		c.conn.Close()
	}
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
	if c.conn == nil {
		return errors.New("connection is nil")
	}

	// 设置超时（可选）
	c.conn.SetWriteDeadline(time.Now().Add(c.writeTimeout))

	// 大端序写入长度前缀（2字节）
	length := len(buf)
	if length > 0xFFFF {
		return errors.New("data too large")
	}
	lengthBytes := []byte{byte(length >> 8), byte(length & 0xFF)}

	// 合并写入（减少系统调用）
	data := append(lengthBytes, buf...)
	_, err := c.conn.Write(data) // 或 io.Copy
	return err
}
