package av1

import (
	"math"
	"math/rand/v2"
	"testing"
)

// cleanPicture is a picture without noise (gradients, edges, texture),
// its content moved by (dx, dy).
func cleanPicture(w, h, depth, dx, dy int) *Source {
	cw, ch := (w+1)/2, (h+1)/2
	s := &Source{Y: make([]uint16, w*h), Cb: make([]uint16, cw*ch), Cr: make([]uint16, cw*ch), StrideY: w, StrideC: cw}
	maxV := float64(int(1)<<uint(depth) - 1)
	luma := func(x, y int) float64 {
		v := 0.3 + 0.3*float64(x)/float64(w) + 0.1*math.Sin(float64(x)/5)*math.Cos(float64(y)/7)
		if (x/23+y/17)%3 == 0 {
			v += 0.2
		}
		return v
	}
	for y := range h {
		for x := range w {
			s.Y[y*w+x] = uint16(luma(x-dx, y-dy) * maxV)
		}
	}
	for y := range ch {
		for x := range cw {
			xx, yy := float64(2*x-dx), float64(2*y-dy)
			s.Cb[y*cw+x] = uint16((0.5 + 0.15*math.Sin(xx/18)) * maxV)
			s.Cr[y*cw+x] = uint16((0.5 + 0.15*math.Cos(yy/14+xx/40)) * maxV)
		}
	}
	return s
}

// noisy is p with Gaussian noise of sigma (in 8-bit units) added.
func noisy(p *Source, depth int, sigma float64, seed uint64) *Source {
	rng := rand.New(rand.NewPCG(seed, 7))
	maxV := float64(int(1)<<uint(depth) - 1)
	scale := sigma * float64(int(1)<<uint(depth-8))
	add := func(in []uint16) []uint16 {
		out := make([]uint16, len(in))
		for i, v := range in {
			out[i] = uint16(min(max(float64(v)+scale*rng.NormFloat64()+0.5, 0), maxV))
		}
		return out
	}
	return &Source{Y: add(p.Y), Cb: add(p.Cb), Cr: add(p.Cr), StrideY: p.StrideY, StrideC: p.StrideC}
}

// psnrPlanes is the PSNR of b against a over all three planes.
func psnrPlanes(a, b *Source, depth int) float64 {
	var se float64
	n := 0
	for p, pa := range [][]uint16{a.Y, a.Cb, a.Cr} {
		pb := [][]uint16{b.Y, b.Cb, b.Cr}[p]
		for i, v := range pa {
			d := float64(v) - float64(pb[i])
			se += d * d
		}
		n += len(pa)
	}
	peak := float64(int(1)<<uint(depth) - 1)
	return 10 * math.Log10(peak*peak/(se/float64(n)))
}

// A noisy still picture: the filtered picture is much nearer the clean
// one than the noisy one is, with the window on both sides or before
// only.
func TestTemporalFilterNoise(t *testing.T) {
	const w, h = 160, 96
	for _, depth := range []int{8, 10} {
		clean := cleanPicture(w, h, depth, 0, 0)
		var win []*Source
		for i := range 7 {
			win = append(win, noisy(clean, depth, 6, uint64(i+1)))
		}
		for _, center := range []int{3, 6} {
			f := temporalFilter(win, center, 120, depth, w, h)
			before, after := psnrPlanes(clean, win[center], depth), psnrPlanes(clean, f, depth)
			t.Logf("%d bits, center %d: noisy %.2f dB, filtered %.2f dB", depth, center, before, after)
			if after < before+3 {
				t.Errorf("%d bits, center %d: filtered %.2f dB against noisy %.2f dB", depth, center, after, before)
			}
		}
	}
}

// A noisy moving picture: the filter follows the motion (it does better
// than the noisy picture, and far better than a plain average, which
// smears), and a clean moving picture comes out nearly as it was.
func TestTemporalFilterMotion(t *testing.T) {
	const w, h, depth = 320, 240, 8
	var clean, win []*Source
	for i := range 7 {
		c := cleanPicture(w, h, depth, i, i/2)
		clean = append(clean, c)
		win = append(win, noisy(c, depth, 6, uint64(i+11)))
	}
	f := temporalFilter(win, 3, 120, depth, w, h)
	avg := &Source{Y: make([]uint16, len(win[3].Y)), Cb: make([]uint16, len(win[3].Cb)), Cr: make([]uint16, len(win[3].Cr)), StrideY: w, StrideC: (w + 1) / 2}
	for p, dst := range [][]uint16{avg.Y, avg.Cb, avg.Cr} {
		for i := range dst {
			s := 0
			for _, s7 := range win {
				s += int([][]uint16{s7.Y, s7.Cb, s7.Cr}[p][i])
			}
			dst[i] = uint16((s + 3) / 7) //nolint:gosec // a mean of samples
		}
	}
	noisyPSNR, filtPSNR, avgPSNR := psnrPlanes(clean[3], win[3], depth), psnrPlanes(clean[3], f, depth), psnrPlanes(clean[3], avg, depth)
	t.Logf("moving: noisy %.2f dB, filtered %.2f dB, plain average %.2f dB", noisyPSNR, filtPSNR, avgPSNR)
	if filtPSNR < noisyPSNR+2 || filtPSNR < avgPSNR+5 {
		t.Errorf("moving: filtered %.2f dB, noisy %.2f dB, plain average %.2f dB", filtPSNR, noisyPSNR, avgPSNR)
	}
	g := temporalFilter(clean, 3, 120, depth, w, h)
	p := psnrPlanes(clean[3], g, depth)
	t.Logf("clean moving picture filtered: %.2f dB", p)
	if p < 40 {
		t.Errorf("clean moving picture filtered: %.2f dB", p)
	}
}

// A noisy moving sequence coded with and without the filter: it decodes
// exactly with it; the PSNRs against the clean and the noisy sources and
// the bytes are logged.
func TestTemporalFilterEncode(t *testing.T) {
	const w, h, depth, n = 160, 96, 8, 17
	var clean, src []*Source
	for i := range n {
		c := cleanPicture(w, h, depth, i, i/3)
		clean = append(clean, c)
		src = append(src, noisy(c, depth, 5, uint64(i+31)))
	}
	o := EncoderOptions{Width: w, Height: h, BitDepth: depth, QIndex: 140, FPSNum: 25, FPSDen: 1}
	defer func(was bool) { dbgNoTF = was }(dbgNoTF)
	dbgNoTF = false // the filter on (off by default)
	checkDecodesWith(t, o, src, nil)
	for _, off := range []bool{false, true} {
		dbgNoTF = off
		e, err := NewEncoder(o)
		if err != nil {
			t.Fatal(err)
		}
		e.keepRecon = true
		var vsClean, vsNoisy float64
		bytes := 0
		for i, u := range encodeAll(t, e, src) {
			r := &Source{Y: u.recon[0], Cb: u.recon[1], Cr: u.recon[2], StrideY: w, StrideC: (w + 1) / 2}
			vsClean += psnrPlanes(clean[i], r, depth) / n
			vsNoisy += psnrPlanes(src[i], r, depth) / n
			bytes += len(u.Data)
		}
		t.Logf("q %d, filter %v: %d bytes, PSNR %.2f dB against the clean source, %.2f dB against the noisy", o.QIndex, !off, bytes, vsClean, vsNoisy)
	}
}
