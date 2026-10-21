package stereo

import (
	"math/rand/v2"
	"slices"
	"testing"
)

// A pair rendered from a textured picture's known disparity (a square
// nearer than its background) is matched back to that disparity.
func TestMatchFindsTheDisparity(t *testing.T) {
	const w, h = 320, 180
	f := newFrame(w, h)
	rng := rand.New(rand.NewPCG(1, 2))
	noise := make([]float32, w*h)
	for i := range noise {
		noise[i] = rng.Float32() * 255
	}
	d := make([]float32, w*h)
	for y := range h {
		for x := range w {
			// a little blur, as a camera's
			var s float32
			for _, o := range [][2]int{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
				s += noise[min(y+o[1], h-1)*w+min(x+o[0], w-1)]
			}
			f.Y[y*w+x] = byte(s / 4)
			d[y*w+x] = -12
			if x >= 120 && x < 200 && y >= 50 && y < 130 {
				d[y*w+x] = 24
			}
		}
	}
	for i := range f.Cb {
		f.Cb[i], f.Cr[i] = 128, 128
	}
	l, r := Pair(f, d)
	m := Match(l, r)
	var bg, sq []float32
	valid := 0
	for y := range m.H {
		for x := range m.W {
			i := y*m.W + x
			if !m.Valid[i] {
				continue
			}
			valid++
			// the square in the left eye: 12 pixels right of the source's
			X, Y := 2*x, 2*y
			switch {
			case X >= 120+12+8 && X < 200+12-8 && Y >= 58 && Y < 122:
				sq = append(sq, m.D[i])
			case X >= 40 && X < 280 && (Y < 40 || Y >= 140):
				bg = append(bg, m.D[i])
			}
		}
	}
	if share := float64(valid) / float64(m.W*m.H); share < 0.7 {
		t.Errorf("%.0f%% of the matches valid, want 70%% or more", 100*share)
	}
	med := func(v []float32) float32 { slices.Sort(v); return v[len(v)/2] }
	if len(bg) == 0 || len(sq) == 0 {
		t.Fatalf("no valid matches: background %d, square %d", len(bg), len(sq))
	}
	if b := med(bg); b < -13 || b > -11 {
		t.Errorf("background %.2f, want -12", b)
	}
	if s := med(sq); s < 23 || s > 25 {
		t.Errorf("square %.2f, want 24", s)
	}
}
