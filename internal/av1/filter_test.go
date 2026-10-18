package av1

import (
	"math/rand/v2"
	"testing"
)

// The filters with AVX2 (where there is) as in Go, for every kernel,
// width and rounding.
func TestFiltersMatch(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	row := make([]uint16, 128+7)
	mid := make([]int32, 135*128)
	for _, w := range []int{4, 8, 16, 24, 32, 64, 128} {
		for fl := range subpelFilters {
			for frac := 1; frac < 16; frac++ {
				for _, bd := range []int{8, 10, 12} {
					for i := range row {
						row[i] = uint16(rng.IntN(1 << bd))
					}
					for i := range mid {
						mid[i] = int32(rng.IntN(1<<(bd+4))) - 1<<(bd+2)
					}
					k := &subpelFilters[fl][frac]
					tp := subpelTaps[fl][frac]
					lo, hi := int(tp[0]), int(tp[1])
					for _, sh := range []uint{3, 5, 7, 11} {
						add := int32(1) << (sh - 1)
						a, b := make([]int32, w), make([]int32, w)
						filterRow(a, row[:w+7], k, lo, hi, add, sh)
						filterRowGo(b, row[:w+7], k, lo, hi, add, sh)
						for c := range a {
							if a[c] != b[c] {
								t.Fatalf("row w %d filter %d frac %d: %d, want %d", w, fl, frac, a[c], b[c])
							}
						}
						filterCol(a, mid, w, k, lo, hi, add, sh)
						filterColGo(b, mid, w, k, lo, hi, add, sh)
						for c := range a {
							if a[c] != b[c] {
								t.Fatalf("col w %d filter %d frac %d: %d, want %d", w, fl, frac, a[c], b[c])
							}
						}
					}
				}
			}
		}
	}
}
