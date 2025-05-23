package flv

import (
	"errors"
)

type Conn struct {
	FlvHeader chan *Header
	FlvTags   chan *Tag
	Done      chan struct{}
}

func NewConn() *Conn {
	return &Conn{
		FlvHeader: make(chan *Header),
		FlvTags:   make(chan *Tag),
		Done:      make(chan struct{}),
	}
}

func (c *Conn) Close() {
	close(c.Done)
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
	case <-c.Done:
		return errors.New("terminated")
	}
}

func (c *Conn) WriteTag(tag *Tag) error {
	select {
	case c.FlvTags <- tag:
		return nil
	case <-c.Done:
		return errors.New("terminated")
	}
}
