package stereo

import "testing"

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
