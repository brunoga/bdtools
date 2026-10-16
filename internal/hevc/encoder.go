package hevc

import (
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"slices"
	"sync"
)

// Encoder codes pictures as an HEVC stream: an IDR picture every Keyint
// pictures, then groups of BFrames + 1 pictures each coded as an anchor
// P picture followed by its B pictures in a hierarchy, each predicted
// from the pictures around it.
//
// Pictures are coded in a pipeline: a picture starts as soon as the
// pictures it predicts from are decoded (by the decoder in the loop,
// which gets the access units in decoding order), several at once, each
// on a wavefront of CTB rows. In decoding order each anchor precedes the
// B pictures of the group before it, so the anchors, which only need
// each other, run ahead while the B pictures fill in.
type Encoder struct {
	o   EncoderOptions
	c   encParams
	s   *sps
	p   *pps
	hdr []byte // the VPS, SPS and PPS NAL units
	dec *Decoder
	n   int // pictures given
	poc int // the next picture's, in its sequence
	// queue holds the pictures of the group being gathered, in display
	// order; pool their buffers when free.
	queue []*gopPicture
	pool  []*Source
	// anchors and bees are the frames the next pictures may predict from:
	// the last Refs anchors, and the B pictures of the group planned.
	anchors, bees []*gopPicture
	// pending are the B pictures of the last group, in their coding order,
	// planned after the next anchor so that anchors run ahead of them.
	pending []*gopPicture

	// The pipeline, under mu: the jobs in decoding order, the coders free,
	// and the cursors of the next job to decode and to hand out.
	mu      sync.Mutex
	cond    *sync.Cond
	jobs    []*picJob
	byFrame map[int]*picJob
	coders  []*picShared
	sendAt  int // index in jobs of the next to verify
	outAt   int
	err     error
	st      EncoderStats
	pics    []*picture // pictures free for reuse
	// The verification: the package's decoder decodes every access unit,
	// in decoding order, on a goroutine of its own, and each picture must
	// be the encoder's own, checksum for checksum.
	verifyQ  chan verifyItem
	verified chan struct{}
	vmu      sync.Mutex
	verr     error
	want     map[int]uint32
	checked  int
	dump     []byte // the stream so far, when HEVC_ENC_DUMP names a file
	// reconHook, for tests, sees the encoder's own reconstruction of each
	// picture (by its number in display order) before the decoder's loop
	// filters.
	reconHook func(pic *picture, frame int)
	// emit takes the jobs the planner makes, in decoding order: submit,
	// or a dry run's list.
	emit func(*picJob)
	// lastOut is the frame the verification's decoder gave out last.
	lastOut int
}

// gopPicture is a picture waiting to be coded.
type gopPicture struct {
	src   *Source
	frame int // in display order, from the first
	poc   int
	idr   bool
	level int // 0 an anchor, deeper B pictures higher
}

// picJob is a picture planned: what it predicts from and how, then its
// access unit and its decoded picture.
type picJob struct {
	p      *gopPicture
	typ    int
	qp     int
	refs   [2][]*gopPicture // the reference lists
	kept   []*gopPicture    // every picture the set keeps: refs and others
	state  int
	au     []byte
	pic    *picture  // the reconstruction, loop filtered as its rows end
	planes refPlanes // its fractional planes, for the pictures predicted from it
	sum    uint32    // its checksum, once coded
}

const (
	jobWaiting = iota
	jobCoding
	jobCoded
)

// EncoderStats is what an encoder has done: pictures and bytes written,
// and the squared error of its reconstructions per plane, for a PSNR.
type EncoderStats struct {
	Pictures int
	Bytes    int64
	SSE      [3]float64
	Samples  [3]float64
}

// PSNR of plane ci (0 luma, 1 Cb, 2 Cr) of the pictures coded, in dB.
func (s *EncoderStats) PSNR(ci, bitDepth int) float64 {
	if s.SSE[ci] == 0 {
		return 99
	}
	maxV := float64(int(1)<<bitDepth - 1)
	return 10 * math.Log10(maxV*maxV*s.Samples[ci]/s.SSE[ci])
}

// Stats reports what the encoder has done.
func (e *Encoder) Stats() EncoderStats {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.st
}

// EncoderOptions configures an Encoder.
type EncoderOptions struct {
	Width, Height int
	BitDepth      int // 8 or 10
	QP            int // 0..51, the I pictures'; the others code higher
	FPSNum        int
	FPSDen        int
	// Keyint is the IDR picture interval; 0 picks 250, 1 codes intra only.
	Keyint int
	// BFrames is how many B pictures come between anchors: 0, 1, 3 or 7
	// (a hierarchy); -1 picks 7, smaller and with more pictures to code
	// at once than 3, whose 6-picture buffer at 1080p keeps to level 4.1
	// (7 needs 10, level 5).
	BFrames int
	// Refs is how many pictures before it a P picture may predict from;
	// 0 picks 2.
	Refs int
	// Threads is how many CTB rows of a picture are coded at once; 0
	// picks the CPUs.
	Threads int
	// Pipeline is how many pictures are coded at once; 0 picks by the
	// threads.
	Pipeline int
	// NoVerify leaves out the decoding of every access unit written, which
	// checks that the stream decodes to the encoder's pictures.
	NoVerify bool
	// Rect tries the rectangular partitions (2NxN, Nx2N) of inter CUs:
	// 1.7% smaller output for 1.8 times the work, off unless asked.
	Rect bool
	// QPOffsetP and QPOffsetB are how much above QP the anchor P pictures
	// and the first level of B pictures code (each deeper level one more);
	// 0 picks 4 and 8, the cascade x265 uses, which measured best.
	QPOffsetP, QPOffsetB int
	// Primaries, Transfer and Matrix are the colour description the
	// stream states (H.273); 0 or 2 for unspecified.
	Primaries, Transfer, Matrix int
	FullRange                   bool
}

// NewEncoder makes an encoder for the options.
func NewEncoder(o EncoderOptions) (*Encoder, error) {
	if o.BitDepth == 0 {
		o.BitDepth = 8
	}
	if o.BitDepth != 8 && o.BitDepth != 10 {
		return nil, fmt.Errorf("hevc: bit depth %d: want 8 or 10", o.BitDepth)
	}
	if o.Width <= 0 || o.Height <= 0 || o.Width%8 != 0 || o.Height%8 != 0 {
		return nil, fmt.Errorf("hevc: picture size %dx%d: want multiples of 8", o.Width, o.Height)
	}
	if o.QP < 0 || o.QP > 51 {
		return nil, fmt.Errorf("hevc: QP %d: want 0..51", o.QP)
	}
	if o.Keyint <= 0 {
		o.Keyint = 250
	}
	if o.Refs <= 0 {
		o.Refs = 2
	}
	if o.Refs > 4 {
		return nil, fmt.Errorf("hevc: %d references: at most 4", o.Refs)
	}
	if o.BFrames < 0 {
		o.BFrames = 7
	}
	if o.BFrames != 0 && o.BFrames != 1 && o.BFrames != 3 && o.BFrames != 7 {
		return nil, fmt.Errorf("hevc: %d B pictures: want 0, 1, 3 or 7", o.BFrames)
	}
	if o.QPOffsetP == 0 {
		o.QPOffsetP = 4
	}
	if o.QPOffsetB == 0 {
		o.QPOffsetB = 8
	}
	if o.Threads <= 0 {
		o.Threads = runtime.GOMAXPROCS(0)
	}
	if o.Pipeline <= 0 {
		// Sixteen pictures in flight kept 24 threads busiest with groups of
		// seven B pictures; more gained nothing.
		o.Pipeline = min(16, max(1, 2*o.Threads/3))
	}
	cp := func(v int) int {
		if v == 0 {
			return 2
		}
		return v
	}
	e := &Encoder{o: o, byFrame: map[int]*picJob{}, lastOut: -1}
	e.cond = sync.NewCond(&e.mu)
	e.emit = e.submit
	dpb, reorder := planDPB(o)
	if dpb > 16 {
		return nil, fmt.Errorf("hevc: %d B pictures and %d references need %d pictures of decoded picture buffer: at most 16", o.BFrames, o.Refs, dpb)
	}
	e.c = encParams{
		width: o.Width, height: o.Height, bitDepth: o.BitDepth,
		fpsNum: o.FPSNum, fpsDen: o.FPSDen,
		primaries: cp(o.Primaries), transfer: cp(o.Transfer), matrix: cp(o.Matrix), fullRange: o.FullRange,
		log2Ctb: 6, log2MinCb: 3, log2MinTb: 2, log2MaxTb: 5,
		maxTrDepthIntra: 1, maxTrDepthInter: 1,
		maxDecPicBuf: dpb, maxReorder: reorder, log2MaxPOCLsb: 8,
		tmvp: true, strongIntra: true, wpp: true,
		initQP: 26,
	}
	var err error
	if e.s, e.p, err = e.c.parameterSets(); err != nil {
		return nil, err
	}
	e.hdr = e.c.headers()
	e.dec = New()
	if !o.NoVerify {
		e.want = map[int]uint32{}
		e.verifyQ = make(chan verifyItem, 1024)
		e.verified = make(chan struct{})
		go e.verify()
	}
	for range o.Pipeline {
		e.coders = append(e.coders, newPicShared(e, o.Threads))
	}
	return e, nil
}

// buffer takes a copy of the source into a buffer of the encoder's.
func (e *Encoder) buffer(src *Source) *Source {
	w, h := e.o.Width, e.o.Height
	var b *Source
	e.mu.Lock()
	if n := len(e.pool); n > 0 {
		b = e.pool[n-1]
		e.pool = e.pool[:n-1]
	}
	e.mu.Unlock()
	if b == nil {
		b = &Source{Y: make([]uint16, w*h), Cb: make([]uint16, w*h/4), Cr: make([]uint16, w*h/4), StrideY: w, StrideC: w / 2}
	}
	for y := range h {
		copy(b.Y[y*w:y*w+w], src.Y[y*src.StrideY:])
	}
	for y := range h / 2 {
		copy(b.Cb[y*w/2:y*w/2+w/2], src.Cb[y*src.StrideC:])
		copy(b.Cr[y*w/2:y*w/2+w/2], src.Cr[y*src.StrideC:])
	}
	return b
}

// Encode takes a picture, returning the access units ready: a group's
// once it is coded, in decoding order, the parameter sets before an IDR
// picture. It blocks while the pipeline is full.
func (e *Encoder) Encode(src *Source) ([][]byte, error) {
	if len(src.Y) < e.o.Height*src.StrideY || len(src.Cb) < e.o.Height/2*src.StrideC {
		return nil, errors.New("hevc: a source picture too small for the encoder's size")
	}
	e.waitRoom()
	e.plan(e.buffer(src))
	return e.collect(false)
}

// plan takes the next picture in display order into the plan: the groups
// it completes become jobs.
func (e *Encoder) plan(src *Source) {
	idr := e.n%e.o.Keyint == 0
	if idr {
		// The pictures waiting cannot predict across an IDR picture: they
		// make a group of their own first, and the B pictures pending go
		// before it.
		if len(e.queue) > 0 {
			e.planGroup()
		}
		e.planPending()
		e.poc = 0
	}
	e.queue = append(e.queue, &gopPicture{src: src, frame: e.n, poc: e.poc, idr: idr})
	e.n++
	e.poc++
	// A group ends at an anchor: an IDR picture (alone), or the
	// BFrames + 1 th picture after the last anchor.
	if idr || len(e.queue) == e.o.BFrames+1 {
		e.planGroup()
	}
}

// planFlush plans the pictures waiting at the end of the stream.
func (e *Encoder) planFlush() {
	if len(e.queue) > 0 {
		e.planGroup()
	}
	e.planPending()
}

// planDPB runs the planner dry over two keyframe intervals (and the
// partial groups at their ends, and the end of a stream) and returns the
// decoded picture buffer the stream needs (C.5.2.2: the pictures its
// reference picture sets keep, those waiting for output, and the current
// one) and its reordering (the most pictures preceding one in decoding
// order and following it in output order).
func planDPB(o EncoderOptions) (dpb, reorder int) {
	measure := func(frames int) {
		e := &Encoder{o: o}
		var jobs []*picJob
		e.emit = func(j *picJob) { jobs = append(jobs, j) }
		for range frames {
			e.plan(nil)
		}
		e.planFlush()
		// Each coded video sequence (from an IDR picture) on its own.
		for start := 0; start < len(jobs); {
			end := start + 1
			for end < len(jobs) && !jobs[end].p.idr {
				end++
			}
			cvs := jobs[start:end]
			// The smallest POC still to decode from each point on.
			minLeft := make([]int, len(cvs)+1)
			minLeft[len(cvs)] = math.MaxInt
			for i := len(cvs) - 1; i >= 0; i-- {
				minLeft[i] = min(minLeft[i+1], cvs[i].p.poc)
			}
			for i, j := range cvs {
				held := map[*gopPicture]bool{}
				for _, k := range j.kept {
					held[k] = true
				}
				late := 0
				for _, k := range cvs[:i] {
					if k.p.poc > j.p.poc {
						late++
					}
					// Waiting for output: a picture before it in POC is
					// still to decode.
					if k.p.poc > minLeft[i] {
						held[k.p] = true
					}
				}
				reorder = max(reorder, late)
				dpb = max(dpb, len(held)+1)
			}
			start = end
		}
	}
	// Two intervals, and streams ending at every point of a group.
	for tail := range o.BFrames + 2 {
		measure(2*o.Keyint + tail)
	}
	return dpb, reorder
}

// waitRoom blocks while the pipeline holds as many pictures to code as it
// may: a group being gathered, and groups being coded.
func (e *Encoder) waitRoom() {
	e.mu.Lock()
	limit := 2*(e.o.BFrames+1) + 2*e.o.Pipeline
	for e.toCode() > limit && e.err == nil {
		e.cond.Wait()
	}
	e.mu.Unlock()
}

// toCode (with mu held) counts the jobs not coded yet.
func (e *Encoder) toCode() int {
	n := 0
	for _, j := range e.jobs[e.outAt:] {
		if j.state < jobCoded {
			n++
		}
	}
	return n
}

// Flush codes the pictures waiting and ends the stream, returning the
// access units left; with the verification, it checks that the stream
// decoded to the encoder's pictures, all of them, in display order.
func (e *Encoder) Flush() ([][]byte, error) {
	e.planFlush()
	aus, err := e.collect(true)
	if err != nil {
		return nil, err
	}
	if e.verifyQ == nil {
		return aus, nil
	}
	close(e.verifyQ)
	<-e.verified
	ferr := e.dec.Flush(e.check)
	e.vmu.Lock()
	defer e.vmu.Unlock()
	if ferr != nil && e.verr == nil {
		e.verr = fmt.Errorf("hevc: the stream written does not decode: %w", ferr)
	}
	if e.verr == nil && (e.checked != e.st.Pictures || e.dec.Errors() != 0) {
		e.verr = fmt.Errorf("hevc: the stream written decodes to %d pictures of %d", e.checked, e.st.Pictures)
	}
	return aus, e.verr
}

// planGroup plans the queued pictures: the last as the anchor, coded
// now after the last group's B pictures pending; the ones before it as a
// hierarchy of B pictures (the middle one first) pending until the next
// anchor.
func (e *Encoder) planGroup() {
	q := e.queue
	e.queue = e.queue[:0]
	anchor := q[len(q)-1]
	if anchor.idr {
		e.anchors = e.anchors[:0]
	}
	e.bees = e.bees[:0]
	anchor.level = 0
	e.emit(e.planPicture(anchor))
	e.planPending()
	var bisect func(lo, hi, level int)
	bisect = func(lo, hi, level int) {
		if hi-lo < 2 {
			return
		}
		mid := (lo + hi) / 2
		q[mid].level = level
		e.pending = append(e.pending, q[mid])
		bisect(lo, mid, level+1)
		bisect(mid, hi, level+1)
	}
	bisect(-1, len(q)-1, 1)
}

// planPending plans the B pictures pending, in their coding order.
func (e *Encoder) planPending() {
	// The B pictures kept are the group's being planned alone.
	e.bees = e.bees[:0]
	jobs := make([]*picJob, 0, len(e.pending))
	for _, p := range e.pending {
		jobs = append(jobs, e.planPicture(p))
	}
	for _, j := range jobs {
		e.emit(j)
	}
	e.pending = e.pending[:0]
}

// submit adds a job to the pipeline and starts what can start.
func (e *Encoder) submit(j *picJob) {
	e.mu.Lock()
	e.jobs = append(e.jobs, j)
	e.byFrame[j.p.frame] = j
	e.dispatch()
	e.mu.Unlock()
}

// planPicture decides a picture's slice type, QP and references among the
// pictures kept, and keeps it in turn.
func (e *Encoder) planPicture(p *gopPicture) *picJob {
	var before, after []*gopPicture
	for _, k := range append(append([]*gopPicture{}, e.anchors...), e.bees...) {
		if k.poc < p.poc {
			before = append(before, k)
		} else if k.poc > p.poc {
			after = append(after, k)
		}
	}
	slices.SortFunc(before, func(a, b *gopPicture) int { return b.poc - a.poc })
	slices.SortFunc(after, func(a, b *gopPicture) int { return a.poc - b.poc })
	j := &picJob{p: p, typ: sliceI}
	switch {
	case p.idr || len(before) == 0 && len(after) == 0:
	case len(after) == 0:
		j.typ = sliceP
		j.refs[0] = before[:min(len(before), e.o.Refs)]
	default:
		j.typ = sliceB
		j.refs[0] = append(append([]*gopPicture{}, before[:min(len(before), e.o.Refs)]...), after[:min(len(after), 1)]...)
		j.refs[1] = append(append([]*gopPicture{}, after[:min(len(after), e.o.Refs)]...), before[:min(len(before), 1)]...)
	}
	j.kept = append(append([]*gopPicture{}, before...), after...)
	j.qp = e.o.QP
	switch {
	case j.typ == sliceI:
	case p.level == 0:
		j.qp = min(51, j.qp+e.o.QPOffsetP)
	default:
		j.qp = min(51, j.qp+e.o.QPOffsetB+p.level-1)
	}
	// Kept for the pictures after it: an anchor among the last Refs + 1
	// anchors (the B pictures of the group before the last one still
	// predict from the anchor before theirs), a B picture among its
	// group's.
	if p.level != 0 {
		e.bees = append(e.bees, p)
	} else {
		e.anchors = append(e.anchors, p)
		if len(e.anchors) > e.o.Refs+1 {
			e.anchors = e.anchors[len(e.anchors)-e.o.Refs-1:]
		}
	}
	return j
}

// dispatch (with mu held) starts the jobs waiting, in decoding order, on
// the coders free. A job's references precede it in that order, so they
// have started: the job waits for the rows of theirs it reads.
func (e *Encoder) dispatch() {
	if e.err != nil {
		return
	}
	for _, j := range e.jobs[e.outAt:] {
		if j.state != jobWaiting {
			continue
		}
		if len(e.coders) == 0 {
			return
		}
		coder := e.coders[len(e.coders)-1]
		e.coders = e.coders[:len(e.coders)-1]
		j.pic = e.newPicture()
		// Its identity, for the pictures predicted from it: its POC, and
		// its slice's reference POCs (the deblocking's strengths and the
		// temporal motion prediction read them).
		var pocs [2][16]int32
		for l := range 2 {
			for i, r := range j.refs[l] {
				pocs[l][i] = int32(r.poc) //nolint:gosec // a POC
			}
		}
		j.pic.poc = j.p.poc
		j.pic.refPOC = append(j.pic.refPOC[:0], pocs)
		j.pic.refLT = append(j.pic.refLT[:0], [2][16]bool{})
		clear(j.pic.ctbSlice)
		j.state = jobCoding
		go e.code(j, coder)
	}
}

// newPicture (with mu held) gives a picture buffer for a job, its rows
// not final.
func (e *Encoder) newPicture() *picture {
	s := e.s
	var pic *picture
	if n := len(e.pics); n > 0 {
		pic = e.pics[n-1]
		e.pics = e.pics[:n-1]
	} else {
		w, h := s.ctbW<<s.log2Ctb, s.ctbH<<s.log2Ctb
		pic = &picture{
			y: make([]uint16, w*h), cb: make([]uint16, w*h/4), cr: make([]uint16, w*h/4),
			strideY: w, strideC: w / 2, sps: s,
			mvfW: w >> 2, mvf: make([]mvField, (w>>2)*(h>>2)),
			ctbSlice: make([]int16, s.ctbW*s.ctbH),
			surface:  -1,
		}
	}
	pic.resetProgress(s.ctbH)
	return pic
}

// code codes a job on a coder, then hands its access unit on and starts
// what can start.
func (e *Encoder) code(j *picJob, c *picShared) {
	var refs [2][]*picture
	var planes [2][]*refPlanes
	e.mu.Lock()
	for l := range 2 {
		for _, r := range j.refs[l] {
			rj := e.byFrame[r.frame]
			refs[l] = append(refs[l], rj.pic)
			planes[l] = append(planes[l], &rj.planes)
		}
	}
	e.mu.Unlock()
	p := j.p
	c.start(j.pic, p.src, j.qp, j.typ, p.poc, refs, planes)
	subs := c.codePicture()
	var au []byte
	nalType := nalTrailR
	if p.idr {
		au = append(au, e.hdr...)
		nalType = nalIDRNLP
	}
	au = sliceSegment(au, nalType, func(w *bitWriter, offsets []int) {
		e.sliceHeader(w, j)
		writeEntryPoints(w, offsets)
	}, subs)
	if e.reconHook != nil {
		e.reconHook(j.pic, p.frame)
	}
	sse := c.pictureSSE()
	sum := pictureSum(j.pic.y, j.pic.cb, j.pic.cr, j.pic.strideY, j.pic.strideC, e.s.width, e.s.height)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.st.Pictures++
	e.st.Bytes += int64(len(au))
	for ci := range 3 {
		e.st.SSE[ci] += sse[ci]
	}
	e.st.Samples[0] += float64(e.o.Width * e.o.Height)
	e.st.Samples[1] += float64(e.o.Width * e.o.Height / 4)
	e.st.Samples[2] += float64(e.o.Width * e.o.Height / 4)
	j.au = au
	j.sum = sum
	j.state = jobCoded
	e.coders = append(e.coders, c)
	e.pool = append(e.pool, p.src)
	// The jobs coded go to the verification in decoding order.
	for e.sendAt < len(e.jobs) && e.jobs[e.sendAt].state == jobCoded {
		if j := e.jobs[e.sendAt]; e.verifyQ != nil {
			e.verifyQ <- verifyItem{frame: j.p.frame, au: j.au, sum: j.sum}
		}
		e.sendAt++
	}
	e.dispatch()
	e.cond.Broadcast()
}

// pictureSum is a checksum of a picture's planes.
func pictureSum(y, cb, cr []uint16, strideY, strideC, w, h int) uint32 {
	var sum uint32 = 2166136261
	add := func(pl []uint16, stride, pw, ph int) {
		for row := range ph {
			for _, v := range pl[row*stride : row*stride+pw] {
				sum = (sum ^ uint32(v)) * 16777619
			}
		}
	}
	add(y, strideY, w, h)
	add(cb, strideC, w/2, h/2)
	add(cr, strideC, w/2, h/2)
	return sum
}

// verifyItem is an access unit to verify, and the checksum of the
// encoder's picture of it.
type verifyItem struct {
	frame int
	au    []byte
	sum   uint32
}

// verify decodes the access units the encoder hands it, in decoding
// order, checking each picture against the encoder's.
func (e *Encoder) verify() {
	defer close(e.verified)
	for j := range e.verifyQ {
		e.vmu.Lock()
		failed := e.verr != nil
		if !failed {
			e.want[j.frame] = j.sum
		}
		e.vmu.Unlock()
		if failed {
			continue
		}
		err := e.dec.Decode(j.au, int64(j.frame), e.check)
		e.vmu.Lock()
		switch {
		case err != nil && e.verr == nil:
			e.verr = fmt.Errorf("hevc: the picture written does not decode: %w", err)
		case e.dec.Errors() != 0 && e.verr == nil:
			if path := os.Getenv("HEVC_ENC_DUMP"); path != "" {
				_ = os.WriteFile(path, append(e.dump, j.au...), 0o600) //nolint:gosec // a debugging aid, the path the operator's
			}
			e.verr = errors.New("hevc: the picture written does not decode")
		}
		if os.Getenv("HEVC_ENC_DUMP") != "" {
			e.dump = append(e.dump, j.au...)
		}
		e.vmu.Unlock()
	}
}

// check compares a decoded picture with the encoder's (on the
// verification's goroutine, or after it).
func (e *Encoder) check(p *Picture) error {
	e.vmu.Lock()
	defer e.vmu.Unlock()
	frame := int(p.PTS)
	want, ok := e.want[frame]
	if !ok {
		return nil
	}
	delete(e.want, frame)
	e.checked++
	if frame <= e.lastOut && e.verr == nil {
		e.verr = fmt.Errorf("hevc: picture %d decodes out of display order, after picture %d", frame, e.lastOut)
	}
	e.lastOut = frame
	if got := pictureSum(p.Y, p.Cb, p.Cr, p.StrideY, p.StrideC, p.Width, p.Height); got != want && e.verr == nil {
		e.verr = fmt.Errorf("hevc: picture %d decodes to another picture than the encoder's", frame)
	}
	return nil
}

// collect returns the access units of the jobs coded, in decoding order,
// waiting for all of them when all is set, and lets go of the jobs no
// picture to come refers to.
func (e *Encoder) collect(all bool) ([][]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out [][]byte
	for {
		for e.outAt < len(e.jobs) && e.jobs[e.outAt].state == jobCoded {
			out = append(out, e.jobs[e.outAt].au)
			e.jobs[e.outAt].au = nil
			e.outAt++
		}
		if e.err == nil {
			e.vmu.Lock()
			e.err = e.verr
			e.vmu.Unlock()
		}
		if e.err != nil {
			return nil, e.err
		}
		if !all || e.outAt == len(e.jobs) {
			break
		}
		e.cond.Wait()
	}
	// Jobs handed out that no job to code refers to can go: neither the
	// jobs planned and not coded nor the pictures kept for the ones not
	// planned yet.
	minRef := math.MaxInt
	for _, j := range e.jobs {
		if j.state == jobCoded {
			continue
		}
		for l := range 2 {
			for _, r := range j.refs[l] {
				minRef = min(minRef, r.frame)
			}
		}
	}
	for _, ks := range [][]*gopPicture{e.anchors, e.bees, e.pending} {
		for _, k := range ks {
			minRef = min(minRef, k.frame)
		}
	}
	drop := 0
	for drop < e.outAt && drop < e.sendAt && e.jobs[drop].p.frame < minRef {
		j := e.jobs[drop]
		delete(e.byFrame, j.p.frame)
		j.planes.release()
		e.pics = append(e.pics, j.pic)
		j.pic = nil
		drop++
	}
	if drop > 0 {
		e.jobs = slices.Clone(e.jobs[drop:])
		e.outAt -= drop
		e.sendAt -= drop
	}
	return out, nil
}

// sliceHeader writes the job's slice segment header up to the entry
// points (7.3.6.1), which sliceSegment writes after it. The reference
// picture set keeps every picture kept, before and after this one.
func (e *Encoder) sliceHeader(w *bitWriter, j *picJob) {
	s := e.s
	p := j.p
	w.flag(true) // first_slice_segment_in_pic_flag
	if p.idr {
		w.flag(false) // no_output_of_prior_pics_flag
	}
	w.ue(0)             // slice_pic_parameter_set_id
	w.ue(uint32(j.typ)) //nolint:gosec // 0..2
	if !p.idr {
		w.u(uint(s.log2MaxPOCLsb), uint32(p.poc&(1<<s.log2MaxPOCLsb-1))) //nolint:gosec // masked
		w.flag(false)                                                    // short_term_ref_pic_set_sps_flag
		used := func(k *gopPicture) bool {
			return slices.Contains(j.refs[0], k) || slices.Contains(j.refs[1], k)
		}
		var before, after []*gopPicture
		for _, k := range j.kept {
			if k.poc < p.poc {
				before = append(before, k)
			} else {
				after = append(after, k)
			}
		}
		w.ue(uint32(len(before))) //nolint:gosec // small
		w.ue(uint32(len(after)))  //nolint:gosec // small
		prev := p.poc
		for _, r := range before {
			w.ue(uint32(prev - r.poc - 1)) //nolint:gosec // positive
			w.flag(used(r))
			prev = r.poc
		}
		prev = p.poc
		for _, r := range after {
			w.ue(uint32(r.poc - prev - 1)) //nolint:gosec // positive
			w.flag(used(r))
			prev = r.poc
		}
		if s.temporalMVP {
			w.flag(true) // slice_temporal_mvp_enabled_flag
		}
	}
	if j.typ != sliceI {
		w.flag(true)                     // num_ref_idx_active_override_flag
		w.ue(uint32(len(j.refs[0]) - 1)) //nolint:gosec // small
		if j.typ == sliceB {
			w.ue(uint32(len(j.refs[1]) - 1)) //nolint:gosec // small
			w.flag(false)                    // mvd_l1_zero_flag
		}
		if s.temporalMVP {
			if j.typ == sliceB {
				w.flag(true) // collocated_from_l0_flag
			}
			if len(j.refs[0]) > 1 {
				w.ue(0) // collocated_ref_idx
			}
		}
		w.ue(0) // five_minus_max_num_merge_cand
	}
	w.se(int32(j.qp - e.p.initQP)) //nolint:gosec // a QP delta
}
