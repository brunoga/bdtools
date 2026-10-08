//go:build (linux || windows) && (amd64 || arm64)

package gpu

import (
	"bytes"
	"testing"
)

func TestNVENCProducesAStream(t *testing.T) {
	for _, c := range []Codec{H264, HEVC} {
		b := encodeTest(t, NVENC, c, 640, 360, 40)
		if !bytes.HasPrefix(b, []byte{0, 0, 0, 1}) || len(b) < 1000 {
			t.Fatalf("codec %d: %d bytes, starting % x", c, len(b), b[:min(8, len(b))])
		}
	}
}

// The stream must decode to the pictures that went in: decoded with this
// repository's decoder, every frame is close to the source pattern.
func TestNVENCDecodesBack(t *testing.T) {
	const w, h, n = 640, 368, 30
	b := encodeTest(t, NVENC, H264, w, h, n)
	checkDecode(t, b, w, h, n)
}

func TestNVENCHEVCDecodesBack(t *testing.T) {
	const w, h, n = 640, 368, 30
	b := encodeTest(t, NVENC, HEVC, w, h, n)
	checkDecodeFFmpeg(t, b, "hevc", w, h, n)
}

// AV1 comes out as low-overhead OBUs, which ffmpeg's obu demuxer reads.
func TestNVENCAV1DecodesBack(t *testing.T) {
	const w, h, n = 640, 368, 30
	b := encodeTest(t, NVENC, AV1, w, h, n)
	checkDecodeFFmpeg(t, b, "obu", w, h, n)
}

// At 10 bits the stream is HEVC Main 10 or 10-bit AV1, made from P010.
func TestNVENC10Bit(t *testing.T) {
	const w, h, n = 640, 368, 30
	checkDecode10(t, encodeDepth(t, NVENC, HEVC, 10, w, h, n), "hevc", w, h, n)
	checkDecode10(t, encodeDepth(t, NVENC, AV1, 10, w, h, n), "obu", w, h, n)
}
