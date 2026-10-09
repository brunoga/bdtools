package hevc

import (
	"sync"
	"sync/atomic"
)

// Frame threading: the pictures are decoded on goroutines of their own,
// several at a time, while the caller's goroutine parses what comes next.
// A picture's CTB rows are loop filtered as they are decoded, and a row
// done is final; a picture predicting from another waits for the rows it
// reads to be final.

// frame is a picture being decoded: what decoding it keeps, and its slice
// segments.
type frame struct {
	d       *Decoder
	threads int
	pic     *picture
	sps     *sps
	pps     *pps
	ps      picState
	pending []*sliceDec
	data    [][]byte // the slice segments' data, copied (reused)
	refs    []*picture

	// Loop filter scratch.
	bsV, bsH []uint8
	saoLines [3][]uint16 // each CTB row's first and last lines

	// Row scheduling: CTBs decoded per row, and each row's steps.
	rowCTBs []atomic.Int32
	mu      sync.Mutex
	state   []uint16
	filter  bool // the deblocking runs
	saoOn   bool // and SAO
	errors  atomic.Int32
}

// A row's steps: decoded; vertical edges, horizontal edges, copied for SAO
// and SAO, each claimed then done.
const (
	rowDecoded = 1 << iota
	rowVClaimed
	rowVDone
	rowHClaimed
	rowHDone
	rowCClaimed
	rowCDone
	rowSClaimed
	rowSDone
	rowFinal
)

// progress is how far a picture is decoded: its final CTB rows.
type progress struct {
	final   []atomic.Bool
	waiters atomic.Int32
	mu      sync.Mutex
	cond    sync.Cond
	users   atomic.Int32 // frames decoding it or predicting from it
}

func (p *picture) resetProgress(rows int) {
	if len(p.prog.final) != rows {
		p.prog.final = make([]atomic.Bool, rows)
	}
	for i := range p.prog.final {
		p.prog.final[i].Store(false)
	}
	if p.prog.cond.L == nil {
		p.prog.cond.L = &p.prog.mu
	}
}

// setFinal marks CTB row r final.
func (p *picture) setFinal(r int) {
	pr := &p.prog
	pr.final[r].Store(true)
	if pr.waiters.Load() > 0 {
		pr.mu.Lock()
		pr.cond.Broadcast()
		pr.mu.Unlock()
	}
}

// setAllFinal marks the whole picture final (one made up, or filled
// otherwise).
func (p *picture) setAllFinal() {
	for r := range p.prog.final {
		p.setFinal(r)
	}
}

// waitRows blocks until CTB rows [r0, r1] are final.
func (p *picture) waitRows(r0, r1 int) {
	pr := &p.prog
	// Lines outside the picture read its edge rows.
	last := len(pr.final) - 1
	r0, r1 = min(max(r0, 0), last), min(max(r1, 0), last)
	for r := r0; r <= r1; r++ {
		if pr.final[r].Load() {
			continue
		}
		pr.mu.Lock()
		pr.waiters.Add(1)
		for !pr.final[r].Load() {
			pr.cond.Wait()
		}
		pr.waiters.Add(-1)
		pr.mu.Unlock()
	}
}

// waitLines blocks until the luma lines [y0, y1] are final.
func (p *picture) waitLines(y0, y1 int) {
	if p.prog.final == nil {
		return
	}
	l := p.sps.log2Ctb
	p.waitRows(y0>>l, y1>>l)
}

// done reports whether the whole picture is final.
func (p *picture) done() bool {
	for i := range p.prog.final {
		if !p.prog.final[i].Load() {
			return false
		}
	}
	return true
}

// waitAll blocks until the whole picture is final.
func (p *picture) waitAll() {
	if p.prog.final != nil {
		p.waitRows(0, len(p.prog.final)-1)
	}
}

// begin readies the frame for pic.
func (f *frame) begin(pic *picture, s *sps, p *pps) {
	f.pic, f.sps, f.pps = pic, s, p
	f.pending = f.pending[:0]
	f.refs = f.refs[:0]
	f.ps.slices = f.ps.slices[:0]
	f.errors.Store(0)
	pic.resetProgress(s.ctbH)
}

// addSlice queues a slice segment, with a copy of its data.
func (f *frame) addSlice(sd *sliceDec, data []byte) {
	i := len(f.pending)
	if i == len(f.data) {
		f.data = append(f.data, nil)
	}
	f.data[i] = append(f.data[i][:0], data...)
	sd.data = f.data[i]
	f.pending = append(f.pending, sd)
	for l := range 2 {
		for _, r := range sd.refList[l] {
			f.addRef(r)
		}
	}
}

func (f *frame) addRef(r *picture) {
	for _, c := range f.refs {
		if c == r {
			return
		}
	}
	r.prog.users.Add(1)
	f.refs = append(f.refs, r)
}

// run decodes the frame's picture and filters it, then releases what it
// holds and gives the frame back.
func (f *frame) run() {
	f.prepare()
	f.decodeSlices()
	// Rows a broken slice left are as they are; filter what is left.
	for r := range f.state {
		f.rowCTBs[r].Store(int32(f.sps.ctbW))
		f.mu.Lock()
		f.state[r] |= rowDecoded
		f.mu.Unlock()
	}
	f.parallel(f.sps.ctbH, 1, func(_, _ int) { f.advance() })
	f.pic.setAllFinal()
	for _, r := range f.refs {
		r.prog.users.Add(-1)
	}
	f.pic.prog.users.Add(-1)
	f.d.asyncErrors.Add(int64(f.errors.Load()))
	f.d.frames <- f
}

// prepare sizes and clears the picture wide state.
func (f *frame) prepare() {
	s := f.sps
	ps := &f.ps
	w4 := (s.ctbW << s.log2Ctb) >> 2
	h4 := (s.ctbH << s.log2Ctb) >> 2
	if ps.w4 != w4 || ps.h4 != h4 {
		ps.w4, ps.h4 = w4, h4
		n := w4 * h4
		ps.predMode = make([]uint8, n)
		ps.intraMode = make([]uint8, n)
		ps.qpY = make([]int8, n)
		ps.noFilter = make([]bool, n)
		ps.tuEdgeV = make([]uint8, n)
		ps.tuEdgeH = make([]uint8, n)
		ps.cbfLuma = make([]bool, n)
	}
	if len(ps.ctDepth) != s.minCbW*s.minCbH {
		ps.ctDepth = make([]uint8, s.minCbW*s.minCbH)
	}
	nCtb := s.ctbW * s.ctbH
	if len(ps.sao) != nCtb {
		ps.sao = make([][3]saoParams, nCtb)
		ps.ctbSlice = make([]int, nCtb)
		ps.ctbDecoded = make([]bool, nCtb)
		ps.wppRowCtx = make([][numContexts]uint8, s.ctbH)
	}
	clear(ps.ctbDecoded)
	clear(ps.tuEdgeV)
	clear(ps.tuEdgeH)
	clear(ps.noFilter)
	clear(ps.cbfLuma)
	clear(ps.sao)
	clear(f.pic.mvf)
	if len(f.state) != s.ctbH {
		f.state = make([]uint16, s.ctbH)
		f.rowCTBs = make([]atomic.Int32, s.ctbH)
	}
	clear(f.state)
	for i := range f.rowCTBs {
		f.rowCTBs[i].Store(0)
	}
	f.filter = len(ps.slices) > 0 && !noLoopFilter
	f.saoOn = false
	if f.filter && s.sao {
		for _, h := range ps.slices {
			if h.saoLuma || h.saoChroma {
				f.saoOn = true
			}
		}
	}
	if f.filter {
		w4, h4 := (s.width+3)>>2, (s.height+3)>>2
		if len(f.bsV) != w4*h4 {
			f.bsV = make([]uint8, w4*h4)
			f.bsH = make([]uint8, w4*h4)
		}
	}
	if f.saoOn {
		pic := f.pic
		for ci, stride := range [3]int{pic.strideY, pic.strideC, pic.strideC} {
			if n := 2 * s.ctbH * stride; len(f.saoLines[ci]) != n {
				f.saoLines[ci] = make([]uint16, n)
			}
		}
	}
}

// ctbDone counts a CTB of row r decoded, filtering what that allows.
func (f *frame) ctbDone(r int) {
	if int(f.rowCTBs[r].Add(1)) != f.sps.ctbW {
		return
	}
	f.mu.Lock()
	f.state[r] |= rowDecoded
	if !f.filter {
		f.state[r] |= rowFinal
		f.pic.setFinal(r)
	}
	f.mu.Unlock()
	f.advance()
}

// advance runs the row steps that can run, until none can.
func (f *frame) advance() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for {
		r, step := f.claim()
		if step == 0 {
			return
		}
		f.mu.Unlock()
		switch step {
		case rowVClaimed:
			f.deblockV(r)
		case rowHClaimed:
			f.deblockH(r)
		case rowCClaimed:
			f.saoCopy(r)
		case rowSClaimed:
			f.sao(r)
		}
		f.mu.Lock()
		f.state[r] |= step << 1 // the step's done bit
		f.finals(r - 1)
		f.finals(r)
	}
}

// has reports whether row r has every bit of b, rows outside the picture
// counting as having them.
func (f *frame) has(r int, b uint16) bool {
	return r < 0 || r >= len(f.state) || f.state[r]&b == b
}

// claim finds a row step that can run and claims it (with f.mu held),
// returning 0 when there is none. A row's vertical edges need it and the
// rows around it decoded (the strengths of its top edge are of the row
// above; the row below predicts from its samples unfiltered); its horizontal
// edges the vertical ones of both; its copy for SAO its own and the next
// row's horizontal edges (the next row's top edge changes its last
// lines); its SAO the copies of the rows around it.
func (f *frame) claim() (int, uint16) {
	if !f.filter {
		return 0, 0
	}
	for r, st := range f.state {
		switch {
		case st&rowVClaimed == 0:
			if st&rowDecoded != 0 && f.has(r-1, rowDecoded) && f.has(r+1, rowDecoded) {
				f.state[r] |= rowVClaimed
				return r, rowVClaimed
			}
		case st&rowHClaimed == 0:
			if st&rowVDone != 0 && f.has(r-1, rowVDone) {
				f.state[r] |= rowHClaimed
				return r, rowHClaimed
			}
		case !f.saoOn:
		case st&rowCClaimed == 0:
			if st&rowHDone != 0 && f.has(r+1, rowHDone) {
				f.state[r] |= rowCClaimed
				return r, rowCClaimed
			}
		case st&rowSClaimed == 0:
			if st&rowCDone != 0 && f.has(r-1, rowCDone) && f.has(r+1, rowCDone) {
				f.state[r] |= rowSClaimed
				return r, rowSClaimed
			}
		}
	}
	return 0, 0
}

// finals marks row r final once nothing changes it any more.
func (f *frame) finals(r int) {
	if r < 0 || r >= len(f.state) || f.state[r]&rowFinal != 0 {
		return
	}
	var ok bool
	switch {
	case f.saoOn:
		ok = f.state[r]&rowSDone != 0
	default:
		ok = f.state[r]&rowHDone != 0 && f.has(r+1, rowHDone)
	}
	if ok {
		f.state[r] |= rowFinal
		f.pic.setFinal(r)
	}
}
