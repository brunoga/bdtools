package hevc

import (
	"runtime"
	"sort"
	"sync/atomic"
)

// Picture is a decoded picture in output order: 4:2:0 planes of samples
// (uint16 whatever the bit depth), its conformance window applied by
// Width, Height and the planes' offsets.
type Picture struct {
	Width, Height    int
	BitDepth         int
	Y, Cb, Cr        []uint16 // from the window's top left
	StrideY, StrideC int
	PTS              int64
	// FrameRateNum and FrameRateDen are the VUI's timing (0 if absent).
	FrameRateNum, FrameRateDen int
	// Primaries, Transfer and Matrix are the VUI's colour description
	// (H.273 code points; 2, unspecified, when there is none).
	Primaries, Transfer, Matrix int
	FullRange                   bool
}

// mvField is a prediction block's motion, kept per 4x4 block for the
// pictures that come after (temporal prediction) and the deblocking.
type mvField struct {
	mv     [2]mv
	refIdx [2]int8
	pred   uint8 // bit 0 list 0, bit 1 list 1; 0 intra
}

type mv struct{ x, y int16 }

// Reference marking.
const (
	unused = iota
	shortTerm
	longTerm
)

type picture struct {
	y, cb, cr []uint16
	strideY   int
	strideC   int
	poc       int
	ref       int // unused, shortTerm, longTerm
	output    bool
	latency   int
	pts       int64
	sps       *sps
	mvf       []mvField // 4x4 blocks
	mvfW      int
	// Each slice's reference POCs and long-term flags, by list and index,
	// for temporal prediction from this picture; slice of each CTB.
	refPOC   [][2][16]int32
	refLT    [][2][16]bool
	ctbSlice []int16
	corrupt  bool // made up for a missing reference
}

// Decoder decodes an H.265/HEVC elementary stream, access unit by access
// unit (or any run of whole NAL units).
type Decoder struct {
	spss [16]*sps
	ppss [64]*pps

	dpb   []*picture
	pool  []*picture
	cur   *picture
	sps   *sps // the active SPS
	pps   *pps
	first bool // no picture yet, or after an end of sequence

	pocTid0    int // the POC of the previous TemporalId 0 picture
	noRASLOut  bool
	curPicType int
	slices     []*sliceHeader
	prevSlice  *sliceHeader
	pts        int64
	errors     int
	pending    []*sliceDec // the picture's slice segments, to decode
	lastPPS    *pps        // the PPS last activated
	threads    int         // goroutines decoding a picture

	// Loop filter scratch.
	bsV, bsH []uint8
	saoSrc   [3][]uint16
	out      func(*Picture) error

	pic picState
}

// New makes a decoder.
func New() *Decoder { return &Decoder{first: true, threads: runtime.GOMAXPROCS(0)} }

// Errors is how many slices could not be decoded.
func (d *Decoder) Errors() int { return d.errors }

// Decode decodes the NAL units of an access unit given at pts, giving out
// the pictures that are due, in output order.
func (d *Decoder) Decode(au []byte, pts int64, out func(*Picture) error) error {
	d.out = out
	d.pts = pts
	// The slices queued are decoded before au, which they point into, goes.
	defer d.decodeSlices()
	for _, n := range nalUnits(au) {
		if err := d.nal(&n); err != nil {
			return err
		}
	}
	return nil
}

// Flush finishes the last picture and gives out every picture still due.
func (d *Decoder) Flush(out func(*Picture) error) error {
	d.out = out
	if err := d.finishPicture(); err != nil {
		return err
	}
	for {
		ok, err := d.bump()
		if err != nil || !ok {
			return err
		}
	}
}

func (d *Decoder) nal(n *nalUnit) error {
	if n.layer != 0 {
		return nil // other layers (MV-HEVC's second view, SHVC)
	}
	r := &bits{b: n.rbsp}
	switch {
	case n.typ == nalVPS:
		return nil
	case n.typ == nalSPS:
		if err := d.parseSPS(r); err != nil {
			if _, ok := err.(unsupported); ok {
				return err
			}
			d.errors++
		}
		return nil
	case n.typ == nalPPS:
		if err := d.parsePPS(r); err != nil {
			if _, ok := err.(unsupported); ok {
				return err
			}
			d.errors++
		}
		return nil
	case n.typ == nalEOS || n.typ == nalEOB:
		if err := d.finishPicture(); err != nil {
			return err
		}
		d.first = true
		return nil
	case n.typ <= nalCRA && (n.typ <= nalRASLR || n.typ >= nalBLAWLP):
		return d.slice(n, r)
	}
	return nil
}

func isIRAP(t int) bool { return t >= nalBLAWLP && t <= nalRsvIRAP23 }

func isRASL(t int) bool { return t == nalRASLN || t == nalRASLR }

// computePOC derives a picture's POC (8.3.1) from its slice header.
func (d *Decoder) computePOC(h *sliceHeader, n *nalUnit, s *sps) int {
	maxLsb := 1 << s.log2MaxPOCLsb
	if isIRAP(n.typ) && d.noRASLOutFor(n.typ) {
		return h.pocLsb
	}
	prevLsb := d.pocTid0 & (maxLsb - 1)
	prevMsb := d.pocTid0 - prevLsb
	msb := prevMsb
	switch {
	case h.pocLsb < prevLsb && prevLsb-h.pocLsb >= maxLsb/2:
		msb += maxLsb
	case h.pocLsb > prevLsb && h.pocLsb-prevLsb > maxLsb/2:
		msb -= maxLsb
	}
	if n.typ >= nalBLAWLP && n.typ <= nalBLANLP {
		msb = 0
	}
	return msb + h.pocLsb
}

// noRASLOutFor reports NoRaslOutputFlag of an IRAP picture of type t: set
// for IDR and BLA pictures, and for a CRA one that starts the stream.
func (d *Decoder) noRASLOutFor(t int) bool {
	return t != nalCRA || d.first
}

// slice handles a coded slice segment.
func (d *Decoder) slice(n *nalUnit, r *bits) error {
	// Peek at first_slice_segment_in_pic_flag: a new picture.
	if len(n.rbsp) == 0 {
		return nil
	}
	if n.rbsp[0]&0x80 != 0 {
		if err := d.finishPicture(); err != nil {
			return err
		}
	} else if d.cur == nil {
		return nil // a picture whose first slice is missing (or was skipped)
	}
	var prev *sliceHeader
	if n.rbsp[0]&0x80 == 0 {
		prev = d.prevSlice
	}
	h, err := d.parseSliceHeader(r, n, prev)
	if err != nil {
		if _, ok := err.(unsupported); ok {
			return err
		}
		d.errors++
		return nil
	}
	if h.firstSliceInPic {
		if isRASL(n.typ) && d.noRASLOut {
			return nil // the leading pictures of a CRA the stream starts with
		}
		if err := d.startPicture(h, n); err != nil {
			if _, ok := err.(unsupported); ok {
				return err
			}
			d.errors++
			return nil
		}
	}
	if d.cur == nil {
		return nil
	}
	if !h.dependent {
		d.prevSlice = h
	}
	if err := d.decodeSlice(h, n, r); err != nil {
		d.errors++
	}
	return nil
}

// decodeSlices decodes the picture's slice segments: each slice (with
// its dependent segments) on its own goroutine, as nothing is predicted
// across slices. Each CTB's slice is known before, from the segments'
// addresses, for the availability of neighbours.
func (d *Decoder) decodeSlices() {
	pend := d.pending
	d.pending = d.pending[:0]
	if len(pend) == 0 {
		return
	}
	ps, s := &d.pic, d.sps
	nCtb := s.ctbW * s.ctbH
	var tasks [][]*sliceDec
	for k, sd := range pend {
		p := sd.p
		start := p.rsToTS[sd.h.segmentAddr]
		end := nCtb
		if k+1 < len(pend) {
			end = max(start, p.rsToTS[pend[k+1].h.segmentAddr])
		}
		sd.endTS = end
		for ts := start; ts < end; ts++ {
			rs := p.tsToRS[ts]
			ps.ctbSlice[rs] = sd.sliceIdx
			d.cur.ctbSlice[rs] = sd.refIdx
		}
		if sd.h.dependent && len(tasks) > 0 {
			sd.chain = tasks[len(tasks)-1][0].chain
			tasks[len(tasks)-1] = append(tasks[len(tasks)-1], sd)
		} else {
			sd.chain = &sliceChain{}
			tasks = append(tasks, []*sliceDec{sd})
		}
	}
	var failed atomic.Int32
	d.parallel(len(tasks), 1, func(lo, hi int) {
		for _, task := range tasks[lo:hi] {
			for _, sd := range task {
				if err := sd.decode(sd.data); err != nil {
					failed.Add(1)
				}
			}
		}
	})
	d.errors += int(failed.Load())
}

// startPicture begins a picture: its POC, reference picture set, output
// of what it frees, and a buffer.
func (d *Decoder) startPicture(h *sliceHeader, n *nalUnit) error {
	p := d.ppss[h.ppsID]
	s := p.sps
	irap := isIRAP(n.typ)
	if irap {
		d.noRASLOut = d.noRASLOutFor(n.typ)
	}
	if irap && d.noRASLOut {
		// C.5.2.2: an IRAP that starts afresh outputs (or, told so,
		// drops) what is waiting, and empties the buffer.
		noOutput := h.noOutputOfPrior
		if n.typ == nalCRA {
			noOutput = true
		}
		if !noOutput {
			for {
				ok, err := d.bump()
				if err != nil {
					return err
				}
				if !ok {
					break
				}
			}
		}
		d.pool = append(d.pool, d.dpb...)
		d.dpb = d.dpb[:0]
	}
	if d.sps != s {
		d.pool = nil
		d.sps = s
	}
	d.pps = p
	poc := h.poc
	// The reference picture set (8.3.2).
	if err := d.applyRPS(h, n, poc); err != nil {
		return err
	}
	// C.5.2.2: free and output what the new picture needs room for.
	d.removeUnused()
	for {
		waiting := 0
		late := false
		for _, pic := range d.dpb {
			if pic.output {
				waiting++
				if s.maxLatency > 0 && pic.latency >= s.maxLatency {
					late = true
				}
			}
		}
		if waiting > s.maxReorder || late || len(d.dpb) >= s.maxDecPicBuf {
			ok, err := d.bump()
			if err != nil {
				return err
			}
			if !ok {
				break
			}
			d.removeUnused()
			continue
		}
		break
	}
	pic := d.newPicture(s)
	pic.poc = poc
	pic.pts = d.pts
	pic.ref = shortTerm
	pic.output = h.picOutput && (!isRASL(n.typ) || !d.noRASLOut)
	d.cur = pic
	d.curPicType = n.typ
	d.slices = d.slices[:0]
	if n.temporalID == 0 && !isRASL(n.typ) && n.typ != nalRADLN && n.typ != nalRADLR &&
		(n.typ > nalSubLayerNR || n.typ%2 != 0) {
		d.pocTid0 = poc
	}
	d.first = false
	d.beginPicture()
	return nil
}

func (d *Decoder) newPicture(s *sps) *picture {
	var pic *picture
	if n := len(d.pool); n > 0 {
		pic = d.pool[n-1]
		d.pool = d.pool[:n-1]
	} else {
		w := s.ctbW << s.log2Ctb
		h := s.ctbH << s.log2Ctb
		pic = &picture{
			y: make([]uint16, w*h), cb: make([]uint16, w*h/4), cr: make([]uint16, w*h/4),
			strideY: w, strideC: w / 2, sps: s,
			mvfW: w >> 2, mvf: make([]mvField, (w>>2)*(h>>2)),
			ctbSlice: make([]int16, s.ctbW*s.ctbH),
		}
	}
	pic.refPOC = pic.refPOC[:0]
	pic.refLT = pic.refLT[:0]
	pic.corrupt = false
	pic.latency = 0
	return pic
}

// applyRPS marks the buffer's pictures by the current picture's reference
// picture set, making up any that are missing (8.3.2, 8.3.3), and keeps
// the sets for the reference lists.
func (d *Decoder) applyRPS(h *sliceHeader, n *nalUnit, poc int) error {
	s := d.ppss[h.ppsID].sps
	ps := &d.pic
	ps.stCurrBefore = ps.stCurrBefore[:0]
	ps.stCurrAfter = ps.stCurrAfter[:0]
	ps.ltCurr = ps.ltCurr[:0]
	if n.typ == nalIDRWRADL || n.typ == nalIDRNLP {
		for _, pic := range d.dpb {
			pic.ref = unused
		}
		return nil
	}
	maxLsb := 1 << s.log2MaxPOCLsb
	keep := map[*picture]int{}
	find := func(p int, full bool) *picture {
		for _, pic := range d.dpb {
			if pic.ref == unused {
				continue
			}
			v := pic.poc
			if !full {
				v &= maxLsb - 1
			}
			if v == p {
				return pic
			}
		}
		return nil
	}
	// Long-term first: a picture can only be one or the other.
	type lt struct {
		pic  *picture
		used bool
	}
	var lts []lt
	for i := range h.numLongTerm {
		p := h.ltPOC[i]
		pic := find(p, h.ltMSBPresent[i])
		if pic == nil {
			full := p
			if !h.ltMSBPresent[i] {
				full = poc - (poc & (maxLsb - 1)) + p
			}
			pic = d.missingRef(s, full)
		}
		lts = append(lts, lt{pic, h.ltUsed[i]})
		keep[pic] = longTerm
	}
	rps := &h.stRPS
	for i := range rps.num() {
		p := poc + rps.deltaPOC[i]
		var pic *picture
		for _, c := range d.dpb {
			if c.ref == shortTerm && c.poc == p && keep[c] == 0 {
				pic = c
				break
			}
		}
		if pic == nil {
			if !rps.used[i] {
				continue // only kept for later pictures: nothing to make up
			}
			pic = d.missingRef(s, p)
		}
		keep[pic] = shortTerm
		if !rps.used[i] {
			continue
		}
		if i < rps.numNeg {
			ps.stCurrBefore = append(ps.stCurrBefore, pic)
		} else {
			ps.stCurrAfter = append(ps.stCurrAfter, pic)
		}
	}
	for _, l := range lts {
		if l.used {
			ps.ltCurr = append(ps.ltCurr, l.pic)
		}
	}
	for _, pic := range d.dpb {
		pic.ref = keep[pic]
	}
	return nil
}

// missingRef makes up a reference the stream lost (or never had: a CRA's
// leading pictures' after a random access), grey, and puts it in the
// buffer.
func (d *Decoder) missingRef(s *sps, poc int) *picture {
	pic := d.newPicture(s)
	mid := uint16(1 << (s.bitDepth - 1))
	for i := range pic.y {
		pic.y[i] = mid
	}
	midC := uint16(1 << (s.bitDepthC - 1))
	for i := range pic.cb {
		pic.cb[i], pic.cr[i] = midC, midC
	}
	clear(pic.mvf)
	pic.poc = poc
	pic.ref = shortTerm
	pic.output = false
	pic.corrupt = true
	d.dpb = append(d.dpb, pic)
	return pic
}

// removeUnused frees the pictures neither waiting for output nor
// referenced.
func (d *Decoder) removeUnused() {
	kept := d.dpb[:0]
	for _, pic := range d.dpb {
		if !pic.output && pic.ref == unused {
			d.pool = append(d.pool, pic)
			continue
		}
		kept = append(kept, pic)
	}
	d.dpb = kept
}

// bump outputs the picture waiting with the smallest POC (C.5.2.4),
// reporting false when none waits.
func (d *Decoder) bump() (bool, error) {
	var best *picture
	for _, pic := range d.dpb {
		if pic.output && (best == nil || pic.poc < best.poc) {
			best = pic
		}
	}
	if best == nil {
		return false, nil
	}
	best.output = false
	err := d.emit(best)
	d.removeUnused()
	return true, err
}

func (d *Decoder) emit(pic *picture) error {
	s := pic.sps
	l, r, t, b := s.confWin[0], s.confWin[1], s.confWin[2], s.confWin[3]
	p := &Picture{
		Width: s.width - l - r, Height: s.height - t - b, BitDepth: s.bitDepth,
		Y:       pic.y[t*pic.strideY+l:],
		Cb:      pic.cb[t/2*pic.strideC+l/2:],
		Cr:      pic.cr[t/2*pic.strideC+l/2:],
		StrideY: pic.strideY, StrideC: pic.strideC,
		PTS:       pic.pts,
		Primaries: s.primaries, Transfer: s.transfer, Matrix: s.matrix, FullRange: s.fullRange,
	}
	if s.timeScale > 0 && s.unitsInTick > 0 {
		p.FrameRateNum, p.FrameRateDen = s.timeScale, s.unitsInTick
	}
	if d.out == nil {
		return nil
	}
	return d.out(p)
}

// finishPicture completes the picture being decoded: its loop filters,
// and its place in the buffer (C.5.2.3).
func (d *Decoder) finishPicture() error {
	d.decodeSlices()
	pic := d.cur
	if pic == nil {
		return nil
	}
	d.endPicture()
	d.cur = nil
	s := pic.sps
	for _, p := range d.dpb {
		if p.output {
			p.latency++
		}
	}
	d.dpb = append(d.dpb, pic)
	for {
		waiting := 0
		late := false
		for _, p := range d.dpb {
			if p.output {
				waiting++
				if s.maxLatency > 0 && p.latency >= s.maxLatency {
					late = true
				}
			}
		}
		if waiting <= s.maxReorder && !late {
			return nil
		}
		ok, err := d.bump()
		if err != nil || !ok {
			return err
		}
	}
}

// buildRefLists makes a P or B slice's reference picture lists (8.3.4).
func (d *Decoder) buildRefLists(h *sliceHeader) bool {
	ps := &d.pic
	total := len(ps.stCurrBefore) + len(ps.stCurrAfter) + len(ps.ltCurr)
	if total == 0 {
		return false
	}
	for l := range 2 {
		n := h.numRefIdx[l]
		ps.refList[l] = ps.refList[l][:0]
		ps.refIsLT[l] = ps.refIsLT[l][:0]
		if n == 0 {
			continue
		}
		var temp []*picture
		var tempLT []bool
		first, second := ps.stCurrBefore, ps.stCurrAfter
		if l == 1 {
			first, second = second, first
		}
		for len(temp) < max(n, total) {
			for _, p := range first {
				temp = append(temp, p)
				tempLT = append(tempLT, false)
			}
			for _, p := range second {
				temp = append(temp, p)
				tempLT = append(tempLT, false)
			}
			for _, p := range ps.ltCurr {
				temp = append(temp, p)
				tempLT = append(tempLT, true)
			}
		}
		for i := range n {
			j := i
			if h.refPicListMod[l] {
				j = h.listEntry[l][i]
				if j >= len(temp) {
					return false
				}
			}
			ps.refList[l] = append(ps.refList[l], temp[j])
			ps.refIsLT[l] = append(ps.refIsLT[l], tempLT[j])
		}
	}
	return true
}

var _ = sort.Ints
