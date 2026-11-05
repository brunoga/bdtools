package av1

import (
	"math/rand/v2"
	"slices"
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

// The residual and reconstruction kernels agree with the Go code.
func TestResidualKernels(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	for _, depth := range []int{8, 10} {
		maxV := int32(1)<<depth - 1
		for _, wh := range [][2]int{{4, 4}, {4, 16}, {8, 8}, {16, 4}, {12, 8}, {32, 32}, {64, 64}, {64, 16}} {
			w, h := wh[0], wh[1]
			const ss = 80
			src, pred := make([]uint16, ss*h), make([]uint16, w*h)
			for i := range src {
				src[i] = uint16(rng.IntN(int(maxV) + 1))
			}
			for i := range pred {
				pred[i] = uint16(rng.IntN(int(maxV) + 1))
			}
			got, want := make([]int32, w*h), make([]int32, w*h)
			residual(src, ss, pred, got, w, h)
			residualGo(src, ss, pred, want, w, h)
			if !slices.Equal(got, want) {
				t.Fatalf("residual %dx%d at %d bits", w, h, depth)
			}
			res := make([]int32, w*h)
			for i := range res {
				res[i] = int32(rng.IntN(int(4*maxV))) - 2*maxV
			}
			a, b := slices.Clone(src), slices.Clone(src)
			reconAdd(a, ss, res, w, h, maxV)
			reconAddGo(b, ss, res, w, h, maxV)
			if !slices.Equal(a, b) {
				t.Fatalf("reconAdd %dx%d at %d bits", w, h, depth)
			}
		}
	}
}
