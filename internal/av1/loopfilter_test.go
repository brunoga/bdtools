package av1

import (
	"math"
	"math/rand/v2"
	"slices"
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
	if testing.Short() {
		t.Skip("slow: the full suite runs it")
	}
	t.Parallel()
	for _, c := range raceCut([]struct{ w, h, depth, q int }{
		{200, 136, 8, 40}, {200, 136, 8, 120}, {200, 136, 8, 200}, {200, 136, 8, 250},
		{202, 130, 8, 230}, {136, 72, 10, 100}, {136, 72, 10, 220}, {90, 70, 10, 250},
	}) {
		o := EncoderOptions{Width: c.w, Height: c.h, BitDepth: c.depth, QIndex: c.q, FPSNum: 25, FPSDen: 1}
		for _, pic := range []*Source{testPicture(c.w, c.h, c.depth, 1), smoothPicture(c.w, c.h, c.depth)} {
			// CDEF and loop restoration off, their choices not to blur
			// the comparison
			n, psnr := checkDecodesWith(t, o, []*Source{pic}, func(e *Encoder) { e.seq.cdef, e.seq.restoration = false, false })
			n0, psnr0 := checkDecodesWith(t, o, []*Source{pic}, func(e *Encoder) { e.seq.cdef, e.seq.restoration, e.lfFixed = false, false, &[5]int{} })
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
	if testing.Short() {
		t.Skip("slow: the full suite runs it")
	}
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

// The edge segment kernel filters as sample does, a line at a time.
func TestLoopFilterKernel(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	// the segments a filter changed, by size, plane and reach (0 any, 1 the
	// third sample from the edge, 2 the sixth)
	changed := map[[3]int]int{}
	defer func() {
		for _, k := range [][3]int{{4, 0, 0}, {4, 1, 0}, {8, 0, 1}, {8, 1, 0}, {16, 0, 1}, {16, 0, 2}} {
			if changed[k] == 0 {
				t.Errorf("no segment of size %d, plane %d changed to reach %d", k[0], k[1], k[2])
			}
		}
	}()
	for _, depth := range []int{8, 10} {
		f := &frameEnc{bitDepth: depth}
		for range 4000 {
			lf := newLoopFilter(f, [4]int{1 + rng.IntN(63), 1, 1, 1}, rng.IntN(8))
			lvl := 1 + rng.IntN(63)
			limit, blimit, thresh := lf.limit[lvl], lf.blimit[lvl], lf.thresh[lvl]
			const stride = 32
			buf := make([]uint16, 32*stride)
			// a smooth base with a step at the edge and noise of a few
			// sizes, so that every filter applies somewhere
			base, stepv, noise := rng.IntN(1<<depth), rng.IntN(1<<(depth-3))-1<<(depth-4), 1+rng.IntN(1<<(depth-6+rng.IntN(4)))
			for i := range buf {
				buf[i] = uint16(min(max(base+rng.IntN(noise)-noise/2+(i%stride/16)*stepv, 0), 1<<depth-1))
			}
			size := []int{4, 8, 16}[rng.IntN(3)]
			plane := rng.IntN(2)
			if plane == 1 && size == 16 {
				size = 8
			}
			step, across := 1, stride
			if rng.IntN(2) == 1 {
				step, across = stride, 1
			}
			pos := 16*step + 8*across
			want := slices.Clone(buf)
			for i := range 4 {
				lf.sample(want, pos+i*across, step, plane, size, limit, blimit, thresh)
			}
			orig := slices.Clone(buf)
			lf.filterSeg(buf, pos, step, across, plane, size, limit, blimit, thresh)
			if !slices.Equal(orig, want) {
				changed[[3]int{size, plane, 0}]++
				// the wide filters touch a third row from the edge
				if want[pos-3*step] != orig[pos-3*step] || want[pos+2*step] != orig[pos+2*step] {
					changed[[3]int{size, plane, 1}]++
				}
				if want[pos-6*step] != orig[pos-6*step] {
					changed[[3]int{size, plane, 2}]++
				}
			}
			if !slices.Equal(buf, want) {
				t.Fatalf("%d bits, size %d plane %d step %d: the kernel differs", depth, size, plane, step)
			}
		}
	}
}
