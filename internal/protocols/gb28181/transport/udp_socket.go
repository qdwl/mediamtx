package transport

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/bluenviron/mediamtx/internal/restrictnetwork"
	"github.com/pion/rtp"
)

const reorderWindowSize = 64

type reorderBuffer struct {
	buf         [reorderWindowSize]*rtp.Packet
	baseSeq     uint16
	initialized bool
	count       int
}

// seqDistance 计算两个序列号之间的距离，考虑 wrap-around
// 返回值范围: [0, 32767]，表示 a 到 b 的正向距离
func seqDistance(a, b uint16) uint16 {
	if b >= a {
		return b - a
	}
	// 处理 wrap-around: b 实际上比 a 大 2^16
	return uint16((int32(b) + 65536 - int32(a)) % 65536)
}

func (rb *reorderBuffer) insert(pkt *rtp.Packet, flushFunc func(*rtp.Packet)) {
	if !rb.initialized {
		rb.baseSeq = pkt.SequenceNumber
		rb.initialized = true
		flushFunc(pkt)
		return
	}

	// 使用改进的序列号距离计算
	distance := seqDistance(rb.baseSeq, pkt.SequenceNumber)

	// 如果包太旧（超过窗口大小），丢弃
	if distance >= reorderWindowSize {
		// 移动窗口：输出前面的包，丢弃末尾的包
		shiftDist := reorderWindowSize / 2 // 移动一半窗口
		rb.flushBeforeOffset(shiftDist, flushFunc)
		rb.shift(shiftDist)
		distance = seqDistance(rb.baseSeq, pkt.SequenceNumber)

		// 如果仍然超出窗口，说明所有缓冲的包都太旧，重置
		if distance >= reorderWindowSize {
			rb.flushAll(flushFunc)
			rb.baseSeq = pkt.SequenceNumber
			flushFunc(pkt)
			return
		}
	}

	offset := int(distance)

	// 允许覆盖：即使位置已有包，也用新包覆盖
	if rb.buf[offset] == nil {
		rb.count++
	}
	rb.buf[offset] = pkt
}

// flushBeforeOffset 输出指定偏移量之前的所有包
func (rb *reorderBuffer) flushBeforeOffset(offset int, flushFunc func(*rtp.Packet)) {
	for i := 0; i < offset && i < reorderWindowSize; i++ {
		if rb.buf[i] != nil {
			flushFunc(rb.buf[i])
			rb.buf[i] = nil
			rb.count--
		}
	}
}

// flushContinuous 从 baseSeq 开始输出连续的包，遇到空槽就停止
func (rb *reorderBuffer) flushContinuous(flushFunc func(*rtp.Packet)) {
	i := 0
	for i < reorderWindowSize && rb.buf[i] != nil {
		flushFunc(rb.buf[i])
		rb.buf[i] = nil
		i++
	}
	rb.shift(i)
}

// flushAll 关闭时输出缓冲中所有包（按顺序，跳过空洞）
func (rb *reorderBuffer) flushAll(flushFunc func(*rtp.Packet)) {
	for i := range reorderWindowSize {
		if rb.buf[i] != nil {
			flushFunc(rb.buf[i])
			rb.buf[i] = nil
		}
	}
	rb.count = 0
}

func (rb *reorderBuffer) shift(n int) {
	if n <= 0 || n > reorderWindowSize {
		return
	}

	// 移动有效包到缓冲区前端
	for i := 0; i < reorderWindowSize-n; i++ {
		rb.buf[i] = rb.buf[i+n]
		rb.buf[i+n] = nil
	}

	// 清理尾部
	for i := reorderWindowSize - n; i < reorderWindowSize; i++ {
		rb.buf[i] = nil
	}

	rb.count -= n
	if rb.count < 0 {
		rb.count = 0
	}

	rb.baseSeq += uint16(n)
}

type UdpSocket struct {
	pc           *net.UDPConn
	remoteAddr   *net.UDPAddr
	writeTimeout time.Duration
	reader       PacketProcessor

	packetChan chan *rtp.Packet
	writeChan  chan []byte
	wg         sync.WaitGroup
	closeChan  chan struct{}
}

func NewUdpSocket(
	reader PacketProcessor,
	localAddr string,
	remoteAddr string,
) (*UdpSocket, error) {
	addr, err := net.ResolveUDPAddr("udp", remoteAddr)
	if err != nil {
		return nil, fmt.Errorf("remote address fmt error")
	}

	tmp, err := net.ListenPacket(restrictnetwork.Restrict("udp", localAddr))
	if err != nil {
		return nil, fmt.Errorf("listen udp server %s failed", localAddr)
	}

	pc := tmp.(*net.UDPConn)
	err = pc.SetReadBuffer(kernelReadBufferSize)
	if err != nil {
		return nil, err
	}

	u := &UdpSocket{
		pc:           pc,
		remoteAddr:   addr,
		writeTimeout: 10 * time.Second,
		reader:       reader,
		closeChan:    make(chan struct{}),
	}

	if reader != nil {
		u.packetChan = make(chan *rtp.Packet, 100)
		u.wg.Add(2)
		go u.runReader()
		go u.runProcessor()
	}

	u.writeChan = make(chan []byte, 50)
	u.wg.Add(1)
	go u.runWriter()

	return u, nil
}

func (u *UdpSocket) Close() {
	close(u.closeChan)
	u.pc.Close()
	u.wg.Wait()
}

func (u *UdpSocket) runReader() {
	defer u.wg.Done()

	buf := make([]byte, maxPacketSize)
	for {
		n, _, err := u.pc.ReadFromUDP(buf)
		if err != nil {
			return
		}

		pktBuf := make([]byte, n)
		copy(pktBuf, buf[:n])

		pkt := &rtp.Packet{}
		err = pkt.Unmarshal(pktBuf)
		if err != nil {
			continue
		}

		select {
		case u.packetChan <- pkt:
		case <-u.closeChan:
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (u *UdpSocket) runProcessor() {
	defer u.wg.Done()

	rb := &reorderBuffer{}

	flush := func(pkt *rtp.Packet) {
		u.reader.ProcessRtpPacket(pkt)
	}

	for {
		select {
		case pkt, ok := <-u.packetChan:
			if !ok {
				rb.flushAll(flush)
				return
			}

			rb.insert(pkt, flush)
			rb.flushContinuous(flush)

		case <-u.closeChan:
			rb.flushAll(flush)
			return
		}
	}
}

func (u *UdpSocket) runWriter() {
	defer u.wg.Done()

	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case buf, ok := <-u.writeChan:
			if !ok {
				return
			}
			<-ticker.C
			u.pc.SetWriteDeadline(time.Now().Add(u.writeTimeout))
			u.pc.WriteTo(buf, u.remoteAddr)

		case <-u.closeChan:
			return
		}
	}
}

func (u *UdpSocket) Write(buf []byte) error {
	pktBuf := make([]byte, len(buf))
	copy(pktBuf, buf)

	select {
	case u.writeChan <- pktBuf:
		return nil
	case <-u.closeChan:
		return fmt.Errorf("socket closed")
	case <-time.After(u.writeTimeout):
		return fmt.Errorf("write timeout: queue full")
	}
}
