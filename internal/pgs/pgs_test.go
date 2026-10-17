package pgs

import (
	"os"
	"path/filepath"
	"testing"
)

// The test subtitles: three shown and each cleared, the one of two lines
// taller than the others.
func TestDecode(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "testdata", "subs", "ocr.sup"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	d := NewDecoder()
	var comps []*Composition
	if err := ReadSup(f, func(pts int64, seg []byte) error {
		if c, ok := d.Segment(pts, seg); ok {
			comps = append(comps, c)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(comps) != 6 {
		t.Fatalf("%d compositions", len(comps))
	}
	var heights []int
	for i, c := range comps {
		if c.Empty() != (i%2 == 1) {
			t.Errorf("composition %d: empty %v", i, c.Empty())
		}
		if c.W != 1920 || c.H != 1080 {
			t.Errorf("plane %dx%d", c.W, c.H)
		}
		if p := c.Draw(); p != nil {
			heights = append(heights, p.H)
			opaque := 0
			for _, a := range p.A {
				if a > 128 {
					opaque++
				}
			}
			if opaque == 0 {
				t.Errorf("composition %d draws nothing", i)
			}
		}
	}
	if len(heights) != 3 || heights[1] < 2*heights[0]-10 {
		t.Errorf("heights %v", heights)
	}
	if comps[2].PTS != 3*90000 {
		t.Errorf("second at %d", comps[2].PTS)
	}
}
