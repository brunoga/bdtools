package mvc

import (
	"errors"
	"fmt"
	"sync"
)

// The dependent view writer: it takes a base view H.264 stream (High
// profile, progressive frames) access unit by access unit and adds a second
// view, turning it into a Stereo High (MVC) stream. Its parameter sets come
// from the base view's, so the two views share picture size, frame_num and
// POC coding; each dependent picture mirrors its base picture's
// nal_ref_idc, frame_num, POC and IDR status, which keeps the two views'
// reference marking and picture order in step whatever the base encoder
// did.

// errDepBase reports a base view the dependent view writer cannot follow.
var errDepBase = errors.New("mvc: base view not supported for MVC encoding")

// depPicture is what a dependent view encoder needs to know about the
// base picture of an access unit.
type depPicture struct {
	refIdc   int
	idr      bool
	idrPicID int
	frameNum int
	pocLsb   int
	deltaPoc [2]int32
	deltaBot int32
	// dec_ref_pic_marking, mirrored so the views' references stay in step
	noOutputOfPriorPics bool
	longTermReference   bool
	adaptiveMarking     bool
	mmco                []mmcoOp
	sps                 *sps
}

// DepWriter adds a dependent view to a base view stream.
type DepWriter struct {
	spsTab    [maxSPS]*sps
	subsetTab [maxSPS]*sps
	ppsTab    [maxPPS]*pps
	spsRBSP   [maxSPS][]byte // the subset SPS RBSPs
	spsNAL    [maxSPS][]byte // the base view's SPS NAL units
	ppsNAL    [maxPPS][]byte // and PPS NAL units
	depPPS    int            // id of the dependent view's PPS
	lastSPS   int            // id of the base view's latest SPS
	qp        int

	// InterViewOnly predicts the dependent view from the base view alone,
	// never from its own earlier pictures.
	InterViewOnly bool

	// Slices is how many slices a dependent view picture is cut into, each
	// coded on its own goroutine; 0 is depSlices.
	Slices int

	// AQ is the strength of adaptive quantisation (BaseOptions.AQ).
	AQ float64

	// NoBSlices codes every dependent view picture as P slices, never
	// predicting from two references at once.
	NoBSlices bool

	// The decoder in the loop: its reconstructions are the references.
	dec *Decoder
	// decBase decodes the base view alone, for the inter-view reference
	// the coding reads: dec has it too, but gets it after the dependent
	// view picture before, which it is still decoding while this one is
	// coded; decBase has it ahead of that.
	decBase *Decoder
	// The pipeline: pictures are coded as jobs, several at once (the B
	// pictures between two references only predict from those), by a
	// pool of coders; dec is fed their access units in order as they
	// finish, and the finished ones are given out in order.
	pool    []*depCoder
	coders  chan *depCoder
	jobs    []*depJob // submitted, not yet given out
	fed     int       // jobs[:fed] have been fed to dec
	lastRef *depJob   // the last reference picture submitted
	mu      sync.Mutex
	failed  error

	noDeblock bool // tests: leave the dependent view unfiltered
}

// NewDepWriter returns a writer whose dependent view is coded at quantiser
// qp.
func NewDepWriter(qp int) *DepWriter {
	return &DepWriter{depPPS: -1, qp: qp, dec: NewDecoder(Options{}), decBase: NewDecoder(Options{BaseOnly: true})}
}

// DepStats counts the dependent view's macroblocks by prediction.
type DepStats struct {
	Skip        int // P_Skip or B_Skip
	Direct      int // B_Direct_16x16
	InterView   int // from the base view picture
	Temporal    int // from one of the dependent view's own pictures
	Bi          int // from two references averaged
	Partitioned int // in two partitions
	Intra       int // intra predicted
}

// Stats returns how the dependent view's macroblocks were predicted so
// far.
func (dw *DepWriter) Stats() DepStats {
	var st depStats
	for _, c := range dw.pool {
		st.add(c.stats)
	}
	return DepStats{Skip: st.skip, Direct: st.direct, InterView: st.interView, Temporal: st.temporal, Bi: st.bi, Partitioned: st.parts, Intra: st.intra}
}

// AccessUnit takes one base view access unit (Annex B) and the dependent
// view's picture for it and returns the MVC access unit: the base view's
// NAL units unchanged, with the MVC parameter sets (subset SPS and the
// dependent view's PPS) after its own when it carries them, then a
// dependent view delimiter and the dependent view's slices, as Blu-ray
// discs have them (but without prefix NAL units: the base view is plain
// H.264). Without a source picture the dependent view repeats the base
// view.
func (dw *DepWriter) AccessUnit(au []byte, src *DepSource) ([]byte, error) {
	if src == nil {
		return dw.AccessUnitFor(au, nil)
	}
	return dw.AccessUnitFor(au, func(int32, bool) (*DepSource, error) { return src, nil })
}

// AccessUnitFor is AccessUnit with the dependent view's picture asked for
// once the base view picture is decoded: src is given its picture order
// count and whether it is an IDR picture (which starts the count over).
// Encoders number frames' POC twice their display position since the last
// IDR picture, so a caller holding the frames in display order can find
// the one. A nil src makes the dependent view repeat the base view.
func (dw *DepWriter) AccessUnitFor(au []byte, src func(poc int32, idr bool) (*DepSource, error)) ([]byte, error) {
	aus, err := dw.Submit(au, src)
	if err != nil {
		return nil, err
	}
	rest, err := dw.Flush()
	if err != nil {
		return nil, err
	}
	var out []byte
	for _, a := range append(aus, rest...) {
		out = append(out, a...)
	}
	return out, nil
}

// depPipeline is how many pictures are coded at once: the B pictures
// between two references and the next reference, as the base view's
// encoder does.
const depPipeline = 5

// depJob is a picture going through the pipeline.
type depJob struct {
	out     []byte // the base view's NAL units and the delimiter, then the dependent view's
	baseEnd int
	pic     *depPicture
	hd      depHdr
	base    *picture // the base view picture (decBase's), held until coded
	ds      *DepSource
	list    [2][]*picture // the references (dec's), held until coded
	bslice  bool
	needs   *depJob // the reference picture before it: fed to dec before this is coded
	// vecs are a reference picture's vectors once coded, the next
	// pictures' search starting points
	vecs    [3][]mv
	from    [3][]mv // the reference before's vecs, given at its start
	coded   bool    // nothing (more) to code
	started bool
	done    chan struct{}
}

// Submit takes the next base view access unit and starts coding its
// dependent view picture, giving back the MVC access units finished so
// far, in order (none, one or several). Flush gives the rest.
func (dw *DepWriter) Submit(au []byte, src func(poc int32, idr bool) (*DepSource, error)) ([][]byte, error) {
	dw.mu.Lock()
	failed := dw.failed
	dw.mu.Unlock()
	if failed != nil {
		return nil, failed
	}
	var out []byte
	var pic *depPicture
	newParams := dw.depPPS < 0
	// An IDR access unit carries every parameter set, the base view's too,
	// whether or not the base view's encoder repeats them there (NVENC
	// does not): a player starting or seeking there needs them all, and
	// one whose MVC decoder resets at an IDR picture finds no subset SPS
	// otherwise.
	sawSPS, done := false, false
	var sawPPS [maxPPS]bool
	err := forEachNAL(au, func(nal []byte) error {
		if len(nal) == 0 {
			return nil
		}
		typ := int(nal[0] & 31)
		switch typ {
		case nalSPS:
			rbsp := unescapeRBSP(nil, nal[1:])
			s, err := parseSPS(rbsp)
			if err != nil {
				return err
			}
			if s.id >= maxSPS {
				return errInvalid
			}
			if s.profileIdc != 66 && s.profileIdc != 77 && s.profileIdc != 100 || !s.frameMbsOnly || s.chromaFormatIdc != 1 || s.bitDepthLuma != 8 {
				return fmt.Errorf("%w: profile %d, frame_mbs_only %v, chroma %d, %d-bit (Baseline, Main or High 4:2:0 8-bit progressive needed)",
					errDepBase, s.profileIdc, s.frameMbsOnly, s.chromaFormatIdc, s.bitDepthLuma)
			}
			if s.pocType == 1 {
				return fmt.Errorf("%w: pic_order_cnt_type 1", errDepBase)
			}
			dw.spsTab[s.id] = s
			dw.lastSPS = s.id
			dw.spsNAL[s.id] = append([]byte(nil), nal...)
			sawSPS = true
			out = append(out, 0, 0, 0, 1)
			out = append(out, nal...)
			sub, subRBSP, err := subsetSPS(rbsp)
			if err != nil {
				return err
			}
			dw.subsetTab[s.id] = sub
			dw.spsRBSP[s.id] = subRBSP
			newParams = true
			return nil
		case nalPPS:
			p, err := parsePPS(unescapeRBSP(nil, nal[1:]))
			if err != nil {
				return err
			}
			dw.ppsTab[p.id] = p
			dw.ppsNAL[p.id] = append([]byte(nil), nal...)
			sawPPS[p.id] = true
			if p.id == dw.depPPS {
				dw.depPPS = -1 // the base view took our id: pick another
			}
			out = append(out, 0, 0, 0, 1)
			out = append(out, nal...)
			newParams = true
			return nil
		case nalSlice, nalSliceIDR:
			if typ == nalSliceIDR && !done && !sawSPS {
				id := dw.lastSPS
				out = append(out, 0, 0, 0, 1)
				out = append(out, dw.spsNAL[id]...)
				for i, p := range dw.ppsTab {
					if p != nil && i != dw.depPPS && p.spsID == id && !sawPPS[i] {
						out = append(out, 0, 0, 0, 1)
						out = append(out, dw.ppsNAL[i]...)
					}
				}
				newParams = true
			}
			done = true
			if newParams {
				// The MVC parameter sets go with the base view's, ahead of
				// its first slice: after it, a subset SPS would start a new
				// access unit (7.4.1.2.3).
				if dw.depPPS < 0 {
					for id := 1; id < maxPPS; id++ {
						if dw.ppsTab[id] == nil {
							dw.depPPS = id
							break
						}
					}
				}
				id := dw.lastSPS
				out = appendNAL(out, []byte{byte(3<<5 | nalSubsetSPS)}, dw.spsRBSP[id])
				var err error
				if out, err = dw.appendDepPPS(out, (id+1)%maxSPS); err != nil {
					return err
				}
				newParams = false
			}
			nh, n, err := parseNALHeader(nal)
			if err != nil {
				return err
			}
			var hdrBuf [256]byte
			raw := nal[n:]
			if len(raw) > len(hdrBuf) {
				raw = raw[:len(hdrBuf)]
			}
			var br bitReader
			br.init(unescapeRBSP(hdrBuf[:0], raw))
			var h sliceHeader
			if err := parseSliceHeader(&br, &h, nh, &dw.spsTab, &dw.subsetTab, &dw.ppsTab); err != nil {
				return err
			}
			if h.firstMb == 0 {
				if pic != nil {
					return fmt.Errorf("%w: two pictures in one access unit", errDepBase)
				}
				pic = &depPicture{
					refIdc: nh.refIdc, idr: nh.idr, idrPicID: h.idrPicID,
					frameNum: h.frameNum, pocLsb: h.pocLsb, deltaPoc: h.deltaPoc, deltaBot: h.deltaPocBottom,
					noOutputOfPriorPics: h.noOutputOfPriorPics, longTermReference: h.longTermReference,
					adaptiveMarking: h.adaptiveMarking, mmco: append([]mmcoOp(nil), h.mmco...),
					sps: h.sps,
				}
				if h.deltaPocBottom != 0 {
					// The dependent view's PPS carries no bottom field
					// order, so its POC would not match the base view's.
					return fmt.Errorf("%w: delta_pic_order_cnt_bottom %d", errDepBase, h.deltaPocBottom)
				}
			}
			// Without a prefix NAL unit the base view is view 0, usable for
			// inter-view prediction, as on Blu-ray discs.
			out = append(out, 0, 0, 0, 1)
			out = append(out, nal...)
			return nil
		case nalPrefix, nalSubsetSPS, nalSliceExt, nalDepDelimiter:
			return fmt.Errorf("%w: it already has MVC NAL units", errDepBase)
		default:
			out = append(out, 0, 0, 0, 1)
			out = append(out, nal...)
			return nil
		}
	})
	if err != nil {
		return nil, err
	}
	j := &depJob{out: out, baseEnd: len(out), pic: pic, done: make(chan struct{})}
	if pic == nil {
		j.coded = true
		close(j.done)
		dw.mu.Lock()
		dw.jobs = append(dw.jobs, j)
		dw.mu.Unlock()
		return dw.collect(false)
	}
	// The dependent view, after its delimiter (primary_pic_type 1, as
	// Blu-ray discs write it).
	j.out = appendNAL(j.out, []byte{nalDepDelimiter}, []byte{0x30})
	if src == nil {
		j.out = dw.appendDepSlice(j.out, pic, nil, depHdr{})
		j.coded = true
		close(j.done)
		dw.mu.Lock()
		dw.jobs = append(dw.jobs, j)
		dw.mu.Unlock()
		return dw.collect(false)
	}
	// The base view picture, decoded on its own for the inter-view
	// reference (dec gets it in order, with the dependent view pictures
	// before it, which may not be coded yet); held until this is coded.
	a := dw.decBase
	if err := a.DecodeAU(out[:j.baseEnd], -1); err != nil {
		return nil, err
	}
	a.finishPicture()
	if a.au == nil || a.au.pics[0] == nil {
		return nil, errInvalid
	}
	j.base = a.au.pics[0]
	a.holdPicture(j.base)
	for {
		f, ok := a.NextFrame()
		if !ok {
			break
		}
		f.Release()
	}
	ds, err := src(j.base.poc, pic.idr)
	if err != nil {
		a.releasePicture(j.base)
		return nil, err
	}
	j.ds = ds
	dw.mu.Lock()
	j.needs = dw.lastRef
	if pic.refIdc != 0 {
		dw.lastRef = j
	}
	dw.jobs = append(dw.jobs, j)
	if dw.coders == nil {
		dw.coders = make(chan *depCoder, depPipeline)
		for i := 0; i < depPipeline; i++ {
			c := newDepCoder(pic.sps.widthMbs, pic.sps.heightMbs(), dw.qp)
			c.interView = true
			dw.pool = append(dw.pool, c)
			dw.coders <- c
		}
	}
	dw.start()
	dw.mu.Unlock()
	return dw.collect(false)
}

// Flush waits for the pictures being coded and gives their access units.
func (dw *DepWriter) Flush() ([][]byte, error) {
	return dw.collect(true)
}

// start launches, in order, the jobs whose reference picture dec has
// been fed (so that its reference lists are the decoder's), holding
// dw.mu.
func (dw *DepWriter) start() {
	for _, j := range dw.jobs {
		if j.started || j.coded {
			continue
		}
		if j.needs != nil && !j.needs.coded {
			break // the rest come after it
		}
		if j.needs != nil && !j.needs.fedTo(dw) {
			break
		}
		j.started = true
		dw.resolveRefs(j)
		if j.needs != nil {
			j.from = j.needs.vecs
			if j.pic != nil && j.pic.refIdc != 0 {
				// the pictures before it, in order, have all been given
				// them: the reference before's vectors are done with
				j.needs.vecs = [3][]mv{}
			}
		}
		go dw.code(j)
	}
}

// fedTo reports whether the job's access unit has been fed to dec
// (holding dw.mu).
func (j *depJob) fedTo(dw *DepWriter) bool {
	for i, k := range dw.jobs {
		if k == j {
			return i < dw.fed
		}
	}
	return true // given out already, so fed
}

// resolveRefs finds the job's references in dec, as it is after the
// pictures before it, and holds them: the nearest short-term reference
// before it in output order and, with a B picture of the base view, the
// nearest after it; the base view picture first or second.
func (dw *DepWriter) resolveRefs(j *depJob) {
	d, pic, base := dw.dec, j.pic, j.base
	var past, future *picture
	if !pic.idr && !dw.InterViewOnly {
		for _, r := range d.views[1].refs {
			if !r.shortRef || r.longRef {
				continue
			}
			if r.poc < base.poc && (past == nil || r.poc > past.poc) {
				past = r
			}
			if r.poc > base.poc && (future == nil || r.poc < future.poc) {
				future = r
			}
		}
	}
	s := pic.sps
	maxFN := 1 << s.log2MaxFrameNum
	picNumDiff := func(r *picture) int { return pic.frameNum - frameNumWrap(r, pic.frameNum, maxFN) - 1 }
	switch {
	case past == nil && future == nil:
		j.bslice, j.list = false, [2][]*picture{{base}, nil}
	case dw.NoBSlices || !s.direct8x8Inference:
		// P slices: the base view first, then the latest reference.
		t := past
		if future != nil && (t == nil || frameNumWrap(future, pic.frameNum, maxFN) > frameNumWrap(t, pic.frameNum, maxFN)) {
			t = future
		}
		j.bslice, j.list = false, [2][]*picture{{base, t}, nil}
		j.hd.diff[0] = picNumDiff(t)
	default:
		t0, t1 := past, future
		if t0 == nil {
			t0 = t1
		}
		if t1 == nil {
			t1 = t0
		}
		j.bslice, j.list = true, [2][]*picture{{t0, base}, {t1}}
		j.hd.diff[0], j.hd.diff[1] = picNumDiff(t0), picNumDiff(t1)
	}
	j.hd.bslice = j.bslice
	for _, l := range j.list {
		for _, r := range l {
			if r != base {
				d.holdPicture(r)
			}
		}
	}
}

// code codes a job's picture on a coder from the pool, then lets the
// pipeline feed it to dec and give it out.
func (dw *DepWriter) code(j *depJob) {
	c := <-dw.coders
	pic := j.pic
	c.src = *j.ds
	c.aq = dw.AQ
	// the picture's kind first: its quantiser's lambda depends on it
	c.bslice, c.list = j.bslice, j.list
	c.planQP(dw.qp)
	c.tag = int(j.base.poc)
	// the search starts from the vectors of the reference picture before
	// it (coded already), whichever coder did it: the same every run
	for k := range c.prev {
		if j.from[k] != nil {
			copy(c.prev[k], j.from[k])
		} else {
			clear(c.prev[k])
		}
	}
	j.from = [3][]mv{}
	for _, l := range c.list {
		for _, r := range l {
			r.waitRows(r.mbH) // decoded in full before predicting from it
			r.halfPel()
		}
	}
	j.out = dw.appendDepSlice(j.out, pic, c, j.hd)
	if pic.refIdc != 0 {
		for k := range c.prev {
			j.vecs[k] = append([]mv(nil), c.prev[k]...)
		}
	}
	dw.coders <- c
	for _, l := range j.list {
		for _, r := range l {
			if r != j.base {
				dw.dec.releasePicture(r)
			}
		}
	}
	dw.decBase.releasePicture(j.base)
	dw.mu.Lock()
	j.coded = true
	close(j.done)
	dw.feed()
	dw.start()
	dw.mu.Unlock()
}

// feed gives dec the access units of the jobs coded so far, in order:
// each base view picture then its dependent view picture, a reference
// for the next ones (holding dw.mu).
func (dw *DepWriter) feed() {
	d := dw.dec
	for dw.fed < len(dw.jobs) && dw.jobs[dw.fed].coded && dw.failed == nil {
		j := dw.jobs[dw.fed]
		dw.fed++
		if j.pic == nil || j.ds == nil {
			continue // nothing decoded for it
		}
		if err := d.DecodeAU(j.out[:j.baseEnd], -1); err != nil {
			dw.failed = err
			return
		}
		d.finishPicture()
		if err := d.DecodeAU(j.out[j.baseEnd:], -1); err != nil {
			dw.failed = err
			return
		}
		d.finishPicture()
		for {
			f, ok := d.NextFrame()
			if !ok {
				break
			}
			f.Release()
		}
	}
}

// collect gives out the access units of the jobs at the front that are
// coded and fed; with flush, it waits for all of them.
func (dw *DepWriter) collect(flush bool) ([][]byte, error) {
	var aus [][]byte
	for {
		dw.mu.Lock()
		if dw.failed != nil {
			err := dw.failed
			dw.mu.Unlock()
			return nil, err
		}
		if len(dw.jobs) > 0 && dw.jobs[0].coded && dw.fed > 0 {
			j := dw.jobs[0]
			dw.jobs = dw.jobs[1:]
			dw.fed--
			dw.mu.Unlock()
			aus = append(aus, j.out)
			continue
		}
		if !flush || len(dw.jobs) == 0 {
			dw.mu.Unlock()
			return aus, nil
		}
		j := dw.jobs[0]
		dw.mu.Unlock()
		<-j.done
	}
}

// BaseView strips an access unit of what an MVC stream adds to the base
// view (prefix NAL units, the subset SPS, the dependent view's slices and
// delimiter), leaving the plain H.264 base view a DepWriter takes.
func BaseView(au []byte) []byte {
	var out []byte
	_ = forEachNAL(au, func(nal []byte) error {
		if len(nal) == 0 {
			return nil
		}
		switch nal[0] & 31 {
		case nalPrefix, nalSubsetSPS, nalSliceExt, nalDepDelimiter:
			return nil
		}
		out = append(out, 0, 0, 0, 1)
		out = append(out, nal...)
		return nil
	})
	return out
}

// IsIDR reports whether the access unit holds an IDR picture.
func IsIDR(au []byte) bool {
	idr := false
	_ = forEachNAL(au, func(nal []byte) error {
		if len(nal) > 0 && nal[0]&31 == nalSliceIDR {
			idr = true
		}
		return nil
	})
	return idr
}

// subsetSPS builds the Stereo High subset SPS (7.3.2.1.3, H.7.3.2.1.4) for
// a base view SPS: the same seq_parameter_set_data with profile_idc 128,
// two views, and view 1 predicting from view 0.
func subsetSPS(base []byte) (*sps, []byte, error) {
	end := rbspDataBits(base)
	if end < 24 {
		return nil, nil, errInvalid
	}
	var w bitWriter
	w.u(8, 128) // profile_idc: Stereo High
	var br bitReader
	br.init(base)
	br.skip(8)
	w.u(16, br.u(16)) // constraint flags, level_idc
	// seq_parameter_set_id: its own, as on Blu-ray discs (the base view's
	// plus one), so no decoder takes one parameter set for the other.
	id := br.ue()
	w.ue((id + 1) % maxSPS)
	switch base[0] {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
	default:
		// Stereo High carries the fields Main and Baseline leave out:
		// 4:2:0, 8-bit, no transform bypass, no scaling matrices.
		w.ue(1)
		w.ue(0)
		w.ue(0)
		w.u(1, 0)
		w.u(1, 0)
	}
	for i := br.bitPos(); i < end; i++ {
		w.u(1, br.u1())
	}
	w.u(1, 1) // bit_equal_to_one
	// seq_parameter_set_mvc_extension
	w.ue(1)       // num_views_minus1
	w.ue(0)       // view_id[0]
	w.ue(1)       // view_id[1]
	for range 2 { // anchor, then non-anchor references of view 1
		w.ue(1) // num_*_refs_l0
		w.ue(0) // *_ref_l0[1][0]: view 0
		w.ue(0) // num_*_refs_l1
	}
	w.ue(0)                 // num_level_values_signalled_minus1
	w.u(8, uint32(base[2])) // level_idc: the base view's
	w.ue(0)                 // num_applicable_ops_minus1
	w.u(3, 0)               // applicable_op_temporal_id
	w.ue(1)                 // applicable_op_num_target_views_minus1
	w.ue(0)                 // applicable_op_target_view_id
	w.ue(1)                 //
	w.ue(1)                 // applicable_op_num_views_minus1
	w.u(1, 0)               // mvc_vui_parameters_present_flag
	w.u(1, 0)               // additional_extension2_flag
	rbsp := w.trailing()
	s, err := parseSubsetSPS(rbsp)
	if err != nil {
		return nil, nil, err
	}
	return s, rbsp, nil
}

// rbspDataBits returns the number of bits before rbsp_trailing_bits.
func rbspDataBits(rbsp []byte) int {
	for i := len(rbsp) - 1; i >= 0; i-- {
		if c := rbsp[i]; c != 0 {
			tz := 0
			for c&1 == 0 {
				c >>= 1
				tz++
			}
			return i*8 + 7 - tz
		}
	}
	return 0
}

// appendDepPPS writes the dependent view's PPS: CABAC, one reference,
// no weighted prediction, deblocking control present, 8x8 transforms.
func (dw *DepWriter) appendDepPPS(out []byte, spsID int) ([]byte, error) {
	var w bitWriter
	w.ue(uint32(dw.depPPS))
	w.ue(uint32(spsID))
	w.flag(true)  // entropy_coding_mode_flag: CABAC
	w.flag(false) // bottom_field_pic_order_in_frame_present_flag
	w.ue(0)       // num_slice_groups_minus1
	w.ue(0)       // num_ref_idx_l0_default_active_minus1
	w.ue(0)       // num_ref_idx_l1_default_active_minus1
	w.flag(false) // weighted_pred_flag
	w.u(2, 0)     // weighted_bipred_idc
	w.se(int32(dw.qp - 26))
	w.se(0)       // pic_init_qs_minus26
	w.se(0)       // chroma_qp_index_offset
	w.flag(true)  // deblocking_filter_control_present_flag
	w.flag(false) // constrained_intra_pred_flag
	w.flag(false) // redundant_pic_cnt_present_flag
	w.flag(true)  // transform_8x8_mode_flag
	w.flag(false) // pic_scaling_matrix_present_flag
	w.se(0)       // second_chroma_qp_index_offset
	rbsp := w.trailing()
	p, err := parsePPS(rbsp)
	if err != nil {
		return nil, err
	}
	dw.ppsTab[p.id] = p
	return appendNAL(out, []byte{byte(3<<5 | nalPPS)}, rbsp), nil
}

// depSlices is how many slices a dependent view picture is cut into, each
// coded on its own goroutine. One: a player's MVC decoder (Amlogic)
// stutters on a base view cut into several, and a picture's macroblock
// rows are decided in parallel anyway (the wavefront in depmb.go), so a
// slice costs nothing in speed.
const depSlices = 1

// depHdr is what a dependent view slice header says of the reference
// lists: P or B slices, and the temporal references' distance in PicNum
// (diff+1 back) for list 0 and list 1.
type depHdr struct {
	bslice bool
	diff   [2]int
}

// appendDepSlice writes the dependent view picture of an access unit,
// coded by c with the reference lists hd describes. Without a coder it is
// one slice of P_Skip macroblocks from the base view: the dependent view
// repeats the base view.
func (dw *DepWriter) appendDepSlice(out []byte, pic *depPicture, c *depCoder, hd depHdr) []byte {
	s := pic.sps
	hdr := mvcNALHeader(nalSliceExt, pic.refIdc, pic.idr, 1, pic.idr, false)
	if c == nil {
		var w bitWriter
		dw.depSliceHeader(&w, pic, 0, 1, depHdr{})
		var e cabacEncoder
		e.start(&w, 1, dw.qp)
		mbs := s.widthMbs * s.heightMbs()
		for mb := 0; mb < mbs; mb++ {
			e.decision(11, 1) // mb_skip_flag (neighbours skipped: ctxInc 0)
			if mb == mbs-1 {
				e.terminate(1)
			} else {
				e.terminate(0)
			}
		}
		w.alignZeros()
		return appendNAL(out, hdr, w.bytes())
	}
	n := depSlices
	if dw.Slices > 0 {
		n = dw.Slices
	}
	n = min(n, c.mbH)
	c.wavefrontBuf() // shared by the slices' copies of c
	parts := make([][]byte, n)
	workers := make([]depCoder, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wk := &workers[i]
		*wk = *c
		wk.rowStart, wk.rowEnd = c.mbH*i/n, c.mbH*(i+1)/n
		wk.stats = depStats{}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var w bitWriter
			dw.depSliceHeader(&w, pic, wk.rowStart*c.mbW, len(c.list[0]), hd)
			var e cabacEncoder
			e.start(&w, 1, dw.qp)
			wk.codeSlice(&e)
			w.alignZeros()
			parts[i] = w.bytes()
		}(i)
	}
	wg.Wait()
	for i := range workers {
		c.stats.add(workers[i].stats)
		out = appendNAL(out, hdr, parts[i])
	}
	return out
}

// depSliceHeader writes a dependent view slice header (7.3.3), up to and
// including cabac_alignment_one_bit.
func (dw *DepWriter) depSliceHeader(w *bitWriter, pic *depPicture, firstMb, nref int, hd depHdr) {
	s := pic.sps
	w.ue(uint32(firstMb)) // first_mb_in_slice
	if hd.bslice {
		w.ue(6) // slice_type: B (all slices)
	} else {
		w.ue(5) // slice_type: P (all slices)
	}
	w.ue(uint32(dw.depPPS))                      // pic_parameter_set_id
	w.u(s.log2MaxFrameNum, uint32(pic.frameNum)) // frame_num
	if pic.idr {
		w.ue(uint32(pic.idrPicID))
	}
	if s.pocType == 0 {
		w.u(s.log2MaxPocLsb, uint32(pic.pocLsb))
	}
	if hd.bslice {
		w.flag(true) // direct_spatial_mv_pred_flag
	}
	w.flag(true)           // num_ref_idx_active_override_flag
	w.ue(uint32(nref - 1)) // num_ref_idx_l0_active_minus1
	if hd.bslice {
		w.ue(0) // num_ref_idx_l1_active_minus1
	}
	// ref_pic_list_mvc_modification. List 0 of a P slice: the base view
	// picture, then the temporal reference; of a B slice: the temporal
	// reference, then the base view picture. List 1: its temporal
	// reference.
	w.flag(true)
	if hd.bslice {
		w.ue(0) // modification_of_pic_nums_idc: abs_diff_pic_num_minus1 subtracted
		w.ue(uint32(hd.diff[0]))
		w.ue(5) // modification_of_pic_nums_idc: abs_diff_view_idx_minus1 added
		w.ue(0) //   view index 0, the base view
	} else {
		w.ue(5)
		w.ue(0)
		if nref > 1 {
			w.ue(0)
			w.ue(uint32(hd.diff[0]))
		}
	}
	w.ue(3)
	if hd.bslice {
		w.flag(true)
		w.ue(0)
		w.ue(uint32(hd.diff[1]))
		w.ue(3)
	}
	if pic.refIdc != 0 {
		if pic.idr {
			w.flag(pic.noOutputOfPriorPics)
			w.flag(pic.longTermReference)
		} else {
			w.flag(pic.adaptiveMarking)
			if pic.adaptiveMarking {
				for _, m := range pic.mmco {
					w.ue(uint32(m.op))
					if m.op == 1 || m.op == 3 {
						w.ue(m.diffPicNums)
					}
					if m.op == 2 {
						w.ue(m.longTermPicNum)
					}
					if m.op == 3 || m.op == 6 {
						w.ue(m.longTermFrameIdx)
					}
					if m.op == 4 {
						w.ue(m.maxLongTermIdxP1)
					}
				}
				w.ue(0)
			}
		}
	}
	w.ue(0) // cabac_init_idc
	w.se(0) // slice_qp_delta
	if dw.noDeblock {
		w.ue(1) // disable_deblocking_filter_idc
	} else {
		w.ue(0) // disable_deblocking_filter_idc
		w.se(0) // slice_alpha_c0_offset_div2
		w.se(0) // slice_beta_offset_div2
	}
	w.alignOnes()
}

// forEachNAL calls fn with each NAL unit of an Annex B buffer, without its
// start code (and with trailing zero bytes removed).
func forEachNAL(b []byte, fn func([]byte) error) error {
	start := findStartCode(b, 0)
	for start >= 0 {
		next := findStartCode(b, start+3)
		end := len(b)
		if next >= 0 {
			end = next
		}
		nal := b[start+3 : end]
		for len(nal) > 0 && nal[len(nal)-1] == 0 {
			nal = nal[:len(nal)-1]
		}
		if err := fn(nal); err != nil {
			return err
		}
		if next < 0 {
			break
		}
		start = next
	}
	return nil
}

// ConfigRecord returns the MVCDecoderConfigurationRecord (ISO/IEC
// 14496-15) of an MVC stream's start, Annex B: its first subset SPS and the
// PPS after it, which the dependent view's slices use. Matroska carries it
// in an 'mvcC' BlockAdditionMapping, which is what tells players the track
// holds a second view.
func ConfigRecord(stream []byte) ([]byte, error) {
	var subset, pps []byte
	err := forEachNAL(stream, func(nal []byte) error {
		switch int(nal[0] & 31) {
		case nalSubsetSPS:
			if subset == nil {
				subset = nal
			}
		case nalPPS:
			if subset != nil && pps == nil {
				pps = nal
			}
		}
		if subset != nil && pps != nil {
			return errStop
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStop) {
		return nil, err
	}
	if subset == nil || pps == nil || len(subset) < 4 {
		return nil, fmt.Errorf("mvc: no subset SPS and PPS at the start of the stream")
	}
	r := []byte{1, subset[1], subset[2], subset[3], 0xff, 0xe1, byte(len(subset) >> 8), byte(len(subset))}
	r = append(r, subset...)
	r = append(r, 1, byte(len(pps)>>8), byte(len(pps)))
	return append(r, pps...), nil
}

var errStop = errors.New("stop")
