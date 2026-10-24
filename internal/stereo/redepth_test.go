package stereo

import (
	"math"
	"slices"
	"testing"

	"github.com/brunoga/bdtools/mvc"
)

// redepthScene is a stereo pair of a textured background (disparity bg)
// behind a textured square (fg), and whether a pixel is the square's.
func redepthScene(w, h int, bg, fg float32) (l, r *mvc.Frame, square func(x, y int) bool) {
	c := newFrame(w, h)
	seed := uint32(1)
	noise := make([]float32, w*h)
	for i := range noise {
		seed = seed*1664525 + 1013904223
		noise[i] = float32(seed >> 24)
	}
	for y := range h {
		for x := range w {
			// two textures, so the square's edge shows
			v := noise[y*w+x]
			if x > 0 {
				v = (v + noise[y*w+x-1]) / 2
			}
			c.Y[y*w+x] = byte(40 + v*0.7)
		}
	}
	for i := range c.Cb {
		c.Cb[i], c.Cr[i] = 128, 128
	}
	square = func(x, y int) bool { return x >= w/3 && x < 2*w/3 && y >= h/3 && y < 2*h/3 }
	d := make([]float32, w*h)
	for y := range h {
		for x := range w {
			d[y*w+x] = bg
			if square(x, y) {
				d[y*w+x] = fg
				c.Y[y*w+x] = 255 - c.Y[y*w+x]/2
			}
		}
	}
	l, r = Pair(c, d)
	return l, r, square
}

// The depth halved: the new eyes' disparity is half the old, the square's
// and the background's.
func TestRedepthHalves(t *testing.T) {
	const w, h = 640, 360
	l, r, square := redepthScene(w, h, 6, 24)
	nl, nr := Redepth(nil, l, r, 0.5, 0)
	dm := Match(nl, nr)
	var in, out []float64
	for y := range dm.H {
		for x := range dm.W {
			i := y*dm.W + x
			if !dm.Valid[i] {
				continue
			}
			// well inside or outside the square
			X, Y := 2*x, 2*y
			switch {
			case square(X-12, Y-12) && square(X+12, Y+12):
				in = append(in, float64(dm.D[i]))
			case !square(X-40, Y) && !square(X+40, Y) && !square(X, Y-12) && !square(X, Y+12):
				out = append(out, float64(dm.D[i]))
			}
		}
	}
	med := func(v []float64) float64 { slices.Sort(v); return v[len(v)/2] }
	if len(in) < 100 || len(out) < 100 {
		t.Fatalf("too few sure matches: %d in, %d out", len(in), len(out))
	}
	if m := med(in); math.Abs(m-12) > 0.5 {
		t.Errorf("the square's disparity %.2f, want 12", m)
	}
	if m := med(out); math.Abs(m-3) > 0.5 {
		t.Errorf("the background's disparity %.2f, want 3", m)
	}
}

// A shift alone moves the eyes apart by it, each half, black coming in.
func TestRedepthShift(t *testing.T) {
	const w, h = 64, 8
	l := newFrame(w, h)
	for y := range h {
		for x := range w {
			l.Y[y*w+x] = byte(4 * x)
		}
	}
	for i := range l.Cb {
		l.Cb[i], l.Cr[i] = 128, 128
	}
	nl, nr := Redepth(nil, l, l, 1, 4)
	if got := nl.Y[3*w+10]; got != byte(4*8) {
		t.Errorf("the left eye at 10: %d, want %d (moved right 2)", got, 4*8)
	}
	if got := nr.Y[3*w+10]; got != byte(4*12) {
		t.Errorf("the right eye at 10: %d, want %d (moved left 2)", got, 4*12)
	}
	if nl.Y[3*w] != 16 || nr.Y[3*w+w-1] != 16 {
		t.Errorf("no black at the edges: %d, %d", nl.Y[3*w], nr.Y[3*w+w-1])
	}
}

// Depth kept and nothing moved: the eyes as they were.
func TestRedepthIdentity(t *testing.T) {
	l, r, _ := redepthScene(128, 64, 4, 10)
	nl, nr := Redepth(nil, l, r, 1, 0)
	if !slices.Equal(nl.Y, l.Y) || !slices.Equal(nr.Y, r.Y) || !slices.Equal(nl.Cb, l.Cb) {
		t.Error("the eyes changed")
	}
}

func BenchmarkRedepthParts(b *testing.B) {
	l, r, _ := redepthScene(1920, 1080, 6, 24)
	b.Run("match", func(b *testing.B) {
		for b.Loop() {
			Match(l, r)
		}
	})
	dm := Match(l, r)
	b.Run("dense", func(b *testing.B) {
		for b.Loop() {
			Dense(dm, 1920, 1080)
		}
	})
	d := Dense(dm, 1920, 1080)
	b.Run("render", func(b *testing.B) {
		for b.Loop() {
			Render(l, d, -0.25)
		}
	})
}
