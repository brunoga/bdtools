package hevc

// predScratch holds a prediction block's two lists' 14-bit samples.
type predScratch struct {
	l    [2][64 * 64]int16
	tmp  [(64 + 7) * 64]int16
	edge [(64 + 7) * (64 + 7)]uint16
}

const (
	predL0 = 0
	predL1 = 1
	predBi = 2
)

// predictionUnit parses a prediction unit (7.3.8.6), derives its motion
// and predicts it.
func (sd *sliceDec) predictionUnit(x0, y0, w, h, partIdx int, skip bool) error {
	c, hd := &sd.c, sd.h
	mergeIdx := 0
	merge := skip
	if !skip {
		merge = c.decision(ctxMergeFlag) == 1
	}
	var f mvField
	if merge {
		if hd.maxMergeCand > 1 {
			if c.decision(ctxMergeIdx) == 1 {
				mergeIdx = 1
				for mergeIdx < hd.maxMergeCand-1 && c.bypass() == 1 {
					mergeIdx++
				}
			}
		}
		if partIdx == 0 {
			sd.mergeFlag = true
		}
		f = sd.mergeCandidate(x0, y0, w, h, partIdx, mergeIdx)
	} else {
		if partIdx == 0 {
			sd.mergeFlag = false
		}
		idc := predL0
		if hd.typ == sliceB {
			if w+h == 12 {
				idc = c.decision(ctxInterPredIdc + 4)
			} else if c.decision(ctxInterPredIdc+sd.ctDepthAt(x0, y0)) == 1 {
				idc = predBi
			} else {
				idc = c.decision(ctxInterPredIdc + 4)
			}
		}
		var mvd [2]mv
		var mvpFlag [2]int
		for l := range 2 {
			if l == 0 && idc == predL1 || l == 1 && idc == predL0 {
				f.refIdx[l] = -1
				continue
			}
			ref := 0
			if hd.numRefIdx[l] > 1 {
				maxCtx := min(hd.numRefIdx[l]-1, 2)
				for ref < maxCtx && c.decision(ctxRefIdxL0+ref) == 1 {
					ref++
				}
				if ref == 2 {
					for ref < hd.numRefIdx[l]-1 && c.bypass() == 1 {
						ref++
					}
				}
			}
			f.refIdx[l] = int8(ref)
			if l == 1 && hd.mvdL1Zero && idc == predBi {
				mvd[1] = mv{}
			} else {
				mvd[l] = sd.mvdCoding()
			}
			mvpFlag[l] = c.decision(ctxMvpLxFlag)
			f.pred |= 1 << l
		}
		for l := range 2 {
			if f.pred&(1<<l) == 0 {
				continue
			}
			mvp := sd.amvp(x0, y0, w, h, partIdx, l, int(f.refIdx[l]), mvpFlag[l])
			f.mv[l] = mv{int16(int32(mvp.x) + int32(mvd[l].x)), int16(int32(mvp.y) + int32(mvd[l].y))}
		}
	}
	if f.pred&1 == 0 {
		f.refIdx[0] = -1
	}
	if f.pred&2 == 0 {
		f.refIdx[1] = -1
	}
	// Store the motion, and the PU's edges for the deblocking.
	s, ps := sd.s, sd.ps
	pic := sd.pic
	for y := y0; y < y0+h; y += 4 {
		for x := x0; x < x0+w; x += 4 {
			pic.mvf[(y>>2)*pic.mvfW+x>>2] = f
		}
	}
	for y := y0; y < y0+h && y < s.height; y += 4 {
		ps.tuEdgeV[sd.blk4(x0, y)] |= 2
	}
	for x := x0; x < x0+w && x < s.width; x += 4 {
		ps.tuEdgeH[sd.blk4(x, y0)] |= 2
	}
	sd.motionCompensate(x0, y0, w, h, &f)
	return nil
}

func (sd *sliceDec) ctDepthAt(x, y int) int {
	s := sd.s
	return int(sd.ps.ctDepth[(y>>s.log2MinCb)*s.minCbW+x>>s.log2MinCb])
}

func (sd *sliceDec) mvdCoding() mv {
	c := &sd.c
	g0x := c.decision(ctxAbsMvdGreater0Flag)
	g0y := c.decision(ctxAbsMvdGreater0Flag)
	g1x, g1y := 0, 0
	if g0x == 1 {
		g1x = c.decision(ctxAbsMvdGreater1Flag + 1)
	}
	if g0y == 1 {
		g1y = c.decision(ctxAbsMvdGreater1Flag + 1)
	}
	comp := func(g0, g1 int) int16 {
		if g0 == 0 {
			return 0
		}
		v := 1
		if g1 == 1 {
			// abs_mvd_minus2: first order Exp-Golomb.
			v = 2
			k := 1
			for k < 31 && c.bypass() == 1 {
				v += 1 << k
				k++
			}
			v += c.bypassBits(k)
		}
		if c.bypass() == 1 {
			v = -v
		}
		return int16(v)
	}
	x := comp(g0x, g1x)
	y := comp(g0y, g1y)
	return mv{x, y}
}

// mvAt is the motion of the current picture's block at (x, y).
func (sd *sliceDec) mvAt(x, y int) *mvField {
	return &sd.pic.mvf[(y>>2)*sd.pic.mvfW+x>>2]
}

// pbAvailable is 6.4.2: a neighbour prediction block's availability.
func (sd *sliceDec) pbAvailable(xPb, yPb, w, h, partIdx, xN, yN int) bool {
	xCb, yCb, nCb := sd.cuX, sd.cuY, 1<<sd.cuLog2
	sameCb := xCb <= xN && yCb <= yN && xCb+nCb > xN && yCb+nCb > yN
	var ok bool
	if !sameCb {
		ok = sd.available(xPb, yPb, xN, yN)
	} else {
		ok = w<<1 != nCb || h<<1 != nCb || partIdx != 1 || yCb+h > yN || xCb+w <= xN
	}
	return ok && sd.ps.predMode[sd.blk4(xN, yN)] != modeIntra
}

func sameMotion(a, b *mvField) bool {
	return a.pred == b.pred && (a.pred&1 == 0 || a.mv[0] == b.mv[0] && a.refIdx[0] == b.refIdx[0]) &&
		(a.pred&2 == 0 || a.mv[1] == b.mv[1] && a.refIdx[1] == b.refIdx[1])
}

// mergeCandidate derives the motion of merge candidate idx (8.5.3.2.2).
func (sd *sliceDec) mergeCandidate(xP, yP, w, h, partIdx, idx int) mvField {
	p, hd := sd.p, sd.h
	origW, origH := w, h
	if p.log2ParMrgLevel > 2 && sd.cuLog2 == 3 {
		xP, yP, w, h, partIdx = sd.cuX, sd.cuY, 8, 8, 0
	}
	pm := sd.partMode
	if origW == w && origH == h && xP == sd.cuX && yP == sd.cuY && w == 1<<sd.cuLog2 && h == 1<<sd.cuLog2 {
		pm = part2Nx2N // a whole CU (or the shared list): no partition rule
	}
	lvl := p.log2ParMrgLevel
	samePar := func(xN, yN int) bool { return xP>>lvl == xN>>lvl && yP>>lvl == yN>>lvl }
	var cands [5]mvField
	n := 0
	// Each neighbour's availability (with the partition and parallel
	// merge exclusions); a candidate is left out when it repeats an
	// available neighbour's motion, whether or not that one was kept.
	get := func(xN, yN int) (*mvField, bool) {
		if samePar(xN, yN) || !sd.pbAvailable(xP, yP, w, h, partIdx, xN, yN) {
			return nil, false
		}
		return sd.mvAt(xN, yN), true
	}
	var a1, b1 *mvField
	okA1, okB1 := false, false
	if partIdx != 1 || pm != partNx2N && pm != partnLx2N && pm != partnRx2N {
		a1, okA1 = get(xP-1, yP+h-1)
	}
	if okA1 {
		cands[n] = *a1
		n++
	}
	if n > idx {
		return sd.finishMerge(cands[idx], origW, origH)
	}
	if partIdx != 1 || pm != part2NxN && pm != part2NxnU && pm != part2NxnD {
		b1, okB1 = get(xP+w-1, yP-1)
	}
	if okB1 && (!okA1 || !sameMotion(a1, b1)) {
		cands[n] = *b1
		n++
	}
	if n > idx {
		return sd.finishMerge(cands[idx], origW, origH)
	}
	if b0, ok := get(xP+w, yP-1); ok && (!okB1 || !sameMotion(b1, b0)) {
		cands[n] = *b0
		n++
	}
	if n > idx {
		return sd.finishMerge(cands[idx], origW, origH)
	}
	if a0, ok := get(xP-1, yP+h); ok && (!okA1 || !sameMotion(a1, a0)) {
		cands[n] = *a0
		n++
	}
	if n > idx {
		return sd.finishMerge(cands[idx], origW, origH)
	}
	if b2, ok := get(xP-1, yP-1); ok && (!okA1 || !sameMotion(a1, b2)) && (!okB1 || !sameMotion(b1, b2)) && n != 4 {
		cands[n] = *b2
		n++
	}
	if n > idx {
		return sd.finishMerge(cands[idx], origW, origH)
	}
	list := make([]mvField, 0, 5)
	list = append(list, cands[:n]...)
	// The temporal candidate.
	if hd.temporalMVP && len(list) < hd.maxMergeCand {
		var col mvField
		if v, ok := sd.temporalMV(xP, yP, w, h, 0, 0); ok {
			col.mv[0], col.pred = v, 1
		}
		if hd.typ == sliceB {
			if v, ok := sd.temporalMV(xP, yP, w, h, 1, 0); ok {
				col.mv[1] = v
				col.pred |= 2
			}
		}
		if col.pred != 0 {
			list = append(list, col)
		}
	}
	if len(list) > idx {
		return sd.finishMerge(list[idx], origW, origH)
	}
	// Combined bi-predictive candidates.
	numOrig := len(list)
	if hd.typ == sliceB && numOrig > 1 && numOrig < hd.maxMergeCand {
		order := [12][2]int{{0, 1}, {1, 0}, {0, 2}, {2, 0}, {1, 2}, {2, 1}, {0, 3}, {3, 0}, {1, 3}, {3, 1}, {2, 3}, {3, 2}}
		for _, o := range order[:numOrig*(numOrig-1)] {
			if len(list) >= hd.maxMergeCand {
				break
			}
			l0, l1 := &list[o[0]], &list[o[1]]
			if l0.pred&1 != 0 && l1.pred&2 != 0 {
				p0 := sd.refList[0][l0.refIdx[0]]
				p1 := sd.refList[1][l1.refIdx[1]]
				if p0 != p1 || l0.mv[0] != l1.mv[1] {
					list = append(list, mvField{
						mv: [2]mv{l0.mv[0], l1.mv[1]}, refIdx: [2]int8{l0.refIdx[0], l1.refIdx[1]}, pred: 3,
					})
				}
			}
		}
	}
	// Zero candidates.
	numRef := hd.numRefIdx[0]
	if hd.typ == sliceB {
		numRef = min(hd.numRefIdx[0], hd.numRefIdx[1])
	}
	for zero := 0; len(list) <= idx; zero++ {
		r := int8(0)
		if zero < numRef {
			r = int8(zero)
		}
		z := mvField{refIdx: [2]int8{r, -1}, pred: 1}
		if hd.typ == sliceB {
			z.refIdx[1] = r
			z.pred = 3
		}
		list = append(list, z)
	}
	return sd.finishMerge(list[idx], origW, origH)
}

// finishMerge applies the restriction of 8x4 and 4x8 blocks to one list.
func (sd *sliceDec) finishMerge(f mvField, w, h int) mvField {
	if f.pred == 3 && w+h == 12 {
		f.pred = 1
		f.refIdx[1] = -1
	}
	return f
}

func clip3(lo, hi, v int) int { return min(max(v, lo), hi) }

// scaleMV scales a vector by POC distances (8-180 to 8-183).
func scaleMV(v mv, td, tb int) mv {
	td = clip3(-128, 127, td)
	tb = clip3(-128, 127, tb)
	tx := (16384 + iabs(td)>>1) / td
	f := clip3(-4096, 4095, (tb*tx+32)>>6)
	sc := func(c int16) int16 {
		p := f * int(c)
		s := 1
		if p < 0 {
			s = -1
		}
		return int16(clip3(-32768, 32767, s*((iabs(p)+127)>>8)))
	}
	return mv{sc(v.x), sc(v.y)}
}

// temporalMV derives the collocated vector for list l and refIdx
// (8.5.3.2.8).
func (sd *sliceDec) temporalMV(xP, yP, w, h, l, refIdx int) (mv, bool) {
	hd, s := sd.h, sd.s
	colList := 0
	if hd.typ == sliceB && !hd.colFromL0 {
		colList = 1
	}
	if hd.colRefIdx >= len(sd.refList[colList]) {
		return mv{}, false
	}
	col := sd.refList[colList][hd.colRefIdx]
	xBr, yBr := xP+w, yP+h
	if yP>>s.log2Ctb == yBr>>s.log2Ctb && yBr < s.height && xBr < s.width {
		if v, ok := sd.colMV(col, (xBr>>4)<<4, (yBr>>4)<<4, l, refIdx); ok {
			return v, true
		}
	}
	xC, yC := xP+w>>1, yP+h>>1
	return sd.colMV(col, (xC>>4)<<4, (yC>>4)<<4, l, refIdx)
}

// colMV is 8.5.3.2.9: the collocated block's vector, scaled.
func (sd *sliceDec) colMV(col *picture, x, y, l, refIdx int) (mv, bool) {
	if col.corrupt || len(col.refPOC) == 0 {
		return mv{}, false
	}
	col.waitLines(y, y)
	f := &col.mvf[(y>>2)*col.mvfW+x>>2]
	if f.pred == 0 {
		return mv{}, false
	}
	var listCol int
	switch {
	case f.pred&1 == 0:
		listCol = 1
	case f.pred == 1:
		listCol = 0
	default:
		if sd.noBackwardPred() {
			listCol = l
		} else {
			listCol = boolInt(sd.h.colFromL0) // N = collocated_from_l0_flag
		}
	}
	sliceIdx := col.ctbSlice[(y>>col.sps.log2Ctb)*col.sps.ctbW+x>>col.sps.log2Ctb]
	if int(sliceIdx) >= len(col.refPOC) || sliceIdx < 0 {
		return mv{}, false
	}
	ri := f.refIdx[listCol]
	colRefPOC := int(col.refPOC[sliceIdx][listCol][ri])
	colRefLT := col.refLT[sliceIdx][listCol][ri]
	curLT := sd.refIsLT[l][refIdx]
	if colRefLT != curLT {
		return mv{}, false
	}
	v := f.mv[listCol]
	colDiff := col.poc - colRefPOC
	curDiff := sd.pic.poc - sd.refList[l][refIdx].poc
	if curLT || colDiff == curDiff || colDiff == 0 {
		return v, true
	}
	return scaleMV(v, colDiff, curDiff), true
}

// noBackwardPred reports whether no reference follows the current picture
// in output order.
func (sd *sliceDec) noBackwardPred() bool {
	for l := range 2 {
		for i := range sd.h.numRefIdx[l] {
			if sd.refList[l][i].poc > sd.pic.poc {
				return false
			}
		}
	}
	return true
}

// amvp derives the predictor of list l's vector (8.5.3.2.6, 8.5.3.2.7).
func (sd *sliceDec) amvp(xP, yP, w, h, partIdx, l, refIdx, flag int) mv {
	target := sd.refList[l][refIdx]
	targetLT := sd.refIsLT[l][refIdx]
	cur := sd.pic.poc
	avail := func(xN, yN int) (*mvField, bool) {
		if !sd.pbAvailable(xP, yP, w, h, partIdx, xN, yN) {
			return nil, false
		}
		return sd.mvAt(xN, yN), true
	}
	// sameRef: the neighbour's vector for the same picture, in list l or
	// the other.
	sameRef := func(f *mvField) (mv, bool) {
		for _, k := range [2]int{l, 1 - l} {
			if f.pred&(1<<k) != 0 && sd.refList[k][f.refIdx[k]] == target {
				return f.mv[k], true
			}
		}
		return mv{}, false
	}
	// otherRef: a vector for a reference of the same kind (long or short
	// term), scaled to the target when short-term.
	otherRef := func(f *mvField) (mv, bool) {
		for _, k := range [2]int{l, 1 - l} {
			if f.pred&(1<<k) == 0 {
				continue
			}
			lt := sd.refIsLT[k][f.refIdx[k]]
			if lt != targetLT {
				continue
			}
			v := f.mv[k]
			ref := sd.refList[k][f.refIdx[k]]
			if !lt && !targetLT && ref.poc != target.poc {
				v = scaleMV(v, cur-ref.poc, cur-target.poc)
			}
			return v, true
		}
		return mv{}, false
	}
	a0, okA0 := avail(xP-1, yP+h)
	a1, okA1 := avail(xP-1, yP+h-1)
	isScaled := okA0 || okA1
	var mvA mv
	availA := false
	for _, n := range []struct {
		f  *mvField
		ok bool
	}{{a0, okA0}, {a1, okA1}} {
		if n.ok && !availA {
			mvA, availA = sameRef(n.f)
		}
	}
	if !availA {
		for _, n := range []struct {
			f  *mvField
			ok bool
		}{{a0, okA0}, {a1, okA1}} {
			if n.ok && !availA {
				mvA, availA = otherRef(n.f)
			}
		}
	}
	b0, okB0 := avail(xP+w, yP-1)
	b1, okB1 := avail(xP+w-1, yP-1)
	b2, okB2 := avail(xP-1, yP-1)
	bs := []struct {
		f  *mvField
		ok bool
	}{{b0, okB0}, {b1, okB1}, {b2, okB2}}
	var mvB mv
	availB := false
	for _, n := range bs {
		if n.ok && !availB {
			mvB, availB = sameRef(n.f)
		}
	}
	if !isScaled && availB {
		mvA, availA = mvB, true
	}
	if !isScaled {
		availB = false
		for _, n := range bs {
			if n.ok && !availB {
				mvB, availB = otherRef(n.f)
			}
		}
	}
	var list [3]mv
	n := 0
	if availA {
		list[n] = mvA
		n++
	}
	if availB && (!availA || mvA != mvB) {
		list[n] = mvB
		n++
	}
	if n < 2 && sd.h.temporalMVP {
		if v, ok := sd.temporalMV(xP, yP, w, h, l, refIdx); ok {
			list[n] = v
			n++
		}
	}
	if flag < n {
		return list[flag]
	}
	return mv{}
}

// motionCompensate predicts the block from its references (8.5.3.3).
func (sd *sliceDec) motionCompensate(x0, y0, w, h int, f *mvField) {
	s, p, hd := sd.s, sd.p, sd.h
	weighted := hd.typ == sliceP && p.weightedPred || hd.typ == sliceB && p.weightedBipred
	for ci := range 3 {
		bx, by, bw, bh := x0, y0, w, h
		depth := s.bitDepth
		if ci > 0 {
			bx, by, bw, bh = x0/2, y0/2, w/2, h/2
			depth = s.bitDepthC
		}
		for l := range 2 {
			if f.pred&(1<<l) == 0 {
				continue
			}
			ref := sd.refList[l][f.refIdx[l]]
			sd.interpolate(ref, ci, bx, by, bw, bh, f.mv[l], sd.pred.l[l][:bw*bh], depth)
		}
		pl, stride := sd.pic.y, sd.pic.strideY
		if ci == 1 {
			pl, stride = sd.pic.cb, sd.pic.strideC
		} else if ci == 2 {
			pl, stride = sd.pic.cr, sd.pic.strideC
		}
		maxV := 1<<depth - 1
		shift1 := 14 - depth
		out := pl[by*stride+bx:]
		l0, l1 := sd.pred.l[0][:bw*bh], sd.pred.l[1][:bw*bh]
		if !weighted {
			switch f.pred {
			case 3:
				putBlock(out, stride, l0, l1, bw, bw, bh, 1, 1, 1<<shift1, uint(shift1+1), 0, maxV)
			case 1:
				putBlock(out, stride, l0, l0, bw, bw, bh, 1, 0, 1<<(shift1-1), uint(shift1), 0, maxV)
			default:
				putBlock(out, stride, l1, l1, bw, bw, bh, 1, 0, 1<<(shift1-1), uint(shift1), 0, maxV)
			}
			continue
		}
		// Explicit weighted prediction (8.5.3.3.4.3).
		log2WD := hd.log2WeightDenom + shift1
		if ci > 0 {
			log2WD = hd.log2WeightDenomC + shift1
		}
		wo := func(l int) (int, int) {
			w := &hd.weights[l][f.refIdx[l]]
			if ci == 0 {
				return w.lumaWeight, w.lumaOffset
			}
			return w.chromaWeight[ci-1], w.chromaOffset[ci-1]
		}
		if f.pred == 3 {
			w0, o0 := wo(0)
			w1, o1 := wo(1)
			putBlock(out, stride, l0, l1, bw, bw, bh, w0, w1, (o0+o1+1)<<log2WD, uint(log2WD+1), 0, maxV)
			continue
		}
		l, a := 0, l0
		if f.pred == 2 {
			l, a = 1, l1
		}
		w0, o0 := wo(l)
		if log2WD >= 1 {
			putBlock(out, stride, a, a, bw, bw, bh, w0, 0, 1<<(log2WD-1), uint(log2WD), o0, maxV)
		} else {
			putBlock(out, stride, a, a, bw, bw, bh, w0, 0, 0, 0, o0, maxV)
		}
	}
}

// interpolate fills dst (w x h, 14-bit) with component ci's prediction
// from ref at the block (x0, y0) moved by v (8.5.3.3.3).
func (sd *sliceDec) interpolate(ref *picture, ci, x0, y0, w, h int, v mv, dst []int16, depth int) {
	s := sd.s
	pl, stride := ref.y, ref.strideY
	pw, ph := s.width, s.height
	var xFrac, yFrac, xInt, yInt int
	taps := 8
	if ci == 0 {
		xFrac, yFrac = int(v.x)&3, int(v.y)&3
		xInt, yInt = x0+int(v.x)>>2, y0+int(v.y)>>2
	} else {
		pl, stride = ref.cb, ref.strideC
		if ci == 2 {
			pl = ref.cr
		}
		pw, ph = pw/2, ph/2
		xFrac, yFrac = int(v.x)&7, int(v.y)&7
		xInt, yInt = x0+int(v.x)>>3, y0+int(v.y)>>3
		taps = 4
	}
	half := taps/2 - 1
	// The rows read, final (frame threading).
	if ci == 0 {
		ref.waitLines(yInt-half, yInt-half+h+taps-1)
	} else {
		ref.waitLines((yInt-half)*2, (yInt-half+h+taps-1)*2+1)
	}
	// The source area with the filters' margins: in place when inside
	// the picture, else copied with its edges repeated.
	sw, sh := w+taps-1, h+taps-1
	sx, sy := xInt-half, yInt-half
	src, sstride := pl, stride
	off := sy*stride + sx
	if sx < 0 || sy < 0 || sx+sw > pw || sy+sh > ph {
		buf := sd.pred.edge[:sw*sh]
		for y := range sh {
			row := pl[clip3(0, ph-1, sy+y)*stride:]
			for x := range sw {
				buf[y*sw+x] = row[clip3(0, pw-1, sx+x)]
			}
		}
		src, sstride, off = buf, sw, 0
	}
	if xFrac == 0 && yFrac == 0 {
		copyBlock(dst, w, w, h, src[off+half*sstride+half:], sstride, uint(14-depth))
		return
	}
	shift1 := uint(depth - 8)
	var cx, cy *tapPairs
	pairs := taps / 2
	if taps == 8 {
		cx, cy = &lumaPairs[xFrac], &lumaPairs[yFrac]
	} else {
		cx, cy = &chromaPairs[xFrac], &chromaPairs[yFrac]
	}
	switch {
	case yFrac == 0:
		hFilter(dst, w, w, h, src[off+half*sstride:], sstride, cx, pairs, shift1)
	case xFrac == 0:
		vFilter(dst, w, w, h, src[off+half:], sstride, cy, pairs, shift1)
	default:
		tmp := sd.pred.tmp[:sh*w]
		hFilter(tmp, w, w, sh, src[off:], sstride, cx, pairs, shift1)
		vFilter16(dst, w, w, h, tmp, w, cy, pairs)
	}
}

// The interpolation filters' rows: dst[x] from src[x .. x+taps-1].
