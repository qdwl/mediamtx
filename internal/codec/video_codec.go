// Package codec contains codec transcoding utilities.
package codec

// VideoCodecID represents a video codec identifier.
type VideoCodecID int

const (
	VideoCodecH264 VideoCodecID = iota
	VideoCodecH265
)
