package hwenc

import (
	"errors"
	"io"
	"testing"
)

// AV1QIndex follows the calibration: the AV1 index with the same PSNR as
// HEVC at a QP, within 3, and the ends of the scale stay inside AV1's.
func TestAV1QIndex(t *testing.T) {
	for qp, want := range map[int]int{14: 26, 18: 49, 24: 92, 30: 139} {
		if got := AV1QIndex(qp); got < want-3 || got > want+3 {
			t.Errorf("QP %d: index %d, measured %d", qp, got, want)
		}
	}
	if AV1QIndex(0) != 1 || AV1QIndex(51) != 255 {
		t.Errorf("ends: %d %d", AV1QIndex(0), AV1QIndex(51))
	}
	for qp := 1; qp <= 51; qp++ {
		if AV1QIndex(qp) < AV1QIndex(qp-1) {
			t.Fatalf("not monotonic at %d", qp)
		}
	}
}

// H.264, Media Foundation and depths other than 8 and 10 are refused before
// any library is loaded.
func TestOpenRefusesDepths(t *testing.T) {
	for _, c := range []struct {
		k     Kind
		codec Codec
		depth int
	}{{NVENC, H264, 10}, {MediaFoundation, HEVC, 10}, {NVENC, HEVC, 12}} {
		if _, err := Open(c.k, Config{Codec: c.codec, Width: 64, Height: 64, FPSNum: 24, FPSDen: 1, BitDepth: c.depth}, io.Discard); !errors.Is(err, ErrDepth) {
			t.Errorf("%s codec %d at %d bits: %v", c.k, c.codec, c.depth, err)
		}
	}
}
