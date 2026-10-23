package stereo

import "testing"

// Each network's depth has its own scale, so its own shape; an unknown
// one (a test's model) gets Small's.
func TestShapeFor(t *testing.T) {
	for _, c := range []struct {
		size    string
		refined bool
		want    Shape
	}{
		{"small", false, DefaultShape}, {"small", true, RefinedShape}, {"", true, RefinedShape},
		{"base", false, BaseShape}, {"base", true, BaseRefinedShape},
		{"large", false, LargeShape}, {"large", true, LargeRefinedShape},
	} {
		if got := ShapeFor(c.size, c.refined); got != c.want {
			t.Errorf("ShapeFor(%q, %v) = %+v, want %+v", c.size, c.refined, got, c.want)
		}
	}
}
