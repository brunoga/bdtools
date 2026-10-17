package av1

import (
	"fmt"
	"sync/atomic"
	"testing"
)

// Compound blocks forced, their predictions weighted by distance
// (compound_idx 0), in mini-GOPs whose references are at unequal
// distances: decoded to the reconstruction.
func TestCompoundDistance(t *testing.T) {
	defer func() { dbgForceCompound, dbgForceInter, dbgForceCompDist, dbgNoJntComp = false, false, false, true }()
	dbgForceCompound, dbgForceInter, dbgForceCompDist, dbgNoJntComp = true, true, true, false
	for _, c := range []struct{ w, h, depth, mini int }{{96, 64, 8, 4}, {72, 56, 10, 8}} {
		t.Run(fmt.Sprintf("%dx%d-%d", c.w, c.h, c.depth), func(t *testing.T) {
			o := EncoderOptions{Width: c.w, Height: c.h, BitDepth: c.depth, QIndex: 120, FPSNum: 25, FPSDen: 1, MiniGOP: c.mini}
			p := testPicture(c.w, c.h, c.depth, 3)
			var pics []*Source
			for i := range c.mini + 1 {
				pics = append(pics, shifted(p, i, i/3))
			}
			var n atomic.Int64
			checkDecodesWith(t, o, pics, func(e *Encoder) {
				e.frameHook = func(f *frameEnc) {
					for _, v := range f.compIdx {
						if v == 0 {
							n.Add(1)
						}
					}
				}
			})
			if n.Load() == 0 {
				t.Fatal("no block weighted by distance")
			}
		})
	}
}

// The distance weights process, worked by hand: the nearer reference
// weighs more (FwdWeight is the first's), equal distances 7 and 9.
func TestDistWeights(t *testing.T) {
	fp := &frameParams{orderHint: 4}
	for _, c := range []struct {
		h0, h1 int
		want   [2]int32
	}{
		{3, 5, [2]int32{7, 9}}, {2, 5, [2]int32{5, 11}}, {3, 8, [2]int32{13, 3}}, {0, 8, [2]int32{7, 9}}, {1, 5, [2]int32{4, 12}}, {4, 8, [2]int32{13, 3}},
	} {
		fp.refHints[0], fp.refHints[BWDREF_FRAME-LAST_FRAME] = c.h0, c.h1
		if got := fp.distWeights([2]int{LAST_FRAME, BWDREF_FRAME}); got != c.want {
			t.Errorf("hints %d, %d: %v, want %v", c.h0, c.h1, got, c.want)
		}
	}
}
