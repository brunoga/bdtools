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
	// Far, when not zero, is the farthest behind the screen anything may
	// go, a fraction of the width: what goes past FarKnee of it is eased
	// towards it, never reaching it (FarLimit).
	Far float64
	// Curve, when not zero or one, bends the depth within the shot's
	// range (its 5th to 95th percentile, steadied over the shot as
	// Budget's is), with Scale: the nearness n of a point in it (0 the
	// farthest, 1 the nearest) becomes n to the power Curve (Curved), the
	// ends staying. Below 1 what is between comes forward, towards the
	// nearest, as the rows of a crowd a network stacks away behind each
	// other; above 1 it goes back.
	Curve float64
}

// Curved is the nearness n (0 the farthest of a shot's range, 1 the
// nearest) bent by the power curve: n to it within the range, the same
// slope as at the range's ends beyond them (1 below, curve above), so it
// stays monotone and continuous; 1 (or 0) leaves it.
func Curved(n, curve float32) float32 {
	switch {
	case curve == 0 || curve == 1 || n <= 0:
		return n
	case n >= 1:
		return 1 + curve*(n-1)
	}
	return float32(math.Pow(float64(n), float64(curve)))
}

// curves reports whether a shape bends the depth (Curve set, with Scale).
func (s Shape) curves() bool { return s.Scale != 0 && s.Curve != 0 && s.Curve != 1 }

// FarKnee is where, as a share of Shape.Far, the depth behind the screen
// starts to be eased.
const FarKnee = 0.7

// FarLimit is a disparity v kept short of far (pixels, 0 for no limit)
// behind the screen: past knee (FarKnee of far) eased as x/(1+x/r), x what
// lies past it and r the rest of the way, which leaves the slope 1 there
// and tends to far. Its steps rounded one at a time, as the GPU's
// disparity kernel takes them.
func FarLimit(v, far, knee float32) float32 {
	if far <= 0 || -v <= knee {
		return v
	}
	r := far - knee
	x := -v - knee
	return -(knee + x/(1+x/r))
}

// ScreenFar is Shape.Far for a screen's size (its diagonal, in inches, of
// a 16:9 picture): the eyes' separation, 63 mm, as a fraction of its
// width; past that the eyes would turn outwards to see what is behind the
// screen.
func ScreenFar(diagonal float64) float64 {
	const eyes = 0.063
	width := diagonal * 0.0254 * 16 / math.Hypot(16, 9)
	return eyes / width
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

// TunedShape is DefaultShape for Small's depth with its head learnt from
// the 3D discs (depth.BuiltinTail), found as DefaultShape was, on the
// benchmark's twelve clips and checked on the wide set (fifty clips of
// twenty-five other films), the glass clips and Avatar 3. The whole head
// learnt (not the tail alone) reads the scene a little differently and
// wants a little less scale and a shallower offset: 0.115% a unit and
// -0.82% of the width against the tail's 0.13% and -0.90%. The made
// right eye: the twelve clips 27.63 dB and VMAF 32.0 (the tail alone at
// its shape 27.35 and 31.1), the wide set 28.66 and 36.2 (28.59 and
// 35.4), the glass clips 28.23 and 48.1 (28.11 and 45.6), Avatar 3 25.65
// and 29.2 (25.83 and 31.6: that film wants a shallower shape still,
// 0.10% and -0.60%, where it gives 25.90 and the other sets lose a dB
// or more; one fixed shape is a compromise between films). Deeper
// offsets lose on every set: the discs go farther back than this (their
// 5th percentile -23 px at 1920 against ours -17), but a fixed mapping
// placing the far end deeper misses more than it gains.
var TunedShape = Shape{Scale: 0.00115, Offset: -0.0082}

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

// steady takes a depth map's range (its 5th to 95th percentile, of every
// 61st value) into the shot's: as it is for the first map of a shot, a
// part of the way for the rest.
func (s *Shaper) steady(depth []float32) {
	sample := make([]float32, 0, len(depth)/61+1)
	for i := 0; i < len(depth); i += 61 { // a prime step: no column pattern
		sample = append(sample, depth[i])
	}
	slices.Sort(sample)
	s.Steady(float64(sample[len(sample)/20]), float64(sample[len(sample)-1-len(sample)/20]))
}

// Steady takes a map's range, lo to hi, into the shot's, as steady does.
func (s *Shaper) Steady(lo, hi float64) {
	if !s.primed {
		s.lo, s.hi, s.primed = lo, hi, true
	} else {
		s.lo += smoothing * (lo - s.lo)
		s.hi += smoothing * (hi - s.hi)
	}
}

// Range is the shot's range so far, lo to hi (Steady's), and whether
// there is one.
func (s *Shaper) Range() (lo, hi float64, ok bool) { return s.lo, s.hi, s.primed }

// CurveArgs is the shape's curve as the disparity kernels take it: the
// shot's range (its farthest and its span, in the depth's units) and the
// power; the power 0 when the shape does not bend the depth, or the
// shot's range is not known yet.
func (s *Shaper) CurveArgs() (lo, span, curve float32) {
	if !s.Shape.curves() || !s.primed {
		return 0, 1, 0
	}
	return float32(s.lo), float32(math.Max(s.hi-s.lo, 1e-6)), float32(s.Shape.Curve)
}

// Disparity is the disparity of a depth map (larger nearer, as the depth
// network gives it), in pixels of a picture width wide.
func (s *Shaper) Disparity(depth []float32, width int) []float32 {
	if s.Shape.Scale != 0 {
		out := make([]float32, len(depth))
		a, b := float32(s.Shape.Scale*float64(width)), float32(s.Shape.Offset*float64(width))
		lim := float32(0.04 * float64(width))
		far := float32(s.Shape.Far * float64(width))
		knee := far * FarKnee
		var lo, span, curve float32
		if s.Shape.curves() {
			s.steady(depth)
			lo, span, curve = s.CurveArgs()
		}
		const chunk = 1 << 16
		rows((len(depth)+chunk-1)/chunk, func(k int) {
			for i := k * chunk; i < min((k+1)*chunk, len(depth)); i++ {
				d := depth[i]
				if curve != 0 {
					d = lo + span*Curved((d-lo)/span, curve)
				}
				out[i] = min(max(FarLimit(a*d+b, far, knee), -lim), lim)
			}
		})
		return out
	}
	s.steady(depth)
	span := math.Max(s.hi-s.lo, 1e-6)
	budget := s.Shape.Budget * float64(width)
	out := make([]float32, len(depth))
	lo32, inv := float32(s.lo), float32(1/span)
	b, screen := float32(budget), float32(s.Shape.Screen)
	far := float32(s.Shape.Far * float64(width))
	knee := far * FarKnee
	const chunk = 1 << 16
	rows((len(depth)+chunk-1)/chunk, func(k int) {
		for i := k * chunk; i < min((k+1)*chunk, len(depth)); i++ {
			n := min(max((depth[i]-lo32)*inv, -0.25), 1.25)
			out[i] = FarLimit(b*(n-screen), far, knee)
		}
	})
	return out
}
