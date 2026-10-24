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

// RefinedShape is DefaultShape for a refined depth (Converter.Refine):
// found as DefaultShape was, on the benchmark's twelve clips, with weights
// learnt from 27 other films (since 53: the same mapping, the same
// scores or a little better; the made right eye 27.5 dB and VMAF 29.5
// against the discs', DefaultShape's on the depth as it is 26.9 and 25.7;
// on the four glass clips, held out of the choice, 26.8 and 33.4 against
// 25.7 and 26.8).
var RefinedShape = Shape{Scale: 0.0010, Offset: -0.0075}

// TunedShape is DefaultShape for Small's depth with its tail learnt from
// the 3D discs (depth.BuiltinTail), found as DefaultShape was, on the
// benchmark's twelve clips: the made right eye 27.3 dB and VMAF 30.0
// against the refined depth's 27.4 and 31.0 there, but on the clips held
// out of the choice better: the four glass clips 27.8 and 41.9 (26.9 and
// 34.6), two films' 28.6 and 34.9 (27.9 and 31.3) and 24.5 and 25.5 (24.4
// and 25.3). Refining the tuned depth by weights learnt on the depth as
// it was loses.
var TunedShape = Shape{Scale: 0.0012, Offset: -0.0090}

// The shapes of the other networks' depth (depth.Model.Size), each its
// own scale, found as RefinedShape was. Base's depth refined (the weights
// learnt on Small's) on the twelve clips: depth correlation 0.859 against
// Small's 0.832; the made right eye 27.7 dB and VMAF 30.4 (Small's 27.5
// and 29.5), on the glass clips 26.6 and 31.9 (26.8 and 33.4). Base's
// depth as it is: 27.3 and 26.2, the best of three scales tried. Large's is Small's scaled
// by the ratio of their depth's fits to the discs' (155: untuned; Large's
// depth measured no better than Base's).
var (
	BaseShape         = Shape{Scale: 0.00026, Offset: -0.0075}
	BaseRefinedShape  = Shape{Scale: 0.00022, Offset: -0.0065}
	LargeShape        = Shape{Scale: DefaultShape.Scale / 155, Offset: DefaultShape.Offset}
	LargeRefinedShape = Shape{Scale: RefinedShape.Scale / 155, Offset: RefinedShape.Offset}
)

// ShapeFor is the shape of a network's depth (by depth.Model.Size), refined
// or not, tuned (Small's with its tail learnt: TunedShape) or not.
func ShapeFor(size string, refined, tuned bool) Shape {
	if tuned && size != "base" && size != "large" {
		return TunedShape
	}
	pick := func(plain, ref Shape) Shape {
		if refined {
			return ref
		}
		return plain
	}
	switch size {
	case "base":
		return pick(BaseShape, BaseRefinedShape)
	case "large":
		return pick(LargeShape, LargeRefinedShape)
	}
	return pick(DefaultShape, RefinedShape)
}

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
