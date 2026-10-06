// Package hwenc drives GPU video encoders through their system libraries —
// NVENC (NVIDIA's driver), VAAPI (Mesa, Intel's media driver) and
// VideoToolbox (macOS) — loaded at run time, so mvctools needs neither cgo
// nor ffmpeg for hardware encoding. Each encoder takes NV12 pictures and
// writes an Annex B H.264 or HEVC stream, or an AV1 stream of OBUs in the
// low-overhead format (each with its size), temporal unit after temporal
// unit.
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
	AV1
)

// Config describes the stream to encode.
type Config struct {
	Codec          Codec
	Width, Height  int
	FPSNum, FPSDen int
	// QP is the constant quantiser for P pictures; I and B pictures get
	// ffmpeg's default factors for the encoder from it, so a value means
	// what it did when these encoders were driven through ffmpeg's -qp.
	// For AV1 it is on the same 0-51 scale and mapped by AV1QIndex.
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

// AV1QIndex maps a quantiser on the H.264/HEVC 0-51 scale to an AV1
// quantizer index (1-255), so that a value means about the same picture
// quality in AV1 as in HEVC. Index 0 would be AV1's lossless mode, which
// no --crf means, so the bottom of the scale stops at 1.
//
// The line was measured on NVENC (same preset, tuning and B-frames for
// both codecs) over 300 frames of a Blu-ray 3D: for HEVC QP 14, 18, 24 and
// 30, the AV1 index with the same luma PSNR against the source was 26, 49,
// 92 and 139, and the line fits those within 3 (about 0.2 dB). At equal
// PSNR the AV1 encodes were 11-15% smaller.
func AV1QIndex(qp int) int {
	v := av1Slope*float64(qp) + av1Offset
	return max(1, min(255, int(v+0.5)))
}

const (
	av1Slope  = 7.1
	av1Offset = -76.0
)
