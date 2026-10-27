package av1

// The constrained directional enhancement filter (spec 7.15), run on the
// deblocked frame. Each 8x8 luma block of a 64x64 with a cdef_idx, unless
// all its blocks are skipped, gets a direction from its luma samples (the
// line of best fit of eight, by the partial sums of the samples along
// each) and a variance; then every sample of the block and of its chroma
// is filtered: a primary filter along the direction (its strength scaled
// by the variance for luma) and a secondary filter across it, each tap's
// difference constrained by the strength and the damping, and the result
// clipped to the taps' range when both filters act. Samples outside the
// frame (beyond MiCols and MiRows) are not taps. Every block reads the
// deblocked frame, so the filter writes a new one.
//
// The encoder picks the strengths as libaom's pickcdef does: every 64x64
// filtered with each of the 64 luma and 64 chroma strength pairs, its
// squared error against the source noted, then for each cdef_bits the
// set of 1<<cdef_bits luma and chroma pairs chosen greedily (each 64x64
// taking the best of the set), and the cdef_bits of least error plus rate
// kept. Every damping is tried so (libaom derives it from q). The errors
// for all strengths of a sample come from the constrained tap
// differences once per strength of each filter, the two filters' sums
// then combined for each pair.

import (
	"math"
	"math/bits"
	"runtime"
	"sync"
	"sync/atomic"
)

// cdefVeryLarge marks a tap outside the frame in a filter window.
const cdefVeryLarge = 30000

// cdefSecValues are the secondary strengths by cdef_*_sec_strength code.
var cdefSecValues = [4]int{0, 1, 2, 4}

// cdefSettings are a frame's CDEF parameters (for the test hook): the
// damping (3..6), cdef_bits and the strength pairs {primary, secondary}
// by index.
type cdefSettings struct {
	damping, bits int
	y, uv         [8][2]int
}

// cdefDirection is the CDEF direction process (spec 7.15.2) for the 8x8
// luma block at (x0, y0) of in: its direction and variance.
func (f *frameEnc) cdefDirection(in []uint16, x0, y0 int) (int, int) {
	var cost [8]int64
	var partial [8][15]int64
	stride := f.stride[0]
	shift := uint(f.bitDepth - 8) //nolint:gosec // 0 or 2
	for i := range 8 {
		row := in[(y0+i)*stride+x0:]
		for j := range 8 {
			x := int64(row[j]>>shift) - 128
			partial[0][i+j] += x
			partial[1][i+j/2] += x
			partial[2][i] += x
			partial[3][3+i-j/2] += x
			partial[4][7+i-j] += x
			partial[5][3-i/2+j] += x
			partial[6][j] += x
			partial[7][i/2+j] += x
		}
	}
	for i := range 8 {
		cost[2] += partial[2][i] * partial[2][i]
		cost[6] += partial[6][i] * partial[6][i]
	}
	cost[2] *= int64(divTable[8])
	cost[6] *= int64(divTable[8])
	for i := range 7 {
		cost[0] += (partial[0][i]*partial[0][i] + partial[0][14-i]*partial[0][14-i]) * int64(divTable[i+1])
		cost[4] += (partial[4][i]*partial[4][i] + partial[4][14-i]*partial[4][14-i]) * int64(divTable[i+1])
	}
	cost[0] += partial[0][7] * partial[0][7] * int64(divTable[8])
	cost[4] += partial[4][7] * partial[4][7] * int64(divTable[8])
	for i := 1; i < 8; i += 2 {
		for j := range 5 {
			cost[i] += partial[i][3+j] * partial[i][3+j]
		}
		cost[i] *= int64(divTable[8])
		for j := range 3 {
			cost[i] += (partial[i][j]*partial[i][j] + partial[i][10-j]*partial[i][10-j]) * int64(divTable[2*j+2])
		}
	}
	var best int64
	dir := 0
	for i, c := range cost {
		if c > best {
			best, dir = c, i
		}
	}
	return dir, int((best - cost[(dir+4)&7]) >> 10)
}

// cdefWindow copies the w x h block of plane p of in at (x0, y0), with a
// border of 2, into win ((w+4) wide), cdefVeryLarge where outside the
// frame.
func (f *frameEnc) cdefWindow(p int, in []uint16, x0, y0, w, h int, win []int32) {
	sub := uint(min(p, 1)) //nolint:gosec // 0 or 1
	limW, limH := (f.miCols*4)>>sub, (f.miRows*4)>>sub
	stride := f.stride[p]
	ww := w + 4
	for i := -2; i < h+2; i++ {
		y := y0 + i
		row := win[(i+2)*ww : (i+3)*ww]
		if y < 0 || y >= limH {
			for j := range row {
				row[j] = cdefVeryLarge
			}
			continue
		}
		src := in[y*stride:]
		for j := -2; j < w+2; j++ {
			if x := x0 + j; x < 0 || x >= limW {
				row[j+2] = cdefVeryLarge
			} else {
				row[j+2] = int32(src[x])
			}
		}
	}
}

// cdefOffsets are the taps' offsets in a window ww wide, by direction and
// distance.
func cdefOffsets(ww int) (off [8][2]int) {
	for d := range 8 {
		for k := range 2 {
			off[d][k] = int(cdefDirections[d][k][0])*ww + int(cdefDirections[d][k][1])
		}
	}
	return off
}

// constrainShift is constrain's damping adjustment for a strength t > 0.
func constrainShift(t, damping int) uint {
	return uint(max(0, damping-(bits.Len(uint(t))-1))) //nolint:gosec // small
}

// constrain is the spec's constrain, with the damping adjustment shift
// precomputed (any when t is 0).
func constrain(d, t int32, shift uint) int32 {
	if d < 0 {
		v := t - (-d)>>shift
		if v <= 0 {
			return 0
		}
		return -min(-d, v)
	}
	v := t - d>>shift
	if v <= 0 {
		return 0
	}
	return min(d, v)
}

// cdefFilter is the CDEF filter process (spec 7.15.3) of a w x h block
// from its window (see cdefWindow) into out.
func cdefFilter(win []int32, w, h int, out []uint16, stride int, pri, sec, damping, dir, coeffShift int) {
	ww := w + 4
	off := cdefOffsets(ww)
	priTaps := cdefPriTaps[(pri>>uint(coeffShift))&1] //nolint:gosec // 0 or 2
	secTaps := cdefSecTaps[(pri>>uint(coeffShift))&1] //nolint:gosec // 0 or 2
	var priShift, secShift uint
	if pri > 0 {
		priShift = constrainShift(pri, damping)
	}
	if sec > 0 {
		secShift = constrainShift(sec, damping)
	}
	p32, s32 := int32(pri), int32(sec) //nolint:gosec // small
	for i := range h {
		for j := range w {
			c := (i+2)*ww + j + 2
			x := win[c]
			var sum int32
			mn, mx := x, x
			for k := range 2 {
				for sign := -1; sign <= 1; sign += 2 {
					if p := win[c+sign*off[dir][k]]; p != cdefVeryLarge {
						sum += int32(priTaps[k]) * constrain(p-x, p32, priShift)
						mn, mx = min(mn, p), max(mx, p)
					}
					for _, d2 := range [2]int{(dir + 2) & 7, (dir + 6) & 7} {
						if s := win[c+sign*off[d2][k]]; s != cdefVeryLarge {
							sum += int32(secTaps[k]) * constrain(s-x, s32, secShift)
							mn, mx = min(mn, s), max(mx, s)
						}
					}
				}
			}
			neg := int32(0)
			if sum < 0 {
				neg = 1
			}
			out[i*stride+j] = uint16(min(max(x+((8+sum-neg)>>4), mn), mx)) //nolint:gosec // a sample
		}
	}
}

// cdefSkipped reports whether the 8x8 luma block at mi (r, c) is all
// skip.
func (f *frameEnc) cdefSkipped(r, c int) bool {
	m := f.mi(r, c)
	return f.skip[m] != 0 && f.skip[m+1] != 0 && f.skip[m+f.miCols] != 0 && f.skip[m+f.miCols+1] != 0
}

// cdefAdjust is the luma primary strength adjusted by the variance.
func cdefAdjust(pri, v int) int {
	if v == 0 {
		return 0
	}
	varStr := 0
	if v>>6 != 0 {
		varStr = min(bits.Len(uint(v>>6))-1, 12) //nolint:gosec // positive
	}
	return (pri*(4+varStr) + 8) >> 4
}

// cdefBlock is the CDEF block process (spec 7.15.1) for the 8x8 luma
// block at mi (r, c), from in into out (both laid out as f.rec), its
// strengths those of index idx of fp.
func (f *frameEnc) cdefBlock(r, c, idx int, in, out *[3][]uint16, win []int32) {
	fp := f.fp
	if idx < 0 || f.cdefSkipped(r, c) {
		return
	}
	cs := f.bitDepth - 8
	ucs := uint(cs) //nolint:gosec // 0 or 2
	yPri, ySec := fp.cdefY[idx][0]<<ucs, fp.cdefY[idx][1]<<ucs
	uvPri, uvSec := fp.cdefUV[idx][0]<<ucs, fp.cdefUV[idx][1]<<ucs
	if yPri == 0 && ySec == 0 && uvPri == 0 && uvSec == 0 {
		return
	}
	x0, y0 := c*4, r*4
	yDir, v := f.cdefDirection(in[0], x0, y0)
	if yPri != 0 || ySec != 0 {
		dir := 0
		if yPri != 0 {
			dir = yDir
		}
		f.cdefWindow(0, in[0], x0, y0, 8, 8, win)
		cdefFilter(win, 8, 8, out[0][y0*f.stride[0]+x0:], f.stride[0], cdefAdjust(yPri, v), ySec, fp.cdefDamping+cs, dir, cs)
	}
	if uvPri != 0 || uvSec != 0 {
		dir := 0
		if uvPri != 0 {
			dir = int(cdefUvDir[1][1][yDir])
		}
		for p := 1; p <= 2; p++ {
			f.cdefWindow(p, in[p], x0/2, y0/2, 4, 4, win)
			cdefFilter(win, 4, 4, out[p][y0/2*f.stride[p]+x0/2:], f.stride[p], uvPri, uvSec, fp.cdefDamping+cs-1, dir, cs)
		}
	}
}

// forSBRows runs fn on each superblock row, rows in parallel.
func (f *frameEnc) forSBRows(fn func(sbr int, win []int32)) {
	rows := (f.miRows + 15) >> 4
	var next atomic.Int32
	var wg sync.WaitGroup
	for range min(runtime.GOMAXPROCS(0), rows) {
		wg.Go(func() {
			win := make([]int32, 12*12)
			for {
				r := int(next.Add(1)) - 1
				if r >= rows {
					return
				}
				fn(r, win)
			}
		})
	}
	wg.Wait()
}

// cdefApply filters f.rec with the frame's CDEF parameters and f.cdefIdx.
func (f *frameEnc) cdefApply() {
	var in, out [3][]uint16
	for p := range 3 {
		in[p] = f.rec[p]
		out[p] = append([]uint16(nil), f.rec[p]...)
	}
	sbCols := (f.miCols + 15) >> 4
	f.forSBRows(func(sbr int, win []int32) {
		for r := sbr * 16; r < min(sbr*16+16, f.miRows); r += 2 {
			for c := 0; c < f.miCols; c += 2 {
				f.cdefBlock(r, c, int(f.cdefIdx[(r>>4)*sbCols+c>>4]), &in, &out, win)
			}
		}
	})
	f.rec = out
}

// cdef decides the frame's CDEF parameters and each 64x64's cdef_idx (or
// takes fixed ones, a coded 64x64's index then its number among them
// modulo 1<<bits) and applies the filter to f.rec.
func (f *frameEnc) cdef(fixed *cdefSettings) {
	fp := f.fp
	if !f.seq.cdef || f.lossless {
		return
	}
	var s cdefSettings
	if fixed != nil {
		s = *fixed
		n := 0
		for i, v := range f.cdefIdx {
			if v >= 0 {
				f.cdefIdx[i] = int8(n % (1 << uint(s.bits))) //nolint:gosec // < 8
				n++
			}
		}
	} else {
		s = f.cdefSearch()
	}
	fp.cdefDamping, fp.cdefBits, fp.cdefY, fp.cdefUV = s.damping, s.bits, s.y, s.uv
	f.cdefApply()
}

// cdefSSE is a 64x64's squared error by strength pair (primary*4 +
// secondary code), luma's and chroma's (both planes).
type cdefSSE struct {
	y, uv [64]int64
}

// cdefSearch picks the CDEF parameters, setting f.cdefIdx: for each
// damping, the strengths and cdef_bits of least error plus rate.
func (f *frameEnc) cdefSearch() cdefSettings {
	var coded []int // the 64x64s with a cdef_idx
	for i, v := range f.cdefIdx {
		if v >= 0 {
			coded = append(coded, i)
		}
	}
	s := cdefSettings{damping: 3 + f.fp.qIndex>>6}
	if len(coded) == 0 {
		return s
	}
	bestRD := math.Inf(1)
	idx := make([]int8, len(coded))
	lo, hi := 3, 6
	if f.tools.cdefDampQ {
		lo, hi = s.damping, s.damping
	}
	for damping := lo; damping <= hi; damping++ {
		if c, rd := f.cdefTry(damping, coded, idx); rd < bestRD {
			s, bestRD = c, rd
			for k, sb := range coded {
				f.cdefIdx[sb] = idx[k]
			}
		}
	}
	return s
}

// cdefTry picks the strengths and cdef_bits for a damping, giving them,
// their error plus rate, and each coded 64x64's index in idx.
func (f *frameEnc) cdefTry(damping int, coded []int, idx []int8) (cdefSettings, float64) {
	s := cdefSettings{damping: damping}
	sbCols := (f.miCols + 15) >> 4
	sse := make([]cdefSSE, len(f.cdefIdx))
	f.forSBRows(func(sbr int, win []int32) {
		for sbc := range sbCols {
			if f.cdefIdx[sbr*sbCols+sbc] >= 0 {
				f.cdefSearchSB(sbr, sbc, damping, &sse[sbr*sbCols+sbc], win)
			}
		}
	})
	list := make([]*cdefSSE, len(coded))
	for i, sb := range coded {
		list[i] = &sse[sb]
	}
	type choice struct {
		levY, levUV [8]int
		rd          float64
	}
	var choices [4]choice
	var wg sync.WaitGroup
	for b := range 4 {
		wg.Go(func() {
			ch := &choices[b]
			tot := cdefJointSearch(&ch.levY, &ch.levUV, 1<<uint(b), list)
			ch.rd = float64(tot) + f.lambda*float64(len(coded)*b+(1<<uint(b))*12)
		})
	}
	wg.Wait()
	best := 0
	for b := 1; b < 4; b++ {
		if choices[b].rd < choices[best].rd {
			best = b
		}
	}
	ch := &choices[best]
	s.bits = best
	n := 1 << uint(best)
	for i := range n {
		s.y[i] = [2]int{ch.levY[i] >> 2, cdefSecValues[ch.levY[i]&3]}
		s.uv[i] = [2]int{ch.levUV[i] >> 2, cdefSecValues[ch.levUV[i]&3]}
	}
	for k, e := range list {
		bi, be := 0, int64(-1)
		for i := range n {
			if v := e.y[ch.levY[i]] + e.uv[ch.levUV[i]]; be < 0 || v < be {
				bi, be = i, v
			}
		}
		idx[k] = int8(bi) //nolint:gosec // < 8
	}
	return s, ch.rd
}

// cdefJointSearch is libaom's joint_strength_search_dual: n luma and
// chroma strength pairs chosen greedily, one at a time, then each
// reconsidered in turn; it gives the total error.
func cdefJointSearch(levY, levUV *[8]int, n int, list []*cdefSSE) int64 {
	var tot int64
	for i := range n {
		tot = cdefSearchOne(levY, levUV, i, list)
	}
	for range 4 * n {
		copy(levY[:n-1], levY[1:n])
		copy(levUV[:n-1], levUV[1:n])
		tot = cdefSearchOne(levY, levUV, n-1, list)
	}
	return tot
}

// cdefSearchOne adds the luma and chroma pair that best complements the
// first n of levY and levUV, giving the total error with it.
func cdefSearchOne(levY, levUV *[8]int, n int, list []*cdefSSE) int64 {
	var tot [64][64]int64
	for _, e := range list {
		best := int64(1) << 62
		for g := range n {
			best = min(best, e.y[levY[g]]+e.uv[levUV[g]])
		}
		for j := range 64 {
			yj := e.y[j]
			row := &tot[j]
			for k, u := range &e.uv {
				row[k] += min(best, yj+u)
			}
		}
	}
	bestTot, bj, bk := int64(-1), 0, 0
	for j := range 64 {
		for k := range 64 {
			if bestTot < 0 || tot[j][k] < bestTot {
				bestTot, bj, bk = tot[j][k], j, k
			}
		}
	}
	levY[n], levUV[n] = bj, bk
	return bestTot
}

// cdefSearchSB notes the squared errors of the 64x64 at (sbr, sbc) of
// f.rec (the deblocked frame) for every strength pair, over the visible
// picture; blocks the filter leaves alone add nothing.
func (f *frameEnc) cdefSearchSB(sbr, sbc, damping int, e *cdefSSE, win []int32) {
	cs := f.bitDepth - 8
	ucs := uint(cs) //nolint:gosec // 0 or 2
	var secT [4]int32
	var secShiftY, secShiftUV [4]uint
	for si := 1; si < 4; si++ {
		t := cdefSecValues[si] << ucs
		secT[si] = int32(t) //nolint:gosec // small
		secShiftY[si] = constrainShift(t, damping+cs)
		secShiftUV[si] = constrainShift(t, damping+cs-1)
	}
	// chroma's primary strengths are the same for every block
	var priTUV [16]int32
	var priShiftUV [16]uint
	var priTapsUV [16][2]int32
	for pi := 1; pi < 16; pi++ {
		t := pi << ucs
		priTUV[pi] = int32(t) //nolint:gosec // small
		priShiftUV[pi] = constrainShift(t, damping+cs-1)
		priTapsUV[pi] = [2]int32{int32(cdefPriTaps[pi&1][0]), int32(cdefPriTaps[pi&1][1])}
	}
	for r := sbr * 16; r < min(sbr*16+16, f.miRows); r += 2 {
		for c := sbc * 16; c < min(sbc*16+16, f.miCols); c += 2 {
			if f.cdefSkipped(r, c) {
				continue
			}
			x0, y0 := c*4, r*4
			if x0 >= f.width || y0 >= f.height {
				continue
			}
			yDir, v := f.cdefDirection(f.rec[0], x0, y0)
			var priT [16]int32
			var priShift [16]uint
			var priTaps [16][2]int32
			for pi := 1; pi < 16; pi++ {
				t := cdefAdjust(pi<<ucs, v)
				priT[pi] = int32(t) //nolint:gosec // small
				if t > 0 {
					priShift[pi] = constrainShift(t, damping+cs)
				}
				tp := cdefPriTaps[(t>>ucs)&1]
				priTaps[pi] = [2]int32{int32(tp[0]), int32(tp[1])}
			}
			f.cdefWindow(0, f.rec[0], x0, y0, 8, 8, win)
			cdefSearchBlock(win, 8, min(8, f.width-x0), min(8, f.height-y0), f.src[0][y0*f.stride[0]+x0:], f.stride[0],
				yDir, &priT, &priShift, &priTaps, &secT, &secShiftY, &e.y)
			cx, cy := x0/2, y0/2
			cw, ch := (f.width+1)/2, (f.height+1)/2
			uvDir := int(cdefUvDir[1][1][yDir])
			for p := 1; p <= 2; p++ {
				f.cdefWindow(p, f.rec[p], cx, cy, 4, 4, win)
				cdefSearchBlock(win, 4, min(4, cw-cx), min(4, ch-cy), f.src[p][cy*f.stride[p]+cx:], f.stride[p],
					uvDir, &priTUV, &priShiftUV, &priTapsUV, &secT, &secShiftUV, &e.uv)
			}
		}
	}
}

// cdefSearchBlock adds the squared errors against src of the visible
// vw x vh of a w x w block, its window win, for every strength pair: the
// primary strengths (as adjusted) priT, their constrain shifts and taps,
// the secondary ones secT, by code.
func cdefSearchBlock(win []int32, w, vw, vh int, src []uint16, stride, dir int,
	priT *[16]int32, priShift *[16]uint, priTaps *[16][2]int32, secT *[4]int32, secShift *[4]uint, e *[64]int64) {
	ww := w + 4
	off := cdefOffsets(ww)
	d0, d1 := (dir+2)&7, (dir+6)&7
	var pd [2][2]int32 // the primary taps' differences, by distance and side
	var sd, s0 [2][4]int32
	var priSum [16]int32
	var secSum, sec0Sum [4]int32
	diff := func(v, x int32) int32 {
		if v == cdefVeryLarge {
			return 0
		}
		return v - x
	}
	for i := range vh {
		for j := range vw {
			c := (i+2)*ww + j + 2
			x := win[c]
			mn, mx := x, x
			for k := range 2 {
				for si, sign := range [2]int{1, -1} {
					p := win[c+sign*off[dir][k]]
					pd[k][si] = diff(p, x)
					s, t := win[c+sign*off[d0][k]], win[c+sign*off[d1][k]]
					sd[k][si], sd[k][2+si] = diff(s, x), diff(t, x)
					s0[k][si], s0[k][2+si] = diff(win[c+sign*off[2][k]], x), diff(win[c+sign*off[6][k]], x)
					for _, v := range [3]int32{p, s, t} {
						if v != cdefVeryLarge {
							mn, mx = min(mn, v), max(mx, v)
						}
					}
				}
			}
			for pi := 1; pi < 16; pi++ {
				t, sh, tp := priT[pi], priShift[pi], &priTaps[pi]
				priSum[pi] = tp[0]*(constrain(pd[0][0], t, sh)+constrain(pd[0][1], t, sh)) +
					tp[1]*(constrain(pd[1][0], t, sh)+constrain(pd[1][1], t, sh))
			}
			for si := 1; si < 4; si++ {
				t, sh := secT[si], secShift[si]
				var a, b, a0, b0 int32
				for q := range 4 {
					a += constrain(sd[0][q], t, sh)
					b += constrain(sd[1][q], t, sh)
					a0 += constrain(s0[0][q], t, sh)
					b0 += constrain(s0[1][q], t, sh)
				}
				secSum[si], sec0Sum[si] = 2*a+b, 2*a0+b0
			}
			sv := int32(src[i*stride+j])
			// the primary off: the direction 0, the result within the
			// taps' range without the clip
			for si := range 4 {
				sum := sec0Sum[si]
				d := int64(x+((8+sum-b2i32(sum < 0))>>4)) - int64(sv)
				e[si] += d * d
			}
			for pi := 1; pi < 16; pi++ {
				ps := priSum[pi]
				ee := e[pi*4 : pi*4+4]
				for si := range 4 {
					sum := ps + secSum[si]
					d := int64(min(max(x+((8+sum-b2i32(sum < 0))>>4), mn), mx)) - int64(sv)
					ee[si] += d * d
				}
			}
		}
	}
}

func b2i32(b bool) int32 {
	if b {
		return 1
	}
	return 0
}
