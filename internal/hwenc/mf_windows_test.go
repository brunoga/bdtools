//go:build windows && (amd64 || arm64)

package hwenc

import (
	"errors"
	"io"
	"testing"
)

// useMFSoftware lets the tests run on machines without a GPU encoder (CI's
// virtual Windows) through Microsoft's software MFTs: the same API and the
// same code, only synchronous.
func useMFSoftware(t *testing.T, codec Codec) {
	t.Helper()
	e, err := Open(MediaFoundation, Config{Codec: codec, Width: 640, Height: 368, FPSNum: 24, FPSDen: 1, QP: 24}, io.Discard)
	if err == nil {
		_ = e.Close()
		return
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	t.Logf("no hardware encoder (%v): testing with the software one", err)
	mfAllowSoftware = true
	t.Cleanup(func() { mfAllowSoftware = false })
}

func TestMediaFoundationDecodesBack(t *testing.T) {
	useMFSoftware(t, H264)
	const w, h, n = 640, 368, 30
	b := encodeTest(t, MediaFoundation, H264, w, h, n)
	checkDecode(t, b, w, h, n)
}

func TestMediaFoundationHEVCDecodesBack(t *testing.T) {
	useMFSoftware(t, HEVC)
	const w, h, n = 640, 368, 30
	b := encodeTest(t, MediaFoundation, HEVC, w, h, n)
	checkDecodeFFmpeg(t, b, "hevc", w, h, n)
}
