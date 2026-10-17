// ADM, the Additive Detail Measure (Li et al., "Image Quality Assessment
// by Separately Evaluating Detail Losses and Additive Impairments").
//
// Ported from libvmaf's integer_adm.c (Copyright 2016-2020 Netflix, Inc.,
// BSD+Patent, see LICENSE.libvmaf): the fixed-point extractor behind
// VMAF_integer_feature_adm2_score, its C (non-SIMD) code paths, with the
// default options.

package vmaf

import (
	"fmt"
	"math"
	"math/bits"
	"sync"
)

// adm2 is libvmaf's VMAF_integer_feature_adm2_score of a distorted
// picture against its reference (same size and depth), with libvmaf's
// default options.
//
// Both pictures are taken apart with a 4-level Daubechies wavelet. At
// each level the distorted picture's detail bands are split into the
// part that is the reference's detail, attenuated (detail loss), and
// what is left (additive impairment). The detail that survived is
// weighted by the eye's contrast sensitivity, masked by the impairments
// around it, and pooled as a cubic mean; the reference's own weighted
// detail is pooled the same way. The score is the first sum over the
// second: 1 when nothing was lost, less as detail goes.
//
// The arithmetic is libvmaf's to the bit: the same integer widths,
// shifts, roundings and wraparounds, and the same float and double
// steps in the same order. Pictures must be at least 33x33.
func adm2(ref, dis Frame) float64 {
	w, h := ref.W, ref.H
	if w < 33 || h < 33 {
		panic(fmt.Sprintf("vmaf: %dx%d picture too small for ADM", w, h))
	}
	if dis.W != w || dis.H != h || dis.BPC != ref.BPC {
		panic("vmaf: ADM pictures differ in size or depth")
	}

	ws := admPool.Get().(*admWork)
	defer admPool.Put(ws)
	ws.alloc(w, h)

	var num, den float64
	stride := ws.stride

	// Scale 0: the 16-bit pipeline, straight from the pictures.
	iy, ix := ws.indices(w, h)
	admDWT0(ref.Y, w, h, ref.BPC, &ws.ref0, stride, ws.tmp16, iy, ix)
	admDWT0(dis.Y, w, h, dis.BPC, &ws.dis0, stride, ws.tmp16, iy, ix)
	w, h = (w+1)/2, (h+1)/2
	admDecouple0(ws, w, h)
	den0 := admDen0(&ws.ref0, w, h, stride)
	admCSF0(ws, w, h)
	num0 := admMask0(ws, w, h)
	num += float64(num0)
	den += float64(den0)

	// The approximation band feeds the next scale, widened to 32 bits.
	for i := 0; i < h; i++ {
		for j := 0; j < w; j++ {
			ws.refIn[i*stride+j] = int32(ws.ref0.a[i*stride+j])
			ws.disIn[i*stride+j] = int32(ws.dis0.a[i*stride+j])
		}
	}

	// Scales 1 to 3: the 32-bit pipeline.
	for scale := 1; scale < 4; scale++ {
		iy, ix := ws.indices(w, h)
		admDWT(ws, w, h, scale, iy, ix)
		w, h = (w+1)/2, (h+1)/2
		admDecouple(ws, w, h)
		d := admDen(&ws.ref, scale, w, h, stride)
		admCSFScale(ws, scale, w, h)
		n := admMask(ws, scale, w, h)
		num += float64(n)
		den += float64(d)
		ws.refIn, ws.ref.a = ws.ref.a, ws.refIn
		ws.disIn, ws.dis.a = ws.dis.a, ws.disIn
	}

	// Sums too small to mean anything count as zero, and a reference
	// with no detail scores 1.
	limit := 1e-10 * float64(ref.W*ref.H) / (1920.0 * 1080.0)
	if num < limit {
		num = 0
	}
	if den < limit {
		den = 0
	}
	if den == 0 {
		return 1
	}
	return num / den
}

// The wavelet: db2, in Q15 fixed point.
var (
	admLo = [4]int32{15826, 27411, 7345, -4240}
	admHi = [4]int32{-4240, -7345, 27411, -15826}
)

// admLoSum is the sum of admLo: subtracting it times half the sample
// range centres the low pass output on zero.
const admLoSum = 46342

// admBands16 and admBands32 are the four bands of one wavelet level:
// a (low pass both ways), v (low vertically, high horizontally), h (high
// vertically, low horizontally) and d (high both ways).
type admBands16 struct{ a, v, h, d []int16 }
type admBands32 struct{ a, v, h, d []int32 }

// admWork is one call's working memory, pooled across calls.
type admWork struct {
	w, h, stride int

	ref0, dis0 admBands16
	r0, a0     [3][]int16 // decoupled h, v, d: restored detail, additive
	csfA0      [3][]int16 // CSF-weighted additive impairment
	csfF0      [3][]int16 // the same, divided by 30: masking contributions
	tmp16      []int16

	refIn, disIn []int32 // the previous scale's approximation band
	ref, dis     admBands32
	r, a         [3][]int32
	csfA, csfF   [3][]int32
	tmp          []int32

	iy, ix [4][]int
}

var admPool = sync.Pool{New: func() any { return new(admWork) }}

func (ws *admWork) alloc(w, h int) {
	if ws.w == w && ws.h == h {
		return
	}
	*ws = admWork{w: w, h: h, stride: (w + 1) / 2}
	n := ws.stride * ((h + 1) / 2)
	i16 := func() []int16 { return make([]int16, n) }
	i32 := func() []int32 { return make([]int32, n) }
	ws.ref0 = admBands16{i16(), i16(), i16(), i16()}
	ws.dis0 = admBands16{i16(), i16(), i16(), i16()}
	ws.ref = admBands32{i32(), i32(), i32(), i32()}
	ws.dis = admBands32{i32(), i32(), i32(), i32()}
	ws.refIn, ws.disIn = i32(), i32()
	for t := 0; t < 3; t++ {
		ws.r0[t], ws.a0[t], ws.csfA0[t], ws.csfF0[t] = i16(), i16(), i16(), i16()
		ws.r[t], ws.a[t], ws.csfA[t], ws.csfF[t] = i32(), i32(), i32(), i32()
	}
	ws.tmp16 = make([]int16, 2*w)
	ws.tmp = make([]int32, 4*w)
	for k := range ws.iy {
		ws.iy[k] = make([]int, (h+1)/2)
		ws.ix[k] = make([]int, (w+1)/2)
	}
}

// indices returns, for each output row (column) of a w x h wavelet
// step, the four input rows (columns) its filter taps read: 2i-1 to
// 2i+2, mirrored about the first sample at the top and about the edge
// at the bottom (h, h+1 read h-1, h-2).
func (ws *admWork) indices(w, h int) (iy, ix *[4][]int) {
	fill := func(ind *[4][]int, n int) {
		for i := 0; i < (n+1)/2; i++ {
			for k := 0; k < 4; k++ {
				v := 2*i - 1 + k
				if v < 0 {
					v = -v
				}
				if v >= n {
					v = 2*n - v - 1
				}
				ind[k][i] = v
			}
		}
	}
	fill(&ws.iy, h)
	fill(&ws.ix, w)
	return &ws.iy, &ws.ix
}

// admDWT0 is the first wavelet level, from the picture's samples to
// 16-bit bands: a vertical pass to Q(16-bpc) after centring the samples
// on zero, then a horizontal one, each rounding off its shift. libvmaf
// has an 8-bit and a 16-bit version of this; with bpc 8 they are the
// same arithmetic.
func admDWT0(src []uint16, w, h, bpc int, dst *admBands16, stride int, tmp []int16, iy, ix *[4][]int) {
	shiftV := uint(bpc)
	addV := int32(1) << (bpc - 1)
	centre := int32(admLoSum) * addV
	lo, hi := tmp[:w], tmp[w:2*w]
	ow := (w + 1) / 2
	for i := 0; i < (h+1)/2; i++ {
		r0 := src[iy[0][i]*w:][:w]
		r1 := src[iy[1][i]*w:][:w]
		r2 := src[iy[2][i]*w:][:w]
		r3 := src[iy[3][i]*w:][:w]
		hi := hi[:len(lo)]
		for j := range lo {
			s0, s1, s2, s3 := int32(r0[j]), int32(r1[j]), int32(r2[j]), int32(r3[j])
			l := admLo[0]*s0 + admLo[1]*s1 + admLo[2]*s2 + admLo[3]*s3 - centre
			lo[j] = int16((l + addV) >> shiftV)
			g := admHi[0]*s0 + admHi[1]*s1 + admHi[2]*s2 + admHi[3]*s3
			hi[j] = int16((g + addV) >> shiftV)
		}
		row := i * stride
		da, dv := dst.a[row:][:ow], dst.v[row:][:ow]
		dh, dd := dst.h[row:][:ow], dst.d[row:][:ow]
		for j := range da {
			j0, j1, j2, j3 := ix[0][j], ix[1][j], ix[2][j], ix[3][j]
			s0, s1, s2, s3 := int32(lo[j0]), int32(lo[j1]), int32(lo[j2]), int32(lo[j3])
			da[j] = int16((admLo[0]*s0 + admLo[1]*s1 + admLo[2]*s2 + admLo[3]*s3 + 32768) >> 16)
			dv[j] = int16((admHi[0]*s0 + admHi[1]*s1 + admHi[2]*s2 + admHi[3]*s3 + 32768) >> 16)
			s0, s1, s2, s3 = int32(hi[j0]), int32(hi[j1]), int32(hi[j2]), int32(hi[j3])
			dh[j] = int16((admLo[0]*s0 + admLo[1]*s1 + admLo[2]*s2 + admLo[3]*s3 + 32768) >> 16)
			dd[j] = int16((admHi[0]*s0 + admHi[1]*s1 + admHi[2]*s2 + admHi[3]*s3 + 32768) >> 16)
		}
	}
}

// Rounding and shifts of the wavelet's vertical and horizontal passes at
// scales 1, 2 and 3.
var (
	admDWTAddV   = [3]int64{0, 32768, 32768}
	admDWTShiftV = [3]uint{0, 16, 16}
	admDWTAddH   = [3]int64{16384, 32768, 16384}
	admDWTShiftH = [3]uint{15, 16, 15}
)

// admDWT is a wavelet level at scales 1 to 3, in 32 bits with 64-bit
// accumulators, on the reference's and the distorted picture's previous
// approximation bands.
func admDWT(ws *admWork, w, h, scale int, iy, ix *[4][]int) {
	stride := ws.stride
	addV, shiftV := admDWTAddV[scale-1], admDWTShiftV[scale-1]
	addH, shiftH := admDWTAddH[scale-1], admDWTShiftH[scale-1]
	tmp := ws.tmp[:4*w]
	for _, p := range [2]struct {
		src []int32
		dst *admBands32
	}{{ws.refIn, &ws.ref}, {ws.disIn, &ws.dis}} {
		lo, hi := tmp[:w], tmp[w:2*w]
		for i := 0; i < (h+1)/2; i++ {
			r0, r1, r2, r3 := p.src[iy[0][i]*stride:], p.src[iy[1][i]*stride:], p.src[iy[2][i]*stride:], p.src[iy[3][i]*stride:]
			for j := 0; j < w; j++ {
				s0, s1, s2, s3 := int64(r0[j]), int64(r1[j]), int64(r2[j]), int64(r3[j])
				l := int64(admLo[0])*s0 + int64(admLo[1])*s1 + int64(admLo[2])*s2 + int64(admLo[3])*s3
				lo[j] = int32((l + addV) >> shiftV)
				g := int64(admHi[0])*s0 + int64(admHi[1])*s1 + int64(admHi[2])*s2 + int64(admHi[3])*s3
				hi[j] = int32((g + addV) >> shiftV)
			}
			row := i * stride
			for j := 0; j < (w+1)/2; j++ {
				j0, j1, j2, j3 := ix[0][j], ix[1][j], ix[2][j], ix[3][j]
				s0, s1, s2, s3 := int64(lo[j0]), int64(lo[j1]), int64(lo[j2]), int64(lo[j3])
				p.dst.a[row+j] = int32((int64(admLo[0])*s0 + int64(admLo[1])*s1 + int64(admLo[2])*s2 + int64(admLo[3])*s3 + addH) >> shiftH)
				p.dst.v[row+j] = int32((int64(admHi[0])*s0 + int64(admHi[1])*s1 + int64(admHi[2])*s2 + int64(admHi[3])*s3 + addH) >> shiftH)
				s0, s1, s2, s3 = int64(hi[j0]), int64(hi[j1]), int64(hi[j2]), int64(hi[j3])
				p.dst.h[row+j] = int32((int64(admLo[0])*s0 + int64(admLo[1])*s1 + int64(admLo[2])*s2 + int64(admLo[3])*s3 + addH) >> shiftH)
				p.dst.d[row+j] = int32((int64(admHi[0])*s0 + int64(admHi[1])*s1 + int64(admHi[2])*s2 + int64(admHi[3])*s3 + addH) >> shiftH)
			}
		}
	}
}

// admRegion is the part of a w x h band that the measure covers: 10% of
// it is dropped on each side. The decoupling and CSF stages work one
// sample further out on each side (plus one more at the far edges) for
// the 3x3 masking neighbourhood; libvmaf computes them in double and
// truncates.
func admRegion(w, h int, margin bool) (left, top, right, bottom int) {
	if !margin {
		left = int(float64(w)*0.1 - 0.5)
		top = int(float64(h)*0.1 - 0.5)
		return left, top, w - left, h - top
	}
	left = int(float64(w)*0.1 - 0.5 - 1)
	top = int(float64(h)*0.1 - 0.5 - 1)
	right, bottom = w-left+2, h-top+2
	left, top = max(left, 0), max(top, 0)
	right, bottom = min(right, w), min(bottom, h)
	return left, top, right, bottom
}

// admAngle reports whether the reference's (oh, ov) and the distorted
// picture's (th, tv) detail vectors point within one degree of each
// other, tested on float conversions as libvmaf does: a distortion
// that only scales the detail up there is enhancement, not loss.
func admAngle(oh, ov, th, tv int64) bool {
	dot := float64(float32(oh*th+ov*tv)) / 4096.0
	if dot < 0 {
		return false
	}
	o := float64(float32(oh*oh+ov*ov)) / 4096.0
	t := float64(float32(th*th+tv*tv)) / 4096.0
	return dot*dot >= float64(admCos1DegSq)*o*t
}

// admEnhGain is the default adm_enhn_gain_limit: how much the restored
// detail may be scaled back up where the distortion enhanced it.
const admEnhGain = 100.0

// admDecouple0 splits the distorted picture's scale 0 detail into the
// reference's detail scaled by k = dis/ref clamped to [0, 1] (Q15, the
// division done by a reciprocal table) and the remainder. Where the two
// point the same way the scaled detail may take the distorted value up
// to admEnhGain times it.
func admDecouple0(ws *admWork, w, h int) {
	left, top, right, bottom := admRegion(w, h, true)
	ref, dis := &ws.ref0, &ws.dis0
	refB := [3][]int16{ref.h, ref.v, ref.d}
	disB := [3][]int16{dis.h, dis.v, dis.d}
	for i := top; i < bottom; i++ {
		for j := left; j < right; j++ {
			p := i*ws.stride + j
			angle := admAngle(int64(ref.h[p]), int64(ref.v[p]), int64(dis.h[p]), int64(dis.v[p]))
			for b := 0; b < 3; b++ {
				o, t := refB[b][p], disB[b][p]
				k := int32(32768)
				if o != 0 {
					k = int32((int64(admDivLookup[int(o)+32768])*int64(t) + 16384) >> 15)
					k = min(max(k, 0), 32768)
				}
				rst := int16((k*int32(o) + 16384) >> 15)
				if angle && k > 0 {
					g := float64(rst) * admEnhGain
					if o > 0 && g < float64(t) || o < 0 && g > float64(t) {
						rst = int16(g)
					} else if o != 0 {
						rst = t
					}
				}
				ws.r0[b][p] = rst
				ws.a0[b][p] = t - rst
			}
		}
	}
}

// admDecouple is admDecouple0 for scales 1 to 3, on 32-bit coefficients:
// a reference coefficient of 15 bits or more is first rounded to 15
// significant bits so the reciprocal table still serves, and the
// quotient shifted back.
func admDecouple(ws *admWork, w, h int) {
	left, top, right, bottom := admRegion(w, h, true)
	ref, dis := &ws.ref, &ws.dis
	refB := [3][]int32{ref.h, ref.v, ref.d}
	disB := [3][]int32{dis.h, dis.v, dis.d}
	for i := top; i < bottom; i++ {
		for j := left; j < right; j++ {
			p := i*ws.stride + j
			angle := admAngle(int64(ref.h[p]), int64(ref.v[p]), int64(dis.h[p]), int64(dis.v[p]))
			for b := 0; b < 3; b++ {
				o, t := refB[b][p], disB[b][p]
				k := int64(32768)
				if o != 0 {
					mag := uint32(abs32(o))
					shift := uint(0)
					if mag >= 32768 {
						shift = uint(17 - bits.LeadingZeros32(mag))
						mag = (mag + 1<<(shift-1)) >> shift
					}
					sign := int64(1)
					if o < 0 {
						sign = -1
					}
					// libvmaf rounds with an int 1<<(14+shift), which
					// wraps negative at shift 17.
					round := int64(int32(1) << (14 + shift))
					k = (int64(admDivLookup[mag+32768])*int64(t)*sign + round) >> (15 + shift)
					k = min(max(k, 0), 32768)
				}
				rst := int32((k*int64(o) + 16384) >> 15)
				if angle && k > 0 {
					g := float64(rst) * admEnhGain
					if o > 0 && g < float64(t) || o < 0 && g > float64(t) {
						rst = int32(g)
					} else if o != 0 {
						rst = t
					}
				}
				ws.r[b][p] = rst
				ws.a[b][p] = t - rst
			}
		}
	}
}

func abs32(x int32) int32 {
	if x < 0 {
		return -x
	}
	return x
}

// admThird is 1.0f/3.0f: pooling is a cubic mean.
const admThird = float32(1.0 / 3.0)

// admNoiseWeight is the default adm_noise_weight: a noise floor of this
// much energy per sample is added to every pooled band.
const admNoiseWeight = 0.03125

// admNoiseFloor is the cube root of the noise floor of a region of the
// given area, as libvmaf rounds it.
func admNoiseFloor(area int) float32 {
	return powf(float32(float64(area)*admNoiseWeight), admThird)
}

// admDen0 is the scale 0 denominator: the reference's detail in the h,
// v and d bands, cubed and summed over the region (in Q-shifted integer
// arithmetic, rows rounded down by a shift chosen from the area so the
// 64-bit sums cannot overflow), then weighted by the CSF cubed, cube
// rooted and lifted by the noise floor.
func admDen0(ref *admBands16, w, h, stride int) float32 {
	left, top, right, bottom := admRegion(w, h, false)
	shift := int32(math.Ceil(math.Log2(float64((bottom-top)*(right-left))) - 20))
	shift = max(shift, 0)
	var add uint64
	if shift > 0 {
		add = 1 << (shift - 1)
	}
	var sum [3]uint64
	for b, band := range [3][]int16{ref.h, ref.v, ref.d} {
		for i := top; i < bottom; i++ {
			var row uint64
			for _, c := range band[i*stride+left : i*stride+right] {
				m := uint64(uint16(abs32(int32(c))))
				row += m * m * m
			}
			sum[b] += (row + add) >> shift
		}
	}
	scale := math.Pow(2, float64(18-shift))
	csf := admCSFs[0]
	floor := admNoiseFloor((bottom - top) * (right - left))
	dh := powf(float32(float64(sum[0])/scale*csf.cubeHV), admThird) + floor
	dv := powf(float32(float64(sum[1])/scale*csf.cubeHV), admThird) + floor
	dd := powf(float32(float64(sum[2])/scale*csf.cubeD), admThird) + floor
	return dh + dv + dd
}

// Per scale 1-3 rounding of the denominator's squares, and the Q format
// its sums are in.
var (
	admDenShiftSq = [3]uint{31, 30, 31}
	admDenQ       = [3]uint32{32, 27, 23}
)

// admDen is admDen0 at scales 1 to 3: each cube is formed as
// ((x*x) >> s) * x >> log2(width), each row sum shifted by log2(height).
// (libvmaf's square rounding adds 2^s, not 2^(s-1).)
func admDen(ref *admBands32, scale, w, h, stride int) float32 {
	left, top, right, bottom := admRegion(w, h, false)
	shiftSq := admDenShiftSq[scale-1]
	addSq := uint64(1) << shiftSq
	shiftCub := uint32(math.Ceil(math.Log2(float64(right - left))))
	addCub := admRoundBit(shiftCub)
	shiftAcc := uint32(math.Ceil(math.Log2(float64(bottom - top))))
	addAcc := admRoundBit(shiftAcc)
	var sum [3]uint64
	for b, band := range [3][]int32{ref.h, ref.v, ref.d} {
		for i := top; i < bottom; i++ {
			var row uint64
			for _, c := range band[i*stride+left : i*stride+right] {
				m := uint64(uint32(abs32(c)))
				row += ((((m*m)+addSq)>>shiftSq)*m + addCub) >> shiftCub
			}
			sum[b] += (row + addAcc) >> shiftAcc
		}
	}
	q := math.Pow(2, float64(admDenQ[scale-1]-shiftAcc-shiftCub))
	csf := admCSFs[scale]
	floor := admNoiseFloor((bottom - top) * (right - left))
	dh := powf(float32(float64(sum[0])/q*csf.cubeHV), admThird) + floor
	dv := powf(float32(float64(sum[1])/q*csf.cubeHV), admThird) + floor
	dd := powf(float32(float64(sum[2])/q*csf.cubeD), admThird) + floor
	return dh + dv + dd
}

// admRoundBit is libvmaf's (uint32_t)pow(2, shift - 1) for a rounding
// shift: half the shift's unit, and 0 for no shift (where libvmaf's
// pow(2, 2^32-1) overflows to a value x86 converts to 0).
func admRoundBit(shift uint32) uint64 {
	if shift == 0 {
		return 0
	}
	return 1 << (shift - 1)
}

// Scale 0 CSF fixed point: the weighted values are brought back to the
// bands' Q format by these shifts, with these roundings (65535 for d,
// as libvmaf has it).
var (
	admCSF0Shift = [3]uint{15, 15, 17}
	admCSF0Add   = [3]int32{16384, 16384, 65535}
)

// admCSF0 weights the scale 0 additive impairment by the contrast
// sensitivity of each band (csfA0), and keeps a thirtieth of its
// magnitude (csfF0): what a sample adds to its neighbours' masking
// threshold. Values that leave 16 bits wrap as in libvmaf.
func admCSF0(ws *admWork, w, h int) {
	left, top, right, bottom := admRegion(w, h, true)
	for b := 0; b < 3; b++ {
		f, add, shift := admCSF0Fix[b], admCSF0Add[b], admCSF0Shift[b]
		src, dst, flt := ws.a0[b], ws.csfA0[b], ws.csfF0[b]
		for i := top; i < bottom; i++ {
			for j := left; j < right; j++ {
				p := i*ws.stride + j
				v := int16((f*int32(src[p]) + add) >> shift)
				dst[p] = v
				flt[p] = int16((4369*abs32(int32(v)) + 2048) >> 12)
			}
		}
	}
}

// admCSFScale is admCSF0 at scales 1 to 3, with 32-bit weights. The
// thirtieth's rounding adds the int32 1<<31, that is subtracts 2^31.
func admCSFScale(ws *admWork, scale, w, h int) {
	left, top, right, bottom := admRegion(w, h, true)
	fix := [3]uint32{admCSFs[scale].fixHV, admCSFs[scale].fixHV, admCSFs[scale].fixD}
	for b := 0; b < 3; b++ {
		f := int64(fix[b])
		src, dst, flt := ws.a[b], ws.csfA[b], ws.csfF[b]
		for i := top; i < bottom; i++ {
			for j := left; j < right; j++ {
				p := i*ws.stride + j
				v := int32((f*int64(src[p]) + 1<<27) >> 28)
				dst[p] = v
				flt[p] = int32((143165577*int64(abs32(v)) - 1<<31) >> 32)
			}
		}
	}
}

// admNeighbours returns, for each position of a band n samples long,
// the positions before and after it, mirrored at the ends as libvmaf's
// masking does (-1 reads 1, n reads n-1).
func admNeighbours(n int) (prev, next []int) {
	prev, next = make([]int, n), make([]int, n)
	for i := 0; i < n; i++ {
		prev[i], next[i] = i-1, i+1
	}
	prev[0], next[n-1] = 1, n-1
	return prev, next
}

// admMask0 is the scale 0 numerator: the restored detail, weighted by
// the CSF, minus the masking threshold around it (a thirtieth of the
// weighted impairment of the 3x3 neighbourhood in all three bands, the
// centre counting 1/15 instead), clipped at zero, cubed and pooled as
// admDen0 does, with the noise floor.
func admMask0(ws *admWork, w, h int) float32 {
	left, top, right, bottom := admRegion(w, h, false)
	stride := ws.stride
	shiftCubHV := uint32(math.Ceil(math.Log2(float64(w)) - 4))
	shiftCubD := uint32(math.Ceil(math.Log2(float64(w)) - 3))
	addCubHV, addCubD := int64(admRoundBit(shiftCubHV)), int64(admRoundBit(shiftCubD))
	shiftAcc := uint32(math.Ceil(math.Log2(float64(h))))
	addAcc := int64(admRoundBit(shiftAcc))
	prevR, nextR := admNeighbours(h)
	prevC, nextC := admNeighbours(w)

	// cube is x (Q21/Q23 after weighting) less the threshold, clipped,
	// cubed with the given roundings.
	cube := func(x, thr int32, shiftSub uint, addSq int64, shiftSq uint, addCub int64, shiftCub uint32) int64 {
		x = abs32(x) - thr<<shiftSub
		x = max(x, 0)
		sq := int32((int64(x)*int64(x) + addSq) >> shiftSq)
		return (int64(sq)*int64(x) + addCub) >> shiftCub
	}

	var accH, accV, accD int64
	for i := top; i < bottom; i++ {
		up, mid, down := prevR[i]*stride, i*stride, nextR[i]*stride
		var rowH, rowV, rowD int64
		for j := left; j < right; j++ {
			l, r := prevC[j], nextC[j]
			var thr int32
			for b := 0; b < 3; b++ {
				f := ws.csfF0[b]
				c := ws.csfA0[b][mid+j]
				thr += int32(f[up+l]) + int32(f[up+j]) + int32(f[up+r]) +
					int32(f[mid+l]) + int32(int16((8738*abs32(int32(c))+2048)>>12)) + int32(f[mid+r]) +
					int32(f[down+l]) + int32(f[down+j]) + int32(f[down+r])
			}
			p := mid + j
			rowH += cube(int32(ws.r0[0][p])*admCSF0Fix[0], thr, 10, 1<<28, 29, addCubHV, shiftCubHV)
			rowV += cube(int32(ws.r0[1][p])*admCSF0Fix[1], thr, 10, 1<<28, 29, addCubHV, shiftCubHV)
			rowD += cube(int32(ws.r0[2][p])*admCSF0Fix[2], thr, 12, 1<<29, 30, addCubD, shiftCubD)
		}
		accH += (rowH + addAcc) >> shiftAcc
		accV += (rowV + addAcc) >> shiftAcc
		accD += (rowD + addAcc) >> shiftAcc
	}

	// The sums are Q81 (h, v) and Q87 (d) less the shifts taken.
	fh := float32(float64(accH) / math.Pow(2, float64(52-shiftCubHV-shiftAcc)))
	fv := float32(float64(accV) / math.Pow(2, float64(52-shiftCubHV-shiftAcc)))
	fd := float32(float64(accD) / math.Pow(2, float64(57-shiftCubD-shiftAcc)))
	floor := admNoiseFloor((bottom - top) * (right - left))
	return (powf(fh, admThird) + floor) + (powf(fv, admThird) + floor) + (powf(fd, admThird) + floor)
}

// admMaskQ is the Q format of admMask's sums at scales 1, 2, 3.
var admMaskQ = [3]uint32{45, 39, 36}

// admMask is admMask0 at scales 1 to 3, in 32 bits: the restored detail
// is CSF-weighted here (Q28 rounding), the threshold's centre term is a
// fifteenth (whose rounding subtracts 2^31, as in admCSFScale).
func admMask(ws *admWork, scale, w, h int) float32 {
	left, top, right, bottom := admRegion(w, h, false)
	stride := ws.stride
	csf := admCSFs[scale]
	fix := [3]int64{int64(csf.fixHV), int64(csf.fixHV), int64(csf.fixD)}
	shiftCub := uint32(math.Ceil(math.Log2(float64(w))))
	addCub := int64(admRoundBit(shiftCub))
	shiftAcc := uint32(math.Ceil(math.Log2(float64(h))))
	addAcc := int64(admRoundBit(shiftAcc))
	prevR, nextR := admNeighbours(h)
	prevC, nextC := admNeighbours(w)

	var acc [3]int64
	for i := top; i < bottom; i++ {
		up, mid, down := prevR[i]*stride, i*stride, nextR[i]*stride
		var row [3]int64
		for j := left; j < right; j++ {
			l, r := prevC[j], nextC[j]
			var thr int32
			for b := 0; b < 3; b++ {
				f := ws.csfF[b]
				c := ws.csfA[b][mid+j]
				thr += f[up+l] + f[up+j] + f[up+r] +
					f[mid+l] + int32((286331153*int64(abs32(c))-1<<31)>>32) + f[mid+r] +
					f[down+l] + f[down+j] + f[down+r]
			}
			for b := 0; b < 3; b++ {
				x := int32((int64(ws.r[b][mid+j])*fix[b] + 1<<27) >> 28)
				x = max(abs32(x)-thr, 0)
				sq := int32((int64(x)*int64(x) + 1<<29) >> 30)
				row[b] += (int64(sq)*int64(x) + addCub) >> shiftCub
			}
		}
		for b := 0; b < 3; b++ {
			acc[b] += (row[b] + addAcc) >> shiftAcc
		}
	}

	q := float32(math.Pow(2, float64(admMaskQ[scale-1]-shiftCub-shiftAcc)))
	floor := admNoiseFloor((bottom - top) * (right - left))
	var sum float32
	for b := 0; b < 3; b++ {
		sum += powf(float32(acc[b])/q, admThird) + floor
	}
	return sum
}
