package av1

// Loop restoration (spec 7.17), run on the CDEF output. Each plane is
// divided into restoration units of LoopRestorationSize (64, 128 or 256
// luma samples; chroma's the same or half), the last row and column of
// units taking what is left over (count_units_in_frame rounds), the unit
// rows offset upwards by 8 luma rows like the stripes. A unit is left
// alone or filtered by the Wiener filter (a separable symmetric 7-tap
// filter, 5-tap for chroma, of unit gain: three coefficients a pass
// coded, the horizontal pass rounded and clipped to an intermediate
// precision before the vertical) or by the self-guided filter (one of 16
// parameter sets: up to two box filters, of radius 2 on every other row
// and of radius 1, each a guided filter of the local mean and variance
// in boxes about the sample with a strength eps, their outputs projected
// with two coded weights onto the difference from the sample). The
// filters read the plane in stripes 64 luma rows high, offset upwards by
// 8: within the stripe the CDEF output, above and below it two rows of
// the frame before CDEF (the deblocked frame), the nearest repeated
// beyond; the plane's edges are extended.
//
// The encoder chooses as libaom's pickrst.c does (Copyright the Alliance
// for Open Media, BSD-2-Clause, see LICENSE.libaom), simplified: the
// statistics are gathered once, a stripe of a 64-wide column (32 for
// chroma) at a time, so that the units of every size are sums of them.
// For the Wiener filter they are the correlations of the 7x7 window
// (folded, as the filter is symmetric, to 4x4 sums of samples) with
// itself and with the source; the filter is solved by least squares,
// the vertical taps for the horizontal and back a few times
// (wiener_decompose_sep_sym), quantized and refined tap by tap by the
// error the statistics predict (finer_search_wiener). For the
// self-guided filter they are the correlations of each box filter's
// output (less the sample) with each other and with the source, for
// every strength; each set's two weights are solved by least squares
// (get_proj_subspace) and quantized (encode_xq), the set of least
// predicted error kept. Each unit size is then judged by the predicted
// errors plus lambda times the rates (the units' symbols, their
// coefficients coded against the previous unit's), and for the size
// chosen the candidates are filtered for their exact errors and each
// plane's type (none, Wiener, self-guided or switchable) and each unit's
// filter decided, in the order the units are coded.

import (
	"math"
	"math/bits"
	"math/rand/v2"
	"runtime"
	"sync"
	"sync/atomic"
)

// lrUnit is a restoration unit's coded parameters.
type lrUnit struct {
	typ    uint8      // RESTORE_NONE, RESTORE_WIENER or RESTORE_SGRPROJ
	set    uint8      // lr_sgr_set
	wiener [2][3]int8 // LrWiener: the vertical pass's coefficients, then the horizontal's
	xqd    [2]int8    // LrSgrXqd
}

// lrGeom is a plane's layout for loop restoration: its size, the
// stripes, and its units of one size.
type lrGeom struct {
	pw, ph     int // the plane's size (Round2 of the frame's)
	sh, off    int // the stripe height and the offset upwards (the units' too)
	size       int // LoopRestorationSize
	rows, cols int // the units
}

// lrPlane is a plane's restoration: FrameRestorationType and its units
// (unitRows by unitCols).
type lrPlane struct {
	lrGeom
	typ   int
	units []lrUnit
}

// lrFrame is a frame's restoration, by plane.
type lrFrame [3]lrPlane

// lrSettings are fixed restoration settings (for tests): each plane's
// FrameRestorationType, lr_unit_shift (0..2) and lr_uv_shift; each unit
// then the frame type's filter (switchable: none, Wiener and self-guided
// in turn) with searched parameters, or with random, a random filter and
// random parameters.
type lrSettings struct {
	types              [3]int
	unitShift, uvShift int
	random             bool
}

// lrCountUnits is count_units_in_frame.
func lrCountUnits(unitSize, frameSize int) int { return max((frameSize+unitSize>>1)/unitSize, 1) }

// lrGeometry is plane p's layout with units of size.
func (f *frameEnc) lrGeometry(p, size int) lrGeom {
	sub := uint(min(p, 1)) //nolint:gosec // 0 or 1
	g := lrGeom{pw: (f.width + int(sub)) >> sub, ph: (f.height + int(sub)) >> sub, sh: 64 >> sub, off: 8 >> sub, size: size}
	g.rows, g.cols = lrCountUnits(size, g.ph), lrCountUnits(size, g.pw)
	return g
}

// unitRect is unit (ur, uc)'s samples: columns x0..x1-1, rows y0..y1-1.
func (g *lrGeom) unitRect(ur, uc int) (x0, x1, y0, y1 int) {
	x0, x1 = uc*g.size, (uc+1)*g.size
	if uc == g.cols-1 {
		x1 = g.pw
	}
	y0, y1 = max(ur*g.size-g.off, 0), (ur+1)*g.size-g.off
	if ur == g.rows-1 {
		y1 = g.ph
	}
	return x0, x1, y0, y1
}

// stripe is stripe k's first and last rows (StripeStartY, StripeEndY).
func (g *lrGeom) stripe(k int) (int, int) {
	s := k*g.sh - g.off
	return s, s + g.sh - 1
}

// stripes is how many stripes the plane has.
func (g *lrGeom) stripes() int { return (g.ph-1+g.off)/g.sh + 1 }

// segments calls fn for each part of unit (ur, uc) in a stripe: its
// columns, rows and the stripe's rows.
func (g *lrGeom) segments(ur, uc int, fn func(x0, y0, w, h, sy0, sy1 int)) {
	x0, x1, y0, y1 := g.unitRect(ur, uc)
	for k := (y0 + g.off) / g.sh; ; k++ {
		sy0, sy1 := g.stripe(k)
		a, b := max(y0, sy0), min(y1, sy1+1)
		if a >= b {
			return
		}
		fn(x0, a, x1-x0, b-a, sy0, sy1)
	}
}

// sbUnits is read_lr's units of the superblock at mi (r, c): rows r0..r1-1
// and columns c0..c1-1.
func (g *lrGeom) sbUnits(p, r, c int) (r0, r1, c0, c1 int) {
	px := 4 >> uint(min(p, 1)) //nolint:gosec // 0 or 1
	r0 = (r*px + g.size - 1) / g.size
	r1 = min(g.rows, ((r+16)*px+g.size-1)/g.size)
	c0 = (c*px + g.size - 1) / g.size
	c1 = min(g.cols, ((c+16)*px+g.size-1)/g.size)
	return r0, r1, c0, c1
}

// lrScratch is a worker's buffers.
type lrScratch struct {
	win, inter, hfold []int32
	sum, sq, vs, vq   []int32
	a, b              []int32
	flt               [2][]int32 // a unit's two box filters
	fltS              [2][]int32 // the search's, every strength's
	out               []uint16
	ymat              []int16 // lrCellStats' rows for gram
}

func grow16(b []int16, n int) []int16 {
	if cap(b) < n {
		return make([]int16, n)
	}
	return b[:n]
}

// gramGo adds to g[u*17+v], for u <= v < k, the sum of the products of
// rows u and v of y (k rows of n).
func gramGo(y []int16, n, k int, g *[17 * 17]int64) {
	for u := range k {
		ru := y[u*n : u*n+n]
		for v := u; v < k; v++ {
			rv := y[v*n : v*n+n]
			var sum int64
			for j, a := range ru {
				sum += int64(a) * int64(rv[j])
			}
			g[u*17+v] += sum
		}
	}
}

func grow32(b []int32, n int) []int32 {
	if cap(b) < n {
		return make([]int32, n)
	}
	return b[:n]
}

// lrWindow fills win with the samples the filters read for the w x h
// block of plane p at (x0, y0), in the stripe of rows sy0..sy1: the
// (w+6) x (h+6) from (x0-3, y0-3), as get_source_sample gives them (from
// cdefd within the stripe, from deb outside it).
func (f *frameEnc) lrWindow(p int, g *lrGeom, cdefd, deb []uint16, x0, y0, w, h, sy0, sy1 int, win []int32) {
	stride := f.stride[p]
	ww := w + 6
	for i := range h + 6 {
		y := min(max(y0+i-3, 0), g.ph-1)
		buf := cdefd
		switch {
		case y < sy0:
			y, buf = max(sy0-2, y), deb
		case y > sy1:
			y, buf = min(sy1+2, y), deb
		}
		row := buf[y*stride : y*stride+g.pw]
		dst := win[i*ww : (i+1)*ww]
		for j := range dst {
			dst[j] = int32(row[min(max(x0+j-3, 0), g.pw-1)])
		}
	}
}

// wienerTaps is the Wiener coefficient process (7.17.5).
func wienerTaps(c [3]int8) (t [7]int32) {
	t[3] = 128
	for i, v := range c {
		t[i], t[6-i] = int32(v), int32(v)
		t[3] -= 2 * int32(v)
	}
	return t
}

// wienerFilter is the Wiener filter process (7.17.4) of a w x h block
// from its window into dst.
func wienerFilter(win []int32, w, h int, coef *[2][3]int8, bitDepth int, dst []uint16, stride int, inter []int32) {
	const round0, round1 = 3, 11 // InterRound0 and InterRound1, not compound
	vf, hf := wienerTaps(coef[0]), wienerTaps(coef[1])
	offset := int32(1) << uint(bitDepth+FILTER_BITS-round0-1)  //nolint:gosec // small
	limit := int32(1)<<uint(bitDepth+1+FILTER_BITS-round0) - 1 //nolint:gosec // small
	ww := w + 6
	for r := range h + 6 {
		row := win[r*ww : (r+1)*ww]
		out := inter[r*w : (r+1)*w]
		for c := range out {
			x := row[c : c+7]
			s := hf[0]*x[0] + hf[1]*x[1] + hf[2]*x[2] + hf[3]*x[3] + hf[4]*x[4] + hf[5]*x[5] + hf[6]*x[6]
			out[c] = min(max((s+1<<(round0-1))>>round0, -offset), limit-offset)
		}
	}
	maxV := int32(1)<<uint(bitDepth) - 1 //nolint:gosec // 8 or 10
	for r := range h {
		out := dst[r*stride : r*stride+w]
		for c := range out {
			k := r*w + c
			s := vf[0]*inter[k] + vf[1]*inter[k+w] + vf[2]*inter[k+2*w] + vf[3]*inter[k+3*w] +
				vf[4]*inter[k+4*w] + vf[5]*inter[k+5*w] + vf[6]*inter[k+6*w]
			out[c] = uint16(min(max((s+1<<(round1-1))>>round1, 0), maxV)) //nolint:gosec // a sample
		}
	}
}

// sgrA2 is the box filter's a2 by z (below 255).
var sgrA2 = func() (t [255]int32) {
	t[0] = 1
	for z := 1; z < 255; z++ {
		t[z] = int32(((z << SGRPROJ_SGR_BITS) + z/2) / (z + 1)) //nolint:gosec // small
	}
	return t
}()

// sgrEps are each pass's distinct strengths (eps) among the sets, and
// sgrEpsIdx each set's by pass (-1 for a radius 0).
var sgrEps, sgrEpsIdx = func() (eps [2][]int, idx [16][2]int) {
	for pass := range 2 {
		for s := range 16 {
			idx[s][pass] = -1
			if sgrParams[s][2*pass] == 0 {
				continue
			}
			e := int(sgrParams[s][2*pass+1])
			k := 0
			for k < len(eps[pass]) && eps[pass][k] != e {
				k++
			}
			if k == len(eps[pass]) {
				eps[pass] = append(eps[pass], e)
			}
			idx[s][pass] = k
		}
	}
	return eps, idx
}()

// sgrBoxSums sums the window's samples (sum) and their squares (sq) over
// the (2r+1)^2 box about each place of a w x h block and of the ring
// around it ((w+2) x (h+2), from (-1, -1)); with odd only the rows of
// odd i (the first pass reads no others). vs and vq are w+6 long.
func sgrBoxSums(win []int32, w, h, r int, odd bool, sum, sq, vs, vq []int32) {
	ww, bw := w+6, w+2
	for i := -1; i <= h; i++ {
		if odd && i&1 == 0 {
			continue
		}
		for x := range ww {
			var s, q int32
			for dy := -r; dy <= r; dy++ {
				v := win[(i+3+dy)*ww+x]
				s += v
				q += v * v
			}
			vs[x], vq[x] = s, q
		}
		o := (i + 1) * bw
		if r == 2 {
			for j := -1; j <= w; j++ {
				x := j + 1
				sum[o+x] = vs[x] + vs[x+1] + vs[x+2] + vs[x+3] + vs[x+4]
				sq[o+x] = vq[x] + vq[x+1] + vq[x+2] + vq[x+3] + vq[x+4]
			}
		} else {
			for j := -1; j <= w; j++ {
				x := j + 2
				sum[o+x-1] = vs[x] + vs[x+1] + vs[x+2]
				sq[o+x-1] = vq[x] + vq[x+1] + vq[x+2]
			}
		}
	}
}

// sgrAB makes the box filter's A and B (7.17.3) from the box sums, for
// radius r and strength eps.
func sgrAB(sum, sq []int32, w, h, r, eps, bitDepth int, odd bool, a, b []int32) {
	n := (2*r + 1) * (2*r + 1)
	n2e := n * n * eps
	s := ((1 << SGRPROJ_MTABLE_BITS) + n2e/2) / n2e
	oneOverN := ((1 << SGRPROJ_RECIP_BITS) + n/2) / n
	bw := w + 2
	sh := uint(bitDepth - 8) //nolint:gosec // 0 or 2
	for i := -1; i <= h; i++ {
		if odd && i&1 == 0 {
			continue
		}
		for k := (i + 1) * bw; k < (i+2)*bw; k++ {
			aa, bb := int(sq[k]), int(sum[k])
			d := bb
			if sh > 0 {
				aa = (aa + 1<<(2*sh-1)) >> (2 * sh)
				d = (bb + 1<<(sh-1)) >> sh
			}
			p := max(0, aa*n-d*d)
			z := (p*s + 1<<(SGRPROJ_MTABLE_BITS-1)) >> SGRPROJ_MTABLE_BITS
			a2 := 256
			if z < 255 {
				a2 = int(sgrA2[z])
			}
			a[k] = int32(a2)                                                                                       //nolint:gosec // <= 256
			b[k] = int32(((1<<SGRPROJ_SGR_BITS-a2)*bb*oneOverN + 1<<(SGRPROJ_RECIP_BITS-1)) >> SGRPROJ_RECIP_BITS) //nolint:gosec // small
		}
	}
}

// sgrFilter is the box filter's output F (7.17.3) of a pass from A and B.
func sgrFilter(win []int32, w, h, pass int, a, b, flt []int32) {
	sgrFilterFrom(win, w, h, pass, a, b, flt, true)
}

// sgrFilterGo is sgrFilter without the AVX2 rows.
func sgrFilterGo(win []int32, w, h, pass int, a, b, flt []int32) {
	sgrFilterFrom(win, w, h, pass, a, b, flt, false)
}

func sgrFilterFrom(win []int32, w, h, pass int, a, b, flt []int32, simd bool) {
	ww, bw := w+6, w+2
	for i := range h {
		u := win[(i+3)*ww+3:]
		out := flt[i*w : (i+1)*w]
		k := (i+1)*bw + 1 // A[i][0]
		j0 := 0
		if simd {
			j0 = sgrRows(out, u, a, b, i, k, bw, pass)
		}
		switch {
		case pass == 0 && i&1 == 0:
			for j := j0; j < len(out); j++ {
				up, dn := k+j-bw, k+j+bw
				av := 6*(a[up]+a[dn]) + 5*(a[up-1]+a[up+1]+a[dn-1]+a[dn+1])
				bv := 6*(b[up]+b[dn]) + 5*(b[up-1]+b[up+1]+b[dn-1]+b[dn+1])
				out[j] = (av*u[j] + bv + 1<<8) >> 9
			}
		case pass == 0:
			for j := j0; j < len(out); j++ {
				c := k + j
				av := 6*a[c] + 5*(a[c-1]+a[c+1])
				bv := 6*b[c] + 5*(b[c-1]+b[c+1])
				out[j] = (av*u[j] + bv + 1<<7) >> 8
			}
		default:
			for j := j0; j < len(out); j++ {
				c := k + j
				av := 4*(a[c]+a[c-1]+a[c+1]+a[c-bw]+a[c+bw]) + 3*(a[c-bw-1]+a[c-bw+1]+a[c+bw-1]+a[c+bw+1])
				bv := 4*(b[c]+b[c-1]+b[c+1]+b[c-bw]+b[c+bw]) + 3*(b[c-bw-1]+b[c-bw+1]+b[c+bw-1]+b[c+bw+1])
				out[j] = (av*u[j] + bv + 1<<8) >> 9
			}
		}
	}
}

// sgrSizes sizes the box filters' buffers for a w x h block.
func (sc *lrScratch) sgrSizes(w, h int) {
	n := (w + 2) * (h + 2)
	sc.sum, sc.sq, sc.a, sc.b = grow32(sc.sum, n), grow32(sc.sq, n), grow32(sc.a, n), grow32(sc.b, n)
	sc.vs, sc.vq = grow32(sc.vs, w+6), grow32(sc.vq, w+6)
}

// lrFilterBlock filters the w x h block of plane p at (x0, y0), in the
// stripe of rows sy0..sy1, with unit u's filter into dst.
func (f *frameEnc) lrFilterBlock(p int, g *lrGeom, u *lrUnit, cdefd, deb []uint16, x0, y0, w, h, sy0, sy1 int,
	dst []uint16, stride int, sc *lrScratch) {
	ww := w + 6
	sc.win = grow32(sc.win, (h+6)*ww)
	f.lrWindow(p, g, cdefd, deb, x0, y0, w, h, sy0, sy1, sc.win)
	if u.typ == RESTORE_WIENER {
		sc.inter = grow32(sc.inter, (h+6)*w)
		wienerFilter(sc.win, w, h, &u.wiener, f.bitDepth, dst, stride, sc.inter)
		return
	}
	sc.sgrSizes(w, h)
	set := int(u.set)
	var r [2]int
	for pass := range 2 {
		r[pass] = int(sgrParams[set][2*pass])
		if r[pass] == 0 {
			continue
		}
		sgrBoxSums(sc.win, w, h, r[pass], pass == 0, sc.sum, sc.sq, sc.vs, sc.vq)
		sgrAB(sc.sum, sc.sq, w, h, r[pass], int(sgrParams[set][2*pass+1]), f.bitDepth, pass == 0, sc.a, sc.b)
		sc.flt[pass] = grow32(sc.flt[pass], w*h)
		sgrFilter(sc.win, w, h, pass, sc.a, sc.b, sc.flt[pass])
	}
	w0, w1 := int32(u.xqd[0]), int32(u.xqd[1])
	w2 := 1<<SGRPROJ_PRJ_BITS - w0 - w1
	maxV := int32(1)<<uint(f.bitDepth) - 1 //nolint:gosec // 8 or 10
	const sh = SGRPROJ_RST_BITS + SGRPROJ_PRJ_BITS
	for i := range h {
		src := sc.win[(i+3)*ww+3:]
		out := dst[i*stride : i*stride+w]
		for j := range out {
			uu := src[j] << SGRPROJ_RST_BITS
			v := w1 * uu
			if r[0] != 0 {
				v += w0 * sc.flt[0][i*w+j]
			} else {
				v += w0 * uu
			}
			if r[1] != 0 {
				v += w2 * sc.flt[1][i*w+j]
			} else {
				v += w2 * uu
			}
			out[j] = uint16(min(max((v+1<<(sh-1))>>sh, 0), maxV)) //nolint:gosec // a sample
		}
	}
}

// lrRef is a plane's RefLrWiener and RefSgrXqd.
type lrRef struct {
	wiener [2][3]int
	xqd    [2]int
}

func newLRRef() lrRef {
	var r lrRef
	for pass := range 2 {
		for i := range 3 {
			r.wiener[pass][i] = int(wienerTapsMid[i])
		}
		r.xqd[pass] = int(sgrprojXqdMid[pass])
	}
	return r
}

// recenterNonneg is libaom's recenter_nonneg, inverse_recenter's inverse.
func recenterNonneg(r, v int) int {
	switch {
	case v > r<<1:
		return v
	case v >= r:
		return (v - r) << 1
	}
	return (r-v)<<1 - 1
}

// writeNS codes v of n values as NS(n) reads it.
func writeNS(c coder, n, v int) {
	w := bits.Len(uint(n)) //nolint:gosec // FloorLog2(n) + 1
	m := 1<<uint(w) - n
	if v < m {
		c.literal(uint32(v), w-1) //nolint:gosec // small
		return
	}
	c.literal(uint32((v+m)>>1), w-1) //nolint:gosec // small
	c.literal(uint32((v+m)&1), 1)    //nolint:gosec // a bit
}

// writeSubexpBool codes v of numSyms values as decode_subexp_bool reads it.
func writeSubexpBool(c coder, numSyms, k, v int) {
	i, mk := 0, 0
	for {
		b2 := k
		if i > 0 {
			b2 = k + i - 1
		}
		a := 1 << uint(b2) //nolint:gosec // small
		if numSyms <= mk+3*a {
			writeNS(c, numSyms-mk, v-mk)
			return
		}
		more := v >= mk+a
		c.boolean(more)
		if !more {
			c.literal(uint32(v-mk), b2) //nolint:gosec // small
			return
		}
		i++
		mk += a
	}
}

// writeSubexpRefBool codes v, in low..high-1, about the reference r, as
// decode_signed_subexp_with_ref_bool reads it.
func writeSubexpRefBool(c coder, low, high, k, r, v int) {
	mx := high - low
	v, r = v-low, r-low
	if r<<1 <= mx {
		writeSubexpBool(c, mx, k, recenterNonneg(r, v))
	} else {
		writeSubexpBool(c, mx, k, recenterNonneg(mx-1-r, mx-1-v))
	}
}

// writeLRUnit codes a unit as read_lr_unit reads it for plane p of
// FrameRestorationType frameType, with the CDFs cd, updating ref.
func writeLRUnit(c coder, cd *cdfs, p, frameType int, u *lrUnit, ref *lrRef) {
	typ := int(u.typ)
	switch frameType {
	case RESTORE_WIENER:
		c.symbol(b2i(typ == RESTORE_WIENER), cd.useWiener[:])
	case RESTORE_SGRPROJ:
		c.symbol(b2i(typ == RESTORE_SGRPROJ), cd.useSgrproj[:])
	default:
		c.symbol(typ, cd.restorationType[:])
	}
	switch typ {
	case RESTORE_WIENER:
		first := min(p, 1)
		for pass := range 2 {
			for j := first; j < 3; j++ {
				v := int(u.wiener[pass][j])
				writeSubexpRefBool(c, int(wienerTapsMin[j]), int(wienerTapsMax[j])+1, int(wienerTapsK[j]), ref.wiener[pass][j], v)
				ref.wiener[pass][j] = v
			}
		}
	case RESTORE_SGRPROJ:
		c.literal(uint32(u.set), SGRPROJ_PARAMS_BITS)
		for i := range 2 {
			v := int(u.xqd[i])
			if sgrParams[u.set][2*i] != 0 {
				writeSubexpRefBool(c, int(sgrprojXqdMin[i]), int(sgrprojXqdMax[i])+1, SGRPROJ_PRJ_SUBEXP_K, ref.xqd[i], v)
			}
			ref.xqd[i] = v
		}
	}
}

// sgrNormalize sets the weights a set without one of the box filters
// implies (read_lr_unit's), from the first.
func sgrNormalize(u *lrUnit) {
	if sgrParams[u.set][0] == 0 {
		u.xqd[0] = 0
	}
	if sgrParams[u.set][2] == 0 {
		u.xqd[1] = int8(min(max(1<<SGRPROJ_PRJ_BITS-int(u.xqd[0]), int(sgrprojXqdMin[1])), int(sgrprojXqdMax[1]))) //nolint:gosec // in range
	}
}

// lrStats are the search's sums over a part of a plane (a stripe of a
// column of cells): the samples, the CDEF output's squared error, for the
// Wiener filter the folded windows' correlations (see lrCellStats) with
// each other (the upper triangle) and with the source, and the source's
// energy, all about the middle value; for the self-guided filter (16
// times the samples) each box filter's output less the sample, by pass
// and strength, with itself (h00, h11) and with the source less the
// sample (c0, c1), and the two of each set with both together (h01).
type lrStats struct {
	n, sse0, s2 int64
	wH          [16][16]int64
	wM          [16]int64
	h00, c0     [12]int64
	h11, c1     [11]int64
	h01         [16]int64
}

func (s *lrStats) add(o *lrStats) {
	s.n += o.n
	s.sse0 += o.sse0
	s.s2 += o.s2
	for i := range s.wH {
		for j := i; j < 16; j++ {
			s.wH[i][j] += o.wH[i][j]
		}
		s.wM[i] += o.wM[i]
		s.h01[i] += o.h01[i]
	}
	for i := range s.h00 {
		s.h00[i] += o.h00[i]
		s.c0[i] += o.c0[i]
	}
	for i := range s.h11 {
		s.h11[i] += o.h11[i]
		s.c1[i] += o.c1[i]
	}
}

// lrCellStats gathers the statistics of the w x h block of plane p at
// (x0, y0), in the stripe of rows sy0..sy1. The Wiener filter's window of
// a sample, 7x7 (5x5 for chroma), folded by its symmetry, is 4x4 (3x3)
// sums of one, two or four samples: the filter's output is then the sum
// of the products of the vertical and horizontal taps with them.
func (f *frameEnc) lrCellStats(p int, g *lrGeom, cdefd, deb []uint16, x0, y0, w, h, sy0, sy1 int, sc *lrScratch, st *lrStats) {
	ww := w + 6
	sc.win = grow32(sc.win, (h+6)*ww)
	win := sc.win
	f.lrWindow(p, g, cdefd, deb, x0, y0, w, h, sy0, sy1, win)
	stride, src := f.stride[p], f.src[p]
	center := int32(1) << uint(f.bitDepth-1) //nolint:gosec // 8 or 10
	k, t0 := 4, 0
	if p > 0 {
		k, t0 = 3, 1
	}
	kk := k * k
	sc.hfold = grow32(sc.hfold, (h+6)*w*4)
	hf := sc.hfold
	for r := range h + 6 {
		row := win[r*ww : (r+1)*ww]
		for j := range w {
			o := (r*w + j) * 4
			for b := t0; b < 3; b++ {
				hf[o+b-t0] = row[j+b] + row[j+6-b] - 2*center
			}
			hf[o+k-1] = row[j+3] - center
		}
	}
	// a row's sums (kk rows of w, padded to n, and the source's after):
	// the sums of their products by gram
	n := (w + 15) &^ 15
	sc.ymat = grow16(sc.ymat, (kk+1)*n)
	ym := sc.ymat[:(kk+1)*n]
	clear(ym)
	var gm [17 * 17]int64
	for i := range h {
		srow := src[(y0+i)*stride+x0:]
		for j := range w {
			for a := t0; a < 3; a++ {
				o1, o2 := ((i+a)*w+j)*4, ((i+6-a)*w+j)*4
				for b := range k {
					ym[((a-t0)*k+b)*n+j] = int16(hf[o1+b] + hf[o2+b]) //nolint:gosec // bitDepth+2 bits
				}
			}
			o := ((i+3)*w + j) * 4
			for b := range k {
				ym[((k-1)*k+b)*n+j] = int16(hf[o+b]) //nolint:gosec // bitDepth+1 bits
			}
			ym[kk*n+j] = int16(int32(srow[j]) - center) //nolint:gosec // bitDepth bits
		}
		gram(ym, n, kk+1, &gm)
	}
	for u := range kk {
		for v := u; v < kk; v++ {
			st.wH[u][v] += gm[u*17+v]
		}
		st.wM[u] += gm[u*17+kk]
	}
	st.s2 += gm[kk*17+kk]
	// the box filters of every strength
	sc.sgrSizes(w, h)
	for pass := range 2 {
		r := 2 - pass
		sgrBoxSums(win, w, h, r, pass == 0, sc.sum, sc.sq, sc.vs, sc.vq)
		sc.fltS[pass] = grow32(sc.fltS[pass], len(sgrEps[pass])*w*h)
		for e, eps := range sgrEps[pass] {
			sgrAB(sc.sum, sc.sq, w, h, r, eps, f.bitDepth, pass == 0, sc.a, sc.b)
			sgrFilter(win, w, h, pass, sc.a, sc.b, sc.fltS[pass][e*w*h:])
		}
	}
	n0, n1, wh := len(sgrEps[0]), len(sgrEps[1]), w*h
	f0, f1 := sc.fltS[0], sc.fltS[1]
	var d0 [12]int64
	var d1 [11]int64
	for i := range h {
		srow := src[(y0+i)*stride+x0:]
		wrow := win[(i+3)*ww+3:]
		for j := range w {
			x := wrow[j]
			d := int64(srow[j]) - int64(x)
			st.sse0 += d * d
			u := x << SGRPROJ_RST_BITS
			e := d << SGRPROJ_RST_BITS
			at := i*w + j
			for q := range n0 {
				v := int64(f0[q*wh+at] - u)
				d0[q] = v
				st.h00[q] += v * v
				st.c0[q] += v * e
			}
			for q := range n1 {
				v := int64(f1[q*wh+at] - u)
				d1[q] = v
				st.h11[q] += v * v
				st.c1[q] += v * e
			}
			for s := range 16 {
				if i0, i1 := sgrEpsIdx[s][0], sgrEpsIdx[s][1]; i0 >= 0 && i1 >= 0 {
					st.h01[s] += d0[i0] * d1[i1]
				}
			}
		}
	}
	st.n += int64(wh)
}

// solveSmall solves the n x n system a x = b (Gaussian elimination),
// reporting whether it could.
func solveSmall(a *[3][3]float64, b *[3]float64, n int) ([3]float64, bool) {
	var x [3]float64
	m, v := *a, *b
	scale := 0.0
	for i := range n {
		scale = max(scale, math.Abs(m[i][i]))
	}
	if scale == 0 {
		return x, false
	}
	for c := range n {
		piv := c
		for r := c + 1; r < n; r++ {
			if math.Abs(m[r][c]) > math.Abs(m[piv][c]) {
				piv = r
			}
		}
		if math.Abs(m[piv][c]) < 1e-12*scale {
			return x, false
		}
		m[c], m[piv] = m[piv], m[c]
		v[c], v[piv] = v[piv], v[c]
		for r := c + 1; r < n; r++ {
			k := m[r][c] / m[c][c]
			for cc := c; cc < n; cc++ {
				m[r][cc] -= k * m[c][cc]
			}
			v[r] -= k * v[c]
		}
	}
	for r := n - 1; r >= 0; r-- {
		s := v[r]
		for cc := r + 1; cc < n; cc++ {
			s -= m[r][cc] * x[cc]
		}
		x[r] = s / m[r][r]
	}
	return x, true
}

// wienerSolver solves a unit's Wiener filter from its statistics.
type wienerSolver struct {
	k, t0 int // the folded taps (4 or 3) and the first coded coefficient
	h     [16][16]float64
	m     [16]float64
	s2    float64
}

// solveDim is the folded taps of one pass (the vertical, or with trans
// the horizontal) of least error with the other pass's taps other
// (update_a_sep_sym and update_b_sep_sym): the taps but the centre free,
// the centre making the gain one.
func (ws *wienerSolver) solveDim(other [4]float64, trans bool, prev [4]float64) [4]float64 {
	k := ws.k
	idx := func(i, j int) int {
		if trans {
			return j*k + i
		}
		return i*k + j
	}
	var zz [4][4]float64
	var sz [4]float64
	for a := range k {
		for c := range k {
			s := 0.0
			for b := range k {
				for d := range k {
					s += other[b] * other[d] * ws.h[idx(a, b)][idx(c, d)]
				}
			}
			zz[a][c] = s
		}
		s := 0.0
		for b := range k {
			s += other[b] * ws.m[idx(a, b)]
		}
		sz[a] = s
	}
	c, n := k-1, k-1
	var g [3][3]float64
	var rhs [3]float64
	for a := range n {
		for a2 := range n {
			g[a][a2] = zz[a][a2] - 2*zz[a][c] - 2*zz[c][a2] + 4*zz[c][c]
		}
		rhs[a] = sz[a] - 2*sz[c] - zz[a][c] + 2*zz[c][c]
	}
	x, ok := solveSmall(&g, &rhs, n)
	if !ok {
		return prev
	}
	var t [4]float64
	t[c] = 1
	for a := range n {
		t[a] = x[a]
		t[c] -= 2 * x[a]
	}
	return t
}

// folded is a pass's folded taps from its coefficients.
func (ws *wienerSolver) folded(c [3]int8) (t [4]float64) {
	ctr := 128
	for j := ws.t0; j < 3; j++ {
		t[j-ws.t0] = float64(c[j]) / 128
		ctr -= 2 * int(c[j])
	}
	t[ws.k-1] = float64(ctr) / 128
	return t
}

// score is the error the statistics predict for the coefficients
// (compute_score).
func (ws *wienerSolver) score(c *[2][3]int8) float64 {
	v, h := ws.folded(c[0]), ws.folded(c[1])
	k := ws.k
	var fv [16]float64
	for a := range k {
		for b := range k {
			fv[a*k+b] = v[a] * h[b]
		}
	}
	e := ws.s2
	for u := range k * k {
		s := 0.0
		for w := range k * k {
			s += ws.h[u][w] * fv[w]
		}
		e += fv[u] * (s - 2*ws.m[u])
	}
	return e
}

// wienerSolve is a unit's Wiener filter from its statistics and the error
// predicted for it.
func wienerSolve(st *lrStats, chroma bool) (lrUnit, float64) {
	ws := &wienerSolver{k: 4}
	if chroma {
		ws.k, ws.t0 = 3, 1
	}
	kk := ws.k * ws.k
	for u := range kk {
		for v := u; v < kk; v++ {
			ws.h[u][v] = float64(st.wH[u][v])
			ws.h[v][u] = ws.h[u][v]
		}
		ws.m[u] = float64(st.wM[u])
	}
	ws.s2 = float64(st.s2)
	var mid [3]int8
	if !chroma {
		mid[0] = wienerTapsMid[0]
	}
	mid[1], mid[2] = wienerTapsMid[1], wienerTapsMid[2]
	h := ws.folded(mid)
	v := h
	for range 5 {
		v = ws.solveDim(h, false, v)
		h = ws.solveDim(v, true, h)
	}
	u := lrUnit{typ: RESTORE_WIENER}
	for pass, t := range [2][4]float64{v, h} {
		for j := ws.t0; j < 3; j++ {
			q := math.Round(t[j-ws.t0] * 128)
			u.wiener[pass][j] = int8(min(max(q, float64(wienerTapsMin[j])), float64(wienerTapsMax[j])))
		}
	}
	best := ws.score(&u.wiener)
	for s := int8(4); s >= 1; s >>= 1 {
		for pass := range 2 {
			for j := ws.t0; j < 3; j++ {
				for range 16 {
					moved := false
					for _, d := range [2]int8{-s, s} {
						c := u.wiener
						nv := int(c[pass][j]) + int(d)
						if nv < int(wienerTapsMin[j]) || nv > int(wienerTapsMax[j]) {
							continue
						}
						c[pass][j] = int8(nv) //nolint:gosec // in range
						if e := ws.score(&c); e < best {
							u.wiener, best, moved = c, e, true
							break
						}
					}
					if !moved {
						break
					}
				}
			}
		}
	}
	return u, max(best, 0)
}

// sgrSolve is a unit's best self-guided set from its statistics, its
// weights, and the error predicted for it.
func sgrSolve(st *lrStats) (lrUnit, float64) {
	best, bestErr := lrUnit{typ: RESTORE_SGRPROJ}, math.Inf(1)
	ee := float64(st.sse0) * (1 << (2 * SGRPROJ_RST_BITS))
	for s := range 16 {
		i0, i1 := sgrEpsIdx[s][0], sgrEpsIdx[s][1]
		var h00, h11, h01, c0, c1 float64
		if i0 >= 0 {
			h00, c0 = float64(st.h00[i0]), float64(st.c0[i0])
		}
		if i1 >= 0 {
			h11, c1 = float64(st.h11[i1]), float64(st.c1[i1])
		}
		var al, be float64 // w0/128 and w2/128
		switch {
		case i0 >= 0 && i1 >= 0:
			h01 = float64(st.h01[s])
			if det := h00*h11 - h01*h01; det > 1e-9*h00*h11 {
				al, be = (c0*h11-c1*h01)/det, (c1*h00-c0*h01)/det
			}
		case i0 >= 0 && h00 > 0:
			al = c0 / h00
		case i1 >= 0 && h11 > 0:
			be = c1 / h11
		}
		// encode_xq
		u := lrUnit{typ: RESTORE_SGRPROJ, set: uint8(s)} //nolint:gosec // < 16
		w0, w2 := int(math.Round(al*128)), int(math.Round(be*128))
		clip := func(v, i int) int8 { return int8(min(max(v, int(sgrprojXqdMin[i])), int(sgrprojXqdMax[i]))) } //nolint:gosec // in range
		switch {
		case i0 < 0:
			u.xqd[1] = clip(128-w2, 1)
		case i1 < 0:
			u.xqd[0] = clip(w0, 0)
		default:
			u.xqd[0] = clip(w0, 0)
			u.xqd[1] = clip(128-int(u.xqd[0])-w2, 1)
		}
		sgrNormalize(&u)
		a := float64(u.xqd[0]) / 128
		b := float64(128-int(u.xqd[0])-int(u.xqd[1])) / 128
		if i0 < 0 {
			a = 0
		}
		if i1 < 0 {
			b = 0
		}
		e := ee - 2*(a*c0+b*c1) + a*a*h00 + 2*a*b*h01 + b*b*h11
		if e < bestErr {
			best, bestErr = u, e
		}
	}
	return best, max(bestErr, 0) / (1 << (2 * SGRPROJ_RST_BITS))
}

// lrCand is a unit's candidates: the errors with no filter, the Wiener
// filter and the self-guided filter, and their parameters.
type lrCand struct {
	sse    [3]float64
	wiener lrUnit
	sgr    lrUnit
}

// lrParallel runs fn(i) for each i < n, on the CPUs.
func lrParallel(n int, fn func(i int, sc *lrScratch)) {
	var next atomic.Int32
	var wg sync.WaitGroup
	for range min(runtime.GOMAXPROCS(0), n) {
		wg.Go(func() {
			var sc lrScratch
			for {
				i := int(next.Add(1)) - 1
				if i >= n {
					return
				}
				fn(i, &sc)
			}
		})
	}
	wg.Wait()
}

// lrOrder is plane p's units (unitRow * unitCols + unitCol) in the order
// they are coded, -1 where a tile starts.
func (f *frameEnc) lrOrder(p int, g *lrGeom) []int {
	t := &f.fp.tiles
	var out []int
	for tr := range t.rows() {
		for tc := range t.cols() {
			out = append(out, -1)
			for sbr := t.rowStarts[tr]; sbr < t.rowStarts[tr+1]; sbr++ {
				for sbc := t.colStarts[tc]; sbc < t.colStarts[tc+1]; sbc++ {
					r0, r1, c0, c1 := g.sbUnits(p, sbr*16, sbc*16)
					for ur := r0; ur < r1; ur++ {
						for uc := c0; uc < c1; uc++ {
							out = append(out, ur*g.cols+uc)
						}
					}
				}
			}
		}
	}
	return out
}

// lrDecide chooses each unit's filter for plane p of FrameRestorationType
// frameType, in coding order, by error plus rate, into units; it gives
// the total.
func (f *frameEnc) lrDecide(p, frameType int, cands []lrCand, order []int, units []lrUnit) float64 {
	cd := f.cdf0
	ref := newLRRef()
	total := 0.0
	for _, i := range order {
		if i < 0 {
			ref = newLRRef()
			continue
		}
		c := &cands[i]
		bestRD, best, bestRef := math.Inf(1), lrUnit{}, ref
		for typ, u := range [3]*lrUnit{{}, &c.wiener, &c.sgr} {
			if math.IsInf(c.sse[typ], 1) || (frameType == RESTORE_WIENER && typ == RESTORE_SGRPROJ) ||
				(frameType == RESTORE_SGRPROJ && typ == RESTORE_WIENER) {
				continue
			}
			r := ref
			var cc costCoder
			writeLRUnit(&cc, cd, p, frameType, u, &r)
			if rd := c.sse[typ] + f.lambda*float64(cc.n)/costScale; rd < bestRD {
				bestRD, best, bestRef = rd, *u, r
			}
		}
		units[i], ref = best, bestRef
		total += bestRD
	}
	return total
}

// lrSearchSize gives plane p's candidates for units of size, from the
// cells' statistics (stripes by columns of cw).
func lrSearchSize(g *lrGeom, cells []lrStats, cw int, chroma bool) []lrCand {
	nc := (g.pw + cw - 1) / cw
	cands := make([]lrCand, g.rows*g.cols)
	lrParallel(len(cands), func(i int, _ *lrScratch) {
		x0, x1, y0, y1 := g.unitRect(i/g.cols, i%g.cols)
		var st lrStats
		for k := (y0 + g.off) / g.sh; k <= (y1-1+g.off)/g.sh; k++ {
			for m := x0 / cw; m <= (x1-1)/cw; m++ {
				st.add(&cells[k*nc+m])
			}
		}
		c := &cands[i]
		c.sse[0] = float64(st.sse0)
		c.wiener, c.sse[1] = wienerSolve(&st, chroma)
		c.sgr, c.sse[2] = sgrSolve(&st)
	})
	return cands
}

// restore decides the frame's loop restoration (or takes fixed settings)
// into f.fp, applies it to f.rec (the CDEF output; deb is the frame
// before CDEF) and gives it for the units' symbols (nil when no plane is
// restored).
func (f *frameEnc) restore(deb [3][]uint16, fixed *lrSettings) *lrFrame {
	fp := f.fp
	fp.lrType, fp.lrUnitShift, fp.lrUVShift = [3]int{}, 0, 0
	if !f.seq.restoration || f.lossless {
		return nil
	}
	cdefd := f.rec
	// the cells' statistics
	var cells [3][]lrStats
	var cellG [3]lrGeom
	type cellJob struct{ p, k, m int }
	var jobs []cellJob
	for p := range 3 {
		g := f.lrGeometry(p, 64)
		cellG[p] = g
		cw := 64 >> uint(min(p, 1)) //nolint:gosec // 0 or 1
		nc := (g.pw + cw - 1) / cw
		cells[p] = make([]lrStats, g.stripes()*nc)
		for i := range cells[p] {
			jobs = append(jobs, cellJob{p, i / nc, i % nc})
		}
	}
	lrParallel(len(jobs), func(i int, sc *lrScratch) {
		j := jobs[i]
		g := &cellG[j.p]
		cw := 64 >> uint(min(j.p, 1)) //nolint:gosec // 0 or 1
		nc := (g.pw + cw - 1) / cw
		sy0, sy1 := g.stripe(j.k)
		y0, y1 := max(sy0, 0), min(sy1+1, g.ph)
		x0, x1 := j.m*cw, min((j.m+1)*cw, g.pw)
		f.lrCellStats(j.p, g, cdefd[j.p], deb[j.p], x0, y0, x1-x0, y1-y0, sy0, sy1, sc, &cells[j.p][j.k*nc+j.m])
	})
	// each plane's candidates and estimated cost by unit size (luma 64 to
	// 256, chroma 32 to 256)
	type sizeResult struct {
		g     lrGeom
		cands []lrCand
		rd    float64
	}
	results := map[[2]int]*sizeResult{}
	none := [3]float64{}
	for p := range 3 {
		for _, c := range cells[p] {
			none[p] += float64(c.sse0)
		}
	}
	evaluate := func(p, size int) *sizeResult {
		if r := results[[2]int{p, size}]; r != nil {
			return r
		}
		g := f.lrGeometry(p, size)
		cw := 64 >> uint(min(p, 1)) //nolint:gosec // 0 or 1
		r := &sizeResult{g: g, cands: lrSearchSize(&g, cells[p], cw, p > 0), rd: none[p]}
		order := f.lrOrder(p, &g)
		units := make([]lrUnit, g.rows*g.cols)
		for _, typ := range [3]int{RESTORE_WIENER, RESTORE_SGRPROJ, RESTORE_SWITCHABLE} {
			r.rd = min(r.rd, f.lrDecide(p, typ, r.cands, order, units))
		}
		results[[2]int{p, size}] = r
		return r
	}
	shift, uvShift := 0, 0
	if fixed != nil {
		shift, uvShift = fixed.unitShift, fixed.uvShift
	} else {
		best := math.Inf(1)
		for s := range 3 {
			for uv := range 2 {
				size := 64 << uint(s)                                                                        //nolint:gosec // 0..2
				rd := evaluate(0, size).rd + evaluate(1, size>>uint(uv)).rd + evaluate(2, size>>uint(uv)).rd //nolint:gosec // 0 or 1
				if rd < best {
					best, shift, uvShift = rd, s, uv
				}
			}
		}
	}
	lr := &lrFrame{}
	used := false
	for p := range 3 {
		size := 64 << uint(shift) //nolint:gosec // 0..2
		if p > 0 {
			size >>= uint(uvShift) //nolint:gosec // 0 or 1
		}
		r := evaluate(p, size)
		pl := &lr[p]
		pl.lrGeom = r.g
		pl.units = make([]lrUnit, r.g.rows*r.g.cols)
		if fixed != nil {
			pl.typ = fixed.types[p]
			f.lrFixedUnits(p, pl, r.cands, fixed.random)
		} else {
			f.lrChoose(p, pl, r.cands, cdefd[p], deb[p], none[p])
		}
		used = used || pl.typ != RESTORE_NONE
	}
	if !used {
		return nil
	}
	fp.lrType = [3]int{lr[0].typ, lr[1].typ, lr[2].typ}
	fp.lrUnitShift, fp.lrUVShift = shift, uvShift
	if lr[1].typ == RESTORE_NONE && lr[2].typ == RESTORE_NONE {
		fp.lrUVShift = 0
	}
	f.lrApply(lr, deb)
	return lr
}

// lrChoose filters plane p's units with their candidates for the exact
// errors, then picks the plane's type and its units' filters (none when
// nothing gains on noneSSE, the error with no filter).
func (f *frameEnc) lrChoose(p int, pl *lrPlane, cands []lrCand, cdefd, deb []uint16, noneSSE float64) {
	g := &pl.lrGeom
	stride := f.stride[p]
	src := f.src[p]
	lrParallel(len(cands), func(i int, sc *lrScratch) {
		c := &cands[i]
		for typ, u := range [3]*lrUnit{nil, &c.wiener, &c.sgr} {
			if u == nil {
				continue
			}
			var se int64
			g.segments(i/g.cols, i%g.cols, func(x0, y0, w, h, sy0, sy1 int) {
				if cap(sc.out) < w*h {
					sc.out = make([]uint16, w*h)
				}
				out := sc.out[:w*h]
				f.lrFilterBlock(p, g, u, cdefd, deb, x0, y0, w, h, sy0, sy1, out, w, sc)
				for y := range h {
					srow := src[(y0+y)*stride+x0:]
					for x, v := range out[y*w : (y+1)*w] {
						d := int64(srow[x]) - int64(v)
						se += d * d
					}
				}
			})
			c.sse[typ] = float64(se)
		}
	})
	order := f.lrOrder(p, g)
	best, bestTyp := noneSSE, RESTORE_NONE
	units := make([]lrUnit, len(pl.units))
	for _, typ := range [3]int{RESTORE_WIENER, RESTORE_SGRPROJ, RESTORE_SWITCHABLE} {
		if rd := f.lrDecide(p, typ, cands, order, units); rd < best {
			best, bestTyp = rd, typ
			copy(pl.units, units)
		}
	}
	pl.typ = bestTyp
}

// lrFixedUnits sets plane p's units for fixed settings: the plane type's
// filter with the searched parameters (switchable: each in turn), or
// random filters and parameters.
func (f *frameEnc) lrFixedUnits(p int, pl *lrPlane, cands []lrCand, random bool) {
	rng := rand.New(rand.NewPCG(uint64(p), uint64(f.fp.orderHint))) //nolint:gosec // test settings
	for i := range pl.units {
		u := &pl.units[i]
		typ := pl.typ
		switch {
		case typ == RESTORE_NONE:
			continue
		case random && typ == RESTORE_SWITCHABLE:
			typ = rng.IntN(3)
		case random:
			typ *= rng.IntN(2)
		case typ == RESTORE_SWITCHABLE:
			typ = i % 3
		}
		switch {
		case typ == RESTORE_WIENER && random:
			*u = lrUnit{typ: RESTORE_WIENER}
			for pass := range 2 {
				for j := min(p, 1); j < 3; j++ {
					u.wiener[pass][j] = int8(int(wienerTapsMin[j]) + rng.IntN(int(wienerTapsMax[j]-wienerTapsMin[j])+1)) //nolint:gosec // in range
				}
			}
		case typ == RESTORE_SGRPROJ && random:
			*u = lrUnit{typ: RESTORE_SGRPROJ, set: uint8(rng.IntN(16))} //nolint:gosec // < 16
			for k := range 2 {
				u.xqd[k] = int8(int(sgrprojXqdMin[k]) + rng.IntN(int(sgrprojXqdMax[k]-sgrprojXqdMin[k])+1)) //nolint:gosec // in range
			}
			sgrNormalize(u)
		case typ == RESTORE_WIENER:
			*u = cands[i].wiener
		case typ == RESTORE_SGRPROJ:
			*u = cands[i].sgr
		}
	}
}

// lrApply filters f.rec (the CDEF output) by the frame's restoration
// units into a new frame, f.rec then.
func (f *frameEnc) lrApply(lr *lrFrame, deb [3][]uint16) {
	var out [3][]uint16
	type job struct{ p, i int }
	var jobs []job
	for p := range 3 {
		out[p] = append([]uint16(nil), f.rec[p]...)
		if lr[p].typ == RESTORE_NONE {
			continue
		}
		for i, u := range lr[p].units {
			if u.typ != RESTORE_NONE {
				jobs = append(jobs, job{p, i})
			}
		}
	}
	lrParallel(len(jobs), func(k int, sc *lrScratch) {
		p, i := jobs[k].p, jobs[k].i
		pl := &lr[p]
		g := &pl.lrGeom
		stride := f.stride[p]
		g.segments(i/g.cols, i%g.cols, func(x0, y0, w, h, sy0, sy1 int) {
			f.lrFilterBlock(p, g, &pl.units[i], f.rec[p], deb[p], x0, y0, w, h, sy0, sy1, out[p][y0*stride+x0:], stride, sc)
		})
	})
	f.rec = out
}

// writeLRSB codes the units of the superblock at mi (r, c) (read_lr).
func writeLRSB(c coder, cd *cdfs, lr *lrFrame, r, col int, refs *[3]lrRef) {
	for p := range 3 {
		pl := &lr[p]
		if pl.typ == RESTORE_NONE {
			continue
		}
		r0, r1, c0, c1 := pl.sbUnits(p, r, col)
		for ur := r0; ur < r1; ur++ {
			for uc := c0; uc < c1; uc++ {
				writeLRUnit(c, cd, p, pl.typ, &pl.units[ur*pl.cols+uc], &refs[p])
			}
		}
	}
}
