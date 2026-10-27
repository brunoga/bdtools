package av1

// The order frames are coded in, and what each refers to: a key frame
// every Keyint pictures; between, mini-GOPs of MiniGOP pictures coded as a
// pyramid: the last picture first (not shown: shown later from its slot,
// show_existing_frame), then the middle of each half, down to the
// pictures between, coded and shown at once. A frame's q index is the
// run's less an offset by its depth in the pyramid (the frames others
// predict from code finer). The coded frames are grouped into temporal
// units, each ending with one shown picture, in display order.

import (
	"sort"
	"sync"
)

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
	orig    *Source // the source before the temporal filter (an anchor's or a key frame's)
	key     bool
	show    bool
	depth   int // in the pyramid: 0 key, 1 the mini-GOP's last, ...
	q       int
	refresh uint8
	refIdx  [REFS_PER_FRAME]int
	// refs are the frames refIdx names (as planned); state is what this
	// frame will be as a reference, filled once it is coded.
	refs  [REFS_PER_FRAME]*refFrameState
	state *refFrameState
	frame *frameEnc
	obu   []byte
	recon *[3][]uint16
	// sbQ is the superblocks' q indexes (the temporal model's), or nil;
	// sbLambda their lambdas' scales (the model's, tplLambda), or nil
	sbQ      []int
	sbLambda []float64
}

// step is a planned step of the stream: a frame coded, or one shown from
// a slot (show_existing_frame).
type step struct {
	job      *frameJob
	showSlot int // with job nil
	disp     int
}

// recentSources is how many source pictures are kept (for the temporal
// model: a mini-GOP and the anchors before it).
const recentSources = 40

// dbgNoTPL turns the temporal model off. On the two 120-picture clips
// it is 0.4% better scaling the superblocks' lambdas (Frozen II 0.9%),
// and was 0.6% worse setting their q indexes (delta_q, the rows coded
// one after another).
var dbgNoTPL = false

// dbgNoOverlay shows a filtered anchor itself.
var dbgNoOverlay bool

// depthQ is how much finer (in q index) a frame at a depth codes, and
// leafQ how much coarser the pictures no other refers to. (Measured on
// two 1080p disc clips: 40, 40, 28, 16 better than 24, 24, 16, 8 by
// 3.5% BD-rate; on the 120-picture clips, with every tool in and
// mini-GOPs of 8, 52, 52, 36, 20 0.9% better than that (Frozen II 1.8%),
// 64, 64, 44, 24 0.7% (Blade Runner 2049 0.9% worse), 32, 32, 22, 12
// 1.2% worse; the leaves 8 coarser 0.2% better; mini-GOPs of 16 with
// these 1.1% better than of 8; of 32 with 52, 52, 40, 30, 20, 10 0.8%
// better than of 16, and 1.2 times faster, 60, 60, 52, 40, 28, 16 1.9%
// worse, 44, 44, 34, 24, 14, 6 and 52, 52, 40, 28, 16, 8 0.7% better.)
var (
	depthQ = [...]int{52, 52, 40, 30, 20, 10, 0, 0}
	leafQ  = 8
)

// keyQ is how much finer key frames code.
var keyQ = 56

// miniGOP is the mini-GOP length.
func (e *Encoder) miniGOP() int {
	if e.o.MiniGOP > 0 {
		return e.o.MiniGOP
	}
	return 32
}

func (e *Encoder) keyint() int {
	if e.o.Keyint > 0 {
		return e.o.Keyint
	}
	return 250
}

// Encode takes the next picture, giving the temporal units that are
// complete (a mini-GOP is coded once all its pictures are in, and while
// the next ones come in: its units come from a later call, or Flush).
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
	e.recent[e.nIn] = c
	delete(e.recent, e.nIn-recentSources)
	e.nIn++
	return e.drain(false), nil
}

// Flush codes the pictures held back, giving their units.
func (e *Encoder) Flush() ([]Unit, error) {
	out := e.drain(true)
	e.collect(0, &out)
	return out, nil
}

// drain codes what the queue allows.
func (e *Encoder) drain(flush bool) []Unit {
	var out []Unit
	for len(e.queue) > 0 {
		first := e.queue[0]
		if first.disp%e.keyint() == 0 {
			// the key frame filtered with the pictures after it (to the
			// next key frame), and shown so
			if tfKeyAfter > 0 && !dbgNoTF && e.o.QIndex > 0 && e.keyint() > 1 {
				after := min(tfKeyAfter, e.keyint()-1)
				if len(e.queue) < 1+after && !flush {
					break
				}
				after = min(after, len(e.queue)-1)
				if after > 0 && first.orig == nil {
					window := make([]*Source, 0, 1+after)
					for _, j := range e.queue[:1+after] {
						window = append(window, j.src)
					}
					first.orig = first.src
					first.src = temporalFilter(window, 0, e.o.QIndex, e.o.BitDepth, e.o.Width, e.o.Height)
				}
			}
			first.key, first.show, first.depth = true, true, 0
			e.tfPast = []*Source{first.src}
			if first.orig != nil {
				e.tfPast[0] = first.orig
			}
			// coded with the mini-GOP after it, once the temporal model
			// has costed both
			e.runKey(&out)
			e.key = []step{e.plan(first)}
			e.queue = e.queue[1:]
			continue
		}
		// the mini-GOP: to the next key frame at most
		n := e.miniGOP()
		next := (first.disp/e.keyint() + 1) * e.keyint()
		n = min(n, next-first.disp)
		// the temporal filter's pictures after the anchor (to the next
		// key frame)
		after := 0
		if !dbgNoTF {
			after = min(tfAfter, next-first.disp-n)
		}
		if len(e.queue) < n+after && !flush {
			break
		}
		n = min(n, len(e.queue))
		after = min(after, len(e.queue)-n)
		var ahead []*Source
		for _, j := range e.queue[n : n+after] {
			ahead = append(ahead, j.src)
		}
		gop := e.queue[:n]
		e.queue = e.queue[n:]
		a := first.disp - 1
		e.gopStart = a
		byDisp := func(d int) *frameJob { return gop[d-first.disp] }
		last := byDisp(a + n)
		e.filterAnchor(gop, ahead)
		if n == 1 {
			e.runKey(&out)
			last.show, last.depth = true, 1
			e.run([]step{e.plan(last)}, &out)
			continue
		}
		last.depth = 1
		steps := []step{e.plan(last)}
		e.pyramid(a, a+n, 2, byDisp, &steps)
		e.tpl(append(e.key[:len(e.key):len(e.key)], steps...))
		e.runKey(&out)
		e.run(steps, &out)
	}
	if flush {
		e.runKey(&out)
	}
	return out
}

// runKey codes the key frame planned (and not yet coded), if any.
func (e *Encoder) runKey(out *[]Unit) {
	if e.key != nil {
		e.run(e.key, out)
		e.key = nil
	}
}

// pyramid plans the pictures a+1..b, b planned already (not shown).
func (e *Encoder) pyramid(a, b, depth int, byDisp func(int) *frameJob, steps *[]step) {
	if b-a == 1 {
		if j := byDisp(b); j.orig != nil && !dbgNoOverlay {
			// the anchor was coded filtered: shown by an overlay, the
			// source itself coded from it (and the others)
			*steps = append(*steps, e.plan(&frameJob{disp: b, src: j.orig, show: true, depth: len(depthQ) - 1}))
			return
		}
		*steps = append(*steps, e.planShow(b))
		return
	}
	m := (a + b) / 2
	j := byDisp(m)
	j.depth = depth
	if m == a+1 {
		j.show = true
		*steps = append(*steps, e.plan(j))
	} else {
		*steps = append(*steps, e.plan(j))
		e.pyramid(a, m, depth+1, byDisp, steps)
	}
	e.pyramid(m, b, depth+1, byDisp, steps)
}

// plan plans a frame: its q, its references and the slot it goes in,
// as the slots will be when it is coded (the slots hold the frames to
// come, filled as they are coded).
func (e *Encoder) plan(j *frameJob) step {
	j.q = e.o.QIndex
	if e.o.QIndex > 0 && !dbgFlatQ {
		j.q = max(e.o.QIndex-depthQ[j.depth], 1)
		switch {
		case j.key:
			j.q = max(e.o.QIndex-keyQ, 1)
		case j.show && e.miniGOP() > 1:
			// a leaf (shown at once: kept in no slot)
			j.q = min(e.o.QIndex+leafQ, 255)
		}
	}
	j.state = &refFrameState{disp: j.disp, depth: j.depth, done: make(chan struct{})}
	if j.key {
		j.state.depth = 0
	}
	if j.key {
		j.refresh = 0xff
	} else {
		j.refIdx = e.refsFor(j.disp, j.depth)
		for i, s := range j.refIdx {
			j.refs[i] = e.slots[s]
		}
		if !j.show || e.miniGOP() == 1 {
			j.refresh = 1 << uint(e.slotFor(j.disp))
		}
	}
	for i := range NUM_REF_FRAMES {
		if j.refresh>>uint(i)&1 != 0 {
			e.slots[i] = j.state
		}
	}
	return step{job: j, disp: j.disp}
}

// planShow plans a picture coded before shown from its slot.
func (e *Encoder) planShow(disp int) step {
	for i, s := range e.slots {
		if s != nil && s.disp == disp {
			return step{showSlot: i, disp: disp}
		}
	}
	panic("av1: a picture to show is in no slot")
}

// gopRun is a mini-GOP's (or key frame's) steps being coded.
type gopRun struct {
	steps []step
	done  chan struct{} // closed once every frame is coded
}

// maxRuns is how many mini-GOPs are coded at once: the next ones' frames
// are coded while this one's below them are. (On the six clips: 4 coded
// 1.07 times the pictures a second of 2, 3 1.04 times.)
const maxRuns = 4

// run starts coding the planned frames, each once its references are
// coded (frames that do not depend on each other at once, mini-GOPs
// too), and gives the temporal units of the runs done, in order (waiting
// for the oldest while maxRuns are coded).
func (e *Encoder) run(steps []step, out *[]Unit) {
	var wg sync.WaitGroup
	for _, s := range steps {
		if j := s.job; j != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for _, r := range j.refs {
					if r != nil {
						<-r.done
					}
				}
				j.obu = e.codeFrame(j)
				j.src, j.orig = nil, nil // held by Encode and the temporal filter as long as they need them
				if e.keepRecon {
					y, cb, cr := j.frame.recon(e.o.Width, e.o.Height)
					j.recon = &[3][]uint16{y, cb, cr}
				} else {
					j.frame = nil // a reference's picture is its state's
				}
				close(j.state.done)
				if dbgSerialFrames {
					return
				}
			}()
			if dbgSerialFrames {
				wg.Wait()
			}
		}
	}
	r := &gopRun{steps: steps, done: make(chan struct{})}
	go func() {
		wg.Wait()
		close(r.done)
	}()
	e.runs = append(e.runs, r)
	keep := maxRuns - 1
	if dbgSerialFrames {
		keep = 0
	}
	e.collect(keep, out)
}

// collect emits the runs done, oldest first, waiting while more than
// keep are left.
func (e *Encoder) collect(keep int, out *[]Unit) {
	for len(e.runs) > 0 {
		r := e.runs[0]
		if len(e.runs) > keep {
			<-r.done
		} else {
			select {
			case <-r.done:
			default:
				return
			}
		}
		e.runs = e.runs[1:]
		e.emitRun(r.steps, out)
	}
}

// emitRun gives a coded run's temporal units.
func (e *Encoder) emitRun(steps []step, out *[]Unit) {
	for _, s := range steps {
		j := s.job
		if j == nil {
			e.pending = append(e.pending, showExisting(s.showSlot)...)
			e.emit(s.disp, out)
			continue
		}
		if j.key {
			e.pending = appendOBU(e.pending, obuSequenceHeader, e.seq.sequenceHeader())
		}
		e.pending = append(e.pending, j.obu...)
		e.recons[j.disp] = j.recon
		e.frame = j.frame
		if j.show {
			e.emit(j.disp, out)
		}
		j.frame, j.obu, j.src, j.orig = nil, nil, nil, nil
	}
}

// dbgSerialFrames codes a mini-GOP's frames one at a time.
var dbgSerialFrames bool

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

// refsPrevDepth is the deepest of the earlier mini-GOPs' frames a frame
// may use, so that the next mini-GOP is coded while the frames below
// this one are. (On the two 120-picture clips, the anchors against no
// limit: 2 cost 0.2% BD-rate and coded 1.12 times faster, 1 cost 1.1%
// and coded 1.37 times faster. On the six clips, the frames below the
// anchors too, against no limit: 2 the same size and 1.18 times the
// pictures a second (with maxRuns 4), 3 0.2% smaller and 1.10 times,
// 4 0.1% smaller and 1.09 times.)
var refsPrevDepth = 2

// refUsable reports whether a frame of a depth may reference one of
// another (earlier: of an earlier mini-GOP): an earlier one to
// refsPrevDepth; within a mini-GOP, only frames above it in the pyramid,
// so that the frames of a depth are coded at once.
func refUsable(refDepth, depth int, earlier bool) bool {
	if earlier {
		return refDepth <= refsPrevDepth
	}
	return refDepth < depth || depth <= 1 && refDepth <= 1
}

// refsFor picks a frame's references among the slots' frames it may use
// (refUsable): LAST, LAST2 and LAST3 the nearest before it, GOLDEN the
// farthest before it; BWDREF and ALTREF2 the nearest after it, ALTREF
// the farthest after it (those before it when none is after).
func (e *Encoder) refsFor(disp, depth int) [REFS_PER_FRAME]int {
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
		if !refUsable(s.depth, depth, s.disp <= e.gopStart) {
			continue
		}
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
