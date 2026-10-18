package av1

import (
	"math/rand/v2"
	"testing"
)

// widen with AVX2 as in Go, every length and shift.
func TestWidenMatches(t *testing.T) {
	src := make([]uint16, 131)
	for i := range src {
		src[i] = uint16(i*7919) & 4095
	}
	for n := range len(src) {
		for _, sh := range []uint{0, 2, 4, 7} {
			a, b := make([]int32, n), make([]int32, n)
			widen(a, src, sh)
			widenGo(b, src, sh)
			for i := range a {
				if a[i] != b[i] {
					t.Fatalf("n %d sh %d at %d: %d, want %d", n, sh, i, a[i], b[i])
				}
			}
		}
	}
}

// The compound average with AVX2 as in Go, plain and distance weighted,
// for every length, the sums out of range both ways.
func TestCompAvgMatches(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	p0, p1 := make([]int32, 131), make([]int32, 131)
	for i := range p0 {
		p0[i], p1[i] = rng.Int32N(1<<16)-1<<13, rng.Int32N(1<<16)-1<<13
	}
	for n := range len(p0) {
		for _, c := range []struct {
			w0, w1 int32
			sh     uint
			maxV   int32
		}{{1, 1, 5, 255}, {1, 1, 3, 1023}, {9, 7, 8, 255}, {13, 3, 6, 1023}} {
			a, b := make([]uint16, n), make([]uint16, n)
			compAvg(a, p0, p1, c.w0, c.w1, c.sh, c.maxV)
			compAvgGo(b, p0, p1, c.w0, c.w1, c.sh, c.maxV)
			for i := range a {
				if a[i] != b[i] {
					t.Fatalf("n %d %+v at %d: %d, want %d", n, c, i, a[i], b[i])
				}
			}
		}
	}
}

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
						maxV := int32(1)<<bd - 1
						p, q := make([]uint16, w), make([]uint16, w)
						filterColClip(p, mid, w, k, lo, hi, add, sh, maxV, a)
						filterColClipGo(q, mid, w, k, lo, hi, add, sh, maxV, b)
						for c := range p {
							if p[c] != q[c] {
								t.Fatalf("clipped col w %d filter %d frac %d: %d, want %d", w, fl, frac, p[c], q[c])
							}
						}
					}
				}
			}
		}
	}
}
