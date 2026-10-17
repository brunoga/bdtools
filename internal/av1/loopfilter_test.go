package av1

import (
	"math"
	"testing"
)

// smoothPicture is a picture of flat areas and gentle gradients (for the
// wide filters).
func smoothPicture(w, h, depth int) *Source {
	cw, ch := (w+1)/2, (h+1)/2
	s := &Source{Y: make([]uint16, w*h), Cb: make([]uint16, cw*ch), Cr: make([]uint16, cw*ch), StrideY: w, StrideC: cw}
	maxV := float64(int(1)<<uint(depth) - 1)
	for y := range h {
		for x := range w {
			v := 0.2 + 0.5*float64(x+y)/float64(w+h)
			if (x/40+y/40)%2 == 0 {
				v += 0.1
			}
			s.Y[y*w+x] = uint16(v * maxV)
		}
	}
	for y := range ch {
		for x := range cw {
			s.Cb[y*cw+x] = uint16((0.4 + 0.2*float64(x)/float64(cw)) * maxV)
			s.Cr[y*cw+x] = uint16((0.6 - 0.1*math.Floor(float64(y)/20)/float64(ch/20+1)) * maxV)
		}
	}
	return s
}

// TestLoopFilterDecodes codes pictures with the levels searched, at low
// to high q, and compares them with the filter off.
func TestLoopFilterDecodes(t *testing.T) {
	t.Parallel()
	for _, c := range raceCut([]struct{ w, h, depth, q int }{
		{200, 136, 8, 40}, {200, 136, 8, 120}, {200, 136, 8, 200}, {200, 136, 8, 250},
		{202, 130, 8, 230}, {136, 72, 10, 100}, {136, 72, 10, 220}, {90, 70, 10, 250},
	}) {
		o := EncoderOptions{Width: c.w, Height: c.h, BitDepth: c.depth, QIndex: c.q, FPSNum: 25, FPSDen: 1}
		for _, pic := range []*Source{testPicture(c.w, c.h, c.depth, 1), smoothPicture(c.w, c.h, c.depth)} {
			// CDEF off, its choice not to blur the comparison
			n, psnr := checkDecodesWith(t, o, []*Source{pic}, func(e *Encoder) { e.seq.cdef = false })
			n0, psnr0 := checkDecodesWith(t, o, []*Source{pic}, func(e *Encoder) { e.seq.cdef, e.lfFixed = false, &[5]int{} })
			t.Logf("  filter: %+.2f dB, %+d bytes", psnr-psnr0, n-n0)
			if psnr < psnr0-1e-9 {
				t.Errorf("%v: the filter lost %.3f dB", c, psnr0-psnr)
			}
		}
	}
}

// TestLoopFilterFixed codes pictures with fixed levels and sharpnesses,
// for every filter path and each plane on its own.
func TestLoopFilterFixed(t *testing.T) {
	t.Parallel()
	for _, lv := range raceCut([][5]int{
		{63, 63, 63, 63, 0}, {10, 40, 5, 63, 0}, {63, 0, 30, 0, 0}, {0, 63, 0, 20, 0},
		{30, 30, 30, 30, 3}, {50, 50, 50, 50, 7}, {4, 4, 4, 4, 1}, {0, 0, 0, 0, 2},
	}) {
		for _, c := range []struct{ w, h, depth, q int }{{200, 136, 8, 200}, {202, 130, 8, 60}, {136, 72, 10, 240}} {
			o := EncoderOptions{Width: c.w, Height: c.h, BitDepth: c.depth, QIndex: c.q, FPSNum: 25, FPSDen: 1}
			for _, pic := range []*Source{testPicture(c.w, c.h, c.depth, 2), smoothPicture(c.w, c.h, c.depth)} {
				checkDecodesWith(t, o, []*Source{pic}, func(e *Encoder) { e.lfFixed = &lv })
			}
		}
	}
}
