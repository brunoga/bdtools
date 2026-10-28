//go:build amd64 && !purego

package av1

import (
	"math/rand/v2"
	"testing"
)

// blockInterPred with filterBlock gives the per-row filters' predictions,
// samples and compound dwords, at every bit depth, for blocks inside the
// reference and past its edges, at every sub-sample position and filter.
func TestFilterBlockKernel(t *testing.T) {
	if !useAVX2 {
		t.Skip("no AVX2")
	}
	defer func() { useAVX2 = true }()
	rng := rand.New(rand.NewPCG(3, 4))
	const rw, rh = 200, 150
	for _, bd := range []int{8, 10, 12} {
		ref := &refFrameState{width: rw, height: rh}
		ref.stride[0] = rw
		ref.rec[0] = make([]uint16, rw*rh)
		for i := range ref.rec[0] {
			ref.rec[0][i] = uint16(rng.IntN(1 << bd))
		}
		for trial := range 3000 {
			w := 8 << rng.IntN(5)
			h := 4 << rng.IntN(6)
			w, h = min(w, 128), min(h, 128)
			// sometimes extremes: flat black or white areas
			x := rng.IntN(rw+40) - 20
			y := rng.IntN(rh+40) - 20
			startX := x<<10 + rng.IntN(16)<<6
			startY := y<<10 + rng.IntN(16)<<6
			filt := [2]int{rng.IntN(3), rng.IntN(3)}
			for _, comp := range []bool{false, true} {
				rnd := interRoundingFor(bd, comp)
				var s1, s2 interScratch
				var got, want []uint16
				var got32, want32 []int32
				if comp {
					got32, want32 = make([]int32, w*h), make([]int32, w*h)
				} else {
					got, want = make([]uint16, w*h), make([]uint16, w*h)
				}
				useAVX2 = true
				s1.blockInterPred(ref, 0, startX, startY, w, h, filt, rnd, bd, got, got32, w)
				useAVX2 = false
				s2.blockInterPred(ref, 0, startX, startY, w, h, filt, rnd, bd, want, want32, w)
				useAVX2 = true
				for i := range want {
					if got[i] != want[i] {
						t.Fatalf("bd %d trial %d %dx%d at %d,%d filt %v: sample %d is %d, want %d", bd, trial, w, h, startX, startY, filt, i, got[i], want[i])
					}
				}
				for i := range want32 {
					if got32[i] != want32[i] {
						t.Fatalf("bd %d trial %d %dx%d compound at %d,%d filt %v: value %d is %d, want %d", bd, trial, w, h, startX, startY, filt, i, got32[i], want32[i])
					}
				}
			}
		}
	}
}
