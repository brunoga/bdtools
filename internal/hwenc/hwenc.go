// Package hwenc drives GPU video encoders through their system libraries —
// NVENC (NVIDIA's driver), VAAPI (Mesa, Intel's media driver) and
// VideoToolbox (macOS) — loaded at run time, so mvctools needs neither cgo
// nor ffmpeg for hardware encoding. Each encoder takes NV12 pictures and
// writes an Annex B H.264 or HEVC stream.
package hwenc

import (
	"errors"
	"io"
)

// Codec is the output codec.
type Codec int

const (
	H264 Codec = iota
	HEVC
)

// Config describes the stream to encode.
type Config struct {
	Codec          Codec
	Width, Height  int
	FPSNum, FPSDen int
	// QP is the constant quantiser for P pictures; I and B pictures get
	// ffmpeg's default factors for the encoder from it, so a value means
	// what it did when these encoders were driven through ffmpeg's -qp.
	QP int
	// GOP is the keyframe interval in frames; 0 picks about 2 s.
	GOP int
	// Device is the VAAPI render node (VAAPI only).
	Device string
}

// Picture is the NV12 frame an encoder wants filled: a full-resolution
// luma plane and a half-resolution plane of interleaved Cb/Cr, each row
// Pitch bytes apart.
type Picture struct {
	Y, UV []byte
	Pitch int
}

// Encoder encodes pictures to an Annex B stream.
type Encoder interface {
	// Encode asks fill to draw the next picture, then encodes it.
	Encode(fill func(*Picture)) error
	// Close finishes the stream (the encoder may hold pictures back for
	// B-frames) and releases the device.
	Close() error
}

// ErrUnavailable says the encoder's library or device is not present.
var ErrUnavailable = errors.New("hwenc: not available")

// Kind names a hardware encoder.
type Kind string

const (
	NVENC        Kind = "nvenc"
	VAAPI        Kind = "vaapi"
	VideoToolbox Kind = "videotoolbox"
)

// Open starts an encoder writing to w.
func Open(k Kind, cfg Config, w io.Writer) (Encoder, error) {
	if cfg.GOP == 0 && cfg.FPSDen > 0 {
		cfg.GOP = 2 * cfg.FPSNum / cfg.FPSDen
	}
	switch k {
	case NVENC:
		return openNVENC(cfg, w)
	case VAAPI:
		return openVAAPI(cfg, w)
	case VideoToolbox:
		return openVideoToolbox(cfg, w)
	}
	return nil, ErrUnavailable
}

// VTQuality maps a constant quantiser, lower is better, onto VideoToolbox's
// quality, 0 to 1 with higher better: QP 0 is 1, QP 51 is 0.
func VTQuality(qp int) float64 { return max(0, min(1, 1-float64(qp)/51)) }
