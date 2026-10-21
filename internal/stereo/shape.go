package stereo

import (
	"math"
	"slices"
)

// Shape is how depth becomes disparity.
type Shape struct {
	// Budget is the disparity across the depth's range (its 5th to 95th
	// percentile; what lies beyond goes up to a quarter of it farther), as
	// a fraction of the picture's width.
	Budget float64
	// Screen is where the screen sits in the depth range, 0 the farthest
	// and 1 the nearest: what is nearer comes out of it, what is farther
	// goes behind.
	Screen float64
	// Scale, when not zero, maps the network's depth to disparity as it
	// is, not normalised a picture at a time: Scale times the depth plus
	// Offset, as fractions of the width (Budget and Screen unused).
	Scale, Offset float64
}

// DefaultShape is the 3D discs' depth (native and studio-converted ones,
// measured by tools/stereobench): the network's depth taken as it is,
// 0.12% of the width a unit of it, the farthest 0.75% behind the screen.
// A fit of the discs' disparity by the network's depth over a whole clip
// finds about that on every clip (0.06% to 0.19% a unit, offsets -1.4% to
// +0.1%), and the network's depth is steady from picture to picture, so
// normalising each picture's range (Budget and Screen) only loses: 1.6 dB
// of the made right eye against the disc's.
var DefaultShape = Shape{Scale: 0.0012, Offset: -0.0075}

// Shaper turns a stream's depth maps into disparity: each map's range (its
// 5th to 95th percentile) normalised to 0..1 (what lies beyond kept to a
// quarter of the range more), the
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
	if s.Shape.Scale != 0 {
		out := make([]float32, len(depth))
		a, b := float32(s.Shape.Scale*float64(width)), float32(s.Shape.Offset*float64(width))
		lim := float32(0.04 * float64(width))
		const chunk = 1 << 16
		rows((len(depth)+chunk-1)/chunk, func(k int) {
			for i := k * chunk; i < min((k+1)*chunk, len(depth)); i++ {
				out[i] = min(max(a*depth[i]+b, -lim), lim)
			}
		})
		return out
	}
	sample := make([]float32, 0, len(depth)/61+1)
	for i := 0; i < len(depth); i += 61 { // a prime step: no column pattern
		sample = append(sample, depth[i])
	}
	slices.Sort(sample)
	lo := float64(sample[len(sample)/20])
	hi := float64(sample[len(sample)-1-len(sample)/20])
	if !s.primed {
		s.lo, s.hi, s.primed = lo, hi, true
	} else {
		s.lo += smoothing * (lo - s.lo)
		s.hi += smoothing * (hi - s.hi)
	}
	span := math.Max(s.hi-s.lo, 1e-6)
	budget := s.Shape.Budget * float64(width)
	out := make([]float32, len(depth))
	lo32, inv := float32(s.lo), float32(1/span)
	b, screen := float32(budget), float32(s.Shape.Screen)
	const chunk = 1 << 16
	rows((len(depth)+chunk-1)/chunk, func(k int) {
		for i := k * chunk; i < min((k+1)*chunk, len(depth)); i++ {
			n := min(max((depth[i]-lo32)*inv, -0.25), 1.25)
			out[i] = b * (n - screen)
		}
	})
	return out
}
