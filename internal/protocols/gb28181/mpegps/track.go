package mpegps

type Track struct {
	StreamId   uint8
	StreamType uint8
	Codec      Codec
	Complete   bool
}
