package dovi

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync"
)

// Composition: a profile 7 full enhancement layer (FEL) made into the
// picture it is meant to give.
//
// Each component of the base layer (BL) goes through the RPU's mapping —
// a piecewise polynomial of the component itself, or a multivariate
// multiple regression (MMR) of all three — and gets the enhancement
// layer's residual, dequantised by the RPU's non-linear quantisation (a
// linear dead zone). The enhancement layer is half the base layer's size
// and is upsampled to it first. The result is the Dolby Vision signal: on
// a UHD Blu-ray, BT.2020 PQ Y'CbCr coded like the HDR10 base layer, which
// is why, with the identity mapping profile 8.1 gives it (see
// ToProfile81), it is an HDR10 picture with its RPU.
//
// The arithmetic follows libplacebo's reshaping and FEL composition
// (shaders/colorspace.c) and vs-nlq's dequantisation: signals normalised
// by 2^depth-1, pivots accumulated, coefficients over 2^coefficient_log2_
// denom, the residual sign(t)·((|t|−½)·S + T) clamped to the RPU's
// vdr_in_max. Where no reference says, choices are made here:
//
//   - The enhancement layer is upsampled as a picture of whole codes, with
//     a Catmull-Rom filter, co-sited with the base layer horizontally and
//     centred vertically (libplacebo's "left" layer siting, consistent on
//     the discs it was checked against). Its chroma planes take the same
//     siting relative to the base layer's.
//   - MMR, which predicts chroma from all three components, takes luma at
//     each 4:2:0 chroma sample's position: [1 2 1] across, the two rows
//     it sits between averaged.
//   - The result is written at 10 bits, as the encoders take it: the
//     signal's 12 bits rounded.
//
// Rows are spread over the processors; on amd64 the row kernels are AVX2
// (kernels_amd64.s), checked against the Go ones.

// Picture is a 4:2:0 picture of 10-bit samples in P010's layout: a luma
// plane, then interleaved Cb/Cr at half resolution, each sample two
// bytes, little-endian, the value in the top 10 bits.
type Picture struct {
	Width, Height int
	Y, UV         []byte
	Pitch         int
}

// Composer composes pictures with one RPU's mapping and quantisation.
type Composer struct {
	luma [1024]float32 // the luma mapping, by code
	// Chroma: a lookup like luma's when the component's mapping is
	// polynomial throughout, else per-sample MMR.
	chroma [2]chromaMap
	// residual is each component's dequantised residual by upsampled
	// enhancement layer code (zero without a residual).
	residual [3][1024]float32
	nlq      bool
	// mmr, when both chroma components are one MMR piece each, is their
	// prediction for a row at a time.
	mmr *mmrRow
}

// mmrRow is two components' single-piece MMR: per component a constant
// and 21 coefficients (the seven terms, then their squares, then their
// cubes; zero beyond the piece's order), and the clamp.
type mmrRow struct {
	constant [2]float32
	coef     [2][21]float32
	lo, hi   [2]float32
	order    int
	table    *mmrTable // the kernel's layout, made on first use
}

type chromaMap struct {
	lut    *[1024]float32
	pieces []mmrPiece
	pivots []float32 // inner pivots, normalised, choosing the piece
	lo, hi float32
}

type mmrPiece struct {
	poly     bool
	p        [3]float32 // polynomial c0, c1, c2
	order    int
	constant float32
	coef     [3][7]float32
}

// NewComposer prepares the composition one RPU describes. It needs a 10-bit
// base and enhancement layer, as a UHD Blu-ray has.
func NewComposer(u *RPU) (*Composer, error) {
	m := u.Mapping
	if m == nil {
		return nil, errors.New("dovi: the RPU has no mapping of its own")
	}
	if u.blDepth() != 10 || u.ELBitDepthMinus8&0xff != 2 {
		return nil, fmt.Errorf("dovi: composing a %d-bit base and %d-bit enhancement layer is not supported",
			u.blDepth(), u.ELBitDepthMinus8&0xff+8)
	}
	if u.CoefDataType != 0 {
		return nil, errors.New("dovi: floating point RPU coefficients are not supported")
	}
	c := &Composer{}
	denom := math.Ldexp(1, -int(u.CoefLog2Denom)) //nolint:gosec // small
	coef := func(i int64, f uint64) float64 { return float64(i) + float64(f)*denom }
	curves := [3]struct {
		pivots []float64
		pieces []mmrPiece
	}{}
	for ci, cv := range m.Curves {
		acc := uint64(0)
		for _, p := range cv.Pivots {
			acc += p
			curves[ci].pivots = append(curves[ci].pivots, float64(acc)/1023)
		}
		for _, p := range cv.Pieces {
			var mp mmrPiece
			switch p.MappingIdc {
			case 0:
				mp.poly = true
				for k := range p.PolyCoef {
					mp.p[k] = float32(coef(p.PolyCoefInt[k], p.PolyCoef[k]))
				}
			case 1:
				mp.order = p.MMROrderMinus1 + 1
				mp.constant = float32(coef(p.MMRConstInt, p.MMRConst))
				for j := range p.MMRCoef {
					for k := range 7 {
						mp.coef[j][k] = float32(coef(p.MMRCoefInt[j][k], p.MMRCoef[j][k]))
					}
				}
			}
			curves[ci].pieces = append(curves[ci].pieces, mp)
		}
	}
	// A component whose pieces are all polynomial is a function of itself:
	// a lookup by code.
	lut := func(ci int) *[1024]float32 {
		cv := curves[ci]
		for _, p := range cv.pieces {
			if !p.poly {
				return nil
			}
		}
		var t [1024]float32
		lo, hi := cv.pivots[0], cv.pivots[len(cv.pivots)-1]
		for code := range t {
			x := float64(code) / 1023
			i := 0
			for i < len(cv.pieces)-1 && x >= cv.pivots[i+1] {
				i++
			}
			p := cv.pieces[i].p
			s := (float64(p[2])*x+float64(p[1]))*x + float64(p[0])
			t[code] = float32(min(max(s, lo), hi))
		}
		return &t
	}
	y := lut(0)
	if y == nil {
		return nil, errors.New("dovi: an MMR luma mapping is not supported")
	}
	c.luma = *y
	for k := range 2 {
		cv := curves[k+1]
		cm := chromaMap{lut: lut(k + 1), pieces: cv.pieces,
			lo: float32(cv.pivots[0]), hi: float32(cv.pivots[len(cv.pivots)-1])}
		for _, p := range cv.pivots[1 : len(cv.pivots)-1] {
			cm.pivots = append(cm.pivots, float32(p))
		}
		c.chroma[k] = cm
	}
	if a, b := c.chroma[0], c.chroma[1]; len(a.pieces) == 1 && len(b.pieces) == 1 && !a.pieces[0].poly && !b.pieces[0].poly {
		k := &mmrRow{order: max(a.pieces[0].order, b.pieces[0].order)}
		for i, cm := range []chromaMap{a, b} {
			p := cm.pieces[0]
			k.constant[i], k.lo[i], k.hi[i] = p.constant, cm.lo, cm.hi
			for j := range p.order {
				copy(k.coef[i][7*j:], p.coef[j][:])
			}
		}
		c.mmr = k
	}
	if q := m.NLQ; q != nil && u.residual() && !q.MEL() {
		c.nlq = true
		for ci := range 3 {
			slope := coef(int64(q.SlopeInt[ci]), q.Slope[ci])     //nolint:gosec // small
			thresh := coef(int64(q.ThreshInt[ci]), q.Thresh[ci])  //nolint:gosec // small
			inMax := coef(int64(q.InMaxInt[ci]), q.InMax[ci])     //nolint:gosec // small
			for code := range c.residual[ci] {
				t := float64(code) - float64(q.Offset[ci])
				if t == 0 {
					continue
				}
				r := (math.Abs(t)-0.5)*slope + thresh
				r = min(r, inMax)
				if t < 0 {
					r = -r
				}
				c.residual[ci][code] = float32(r)
			}
		}
	}
	return c, nil
}

// HasResidual reports whether the enhancement layer changes the picture:
// false for a minimal one, whose composition is the mapping alone.
func (c *Composer) HasResidual() bool { return c.nlq }

// Compose writes the composition of bl and el (half bl's size, or nil for
// the mapping alone) into dst, the size of bl. Rows are shared out over
// the machine's processors.
func (c *Composer) Compose(dst, bl, el *Picture) error {
	if bl.Width&1 != 0 || bl.Height&1 != 0 || dst.Width != bl.Width || dst.Height != bl.Height {
		return errors.New("dovi: picture sizes do not match")
	}
	if el != nil && (el.Width*2 != bl.Width || el.Height*2 != bl.Height) {
		return fmt.Errorf("dovi: a %dx%d enhancement layer for a %dx%d base layer", el.Width, el.Height, bl.Width, bl.Height)
	}
	if !c.nlq {
		el = nil
	}
	rows := bl.Height / 2 // chroma rows
	workers := min(runtime.GOMAXPROCS(0), rows)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.band(dst, bl, el, rows*w/workers, rows*(w+1)/workers)
		}()
	}
	wg.Wait()
	return nil
}

// band composes chroma rows [from, to) and the luma rows they cover.
func (c *Composer) band(dst, bl, el *Picture, from, to int) {
	w := bl.Width
	cw := w / 2
	// Upsampled enhancement layer rows: luma, and chroma (Cb, Cr).
	var ely, elb, elr, tmp, tmp2 []int32
	if el != nil {
		ely, elb, elr = make([]int32, w), make([]int32, cw), make([]int32, cw)
		tmp, tmp2 = make([]int32, w), make([]int32, cw)
	}
	sy, sb, sr := make([]float32, cw), make([]float32, cw), make([]float32, cw)
	ob, or := make([]float32, cw), make([]float32, cw)
	for cy := from; cy < to; cy++ {
		// Luma: rows 2cy and 2cy+1.
		for r := range 2 {
			y := 2*cy + r
			var e []int32
			if el != nil {
				upsampleLuma(ely, tmp, el, y)
				e = ely
			}
			lumaRow(dst.Y[y*dst.Pitch:y*dst.Pitch+2*w], bl.Y[y*bl.Pitch:], e, &c.luma, &c.residual[0])
		}
		// Chroma: predicted from the base layer, then the residual.
		y0, y1 := bl.Y[2*cy*bl.Pitch:], bl.Y[(2*cy+1)*bl.Pitch:]
		chromaPrep(sy, sb, sr, y0, y1, bl.UV[cy*bl.Pitch:])
		if c.mmr != nil {
			c.mmr.row(ob, or, sy, sb, sr)
		} else {
			for x := range cw {
				sig := [3]float32{sy[x], sb[x], sr[x]}
				ob[x], or[x] = c.chroma[0].apply(sig, 1), c.chroma[1].apply(sig, 2)
			}
		}
		var eb, er []int32
		if el != nil {
			upsampleChroma(elb, elr, tmp, tmp2, el, cy)
			eb, er = elb, elr
		}
		chromaStore(dst.UV[cy*dst.Pitch:cy*dst.Pitch+2*w], ob, or, eb, er, &c.residual[1], &c.residual[2])
	}
}

// lumaRowGo maps a row of luma (P010) by lut, adds the residual of the
// upsampled enhancement layer codes el (nil for none), and stores it.
func lumaRowGo(out, bl []byte, el []int32, lut, res *[1024]float32) {
	for x := range len(out) / 2 {
		v := lut[binary.LittleEndian.Uint16(bl[2*x:])>>6]
		if el != nil {
			v += res[el[x]]
		}
		put10(out[2*x:], v)
	}
}

// chromaPrepGo gives each chroma sample of a row its normalised luma (from
// the two luma rows: [1 2 1] across, the rows averaged), Cb and Cr.
func chromaPrepGo(sy, sb, sr []float32, y0, y1, uv []byte) {
	for x := range sy {
		l := max(2*x-1, 0)
		ys := sample(y0, l) + 2*sample(y0, 2*x) + sample(y0, 2*x+1) +
			sample(y1, l) + 2*sample(y1, 2*x) + sample(y1, 2*x+1)
		sy[x] = float32(ys) * (1.0 / (8 * 1023))
		sb[x] = float32(sample(uv, 2*x)) * (1.0 / 1023)
		sr[x] = float32(sample(uv, 2*x+1)) * (1.0 / 1023)
	}
}

// chromaStoreGo adds the residuals of the upsampled enhancement layer
// codes eb, er (nil for none) to the predicted Cb and Cr and stores them.
func chromaStoreGo(out []byte, ob, or []float32, eb, er []int32, resB, resR *[1024]float32) {
	for x := range ob {
		vb, vr := ob[x], or[x]
		if eb != nil {
			vb += resB[eb[x]]
			vr += resR[er[x]]
		}
		put10(out[4*x:], vb)
		put10(out[4*x+2:], vr)
	}
}

// put10 stores a normalised value as a P010 sample.
func put10(b []byte, v float32) {
	code := int32(v*1023 + 0.5)
	code = min(max(code, 0), 1023) << 6
	b[0], b[1] = byte(code), byte(code>>8)
}

// rowGo is mmrRow.row in Go.
func (k *mmrRow) rowGo(ob, or, sy, sb, sr []float32) {
	for x := range ob {
		y, b, r := sy[x], sb[x], sr[x]
		f := [21]float32{y, b, r, y * b, y * r, b * r, y * b * r}
		n := 7
		for o := 1; o < k.order; o++ {
			for i := range 7 {
				f[n+i] = f[n-7+i] * f[i]
			}
			n += 7
		}
		sb0, sr0 := k.constant[0], k.constant[1]
		for i := range n {
			sb0 += k.coef[0][i] * f[i]
			sr0 += k.coef[1][i] * f[i]
		}
		ob[x] = min(max(sb0, k.lo[0]), k.hi[0])
		or[x] = min(max(sr0, k.lo[1]), k.hi[1])
	}
}

// apply maps one chroma sample, component ci (1 or 2), from all three.
func (m *chromaMap) apply(sig [3]float32, ci int) float32 {
	x := sig[ci]
	if m.lut != nil {
		return m.lut[int(x*1023+0.5)]
	}
	i := 0
	for i < len(m.pivots) && x >= m.pivots[i] {
		i++
	}
	p := &m.pieces[i]
	var s float32
	if p.poly {
		s = (p.p[2]*x+p.p[1])*x + p.p[0]
	} else {
		s = p.constant
		x1 := [7]float32{sig[0], sig[1], sig[2], sig[0] * sig[1], sig[0] * sig[2], sig[1] * sig[2], sig[0] * sig[1] * sig[2]}
		xn := x1
		for j := range p.order {
			for k := range 7 {
				s += p.coef[j][k] * xn[k]
			}
			for k := range 7 {
				xn[k] *= x1[k]
			}
		}
	}
	return min(max(s, m.lo), m.hi)
}

// Catmull-Rom weights, in 128ths, for the two output rows between input
// rows: the first a quarter of a row before an input row, the second a
// quarter after (the vertical, centred siting).
var (
	vertEven = [4]int32{-3, 29, 111, -9} // rows j-2 .. j+1
	vertOdd  = [4]int32{-9, 111, 29, -3} // rows j-1 .. j+2
)

// upsampleLuma writes row y of the enhancement layer's luma upsampled to
// the base layer's: vertically by vertEven/vertOdd, across co-sited (even
// outputs are the input, odd ones halfway: -1 9 9 -1).
func upsampleLuma(out, tmp []int32, el *Picture, y int) {
	rows, wts := verticalTaps(y/2, y&1 != 0, el.Height)
	p := el.Pitch
	verticalY(tmp[:el.Width], el.Y[rows[0]*p:], el.Y[rows[1]*p:], el.Y[rows[2]*p:], el.Y[rows[3]*p:], wts)
	horizontal(out, tmp[:el.Width])
}

// upsampleChroma writes chroma row cy of the enhancement layer upsampled
// to the base layer's chroma: Cb into ob, Cr into or.
func upsampleChroma(ob, or, tb, tr []int32, el *Picture, cy int) {
	rows, wts := verticalTaps(cy/2, cy&1 != 0, el.Height/2)
	p, cw := el.Pitch, el.Width/2
	verticalUV(tb[:cw], tr[:cw], el.UV[rows[0]*p:], el.UV[rows[1]*p:], el.UV[rows[2]*p:], el.UV[rows[3]*p:], wts)
	horizontal(ob, tb[:cw])
	horizontal(or, tr[:cw])
}

// verticalYGo filters four rows of luma into out, scaled by 128.
func verticalYGo(out []int32, r0, r1, r2, r3 []byte, w [4]int32) {
	for x := range out {
		i := 2 * x
		out[x] = w[0]*int32(binary.LittleEndian.Uint16(r0[i:])>>6) + w[1]*int32(binary.LittleEndian.Uint16(r1[i:])>>6) +
			w[2]*int32(binary.LittleEndian.Uint16(r2[i:])>>6) + w[3]*int32(binary.LittleEndian.Uint16(r3[i:])>>6)
	}
}

// verticalUVGo filters four rows of interleaved chroma into ob (Cb) and
// or (Cr), scaled by 128.
func verticalUVGo(ob, or []int32, r0, r1, r2, r3 []byte, w [4]int32) {
	for x := range ob {
		i := 4 * x
		ob[x] = w[0]*int32(binary.LittleEndian.Uint16(r0[i:])>>6) + w[1]*int32(binary.LittleEndian.Uint16(r1[i:])>>6) +
			w[2]*int32(binary.LittleEndian.Uint16(r2[i:])>>6) + w[3]*int32(binary.LittleEndian.Uint16(r3[i:])>>6)
		or[x] = w[0]*int32(binary.LittleEndian.Uint16(r0[i+2:])>>6) + w[1]*int32(binary.LittleEndian.Uint16(r1[i+2:])>>6) +
			w[2]*int32(binary.LittleEndian.Uint16(r2[i+2:])>>6) + w[3]*int32(binary.LittleEndian.Uint16(r3[i+2:])>>6)
	}
}

func verticalTaps(j int, odd bool, h int) ([4]int, [4]int32) {
	first, wts := j-2, vertEven
	if odd {
		first, wts = j-1, vertOdd
	}
	var rows [4]int
	for k := range 4 {
		rows[k] = min(max(first+k, 0), h-1)
	}
	return rows, wts
}

// horizontalGo doubles a row of values scaled by 128: even outputs are the
// inputs, odd ones the Catmull-Rom midpoint (-1 9 9 -1)/16. Results are
// whole codes, rounded and clamped to 10 bits.
func horizontalGo(out, in []int32) { horizontalTail(out, in, 0) }

// horizontalTail is horizontalGo from input i on.
func horizontalTail(out, in []int32, i int) {
	n := len(in)
	at := func(k int) int32 { return in[min(max(k, 0), n-1)] }
	for ; i < n; i++ {
		out[2*i] = code(in[i], 128)
		out[2*i+1] = code(9*(in[i]+at(i+1))-at(i-1)-at(i+2), 2048)
	}
}

// code is v/div rounded, as a 10-bit code.
func code(v, div int32) int32 {
	v = (v + div/2) / div
	return min(max(v, 0), 1023)
}

func sample(row []byte, x int) int32 { return int32(binary.LittleEndian.Uint16(row[2*x:]) >> 6) }
