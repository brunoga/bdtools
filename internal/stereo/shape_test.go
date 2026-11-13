package stereo

import (
	"math"
	"testing"
)

// Each network's depth has its own scale, so its own shape; an unknown
// one (a test's model) gets Small's.
func TestShapeFor(t *testing.T) {
	for _, c := range []struct {
		size    string
		refined bool
		tuned   bool
		want    Shape
	}{
		{"small", false, false, DefaultShape}, {"small", true, false, RefinedShape}, {"", true, false, RefinedShape},
		{"small", false, true, TunedShape}, {"small", true, true, TunedShape},
		{"base", false, false, BaseShape}, {"base", true, false, BaseRefinedShape}, {"base", false, true, BaseShape},
		{"large", false, false, LargeShape}, {"large", true, false, LargeRefinedShape},
	} {
		if got := ShapeFor(c.size, c.refined, c.tuned); got != c.want {
			t.Errorf("ShapeFor(%q, %v, %v) = %+v, want %+v", c.size, c.refined, c.tuned, got, c.want)
		}
	}
}

// Far eases what lies behind the knee towards the limit, smoothly and
// never past it; the nearer is left as it is.
func TestFarLimit(t *testing.T) {
	const far, knee = 40, 28
	for _, v := range []float32{20, 0, -10, -28} {
		if got := FarLimit(v, far, knee); got != v {
			t.Errorf("FarLimit(%g) = %g, not as it was", v, got)
		}
	}
	prev := float32(-knee)
	for v := float32(-knee - 1); v > -1000; v -= 7 {
		got := FarLimit(v, far, knee)
		if got <= -far || got >= prev {
			t.Fatalf("FarLimit(%g) = %g: past the limit, or not farther than %g", v, got, prev)
		}
		prev = got
	}
	// the slope 1 at the knee: no kink
	if d := FarLimit(-knee-0.01, far, knee) + knee; d > -0.0099 || d < -0.0101 {
		t.Errorf("just past the knee: %g", d)
	}
	if got := FarLimit(-100, 0, 0); got != -100 {
		t.Errorf("no limit: %g", got)
	}
}

func TestScreenFar(t *testing.T) {
	// a 65-inch screen is 1.44 m wide: 63 mm is 4.4% of it
	if f := ScreenFar(65); f < 0.0435 || f > 0.0442 {
		t.Errorf("65 inches: %g", f)
	}
	// ten metres wide: 0.63%
	if f := ScreenFar(10 / 0.0254 / (16 / math.Hypot(16, 9))); math.Abs(f-0.0063) > 1e-6 {
		t.Errorf("10 m wide: %g", f)
	}
	s := Shaper{Shape: Shape{Scale: 0.001, Offset: -0.05, Far: 0.02}}
	for _, d := range s.Disparity([]float32{0, 10, 100}, 1000) {
		if d <= -20 {
			t.Errorf("a disparity %g past the screen's limit -20", d)
		}
	}
}

// A curve bends the nearness within a shot's range: 1 leaves it, the ends
// stay, below 1 what is between comes forward, above 1 it goes back, and
// it stays monotone past the ends.
func TestCurved(t *testing.T) {
	for _, n := range []float32{-0.2, 0, 0.3, 1, 1.2} {
		if got := Curved(n, 1); got != n {
			t.Errorf("Curved(%g, 1) = %g", n, got)
		}
		if got := Curved(n, 0); got != n {
			t.Errorf("Curved(%g, 0) = %g", n, got)
		}
	}
	for _, curve := range []float32{0.3, 0.5, 2} {
		if Curved(0, curve) != 0 || Curved(1, curve) != 1 {
			t.Errorf("curve %g: the ends moved: %g, %g", curve, Curved(0, curve), Curved(1, curve))
		}
		mid := Curved(0.5, curve)
		if (curve < 1) != (mid > 0.5) {
			t.Errorf("curve %g: the middle at %g", curve, mid)
		}
		last := float32(-1)
		for n := float32(-0.3); n <= 1.3; n += 0.01 {
			if got := Curved(n, curve); got <= last {
				t.Fatalf("curve %g: not monotone at %g: %g after %g", curve, n, got, last)
			} else {
				last = got
			}
		}
	}
	// the shaper: a curve below 1 moves the middle of a shot's range nearer,
	// the ends of it staying where the linear mapping put them
	depth := make([]float32, 1000)
	for i := range depth {
		depth[i] = float32(i) / 100 // 0 to 10
	}
	plain := Shaper{Shape: Shape{Scale: 0.001, Offset: -0.005}}
	bent := Shaper{Shape: Shape{Scale: 0.001, Offset: -0.005, Curve: 0.4}}
	p, b := plain.Disparity(depth, 1000), bent.Disparity(depth, 1000)
	lo, hi, ok := bent.Range()
	if !ok || lo > 0.6 || hi < 9.4 {
		t.Fatalf("the range %g to %g, %v", lo, hi, ok)
	}
	for i := range depth {
		switch n := (float64(depth[i]) - lo) / (hi - lo); {
		case n < 0 || n > 1:
			continue
		case n == 0 || n == 1:
			if math.Abs(float64(p[i]-b[i])) > 1e-5 {
				t.Errorf("depth %g, at the range's end: bent %g, plain %g", depth[i], b[i], p[i])
			}
		case n > 0.05 && n < 0.95:
			if b[i] <= p[i] {
				t.Errorf("depth %g: bent %g not nearer than plain %g", depth[i], b[i], p[i])
			}
		}
	}
	if _, _, ok := plain.Range(); ok {
		t.Error("the plain shaper measured a range")
	}
}
