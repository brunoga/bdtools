package av1

import (
	"math"
	"sync/atomic"
)

// Inter-intra (interintra): a single reference block's prediction blended
// with an intra prediction of the whole block (DC, V, H or SMOOTH, from
// the neighbours as for an intra block), by a mask: one fading from the
// edge the intra mode predicts from (the intra mode variant mask), or a
// wedge (the luma's, subsampled for chroma; wedge_sign 0).

// dbgNoInterIntra turns inter-intra (enable_interintra_compound) off;
// dbgForceInterIntra codes single reference blocks inter-intra where they
// can be (tests).
var dbgNoInterIntra, dbgForceInterIntra = false, false

// interIntraBlocks counts the blocks coded inter-intra, smooth and by
// wedge (for tests).
var interIntraBlocks [2]atomic.Int64

// interIntraCands is how many of the best single reference candidates
// are also estimated inter-intra.
var interIntraCands = 2

// interIntra is a block's inter-intra coding.
type interIntra struct {
	on    bool // interintra
	mode  int  // interintra_mode
	wedge bool // wedge_interintra
	idx   int  // wedge_index
}

// interIntraModes are the intra modes of interintra_mode.
var interIntraModes = [INTERINTRA_MODES]int{DC_PRED, V_PRED, H_PRED, SMOOTH_PRED}

// readsInterIntra reports whether read_interintra_mode reads interintra
// for the block: a single reference block, not skip_mode, 8x8 to 32x32.
func (t *tileEnc) readsInterIntra() bool {
	b := &t.b
	return t.f.seq.interIntra && !b.skipMode && !t.isCompound() && b.size >= BLOCK_8X8 && b.size <= BLOCK_32X32
}

// writeInterIntra writes read_interintra_mode's syntax.
func (t *tileEnc) writeInterIntra(cd coder) {
	b := &t.b
	if !t.readsInterIntra() {
		return
	}
	ctx := int(sizeGroup[b.size]) - 1
	cd.symbol(b2i(b.ii.on), t.cdf.interIntra[ctx][:])
	if !b.ii.on {
		return
	}
	cd.symbol(b.ii.mode, t.cdf.interIntraMode[ctx][:])
	cd.symbol(b2i(b.ii.wedge), t.cdf.wedgeInterIntra[b.size][:])
	if b.ii.wedge {
		cd.symbol(b.ii.idx, t.cdf.wedgeIndex[b.size][:])
	}
}

// interIntraPred predicts a plane of the block intra by an interintra
// mode, the whole block at once (w by h, stride w).
func (t *tileEnc) interIntraPred(plane, mode int, pred []uint16) {
	f, b := t.f, &t.b
	sub, sz := 0, b.size
	if plane > 0 {
		sub, sz = 1, int(subsampledSize[b.size][1][1])
	}
	ib := t.intraBlockFor(plane, (b.c>>uint(sub))*4, (b.r>>uint(sub))*4, int(maxTxSizeRect[sz]))
	ib.mode, ib.angleDelta, ib.filterIntra = interIntraModes[mode], 0, -1
	t.edges.predictIntra(&ib, f.rec[plane], f.stride[plane], pred)
}

// interIntraBlend is the mask blend process for inter-intra: a plane's
// inter prediction in dst (w by h, stride dstStride) blended with its
// intra prediction intra (stride w), the mask weighing the intra.
func interIntraBlend(ii *interIntra, size, plane int, intra []uint16, w, h int, dst []uint16, dstStride int) {
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

// blendInterIntra blends a plane of the block's inter prediction (dst,
// stride dstStride) with its intra one, when the block is inter-intra.
func (t *tileEnc) blendInterIntra(plane int, dst []uint16, dstStride int) {
	b := &t.b
	if !b.ii.on {
		return
	}
	sub := min(plane, 1)
	w, h := int(blockWidth[b.size])>>uint(sub), int(blockHeight[b.size])>>uint(sub)
	intra := t.iiPred[:w*h]
	t.interIntraPred(plane, b.ii.mode, intra)
	interIntraBlend(&b.ii, b.size, plane, intra, w, h, dst, dstStride)
}

// interIntraVariants estimates a single reference candidate inter-intra:
// each mode with its smooth mask, then the best mode with each wedge, by
// the luma SATD and rate. It gives the best smooth and the best wedge
// variant. The luma intra predictions are the block's (intra, by mode).
func (t *tileEnc) interIntraVariants(cd interCand, intra *[INTERINTRA_MODES][32 * 32]uint16) []interCand {
	f, b := t.f, &t.b
	w, h := int(blockWidth[b.size]), int(blockHeight[b.size])
	b.refFrame, b.refFrame2, b.yMode, b.refMvIdx, b.mv, b.mv2 = cd.ref, NONE, cd.mode, cd.idx, cd.m, mv{}
	b.interp, b.obmc, b.warp, b.compDist, b.masked, b.ii = cd.interp, false, false, false, false, interIntra{}
	t.setModes()
	inter := t.iiInter[:w*h]
	f.predictInterBlock(&f.refs, b, 0, f.filt, inter, w, &t.is)
	sl := math.Sqrt(t.lambda)
	pred := t.interPred[0][:w*h]
	try := func(ii interIntra) float64 {
		b.ii = ii
		copy(pred, inter)
		interIntraBlend(&ii, b.size, 0, intra[ii.mode][:], w, h, pred, w)
		satd := t.satd(0, b.c*4, b.r*4, w, h, pred)
		t.cost.n = 0
		t.writeInterModes(&t.cost)
		return float64(satd) + sl*float64(t.cost.n)/costScale
	}
	best, bestE := interIntra{}, math.Inf(1)
	for mode := range INTERINTRA_MODES {
		ii := interIntra{on: true, mode: mode}
		if e := try(ii); e < bestE {
			best, bestE = ii, e
		}
	}
	c := cd
	c.ii, c.est = best, bestE
	out := []interCand{c}
	bestW, bestWE := interIntra{}, math.Inf(1)
	for idx := range WEDGE_TYPES {
		ii := interIntra{on: true, mode: best.mode, wedge: true, idx: idx}
		if e := try(ii); e < bestWE {
			bestW, bestWE = ii, e
		}
	}
	c.ii, c.est = bestW, bestWE
	out = append(out, c)
	b.ii = interIntra{}
	return out
}

// interIntraCandidates adds the best single reference candidates' (cands
// sorted) inter-intra variants.
func (t *tileEnc) interIntraCandidates(cands []interCand) []interCand {
	b := &t.b
	b.refFrame2, b.skipMode = NONE, false
	if !t.readsInterIntra() {
		return cands
	}
	w, h := int(blockWidth[b.size]), int(blockHeight[b.size])
	intra := &t.iiIntra
	for mode := range INTERINTRA_MODES {
		t.interIntraPred(0, mode, intra[mode][:w*h])
	}
	var extra []interCand
	for _, cd := range cands {
		if cd.ref2 <= INTRA_FRAME && !cd.ii.on && len(extra) < 2*interIntraCands {
			extra = append(extra, t.interIntraVariants(cd, intra)...)
		}
	}
	if dbgForceInterIntra && len(extra) > 0 {
		return extra
	}
	return append(cands, extra...)
}
