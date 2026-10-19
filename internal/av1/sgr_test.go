package av1

import (
	"math/rand/v2"
	"slices"
	"testing"
)

// sgrFilter (AVX2 rows where there are) matches sgrFilterGo, both passes,
// at widths with and without a tail.
func TestSgrFilterMatches(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 9))
	for _, w := range []int{1, 7, 8, 13, 64, 67} {
		for pass := range 2 {
			const h = 6
			win := make([]int32, (w+6)*(h+6))
			for i := range win {
				win[i] = rng.Int32N(1 << 12)
			}
			a, b := make([]int32, (w+2)*(h+2)), make([]int32, (w+2)*(h+2))
			for i := range a {
				a[i], b[i] = rng.Int32N(257), rng.Int32N(1<<19)
			}
			got, want := make([]int32, w*h), make([]int32, w*h)
			sgrFilter(win, w, h, pass, a, b, got)
			sgrFilterGo(win, w, h, pass, a, b, want)
			if !slices.Equal(got, want) {
				t.Errorf("width %d pass %d: differs", w, pass)
			}
		}
	}
}
