//go:build darwin && (amd64 || arm64)

package gpu

import (
	"errors"
	"io"
	"testing"
)

// useVTSoftware lets the tests run on Macs without a media engine (CI's
// virtual ones) through Apple's software encoder: the same API, the same
// code here.
func useVTSoftware(t *testing.T, codec Codec) {
	t.Helper()
	e, err := Open(VideoToolbox, Config{Codec: codec, Width: 640, Height: 360, FPSNum: 24, FPSDen: 1, QP: 24}, io.Discard)
	if err == nil {
		_ = e.Close()
		return
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	t.Logf("no hardware encoder (%v): testing with the software one", err)
	vtRequireHardware = false
	t.Cleanup(func() { vtRequireHardware = true })
}

func TestVideoToolboxDecodesBack(t *testing.T) {
	useVTSoftware(t, H264)
	const w, h, n = 640, 360, 30
	b := encodeTest(t, VideoToolbox, H264, w, h, n)
	checkDecode(t, b, w, h, n)
}

func TestVideoToolboxHEVCDecodesBack(t *testing.T) {
	useVTSoftware(t, HEVC)
	const w, h, n = 640, 360, 30
	b := encodeTest(t, VideoToolbox, HEVC, w, h, n)
	checkDecodeFFmpeg(t, b, "hevc", w, h, n)
}

// At 10 bits VideoToolbox makes HEVC Main 10 from P010 ('x420') buffers.
func TestVideoToolbox10Bit(t *testing.T) {
	useVTSoftware(t, HEVC)
	const w, h, n = 640, 360, 30
	checkDecode10(t, encodeDepth(t, VideoToolbox, HEVC, 10, w, h, n), "hevc", w, h, n)
}
