package av1

import (
	"math/rand/v2"
	"testing"
)

// The kernels agree with the Go code.
func TestSATDKernels(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	a, b := make([]uint16, 80*80), make([]uint16, 80*80)
	for _, depth := range []int{8, 10} {
		for i := range a {
			a[i], b[i] = uint16(rng.IntN(1<<depth)), uint16(rng.IntN(1<<depth))
		}
		for range 200 {
			x, y := rng.IntN(16), rng.IntN(16)
			if g, w := satd8(a[y*80+x:], 80, b[y*80+x:], 80), satd8Go(a[y*80+x:], 80, b[y*80+x:], 80); g != w {
				t.Fatalf("satd8 %d, want %d", g, w)
			}
			if g, w := satd16x8(a[y*80+x:], 80, b[y*80+x:], 80), satd8Go(a[y*80+x:], 80, b[y*80+x:], 80)+satd8Go(a[y*80+x+8:], 80, b[y*80+x+8:], 80); g != w {
				t.Fatalf("satd16x8 %d, want %d", g, w)
			}
			for _, wh := range [][2]int{{8, 8}, {16, 4}, {32, 32}, {64, 16}} {
				if g, w := sadBlock(a[y*80+x:], 80, b[y*80+x:], 80, wh[0], wh[1]), sadBlockGo(a[y*80+x:], 80, b[y*80+x:], 80, wh[0], wh[1]); g != w {
					t.Fatalf("sad %v %d, want %d", wh, g, w)
				}
			}
		}
	}
}
