package mpegps

import "time"

type Track struct {
	StreamId   uint8
	StreamType uint8
	Codec      Codec
	Complete   bool
	Updated    time.Time
}
