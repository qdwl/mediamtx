package transport

import "net"

type TcpClient struct {
	conn net.Conn
}

func NewTcpClient(
	reader PacketProcessor,
	localAddr string,
	remoteAddr string,
) (*TcpClient, error) {
	return nil, nil
}

func (s *TcpClient) Close() {

}

func (s *TcpClient) Write(buf []byte) error {
	return nil
}
