package convert

import (
	"testing"

	"github.com/brunoga/bdtools/mvc"
)

// TestDrawHUD: the text lands in a black box at the bottom left of both
// eyes, the same in each, and drawing it again changes nothing.
func TestDrawHUD(t *testing.T) {
	mk := func() *mvc.Frame {
		w, h := 640, 360
		f := &mvc.Frame{Width: w, Height: h, StrideY: w, StrideC: w / 2, Y: make([]byte, w*h), Cb: make([]byte, w*h/4), Cr: make([]byte, w*h/4)}
		for i := range f.Y {
			f.Y[i] = 100
		}
		return f
	}
	l, r := mk(), mk()
	drawHUD([]*mvc.Frame{l, r}, "0:21:50.0 >  depth 1.50  screen +0.00")
	var dark, bright int
	for y := 0; y < l.Height; y++ {
		for x := 0; x < l.Width; x++ {
			v := l.Y[y*l.Width+x]
			if v != r.Y[y*r.Width+x] {
				t.Fatalf("the eyes differ at %d,%d", x, y)
			}
			if v == 16 {
				dark++
			}
			if v == 235 {
				bright++
			}
		}
	}
	if dark < 1000 || bright < 100 {
		t.Fatalf("dark %d, bright %d", dark, bright)
	}
	if l.Y[0] != 100 || l.Y[(l.Height-1)*l.Width+l.Width-1] != 100 {
		t.Fatal("the picture outside the box changed")
	}
	before := append([]byte(nil), l.Y...)
	drawHUD([]*mvc.Frame{l}, "0:21:50.0 >  depth 1.50  screen +0.00")
	for i := range before {
		if before[i] != l.Y[i] {
			t.Fatalf("drawing again changed pixel %d", i)
		}
	}
}
