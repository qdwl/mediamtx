package transport

import "net"

type TcpServer struct {
	conn net.Conn
}

func NewTcpServer(
	reader PacketProcessor,
	localAddr string,
	remoteAddr string,
) (*TcpServer, error) {
	return nil, nil
}

func (s *TcpServer) Close() {

}

func (s *TcpServer) Write(buf []byte) error {
	return nil
}
