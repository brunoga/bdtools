package stereo

import (
	"math"
	"slices"
)

// Shape is how depth becomes disparity.
type Shape struct {
	// Budget is the disparity from the farthest to the nearest, as a
	// fraction of the picture's width.
	Budget float64
	// Screen is where the screen sits in the depth range, 0 the farthest
	// and 1 the nearest: what is nearer comes out of it, what is farther
	// goes behind.
	Screen float64
}

// DefaultShape is a gentle depth: 3% of the width in all, most of it
// behind the screen (2.1%), 0.9% in front.
var DefaultShape = Shape{Budget: 0.03, Screen: 0.7}

// Shaper turns a stream's depth maps into disparity: each map's range (its
// 1st to 99th percentile, the extremes left out) normalised to 0..1, the
// range steadied over the frames of a shot, then the shape's budget and
// screen applied.
type Shaper struct {
	Shape  Shape
	lo, hi float64
	primed bool
}

// Reset starts a new shot: the next map's range is taken as it is.
func (s *Shaper) Reset() { s.primed = false }

// smoothing is how much of a new map's range is taken each frame within a
// shot.
const smoothing = 0.15

// Disparity is the disparity of a depth map (larger nearer, as the depth
// network gives it), in pixels of a picture width wide.
func (s *Shaper) Disparity(depth []float32, width int) []float32 {
	sample := make([]float32, 0, len(depth)/16+1)
	for i := 0; i < len(depth); i += 16 {
		sample = append(sample, depth[i])
	}
	slices.Sort(sample)
	lo := float64(sample[len(sample)/100])
	hi := float64(sample[len(sample)-1-len(sample)/100])
	if !s.primed {
		s.lo, s.hi, s.primed = lo, hi, true
	} else {
		s.lo += smoothing * (lo - s.lo)
		s.hi += smoothing * (hi - s.hi)
	}
	span := math.Max(s.hi-s.lo, 1e-6)
	budget := s.Shape.Budget * float64(width)
	out := make([]float32, len(depth))
	for i, v := range depth {
		n := math.Min(math.Max((float64(v)-s.lo)/span, 0), 1)
		out[i] = float32(budget * (n - s.Shape.Screen))
	}
	return out
}
