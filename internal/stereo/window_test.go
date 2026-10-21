package stereo

import "testing"

// Something in front of the screen at the left edge: a strip as wide as
// its disparity blacks the left eye's left side; nothing in front at the
// right edge, so the right eye keeps its right side; and the strip
// narrows slowly when the thing goes.
func TestFloatingWindow(t *testing.T) {
	const w, h = 200, 40
	d := make([]float32, w*h)
	for y := range h {
		for x := range w {
			d[y*w+x] = -4
			if x < 30 {
				d[y*w+x] = 9
			}
		}
	}
	l, r := newFrame(w, h), newFrame(w, h)
	for i := range l.Y {
		l.Y[i], r.Y[i] = 200, 200
	}
	var win window
	win.apply(l, r, d, 0, h)
	for x := range 12 {
		if want := byte(200); x < 9 {
			want = 16
			if l.Y[5*w+x] != want {
				t.Fatalf("left eye at %d: %d, want black", x, l.Y[5*w+x])
			}
		} else if l.Y[5*w+x] != want {
			t.Fatalf("left eye at %d: %d, want the picture", x, l.Y[5*w+x])
		}
	}
	if r.Y[5*w+w-1] != 200 {
		t.Error("the right eye's right side masked with nothing in front there")
	}
	for i := range d {
		d[i] = -4
	}
	win.apply(l, r, d, 0, h)
	if win.left != 9-windowFall {
		t.Errorf("the strip went to %g at once, want it narrowing by %g", win.left, windowFall)
	}
}
