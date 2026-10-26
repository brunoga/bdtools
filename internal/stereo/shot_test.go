package stereo

import (
	"testing"

	"github.com/brunoga/bdtools/internal/depth"
)

// A picture's Shot is its shape and flatness: a stretch graded by hand
// in place of the converter's own.
func TestShot(t *testing.T) {
	m, err := depth.Load("../depth/testdata/tiny.pth")
	if err != nil {
		t.Fatal(err)
	}
	base := Shape{Scale: 0.004, Offset: -0.01}
	c := NewConverter(m, base)
	c.KeepCredits = true
	c.Shot = func(pts int64) (Shape, bool) {
		switch pts {
		case 1:
			return Shape{Scale: 2 * base.Scale, Offset: 2 * base.Offset}, false
		case 2:
			return base, true
		}
		return base, false
	}
	f := newFrame(64, 48)
	for i := range f.Y {
		f.Y[i] = byte(40 + i%64*3)
	}
	w := float32(f.Width)
	for pts, k := range []float32{1, 2, 0} {
		f.PTS = int64(pts)
		dep, disp, err := c.Next(f)
		if err != nil {
			t.Fatal(err)
		}
		a, b, lim := k*float32(base.Scale)*w, k*float32(base.Offset)*w, 0.04*w
		for i, v := range disp {
			if want := min(max(a*dep[i]+b, -lim), lim); v-want > 1e-4 || want-v > 1e-4 {
				t.Fatalf("picture %d, pixel %d: %g, want %g (the depth %g times)", pts, i, v, want, k)
			}
		}
	}
}
