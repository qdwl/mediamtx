package pusher

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/protocols/rtmp"
	"github.com/bluenviron/mediamtx/internal/stream"
)

type pusher struct {
	parentCtx    context.Context
	pathName     string
	pushAddr     string
	wg           *sync.WaitGroup
	pathManager  serverPathManager
	parent       *Server
	readTimeout  conf.Duration
	writeTimeout conf.Duration

	ctx       context.Context
	ctxCancel func()
	path      defs.Path
}

func (p *pusher) initialize() {
	p.ctx, p.ctxCancel = context.WithCancel(p.parentCtx)

	p.Log(logger.Info, "opened")

	p.wg.Add(1)
	go p.run()
}

func (p *pusher) Log(level logger.Level, format string, args ...interface{}) {
	p.parent.Log(level, "[PUSH %v] "+format, append([]interface{}{p.pathName}, args...)...)
}

func (p *pusher) Close() {
	p.ctxCancel()
}

func (p *pusher) ID() string {
	hash := md5.Sum([]byte(p.pushAddr + p.pathName))
	return hex.EncodeToString(hash[:])
}

func (p *pusher) run() {
	defer p.wg.Done()
	err := p.runInner()

	p.ctxCancel()

	p.parent.closePush(p)

	p.Log(logger.Info, "closed: %v", err)
}

func (p *pusher) runInner() error {
	p.Log(logger.Debug, "connecting")

	u, err := url.Parse(fmt.Sprintf("%s/%s", p.pushAddr, p.pathName))
	if err != nil {
		return err
	}

	// add default port
	_, _, err = net.SplitHostPort(u.Host)
	if err != nil {
		if u.Scheme == "rtmp" {
			u.Host = net.JoinHostPort(u.Host, "1935")
		} else {
			u.Host = net.JoinHostPort(u.Host, "1936")
		}
	}

	nconn, err := func() (net.Conn, error) {
		ctx2, cancel2 := context.WithTimeout(p.ctx, time.Duration(p.readTimeout))
		defer cancel2()

		return (&net.Dialer{}).DialContext(ctx2, "tcp", u.Host)
	}()
	if err != nil {
		return err
	}

	wirteErr := make(chan error)
	go func() {
		wirteErr <- p.runWriter(u, nconn)
	}()

	for {
		select {
		case err := <-wirteErr:
			nconn.Close()
			return err

		case <-p.ctx.Done():
			nconn.Close()
			<-wirteErr
			p.Log(logger.Info, "push exit")
			return errors.New("terminated")
		}
	}
}

func (p *pusher) runWriter(u *url.URL, nconn net.Conn) error {
	nconn.SetReadDeadline(time.Now().Add(time.Duration(p.readTimeout)))
	nconn.SetWriteDeadline(time.Now().Add(time.Duration(p.writeTimeout)))
	conn := &rtmp.Conn{
		RW:      nconn,
		Client:  true,
		URL:     u,
		Publish: true,
	}
	err := conn.Initialize()
	if err != nil {
		return err
	}

	var path defs.Path
	var stream *stream.Stream
	var count int64 = 0
	for {
		<-time.After(100 * time.Millisecond)
		path, stream, err = p.pathManager.AddReader(defs.PathAddReaderReq{
			Author: p,
			AccessRequest: defs.PathAccessRequest{
				Name:     p.pathName,
				SkipAuth: true,
			},
		})
		count++
		if err == nil || count > 100 {
			break
		} else {
			p.Log(logger.Debug, "find stream failed, %v", err)
			continue
		}
	}

	if err != nil {
		return err
	}

	p.path = path
	defer p.path.RemoveReader(defs.PathRemoveReaderReq{Author: p})

	err = rtmp.FromStream(stream, p, conn, nconn, time.Duration(p.writeTimeout))
	if err != nil {
		return err
	}

	p.Log(logger.Info, "is pushing path '%s' to '%s', %s",
		path.Name(), p.pushAddr, defs.FormatsInfo(stream.ReaderFormats(p)))

	// disable read deadline
	nconn.SetReadDeadline(time.Time{})

	stream.StartReader(p)
	defer stream.RemoveReader(p)

	select {
	case <-p.ctx.Done():
		return fmt.Errorf("terminated")

	case err := <-stream.ReaderError(p):
		return err
	}
}

// APIReaderDescribe implements reader.
func (p *pusher) APIReaderDescribe() defs.APIPathSourceOrReader {
	return defs.APIPathSourceOrReader{
		Type: "push",
		ID:   "",
	}
}
