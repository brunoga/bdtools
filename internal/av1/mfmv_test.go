package av1

import (
	"fmt"
	"sync/atomic"
	"testing"
)

// Steady motion over two mini-GOPs: the later frames' motion fields hold
// projected vectors, and the streams decode to the reconstruction.
func TestRefFrameMvs(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ w, h, depth, mini, n int }{{136, 96, 8, 4, 9}, {104, 72, 10, 8, 10}} {
		t.Run(fmt.Sprintf("%dx%d-%d", c.w, c.h, c.depth), func(t *testing.T) {
			o := EncoderOptions{Width: c.w, Height: c.h, BitDepth: c.depth, QIndex: 100, FPSNum: 25, FPSDen: 1, MiniGOP: c.mini}
			p := testPicture(c.w, c.h, c.depth, 4)
			var pics []*Source
			for i := range c.n {
				pics = append(pics, shifted(p, 3*i, i))
			}
			var projected atomic.Int64
			checkDecodesWith(t, o, pics, func(e *Encoder) {
				e.frameHook = func(f *frameEnc) {
					if f.inter == nil {
						return
					}
					for ref := LAST_FRAME; ref <= ALTREF_FRAME; ref++ {
						for _, m := range f.inter.motionFieldMvs[ref] {
							if m != mfInvalid {
								projected.Add(1)
							}
						}
					}
				}
			})
			if projected.Load() == 0 {
				t.Fatal("no motion vector projected")
			}
		})
	}
}

// The projection of a vector through the frame distances, and the
// positions it may land at.
func TestMvProjection(t *testing.T) {
	for _, c := range []struct {
		m        mv
		num, den int
		want     mv
	}{
		{mv{16, -24}, 1, 1, mv{16, -24}}, {mv{16, -24}, -2, 1, mv{-32, 48}}, {mv{30, 9}, 1, 2, mv{15, 5}},
		{mv{-30, -9}, 1, 2, mv{-15, -5}}, {mv{100, 0}, 3, 4, mv{75, 0}}, {mv{16000, 0}, 4, 1, mv{16383, 0}},
	} {
		if got := mvProjection(c.m, c.num, c.den); got != c.want {
			t.Errorf("%v * %d / %d = %v, want %v", c.m, c.num, c.den, got, c.want)
		}
	}
	// 64 eighth samples a row of 8x8 blocks: from 8x8 row 3 down one
	for _, c := range []struct {
		v8, delta, sign, max8, maxOff, want int
		ok                                  bool
	}{
		{3, 64, 1, 20, 0, 4, true}, {3, 64, -1, 20, 0, 2, true}, {7, 64, 1, 20, 0, 8, false}, {7, 64, 1, 20, 8, 8, true}, {0, -64, 1, 20, 8, -1, false},
	} {
		if got, ok := mfPosition(c.v8, c.delta, c.sign, c.max8, c.maxOff); got != c.want || ok != c.ok {
			t.Errorf("project(%d, %d, %d): %d %v, want %d %v", c.v8, c.delta, c.sign, got, ok, c.want, c.ok)
		}
	}
}
