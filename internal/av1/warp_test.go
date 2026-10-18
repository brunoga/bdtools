package av1

import (
	"fmt"
	"testing"
)

// Blocks forced warped where their neighbours give a model, on bands of
// rows moving apart (the neighbours' motion differs) and on plain motion:
// decoded to the reconstruction.
func TestWarpedMotion(t *testing.T) {
	defer func() { dbgForceInter, dbgForceWarp = false, false }()
	dbgForceInter, dbgForceWarp = true, true
	for _, c := range []struct {
		w, h, depth int
		banded      bool
	}{{136, 96, 8, true}, {104, 80, 10, false}, {96, 72, 8, true}} {
		t.Run(fmt.Sprintf("%dx%d-%d-%v", c.w, c.h, c.depth, c.banded), func(t *testing.T) {
			o := EncoderOptions{Width: c.w, Height: c.h, BitDepth: c.depth, QIndex: 110, FPSNum: 25, FPSDen: 1, MiniGOP: 4}
			p := testPicture(c.w, c.h, c.depth, 6)
			var pics []*Source
			for i := range 5 {
				if c.banded {
					pics = append(pics, bands(p, 2*i, i))
				} else {
					pics = append(pics, shifted(p, 3*i, -i))
				}
			}
			before := warpedBlocks.Load()
			checkDecodes(t, o, pics)
			n := warpedBlocks.Load() - before
			if n == 0 {
				t.Fatal("no block warped")
			}
			t.Logf("%d blocks warped", n)
		})
	}
}

// The resolve divisor process: d*factor >> shift is about 1.
func TestResolveDivisor(t *testing.T) {
	for _, d := range []int64{1, 2, 3, 255, 256, 257, 1000, 65536, 70000, 1 << 40, -5, -1 << 20} {
		shift, factor := resolveDivisor(d)
		got := float64(d) * float64(factor) / float64(int64(1)<<uint(shift))
		if got < 0.99 || got > 1.01 {
			t.Errorf("%d: %d >> %d gives %.4f", d, factor, shift, got)
		}
	}
	// the identity model shears nothing
	id := [6]int32{0, 0, 1 << WARPEDMODEL_PREC_BITS, 0, 0, 1 << WARPEDMODEL_PREC_BITS}
	if s := setupShear(&id); !s.valid || s.alpha != 0 || s.beta != 0 || s.gamma != 0 || s.delta != 0 {
		t.Errorf("identity: %+v", s)
	}
}
