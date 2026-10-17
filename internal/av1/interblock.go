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
	dbgInterModes    int
	dbgForceInter    bool
	dbgForceCompound bool
	dbgForceSkipMode bool
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
	t.writeRefFrames(cd)
	if t.isCompound() {
		t.writeCompoundModes(cd)
		return
	}
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

// interRefs are the references inter blocks try.
var interRefs = []int{LAST_FRAME, BWDREF_FRAME, ALTREF_FRAME, GOLDEN_FRAME}

// interCand is an inter mode to try.
type interCand struct {
	ref, ref2 int // ref2 NONE for a single reference
	m2        mv
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
	b := &t.b
	b.isInter, b.txSz = true, int(maxTxSizeRect[size])
	var cands []interCand
	seen := map[*refFrameState]bool{}
	for _, ref := range interRefs {
		if r := f.refs[ref]; r == nil || seen[r] {
			continue
		}
		seen[f.refs[ref]] = true
		cands = t.interCandsFor(ref, cands)
	}
	if len(cands) == 0 {
		return math.Inf(1), nil
	}
	if f.fp.refSelect && min(b.bw4, b.bh4) >= 2 && !dbgNoCompound {
		newOf := map[int]mv{}
		for _, cd := range cands {
			if cd.mode == NEWMV {
				newOf[cd.ref] = cd.m
			}
		}
		for _, pair := range [][2]int{{LAST_FRAME, BWDREF_FRAME}, {LAST_FRAME, ALTREF_FRAME}} {
			r0, r1 := f.refs[pair[0]], f.refs[pair[1]]
			if r0 == nil || r1 == nil || !seen[r0] || !seen[r1] || r0.disp >= f.fp.orderHint || r1.disp <= f.fp.orderHint {
				continue
			}
			if pair[1] == ALTREF_FRAME && f.refs[BWDREF_FRAME] == r1 {
				continue // the same pair
			}
			first := len(cands)
			cands = t.compoundCands(pair[0], pair[1], newOf[pair[0]], newOf[pair[1]], cands)
			t.estimate(cands[first:])
		}
		if dbgForceCompound {
			var comp []interCand
			for _, cd := range cands {
				if cd.ref2 > INTRA_FRAME && (dbgInterModes == 0 || dbgInterModes>>uint(cd.mode-NEAREST_NEARESTMV)&1 != 0) {
					comp = append(comp, cd)
				}
			}
			if len(comp) > 0 {
				cands = comp
			}
		}
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
		dec := &blockDec{r: r, c: c, size: size, isInter: true, refFrame: cd.ref, refFrame2: cd.ref2, yMode: cd.mode, refMvIdx: cd.idx, mv: cd.m, mv2: cd.m2}
		t.findMvStack(&t.stack, cd.ref2 > INTRA_FRAME, [2]int{cd.ref, cd.ref2})
		j := t.codeInter(dec, true)
		if j < bestJ {
			bestJ, bestDec = j, dec
			t.save(best, r, c, size, true)
		}
	}
	if f.fp.skipModePresent && !dbgNoSkipMode {
		t.restore(start, true)
		sm := f.fp.skipModeFrame
		dec := &blockDec{r: r, c: c, size: size, isInter: true, refFrame: sm[0], refFrame2: sm[1], yMode: NEAREST_NEARESTMV, skipMode: true, skip: true}
		t.findMvStack(&t.stack, true, [2]int{sm[0], sm[1]})
		dec.mv, dec.mv2 = t.stack.predMv(NEAREST_NEARESTMV, 0, 0), t.stack.predMv(NEAREST_NEARESTMV, 0, 1)
		if j := t.codeInter(dec, true); j < bestJ || dbgForceSkipMode {
			bestJ, bestDec = j, dec
			t.save(best, r, c, size, true)
		}
	}
	t.restore(best, true)
	t.setupBlock(r, c, size)
	t.findMvStack(&t.stack, bestDec.refFrame2 > INTRA_FRAME, [2]int{bestDec.refFrame, bestDec.refFrame2})
	t.setInterState(bestDec)
	t.setModes()
	t.setInfo()
	return bestJ, bestDec
}

// setInterState puts an inter block's decisions in t.b.
func (t *tileEnc) setInterState(dec *blockDec) {
	b := &t.b
	b.isInter, b.refFrame, b.yMode, b.refMvIdx, b.mv = true, dec.refFrame, dec.yMode, dec.refMvIdx, dec.mv
	b.refFrame2, b.mv2 = dec.refFrame2, dec.mv2
	b.skipMode = dec.skipMode
	if b.refFrame2 <= INTRA_FRAME {
		b.refFrame2, b.mv2 = NONE, mv{}
	}
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
	if dec.skipMode {
		t.writeSkipMode(&t.cost)
	} else {
		t.writeInterModes(&t.cost)
	}
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
	if dec.skipMode {
		// skip_mode alone: no skip flag, no residual
		jSkip = float64(sse) + f.lambda*float64(modeBits)/costScale
		b.skip = true
		t.putPrediction(planes)
		t.resetBlockContext()
		return jSkip
	}
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
	comp := dec.refFrame2 > INTRA_FRAME
	t.findMvStack(&t.stack, comp, [2]int{dec.refFrame, dec.refFrame2})
	// the stack is the search's, so this changes nothing; but a vector a
	// mode takes from the stack must be the stack's
	if lo, hi := t.stack.refMvIdxRange(dec.yMode); dec.refMvIdx < lo || dec.refMvIdx > hi {
		dec.refMvIdx = min(max(dec.refMvIdx, lo), hi)
	}
	if getMode(dec.yMode, 0) != NEWMV {
		dec.mv = t.stack.predMv(dec.yMode, dec.refMvIdx, 0)
	}
	if comp && getMode(dec.yMode, 1) != NEWMV {
		dec.mv2 = t.stack.predMv(dec.yMode, dec.refMvIdx, 1)
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
	if dec.skipMode {
		t.writeSkipMode(t.w)
		t.resetBlockContext()
		t.setModes()
		t.setInfo()
		return
	}
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

// interCandsFor adds a reference's candidates, with their estimates:
// NEARESTMV, NEARMV with each RefMvIdx, GLOBALMV, and NEWMV from a motion
// search (its RefMvIdx the one that codes its vector cheapest).
func (t *tileEnc) interCandsFor(ref int, cands []interCand) []interCand {
	f, b, s := t.f, &t.b, &t.stack
	r, c, size := b.r, b.c, b.size
	b.refFrame = ref
	t.findMvStack(s, false, [2]int{ref, NONE})
	sl := math.Sqrt(f.lambda)
	first := len(cands)
	cands = append(cands, interCand{ref: ref, mode: NEARESTMV, m: s.predMv(NEARESTMV, 0, 0)})
	if lo, hi := s.refMvIdxRange(NEARMV); s.numMvFound > 1 {
		for idx := lo; idx <= hi; idx++ {
			cands = append(cands, interCand{ref: ref, mode: NEARMV, idx: idx, m: s.predMv(NEARMV, idx, 0)})
		}
	}
	cands = append(cands, interCand{ref: ref, mode: GLOBALMV, m: s.predMv(GLOBALMV, 0, 0)})
	pred0 := s.predMv(NEWMV, 0, 0)
	me := meBlock{ref: f.refs[ref], src: f.src[0], srcStride: f.stride[0], x: c * 4, y: r * 4,
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
	m, _ := t.ms.search(&me, starts)
	lo, hi := s.refMvIdxRange(NEWMV)
	best, bestBits := lo, int64(math.MaxInt64)
	for idx := lo; idx <= hi; idx++ {
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
	cands = append(cands, interCand{ref: ref, mode: NEWMV, idx: best, m: m})
	if dbgInterModes != 0 {
		keep := cands[:first]
		for _, cd := range cands[first:] {
			if dbgInterModes>>uint(cd.mode-NEARESTMV)&1 != 0 {
				keep = append(keep, cd)
			}
		}
		cands = keep
	}
	t.estimate(cands[first:])
	return cands
}

// estimate sets candidates' estimates: their luma prediction's SATD and
// rate (the stack the block's for their references).
func (t *tileEnc) estimate(cands []interCand) {
	f, b := t.f, &t.b
	w, h := int(blockWidth[b.size]), int(blockHeight[b.size])
	sl := math.Sqrt(f.lambda)
	for i := range cands {
		cd := &cands[i]
		b.refFrame, b.refFrame2 = cd.ref, cd.ref2
		if b.refFrame2 <= INTRA_FRAME {
			b.refFrame2 = NONE
		}
		b.yMode, b.refMvIdx, b.mv, b.mv2 = cd.mode, cd.idx, cd.m, cd.m2
		t.setModes()
		f.predictInterBlock(&f.refs, b, 0, f.filt, t.interPred[0][:], w, &t.is)
		satd := t.satd(0, b.c*4, b.r*4, w, h, t.interPred[0][:])
		t.cost.n = 0
		t.writeInterModes(&t.cost)
		cd.est = float64(satd) + sl*float64(t.cost.n)/costScale
	}
}
