package flv

import (
	"context"
	"errors"
)

type Conn struct {
	FlvHeader chan *Header
	FlvTags   chan *Tag
	ctx       context.Context
	ctxCancel func()
}

func NewConn() *Conn {
	ctx, ctxCancel := context.WithCancel(context.Background())

	return &Conn{
		FlvHeader: make(chan *Header),
		FlvTags:   make(chan *Tag),
		ctx:       ctx,
		ctxCancel: ctxCancel,
	}
}

func (c *Conn) Close() {
	c.ctxCancel()
}

func (c *Conn) Write(data TagData) error {
	tag, err := data.marshal()
	if err != nil {
		return err
	}

	return c.WriteTag(tag)
}

func (c *Conn) WriteHeader(header *Header) error {
	select {
	case c.FlvHeader <- header:
		return nil
	case <-c.ctx.Done():
		return errors.New("terminated")
	}
}

func (c *Conn) WriteTag(tag *Tag) error {
	select {
	case c.FlvTags <- tag:
		return nil
	case <-c.ctx.Done():
		return errors.New("terminated")
	}
}
