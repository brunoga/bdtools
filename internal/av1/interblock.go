package av1

// Inter blocks decided and coded: one reference (LAST_FRAME), the modes
// the motion vector stack offers (NEARESTMV, NEARMV with each RefMvIdx,
// GLOBALMV) and NEWMV from a motion search, ranked by the prediction's
// SATD and their rate, the best few coded in full (the residual with
// the inter transform types, or none: skip) and kept by rate and
// distortion.

import (
	"math"
	"sort"
)

// interCands is how many inter candidates are coded in full.
var interCands = 2

// Debugging: dbgInterModes keeps the inter modes of its bits (1 <<
// (mode - NEARESTMV)); dbgForceInter prefers any inter coding to intra.
var (
	dbgInterModes int
	dbgForceInter bool
)

// interStride is the stride of a plane's inter prediction.
func (t *tileEnc) interStride(plane int) int {
	return int(blockWidth[t.b.size]) >> uint(min(plane, 1))
}

// interTxTypesFor are an inter block's transform types to try: the
// luma's set's, the chroma's compute_tx_type's.
func (t *tileEnc) interTxTypesFor(plane, txSz int) []int {
	f, b := t.f, &t.b
	if plane == 0 {
		if f.fp.qIndex == 0 {
			return []int{DCT_DCT}
		}
		return t.interTxTypes(txSz)
	}
	x4, y4 := max(b.c, t.txX4<<1), max(b.r, t.txY4<<1)
	tt := int(f.txType[f.mi(min(y4, f.miRows-1), min(x4, f.miCols-1))])
	if txTypeInSetInter[t.interTxSet(txSz)][tt] == 0 {
		return []int{DCT_DCT}
	}
	return []int{tt}
}

// writeInterModes writes an inter block's is_inter, reference, mode,
// drl_mode and motion vector difference.
func (t *tileEnc) writeInterModes(cd coder) {
	b, s := &t.b, &t.stack
	t.writeIsInter(cd, true)
	t.writeRefFrame(cd, b.refFrame)
	c := t.cdf
	cd.symbol(b2i(b.yMode != NEWMV), c.newMv[s.newMvContext][:])
	if b.yMode != NEWMV {
		cd.symbol(b2i(b.yMode != GLOBALMV), c.zeroMv[s.zeroMvContext][:])
		if b.yMode != GLOBALMV {
			cd.symbol(b2i(b.yMode == NEARMV), c.refMv[s.refMvContext][:])
		}
	}
	ctx, val, n := s.drlModes(b.yMode, b.refMvIdx)
	for k := range n {
		cd.symbol(val[k], c.drlMode[ctx[k]][:])
	}
	if b.yMode == NEWMV {
		p := s.predMv(NEWMV, b.refMvIdx, 0)
		t.writeMvDiff(cd, mv{b.mv.row - p.row, b.mv.col - p.col})
	}
}

// predictBlock makes an inter block's prediction of every plane.
func (t *tileEnc) predictBlock() {
	f, b := t.f, &t.b
	planes := 1
	if b.hasChroma {
		planes = 3
	}
	for p := range planes {
		f.predictInterBlock(&f.refs, b, p, f.filt, t.interPred[p][:], t.interStride(p), &t.is)
	}
}

// interCand is an inter mode to try.
type interCand struct {
	mode, idx int
	m         mv
	est       float64
}

// searchInterBlock decides the inter coding of a block at (r, c): nil
// when the block may not be inter.
func (t *tileEnc) searchInterBlock(r, c, size int) (float64, *blockDec) {
	f := t.f
	if f.refs[LAST_FRAME] == nil || blockWidth[size] < 8 || blockHeight[size] < 8 {
		return math.Inf(1), nil
	}
	t.setupBlock(r, c, size)
	b, s := &t.b, &t.stack
	b.isInter, b.refFrame, b.txSz = true, LAST_FRAME, int(maxTxSizeRect[size])
	t.findMvStack(s, false, [2]int{LAST_FRAME, NONE})
	sl := math.Sqrt(f.lambda)
	var cands []interCand
	cands = append(cands, interCand{mode: NEARESTMV, m: s.predMv(NEARESTMV, 0, 0)})
	if lo, hi := s.refMvIdxRange(NEARMV); s.numMvFound > 1 {
		for idx := lo; idx <= hi; idx++ {
			cands = append(cands, interCand{mode: NEARMV, idx: idx, m: s.predMv(NEARMV, idx, 0)})
		}
	}
	cands = append(cands, interCand{mode: GLOBALMV, m: s.predMv(GLOBALMV, 0, 0)})
	// NEWMV: a motion search, the vector's rate from RefMvIdx 0's
	// prediction, then the RefMvIdx that codes it cheapest.
	pred0 := s.predMv(NEWMV, 0, 0)
	me := meBlock{ref: f.refs[LAST_FRAME], src: f.src[0], srcStride: f.stride[0], x: c * 4, y: r * 4,
		w: int(blockWidth[size]), h: int(blockHeight[size]), bitDepth: f.bitDepth, filt: f.filt,
		allowHP: f.fp.highPrecisionMV, cost: func(m mv) float64 {
			t.cost.n = 0
			t.writeMvDiff(&t.cost, mv{m.row - pred0.row, m.col - pred0.col})
			return sl * float64(t.cost.n) / costScale
		}}
	starts := []mv{{}}
	for i := range min(s.numMvFound, 4) {
		starts = append(starts, s.refStackMv[i][0])
	}
	if m, _ := t.ms.search(&me, starts); true {
		lo, hi := s.refMvIdxRange(NEWMV)
		best, bestBits := lo, int64(math.MaxInt64)
		for idx := lo; idx <= hi; idx++ {
			b.yMode, b.refMvIdx, b.mv = NEWMV, idx, m
			t.cost.n = 0
			ctx, val, n := s.drlModes(NEWMV, idx)
			for k := range n {
				t.cost.symbol(val[k], t.cdf.drlMode[ctx[k]][:])
			}
			p := s.predMv(NEWMV, idx, 0)
			t.writeMvDiff(&t.cost, mv{m.row - p.row, m.col - p.col})
			if t.cost.n < bestBits {
				best, bestBits = idx, t.cost.n
			}
		}
		cands = append(cands, interCand{mode: NEWMV, idx: best, m: m})
	}
	if dbgInterModes != 0 {
		var keep []interCand
		for _, c := range cands {
			if dbgInterModes>>uint(c.mode-NEARESTMV)&1 != 0 {
				keep = append(keep, c)
			}
		}
		cands = keep
		if len(cands) == 0 {
			return math.Inf(1), nil
		}
	}
	// estimates
	w, h := int(blockWidth[size]), int(blockHeight[size])
	for i := range cands {
		cd := &cands[i]
		b.yMode, b.refMvIdx, b.mv = cd.mode, cd.idx, cd.m
		t.setModes()
		f.predictInterBlock(&f.refs, b, 0, f.filt, t.interPred[0][:], w, &t.is)
		satd := t.satd(0, c*4, r*4, w, h, t.interPred[0][:])
		t.cost.n = 0
		t.writeInterModes(&t.cost)
		cd.est = float64(satd) + sl*float64(t.cost.n)/costScale
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].est < cands[j].est })
	d := 5 + depthOf(min(size, BLOCK_64X64))
	start, best := t.snap(d, 0), t.snap(d, 1)
	t.save(start, r, c, size, true)
	bestJ := math.Inf(1)
	var bestDec *blockDec
	for i, cd := range cands[:min(interCands, len(cands))] {
		if i > 0 {
			t.restore(start, true)
		}
		dec := &blockDec{r: r, c: c, size: size, isInter: true, refFrame: LAST_FRAME, yMode: cd.mode, refMvIdx: cd.idx, mv: cd.m}
		j := t.codeInter(dec, true)
		if j < bestJ {
			bestJ, bestDec = j, dec
			t.save(best, r, c, size, true)
		}
	}
	t.restore(best, true)
	t.setupBlock(r, c, size)
	t.setInterState(bestDec)
	t.setModes()
	t.setInfo()
	return bestJ, bestDec
}

// setInterState puts an inter block's decisions in t.b.
func (t *tileEnc) setInterState(dec *blockDec) {
	b := &t.b
	b.isInter, b.refFrame, b.yMode, b.refMvIdx, b.mv = true, dec.refFrame, dec.yMode, dec.refMvIdx, dec.mv
	b.txSz = int(maxTxSizeRect[dec.size])
	b.skip = dec.skip
}

// codeInter codes an inter block as dec says (its transform types and
// whether to skip decided, when search), giving its cost; the state is
// left as coding it leaves it (the stack must be the block's).
func (t *tileEnc) codeInter(dec *blockDec, search bool) float64 {
	f, b := t.f, &t.b
	t.setInterState(dec)
	t.setModes()
	t.predictBlock()
	planes := 1
	if b.hasChroma {
		planes = 3
	}
	t.cost.n = 0
	t.writeInterModes(&t.cost)
	modeBits := t.cost.n
	// skip: the prediction alone
	var sse int64
	for p := range planes {
		sub := min(p, 1)
		pw := t.interStride(p)
		x0, y0 := (b.c>>uint(sub))*4, (b.r>>uint(sub))*4
		w, h := int(blockWidth[b.size])>>uint(sub), int(blockHeight[b.size])>>uint(sub)
		vw, vh := min(w, ((f.width+sub)>>uint(sub))-x0), min(h, ((f.height+sub)>>uint(sub))-y0)
		st := f.stride[p]
		for y := range vh {
			for x := range vw {
				d := int64(f.src[p][(y0+y)*st+x0+x]) - int64(t.interPred[p][y*pw+x])
				sse += d * d
			}
		}
	}
	skipBits := func(skip bool) int64 {
		b.skip = skip
		t.cost.n = 0
		t.writeSkip(&t.cost)
		return t.cost.n
	}
	jSkip := float64(sse) + f.lambda*float64(modeBits+skipBits(true))/costScale
	if !search && dec.skip {
		t.putPrediction(planes)
		return jSkip
	}
	j := f.lambda * float64(modeBits+skipBits(false)) / costScale
	if search {
		dec.txTypes = dec.txTypes[:0]
	}
	txSz := b.txSz
	stepX, stepY := int(txWidth[txSz])>>2, int(txHeight[txSz])>>2
	k := 0
	zero := true
	for y := 0; y < b.bh4; y += stepY {
		for x := 0; x < b.bw4; x += stepX {
			if b.r+y >= f.miRows || b.c+x >= f.miCols {
				continue
			}
			if f.fp.txSelect && b.size > BLOCK_4X4 && txSz != TX_4X4 {
				t.cost.n = 0
				t.writeTxfmSplit(&t.cost, b.r+y, b.c+x, txSz, false)
				j += f.lambda * float64(t.cost.n) / costScale
			}
			tt := -1
			if !search && k < len(dec.txTypes) {
				tt = int(dec.txTypes[k])
			}
			jj, eob, chosen, done := t.transformBlock(0, b.c*4+4*x, b.r*4+4*y, txSz, tt)
			if !done {
				continue
			}
			k++
			if search {
				dec.txTypes = append(dec.txTypes, uint8(chosen)) //nolint:gosec // a type
			}
			j += jj
			zero = zero && eob == 0
		}
	}
	if b.hasChroma {
		uvTx := t.planeTxSize(1)
		sx, sy := int(txWidth[uvTx])>>2, int(txHeight[uvTx])>>2
		planeSz := int(subsampledSize[b.size][1][1])
		n4w, n4h := int(num4x4BlocksWide[planeSz]), int(num4x4BlocksHigh[planeSz])
		for p := 1; p <= 2; p++ {
			for y := 0; y < n4h; y += sy {
				for x := 0; x < n4w; x += sx {
					jj, eob, _, done := t.transformBlock(p, (b.c>>1)*4+4*x, (b.r>>1)*4+4*y, uvTx, -1)
					if done {
						j += jj
						zero = zero && eob == 0
					}
				}
			}
		}
	}
	if search && (zero || jSkip <= j) {
		// skip: the prediction alone, the contexts as a skipped block's
		dec.skip = true
		b.skip = true
		t.putPrediction(planes)
		t.resetBlockContext()
		return jSkip
	}
	b.skip = zero
	return j
}

// putPrediction puts an inter block's prediction in the reconstruction.
func (t *tileEnc) putPrediction(planes int) {
	f, b := t.f, &t.b
	for p := range planes {
		sub := min(p, 1)
		pw := t.interStride(p)
		x0, y0 := (b.c>>uint(sub))*4, (b.r>>uint(sub))*4
		h := int(blockHeight[b.size]) >> uint(sub)
		st := f.stride[p]
		for y := range h {
			copy(f.rec[p][(y0+y)*st+x0:(y0+y)*st+x0+pw], t.interPred[p][y*pw:(y+1)*pw])
		}
	}
	t.markDecoded()
}

// markDecoded sets the BlockDecoded flags and LoopfilterTxSizes of a
// block coded without its transform blocks being coded one by one.
func (t *tileEnc) markDecoded() {
	f, b := t.f, &t.b
	planes := 1
	if b.hasChroma {
		planes = 3
	}
	for p := range planes {
		sub := min(p, 1)
		txSz := t.planeTxSize(p)
		stepX, stepY := int(txWidth[txSz])>>2, int(txHeight[txSz])>>2
		planeSz := b.size
		if p > 0 {
			planeSz = int(subsampledSize[b.size][1][1])
		}
		n4w, n4h := int(num4x4BlocksWide[planeSz]), int(num4x4BlocksHigh[planeSz])
		lw := f.miCols >> uint(sub)
		for y := 0; y < n4h; y += stepY {
			for x := 0; x < n4w; x += stepX {
				sx, sy := (b.c>>uint(sub))+x, (b.r>>uint(sub))+y
				if sx >= lw || sy >= f.miRows>>uint(sub) {
					continue
				}
				row, col := sy<<uint(sub), sx<<uint(sub)
				sbRow, sbCol := row&15, col&15
				for i := range stepY {
					for j := range stepX {
						if yy, xx := sy+i, sx+j; yy < f.miRows>>uint(sub) && xx < lw {
							f.lfTx[p][yy*lw+xx] = uint8(txSz) //nolint:gosec // a size
						}
						t.decoded[p][(sbRow>>uint(sub))+i+1][(sbCol>>uint(sub))+j+1] = true
					}
				}
			}
		}
	}
}

// finalInterBlock codes a decided inter block for real.
func (t *tileEnc) finalInterBlock(dec *blockDec) {
	f := t.f
	t.setupBlock(dec.r, dec.c, dec.size)
	b := &t.b
	t.setInterState(dec)
	t.findMvStack(&t.stack, false, [2]int{dec.refFrame, NONE})
	// the stack is the search's, so this changes nothing; but a vector a
	// mode takes from the stack must be the stack's
	if lo, hi := t.stack.refMvIdxRange(dec.yMode); dec.refMvIdx < lo || dec.refMvIdx > hi {
		dec.refMvIdx = min(max(dec.refMvIdx, lo), hi)
	}
	if dec.yMode != NEWMV {
		dec.mv = t.stack.predMv(dec.yMode, dec.refMvIdx, 0)
	}
	t.setInterState(dec)
	var ctx snapshot
	t.save(&ctx, dec.r, dec.c, dec.size, false)
	t.txs, t.levels = t.txs[:0], t.levels[:0]
	t.cdf = t.sbCDF
	t.codeInter(dec, false)
	skip := dec.skip || b.skip
	ctx.decoded = t.decoded
	t.restore(&ctx, false)
	t.cdf = t.live
	b.skip = skip
	t.writeSkip(t.w)
	if f.seq.cdef && !b.skip && !f.lossless && !t.cdefDone {
		i := (b.r>>4)*((f.miCols+15)>>4) + b.c>>4
		t.w.cdef(i)
		f.cdefIdx[i] = 0
		t.cdefDone = true
	}
	t.writeInterModes(t.w)
	if !b.skip {
		txSz := b.txSz
		stepX, stepY := int(txWidth[txSz])>>2, int(txHeight[txSz])>>2
		if f.fp.txSelect && b.size > BLOCK_4X4 && txSz != TX_4X4 {
			for y := 0; y < b.bh4; y += stepY {
				for x := 0; x < b.bw4; x += stepX {
					if b.r+y < f.miRows && b.c+x < f.miCols {
						t.writeTxfmSplit(t.w, b.r+y, b.c+x, txSz, false)
					}
				}
			}
		}
		for i := range t.txs {
			t.writeCoeffs(t.w, &t.txs[i], true)
		}
	} else {
		t.resetBlockContext()
	}
	t.setModes()
	t.setInfo()
}
