package stereo

import (
	"testing"

	"github.com/brunoga/bdtools/mvc"
)

// scene is a textured background 10 pixels behind the screen with a bright
// square 20 in front of it: its picture and disparity.
func scene(w, h int) (*mvc.Frame, []float32) {
	f := newFrame(w, h)
	d := make([]float32, w*h)
	for y := range h {
		for x := range w {
			f.Y[y*w+x] = byte(40 + (x*7+y*3)%60) // texture, never 250
			d[y*w+x] = -10
			if x >= 40 && x < 60 && y >= 10 && y < 30 {
				f.Y[y*w+x], d[y*w+x] = 250, 20
			}
		}
	}
	for i := range f.Cb {
		f.Cb[i], f.Cr[i] = 128, 128
	}
	return f, d
}

// The square moves by half its disparity in each eye (right in the left
// eye, left in the right one), the background the other way by half its
// own; the square stays whole, in front; what the square uncovered is
// filled with background, not with the square.
func TestPairShiftsByDepth(t *testing.T) {
	const w, h = 120, 40
	f, d := scene(w, h)
	l, r := Pair(f, d)
	for _, c := range []struct {
		name  string
		eye   *mvc.Frame
		shift int
	}{{"left", l, 10}, {"right", r, -10}} {
		row := c.eye.Y[20*w : 21*w]
		for x := 40 + c.shift; x < 60+c.shift; x++ {
			if row[x] != 250 {
				t.Errorf("%s: square pixel at %d is %d, want 250", c.name, x, row[x])
			}
		}
		// the background beside the square, its own shift (-5 in the
		// left eye): the same texture as the source's
		bg := -c.shift / 2
		for _, x := range []int{10, 100} {
			if got, want := row[x], f.Y[20*w+x-bg]; got != want {
				t.Errorf("%s: background at %d is %d, want the source's %d at %d", c.name, x, got, want, x-bg)
			}
		}
		for x := range w {
			// the square (shifted) and the strip it uncovered (the
			// background beside it shifted the other way)
			if c.shift > 0 && x >= 35 && x < 70 || c.shift < 0 && x >= 30 && x < 65 {
				continue
			}
			if row[x] == 250 {
				t.Errorf("%s: the square smeared to %d", c.name, x)
			}
		}
	}
	// a row through the background only is the background shifted
	row := l.Y[0:w]
	for x := 10; x < w-10; x++ {
		if row[x] != f.Y[x+5] {
			t.Fatalf("left eye's top row at %d: %d, want %d", x, row[x], f.Y[x+5])
		}
	}
}

// A hole is filled from its farther side.
func TestFillHolesFromTheBackground(t *testing.T) {
	v := []float32{1, 2, 3, 0, 0, 0, 9, 9}
	z := []float32{-5, -5, -5, 0, 0, 0, 20, 20} // left far, right near
	set := []bool{true, true, true, false, false, false, true, true}
	fillHoles(v, z, set)
	for i, want := range []float32{1, 2, 3, 3, 3, 3, 9, 9} {
		if v[i] != want {
			t.Fatalf("filled %v, want the left side's edge carried across", v)
		}
	}
}
