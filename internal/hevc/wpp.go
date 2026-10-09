package hevc

import (
	"errors"
	"sync"
	"sync/atomic"
)

var errAborted = errors.New("hevc: another row failed")

// rowProgress tracks how far each CTB row of a slice segment has been
// decoded, for the row below to wait on.
type rowProgress struct {
	rows    []rowState
	aborted atomic.Bool
}

type rowState struct {
	done    atomic.Int32 // the columns decoded: the row's CTBs up to done-1
	waiting atomic.Bool
	mu      sync.Mutex
	cond    sync.Cond
}

func newRowProgress(n int) *rowProgress {
	rp := &rowProgress{rows: make([]rowState, n)}
	for i := range rp.rows {
		rp.rows[i].cond.L = &rp.rows[i].mu
	}
	return rp
}

func (rp *rowProgress) set(row, n int) {
	r := &rp.rows[row]
	r.done.Store(int32(n))
	if r.waiting.Load() {
		r.mu.Lock()
		r.cond.Broadcast()
		r.mu.Unlock()
	}
}

func (rp *rowProgress) abort() {
	rp.aborted.Store(true)
	for i := range rp.rows {
		r := &rp.rows[i]
		r.mu.Lock()
		r.cond.Broadcast()
		r.mu.Unlock()
	}
}

// wait blocks until row has n CTBs decoded, reporting false if decoding
// was aborted.
func (rp *rowProgress) wait(row, n int) bool {
	r := &rp.rows[row]
	if int(r.done.Load()) >= n {
		return true
	}
	r.mu.Lock()
	r.waiting.Store(true)
	for int(r.done.Load()) < n && !rp.aborted.Load() {
		r.cond.Wait()
	}
	r.waiting.Store(false)
	r.mu.Unlock()
	return !rp.aborted.Load()
}

// wppParallel reports whether the slice segment can have its CTB rows
// decoded in parallel: wavefronts without tiles, one entry point per row.
func (sd *sliceDec) wppParallel(data []byte) bool {
	if !sd.p.entropySync || sd.p.tiles || len(sd.substreams) == 0 || sd.d.threads < 2 {
		return false
	}
	s := sd.s
	if sd.h.segmentAddr/s.ctbW+len(sd.substreams) >= s.ctbH {
		return false
	}
	prev := 0
	for _, o := range sd.substreams {
		if o <= prev || o > len(data) {
			return false
		}
		prev = o
	}
	return true
}

// decodeWPP decodes the slice segment's CTB rows in parallel, each from
// its substream, each two CTBs behind the row above so that everything
// it predicts from has been decoded (as with the context storage of
// 9.3.2.2).
func (sd *sliceDec) decodeWPP(data []byte) error {
	s := sd.s
	rows := len(sd.substreams) + 1
	y0 := sd.h.segmentAddr / s.ctbW
	rp := newRowProgress(rows)
	errs := make([]error, rows)
	var wg sync.WaitGroup
	sem := make(chan struct{}, sd.d.threads)
	for k := range rows {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Rows start in order, so a row's wait never holds a slot the
			// row above needs.
			if k > 0 && !rp.wait(k-1, 1) {
				return
			}
			sem <- struct{}{}
			defer func() { <-sem }()
			w := new(sliceDec)
			*w = *sd
			start := 0
			if k > 0 {
				start = sd.substreams[k-1]
			}
			err := w.decodeRow(data, start, k, y0+k, k == rows-1, rp)
			if err != nil {
				errs[k] = err
				rp.abort()
				return
			}
			rp.set(k, s.ctbW)
			if k == rows-1 {
				if sd.p.dependentSlices {
					sd.chain.dsCtx = w.c.ctx
				}
				sd.chain.lastQPY = w.lastCUQpY
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil && err != errAborted {
			return err
		}
	}
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// decodeRow decodes row k (CTB row ctbY) of the slice segment from its
// substream at start, ending the segment if last.
func (sd *sliceDec) decodeRow(data []byte, start, k, ctbY int, last bool, rp *rowProgress) error {
	h, s, ps := sd.h, sd.s, sd.ps
	x := 0
	if k == 0 {
		x = h.segmentAddr % s.ctbW
		if h.dependent && h.segmentAddr > 0 {
			sd.qpY = sd.chain.lastQPY
			sd.lastCUQpY = sd.chain.lastQPY
		} else {
			sd.qpY = h.sliceQP
		}
	} else {
		sd.qpY = h.sliceQP
	}
	sd.c.init(data, start)
	sd.firstQGInTask = k > 0 || !h.dependent || x == 0
	first := x
	for ; x < s.ctbW; x++ {
		if k > 0 && !rp.wait(k-1, min(x+2, s.ctbW)) {
			return errAborted
		}
		sd.ctbAddrRS = ctbY*s.ctbW + x
		sd.ctbAddrTS = sd.ctbAddrRS
		switch {
		case x == 0:
			x0, y0 := 0, ctbY<<s.log2Ctb
			if sd.available(x0, y0, x0+s.ctbSize, y0-s.ctbSize) {
				sd.c.ctx = ps.wppRowCtx[ctbY-1]
			} else {
				sd.c.initContexts(sd.initType(), h.sliceQP)
			}
		case x != first:
		case h.dependent:
			sd.c.ctx = sd.chain.dsCtx
		default:
			sd.c.initContexts(sd.initType(), h.sliceQP)
		}
		if sd.ctbAddrTS >= sd.endTS {
			return errStream
		}
		if err := sd.ctu(x, ctbY); err != nil {
			return err
		}
		ps.ctbDecoded[sd.ctbAddrRS] = true
		sd.f.ctbDone(ctbY)
		if x == 1 || s.ctbW == 1 {
			ps.wppRowCtx[ctbY] = sd.c.ctx
		}
		end := sd.c.terminate() == 1
		if end {
			if !last {
				return errStream
			}
			return nil
		}
		if x == s.ctbW-1 {
			break
		}
		rp.set(k, x+1)
	}
	// The row ended without ending the segment: end_of_subset_one_bit, the
	// next row in the next substream.
	if last || sd.c.terminate() != 1 {
		return errStream
	}
	return nil
}
