//go:build amd64 && !purego

package mvc

import (
	"math/rand"
	"testing"
)

// TestDeblockMBAsm compares the assembly deblocking control with the Go
// path on random macroblock data.
func TestDeblockMBAsm(t *testing.T) {
	if !useAVX2Asm {
		t.Skip("no AVX2")
	}
	rng := rand.New(rand.NewSource(7))
	const mbW, mbH = 4, 3
	sp := &sps{chromaFormatIdc: 1}
	run := func(asm bool, seed int64) *picture {
		r := rand.New(rand.NewSource(seed))
		pic := allocNewPicture(mbW, mbH)
		for c := 0; c < 3; c++ {
			for i := range pic.planes[c] {
				pic.planes[c][i] = byte(r.Intn(256))
			}
		}
		fc := newFrameCtx(mbW, mbH)
		fc.reset(pic, sp)
		fc.slices = append(fc.slices, sliceParams{alphaOffset: r.Intn(13) - 6, betaOffset: r.Intn(13) - 6,
			chromaQPOffset: [2]int{r.Intn(13) - 6, r.Intn(13) - 6}})
		fc.slices = append(fc.slices, sliceParams{})
		maxRef := int32(r.Intn(3) + 1)
		for i := range fc.mbs {
			m := &fc.mbs[i]
			m.flags = mbfAvail
			if r.Intn(4) == 0 {
				m.flags |= mbfIntra
			}
			if r.Intn(3) == 0 {
				m.flags |= mbfT8x8
			}
			m.qp = int8(r.Intn(52))
			m.nzMask = uint16(r.Intn(1 << 16))
			if r.Intn(2) == 0 {
				m.nzMask = 0
			}
			m.mvEdges = 0x77
			m.slice = uint16(r.Intn(2) * r.Intn(2))
		}
		for b := 0; b < mbW*mbH*16; b++ {
			for l := 0; l < 2; l++ {
				id := int32(-1)
				if r.Intn(3) != 0 {
					id = r.Int31n(maxRef)
				}
				fc.refIDs[l][b] = id
				if id >= 0 {
					pic.mvs[l][b] = mv{int16(r.Intn(9) - 4), int16(r.Intn(9) - 4)}
					if r.Intn(8) == 0 {
						pic.mvs[l][b] = mv{int16(r.Intn(2000) - 1000), int16(r.Intn(2000) - 1000)}
					}
				}
			}
			if fc.refIDs[0][b] < 0 && fc.refIDs[1][b] < 0 {
				fc.refIDs[0][b] = 0
			}
		}
		// uniform motion within some macroblocks
		for i := range fc.mbs {
			if r.Intn(2) == 0 {
				continue
			}
			base := (i/mbW)*16*mbW + (i%mbW)*4
			for l := 0; l < 2; l++ {
				for y := 0; y < 4; y++ {
					for x := 0; x < 4; x++ {
						j := base + y*mbW*4 + x
						fc.refIDs[l][j] = fc.refIDs[l][base]
						pic.mvs[l][j] = pic.mvs[l][base]
					}
				}
			}
		}
		save := useAVX2Asm
		useAVX2Asm = asm
		for y := 0; y < mbH; y++ {
			fc.deblockRow(y)
		}
		useAVX2Asm = save
		return pic
	}
	for iter := 0; iter < 300; iter++ {
		seed := rng.Int63()
		a, b := run(true, seed), run(false, seed)
		for c := 0; c < 3; c++ {
			for i := range a.planes[c] {
				if a.planes[c][i] != b.planes[c][i] {
					st := a.stride[c]
					o := i - a.origin[c]
					t.Fatalf("iter %d: plane %d differs at (%d,%d)", iter, c, o%st, o/st)
				}
			}
		}
	}
}
