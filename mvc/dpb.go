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
	p.shortRef, p.longRef = false, false
}

func (v *viewState) unmarkAll() {
	for _, r := range v.refs {
		r.shortRef, r.longRef = false, false
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
	if maxRefs < 1 {
		maxRefs = 1
	}
	for {
		ns, nl := v.numShortLong()
		if ns+nl < maxRefs || ns == 0 {
			return
		}
		var oldest *picture
		for _, r := range v.refs {
			if r.shortRef && (oldest == nil || frameNumWrap(r, curFrameNum, maxFrameNum) < frameNumWrap(oldest, curFrameNum, maxFrameNum)) {
				oldest = r
			}
		}
		v.removeRef(oldest)
	}
}

func frameNumWrap(p *picture, cur, max int) int {
	if p.frameNum > cur {
		return p.frameNum - max
	}
	return p.frameNum
}

// markPicture performs reference picture marking for the decoded picture p
// (8.2.5).
func (v *viewState) markPicture(p *picture, h *sliceHeader) {
	s := h.sps
	maxFrameNum := 1 << s.log2MaxFrameNum
	if h.nal.idr {
		v.unmarkAll()
		if h.longTermReference {
			p.longRef = true
			p.longTermFrameIdx = 0
			v.maxLongTermIdx = 0
		} else {
			p.shortRef = true
			v.maxLongTermIdx = -1
		}
		v.refs = append(v.refs, p)
		return
	}
	becameLong := false
	if h.adaptiveMarking {
		cur := h.frameNum
		for _, m := range h.mmco {
			switch m.op {
			case 1:
				pn := cur - int(m.diffPicNums+1)
				for _, r := range v.refs {
					if r.shortRef && frameNumWrap(r, cur, maxFrameNum) == pn {
						v.removeRef(r)
						break
					}
				}
			case 2:
				for _, r := range v.refs {
					if r.longRef && r.longTermFrameIdx == int(m.longTermPicNum) {
						v.removeRef(r)
						break
					}
				}
			case 3:
				pn := cur - int(m.diffPicNums+1)
				idx := int(m.longTermFrameIdx)
				var target *picture
				for _, r := range v.refs {
					if r.shortRef && frameNumWrap(r, cur, maxFrameNum) == pn {
						target = r
						break
					}
				}
				for _, r := range v.refs {
					if r.longRef && r.longTermFrameIdx == idx && r != target {
						v.removeRef(r)
						break
					}
				}
				if target != nil {
					target.shortRef = false
					target.longRef = true
					target.longTermFrameIdx = idx
				}
			case 4:
				v.maxLongTermIdx = int(m.maxLongTermIdxP1) - 1
				for i := 0; i < len(v.refs); {
					r := v.refs[i]
					if r.longRef && r.longTermFrameIdx > v.maxLongTermIdx {
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
				for _, r := range v.refs {
					if r.longRef && r.longTermFrameIdx == idx {
						v.removeRef(r)
						break
					}
				}
				p.longRef = true
				p.longTermFrameIdx = idx
				becameLong = true
			}
		}
	} else {
		v.slidingWindow(s.maxNumRefFrames, h.frameNum, maxFrameNum)
	}
	if !becameLong {
		p.shortRef = true
		// guard against streams exceeding max_num_ref_frames
		if h.adaptiveMarking {
			ns, nl := v.numShortLong()
			if ns+nl >= max(s.maxNumRefFrames, 1) {
				v.slidingWindow(s.maxNumRefFrames, h.frameNum, maxFrameNum)
			}
		}
	}
	v.refs = append(v.refs, p)
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

// modifyRefList applies ref_pic_list_modification (8.2.4.3 and H.8.2.2.3).
func (v *viewState) modifyRefList(list []*picture, n int, mods []refPicMod, h *sliceHeader, interView []*picture) []*picture {
	maxPicNum := 1 << h.sps.log2MaxFrameNum
	cur := h.frameNum
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
