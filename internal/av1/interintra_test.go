package av1

import (
	"fmt"
	"testing"
)

// Single reference blocks forced inter-intra (smooth or by wedge):
// decoded to the reconstruction, 8 and 10 bit.
func TestInterIntra(t *testing.T) {
	defer func() { dbgForceInter, dbgForceInterIntra = false, false }()
	dbgForceInter, dbgForceInterIntra = true, true
	for _, c := range raceCut([]struct{ w, h, depth int }{{136, 96, 8}, {104, 72, 10}}) {
		t.Run(fmt.Sprintf("%dx%d-%d", c.w, c.h, c.depth), func(t *testing.T) {
			o := EncoderOptions{Width: c.w, Height: c.h, BitDepth: c.depth, QIndex: 110, FPSNum: 25, FPSDen: 1, MiniGOP: 4}
			p := testPicture(c.w, c.h, c.depth, 5)
			var pics []*Source
			for i := range 5 {
				pics = append(pics, bands(p, 2*i, i))
			}
			s0, w0 := interIntraBlocks[0].Load(), interIntraBlocks[1].Load()
			checkDecodes(t, o, pics)
			s, w := interIntraBlocks[0].Load()-s0, interIntraBlocks[1].Load()-w0
			t.Logf("%d blocks smooth, %d by wedge", s, w)
			if s == 0 || w == 0 {
				t.Fatal("a kind of inter-intra unused")
			}
		})
	}
}

// The intra mode variant masks: DC even, V and H fading from the edge
// they predict from, SMOOTH from both.
func TestInterIntraMasks(t *testing.T) {
	const w, h = 16, 8
	for mode := range INTERINTRA_MODES {
		ii := interIntra{on: true, mode: mode}
		intra, dst := make([]uint16, w*h), make([]uint16, w*h)
		for i := range intra {
			intra[i] = 64
		}
		interIntraBlend(&ii, BLOCK_16X8, 0, intra, w, h, dst, w)
		at := func(i, j int) int { return int(dst[i*w+j]) }
		switch mode {
		case II_DC_PRED:
			if at(0, 0) != 32 || at(h-1, w-1) != 32 {
				t.Errorf("DC: %d, %d", at(0, 0), at(h-1, w-1))
			}
		case II_V_PRED:
			if at(0, w-1) != at(0, 0) || at(h-1, 0) >= at(0, 0) {
				t.Errorf("V: %d %d %d", at(0, 0), at(0, w-1), at(h-1, 0))
			}
		case II_H_PRED:
			if at(h-1, 0) != at(0, 0) || at(0, w-1) >= at(0, 0) {
				t.Errorf("H: %d %d %d", at(0, 0), at(h-1, 0), at(0, w-1))
			}
		case II_SMOOTH_PRED:
			if at(h-1, w-1) >= at(0, w-1) || at(0, w-1) != at(0, 0) {
				t.Errorf("SMOOTH: %d %d %d", at(0, 0), at(0, w-1), at(h-1, w-1))
			}
		}
	}
}
