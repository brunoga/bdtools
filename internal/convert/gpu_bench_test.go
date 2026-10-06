package convert

import (
	"fmt"
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

// A 10-bit picture is the 8-bit one at four times the value: exactly, side
// by side; squeezed, within 2 of it, since both round the same filter sum,
// to 8 bits (off by up to 2 at 10-bit scale) and to 10 (off by up to 1/2).
func TestDrawSBS10(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	frame := func(w, h int) *mvc.Frame {
		f := benchFrame(w, h)
		for _, p := range [][]byte{f.Y, f.Cb, f.Cr} {
			for i := range p {
				p[i] = byte(r.IntN(256))
			}
		}
		return f
	}
	const w, h = 70, 12 // a width that leaves widen a tail
	sf := &mvc.StereoFrame{Base: frame(w, h), Dependent: frame(w, h)}
	for _, half := range []bool{false, true} {
		ow := 2 * w
		if half {
			ow = w
		}
		p8 := &hwenc.Picture{Y: make([]byte, ow*h), UV: make([]byte, ow*h/2), Pitch: ow}
		p10 := &hwenc.Picture{Y: make([]byte, 2*ow*h), UV: make([]byte, ow*h), Pitch: 2 * ow, Depth: 10}
		drawSBS(p8, sf, false, half)
		drawSBS(p10, sf, false, half)
		for _, pl := range []struct {
			name    string
			p8, p10 []byte
		}{{"Y", p8.Y, p10.Y}, {"UV", p8.UV, p10.UV}} {
			for i, v8 := range pl.p8 {
				raw := int(pl.p10[2*i]) | int(pl.p10[2*i+1])<<8
				if raw&63 != 0 {
					t.Fatalf("half=%v %s[%d]: %#04x has bits below the top ten", half, pl.name, i, raw)
				}
				v10 := raw >> 6
				if !half && v10 != 4*int(v8) {
					t.Fatalf("full %s[%d]: %d, want %d", pl.name, i, v10, 4*int(v8))
				}
				// 8 bits top out at 255, which is 1020 at 10: anything the
				// filter overshoots to clips higher at 10 bits.
				if d := v10 - 4*int(v8); half && (d < -2 || d > 2) && (v8 != 255 || v10 < 1018) {
					t.Fatalf("half %s[%d]: %d, not within 2 of %d", pl.name, i, v10, 4*int(v8))
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
	for _, depth := range []int{8, 10} {
		n := 1 // bytes a sample
		if depth == 10 {
			n = 2
		}
		p := &hwenc.Picture{Y: make([]byte, n*3840*1080), UV: make([]byte, n*3840*540), Pitch: n * 3840, Depth: depth}
		for _, half := range []bool{false, true} {
			name := fmt.Sprintf("%dbit/full", depth)
			if half {
				name = fmt.Sprintf("%dbit/half", depth)
			}
			b.Run(name, func(b *testing.B) {
				for b.Loop() {
					drawSBS(p, sf, false, half)
				}
			})
		}
	}
}
