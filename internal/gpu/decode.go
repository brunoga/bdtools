package gpu

import "errors"

// VideoCodec is a compressed video format a decoder takes.
type VideoCodec int

const (
	DecodeH264 VideoCodec = iota
	DecodeHEVC
	DecodeVC1
	DecodeMPEG2
	DecodeAV1
)

func (c VideoCodec) String() string {
	return [...]string{"H.264", "HEVC", "VC-1", "MPEG-2", "AV1"}[c]
}

// DecodeConfig describes the stream to decode.
type DecodeConfig struct {
	Codec VideoCodec
	// LowDelay asks for pictures soon after their access units, at some
	// cost in speed, where a decoder holds them back (ffmpeg's frame
	// threads): for a stream decoded beside another and paired with it.
	LowDelay bool
}

// ColorInfo is a stream's colour signalling, as its sequence header states
// it (H.273 code points; 2 is "unspecified").
type ColorInfo struct {
	Primaries, Transfer, Matrix int
	FullRange                   bool
}

// DecodedPicture is one picture in display order, in the layout the
// encoders take: a luma plane and a half-resolution plane of interleaved
// Cb/Cr, each row Pitch bytes apart. At Depth 8 it is NV12; above it, P010
// (two bytes a sample, little-endian, the value in the top bits). Its planes
// are valid only during the call that delivers it.
type DecodedPicture struct {
	Width, Height int
	Depth         int
	Y, UV         []byte
	Pitch         int
	PTS           int64 // as given to Decode
	Color         ColorInfo
	// FrameRateNum and FrameRateDen are the stream's, when it states one.
	FrameRateNum, FrameRateDen int
}

// Decoder decodes a compressed video stream.
type Decoder interface {
	// Decode feeds one access unit (Annex B; for VC-1 and MPEG-2 the
	// elementary stream's own framing) presented at pts. Pictures come out
	// in display order through the function given to OpenDecoder, from
	// within Decode or Flush.
	Decode(au []byte, pts int64) error
	// Flush ends the stream: the pictures held back for reordering come
	// out.
	Flush() error
	// Close releases the device.
	Close() error
}

// ErrDecodeUnavailable says the decoder's library or device is not present,
// or it cannot decode this stream.
var ErrDecodeUnavailable = errors.New("gpu: decoder not available")

// OpenDecoder starts a decoder of kind k, calling picture with each decoded
// picture in display order.
func OpenDecoder(k Kind, cfg DecodeConfig, picture func(*DecodedPicture) error) (Decoder, error) {
	switch k {
	case NVENC: // the NVIDIA GPU: NVDEC
		return openNVDEC(cfg, picture)
	case VideoToolbox:
		return openVTDecoder(cfg, picture)
	}
	return nil, ErrDecodeUnavailable
}
