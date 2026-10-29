package av1

// Motion vector prediction (spec 7.10.2): the stack of candidate motion
// vectors find_mv_stack builds from the neighbours of a block, the
// contexts inter_block_mode_info codes its modes with, and the predicted
// motion vector assign_mv takes from the stack (spec 5.11.26).
//
// Everything here mirrors the spec process by process (the comments name
// the subsections); a decoder rebuilds the same stack from the same
// neighbours, so any difference misparses the stream.

// interFrameInfo is what motion vector prediction needs of the frame
// header, named after the spec. Arrays by reference frame are indexed by
// LAST_FRAME..ALTREF_FRAME (INTRA_FRAME's entry unused).
type interFrameInfo struct {
	allowHighPrecisionMv bool
	forceIntegerMv       bool
	useRefFrameMvs       bool
	enableOrderHint      bool
	orderHintBits        int
	orderHint            int                       // OrderHint, the frame's
	orderHints           [TOTAL_REFS_PER_FRAME]int // OrderHints, the references'
	refFrameSignBias     [TOTAL_REFS_PER_FRAME]int
	gmType               [TOTAL_REFS_PER_FRAME]int
	gmParams             [TOTAL_REFS_PER_FRAME][6]int32
	// motionFieldMvs is MotionFieldMvs[ref][y8][x8] (at y8*(miCols>>1)+x8),
	// what the motion field estimation process (7.9) gives: used only when
	// useRefFrameMvs is set, and that process is not ported, so it must
	// be filled by whoever sets the flag.
	motionFieldMvs [TOTAL_REFS_PER_FRAME][]mv
}

// newInterFrameInfo is an interFrameInfo with every reference's global
// motion IDENTITY (gm_params the identity matrix, as setup_past_independence
// and a gm_type of IDENTITY leave them).
func newInterFrameInfo() *interFrameInfo {
	fi := &interFrameInfo{}
	for ref := LAST_FRAME; ref <= ALTREF_FRAME; ref++ {
		fi.gmType[ref] = IDENTITY
		for i := range 6 {
			if i%3 == 2 {
				fi.gmParams[ref][i] = 1 << WARPEDMODEL_PREC_BITS
			}
		}
	}
	return fi
}

// getRelativeDist is get_relative_dist.
func (fi *interFrameInfo) getRelativeDist(a, b int) int {
	if !fi.enableOrderHint {
		return 0
	}
	diff := a - b
	m := 1 << uint(fi.orderHintBits-1)
	return (diff & (m - 1)) - (diff & m)
}

// setRefFrameSignBias sets RefFrameSignBias from OrderHints and
// OrderHint, as uncompressed_header does.
func (fi *interFrameInfo) setRefFrameSignBias() {
	for ref := LAST_FRAME; ref <= ALTREF_FRAME; ref++ {
		fi.refFrameSignBias[ref] = 0
		if fi.enableOrderHint && fi.getRelativeDist(fi.orderHints[ref], fi.orderHint) > 0 {
			fi.refFrameSignBias[ref] = 1
		}
	}
}

// mvStack is find_mv_stack's outputs (and its working state), named
// after the spec. Only the first numMvFound entries of refStackMv and
// weightStack are candidates; for single prediction entries numMvFound..1
// also hold the global motion vector the extra search pads with.
type mvStack struct {
	numMvFound    int
	newMvCount    int
	refStackMv    [MAX_REF_MV_STACK_SIZE][2]mv
	weightStack   [MAX_REF_MV_STACK_SIZE]int
	globalMvs     [2]mv
	newMvContext  int
	refMvContext  int
	zeroMvContext int
	drlCtxStack   [MAX_REF_MV_STACK_SIZE]int

	// The inputs, and the state the processes share.
	refFrame                   [2]int
	isCompound                 bool
	foundMatch                 bool
	closeMatches, totalMatches int
	refIDCount, refDiffCount   [2]int
	refIDMvs, refDiffMvs       [2][2]mv
}

// findMvStack is the find MV stack process (7.10.2.2) for the block in
// t.b with references refFrame (RefFrame; refFrame[1] NONE, or ignored,
// for single prediction), filling s. The neighbours are what the frame's
// mi arrays hold; the above-right candidate is taken only where it has
// been coded (t.decoded, the luma's BlockDecoded flags, which must be
// up to date for every block coded before this one).
func (t *tileEnc) findMvStack(s *mvStack, isCompound bool, refFrame [2]int) {
	b := &t.b
	s.refFrame, s.isCompound = refFrame, isCompound
	bw4, bh4 := b.bw4, b.bh4

	s.numMvFound = 0
	s.newMvCount = 0
	s.globalMvs[0] = t.setupGlobalMv(s, 0)
	s.globalMvs[1] = mv{}
	if isCompound {
		s.globalMvs[1] = t.setupGlobalMv(s, 1)
	}
	s.foundMatch = false

	// The nearest neighbours: the row above, the column to the left and
	// the above-right corner.
	t.scanRow(s, -1)
	foundAboveMatch := s.foundMatch
	s.foundMatch = false
	t.scanCol(s, -1)
	foundLeftMatch := s.foundMatch
	s.foundMatch = false
	if max(bw4, bh4) <= 16 {
		t.scanPoint(s, -1, bw4)
	}
	if s.foundMatch {
		foundAboveMatch = true
	}
	s.closeMatches = b2i(foundAboveMatch) + b2i(foundLeftMatch)
	numNearest := s.numMvFound
	numNew := s.newMvCount
	for idx := range numNearest {
		s.weightStack[idx] += REF_CAT_LEVEL
	}
	s.zeroMvContext = 0
	if t.f.inter.useRefFrameMvs {
		if t.f.inter.motionFieldMvs[refFrame[0]] == nil {
			panic("av1: use_ref_frame_mvs without MotionFieldMvs (motion field estimation is not ported)")
		}
		t.temporalScan(s)
	}

	// The outer neighbours: the above-left corner, then rows and columns
	// further away.
	t.scanPoint(s, -1, -1)
	if s.foundMatch {
		foundAboveMatch = true
	}
	s.foundMatch = false
	t.scanRow(s, -3)
	if s.foundMatch {
		foundAboveMatch = true
	}
	s.foundMatch = false
	t.scanCol(s, -3)
	if s.foundMatch {
		foundLeftMatch = true
	}
	s.foundMatch = false
	if bh4 > 1 {
		t.scanRow(s, -5)
	}
	if s.foundMatch {
		foundAboveMatch = true
	}
	s.foundMatch = false
	if bw4 > 1 {
		t.scanCol(s, -5)
	}
	if s.foundMatch {
		foundLeftMatch = true
	}
	s.totalMatches = b2i(foundAboveMatch) + b2i(foundLeftMatch)

	s.sort(0, numNearest)
	s.sort(numNearest, s.numMvFound)
	if s.numMvFound < 2 {
		t.extraSearch(s)
	}
	t.contextAndClamping(s, numNew)
}

// setupGlobalMv is the setup global MV process (7.10.2.1).
func (t *tileEnc) setupGlobalMv(s *mvStack, refList int) mv {
	fi, b := t.f.inter, &t.b
	ref := s.refFrame[refList]
	typ := IDENTITY
	if ref != INTRA_FRAME {
		typ = fi.gmType[ref]
	}
	var m mv
	switch {
	case ref == INTRA_FRAME || typ == IDENTITY:
	case typ == TRANSLATION:
		m.row = fi.gmParams[ref][0] >> (WARPEDMODEL_PREC_BITS - 3)
		m.col = fi.gmParams[ref][1] >> (WARPEDMODEL_PREC_BITS - 3)
	default:
		gm := &fi.gmParams[ref]
		x := int64(b.c*MI_SIZE + int(blockWidth[b.size])/2 - 1)
		y := int64(b.r*MI_SIZE + int(blockHeight[b.size])/2 - 1)
		xc := (int64(gm[2])-(1<<WARPEDMODEL_PREC_BITS))*x + int64(gm[3])*y + int64(gm[0])
		yc := int64(gm[4])*x + (int64(gm[5])-(1<<WARPEDMODEL_PREC_BITS))*y + int64(gm[1])
		if fi.allowHighPrecisionMv {
			m.row = int32(round2Signed64(yc, WARPEDMODEL_PREC_BITS-3)) //nolint:gosec // a motion vector
			m.col = int32(round2Signed64(xc, WARPEDMODEL_PREC_BITS-3)) //nolint:gosec // a motion vector
		} else {
			m.row = int32(round2Signed64(yc, WARPEDMODEL_PREC_BITS-2) * 2) //nolint:gosec // a motion vector
			m.col = int32(round2Signed64(xc, WARPEDMODEL_PREC_BITS-2) * 2) //nolint:gosec // a motion vector
		}
	}
	fi.lowerMvPrecision(&m)
	return m
}

func round2Signed64(v int64, n int) int64 {
	if v < 0 {
		return -((-v + 1<<uint(n-1)) >> uint(n))
	}
	return (v + 1<<uint(n-1)) >> uint(n)
}

// scanRow is the scan row process (7.10.2.3).
func (t *tileEnc) scanRow(s *mvStack, deltaRow int) {
	f, b := t.f, &t.b
	bw4 := b.bw4
	end4 := min(bw4, f.miCols-b.c, 16)
	deltaCol := 0
	useStep16 := bw4 >= 16
	if abs(deltaRow) > 1 {
		deltaRow += b.r & 1
		deltaCol = 1 - (b.c & 1)
	}
	for i := 0; i < end4; {
		mvRow := b.r + deltaRow
		mvCol := b.c + deltaCol + i
		if !t.inside(mvRow, mvCol) {
			break
		}
		l := min(bw4, int(num4x4BlocksWide[f.mis[f.mi(mvRow, mvCol)].miSize]))
		if abs(deltaRow) > 1 {
			l = max(2, l)
		}
		if useStep16 {
			l = max(4, l)
		}
		weight := l * 2
		t.addRefMvCandidate(s, mvRow, mvCol, weight)
		i += l
	}
}

// scanCol is the scan col process (7.10.2.4).
func (t *tileEnc) scanCol(s *mvStack, deltaCol int) {
	f, b := t.f, &t.b
	bh4 := b.bh4
	end4 := min(bh4, f.miRows-b.r, 16)
	deltaRow := 0
	useStep16 := bh4 >= 16
	if abs(deltaCol) > 1 {
		deltaRow = 1 - (b.r & 1)
		deltaCol += b.c & 1
	}
	for i := 0; i < end4; {
		mvRow := b.r + deltaRow + i
		mvCol := b.c + deltaCol
		if !t.inside(mvRow, mvCol) {
			break
		}
		l := min(bh4, int(num4x4BlocksHigh[f.mis[f.mi(mvRow, mvCol)].miSize]))
		if abs(deltaCol) > 1 {
			l = max(2, l)
		}
		if useStep16 {
			l = max(4, l)
		}
		weight := l * 2
		t.addRefMvCandidate(s, mvRow, mvCol, weight)
		i += l
	}
}

// scanPoint is the scan point process (7.10.2.5).
func (t *tileEnc) scanPoint(s *mvStack, deltaRow, deltaCol int) {
	mvRow, mvCol := t.b.r+deltaRow, t.b.c+deltaCol
	if t.inside(mvRow, mvCol) && t.miWritten(mvRow, mvCol) {
		t.addRefMvCandidate(s, mvRow, mvCol, 4)
	}
}

// miWritten tells whether RefFrames[r][c] has been written for this
// frame, that is whether the position has been coded: the luma's
// BlockDecoded flags of the current superblock, which cover the
// superblock and the row above and the column left of it. (r, c) is
// inside the tile and within that area.
func (t *tileEnc) miWritten(r, c int) bool {
	sbR, sbC := t.b.r&^15, t.b.c&^15
	return t.decoded[0][r-sbR+1][c-sbC+1]
}

// temporalScan is the temporal scan process (7.10.2.6), only invoked
// when use_ref_frame_mvs is 1 (never, for now: MotionFieldMvs comes from
// the motion field estimation process, which is not ported).
func (t *tileEnc) temporalScan(s *mvStack) {
	b := &t.b
	bw4, bh4 := b.bw4, b.bh4
	stepW4, stepH4 := 2, 2
	if bw4 >= 16 {
		stepW4 = 4
	}
	if bh4 >= 16 {
		stepH4 = 4
	}
	for deltaRow := 0; deltaRow < min(bh4, 16); deltaRow += stepH4 {
		for deltaCol := 0; deltaCol < min(bw4, 16); deltaCol += stepW4 {
			t.addTplRefMv(s, deltaRow, deltaCol)
		}
	}
	allowExtension := bh4 >= int(num4x4BlocksHigh[BLOCK_8X8]) &&
		bh4 < int(num4x4BlocksHigh[BLOCK_64X64]) &&
		bw4 >= int(num4x4BlocksWide[BLOCK_8X8]) &&
		bw4 < int(num4x4BlocksWide[BLOCK_64X64])
	if allowExtension {
		tplSamplePos := [3][2]int{{bh4, -2}, {bh4, bw4}, {bh4 - 2, bw4}}
		for i := range 3 {
			deltaRow, deltaCol := tplSamplePos[i][0], tplSamplePos[i][1]
			// check_sb_border
			row, col := (b.r&15)+deltaRow, (b.c&15)+deltaCol
			if row >= 0 && row < 16 && col >= 0 && col < 16 {
				t.addTplRefMv(s, deltaRow, deltaCol)
			}
		}
	}
}

// addTplRefMv is the temporal sample process (7.10.2.7).
func (t *tileEnc) addTplRefMv(s *mvStack, deltaRow, deltaCol int) {
	f, b := t.f, &t.b
	fi := f.inter
	mvRow, mvCol := (b.r+deltaRow)|1, (b.c+deltaCol)|1
	if !t.inside(mvRow, mvCol) {
		return
	}
	x8, y8 := mvCol>>1, mvRow>>1
	at := y8*(f.miCols>>1) + x8
	const invalid = -1 << 15
	if deltaRow == 0 && deltaCol == 0 {
		s.zeroMvContext = 1
	}
	if !s.isCompound {
		candMv := fi.motionFieldMvs[s.refFrame[0]][at]
		if candMv.row == invalid {
			return
		}
		fi.lowerMvPrecision(&candMv)
		if deltaRow == 0 && deltaCol == 0 {
			if abs32(candMv.row-s.globalMvs[0].row) >= 16 || abs32(candMv.col-s.globalMvs[0].col) >= 16 {
				s.zeroMvContext = 1
			} else {
				s.zeroMvContext = 0
			}
		}
		idx := 0
		for ; idx < s.numMvFound; idx++ {
			if candMv == s.refStackMv[idx][0] {
				break
			}
		}
		if idx < s.numMvFound {
			s.weightStack[idx] += 2
		} else if s.numMvFound < MAX_REF_MV_STACK_SIZE {
			s.refStackMv[s.numMvFound][0] = candMv
			s.weightStack[s.numMvFound] = 2
			s.numMvFound++
		}
		return
	}
	candMv0 := fi.motionFieldMvs[s.refFrame[0]][at]
	if candMv0.row == invalid {
		return
	}
	candMv1 := fi.motionFieldMvs[s.refFrame[1]][at]
	if candMv1.row == invalid {
		return
	}
	fi.lowerMvPrecision(&candMv0)
	fi.lowerMvPrecision(&candMv1)
	if deltaRow == 0 && deltaCol == 0 {
		g := &s.globalMvs
		if abs32(candMv0.row-g[0].row) >= 16 || abs32(candMv0.col-g[0].col) >= 16 ||
			abs32(candMv1.row-g[1].row) >= 16 || abs32(candMv1.col-g[1].col) >= 16 {
			s.zeroMvContext = 1
		} else {
			s.zeroMvContext = 0
		}
	}
	idx := 0
	for ; idx < s.numMvFound; idx++ {
		if candMv0 == s.refStackMv[idx][0] && candMv1 == s.refStackMv[idx][1] {
			break
		}
	}
	if idx < s.numMvFound {
		s.weightStack[idx] += 2
	} else if s.numMvFound < MAX_REF_MV_STACK_SIZE {
		s.refStackMv[s.numMvFound] = [2]mv{candMv0, candMv1}
		s.weightStack[s.numMvFound] = 2
		s.numMvFound++
	}
}

// addRefMvCandidate is the add reference motion vector process
// (7.10.2.8).
func (t *tileEnc) addRefMvCandidate(s *mvStack, mvRow, mvCol, weight int) {
	f := t.f
	i := f.mi(mvRow, mvCol)
	if f.mis[i].isInter == 0 {
		return
	}
	if !s.isCompound {
		for candList := range 2 {
			if int(f.mis[i].refFrames[candList]) == s.refFrame[0] {
				t.searchStack(s, i, candList, weight)
			}
		}
		return
	}
	if int(f.mis[i].refFrames[0]) == s.refFrame[0] && int(f.mis[i].refFrames[1]) == s.refFrame[1] {
		t.compoundSearchStack(s, i, weight)
	}
}

// searchStack is the search stack process (7.10.2.9), for the candidate
// at mi index i.
func (t *tileEnc) searchStack(s *mvStack, i, candList, weight int) {
	f := t.f
	fi := f.inter
	candMode := int(f.mis[i].yMode)
	candSize := f.mis[i].miSize
	large := min(blockWidth[candSize], blockHeight[candSize]) >= 8
	var candMv mv
	if (candMode == GLOBALMV || candMode == GLOBAL_GLOBALMV) && fi.gmType[s.refFrame[0]] > TRANSLATION && large {
		candMv = s.globalMvs[0]
	} else {
		candMv = f.mis[i].mvs[candList]
	}
	fi.lowerMvPrecision(&candMv)
	if hasNewmv(candMode) {
		s.newMvCount++
	}
	s.foundMatch = true
	for idx := range s.numMvFound {
		if candMv == s.refStackMv[idx][0] {
			s.weightStack[idx] += weight
			return
		}
	}
	if s.numMvFound < MAX_REF_MV_STACK_SIZE {
		s.refStackMv[s.numMvFound][0] = candMv
		s.weightStack[s.numMvFound] = weight
		s.numMvFound++
	}
}

// compoundSearchStack is the compound search stack process (7.10.2.10),
// for the candidate at mi index i.
func (t *tileEnc) compoundSearchStack(s *mvStack, i, weight int) {
	f := t.f
	fi := f.inter
	candMvs := [2]mv{f.mis[i].mvs[0], f.mis[i].mvs[1]}
	candMode := int(f.mis[i].yMode)
	if candMode == GLOBAL_GLOBALMV {
		for refList := range 2 {
			if fi.gmType[s.refFrame[refList]] > TRANSLATION {
				candMvs[refList] = s.globalMvs[refList]
			}
		}
	}
	fi.lowerMvPrecision(&candMvs[0])
	fi.lowerMvPrecision(&candMvs[1])
	s.foundMatch = true
	found := false
	for idx := range s.numMvFound {
		if candMvs == s.refStackMv[idx] {
			s.weightStack[idx] += weight
			found = true
			break
		}
	}
	if !found && s.numMvFound < MAX_REF_MV_STACK_SIZE {
		s.refStackMv[s.numMvFound] = candMvs
		s.weightStack[s.numMvFound] = weight
		s.numMvFound++
	}
	if hasNewmv(candMode) {
		s.newMvCount++
	}
}

// hasNewmv is has_newmv.
func hasNewmv(mode int) bool {
	return mode == NEWMV || mode == NEW_NEWMV || mode == NEAR_NEWMV ||
		mode == NEW_NEARMV || mode == NEAREST_NEWMV || mode == NEW_NEARESTMV
}

// lowerMvPrecision is the lower precision process (7.10.2.11).
func (fi *interFrameInfo) lowerMvPrecision(candMv *mv) {
	if fi.allowHighPrecisionMv {
		return
	}
	for _, v := range [2]*int32{&candMv.row, &candMv.col} {
		if fi.forceIntegerMv {
			a := abs32(*v)
			aInt := (a + 3) >> 3
			if *v > 0 {
				*v = aInt << 3
			} else {
				*v = -(aInt << 3)
			}
		} else if *v&1 != 0 {
			if *v > 0 {
				*v--
			} else {
				*v++
			}
		}
	}
}

// sort is the sorting process (7.10.2.12): a stable bubble sort of
// entries start..end-1 by weight, heaviest first.
func (s *mvStack) sort(start, end int) {
	for end > start {
		newEnd := start
		for idx := start + 1; idx < end; idx++ {
			if s.weightStack[idx-1] < s.weightStack[idx] {
				// swap_stack
				s.weightStack[idx-1], s.weightStack[idx] = s.weightStack[idx], s.weightStack[idx-1]
				if s.isCompound {
					s.refStackMv[idx-1], s.refStackMv[idx] = s.refStackMv[idx], s.refStackMv[idx-1]
				} else {
					s.refStackMv[idx-1][0], s.refStackMv[idx][0] = s.refStackMv[idx][0], s.refStackMv[idx-1][0]
				}
				newEnd = idx
			}
		}
		end = newEnd
	}
}

// extraSearch is the extra search process (7.10.2.13).
func (t *tileEnc) extraSearch(s *mvStack) {
	f, b := t.f, &t.b
	s.refIDCount, s.refDiffCount = [2]int{}, [2]int{}
	w4 := min(16, b.bw4, f.miCols-b.c)
	h4 := min(16, b.bh4, f.miRows-b.r)
	num4x4 := min(w4, h4)
	for pass := range 2 {
		idx := 0
		for idx < num4x4 && s.numMvFound < 2 {
			var mvRow, mvCol int
			if pass == 0 {
				mvRow, mvCol = b.r-1, b.c+idx
			} else {
				mvRow, mvCol = b.r+idx, b.c-1
			}
			if !t.inside(mvRow, mvCol) {
				break
			}
			t.addExtraMvCandidate(s, mvRow, mvCol)
			if pass == 0 {
				idx += int(num4x4BlocksWide[f.mis[f.mi(mvRow, mvCol)].miSize])
			} else {
				idx += int(num4x4BlocksHigh[f.mis[f.mi(mvRow, mvCol)].miSize])
			}
		}
	}
	if s.isCompound {
		var combinedMvs [2][2]mv
		for list := range 2 {
			compCount := 0
			for idx := range s.refIDCount[list] {
				combinedMvs[compCount][list] = s.refIDMvs[list][idx]
				compCount++
			}
			for idx := 0; idx < s.refDiffCount[list] && compCount < 2; idx++ {
				combinedMvs[compCount][list] = s.refDiffMvs[list][idx]
				compCount++
			}
			for compCount < 2 {
				combinedMvs[compCount][list] = s.globalMvs[list]
				compCount++
			}
		}
		if s.numMvFound == 1 {
			if combinedMvs[0][0] == s.refStackMv[0][0] && combinedMvs[0][1] == s.refStackMv[0][1] {
				s.refStackMv[s.numMvFound] = combinedMvs[1]
			} else {
				s.refStackMv[s.numMvFound] = combinedMvs[0]
			}
			s.weightStack[s.numMvFound] = 2
			s.numMvFound++
		} else {
			for idx := range 2 {
				s.refStackMv[s.numMvFound] = combinedMvs[idx]
				s.weightStack[s.numMvFound] = 2
				s.numMvFound++
			}
		}
		return
	}
	for idx := s.numMvFound; idx < 2; idx++ {
		s.refStackMv[idx][0] = s.globalMvs[0]
	}
}

// addExtraMvCandidate is the add extra MV candidate process (7.10.2.14).
func (t *tileEnc) addExtraMvCandidate(s *mvStack, mvRow, mvCol int) {
	f := t.f
	fi := f.inter
	i := f.mi(mvRow, mvCol)
	if s.isCompound {
		for candList := range 2 {
			candRef := int(f.mis[i].refFrames[candList])
			if candRef > INTRA_FRAME {
				for list := range 2 {
					candMv := f.mis[i].mvs[candList]
					if candRef == s.refFrame[list] && s.refIDCount[list] < 2 {
						s.refIDMvs[list][s.refIDCount[list]] = candMv
						s.refIDCount[list]++
					} else if s.refDiffCount[list] < 2 {
						if fi.refFrameSignBias[candRef] != fi.refFrameSignBias[s.refFrame[list]] {
							candMv.row *= -1
							candMv.col *= -1
						}
						s.refDiffMvs[list][s.refDiffCount[list]] = candMv
						s.refDiffCount[list]++
					}
				}
			}
		}
		return
	}
	for candList := range 2 {
		candRef := int(f.mis[i].refFrames[candList])
		if candRef > INTRA_FRAME {
			candMv := f.mis[i].mvs[candList]
			if fi.refFrameSignBias[candRef] != fi.refFrameSignBias[s.refFrame[0]] {
				candMv.row *= -1
				candMv.col *= -1
			}
			idx := 0
			for ; idx < s.numMvFound; idx++ {
				if candMv == s.refStackMv[idx][0] {
					break
				}
			}
			if idx == s.numMvFound {
				s.refStackMv[idx][0] = candMv
				s.weightStack[idx] = 2
				s.numMvFound++
			}
		}
	}
}

// contextAndClamping is the context and clamping process (7.10.2.15).
func (t *tileEnc) contextAndClamping(s *mvStack, numNew int) {
	b := &t.b
	bw, bh := int32(blockWidth[b.size]), int32(blockHeight[b.size])
	numLists := 1
	if s.isCompound {
		numLists = 2
	}
	for idx := range s.numMvFound {
		z := 0
		if idx+1 < s.numMvFound {
			w0, w1 := s.weightStack[idx], s.weightStack[idx+1]
			if w0 >= REF_CAT_LEVEL {
				if w1 < REF_CAT_LEVEL {
					z = 1
				}
			} else {
				z = 2
			}
		}
		s.drlCtxStack[idx] = z
	}
	for list := range numLists {
		for idx := range s.numMvFound {
			refMv := &s.refStackMv[idx][list]
			refMv.row = t.clampMvRow(refMv.row, MV_BORDER+bh*8)
			refMv.col = t.clampMvCol(refMv.col, MV_BORDER+bw*8)
		}
	}
	switch s.closeMatches {
	case 0:
		s.newMvContext = min(s.totalMatches, 1) // 0,1
		s.refMvContext = s.totalMatches
	case 1:
		s.newMvContext = 3 - min(numNew, 1) // 2,3
		s.refMvContext = 2 + s.totalMatches
	default:
		s.newMvContext = 5 - min(numNew, 1) // 4,5
		s.refMvContext = 5
	}
}

// clampMvRow is clamp_mv_row.
func (t *tileEnc) clampMvRow(mvec, border int32) int32 {
	b := &t.b
	mbToTopEdge := -int32(b.r * MI_SIZE * 8)                          //nolint:gosec // a frame position
	mbToBottomEdge := int32((t.f.miRows - b.bh4 - b.r) * MI_SIZE * 8) //nolint:gosec // a frame position
	return min(max(mvec, mbToTopEdge-border), mbToBottomEdge+border)
}

// clampMvCol is clamp_mv_col.
func (t *tileEnc) clampMvCol(mvec, border int32) int32 {
	b := &t.b
	mbToLeftEdge := -int32(b.c * MI_SIZE * 8)                        //nolint:gosec // a frame position
	mbToRightEdge := int32((t.f.miCols - b.bw4 - b.c) * MI_SIZE * 8) //nolint:gosec // a frame position
	return min(max(mvec, mbToLeftEdge-border), mbToRightEdge+border)
}

// hasOverlappableCandidates is the has overlappable candidates process
// (7.10.3).
func (t *tileEnc) hasOverlappableCandidates() bool {
	f, b := t.f, &t.b
	if b.availU {
		w4 := b.bw4
		for x4 := b.c; x4 < min(f.miCols, b.c+w4); x4 += 2 {
			if f.mis[f.mi(b.r-1, x4|1)].refFrames[0] > INTRA_FRAME {
				return true
			}
		}
	}
	if b.availL {
		h4 := b.bh4
		for y4 := b.r; y4 < min(f.miRows, b.r+h4); y4 += 2 {
			if f.mis[f.mi(y4|1, b.c-1)].refFrames[0] > INTRA_FRAME {
				return true
			}
		}
	}
	return false
}

// The mode side (inter_block_mode_info and assign_mv, spec 5.11.23 and
// 5.11.26).

// getMode is get_mode: the single mode (NEARESTMV, NEARMV, GLOBALMV or
// NEWMV) yMode uses for refList.
func getMode(yMode, refList int) int {
	if refList == 0 {
		switch {
		case yMode < NEAREST_NEARESTMV:
			return yMode
		case yMode == NEW_NEWMV || yMode == NEW_NEARESTMV || yMode == NEW_NEARMV:
			return NEWMV
		case yMode == NEAREST_NEARESTMV || yMode == NEAREST_NEWMV:
			return NEARESTMV
		case yMode == NEAR_NEARMV || yMode == NEAR_NEWMV:
			return NEARMV
		}
		return GLOBALMV
	}
	switch yMode {
	case NEW_NEWMV, NEAREST_NEWMV, NEAR_NEWMV:
		return NEWMV
	case NEAREST_NEARESTMV, NEW_NEARESTMV:
		return NEARESTMV
	case NEAR_NEARMV, NEW_NEARMV:
		return NEARMV
	}
	return GLOBALMV
}

// hasNearmv is has_nearmv.
func hasNearmv(yMode int) bool {
	return yMode == NEARMV || yMode == NEAR_NEARMV || yMode == NEAR_NEWMV || yMode == NEW_NEARMV
}

// refMvIdxRange gives the RefMvIdx values yMode can signal with this
// stack, lo..hi inclusive: 0..2 for NEWMV and NEW_NEWMV, 1..3 for the
// modes with a NEARMV, 0 for the others, cut by NumMvFound as the
// drl_mode loop is.
func (s *mvStack) refMvIdxRange(yMode int) (lo, hi int) {
	switch {
	case yMode == NEWMV || yMode == NEW_NEWMV:
		return 0, max(0, min(2, s.numMvFound-1))
	case hasNearmv(yMode):
		return 1, max(1, min(3, s.numMvFound-1))
	}
	return 0, 0
}

// drlModes gives the drl_mode symbols inter_block_mode_info codes for
// refMvIdx (in refMvIdxRange(yMode)): n of them, each with its context
// (DrlCtxStack[idx]) and value.
func (s *mvStack) drlModes(yMode, refMvIdx int) (ctx, val [2]int, n int) {
	first := 0
	switch {
	case yMode == NEWMV || yMode == NEW_NEWMV:
	case hasNearmv(yMode):
		first = 1
	default:
		return ctx, val, 0
	}
	for idx := first; idx < first+2; idx++ {
		if s.numMvFound > idx+1 {
			ctx[n] = s.drlCtxStack[idx]
			if refMvIdx == idx {
				n++
				break
			}
			val[n] = 1
			n++
		}
	}
	return ctx, val, n
}

// predMv is assign_mv's PredMv[i] (not for intra block copy) for yMode
// and RefMvIdx refMvIdx: the motion vector itself for NEARESTMV, NEARMV
// and GLOBALMV, what read_mv codes the difference from for NEWMV.
func (s *mvStack) predMv(yMode, refMvIdx, i int) mv {
	compMode := getMode(yMode, i)
	if compMode == GLOBALMV {
		return s.globalMvs[i]
	}
	pos := refMvIdx
	if compMode == NEARESTMV {
		pos = 0
	}
	if compMode == NEWMV && s.numMvFound <= 1 {
		pos = 0
	}
	return s.refStackMv[pos][i]
}

// compoundModeCtx is compound_mode's context.
func (s *mvStack) compoundModeCtx() int {
	return int(compoundModeCtxMap[s.refMvContext>>1][min(s.newMvContext, COMP_NEWMV_CTXS-1)])
}
