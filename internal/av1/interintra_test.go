package av1

import (
	"fmt"
	"math/rand/v2"
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

// interIntraBlendRef is interIntraBlend a sample at a time, as the spec
// writes it.
func interIntraBlendRef(ii *interIntra, size, plane int, intra []uint16, w, h int, dst []uint16, dstStride int) {
	var wm []uint8
	mw := int(blockWidth[size])
	if ii.wedge {
		wm = wedgeMasks[size][0][ii.idx]
	}
	scale := MAX_SB_SIZE / max(w, h)
	for i := range h {
		row := dst[i*dstStride : i*dstStride+w]
		for j := range row {
			var m int32
			switch {
			case ii.wedge && plane == 0:
				m = int32(wm[i*mw+j])
			case ii.wedge:
				a, b := wm[2*i*mw+2*j:], wm[(2*i+1)*mw+2*j:]
				m = (int32(a[0]) + int32(a[1]) + int32(b[0]) + int32(b[1]) + 2) >> 2
			case ii.mode == II_V_PRED:
				m = int32(iiWeights1d[i*scale])
			case ii.mode == II_H_PRED:
				m = int32(iiWeights1d[j*scale])
			case ii.mode == II_SMOOTH_PRED:
				m = int32(iiWeights1d[min(i, j)*scale])
			default:
				m = 32
			}
			row[j] = uint16((m*int32(intra[i*w+j]) + (64-m)*int32(row[j]) + 32) >> 6) //nolint:gosec // a sample
		}
	}
}

// interIntraBlend gives interIntraBlendRef's samples for every mode and
// wedge, size and plane.
func TestInterIntraBlendExact(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for size := range BLOCK_SIZES {
		for plane := range 2 {
			w, h := int(blockWidth[size]), int(blockHeight[size])
			if plane > 0 {
				w, h = w/2, h/2
			}
			if w < 4 || h < 4 {
				continue
			}
			var cases []interIntra
			for mode := range INTERINTRA_MODES {
				cases = append(cases, interIntra{on: true, mode: mode})
			}
			for idx, m := range wedgeMasks[size][0] {
				if m != nil {
					cases = append(cases, interIntra{on: true, wedge: true, idx: idx})
				}
			}
			for _, ii := range cases {
				intra := make([]uint16, w*h)
				dst := make([]uint16, w*h)
				for i := range intra {
					intra[i], dst[i] = uint16(rng.IntN(1024)), uint16(rng.IntN(1024))
				}
				want := append([]uint16(nil), dst...)
				interIntraBlend(&ii, size, plane, intra, w, h, dst, w)
				interIntraBlendRef(&ii, size, plane, intra, w, h, want, w)
				for i := range want {
					if dst[i] != want[i] {
						t.Fatalf("size %d plane %d %+v: sample %d is %d, want %d", size, plane, ii, i, dst[i], want[i])
					}
				}
			}
		}
	}
}
