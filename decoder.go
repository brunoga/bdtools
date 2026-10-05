// Package mvc implements an H.264/AVC decoder with Multiview Video Coding
// (MVC, Annex H) support for stereo streams such as 3D Blu-ray, producing
// both the base view and the dependent view for each access unit.
package mvc

import (
	"bytes"
	"image"
	"runtime"
	"sync"
)

// Frame is one decoded view component. The planes are sub-slices of
// internal buffers and remain valid until the owning StereoFrame is
// released.
type Frame struct {
	Y, Cb, Cr []byte
	StrideY   int
	StrideC   int
	Width     int // cropped luma width
	Height    int // cropped luma height
	ViewID    int
	POC       int32
	PTS       int64 // timestamp passed to DecodeAU, or -1

	pic *picture
}

// StereoFrame holds the output of one access unit: the base view and, for
// MVC streams, the dependent view.
type StereoFrame struct {
	Base      *Frame
	Dependent *Frame // nil when the access unit has no dependent view

	d *Decoder
}

// Image returns the frame as an image.YCbCr sharing the frame's memory.
func (f *Frame) Image() *image.YCbCr {
	return &image.YCbCr{
		Y: f.Y, Cb: f.Cb, Cr: f.Cr,
		YStride: f.StrideY, CStride: f.StrideC,
		SubsampleRatio: image.YCbCrSubsampleRatio420,
		Rect:           image.Rect(0, 0, f.Width, f.Height),
	}
}

// Release returns the frame buffers to the decoder for reuse. The Frame
// planes must not be used afterwards.
func (f *StereoFrame) Release() {
	if f == nil || f.d == nil {
		return
	}
	d := f.d
	f.d = nil
	d.poolMu.Lock()
	if f.Base != nil && f.Base.pic != nil {
		f.Base.pic.userHeld = false
	}
	if f.Dependent != nil && f.Dependent.pic != nil {
		f.Dependent.pic.userHeld = false
	}
	d.poolMu.Unlock()
}

// Options configure a Decoder.
type Options struct {
	// BaseOnly skips decoding of the dependent view.
	BaseOnly bool
	// Threads is the maximum number of pictures decoded concurrently
	// (0 = number of CPUs).
	Threads int
}

type accessUnit struct {
	pics [2]*picture
	poc  int32
}

type curPic struct {
	view    int
	pic     *picture
	fc      *frameCtx
	hdr     sliceHeader
	jobs    chan *sliceJob
	held    []*picture // reference pictures used by this picture
	nslices int
	conceal *picture // source for macroblocks lost to errors
}

// sliceJob carries everything a worker needs to decode one slice.
type sliceJob struct {
	h        sliceHeader
	rbsp     []byte
	rbspLen  int
	bitPos   int
	refList  [2][]*picture
	info     sliceRefInfo
	sliceIdx int
	curPOC   int32
	dq       *dqTables
}

// Decoder decodes H.264 / MVC NAL units.
type Decoder struct {
	lastSPS int // id of the most recent base-view SPS, for FrameRate
	opts    Options
	sps     [maxSPS]*sps
	subset  [maxSPS]*sps
	pps     [maxPPS]*pps
	rbsp    []byte

	views   [maxViews]viewState
	cur     *curPic
	au      *accessUnit
	pending []*accessUnit
	ready   []*StereoFrame
	prefix  nalHeader
	hasPfx  bool
	nextID  int32
	baseSPS *sps
	reorder int
	hdr     sliceHeader
	fcPool  []*frameCtx
	poolMu  sync.Mutex
	pics    []*picture
	annexB  []byte
	lastErr error

	pts       int64
	grey      *picture
	sem       chan struct{}
	wg        sync.WaitGroup
	sdPool    sync.Pool
	jobPool   sync.Pool
	rawNAL    []byte
	stash     [][]byte // dependent view NALs received before their base view
	replaying bool
}

// NewDecoder returns a new decoder.
func NewDecoder(opts Options) *Decoder {
	d := &Decoder{opts: opts, pts: -1}
	for i := range d.views {
		d.views[i].reset()
	}
	n := opts.Threads
	if n <= 0 {
		n = runtime.NumCPU()
	}
	d.sem = make(chan struct{}, n)
	d.sdPool.New = func() any { return new(sliceDec) }
	d.jobPool.New = func() any { return new(sliceJob) }
	return d
}

// Decode feeds Annex B byte stream data (start code delimited NAL units).
// Data may be split at arbitrary boundaries across calls.
func (d *Decoder) Decode(data []byte) error {
	d.annexB = append(d.annexB, data...)
	buf := d.annexB
	var err error
	start := findStartCode(buf, 0)
	if start < 0 {
		// keep at most 3 bytes in case a start code is split
		if len(buf) > 3 {
			d.annexB = append(d.annexB[:0], buf[len(buf)-3:]...)
		}
		return nil
	}
	for {
		nalStart := start + 3
		next := findStartCode(buf, nalStart)
		if next < 0 {
			break
		}
		if e := d.DecodeNAL(trimTrailingZeros(buf[nalStart:next])); e != nil && err == nil {
			err = e
		}
		start = next
	}
	n := copy(d.annexB, buf[start:])
	d.annexB = d.annexB[:n]
	return err
}

// findStartCode returns the index of the next 00 00 01 at or after i.
func findStartCode(b []byte, i int) int {
	j := bytes.Index(b[i:], []byte{0, 0, 1})
	if j < 0 {
		return -1
	}
	return i + j
}

func trimTrailingZeros(b []byte) []byte {
	n := len(b)
	for n > 0 && b[n-1] == 0 {
		n--
	}
	return b[:n]
}

// decoded reports whether all views of the frame are fully decoded.
func (f *StereoFrame) decoded() bool {
	for _, fr := range []*Frame{f.Base, f.Dependent} {
		if fr != nil && int(fr.pic.prog.Load()) < fr.pic.mbH {
			return false
		}
	}
	return true
}

// WaitFrame returns the next frame in output order, waiting for its
// decoding to complete. It returns false if no frame is pending output.
func (d *Decoder) WaitFrame() (*StereoFrame, bool) {
	if len(d.ready) == 0 {
		return nil, false
	}
	f := d.ready[0]
	for _, fr := range []*Frame{f.Base, f.Dependent} {
		if fr != nil {
			fr.pic.waitRows(fr.pic.mbH)
		}
	}
	return d.NextFrame()
}

// DecodeAU decodes one complete access unit (or one view of it) in Annex B
// format and tags the pictures it starts with the timestamp pts, which is
// returned in Frame.PTS. Unlike Decode, the end of data is treated as the
// end of the last NAL unit. Use it when the input is already split into
// access units, e.g. by a transport stream demuxer.
func (d *Decoder) DecodeAU(data []byte, pts int64) error {
	d.pts = pts
	err := d.Decode(data)
	if len(d.annexB) > 0 {
		if start := findStartCode(d.annexB, 0); start >= 0 {
			if e := d.DecodeNAL(trimTrailingZeros(d.annexB[start+3:])); e != nil && err == nil {
				err = e
			}
		}
		d.annexB = d.annexB[:0]
	}
	d.pts = -1
	return err
}

// Flush finishes decoding of buffered data and outputs all pending frames.
func (d *Decoder) Flush() error {
	var err error
	if len(d.annexB) > 0 {
		start := findStartCode(d.annexB, 0)
		if start >= 0 {
			err = d.DecodeNAL(trimTrailingZeros(d.annexB[start+3:]))
		}
		d.annexB = d.annexB[:0]
	}
	d.finishPicture()
	d.replayStash()
	d.finishAU()
	d.flushOutput()
	d.wg.Wait()
	return err
}

// NextFrame returns the next decoded frame in output order, if any.
func (d *Decoder) NextFrame() (*StereoFrame, bool) {
	if len(d.ready) == 0 {
		return nil, false
	}
	f := d.ready[0]
	if !f.decoded() {
		return nil, false
	}
	copy(d.ready, d.ready[1:])
	d.ready = d.ready[:len(d.ready)-1]
	return f, true
}

// DecodeNAL decodes a single NAL unit (without start code).
func (d *Decoder) DecodeNAL(nal []byte) (err error) {
	if recoverPanics {
		defer func() {
			// malformed input must never crash the host program
			if r := recover(); r != nil {
				err = errInvalid
			}
		}()
	}
	return d.decodeNAL(nal)
}

func (d *Decoder) decodeNAL(nal []byte) error {
	nh, hl, err := parseNALHeader(nal)
	if err != nil {
		if err == errUnsupported {
			return nil
		}
		return err
	}
	switch nh.typ {
	case nalSlice, nalSliceIDR, nalSliceExt:
		if nh.typ == nalSliceExt && d.opts.BaseOnly {
			return nil
		}
		d.rawNAL = nal
		d.rbsp = unescapeRBSP(d.rbsp, nal[hl:])
		err = d.handleSlice(nh)
		if nh.typ != nalSliceExt {
			d.hasPfx = false
		}
	case nalSPS:
		d.rbsp = unescapeRBSP(d.rbsp, nal[hl:])
		s, e := parseSPS(d.rbsp)
		if e != nil {
			return e
		}
		d.sps[s.id] = s
		d.lastSPS = s.id
	case nalSubsetSPS:
		d.rbsp = unescapeRBSP(d.rbsp, nal[hl:])
		s, e := parseSubsetSPS(d.rbsp)
		if e != nil {
			if e == errUnsupported {
				return nil
			}
			return e
		}
		d.subset[s.id] = s
	case nalPPS:
		d.rbsp = unescapeRBSP(d.rbsp, nal[hl:])
		p, e := parsePPS(d.rbsp)
		if e != nil {
			return e
		}
		d.pps[p.id] = p
	case nalPrefix:
		d.prefix = nh
		d.hasPfx = true
	case nalAUD:
		d.finishPicture()
		d.replayStash()
		d.finishAU()
	case nalEndSeq, nalEndStream:
		d.finishPicture()
		d.replayStash()
		d.finishAU()
		d.flushOutput()
	}
	return err
}

// newPicture reports whether the slice header starts a new picture
// (7.4.1.2.4).
func newPicture(a, b *sliceHeader) bool {
	return b.firstMb == 0 || a.frameNum != b.frameNum || a.ppsID != b.ppsID ||
		(a.nal.refIdc == 0) != (b.nal.refIdc == 0) || a.nal.idr != b.nal.idr ||
		a.pocLsb != b.pocLsb || a.deltaPocBottom != b.deltaPocBottom ||
		a.deltaPoc != b.deltaPoc || (a.nal.idr && a.idrPicID != b.idrPicID)
}

func (d *Decoder) handleSlice(nh nalHeader) error {
	var br bitReader
	br.init(d.rbsp)
	h := &d.hdr
	if err := parseSliceHeader(&br, h, nh, &d.sps, &d.subset, &d.pps); err != nil {
		if err == errUnsupported {
			d.lastErr = err
		}
		return err
	}
	view := 0
	if nh.typ == nalSliceExt {
		if h.sps.viewIndex(nh.viewID) != 1 {
			return nil // only stereo (two views) is decoded
		}
		view = 1
	} else if d.hasPfx {
		h.nal.viewID = d.prefix.viewID
		h.nal.anchor = d.prefix.anchor
		h.nal.interView = d.prefix.interView
	}
	if h.redundantPicCnt > 0 {
		return nil
	}
	if view == 1 && !d.replaying {
		continuing := d.cur != nil && d.cur.view == 1 && !newPicture(&d.cur.hdr, h)
		if !continuing && (d.au == nil || d.au.pics[0] == nil || d.au.pics[1] != nil) {
			// dependent view before its base view: keep it until the base
			// view picture of the access unit has been decoded
			if len(d.stash) < 256 {
				d.stash = append(d.stash, append([]byte(nil), d.rawNAL...))
			}
			return nil
		}
	}
	if d.cur == nil || d.cur.view != view || newPicture(&d.cur.hdr, h) {
		d.finishPicture()
		if view == 0 {
			d.replayStash()
			d.finishAU()
		} else if d.au == nil || d.au.pics[1] != nil {
			// dependent view without a base view picture
			d.finishAU()
			d.au = &accessUnit{}
		}
		if err := d.startPicture(view, h); err != nil {
			return err
		}
	}
	return d.decodeSlice(&br, h)
}

// replayStash decodes dependent view NALs that preceded their base view.
func (d *Decoder) replayStash() {
	if len(d.stash) == 0 || d.replaying {
		return
	}
	if d.au == nil || d.au.pics[0] == nil {
		return
	}
	st := d.stash
	d.stash = nil
	// decode with fresh scratch state so the caller's header and RBSP
	// buffer are preserved
	hdr, rbsp, pfx, hasPfx := d.hdr, d.rbsp, d.prefix, d.hasPfx
	d.hdr, d.rbsp = sliceHeader{}, nil
	d.replaying = true
	for _, n := range st {
		_ = d.DecodeNAL(n) // errors were reported when the unit was first seen
	}
	d.replaying = false
	d.finishPicture()
	d.hdr, d.rbsp, d.prefix, d.hasPfx = hdr, rbsp, pfx, hasPfx
}

func (d *Decoder) getFrameCtx(mbW, mbH int) *frameCtx {
	d.poolMu.Lock()
	defer d.poolMu.Unlock()
	for i, fc := range d.fcPool {
		if fc.mbW == mbW && fc.mbH == mbH {
			d.fcPool = append(d.fcPool[:i], d.fcPool[i+1:]...)
			return fc
		}
	}
	return newFrameCtx(mbW, mbH)
}

// allocPicture returns a free picture buffer of the given size.
func (d *Decoder) allocPicture(mbW, mbH int) *picture {
	d.poolMu.Lock()
	defer d.poolMu.Unlock()
	for _, p := range d.pics {
		if p.mbW == mbW && p.mbH == mbH && !p.inUse() {
			p.reset()
			p.decoding = true
			return p
		}
	}
	// drop buffers of other sizes that are no longer used
	keep := d.pics[:0]
	for _, p := range d.pics {
		if p.inUse() || (p.mbW == mbW && p.mbH == mbH) {
			keep = append(keep, p)
		}
	}
	d.pics = keep
	p := allocNewPicture(mbW, mbH)
	p.decoding = true
	d.pics = append(d.pics, p)
	return p
}

func (d *Decoder) startPicture(view int, h *sliceHeader) error {
	s := h.sps
	if view == 0 {
		if d.baseSPS != nil && (d.baseSPS.widthMbs != s.widthMbs || d.baseSPS.heightMbs() != s.heightMbs()) {
			d.flushOutput()
			for i := range d.views {
				d.views[i].unmarkAll()
			}
		}
		d.baseSPS = s
		d.reorder = dpbSize(s)
		if s.numReorderFrames >= 0 {
			d.reorder = s.numReorderFrames
		}
		if d.au == nil {
			d.au = &accessUnit{}
		}
	}
	v := &d.views[view]
	if h.nal.idr {
		v.prevRefFrameNum = 0
	} else if v.seen && h.frameNum != v.prevRefFrameNum &&
		h.frameNum != (v.prevRefFrameNum+1)%(1<<s.log2MaxFrameNum) {
		d.fillFrameNumGap(view, h)
	}
	v.seen = true
	poc, fno := v.computePOC(h)
	pic := d.allocPicture(s.widthMbs, s.heightMbs())
	d.nextID++
	pic.id = d.nextID
	pic.poc = poc
	pic.frameNum = h.frameNum
	pic.viewID = h.nal.viewID
	pic.viewIdx = view
	pic.idr = h.nal.idr
	pic.anchor = h.nal.anchor || h.nal.idr
	pic.interView = h.nal.interView
	pic.outputNeeded = true
	pic.pts = d.pts
	pic.cropLeft, pic.cropRight, pic.cropTop, pic.cropBottom = s.cropLeft, s.cropRight, s.cropTop, s.cropBottom
	fc := d.getFrameCtx(s.widthMbs, s.heightMbs())
	fc.reset(pic, s)
	cp := &curPic{view: view, pic: pic, fc: fc, hdr: *h, jobs: make(chan *sliceJob, 16)}
	cp.hdr.mmco = append([]mmcoOp(nil), h.mmco...)
	if n := len(v.refs); n > 0 {
		if c := v.refs[n-1]; c.mbW == pic.mbW && c.mbH == pic.mbH {
			cp.conceal = c
			d.hold(cp, c)
		}
	}
	d.cur = cp
	d.sem <- struct{}{}
	d.wg.Add(1)
	go d.runPicture(cp)
	v.prevFrameNumOffset = fno
	d.au.pics[view] = pic
	if view == 0 {
		d.au.poc = poc
	}
	return nil
}

// greyPicture returns a mid-grey picture without motion, used in place of
// missing reference pictures.
func (d *Decoder) greyPicture(mbW, mbH int) *picture {
	if g := d.grey; g != nil && g.mbW == mbW && g.mbH == mbH {
		return g
	}
	g := allocNewPicture(mbW, mbH)
	d.nextID++
	g.id = d.nextID
	initGrey(g)
	g.nonExisting = true
	d.grey = g
	return g
}

func initGrey(pic *picture) {
	for c := 0; c < 3; c++ {
		for i := range pic.planes[c] {
			pic.planes[c][i] = 128
		}
	}
	for l := 0; l < 2; l++ {
		for i := range pic.refs[l] {
			pic.refs[l][i] = -1
			pic.mvs[l][i] = mv{}
		}
	}
	clear(pic.mbSlice)
	pic.setSliceRef(0, &sliceRefInfo{})
	pic.setProgress(pic.mbH)
	pic.done = true
}

// fillFrameNumGap inserts "non-existing" frames for gaps in frame_num
// (8.2.5.2).
func (d *Decoder) fillFrameNumGap(view int, h *sliceHeader) {
	v := &d.views[view]
	s := h.sps
	maxFrameNum := 1 << s.log2MaxFrameNum
	fn := (v.prevRefFrameNum + 1) % maxFrameNum
	count := 0
	for fn != h.frameNum && count < maxFrameNum {
		count++
		if count > 16 {
			// only the last max_num_ref_frames gap frames can matter
			skip := (h.frameNum - fn + maxFrameNum) % maxFrameNum
			if skip > s.maxNumRefFrames {
				fn = (h.frameNum - s.maxNumRefFrames + maxFrameNum) % maxFrameNum
				continue
			}
		}
		pic := d.allocPicture(s.widthMbs, s.heightMbs())
		d.nextID++
		pic.id = d.nextID
		pic.frameNum = fn
		pic.viewIdx = view
		pic.nonExisting = true
		pic.decoding = false
		initGrey(pic)
		// POC for non-existing frames is not used for output; derive an
		// approximation for B-frame ordering.
		pic.poc = v.prevPocMsb + v.prevPocLsb
		v.slidingWindow(s.maxNumRefFrames, fn, maxFrameNum)
		pic.shortRef = true
		v.refs = append(v.refs, pic)
		v.prevRefFrameNum = fn
		v.prevFrameNum = fn
		fn = (fn + 1) % maxFrameNum
	}
}

// interViewRefs returns the inter-view reference pictures for list l of
// the current dependent view picture.
func (d *Decoder) interViewRefs(h *sliceHeader, l int) []*picture {
	m := &h.sps.mvc
	vi := h.sps.viewIndex(h.nal.viewID)
	if vi < 1 || vi >= m.numViews {
		return nil
	}
	var ids []int
	if h.nal.anchor || h.nal.idr {
		ids = m.anchorRefs[l][vi]
	} else {
		ids = m.nonAnchorRefs[l][vi]
	}
	var out []*picture
	for _, id := range ids {
		if b := d.au.pics[0]; b != nil && b.viewID == id && b.interView {
			out = append(out, b)
		}
	}
	return out
}

func (d *Decoder) decodeSlice(br *bitReader, h *sliceHeader) error {
	cp := d.cur
	fc := cp.fc
	pic := cp.pic
	v := &d.views[cp.view]
	if h.firstMb >= fc.mbW*fc.mbH || cp.nslices >= maxSlices {
		return errInvalid
	}
	dq := h.pps.dequant(h.sps)
	job := d.jobPool.Get().(*sliceJob)
	job.h = *h
	job.h.mmco, job.h.refPicMods = nil, [2][]refPicMod{}
	job.sliceIdx = cp.nslices
	job.curPOC = pic.poc
	job.dq = dq
	job.refList[0], job.refList[1] = job.refList[0][:0], job.refList[1][:0]

	// reference lists
	if h.sliceType != sliceI {
		// An IDR view component never references earlier pictures of its
		// view (they are unmarked by its own decoding); in MVC a dependent
		// view IDR may still be a P/B slice using inter-view references.
		var l0, l1 []*picture
		if !h.nal.idr {
			l0, l1 = v.initRefLists(h, pic.poc)
		}
		lists := [2][]*picture{l0, l1}
		nl := 1
		if h.sliceType == sliceB {
			nl = 2
		}
		for l := 0; l < nl; l++ {
			var iv []*picture
			if cp.view > 0 {
				iv = d.interViewRefs(h, l)
				lists[l] = append(lists[l], iv...)
			}
			n := h.numRefIdxActive[l]
			if len(lists[l]) > n {
				lists[l] = lists[l][:n]
			}
			if h.refPicModFlag[l] {
				lists[l] = v.modifyRefList(lists[l], n, h.refPicMods[l], h, iv)
			} else {
				for len(lists[l]) < n {
					lists[l] = append(lists[l], nil)
				}
			}
			// replace missing references to keep decoding robust
			var fallback *picture
			for _, p := range lists[l] {
				if p != nil {
					fallback = p
					break
				}
			}
			if fallback == nil {
				for _, p := range v.refs {
					fallback = p
				}
			}
			if fallback == nil {
				fallback = d.greyPicture(fc.mbW, fc.mbH)
			}
			for i, p := range lists[l] {
				if p == nil {
					lists[l][i] = fallback
				}
			}
			job.refList[l] = append(job.refList[l], lists[l]...)
		}
	}
	info := &job.info
	*info = sliceRefInfo{}
	for l := 0; l < 2; l++ {
		for i, p := range job.refList[l] {
			info.ids[l][i] = p.id
			info.poc[l][i] = p.poc
			info.lt[l][i] = p.longRef
			d.hold(cp, p)
		}
	}
	pic.setSliceRef(job.sliceIdx, info)
	cp.nslices++
	job.rbsp = append(job.rbsp[:0], br.buf...)
	job.rbspLen = len(br.buf)
	job.rbsp = append(job.rbsp, make([]byte, cabacPad)...)
	job.bitPos = br.bitPos()
	cp.jobs <- job
	return nil
}

// hold records that the current picture references p, keeping its buffer
// alive until the decode completes.
func (d *Decoder) hold(cp *curPic, p *picture) {
	for _, q := range cp.held {
		if q == p {
			return
		}
	}
	cp.held = append(cp.held, p)
	d.poolMu.Lock()
	p.inflight++
	d.poolMu.Unlock()
}

// runPicture decodes the slices of one picture (worker goroutine).
func (d *Decoder) runPicture(cp *curPic) {
	sd := d.sdPool.Get().(*sliceDec)
	fc := cp.fc
	for job := range cp.jobs {
		sd.runJob(fc, job)
		job.refList[0], job.refList[1] = job.refList[0][:0], job.refList[1][:0]
		d.jobPool.Put(job)
	}
	fc.conceal(cp.conceal)
	fc.finishRowsSafe()
	d.sdPool.Put(sd)
	d.poolMu.Lock()
	for _, p := range cp.held {
		p.inflight--
	}
	cp.pic.decoding = false
	d.fcPool = append(d.fcPool, fc)
	d.poolMu.Unlock()
	<-d.sem
	d.wg.Done()
}

// runJob decodes one slice.
func (s *sliceDec) runJob(fc *frameCtx, job *sliceJob) {
	if recoverPanics {
		defer func() {
			if r := recover(); r != nil {
				fc.broken = true
			}
		}()
	}
	h := &job.h
	s.fc = fc
	s.h = h
	s.pic = fc.pic
	s.sliceIdx = job.sliceIdx
	s.firstMb = h.firstMb
	s.cabacOn = h.pps.cabac
	s.refList = job.refList
	s.refInfo = &job.info
	s.curPOC = job.curPOC
	s.dq4, s.dq8 = &job.dq.dq4, &job.dq.dq8
	s.dq4z, s.dq8z = &job.dq.dq4z, &job.dq.dq8z
	for len(fc.slices) <= job.sliceIdx {
		fc.slices = append(fc.slices, sliceParams{})
	}
	fc.slices[job.sliceIdx] = sliceParams{
		disableDeblock: h.disableDeblock,
		alphaOffset:    h.alphaOffset,
		betaOffset:     h.betaOffset,
		chromaQPOffset: h.pps.chromaQPOffset,
	}
	s.deblock = h.disableDeblock != 1
	s.setupInter()
	s.setQP(h.pps.picInitQP + h.qpDelta)
	s.lastDQ = 0
	s.br.init(job.rbsp[:job.rbspLen])
	s.padded = job.rbsp
	s.br.skip(uint(job.bitPos))
	if err := s.decodeSliceData(); err != nil {
		fc.broken = true
	}
}

// setupInter prepares weighted prediction and direct mode tables.
func (s *sliceDec) setupInter() {
	h := s.h
	s.wpMode = wpDefault
	if h.hasWeights {
		s.wpMode = wpExplicit
	} else if h.sliceType == sliceB && h.pps.weightedBipredIdc == 2 {
		s.wpMode = wpImplicit
	}
	if h.sliceType != sliceB {
		return
	}
	cur := s.curPOC
	s.colPic = s.refList[1][0]
	if s.wpMode == wpImplicit {
		for i := range s.refList[0] {
			p0poc := s.refInfo.poc[0][i]
			for j := range s.refList[1] {
				p1poc := s.refInfo.poc[1][j]
				w0, w1 := int16(32), int16(32)
				lt := s.refIsLongTerm(0, i) || s.refIsLongTerm(1, j)
				td := clip3(-128, 127, p1poc-p0poc)
				if !lt && td != 0 {
					tb := clip3(-128, 127, cur-p0poc)
					tx := (16384 + iabs(td/2)) / td
					dsf := clip3(-1024, 1023, (tb*tx+32)>>6)
					if dsf>>2 >= -64 && dsf>>2 <= 128 {
						w0, w1 = int16(64-dsf>>2), int16(dsf>>2)
					}
				}
				s.implicitW[i][j] = [2]int16{w0, w1}
			}
		}
	}
	if !h.directSpatial {
		if s.mapCol == nil {
			s.mapCol = make(map[int32]int)
		}
		clear(s.mapCol)
		for i := len(s.refList[0]) - 1; i >= 0; i-- {
			s.mapCol[s.refInfo.ids[0][i]] = i
		}
		p1poc := s.refInfo.poc[1][0]
		for i := range s.refList[0] {
			p0poc := s.refInfo.poc[0][i]
			td := clip3(-128, 127, p1poc-p0poc)
			if s.refIsLongTerm(0, i) || td == 0 {
				s.distScale[i] = 256
				s.dsLongTerm[i] = true
				continue
			}
			s.dsLongTerm[i] = false
			tb := clip3(-128, 127, cur-p0poc)
			tx := (16384 + iabs(td/2)) / td
			s.distScale[i] = clip3(-1024, 1023, (tb*tx+32)>>6)
		}
	}
}

func (s *sliceDec) decodeSliceData() error {
	h := s.h
	total := s.fc.mbW * s.fc.mbH
	addr := h.firstMb
	if s.cabacOn {
		s.br.alignByte()
		start := s.br.bitPos() / 8
		s.cab.init(s.padded, start)
		set := 0
		if h.sliceType != sliceI {
			set = h.cabacInitIdc + 1
		}
		initContexts(&s.ctx, set, s.qp)
		for {
			if addr >= total {
				return errInvalid
			}
			s.setMbPos(addr)
			if h.sliceType != sliceI && s.cabacSkipFlag() {
				s.decodeSkipMB()
			} else if err := s.decodeMB(); err != nil {
				return err
			}
			if s.mbX == s.fc.mbW-1 {
				s.fc.rowDecoded(s.mbY)
			}
			if s.cab.terminate() != 0 {
				break
			}
			addr++
			if s.cab.pos > len(s.cab.buf) {
				return errInvalid
			}
		}
		return nil
	}
	for {
		if h.sliceType != sliceI {
			run := int(s.br.ue())
			for ; run > 0; run-- {
				if addr >= total {
					return errInvalid
				}
				s.setMbPos(addr)
				s.decodeSkipMB()
				if s.mbX == s.fc.mbW-1 {
					s.fc.rowDecoded(s.mbY)
				}
				addr++
			}
			if !s.br.moreRBSPData() {
				break
			}
		}
		if addr >= total {
			return errInvalid
		}
		s.setMbPos(addr)
		if err := s.decodeMB(); err != nil {
			return err
		}
		if s.mbX == s.fc.mbW-1 {
			s.fc.rowDecoded(s.mbY)
		}
		addr++
		if !s.br.moreRBSPData() {
			break
		}
		if s.br.overrun() {
			return errInvalid
		}
	}
	return nil
}

// finishPicture completes decoding of the current picture: deblocking,
// border extension and reference marking.
func (d *Decoder) finishPicture() {
	cp := d.cur
	if cp == nil {
		return
	}
	d.cur = nil
	pic := cp.pic
	close(cp.jobs)
	h := &cp.hdr
	v := &d.views[cp.view]
	if h.nal.refIdc != 0 {
		v.markPicture(pic, h)
	}
	mmco5 := h.hasMMCO5
	pic.mmco5 = mmco5
	v.prevHadMMCO5 = mmco5
	v.prevFrameNum = h.frameNum
	if h.nal.refIdc != 0 {
		v.prevRefFrameNum = h.frameNum
	}
	if mmco5 {
		// the picture is treated as frame_num 0 / POC 0 afterwards
		top := pic.poc
		if h.deltaPocBottom < 0 {
			top = pic.poc - h.deltaPocBottom
		}
		tempPOC := pic.poc
		pic.poc = 0
		pic.frameNum = 0
		v.prevFrameNum = 0
		v.prevRefFrameNum = 0
		v.prevFrameNumOffset = 0
		v.prevPocMsb = 0
		v.prevPocLsb = top - tempPOC
	}
}

// finishAU queues the current access unit for output.
func (d *Decoder) finishAU() {
	au := d.au
	if au == nil {
		return
	}
	d.au = nil
	b := au.pics[0]
	if b == nil {
		// orphan dependent view picture: drop from output
		if p := au.pics[1]; p != nil {
			d.poolMu.Lock()
			p.outputNeeded = false
			d.poolMu.Unlock()
		}
		return
	}
	if b.idr || b.mmco5 {
		d.flushOutput()
	}
	au.poc = b.poc
	d.pending = append(d.pending, au)
	for len(d.pending) > d.reorder {
		d.outputOne()
	}
}

func (d *Decoder) flushOutput() {
	for len(d.pending) > 0 {
		d.outputOne()
	}
}

func (d *Decoder) outputOne() {
	best := 0
	for i, au := range d.pending {
		if au.poc < d.pending[best].poc {
			best = i
		}
	}
	au := d.pending[best]
	d.pending = append(d.pending[:best], d.pending[best+1:]...)
	sf := &StereoFrame{d: d}
	d.poolMu.Lock()
	for i, p := range au.pics {
		if p == nil {
			continue
		}
		p.outputNeeded = false
		p.userHeld = true
		f := p.frame()
		if i == 0 {
			sf.Base = f
		} else {
			sf.Dependent = f
		}
	}
	d.poolMu.Unlock()
	d.ready = append(d.ready, sf)
}

func (p *picture) frame() *Frame {
	f := &Frame{
		StrideY: p.stride[0],
		StrideC: p.stride[1],
		Width:   p.width - p.cropLeft - p.cropRight,
		Height:  p.height - p.cropTop - p.cropBottom,
		ViewID:  p.viewID,
		POC:     p.poc,
		PTS:     p.pts,
		pic:     p,
	}
	yo := p.origin[0] + p.cropTop*p.stride[0] + p.cropLeft
	f.Y = p.planes[0][yo : yo+(f.Height-1)*f.StrideY+f.Width]
	co := p.origin[1] + p.cropTop/2*p.stride[1] + p.cropLeft/2
	cw, ch := f.Width/2, f.Height/2
	f.Cb = p.planes[1][co : co+(ch-1)*f.StrideC+cw]
	f.Cr = p.planes[2][co : co+(ch-1)*f.StrideC+cw]
	return f
}

// recoverPanics converts panics caused by malformed input into errors. Tests
// disable it to surface bugs.
var recoverPanics = true

// FrameRate returns the frame rate carried by the most recent sequence
// parameter set's VUI timing information, as a fraction (e.g. 24000/1001),
// or 0/0 when the stream does not say.
func (d *Decoder) FrameRate() (num, den int) {
	s := d.sps[d.lastSPS]
	if s == nil || !s.valid || s.numUnitsInTick == 0 || s.timeScale == 0 {
		return 0, 0
	}
	// time_scale ticks per second, two field ticks per frame.
	num, den = int(s.timeScale), 2*int(s.numUnitsInTick)
	for a, b := num, den; b != 0; {
		a, b = b, a%b
		if b == 0 {
			num, den = num/a, den/a
		}
	}
	return num, den
}
