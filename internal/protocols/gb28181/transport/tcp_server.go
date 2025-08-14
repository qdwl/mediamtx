package transport

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"time"

	"github.com/bluenviron/mediamtx/internal/restrictnetwork"
	"github.com/pion/rtp"
)

type TcpServer struct {
	ln           net.Listener
	conn         net.Conn
	remoteAddr   *net.TCPAddr
	writeTimeout time.Duration
	reader       PacketProcessor

	done chan struct{}
}

func NewTcpServer(
	reader PacketProcessor,
	localAddr string,
	remoteAddr string,
) (*TcpServer, error) {
	raddr, err := net.ResolveTCPAddr("tcp", remoteAddr)
	if err != nil {
		fmt.Println("ResolveTCPAddr failed:", err)
		return nil, fmt.Errorf("remote address fmt error")
	}

	tmp, err := net.Listen(restrictnetwork.Restrict("tcp", localAddr))
	if err != nil {
		return nil, fmt.Errorf("listen tcp server %s failed", localAddr)
	}

	t := &TcpServer{
		ln:           tmp,
		remoteAddr:   raddr,
		writeTimeout: 10 * time.Second,
		reader:       reader,
		done:         make(chan struct{}),
	}

	go t.runReader()

	return t, nil
}

func (s *TcpServer) Close() {
	log.Printf("close tcp server, s.conn:%p\n", s.conn)
	s.ln.Close()
	if s.conn != nil {
		s.conn.Close()
	}
	<-s.done
}

func (s *TcpServer) runReader() {
	defer log.Println("TcpServer exit")
	defer close(s.done)
	tmp, err := s.ln.Accept()
	if err != nil {
		return
	}
	defer s.ln.Close()

	conn := tmp.(*net.TCPConn)
	err = conn.SetReadBuffer(kernelReadBufferSize)
	if err != nil {
		return
	}

	s.conn = conn
	defer s.conn.Close()

	for {
		lengthBytes := make([]byte, 2)
		_, err := io.ReadFull(conn, lengthBytes)
		if err != nil {
			break
		}

		length := int(lengthBytes[0])<<8 | int(lengthBytes[1])

		buf := make([]byte, length)
		_, err = io.ReadFull(conn, buf)
		if err != nil {
			log.Printf("tcp server read err %+v\n", err)
			return
		}

		func() {
			pkt := &rtp.Packet{}
			err := pkt.Unmarshal(buf)
			if err != nil {
				return
			}

			s.reader.ProcessRtpPacket(pkt)
		}()
	}
}

func (s *TcpServer) Write(buf []byte) error {
	if s.conn == nil {
		return errors.New("connection is nil")
	}

	// 设置超时（可选）
	s.conn.SetWriteDeadline(time.Now().Add(s.writeTimeout))

	// 大端序写入长度前缀（2字节）
	length := len(buf)
	if length > 0xFFFF {
		return errors.New("data too large")
	}
	lengthBytes := []byte{byte(length >> 8), byte(length & 0xFF)}

	// 合并写入（减少系统调用）
	data := append(lengthBytes, buf...)
	_, err := s.conn.Write(data) // 或 io.Copy
	return err
}
