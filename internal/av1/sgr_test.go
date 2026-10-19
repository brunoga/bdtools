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

// sgrABGo is sgrAB without AVX2.
func sgrABGo(sum, sq []int32, w, h, r, eps, bitDepth int, odd bool, a, b []int32) {
	sgrABFrom(sum, sq, w, h, r, eps, bitDepth, odd, a, b, false)
}

// sgrAB (AVX2 rows where there are) matches sgrABGo: both radii, 8 and 10
// bits, every strength, widths with and without a tail.
func TestSgrABMatches(t *testing.T) {
	rng := rand.New(rand.NewPCG(2, 6))
	for _, depth := range []int{8, 10} {
		maxV := int32(1)<<depth - 1
		for _, w := range []int{5, 6, 14, 62} {
			for pass := range 2 {
				r := 2 - pass
				n := int32((2*r + 1) * (2*r + 1))
				const h = 5
				size := (w + 2) * (h + 2)
				sum, sq := make([]int32, size), make([]int32, size)
				for i := range sum {
					// a box of n samples about a mean, spread or flat
					m, d := rng.Int32N(maxV+1), rng.Int32N(maxV/4+1)
					sum[i] = n * m
					sq[i] = n*m*m + n*d*d
					if rng.IntN(4) == 0 {
						sq[i] = n * m * m
					}
				}
				for _, eps := range sgrEps[pass] {
					ga, gb := make([]int32, size), make([]int32, size)
					wa, wb := make([]int32, size), make([]int32, size)
					sgrAB(sum, sq, w, h, r, eps, depth, pass == 0, ga, gb)
					sgrABGo(sum, sq, w, h, r, eps, depth, pass == 0, wa, wb)
					if !slices.Equal(ga, wa) || !slices.Equal(gb, wb) {
						t.Fatalf("%d bits width %d pass %d eps %d: differs", depth, w, pass, eps)
					}
				}
			}
		}
	}
}
