// Package stereo makes a stereo pair of a 2D picture and its disparity:
// each eye is the picture shifted by half the disparity, rendered row by
// row so that what is nearer covers what is behind it, and what an eye
// sees that the picture does not (behind a nearer thing's edge) filled
// from the farther side.
package stereo

import (
	"math"
	"runtime"
	"sync"

	"github.com/brunoga/bdtools/mvc"
)

// gapPixels is how far apart two neighbouring pixels may land in an eye and
// still be joined (interpolated between): farther, across a step in depth,
// what lies between is a disocclusion, a hole to fill, not a stretch.
const gapPixels = 1.5

// Pair renders the two eyes of f (8-bit 4:2:0) for its disparity, one
// value a luma pixel (d[y*Width+x], in pixels between the eyes: positive
// nearer than the screen, the left eye's view of a point that far right
// of the right eye's).
func Pair(f *mvc.Frame, d []float32) (left, right *mvc.Frame) {
	left, _ = Render(f, d, 0.5)
	right, _ = Render(f, d, -0.5)
	return left, right
}

func newFrame(w, h int) *mvc.Frame {
	cw, ch := (w+1)/2, (h+1)/2
	return &mvc.Frame{Y: make([]byte, w*h), Cb: make([]byte, cw*ch), Cr: make([]byte, cw*ch),
		StrideY: w, StrideC: cw, Width: w, Height: h, PTS: -1}
}

// Render is one eye: each pixel of f moved by sign times its disparity d
// (0.5 the left eye of a pair f is the middle of, -1 the right eye of a
// pair f is the left eye of); holes says which of its luma pixels were
// filled, seen by no pixel of f.
func Render(f *mvc.Frame, d []float32, sign float32) (eye *mvc.Frame, holes []bool) {
	w, h := f.Width, f.Height
	cw, ch := (w+1)/2, (h+1)/2
	out := newFrame(w, h)
	out.PTS = f.PTS
	holes = make([]bool, w*h)
	scratchRows(h+2*ch, func(r int, rs *rowScratch) {
		switch {
		case r < h:
			rs.render(f.Y[r*f.StrideY:r*f.StrideY+w], d[r*w:(r+1)*w], sign, out.Y[r*w:(r+1)*w])
			for x, s := range rs.seen {
				holes[r*w+x] = !s
			}
		default:
			c := r - h
			plane, outPlane := f.Cb, out.Cb
			if c >= ch {
				c -= ch
				plane, outPlane = f.Cr, out.Cr
			}
			// the chroma row's disparity: its luma row's, every other
			// sample, halved (the plane is half as wide)
			lr := min(2*c, h-1)
			if cap(rs.dc) < cw {
				rs.dc = make([]float32, cw)
			}
			dc := rs.dc[:cw]
			for x := range dc {
				dc[x] = d[lr*w+min(2*x, w-1)] / 2
			}
			rs.render(plane[c*f.StrideC:c*f.StrideC+cw], dc, sign, outPlane[c*cw:(c+1)*cw])
		}
	})
	return out, holes
}

// scratchRows runs f for each row over the CPUs, four rows at a time,
// each worker with its own scratch.
func scratchRows(n int, f func(r int, s *rowScratch)) {
	workers := min(runtime.GOMAXPROCS(0), n)
	var wg sync.WaitGroup
	var mu sync.Mutex
	next := 0
	for range workers {
		wg.Go(func() {
			var s rowScratch
			for {
				mu.Lock()
				r := next
				next += 4
				mu.Unlock()
				if r >= n {
					return
				}
				for i := r; i < min(r+4, n); i++ {
					f(i, &s)
				}
			}
		})
	}
	wg.Wait()
}

// rows runs f for each row over the CPUs.
func rows(n int, f func(r int)) {
	workers := min(runtime.GOMAXPROCS(0), n)
	var wg sync.WaitGroup
	var mu sync.Mutex
	next := 0
	for range workers {
		wg.Go(func() {
			for {
				mu.Lock()
				r := next
				next++
				mu.Unlock()
				if r >= n {
					return
				}
				f(r)
			}
		})
	}
	wg.Wait()
}

type rowScratch struct {
	pos, z, v, dc []float32
	set, seen     []bool // seen: set before the holes were filled
}

// render shifts src by sign*d into out: each pair of neighbours is a
// segment, rasterised into the targets it covers (the nearer winning
// where segments fold over each other); a segment stretched across a step
// in depth is left open, and the holes are filled from the farther side.
func (s *rowScratch) render(src []byte, d []float32, sign float32, out []byte) {
	w := len(src)
	if cap(s.pos) < w {
		s.pos, s.z, s.v, s.set = make([]float32, w), make([]float32, w), make([]float32, w), make([]bool, w)
	}
	pos, z, v, set := s.pos[:w], s.z[:w], s.v[:w], s.set[:w]
	for x := range w {
		pos[x] = float32(x) + sign*d[x]
		z[x] = float32(math.Inf(-1))
		set[x] = false
	}
	put := func(X int, val, depth float32) {
		if X < 0 || X >= w || depth <= z[X] {
			return
		}
		z[X], v[X], set[X] = depth, val, true
	}
	for x := range w - 1 {
		p0, p1 := pos[x], pos[x+1]
		a, b := float32(src[x]), float32(src[x+1])
		d0, d1 := d[x], d[x+1]
		lo, hi := min(p0, p1), max(p0, p1)
		if hi-lo > gapPixels {
			// open: each end lands on its own
			put(int(math.Round(float64(p0))), a, d0)
			put(int(math.Round(float64(p1))), b, d1)
			continue
		}
		for X := int(math.Ceil(float64(lo))); float32(X) <= hi; X++ {
			t := float32(0)
			if p1 != p0 {
				t = (float32(X) - p0) / (p1 - p0)
			}
			put(X, a+(b-a)*t, d0+(d1-d0)*t)
		}
	}
	put(int(math.Round(float64(pos[w-1]))), float32(src[w-1]), d[w-1])
	if cap(s.seen) < w {
		s.seen = make([]bool, w)
	}
	s.seen = s.seen[:w]
	copy(s.seen, set)
	fillHoles(v, z, set)
	for X := range w {
		out[X] = byte(min(max(v[X]+0.5, 0), 255))
	}
}

// fillHoles fills the runs of unset targets from their farther side: its
// texture mirrored across the hole's edge (continued, not one pixel
// smeared); at the row's ends, from the side there is.
func fillHoles(v, z []float32, set []bool) {
	w := len(v)
	for x := 0; x < w; {
		if set[x] {
			x++
			continue
		}
		a := x
		for x < w && !set[x] {
			x++
		}
		b := x // the hole is [a, b)
		left, right := a-1, b
		useRight := false
		switch {
		case left < 0 && right >= w:
			continue // nothing set in the row
		case left < 0:
			useRight = true
		case right >= w:
			useRight = false
		default:
			useRight = z[right] < z[left] //nolint:gosec // both within the row here: the farther side
		}
		for X := a; X < b; X++ {
			var src int
			if useRight {
				src = min(right+(b-1-X), w-1)
				for src > right && !set[src] {
					src--
				}
			} else {
				src = max(left-(X-a), 0)
				for src < left && !set[src] {
					src++
				}
			}
			v[X], z[X] = v[src], z[src]
		}
		for X := a; X < b; X++ {
			set[X] = true
		}
	}
}
