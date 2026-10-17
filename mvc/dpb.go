package mvc

import "sort"

// viewState holds per-view decoding state: reference pictures and the
// variables carried between pictures for POC and frame_num derivation.
type viewState struct {
	refs []*picture // pictures marked as used for reference

	prevRefFrameNum    int
	prevPocMsb         int32
	prevPocLsb         int32
	prevFrameNumOffset int32
	prevFrameNum       int
	prevHadMMCO5       bool
	maxLongTermIdx     int // -1: no long-term frame indices
	seen               bool
}

func (v *viewState) reset() {
	*v = viewState{maxLongTermIdx: -1}
}

// computePOC derives the picture order count (8.2.1) for a frame.
func (v *viewState) computePOC(h *sliceHeader) (poc, top, bot int32, frameNumOffset int32) {
	s := h.sps
	idr := h.nal.idr
	maxFrameNum := int32(1) << s.log2MaxFrameNum
	switch s.pocType {
	case 0:
		var prevMsb, prevLsb int32
		if !idr {
			prevMsb, prevLsb = v.prevPocMsb, v.prevPocLsb
		}
		maxLsb := int32(1) << s.log2MaxPocLsb
		lsb := int32(h.pocLsb)
		var msb int32
		switch {
		case lsb < prevLsb && prevLsb-lsb >= maxLsb/2:
			msb = prevMsb + maxLsb
		case lsb > prevLsb && lsb-prevLsb > maxLsb/2:
			msb = prevMsb - maxLsb
		default:
			msb = prevMsb
		}
		top := msb + lsb
		bot := top + h.deltaPocBottom
		poc = min(top, bot)
		if h.fieldPic {
			// a field's own order count: TopFieldOrderCnt or
			// BottomFieldOrderCnt, both msb + lsb
			poc, bot = top, top
		}
		if h.nal.refIdc != 0 {
			v.prevPocMsb = msb
			v.prevPocLsb = lsb
		}
		return poc, top, bot, 0
	case 1:
		var off int32
		if !idr {
			off = v.prevFrameNumOffset
			if v.prevHadMMCO5 {
				off = 0
			}
			if v.prevFrameNum > h.frameNum {
				off += maxFrameNum
			}
		}
		n := int32(len(s.offsetForRefFrame))
		var abs int32
		if n != 0 {
			abs = off + int32(h.frameNum)
		}
		if h.nal.refIdc == 0 && abs > 0 {
			abs--
		}
		var expected int32
		if abs > 0 {
			var deltaCycle int32
			for _, o := range s.offsetForRefFrame {
				deltaCycle += o
			}
			cycle := (abs - 1) / n
			inCycle := (abs - 1) % n
			expected = cycle * deltaCycle
			for i := int32(0); i <= inCycle; i++ {
				expected += s.offsetForRefFrame[i]
			}
		}
		if h.nal.refIdc == 0 {
			expected += s.offsetForNonRefPic
		}
		top := expected + h.deltaPoc[0]
		bot := top + s.offsetForTopToBottom + h.deltaPoc[1]
		if h.fieldPic {
			f := top
			if h.bottomField {
				f = expected + s.offsetForTopToBottom + h.deltaPoc[0]
			}
			return f, f, f, off
		}
		return min(top, bot), top, bot, off
	default:
		var off int32
		if !idr {
			off = v.prevFrameNumOffset
			if v.prevHadMMCO5 {
				off = 0
			}
			if v.prevFrameNum > h.frameNum {
				off += maxFrameNum
			}
		}
		var t int32
		switch {
		case idr:
			t = 0
		case h.nal.refIdc == 0:
			t = 2*(off+int32(h.frameNum)) - 1
		default:
			t = 2 * (off + int32(h.frameNum))
		}
		return t, t, t, off
	}
}

func (v *viewState) removeRef(p *picture) {
	for i, r := range v.refs {
		if r == p {
			v.refs = append(v.refs[:i], v.refs[i+1:]...)
			break
		}
	}
	p.shortRef, p.longRef, p.refMask = false, false, 0
}

func (v *viewState) unmarkAll() {
	for _, r := range v.refs {
		r.shortRef, r.longRef, r.refMask = false, false, 0
	}
	v.refs = v.refs[:0]
}

func (v *viewState) numShortLong() (int, int) {
	ns, nl := 0, 0
	for _, r := range v.refs {
		if r.longRef {
			nl++
		} else {
			ns++
		}
	}
	return ns, nl
}

// slidingWindow applies the sliding window marking (8.2.5.3).
func (v *viewState) slidingWindow(maxRefs int, curFrameNum int, maxFrameNum int) {
	for {
		ns, nl := v.numShortLong()
		if ns+nl < max(maxRefs, 1) || ns == 0 {
			return
		}
		v.removeRef(v.oldestShort(curFrameNum, maxFrameNum, nil))
	}
}

func frameNumWrap(p *picture, cur, max int) int {
	if p.frameNum > cur {
		return p.frameNum - max
	}
	return p.frameNum
}

// markPicture performs reference picture marking for the decoded picture
// p (8.2.5): a frame, or one field of the frame store p (h.fieldPic).
// The frames in refs are the frame stores with a field marked; a frame's
// refMask says which, its shortRef and longRef which list it is in.
func (v *viewState) markPicture(p *picture, h *sliceHeader) {
	s := h.sps
	structure := uint8(3)
	if h.fieldPic {
		structure = 1
		if h.bottomField {
			structure = 2
		}
	}
	// the second field of a frame whose first field is marked joins it
	second := h.fieldPic && p.refMask != 0 && v.has(p)
	if h.nal.idr && !second {
		v.unmarkAll()
		if h.longTermReference {
			p.longRef, p.longTermFrameIdx = true, 0
			v.maxLongTermIdx = 0
		} else {
			p.shortRef = true
			v.maxLongTermIdx = -1
		}
		p.refMask = structure
		v.refs = append(v.refs, p)
		return
	}
	maxFrameNum := 1 << s.log2MaxFrameNum
	maxPicNum, curPicNum := maxFrameNum, h.frameNum
	if h.fieldPic {
		maxPicNum, curPicNum = 2*maxFrameNum, 2*h.frameNum+1
	}
	// extract: a picture number's frame number (or long-term frame
	// index) and the fields it names
	extract := func(n int) (int, uint8) {
		if !h.fieldPic {
			return n, 3
		}
		if n&1 != 0 {
			return n >> 1, structure // the same parity
		}
		return n >> 1, structure ^ 3
	}
	unref := func(r *picture, mask uint8) {
		if r.refMask &^= mask; r.refMask == 0 {
			v.removeRef(r)
		}
	}
	assigned := false
	if !h.adaptiveMarking {
		ns, nl := v.numShortLong()
		if ns > 0 && ns+nl >= max(s.maxNumRefFrames, 1) && !second {
			v.removeRef(v.oldestShort(h.frameNum, maxFrameNum, nil))
		}
	} else {
		for _, m := range h.mmco {
			switch m.op {
			case 1:
				fn, st := extract((curPicNum - int(m.diffPicNums+1)) & (maxPicNum - 1))
				if r := v.findShort(fn); r != nil {
					unref(r, st)
				}
			case 2:
				idx, st := extract(int(m.longTermPicNum))
				if r := v.findLong(idx); r != nil {
					unref(r, st)
				}
			case 3:
				fn, _ := extract((curPicNum - int(m.diffPicNums+1)) & (maxPicNum - 1))
				idx := int(m.longTermFrameIdx)
				r := v.findShort(fn)
				if r == nil {
					continue
				}
				if l := v.findLong(idx); l != nil && l != r {
					v.removeRef(l)
				}
				r.shortRef, r.longRef, r.longTermFrameIdx = false, true, idx
			case 4:
				v.maxLongTermIdx = int(m.maxLongTermIdxP1) - 1
				for i := 0; i < len(v.refs); {
					if r := v.refs[i]; r.longRef && r.longTermFrameIdx > v.maxLongTermIdx {
						v.removeRef(r)
						continue
					}
					i++
				}
			case 5:
				v.unmarkAll()
				v.maxLongTermIdx = -1
			case 6:
				idx := int(m.longTermFrameIdx)
				if l := v.findLong(idx); l != nil && l != p {
					v.removeRef(l)
				}
				p.shortRef, p.longRef, p.longTermFrameIdx = false, true, idx
				p.refMask |= structure
				if !v.has(p) {
					v.refs = append(v.refs, p)
				}
				assigned = true
			}
		}
	}
	if !assigned {
		p.refMask |= structure
		if !p.longRef {
			p.shortRef = true
		}
		if !v.has(p) {
			v.refs = append(v.refs, p)
		}
	}
	// guard against streams exceeding max_num_ref_frames
	if ns, nl := v.numShortLong(); ns+nl > max(s.maxNumRefFrames, 1) {
		if r := v.oldestShort(h.frameNum, maxFrameNum, p); r != nil {
			v.removeRef(r)
		} else {
			for _, r := range v.refs {
				if r.longRef && r != p {
					v.removeRef(r)
					break
				}
			}
		}
	}
}

func (v *viewState) has(p *picture) bool {
	for _, r := range v.refs {
		if r == p {
			return true
		}
	}
	return false
}

// findShort and findLong return the frame in the short-term list with
// the frame number, in the long-term list with the index.
func (v *viewState) findShort(frameNum int) *picture {
	for _, r := range v.refs {
		if r.shortRef && r.frameNum == frameNum {
			return r
		}
	}
	return nil
}

func (v *viewState) findLong(idx int) *picture {
	for _, r := range v.refs {
		if r.longRef && r.longTermFrameIdx == idx {
			return r
		}
	}
	return nil
}

// oldestShort is the short-term frame with the least FrameNumWrap, but
// not except.
func (v *viewState) oldestShort(cur, maxFrameNum int, except *picture) *picture {
	var oldest *picture
	for _, r := range v.refs {
		if r.shortRef && r != except && (oldest == nil || frameNumWrap(r, cur, maxFrameNum) < frameNumWrap(oldest, cur, maxFrameNum)) {
			oldest = r
		}
	}
	return oldest
}

// initRefLists builds the initial reference picture lists (8.2.4.2).
func (v *viewState) initRefLists(h *sliceHeader, curPOC int32) (l0, l1 []*picture) {
	maxFrameNum := 1 << h.sps.log2MaxFrameNum
	cur := h.frameNum
	var short, long []*picture
	for _, r := range v.refs {
		if r.longRef {
			long = append(long, r)
		} else if r.shortRef {
			short = append(short, r)
		}
	}
	sort.Slice(long, func(i, j int) bool { return long[i].longTermFrameIdx < long[j].longTermFrameIdx })
	if h.sliceType == sliceP {
		sort.Slice(short, func(i, j int) bool {
			return frameNumWrap(short[i], cur, maxFrameNum) > frameNumWrap(short[j], cur, maxFrameNum)
		})
		l0 = append(append(l0, short...), long...)
		return l0, nil
	}
	var before, after []*picture
	for _, r := range short {
		if r.poc < curPOC {
			before = append(before, r)
		} else {
			after = append(after, r)
		}
	}
	sort.Slice(before, func(i, j int) bool { return before[i].poc > before[j].poc })
	sort.Slice(after, func(i, j int) bool { return after[i].poc < after[j].poc })
	l0 = append(append(append(l0, before...), after...), long...)
	l1 = append(append(append(l1, after...), before...), long...)
	if len(l1) > 1 && len(l0) == len(l1) {
		same := true
		for i := range l0 {
			if l0[i] != l1[i] {
				same = false
				break
			}
		}
		if same {
			l1[0], l1[1] = l1[1], l1[0]
		}
	}
	return l0, l1
}

// initFieldRefLists builds the initial reference lists of a field
// (8.2.4.2.2, 8.2.4.2.4 and 8.2.4.2.5): the frames ordered as for a frame
// (by POC around the field's own, ties to the frame store's least field
// POC), then their fields taken alternately, starting with the field's
// own parity.
func (v *viewState) initFieldRefLists(h *sliceHeader, curPOC int32) (l0, l1 []*picture) {
	maxFrameNum := 1 << h.sps.log2MaxFrameNum
	cur := h.frameNum
	parity := 0
	if h.bottomField {
		parity = 1
	}
	var short, long []*picture
	for _, r := range v.refs {
		if r.longRef {
			long = append(long, r)
		} else if r.shortRef {
			short = append(short, r)
		}
	}
	sort.Slice(long, func(i, j int) bool { return long[i].longTermFrameIdx < long[j].longTermFrameIdx })
	fields := func(frames []*picture) []*picture {
		var out []*picture
		i := [2]int{}
		for i[0] < len(frames) || i[1] < len(frames) {
			for k, par := range [2]int{parity, parity ^ 1} {
				for i[k] < len(frames) && frames[i[k]].refMask>>par&1 == 0 {
					i[k]++
				}
				if i[k] < len(frames) {
					out = append(out, frames[i[k]].fieldRef(par))
					i[k]++
				}
			}
		}
		return out
	}
	if h.sliceType == sliceP {
		sort.Slice(short, func(i, j int) bool {
			return frameNumWrap(short[i], cur, maxFrameNum) > frameNumWrap(short[j], cur, maxFrameNum)
		})
		return append(fields(short), fields(long)...), nil
	}
	var before, after []*picture
	for _, r := range short {
		if r.poc <= curPOC {
			before = append(before, r)
		} else {
			after = append(after, r)
		}
	}
	sort.Slice(before, func(i, j int) bool { return before[i].poc > before[j].poc })
	sort.Slice(after, func(i, j int) bool { return after[i].poc < after[j].poc })
	lt := fields(long)
	l0 = append(fields(append(append([]*picture(nil), before...), after...)), lt...)
	l1 = append(fields(append(append([]*picture(nil), after...), before...)), lt...)
	if len(l1) > 1 && len(l0) == len(l1) {
		same := true
		for i := range l0 {
			if l0[i] != l1[i] {
				same = false
				break
			}
		}
		if same {
			l1[0], l1[1] = l1[1], l1[0]
		}
	}
	return l0, l1
}

// modifyRefList applies ref_pic_list_modification (8.2.4.3 and H.8.2.2.3).
func (v *viewState) modifyRefList(list []*picture, n int, mods []refPicMod, h *sliceHeader, interView []*picture) []*picture {
	maxPicNum := 1 << h.sps.log2MaxFrameNum
	cur := h.frameNum
	if h.fieldPic {
		return v.modifyFieldRefList(list, n, mods, h)
	}
	// working list of n+1 entries
	work := make([]*picture, n+1)
	copy(work, list)
	picNumPred := cur
	viewIdxPred := -1
	refIdx := 0
	for _, m := range mods {
		if refIdx >= n {
			break
		}
		var pic *picture
		switch m.idc {
		case 0, 1:
			abs := int(m.val) + 1
			var noWrap int
			if m.idc == 0 {
				noWrap = picNumPred - abs
				if noWrap < 0 {
					noWrap += maxPicNum
				}
			} else {
				noWrap = picNumPred + abs
				if noWrap >= maxPicNum {
					noWrap -= maxPicNum
				}
			}
			picNumPred = noWrap
			pn := noWrap
			if pn > cur {
				pn -= maxPicNum
			}
			for _, r := range v.refs {
				if r.shortRef && frameNumWrap(r, cur, maxPicNum) == pn {
					pic = r
					break
				}
			}
		case 2:
			for _, r := range v.refs {
				if r.longRef && r.longTermFrameIdx == int(m.val) {
					pic = r
					break
				}
			}
		case 4, 5:
			num := len(interView)
			if num == 0 {
				continue
			}
			abs := int(m.val) + 1
			var idx int
			if m.idc == 4 {
				idx = viewIdxPred - abs
				for idx < 0 {
					idx += num
				}
			} else {
				idx = (viewIdxPred + abs) % num
			}
			viewIdxPred = idx
			pic = interView[idx]
		}
		for c := n; c > refIdx; c-- {
			work[c] = work[c-1]
		}
		work[refIdx] = pic
		refIdx++
		ni := refIdx
		for c := refIdx; c <= n; c++ {
			if work[c] != pic || pic == nil {
				work[ni] = work[c]
				ni++
			}
		}
		for ; ni <= n; ni++ {
			work[ni] = nil
		}
	}
	return work[:n]
}

// dpbSize returns MaxDpbFrames for the SPS (A.3.1).
func dpbSize(s *sps) int {
	if s.maxDecFrameBuffering >= 0 {
		return max(s.maxDecFrameBuffering, 1)
	}
	var maxDpbMbs int
	switch s.levelIdc {
	case 9, 10:
		maxDpbMbs = 396
	case 11:
		if s.constraintFlags&0x10 != 0 && s.profileIdc != 100 {
			maxDpbMbs = 396
		} else {
			maxDpbMbs = 900
		}
	case 12, 13, 20:
		maxDpbMbs = 2376
	case 21:
		maxDpbMbs = 4752
	case 22, 30:
		maxDpbMbs = 8100
	case 31:
		maxDpbMbs = 18000
	case 32:
		maxDpbMbs = 20480
	case 40, 41:
		maxDpbMbs = 32768
	case 42:
		maxDpbMbs = 34816
	case 50:
		maxDpbMbs = 110400
	case 51, 52:
		maxDpbMbs = 184320
	default:
		maxDpbMbs = 696320
	}
	n := maxDpbMbs / (s.widthMbs * s.heightMbs())
	if n > 16 {
		n = 16
	}
	if n < s.maxNumRefFrames {
		n = s.maxNumRefFrames
	}
	return max(n, 1)
}

// modifyFieldRefList is modifyRefList for a field: picture numbers count
// fields, 2 FrameNumWrap + 1 for the field's own parity and 2 FrameNumWrap
// for the other (long-term ones likewise from LongTermFrameIdx).
func (v *viewState) modifyFieldRefList(list []*picture, n int, mods []refPicMod, h *sliceHeader) []*picture {
	maxPicNum := 2 << h.sps.log2MaxFrameNum
	parity := 0
	if h.bottomField {
		parity = 1
	}
	work := make([]*picture, n+1)
	copy(work, list)
	picNumPred := 2*h.frameNum + 1
	refIdx := 0
	for _, m := range mods {
		if refIdx >= n {
			break
		}
		var pic *picture
		switch m.idc {
		case 0, 1:
			abs := int(m.val) + 1
			if m.idc == 0 {
				picNumPred -= abs
			} else {
				picNumPred += abs
			}
			picNumPred &= maxPicNum - 1
			par := parity
			if picNumPred&1 == 0 {
				par ^= 1
			}
			if r := v.findShort(picNumPred >> 1); r != nil && r.refMask>>par&1 != 0 {
				pic = r.fieldRef(par)
			}
		case 2:
			par := parity
			if m.val&1 == 0 {
				par ^= 1
			}
			if r := v.findLong(int(m.val >> 1)); r != nil && r.refMask>>par&1 != 0 {
				pic = r.fieldRef(par)
			}
		default:
			continue
		}
		for c := n; c > refIdx; c-- {
			work[c] = work[c-1]
		}
		work[refIdx] = pic
		refIdx++
		ni := refIdx
		for c := refIdx; c <= n; c++ {
			if work[c] != pic || pic == nil {
				work[ni] = work[c]
				ni++
			}
		}
		for ; ni <= n; ni++ {
			work[ni] = nil
		}
	}
	return work[:n]
}
