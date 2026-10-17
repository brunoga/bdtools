package av1

import (
	"fmt"
	"testing"
)

// halfFlatPicture is testPicture with its left part flat (whole skipped
// superblocks at a high q).
func halfFlatPicture(w, h, depth int, seed uint64) *Source {
	s := testPicture(w, h, depth, seed)
	flat := uint16(int(1)<<uint(depth)/2 - 3)
	for y := range h {
		for x := range w * 5 / 8 {
			s.Y[y*s.StrideY+x] = flat
		}
	}
	for y := range (h + 1) / 2 {
		for x := range (w + 1) / 2 * 5 / 8 {
			s.Cb[y*s.StrideC+x], s.Cr[y*s.StrideC+x] = flat+7, flat-5
		}
	}
	return s
}

// TestCdefFixed codes pictures with fixed CDEF parameters: each filter on
// its own and both, every damping, several strength sets with the
// 64x64s' indexes in turn, both bit depths, sizes not whole superblocks
// and pictures with skipped superblocks.
func TestCdefFixed(t *testing.T) {
	t.Parallel()
	sets := []cdefSettings{
		{damping: 3, y: [8][2]int{{5, 0}}, uv: [8][2]int{{3, 0}}},   // primary only
		{damping: 4, y: [8][2]int{{0, 2}}, uv: [8][2]int{{0, 4}}},   // secondary only
		{damping: 5, y: [8][2]int{{15, 4}}, uv: [8][2]int{{14, 1}}}, // both
		{damping: 6, y: [8][2]int{{9, 1}}, uv: [8][2]int{{0, 0}}},   // luma only
		{damping: 3, bits: 1, y: [8][2]int{{2, 1}, {12, 4}}, uv: [8][2]int{{0, 0}, {7, 2}}},
		{damping: 6, bits: 2, y: [8][2]int{{1, 0}, {0, 0}, {6, 2}, {15, 0}}, uv: [8][2]int{{4, 4}, {1, 1}, {0, 2}, {11, 0}}},
		{damping: 4, bits: 3, y: [8][2]int{{0, 0}, {1, 1}, {2, 2}, {3, 4}, {4, 0}, {8, 1}, {13, 2}, {15, 4}},
			uv: [8][2]int{{15, 4}, {13, 2}, {8, 1}, {4, 0}, {3, 4}, {2, 2}, {1, 1}, {0, 0}}},
	}
	for _, c := range raceCut([]struct {
		w, h, depth, q int
		flat           bool
	}{
		{200, 136, 8, 120, false}, {90, 70, 10, 160, false}, {200, 136, 8, 230, true}, {136, 72, 10, 250, true},
	}) {
		o := EncoderOptions{Width: c.w, Height: c.h, BitDepth: c.depth, QIndex: c.q, FPSNum: 25, FPSDen: 1}
		pic := testPicture(c.w, c.h, c.depth, 3)
		if c.flat {
			pic = halfFlatPicture(c.w, c.h, c.depth, 3)
		}
		for i, s := range sets {
			t.Run(fmt.Sprintf("%dx%d-%d-q%d-set%d", c.w, c.h, c.depth, c.q, i), func(t *testing.T) {
				var e *Encoder
				checkDecodesWith(t, o, []*Source{pic}, func(enc *Encoder) { e, enc.cdefFixed = enc, &s })
				if c.flat {
					skipped := 0
					for _, v := range e.frame.cdefIdx {
						if v < 0 {
							skipped++
						}
					}
					if skipped == 0 {
						t.Errorf("no 64x64 without a cdef_idx")
					}
				}
			})
		}
	}
}

// TestCdefSearch codes pictures with the parameters searched, at low to
// high q, and compares them with CDEF off.
func TestCdefSearch(t *testing.T) {
	t.Parallel()
	for _, c := range raceCut([]struct{ w, h, depth, q int }{
		{200, 136, 8, 60}, {200, 136, 8, 160}, {200, 136, 8, 240}, {136, 72, 10, 120}, {90, 70, 10, 220},
	}) {
		o := EncoderOptions{Width: c.w, Height: c.h, BitDepth: c.depth, QIndex: c.q, FPSNum: 25, FPSDen: 1}
		for _, pic := range []*Source{testPicture(c.w, c.h, c.depth, 4), halfFlatPicture(c.w, c.h, c.depth, 4)} {
			var e *Encoder
			n, psnr := checkDecodesWith(t, o, []*Source{pic}, func(enc *Encoder) { e = enc })
			fp := e.frame.fp
			n0, psnr0 := checkDecodesWith(t, o, []*Source{pic}, func(enc *Encoder) { enc.seq.cdef = false })
			t.Logf("  cdef damping %d bits %d y %v uv %v: %+.2f dB, %+d bytes", fp.cdefDamping, fp.cdefBits,
				fp.cdefY[:1<<uint(fp.cdefBits)], fp.cdefUV[:1<<uint(fp.cdefBits)], psnr-psnr0, n-n0)
		}
	}
}

// TestCdefSearchErrors checks the search's squared errors against the
// filter's: each strength pair applied to a whole frame changes its
// error by the sum of the 64x64s' differences from strength 0.
func TestCdefSearchErrors(t *testing.T) {
	for _, c := range []struct{ w, h, depth, q int }{{200, 136, 8, 180}, {90, 70, 10, 200}} {
		o := EncoderOptions{Width: c.w, Height: c.h, BitDepth: c.depth, QIndex: c.q, FPSNum: 25, FPSDen: 1}
		e, err := NewEncoder(o)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Encode(halfFlatPicture(c.w, c.h, c.depth, 5)); err != nil {
			t.Fatal(err)
		}
		f := e.frame
		rec := f.rec
		for _, damping := range []int{3, 6} {
			f.rec = rec
			sse := make([]cdefSSE, len(f.cdefIdx))
			sbCols := (f.miCols + 15) >> 4
			win := make([]int32, 144)
			for i, v := range f.cdefIdx {
				if v >= 0 {
					f.cdefSearchSB(i/sbCols, i%sbCols, damping, &sse[i], win)
				}
			}
			for _, pair := range [][2]int{{1, 0}, {0, 2}, {5, 7}, {13, 61}, {63, 63}, {40, 0}} {
				var dy, duv int64
				for i, v := range f.cdefIdx {
					if v >= 0 {
						f.cdefIdx[i] = 0
						dy += sse[i].y[pair[0]] - sse[i].y[0]
						duv += sse[i].uv[pair[1]] - sse[i].uv[0]
					}
				}
				f.rec = rec
				f.fp.cdefDamping = damping
				f.fp.cdefY[0] = [2]int{pair[0] >> 2, cdefSecValues[pair[0]&3]}
				f.fp.cdefUV[0] = [2]int{pair[1] >> 2, cdefSecValues[pair[1]&3]}
				f.cdefApply()
				gy := f.planeSSE(0, f.rec[0]) - f.planeSSE(0, rec[0])
				guv := f.planeSSE(1, f.rec[1]) + f.planeSSE(2, f.rec[2]) - f.planeSSE(1, rec[1]) - f.planeSSE(2, rec[2])
				if gy != dy || guv != duv {
					t.Errorf("%v damping %d pair %v: the filter changes the error by %d and %d, the search says %d and %d",
						c, damping, pair, gy, guv, dy, duv)
				}
			}
		}
		f.rec = rec
	}
}
