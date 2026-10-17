package av1

// Temporal motion vectors (use_ref_frame_mvs): each frame keeps, on an
// 8x8 grid, a motion vector of its blocks that point back in time (the
// motion field motion vector storage process, 7.19), and a later frame
// projects its references' through the time between them (the motion
// field estimation process, 7.9) to give find_mv_stack candidates where
// the motion carries on.

// dbgNoRefFrameMvs turns use_ref_frame_mvs off.
var dbgNoRefFrameMvs = false

// mfInvalid is the motion field's empty entry (-1 << 15 both ways).
var mfInvalid = mv{row: -1 << 15, col: -1 << 15}

// saveMotionField fills st's saved motion field from the coded frame f:
// MfRefFrames and MfMvs at the 8x8 grid's points (row and col odd, the
// only ones read), and the frame's references' order hints.
func (f *frameEnc) saveMotionField(st *refFrameState) {
	w8, h8 := f.miCols>>1, f.miRows>>1
	st.mfRef = make([]int8, w8*h8)
	st.mfMv = make([]mv, w8*h8)
	if f.inter != nil {
		st.refHints = f.inter.orderHints
	}
	for y8 := range h8 {
		for x8 := range w8 {
			i := f.mi(2*y8+1, 2*x8+1)
			k := y8*w8 + x8
			st.mfRef[k] = NONE
			if f.inter == nil {
				continue
			}
			for list := range 2 {
				r := int(f.refFrames[list][i])
				if r <= INTRA_FRAME {
					continue
				}
				if f.inter.getRelativeDist(f.inter.orderHints[r], f.inter.orderHint) >= 0 {
					continue
				}
				m := f.mvs[list][i]
				if abs32(m.row) <= REFMVS_LIMIT && abs32(m.col) <= REFMVS_LIMIT {
					st.mfRef[k], st.mfMv[k] = int8(r), m //nolint:gosec // a reference
				}
			}
		}
	}
}

// motionFieldEstimation is the motion field estimation process: fi's
// MotionFieldMvs from the references' saved motion fields.
func (f *frameEnc) motionFieldEstimation() {
	fi := f.inter
	w8, h8 := f.miCols>>1, f.miRows>>1
	for ref := LAST_FRAME; ref <= ALTREF_FRAME; ref++ {
		m := make([]mv, w8*h8)
		for i := range m {
			m[i] = mfInvalid
		}
		fi.motionFieldMvs[ref] = m
	}
	last := f.refs[LAST_FRAME]
	if last.refHints[ALTREF_FRAME] != fi.orderHints[GOLDEN_FRAME] {
		f.mfProject(LAST_FRAME, -1)
	}
	refStamp := MFMV_STACK_SIZE - 2
	for _, src := range []int{BWDREF_FRAME, ALTREF2_FRAME, ALTREF_FRAME} {
		if fi.getRelativeDist(fi.orderHints[src], fi.orderHint) <= 0 {
			continue
		}
		if src == ALTREF_FRAME && refStamp < 0 {
			continue
		}
		if f.mfProject(src, 1) {
			refStamp--
		}
	}
	if refStamp >= 0 {
		f.mfProject(LAST2_FRAME, -1)
	}
}

// mfProject is the projection process: src's motion field into
// MotionFieldMvs, false if src cannot be projected.
func (f *frameEnc) mfProject(src, dstSign int) bool {
	fi := f.inter
	r := f.refs[src]
	if r.miRows != f.miRows || r.miCols != f.miCols || r.frameType == KEY_FRAME || r.frameType == INTRA_ONLY_FRAME || r.mfRef == nil {
		return false
	}
	w8, h8 := f.miCols>>1, f.miRows>>1
	refToCur := fi.getRelativeDist(fi.orderHints[src], fi.orderHint)
	var refToDst [TOTAL_REFS_PER_FRAME]int
	for dst := LAST_FRAME; dst <= ALTREF_FRAME; dst++ {
		refToDst[dst] = fi.getRelativeDist(fi.orderHint, fi.orderHints[dst])
	}
	for y8 := range h8 {
		for x8 := range w8 {
			k := y8*w8 + x8
			srcRef := int(r.mfRef[k])
			if srcRef <= INTRA_FRAME {
				continue
			}
			refOffset := fi.getRelativeDist(fi.orderHints[src], r.refHints[srcRef])
			if abs(refToCur) > MAX_FRAME_DISTANCE || abs(refOffset) > MAX_FRAME_DISTANCE || refOffset <= 0 {
				continue
			}
			m := r.mfMv[k]
			p := mvProjection(m, refToCur*dstSign, refOffset)
			posY8, okY := mfPosition(y8, int(p.row), dstSign, h8, MAX_OFFSET_HEIGHT)
			posX8, okX := mfPosition(x8, int(p.col), dstSign, w8, MAX_OFFSET_WIDTH)
			if !okY || !okX {
				continue
			}
			for dst := LAST_FRAME; dst <= ALTREF_FRAME; dst++ {
				fi.motionFieldMvs[dst][posY8*w8+posX8] = mvProjection(m, refToDst[dst], refOffset)
			}
		}
	}
	return true
}

// mvProjection is the get mv projection process.
func mvProjection(m mv, numerator, denominator int) mv {
	den := min(MAX_FRAME_DISTANCE, denominator)
	num := min(max(numerator, -MAX_FRAME_DISTANCE), MAX_FRAME_DISTANCE)
	proj := func(v int32) int32 {
		s := round2Signed64(int64(v)*int64(num)*int64(divMult[den]), 14)
		return int32(min(max(s, -(1<<14)+1), (1<<14)-1)) //nolint:gosec // clipped
	}
	return mv{row: proj(m.row), col: proj(m.col)}
}

// mfPosition is get_block_position's project: the 8x8 position v8 moves
// to by delta, and whether it may be used.
func mfPosition(v8, delta, dstSign, max8, maxOff8 int) (int, bool) {
	base8 := (v8 >> 3) << 3
	var offset8 int
	if delta >= 0 {
		offset8 = delta >> (3 + 1 + MI_SIZE_LOG2)
	} else {
		offset8 = -((-delta) >> (3 + 1 + MI_SIZE_LOG2))
	}
	v8 += dstSign * offset8
	ok := v8 >= 0 && v8 < max8 && v8 >= base8-maxOff8 && v8 < base8+8+maxOff8
	return v8, ok
}
