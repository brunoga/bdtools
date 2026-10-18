package av1

// Compound prediction: a block predicted from two references, the two
// averaged, weighted by distance or masked. Its syntax: comp_mode, the
// references (bidirectional or unidirectional), compound_mode and the
// motion vectors of its NEW parts.

// isCompound reports whether the block in t.b is compound.
func (t *tileEnc) isCompound() bool { return t.b.refFrame2 > INTRA_FRAME }

// neighbourRefs are the above and left blocks' references (INTRA_FRAME
// and NONE where unavailable), as inter_frame_mode_info sets them.
func (t *tileEnc) neighbourRefs() (above, left [2]int) {
	f, b := t.f, &t.b
	above, left = [2]int{INTRA_FRAME, NONE}, [2]int{INTRA_FRAME, NONE}
	if b.availU {
		i := f.mi(b.r-1, b.c)
		above = [2]int{int(f.refFrames[0][i]), int(f.refFrames[1][i])}
	}
	if b.availL {
		i := f.mi(b.r, b.c-1)
		left = [2]int{int(f.refFrames[0][i]), int(f.refFrames[1][i])}
	}
	return above, left
}

func checkBackward(ref int) bool { return ref >= BWDREF_FRAME && ref <= ALTREF_FRAME }

func samedirRefPair(a, b int) bool { return (a >= BWDREF_FRAME) == (b >= BWDREF_FRAME) }

// compModeCtx is comp_mode's context.
func (t *tileEnc) compModeCtx() int {
	b := &t.b
	above, left := t.neighbourRefs()
	aIntra, lIntra := above[0] <= INTRA_FRAME, left[0] <= INTRA_FRAME
	aSingle, lSingle := above[1] <= INTRA_FRAME, left[1] <= INTRA_FRAME
	switch {
	case b.availU && b.availL:
		switch {
		case aSingle && lSingle:
			return b2i(checkBackward(above[0]) != checkBackward(left[0]))
		case aSingle:
			return 2 + b2i(checkBackward(above[0]) || aIntra)
		case lSingle:
			return 2 + b2i(checkBackward(left[0]) || lIntra)
		}
		return 4
	case b.availU:
		if aSingle {
			return b2i(checkBackward(above[0]))
		}
		return 3
	case b.availL:
		if lSingle {
			return b2i(checkBackward(left[0]))
		}
		return 3
	}
	return 1
}

// compRefTypeCtx is comp_ref_type's context.
func (t *tileEnc) compRefTypeCtx() int {
	b := &t.b
	above, left := t.neighbourRefs()
	aIntra, lIntra := above[0] <= INTRA_FRAME, left[0] <= INTRA_FRAME
	aSingle, lSingle := above[1] <= INTRA_FRAME, left[1] <= INTRA_FRAME
	aComp := b.availU && !aIntra && !aSingle
	lComp := b.availL && !lIntra && !lSingle
	aUni := aComp && samedirRefPair(above[0], above[1])
	lUni := lComp && samedirRefPair(left[0], left[1])
	switch {
	case b.availU && !aIntra && b.availL && !lIntra:
		same := samedirRefPair(above[0], left[0])
		switch {
		case !aComp && !lComp:
			return 1 + 2*b2i(same)
		case !aComp:
			if !lUni {
				return 1
			}
			return 3 + b2i(same)
		case !lComp:
			if !aUni {
				return 1
			}
			return 3 + b2i(same)
		case !aUni && !lUni:
			return 0
		case !aUni || !lUni:
			return 2
		}
		return 3 + b2i((above[0] == BWDREF_FRAME) == (left[0] == BWDREF_FRAME))
	case b.availU && b.availL:
		switch {
		case aComp:
			return 1 + 2*b2i(aUni)
		case lComp:
			return 1 + 2*b2i(lUni)
		}
		return 2
	case aComp:
		return 4 * b2i(aUni)
	case lComp:
		return 4 * b2i(lUni)
	}
	return 2
}

// writeRefFrames is read_ref_frames: comp_mode when the frame allows
// compound blocks, then the reference or references.
func (t *tileEnc) writeRefFrames(cd coder) {
	b := &t.b
	if t.f.fp.refSelect && min(b.bw4, b.bh4) >= 2 {
		cd.symbol(b2i(t.isCompound()), t.cdf.compMode[t.compModeCtx()][:])
	}
	if !t.isCompound() {
		t.writeRefFrame(cd, b.refFrame)
		return
	}
	cnt := func(refs ...int) int {
		n := 0
		for _, r := range refs {
			n += t.countRefs(r)
		}
		return n
	}
	c := t.cdf
	r0, r1 := b.refFrame, b.refFrame2
	if samedirRefPair(r0, r1) {
		cd.symbol(UNIDIR_COMP_REFERENCE, c.compRefType[t.compRefTypeCtx()][:])
		fwd := cnt(LAST_FRAME, LAST2_FRAME, LAST3_FRAME, GOLDEN_FRAME)
		bwd := cnt(BWDREF_FRAME, ALTREF2_FRAME, ALTREF_FRAME)
		cd.symbol(b2i(r0 == BWDREF_FRAME), c.uniCompRef[refCountCtx(fwd, bwd)][0][:])
		if r0 == BWDREF_FRAME {
			return
		}
		cd.symbol(b2i(r1 != LAST2_FRAME), c.uniCompRef[refCountCtx(cnt(LAST2_FRAME), cnt(LAST3_FRAME, GOLDEN_FRAME))][1][:])
		if r1 != LAST2_FRAME {
			cd.symbol(b2i(r1 == GOLDEN_FRAME), c.uniCompRef[refCountCtx(cnt(LAST3_FRAME), cnt(GOLDEN_FRAME))][2][:])
		}
		return
	}
	cd.symbol(BIDIR_COMP_REFERENCE, c.compRefType[t.compRefTypeCtx()][:])
	high := r0 == LAST3_FRAME || r0 == GOLDEN_FRAME
	cd.symbol(b2i(high), c.compRef[refCountCtx(cnt(LAST_FRAME, LAST2_FRAME), cnt(LAST3_FRAME, GOLDEN_FRAME))][0][:])
	if high {
		cd.symbol(b2i(r0 == GOLDEN_FRAME), c.compRef[refCountCtx(cnt(LAST3_FRAME), cnt(GOLDEN_FRAME))][2][:])
	} else {
		cd.symbol(b2i(r0 == LAST2_FRAME), c.compRef[refCountCtx(cnt(LAST_FRAME), cnt(LAST2_FRAME))][1][:])
	}
	cd.symbol(b2i(r1 == ALTREF_FRAME), c.compBwdRef[refCountCtx(cnt(BWDREF_FRAME, ALTREF2_FRAME), cnt(ALTREF_FRAME))][0][:])
	if r1 != ALTREF_FRAME {
		cd.symbol(b2i(r1 == ALTREF2_FRAME), c.compBwdRef[refCountCtx(cnt(BWDREF_FRAME), cnt(ALTREF2_FRAME))][1][:])
	}
}

// writeCompoundModes writes a compound block's compound_mode, drl_mode
// and motion vector differences.
func (t *tileEnc) writeCompoundModes(cd coder) {
	b, s := &t.b, &t.stack
	cd.symbol(b.yMode-NEAREST_NEARESTMV, t.cdf.compoundMode[s.compoundModeCtx()][:])
	ctx, val, n := s.drlModes(b.yMode, b.refMvIdx)
	for k := range n {
		cd.symbol(val[k], t.cdf.drlMode[ctx[k]][:])
	}
	for i, m := range [2]mv{b.mv, b.mv2} {
		if getMode(b.yMode, i) == NEWMV {
			p := s.predMv(b.yMode, b.refMvIdx, i)
			t.writeMvDiff(cd, mv{m.row - p.row, m.col - p.col})
		}
	}
}

// compoundCands adds a pair of references' compound candidates:
// NEAREST_NEARESTMV, NEAR_NEARMV with each RefMvIdx, GLOBAL_GLOBALMV and
// NEW_NEWMV with each reference's own NEWMV vector.
func (t *tileEnc) compoundCands(r0, r1 int, new0, new1 mv, cands []interCand) []interCand {
	s := &t.stack
	t.findMvStack(s, true, [2]int{r0, r1})
	add := func(mode, idx int, m0, m1 mv) {
		cands = append(cands, interCand{ref: r0, ref2: r1, mode: mode, idx: idx, m: m0, m2: m1})
	}
	add(NEAREST_NEARESTMV, 0, s.predMv(NEAREST_NEARESTMV, 0, 0), s.predMv(NEAREST_NEARESTMV, 0, 1))
	if lo, hi := s.refMvIdxRange(NEAR_NEARMV); s.numMvFound > 1 {
		for idx := lo; idx <= hi; idx++ {
			add(NEAR_NEARMV, idx, s.predMv(NEAR_NEARMV, idx, 0), s.predMv(NEAR_NEARMV, idx, 1))
		}
	}
	add(GLOBAL_GLOBALMV, 0, s.predMv(GLOBAL_GLOBALMV, 0, 0), s.predMv(GLOBAL_GLOBALMV, 0, 1))
	lo, hi := s.refMvIdxRange(NEW_NEWMV)
	best, bestBits := lo, int64(-1)
	for idx := lo; idx <= hi; idx++ {
		t.cost.n = 0
		ctx, val, n := s.drlModes(NEW_NEWMV, idx)
		for k := range n {
			t.cost.symbol(val[k], t.cdf.drlMode[ctx[k]][:])
		}
		for i, m := range [2]mv{new0, new1} {
			p := s.predMv(NEW_NEWMV, idx, i)
			t.writeMvDiff(&t.cost, mv{m.row - p.row, m.col - p.col})
		}
		if bestBits < 0 || t.cost.n < bestBits {
			best, bestBits = idx, t.cost.n
		}
	}
	add(NEW_NEWMV, best, new0, new1)
	// one NEW vector: the other the stack's
	add(NEAREST_NEWMV, 0, s.predMv(NEAREST_NEWMV, 0, 0), new1)
	add(NEW_NEARESTMV, 0, new0, s.predMv(NEW_NEARESTMV, 0, 1))
	if lo, hi := s.refMvIdxRange(NEAR_NEWMV); s.numMvFound > 1 {
		for _, mode := range []int{NEAR_NEWMV, NEW_NEARMV} {
			best, bestBits := lo, int64(-1)
			for idx := lo; idx <= hi; idx++ {
				t.cost.n = 0
				ctx, val, n := s.drlModes(mode, idx)
				for k := range n {
					t.cost.symbol(val[k], t.cdf.drlMode[ctx[k]][:])
				}
				for i, m := range [2]mv{new0, new1} {
					if getMode(mode, i) == NEWMV {
						p := s.predMv(mode, idx, i)
						t.writeMvDiff(&t.cost, mv{m.row - p.row, m.col - p.col})
					}
				}
				if bestBits < 0 || t.cost.n < bestBits {
					best, bestBits = idx, t.cost.n
				}
			}
			m0, m1 := new0, new1
			if getMode(mode, 0) != NEWMV {
				m0 = s.predMv(mode, best, 0)
			}
			if getMode(mode, 1) != NEWMV {
				m1 = s.predMv(mode, best, 1)
			}
			add(mode, best, m0, m1)
		}
	}
	return cands
}

// dbgNoJntComp turns distance weighted compound (enable_jnt_comp) off:
// off by default, as it gained nothing (+0.1% on the 120-picture clips,
// 5% more time); dbgForceCompDist weights every compound block's
// predictions by distance (for tests).
var dbgNoJntComp, dbgForceCompDist = true, false

// writeCompoundIdx writes a compound block's compound_idx (read_compound_type
// with masked compound off).
func (t *tileEnc) writeCompoundIdx(cd coder) {
	f, b := t.f, &t.b
	if !f.seq.jntComp || b.skipMode {
		return
	}
	fwd := abs(relativeDist(f.fp.refHints[b.refFrame-LAST_FRAME], f.fp.orderHint))
	bck := abs(relativeDist(f.fp.refHints[b.refFrame2-LAST_FRAME], f.fp.orderHint))
	ctx := 0
	if fwd == bck {
		ctx = 3
	}
	for _, n := range [2]struct {
		avail bool
		r, c  int
	}{{b.availU, b.r - 1, b.c}, {b.availL, b.r, b.c - 1}} {
		if !n.avail {
			continue
		}
		i := f.mi(n.r, n.c)
		if f.refFrames[1][i] > INTRA_FRAME {
			ctx += int(f.compIdx[i])
		} else if f.refFrames[0][i] == ALTREF_FRAME {
			ctx++
		}
	}
	cd.symbol(b2i(!b.compDist), t.cdf.compoundIdx[ctx][:])
}

// distWeights are the distance weights process's FwdWeight and BckWeight
// for a compound block's references.
func (f *frameParams) distWeights(ref [2]int) [2]int32 {
	var dist [2]int
	for k := range 2 {
		dist[k] = min(abs(relativeDist(f.refHints[ref[k]-LAST_FRAME], f.orderHint)), MAX_FRAME_DISTANCE)
	}
	d0, d1 := dist[1], dist[0]
	order := b2i(d0 <= d1)
	if d0 == 0 || d1 == 0 {
		return [2]int32{int32(quantDistLookup[3][order]), int32(quantDistLookup[3][1-order])}
	}
	i := 0
	for ; i < 3; i++ {
		c0, c1 := int(quantDistWeight[i][order]), int(quantDistWeight[i][1-order])
		if order == 1 {
			if d0*c0 > d1*c1 {
				break
			}
		} else if d0*c0 < d1*c1 {
			break
		}
	}
	return [2]int32{int32(quantDistLookup[i][order]), int32(quantDistLookup[i][1-order])}
}
