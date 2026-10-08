//go:build linux && (amd64 || arm64)

package gpu

import (
	"bytes"
	"testing"
)

func TestVAAPIProducesAStream(t *testing.T) {
	for _, c := range []Codec{H264, HEVC} {
		b := encodeTest(t, VAAPI, c, 640, 360, 40)
		if !bytes.HasPrefix(b, []byte{0, 0, 0, 1}) || len(b) < 1000 {
			t.Fatalf("codec %d: %d bytes, starting % x", c, len(b), b[:min(8, len(b))])
		}
	}
}

// Across GOPs, with B-frames and a height that needs cropping, the stream
// decodes back to the pattern that went in.
func TestVAAPIDecodesBack(t *testing.T) {
	const w, h, n = 640, 360, 30
	b := encodeTest(t, VAAPI, H264, w, h, n)
	checkDecode(t, b, w, h, n)
}

func TestVAAPIHEVCDecodesBack(t *testing.T) {
	const w, h, n = 640, 360, 30
	b := encodeTest(t, VAAPI, HEVC, w, h, n)
	checkDecodeFFmpeg(t, b, "hevc", w, h, n)
}

// AV1 headers are written here, the tiles by the driver: the stream must
// decode, across a key frame, with no errors.
func TestVAAPIAV1DecodesBack(t *testing.T) {
	const w, h, n = 640, 360, 30
	b := encodeTest(t, VAAPI, AV1, w, h, n)
	checkDecodeFFmpeg(t, b, "obu", w, h, n)
}

// At 10 bits the stream is HEVC Main 10 or 10-bit AV1, made from P010.
func TestVAAPI10Bit(t *testing.T) {
	const w, h, n = 640, 368, 30
	checkDecode10(t, encodeDepth(t, VAAPI, HEVC, 10, w, h, n), "hevc", w, h, n)
	checkDecode10(t, encodeDepth(t, VAAPI, AV1, 10, w, h, n), "obu", w, h, n)
}
