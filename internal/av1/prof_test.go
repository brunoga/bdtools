package av1

import (
	"os"
	"strconv"
	"testing"
)

// TestProfSeq codes the first $AV1PROF_N (default 4) pictures of the
// 1080p clip $AV1PROF on one tile, for a profile.
func TestProfSeq(t *testing.T) {
	file := os.Getenv("AV1PROF")
	if file == "" {
		t.Skip("AV1PROF unset")
	}
	n, _ := strconv.Atoi(os.Getenv("AV1PROF_N"))
	if n == 0 {
		n = 4
	}
	b, err := os.ReadFile(file) //nolint:gosec // a test
	if err != nil {
		t.Fatal(err)
	}
	const w, h = 1920, 1080
	e, _ := NewEncoder(EncoderOptions{Width: w, Height: h, BitDepth: 8, QIndex: 120})
	for k := range n {
		p := b[k*w*h*3/2:]
		src := &Source{Y: make([]uint16, w*h), Cb: make([]uint16, w*h/4), Cr: make([]uint16, w*h/4), StrideY: w, StrideC: w / 2}
		for i := range src.Y {
			src.Y[i] = uint16(p[i])
		}
		for i := range src.Cb {
			src.Cb[i], src.Cr[i] = uint16(p[w*h+i]), uint16(p[w*h*5/4+i])
		}
		units, _ := e.Encode(src)
		for _, u := range units {
			t.Logf("picture %d: %d bytes", k, len(u.Data))
		}
	}
}
