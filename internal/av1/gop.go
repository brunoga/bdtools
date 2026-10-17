package av1

// The order frames are coded in, and what each refers to: a key frame
// every Keyint pictures; between, mini-GOPs of MiniGOP pictures coded as a
// pyramid: the last picture first (not shown: shown later from its slot,
// show_existing_frame), then the middle of each half, down to the
// pictures between, coded and shown at once. A frame's q index is the
// run's less an offset by its depth in the pyramid (the frames others
// predict from code finer). The coded frames are grouped into temporal
// units, each ending with one shown picture, in display order.

import "sort"

// Unit is a temporal unit: what a decoder needs to show the next
// picture.
type Unit struct {
	Data []byte
	// recon is the picture shown, as decoded (tests ask for it).
	recon *[3][]uint16
}

// frameJob is a frame to code.
type frameJob struct {
	disp    int // in display order
	src     *Source
	key     bool
	show    bool
	depth   int // in the pyramid: 0 key, 1 the mini-GOP's last, ...
	q       int
	refresh uint8
	refIdx  [REFS_PER_FRAME]int
}

// depthQ is how much finer (in q index) a frame at a depth codes.
var depthQ = [...]int{24, 24, 16, 8, 0, 0, 0, 0}

// miniGOP is the mini-GOP length.
func (e *Encoder) miniGOP() int {
	if e.o.MiniGOP > 0 {
		return e.o.MiniGOP
	}
	return 8
}

func (e *Encoder) keyint() int {
	if e.o.Keyint > 0 {
		return e.o.Keyint
	}
	return 250
}

// Encode takes the next picture, giving the temporal units that are
// complete (a mini-GOP is coded once all its pictures are in).
func (e *Encoder) Encode(src *Source) ([]Unit, error) {
	o := e.o
	cw, ch := (o.Width+1)/2, (o.Height+1)/2
	c := &Source{Y: make([]uint16, o.Width*o.Height), Cb: make([]uint16, cw*ch), Cr: make([]uint16, cw*ch), StrideY: o.Width, StrideC: cw}
	for y := range o.Height {
		copy(c.Y[y*o.Width:(y+1)*o.Width], src.Y[y*src.StrideY:])
	}
	for y := range ch {
		copy(c.Cb[y*cw:(y+1)*cw], src.Cb[y*src.StrideC:])
		copy(c.Cr[y*cw:(y+1)*cw], src.Cr[y*src.StrideC:])
	}
	e.queue = append(e.queue, &frameJob{disp: e.nIn, src: c})
	e.nIn++
	return e.drain(false), nil
}

// Flush codes the pictures held back, giving their units.
func (e *Encoder) Flush() ([]Unit, error) { return e.drain(true), nil }

// drain codes what the queue allows.
func (e *Encoder) drain(flush bool) []Unit {
	var out []Unit
	for len(e.queue) > 0 {
		first := e.queue[0]
		if first.disp%e.keyint() == 0 {
			first.key, first.show, first.depth = true, true, 0
			e.code(first, &out)
			e.queue = e.queue[1:]
			continue
		}
		// the mini-GOP: to the next key frame at most
		n := e.miniGOP()
		next := (first.disp/e.keyint() + 1) * e.keyint()
		n = min(n, next-first.disp)
		if len(e.queue) < n && !flush {
			break
		}
		n = min(n, len(e.queue))
		gop := e.queue[:n]
		e.queue = e.queue[n:]
		a := first.disp - 1
		byDisp := func(d int) *frameJob { return gop[d-first.disp] }
		last := byDisp(a + n)
		if n == 1 {
			last.show, last.depth = true, 1
			e.code(last, &out)
			continue
		}
		last.depth = 1
		e.code(last, &out)
		e.pyramid(a, a+n, 2, byDisp, &out)
	}
	return out
}

// pyramid codes the pictures a+1..b, b coded already (not shown).
func (e *Encoder) pyramid(a, b, depth int, byDisp func(int) *frameJob, out *[]Unit) {
	if b-a == 1 {
		e.showSlot(b, out)
		return
	}
	m := (a + b) / 2
	j := byDisp(m)
	j.depth = depth
	if m == a+1 {
		j.show = true
		e.code(j, out)
	} else {
		e.code(j, out)
		e.pyramid(a, m, depth+1, byDisp, out)
	}
	e.pyramid(m, b, depth+1, byDisp, out)
}

// code codes a frame: its references and slot chosen, its q set.
func (e *Encoder) code(j *frameJob, out *[]Unit) {
	j.q = e.o.QIndex
	if e.o.QIndex > 0 && !dbgFlatQ {
		j.q = max(e.o.QIndex-depthQ[j.depth], 1)
	}
	if j.key {
		j.refresh = 0xff
	} else {
		j.refIdx = e.refsFor(j.disp)
		if !j.show || e.miniGOP() == 1 {
			j.refresh = 1 << uint(e.slotFor(j.disp))
		}
	}
	obu := e.codeFrame(j)
	if j.key {
		e.pending = appendOBU(e.pending, obuSequenceHeader, e.seq.sequenceHeader())
	}
	e.pending = append(e.pending, obu...)
	if e.keepRecon {
		y, cb, cr := e.Recon()
		e.recons[j.disp] = &[3][]uint16{y, cb, cr}
	}
	if j.show {
		e.emit(j.disp, out)
	}
}

// showSlot shows a picture coded before, from its slot.
func (e *Encoder) showSlot(disp int, out *[]Unit) {
	for i, s := range e.slots {
		if s != nil && s.disp == disp {
			e.pending = append(e.pending, showExisting(i)...)
			e.emit(disp, out)
			return
		}
	}
	panic("av1: a picture to show is in no slot")
}

// emit ends a temporal unit with the picture disp.
func (e *Encoder) emit(disp int, out *[]Unit) {
	u := Unit{Data: append(appendOBU(nil, obuTemporalDelimiter, nil), e.pending...)}
	if e.keepRecon {
		u.recon = e.recons[disp]
		delete(e.recons, disp)
	}
	e.pending = e.pending[:0]
	*out = append(*out, u)
}

// refsFor picks a frame's references among the slots' frames: LAST,
// LAST2 and LAST3 the nearest before it, GOLDEN the farthest before it;
// BWDREF and ALTREF2 the nearest after it, ALTREF the farthest after it
// (those before it when none is after).
func (e *Encoder) refsFor(disp int) [REFS_PER_FRAME]int {
	slotOf := map[*refFrameState]int{}
	var past, future []*refFrameState
	for i := len(e.slots) - 1; i >= 0; i-- {
		s := e.slots[i]
		if s == nil {
			continue
		}
		if _, seen := slotOf[s]; seen {
			continue
		}
		slotOf[s] = i
		if s.disp < disp {
			past = append(past, s)
		} else {
			future = append(future, s)
		}
	}
	sort.Slice(past, func(i, j int) bool { return past[i].disp > past[j].disp })
	sort.Slice(future, func(i, j int) bool { return future[i].disp < future[j].disp })
	pick := func(l []*refFrameState, i int) *refFrameState { return l[min(i, len(l)-1)] }
	if len(future) == 0 {
		future = past[:1]
	}
	refs := [REFS_PER_FRAME]*refFrameState{
		pick(past, 0), pick(past, 1), pick(past, 2), pick(past, len(past)-1),
		pick(future, 0), pick(future, 1), pick(future, len(future)-1),
	}
	var idx [REFS_PER_FRAME]int
	for i, r := range refs {
		idx[i] = slotOf[r]
	}
	return idx
}

// slotFor is the slot a kept frame goes in: an empty one (or one whose
// frame another holds too), else the one
// whose frame is the oldest of those before the frame, the newest of them
// kept (it is the next frames' LAST or GOLDEN).
func (e *Encoder) slotFor(disp int) int {
	count := map[*refFrameState]int{}
	for _, s := range e.slots {
		count[s]++
	}
	for i, s := range e.slots {
		if s == nil || count[s] > 1 {
			return i // empty, or its frame is in another slot too
		}
	}
	newest := -1
	for _, s := range e.slots {
		if s.disp < disp && s.disp > newest {
			newest = s.disp
		}
	}
	best, bestDisp := -1, 0
	for i, s := range e.slots {
		if s.disp < disp && s.disp != newest && (best < 0 || s.disp < bestDisp) {
			best, bestDisp = i, s.disp
		}
	}
	if best < 0 {
		// every other slot is after the frame: the farthest
		for i, s := range e.slots {
			if s.disp != newest && (best < 0 || s.disp > bestDisp) {
				best, bestDisp = i, s.disp
			}
		}
	}
	return best
}
