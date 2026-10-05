package convert

import (
	"math/rand/v2"
	"testing"

	"github.com/brunoga/mvc"
	"github.com/brunoga/mvc/internal/hwenc"
)

func squeezeRef(dst, src []byte) {
	n := len(src)
	at := func(i int) int { return int(src[max(0, min(n-1, i))]) }
	for x := range dst {
		i := 2 * x
		dst[x] = clip8((-at(i-1) + 5*at(i) + 5*at(i+1) - at(i+2) + 4) >> 3)
	}
}

// The fast squeeze matches the plain filter at every width and step.
func TestSqueezeMatchesTheFilter(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for n := 2; n <= 40; n += 2 {
		src := make([]byte, n)
		for i := range src {
			src[i] = byte(r.IntN(256))
		}
		want := make([]byte, n/2)
		squeezeRef(want, src)
		for _, step := range []int{1, 2} {
			got := make([]byte, n/2*step)
			squeeze(got, src, step)
			for x := range want {
				if got[x*step] != want[x] {
					t.Fatalf("n=%d step=%d x=%d: %d, want %d", n, step, x, got[x*step], want[x])
				}
			}
		}
	}
}

func benchFrame(w, h int) *mvc.Frame {
	f := &mvc.Frame{Width: w, Height: h, StrideY: w, StrideC: w / 2}
	f.Y, f.Cb, f.Cr = make([]byte, w*h), make([]byte, w*h/4), make([]byte, w*h/4)
	return f
}

func BenchmarkDrawSBS(b *testing.B) {
	sf := &mvc.StereoFrame{Base: benchFrame(1920, 1080), Dependent: benchFrame(1920, 1080)}
	p := &hwenc.Picture{Y: make([]byte, 3840*1080), UV: make([]byte, 3840*540), Pitch: 3840}
	for _, half := range []bool{false, true} {
		name := "full"
		if half {
			name = "half"
		}
		b.Run(name, func(b *testing.B) {
			for b.Loop() {
				drawSBS(p, sf, false, half)
			}
		})
	}
}
