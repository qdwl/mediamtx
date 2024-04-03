package flv

import (
	"errors"

	"github.com/notedit/rtmp/format/flv/flvio"
)

type flvHeader struct {
	hasVideo bool
	hasAudio bool
}

type conn struct {
	flvHeader chan flvHeader
	flvTags   chan flvio.Tag
	done      chan struct{}
}

func newConn() *conn {
	return &conn{
		flvHeader: make(chan flvHeader),
		flvTags:   make(chan flvio.Tag),
		done:      make(chan struct{}),
	}
}

func (c *conn) close() {
	close(c.done)
}

func (c *conn) writeHeader(header flvHeader) error {
	select {
	case c.flvHeader <- header:
		return nil
	case <-c.done:
		return errors.New("terminated")
	}
}

func (c *conn) writeTag(tag flvio.Tag) error {
	select {
	case c.flvTags <- tag:
		return nil
	case <-c.done:
		return errors.New("terminated")
	}
}
