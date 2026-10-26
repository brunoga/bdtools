package main

import (
	"testing"

	"github.com/brunoga/bdtools/internal/stereo"
	"github.com/brunoga/bdtools/mvc"
)

// scene is a textured square (disparity fg) before a textured background
// (bg), seen by two cameras: each eye has the background's texture where
// the other's view of it is hidden, as a real pair has.
func scene(w, h, bg, fg int) (l, r *mvc.Frame, d []float32) {
	noise := func(seed uint32, n int) []byte {
		out := make([]byte, n)
		for i := range out {
			seed = seed*1664525 + 1013904223
			out[i] = byte(40 + (seed>>24)*3/5)
		}
		return out
	}
	back, front := noise(1, (w+64)*h), noise(2, (w+64)*h)
	eye := func(shift float32) (*mvc.Frame, []float32) {
		f := &mvc.Frame{Width: w, Height: h, StrideY: w, StrideC: w / 2, Y: make([]byte, w*h),
			Cb: make([]byte, w*h/4), Cr: make([]byte, w*h/4)}
		for i := range f.Cb {
			f.Cb[i], f.Cr[i] = 128, 128
		}
		dd := make([]float32, w*h)
		for y := range h {
			for x := range w {
				// the scene point this pixel sees: the square's, else the background's
				sx := x - int(shift*float32(fg))
				if y >= h/3 && y < 2*h/3 && sx >= w/3 && sx < 2*w/3 {
					f.Y[y*w+x] = 255 - front[y*(w+64)+sx]/2
					dd[y*w+x] = float32(fg)
					continue
				}
				f.Y[y*w+x] = back[y*(w+64)+x-int(shift*float32(bg))+32]
				dd[y*w+x] = float32(bg)
			}
		}
		return f, dd
	}
	l, d = eye(0)
	r, _ = eye(-1)
	return l, r, d
}

// The detail behind the square's edges is the background's own in a real
// pair, stretched in when the right eye is rendered from the left.
func TestDetail(t *testing.T) {
	l, r, d := scene(640, 360, 4, 40)
	ml, mr := stereo.MatchBoth(l, r)
	real, revealed, ok := detail(l, r, ml, mr)
	if !ok {
		t.Fatal("the real pair: nothing measured")
	}
	if real < 0.6 {
		t.Errorf("the real pair's detail %.3f, want about 1", real)
	}
	if revealed <= 0 {
		t.Errorf("the real pair reveals nothing")
	}
	made, _ := stereo.Render(l, d, -1)
	fl, fr := stereo.MatchBoth(l, made)
	fake, _, ok := detail(l, made, fl, fr)
	if !ok {
		t.Fatal("the rendered pair: nothing measured")
	}
	if fake > 0.3 {
		t.Errorf("the rendered pair's detail %.3f, want it low (stretched)", fake)
	}
}
