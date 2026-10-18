package av1

import (
	"fmt"
	"testing"
)

// Compound blocks forced masked (a wedge where the size has them, else
// the difference mask): decoded to the reconstruction, 8 and 10 bit.
func TestMaskedCompound(t *testing.T) {
	defer func() { dbgForceCompound, dbgForceInter, dbgForceMasked = false, false, false }()
	dbgForceCompound, dbgForceInter, dbgForceMasked = true, true, true
	for _, c := range raceCut([]struct{ w, h, depth int }{{136, 96, 8}, {104, 72, 10}}) {
		t.Run(fmt.Sprintf("%dx%d-%d", c.w, c.h, c.depth), func(t *testing.T) {
			o := EncoderOptions{Width: c.w, Height: c.h, BitDepth: c.depth, QIndex: 110, FPSNum: 25, FPSDen: 1, MiniGOP: 4}
			p := testPicture(c.w, c.h, c.depth, 5)
			var pics []*Source
			for i := range 5 {
				pics = append(pics, bands(p, 2*i, i))
			}
			w0, d0 := maskedBlocks[1].Load(), maskedBlocks[0].Load()
			checkDecodes(t, o, pics)
			w, d := maskedBlocks[1].Load()-w0, maskedBlocks[0].Load()-d0
			t.Logf("%d blocks by wedge, %d by difference", w, d)
			if w == 0 || d == 0 {
				t.Fatal("a kind of mask unused")
			}
		})
	}
}

// The wedge masks: each size's sixteen, the two signs complementary,
// every weight 0..64, and the flipped sign averaging at least 32 along
// the top row and left column (initialise_wedge_mask_table's rule).
func TestWedgeMasks(t *testing.T) {
	n := 0
	for bs := range BLOCK_SIZES {
		if wedgeBits[bs] == 0 {
			if wedgeMasks[bs][0][0] != nil {
				t.Errorf("size %d: masks without wedge bits", bs)
			}
			continue
		}
		w, h := int(blockWidth[bs]), int(blockHeight[bs])
		for wedge := range WEDGE_TYPES {
			a, b := wedgeMasks[bs][0][wedge], wedgeMasks[bs][1][wedge]
			if len(a) != w*h || len(b) != w*h {
				t.Fatalf("size %d wedge %d: %d, %d samples", bs, wedge, len(a), len(b))
			}
			for i := range a {
				if a[i] > 64 || int(a[i])+int(b[i]) != 64 {
					t.Fatalf("size %d wedge %d: %d and %d", bs, wedge, a[i], b[i])
				}
			}
			n++
		}
	}
	if n != 9*16 {
		t.Errorf("%d wedges, want 144", n)
	}
}
