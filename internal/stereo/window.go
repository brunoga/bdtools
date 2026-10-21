package stereo

import (
	"math"
	"slices"

	"github.com/brunoga/bdtools/mvc"
)

// Floating windows. Something nearer than the screen that the picture's
// left or right edge cuts reads wrong: the edge, at the screen, hides what
// is in front of it. Stereographers move that edge of the window nearer:
// a black strip down that side of one eye (the left eye's left side, the
// right eye's right side), as wide as the disparity of what touches it,
// puts the edge at that depth.

// windowEdge is how much of the picture's sides is looked at (a fraction
// of the width); windowRise and windowFall how fast a strip widens and
// narrows within a shot (pixels a frame): a cut takes it as it is.
const (
	windowEdge = 0.03
	windowRise = 2.0
	windowFall = 0.5
)

// window is a stream's floating window: each side's strip now, in pixels.
type window struct {
	left, right float64
	primed      bool
}

// reset starts a new shot.
func (w *window) reset() { w.primed = false }

// apply masks the strips of a pair for its disparity d over the active
// rows [top, bottom).
func (w *window) apply(l, r *mvc.Frame, d []float32, top, bottom int) {
	width := l.Width
	edge := max(1, int(windowEdge*float64(width)))
	need := func(x0, x1 int) float64 {
		var near []float32
		for y := top; y < bottom; y += 2 {
			for x := x0; x < x1; x++ {
				near = append(near, d[y*width+x])
			}
		}
		if len(near) == 0 {
			return 0
		}
		slices.Sort(near)
		return math.Max(float64(near[len(near)-1-len(near)/50]), 0)
	}
	nl, nr := need(0, edge), need(width-edge, width)
	follow := func(cur, target float64) float64 {
		if !w.primed {
			return target
		}
		if target > cur {
			return math.Min(target, cur+windowRise)
		}
		return math.Max(target, cur-windowFall)
	}
	w.left, w.right = follow(w.left, nl), follow(w.right, nr)
	w.primed = true
	mask(l, 0, int(math.Ceil(w.left)))
	mask(r, width-int(math.Ceil(w.right)), width)
}

// mask blacks columns [x0, x1) of a frame (limited range black).
func mask(f *mvc.Frame, x0, x1 int) {
	x0, x1 = max(x0, 0), min(x1, f.Width)
	if x0 >= x1 {
		return
	}
	for y := range f.Height {
		row := f.Y[y*f.StrideY:]
		for x := x0; x < x1; x++ {
			row[x] = 16
		}
	}
	c0, c1 := x0/2, (x1+1)/2
	for y := range (f.Height + 1) / 2 {
		cb, cr := f.Cb[y*f.StrideC:], f.Cr[y*f.StrideC:]
		for x := c0; x < min(c1, (f.Width+1)/2); x++ {
			cb[x], cr[x] = 128, 128
		}
	}
}
