package av1

import "testing"

// The counts load_cdfs clears are every CDF's last entry: the defaults
// loaded are the defaults (whose counts are 0), and a CDF used then loaded
// has its count 0 and its values kept.
func TestCDFLoaded(t *testing.T) {
	c := newCDFs(100)
	if *c.loaded() != *c {
		t.Fatal("the defaults loaded changed")
	}
	adaptCDF(c.skip[1][:], 1)
	adaptCDF(c.coeffBr[2][1][5][:], 2)
	adaptCDF(c.mvClass[0][1][:], 3)
	l := c.loaded()
	if l.skip[1][2] != 0 || l.coeffBr[2][1][5][4] != 0 || l.mvClass[0][1][MV_CLASSES] != 0 {
		t.Error("a count kept")
	}
	if l.skip[1][0] != c.skip[1][0] || l.coeffBr[2][1][5][0] != c.coeffBr[2][1][5][0] {
		t.Error("a value lost")
	}
	if c.skip[1][2] == 0 {
		t.Error("the source changed")
	}
}
