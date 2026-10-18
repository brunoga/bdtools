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
var interCands = 4

// filterRD chooses the best candidate's interpolation filter by coding it
// with each (else the estimates' SATD alone chooses).
var filterRD = true

// obmcCands is how many of them (single reference ones) are also coded
// with OBMC.
var obmcCands = 2

// Debugging: dbgInterModes keeps the inter modes of its bits (1 <<
// (mode - NEARESTMV)); dbgForceInter prefers any inter coding to intra.
var (
	dbgInterModes    int
	dbgForceInter    bool
	dbgForceCompound bool
	dbgForceSkipMode bool
	// dbgForceObmc codes OBMC wherever it may be; dbgNoObmc never (and
	// the frames not motion mode switchable)
	dbgForceObmc, dbgNoObmc bool
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
		t.writeCompoundIdx(cd)
		t.writeInterpFilter(cd)
		return
	}
	defer t.writeInterpFilter(cd)
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
	if t.readsUseObmc() {
		if t.readsMotionMode() {
			mode := SIMPLE
			if b.obmc {
				mode = OBMC
			} else if b.warp {
				mode = LOCALWARP
			}
			cd.symbol(mode, c.motionMode[b.size][:])
		} else {
			cd.symbol(b2i(b.obmc), c.useObmc[b.size][:])
		}
	}
}

// readsUseObmc reports whether read_motion_mode reads use_obmc for the
// block (warped motion off, so use_obmc is its motion_mode): a single
// reference block of 8 by 8 or more, not skip_mode, with an overlappable
// neighbour.
func (t *tileEnc) readsUseObmc() bool {
	f, b := t.f, &t.b
	if b.skipMode || !f.fp.motionModeSwitchable || min(blockWidth[b.size], blockHeight[b.size]) < 8 {
		return false
	}
	fi := f.inter
	if !fi.forceIntegerMv && (b.yMode == GLOBALMV || b.yMode == GLOBAL_GLOBALMV) && fi.gmType[b.refFrame] > TRANSLATION {
		return false
	}
	return !t.isCompound() && t.hasOverlappableCandidates()
}

// readsMotionMode reports whether read_motion_mode, reading it at all
// (readsUseObmc), reads motion_mode rather than use_obmc: warped motion
// allowed and a warp sample found.
func (t *tileEnc) readsMotionMode() bool {
	return t.f.fp.allowWarp && t.findWarpSamples().n > 0
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
		if b.obmc {
			f.overlapPredict(&f.refs, b, p, f.filt, t.interPred[p][:], t.interStride(p), &t.is)
		}
	}
}

// interRefs are the references inter blocks try (a frame two of them
// are tried once), compPairs the compound pairs.
var (
	interRefs = []int{LAST_FRAME, BWDREF_FRAME, ALTREF2_FRAME, ALTREF_FRAME, LAST2_FRAME, LAST3_FRAME, GOLDEN_FRAME}
	compPairs = [][2]int{{LAST_FRAME, BWDREF_FRAME}, {LAST_FRAME, ALTREF2_FRAME}, {LAST_FRAME, ALTREF_FRAME},
		{LAST2_FRAME, BWDREF_FRAME}, {LAST2_FRAME, ALTREF2_FRAME}, {LAST2_FRAME, ALTREF_FRAME}}
)

// dbgFewRefs limits the search to the references and pairs it had first
// (LAST, BWDREF, ALTREF, GOLDEN; LAST with BWDREF or ALTREF).
var dbgFewRefs = false

// interCand is an inter mode to try.
type interCand struct {
	interp    int // the interpolation filter
	compDist  bool
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
	refs, pairs := interRefs, compPairs
	if dbgFewRefs {
		refs = []int{LAST_FRAME, BWDREF_FRAME, ALTREF_FRAME, GOLDEN_FRAME}
		pairs = [][2]int{{LAST_FRAME, BWDREF_FRAME}, {LAST_FRAME, ALTREF_FRAME}}
	}
	// the reference tried for each frame
	tried := map[*refFrameState]int{}
	for _, ref := range refs {
		if r := f.refs[ref]; r == nil || seen[r] {
			continue
		}
		seen[f.refs[ref]] = true
		tried[f.refs[ref]] = ref
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
		pairSeen := map[[2]*refFrameState]bool{}
		for _, pair := range pairs {
			r0, r1 := f.refs[pair[0]], f.refs[pair[1]]
			if r0 == nil || r1 == nil || !seen[r0] || !seen[r1] || r0.disp >= f.fp.orderHint || r1.disp <= f.fp.orderHint {
				continue
			}
			if pairSeen[[2]*refFrameState{r0, r1}] {
				continue // the same frames as a pair tried
			}
			pairSeen[[2]*refFrameState{r0, r1}] = true
			first := len(cands)
			cands = t.compoundCands(pair[0], pair[1], newOf[tried[r0]], newOf[tried[r1]], cands)
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
		dec := &blockDec{r: r, c: c, size: size, isInter: true, refFrame: cd.ref, refFrame2: cd.ref2, yMode: cd.mode, refMvIdx: cd.idx, mv: cd.m, mv2: cd.m2, interp: cd.interp, compDist: cd.compDist}
		t.findMvStack(&t.stack, cd.ref2 > INTRA_FRAME, [2]int{cd.ref, cd.ref2})
		t.setInterState(dec)
		// OBMC tried for the best two single reference candidates, with
		// their vector and filter (all of them when forced, without the
		// simple prediction)
		obmc := (i < obmcCands || dbgForceObmc) && !dbgNoObmc && t.readsUseObmc()
		if obmc && !dbgForceObmc {
			obmc = t.obmcPredictsBetter()
		}
		if !obmc || !dbgForceObmc {
			if j := t.codeInter(dec, true); j < bestJ {
				bestJ, bestDec = j, dec
				t.save(best, r, c, size, true)
			}
		}
		if obmc {
			t.restore(start, true)
			do := *dec
			do.obmc, do.skip, do.txSplit, do.txTypes = true, false, nil, nil
			if j := t.codeInter(&do, true); j < bestJ || dbgForceObmc {
				bestJ, bestDec = j, &do
				t.save(best, r, c, size, true)
			}
		}
		// warped, for the best two single reference candidates whose
		// neighbours give a valid model
		if (i < warpCands || dbgForceWarp) && cd.ref2 <= INTRA_FRAME && f.fp.allowWarp && t.readsUseObmc() && t.readsMotionMode() {
			t.restore(start, true)
			dw := *dec
			dw.warp, dw.obmc, dw.skip, dw.txSplit, dw.txTypes, dw.interp = true, false, false, nil, nil, EIGHTTAP
			t.setInterState(&dw)
			if b.warpValid {
				if j := t.codeInter(&dw, true); j < bestJ || dbgForceWarp {
					bestJ, bestDec = j, &dw
					t.save(best, r, c, size, true)
				}
			}
		}
	}
	// the filter of the best, by rate and distortion
	if filterRD && bestDec != nil && f.fp.interpFilter == SWITCHABLE && !dbgNoSwitchable {
		b.refFrame, b.refFrame2, b.yMode = bestDec.refFrame, bestDec.refFrame2, bestDec.yMode
		if b.refFrame2 <= INTRA_FRAME {
			b.refFrame2 = NONE
		}
		b.skipMode, b.warp = false, bestDec.warp
		if t.needsInterpFilter() {
			base := *bestDec
			for _, fl := range []int{EIGHTTAP, EIGHTTAP_SMOOTH, EIGHTTAP_SHARP} {
				if fl == base.interp {
					continue
				}
				t.restore(start, true)
				dec := base
				dec.interp, dec.txTypes, dec.txSplit = fl, nil, nil
				t.findMvStack(&t.stack, dec.refFrame2 > INTRA_FRAME, [2]int{dec.refFrame, dec.refFrame2})
				if j := t.codeInter(&dec, true); j < bestJ {
					bestJ, bestDec = j, &dec
					t.save(best, r, c, size, true)
				}
			}
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

// obmcPredictsBetter reports whether the block's luma prediction (the
// state's) has a lower SATD with OBMC than without: OBMC is coded in full
// only then.
func (t *tileEnc) obmcPredictsBetter() bool {
	f, b := t.f, &t.b
	w, h := int(blockWidth[b.size]), int(blockHeight[b.size])
	t.setModes()
	pred := t.interPred[0][:]
	f.predictInterBlock(&f.refs, b, 0, f.filt, pred, w, &t.is)
	simple := t.satd(0, b.c*4, b.r*4, w, h, pred)
	f.overlapPredict(&f.refs, b, 0, f.filt, pred, w, &t.is)
	return t.satd(0, b.c*4, b.r*4, w, h, pred) < simple
}

// setInterState puts an inter block's decisions in t.b.
func (t *tileEnc) setInterState(dec *blockDec) {
	b := &t.b
	b.isInter, b.refFrame, b.yMode, b.refMvIdx, b.mv = true, dec.refFrame, dec.yMode, dec.refMvIdx, dec.mv
	b.refFrame2, b.mv2 = dec.refFrame2, dec.mv2
	b.skipMode = dec.skipMode
	b.interp = dec.interp
	b.compDist = dec.compDist && !dec.skipMode && dec.refFrame2 > INTRA_FRAME
	b.obmc = dec.obmc
	b.warp, b.warpValid = dec.warp, false
	if b.warp {
		b.interp = EIGHTTAP // interp_filter not coded
		ws := t.findWarpSamples()
		b.warpParams, b.warpValid = t.warpEstimation(&ws)
		if b.warpValid {
			b.warpValid = setupShear(&b.warpParams).valid
		}
	}
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
	jSkip := float64(sse) + t.lambda*float64(modeBits+skipBits(true))/costScale
	if dec.skipMode {
		// skip_mode alone: no skip flag, no residual
		jSkip = float64(sse) + t.lambda*float64(modeBits)/costScale
		b.skip = true
		t.putPrediction(planes)
		t.resetBlockContext()
		return jSkip
	}
	if !search && dec.skip {
		t.putPrediction(planes)
		return jSkip
	}
	j := t.lambda * float64(modeBits+skipBits(false)) / costScale
	if search {
		dec.txTypes, dec.txSplit = dec.txTypes[:0], dec.txSplit[:0]
	}
	txSz := b.txSz
	stepX, stepY := int(txWidth[txSz])>>2, int(txHeight[txSz])>>2
	cur := &varTxCursor{}
	zero := true
	for y := 0; y < b.bh4; y += stepY {
		for x := 0; x < b.bw4; x += stepX {
			jj, z := t.varTx(dec, b.r+y, b.c+x, txSz, 0, search, cur)
			j += jj
			zero = zero && z
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
	if !t.readsUseObmc() {
		dec.obmc, dec.warp = false, false // as the search found it (so never)
	}
	t.setInterState(dec)
	if dec.warp && !t.readsMotionMode() {
		dec.warp = false // never, as for OBMC
		t.setInterState(dec)
	}
	if b.warp && b.warpValid {
		warpedBlocks.Add(1)
	}
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
		t.writeDeltaQ(t.w)
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
	t.writeDeltaQ(t.w)
	t.writeInterModes(t.w)
	if !b.skip {
		txSz := b.txSz
		stepX, stepY := int(txWidth[txSz])>>2, int(txHeight[txSz])>>2
		cur := &varTxCursor{}
		for y := 0; y < b.bh4; y += stepY {
			for x := 0; x < b.bw4; x += stepX {
				t.writeVarTx(t.w, dec, b.r+y, b.c+x, txSz, 0, cur)
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
	sl := math.Sqrt(t.lambda)
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
	sl := math.Sqrt(t.lambda)
	for i := range cands {
		cd := &cands[i]
		b.refFrame, b.refFrame2 = cd.ref, cd.ref2
		if b.refFrame2 <= INTRA_FRAME {
			b.refFrame2 = NONE
		}
		b.yMode, b.refMvIdx, b.mv, b.mv2 = cd.mode, cd.idx, cd.m, cd.m2
		b.interp, b.obmc, b.compDist = EIGHTTAP, false, false
		filters := []int{EIGHTTAP}
		if t.needsInterpFilter() && !dbgNoSwitchable {
			filters = []int{EIGHTTAP, EIGHTTAP_SMOOTH, EIGHTTAP_SHARP}
		}
		weights := []bool{false}
		if b.refFrame2 > INTRA_FRAME && f.seq.jntComp {
			weights = []bool{false, true}
			if dbgForceCompDist {
				weights = []bool{true}
			}
		}
		cd.est, cd.interp, cd.compDist = math.Inf(1), EIGHTTAP, false
		for _, dist := range weights {
			b.compDist = dist
			for _, fl := range filters {
				b.interp = fl
				t.setModes()
				f.predictInterBlock(&f.refs, b, 0, f.filt, t.interPred[0][:], w, &t.is)
				satd := t.satd(0, b.c*4, b.r*4, w, h, t.interPred[0][:])
				t.cost.n = 0
				t.writeInterModes(&t.cost)
				if e := float64(satd) + sl*float64(t.cost.n)/costScale; e < cd.est {
					cd.est, cd.interp, cd.compDist = e, fl, dist
				}
			}
		}
		b.compDist = false
	}
}

// varTxCursor is where a decided transform tree is read from.
type varTxCursor struct{ split, typ int }

// txBlockSize is the block size of a transform size's dimensions.
var txBlockSize = func() (m [TX_SIZES_ALL]int) {
	for tx := range TX_SIZES_ALL {
		for bs := range BLOCK_SIZES {
			if blockWidth[bs] == txWidth[tx] && blockHeight[bs] == txHeight[tx] {
				m[tx] = bs
			}
		}
	}
	return m
}()

// varTxSplits reports whether a node of an inter block's transform tree
// codes txfm_split.
func (t *tileEnc) varTxSplits(txSz, depth int) bool {
	return t.f.fp.txSelect && t.b.size > BLOCK_4X4 && txSz != TX_4X4 && depth < MAX_VARTX_DEPTH
}

// varTx codes a node of an inter block's luma transform tree at mi (row,
// col) (read_var_tx_size's order): whole, or split, as the search finds
// cheaper (search) or dec says. It gives the cost and whether every
// coefficient is zero; the leaves set InterTxSizes.
func (t *tileEnc) varTx(dec *blockDec, row, col, txSz, depth int, search bool, cur *varTxCursor) (float64, bool) {
	f := t.f
	if row >= f.miRows || col >= f.miCols {
		return 0, true
	}
	splits := t.varTxSplits(txSz, depth)
	leaf := func() (float64, bool) {
		j := 0.0
		if splits {
			t.cost.n = 0
			t.writeTxfmSplit(&t.cost, row, col, txSz, false)
			j = t.lambda * float64(t.cost.n) / costScale
		}
		tt := -1
		if !search {
			tt = int(dec.txTypes[cur.typ])
			cur.typ++
		}
		jj, eob, chosen, _ := t.transformBlock(0, col*4, row*4, txSz, tt)
		if search {
			dec.txTypes = append(dec.txTypes, uint8(chosen)) //nolint:gosec // a type
		}
		t.setTxSize(row, col, txSz)
		return j + jj, eob == 0
	}
	split := func() (float64, bool) {
		t.cost.n = 0
		t.writeTxfmSplit(&t.cost, row, col, txSz, true)
		j := t.lambda * float64(t.cost.n) / costScale
		sub := int(splitTxSize[txSz])
		sw, sh := int(txWidth[sub])>>2, int(txHeight[sub])>>2
		zero := true
		for i := 0; i < int(txHeight[txSz])>>2; i += sh {
			for k := 0; k < int(txWidth[txSz])>>2; k += sw {
				jj, z := t.varTx(dec, row+i, col+k, sub, depth+1, search, cur)
				j += jj
				zero = zero && z
			}
		}
		return j, zero
	}
	if !splits {
		return leaf()
	}
	if !search {
		s := dec.txSplit[cur.split]
		cur.split++
		if s {
			return split()
		}
		return leaf()
	}
	// search: whole, then split, the better kept
	if dbgNoVarTx {
		dec.txSplit = append(dec.txSplit, false)
		return leaf()
	}
	bs := txBlockSize[txSz]
	// the tree's own slots, by its depth (the block search holds the
	// others)
	start, whole := t.snap(0, 4+2*depth), t.snap(0, 5+2*depth)
	t.save(start, row, col, bs, true)
	at := len(dec.txSplit)
	dec.txSplit = append(dec.txSplit, false)
	nTypes := len(dec.txTypes)
	j0, z0 := leaf()
	t0 := dec.txTypes[nTypes:]
	t0 = append([]uint8(nil), t0...)
	t.save(whole, row, col, bs, true)
	t.restore(start, true)
	dec.txTypes = dec.txTypes[:nTypes]
	dec.txSplit[at] = true
	j1, z1 := split()
	if j1 < j0 {
		return j1, z1
	}
	dec.txSplit = append(dec.txSplit[:at], false)
	dec.txTypes = append(dec.txTypes[:nTypes], t0...)
	t.restore(whole, true)
	return j0, z0
}

// setTxSize sets InterTxSizes for a transform's mi.
func (t *tileEnc) setTxSize(row, col, txSz int) {
	f := t.f
	for i := range int(txHeight[txSz]) >> 2 {
		for k := range int(txWidth[txSz]) >> 2 {
			if row+i < f.miRows && col+k < f.miCols {
				f.txSize[f.mi(row+i, col+k)] = uint8(txSz) //nolint:gosec // a size
			}
		}
	}
}

// writeVarTx writes an inter block's transform tree's txfm_split
// symbols, setting InterTxSizes as a decoder does.
func (t *tileEnc) writeVarTx(cd coder, dec *blockDec, row, col, txSz, depth int, cur *varTxCursor) {
	f := t.f
	if row >= f.miRows || col >= f.miCols {
		return
	}
	split := false
	if t.varTxSplits(txSz, depth) {
		split = dec.txSplit[cur.split]
		cur.split++
		t.writeTxfmSplit(cd, row, col, txSz, split)
	}
	if !split {
		t.setTxSize(row, col, txSz)
		return
	}
	sub := int(splitTxSize[txSz])
	sw, sh := int(txWidth[sub])>>2, int(txHeight[sub])>>2
	for i := 0; i < int(txHeight[txSz])>>2; i += sh {
		for k := 0; k < int(txWidth[txSz])>>2; k += sw {
			t.writeVarTx(cd, dec, row+i, col+k, sub, depth+1, cur)
		}
	}
}

// needsInterpFilter is needs_interp_filter (global motion IDENTITY).
func (t *tileEnc) needsInterpFilter() bool {
	b := &t.b
	if b.skipMode || b.warp || t.f.fp.interpFilter != SWITCHABLE {
		return false
	}
	large := min(blockWidth[b.size], blockHeight[b.size]) >= 8
	return !large || (b.yMode != GLOBALMV && b.yMode != GLOBAL_GLOBALMV)
}

// writeInterpFilter is interp_filter (one: no dual filter).
func (t *tileEnc) writeInterpFilter(cd coder) {
	f, b := t.f, &t.b
	if !t.needsInterpFilter() {
		return
	}
	ctx := b2i(t.isCompound()) * 4
	lt, at := 3, 3
	if b.availL {
		i := f.mi(b.r, b.c-1)
		if int(f.refFrames[0][i]) == b.refFrame || int(f.refFrames[1][i]) == b.refFrame {
			lt = int(f.interp[i])
		}
	}
	if b.availU {
		i := f.mi(b.r-1, b.c)
		if int(f.refFrames[0][i]) == b.refFrame || int(f.refFrames[1][i]) == b.refFrame {
			at = int(f.interp[i])
		}
	}
	switch {
	case lt == at:
		ctx += lt
	case lt == 3:
		ctx += at
	case at == 3:
		ctx += lt
	default:
		ctx += 3
	}
	cd.symbol(b.interp, t.cdf.interpFilter[ctx][:])
}
