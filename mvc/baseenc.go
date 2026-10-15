package mvc

import (
	"errors"
	"fmt"
	"sync"
)

// The base view encoder: an H.264 High profile encoder for a 2D stream or
// the left eye of an MVC one, on the macroblock coder the dependent view
// uses. Its pictures are IDR pictures every so often, P pictures between
// them and B pictures (never references) between those; it writes them
// in decoding order, each as one access unit, and keeps the decoder in
// the loop for its references.

// BaseOptions configure a BaseEncoder.
type BaseOptions struct {
	Width, Height int // the frames' size; the coded size is whole macroblocks
	QP            int // the P pictures' quantiser; I pictures 3 below, B pictures 2 above
	// AQ is the strength of adaptive quantisation, each macroblock's
	// quantiser moved by its energy about the picture's mean (flat and
	// dark ones lower, busy ones higher), as x264's variance mode does
	// about a fixed energy (AQAbsolute): 1 is its usual setting, 0 none.
	// Measured on Gravity, either loses about a VMAF point at equal size.
	AQ      float64
	Keyint  int // frames between IDR pictures (0: 250)
	BFrames int // B pictures between references (0: none)
	Refs    int // reference pictures kept (0: 3)
	Slices  int // slices per picture, each coded on its own goroutine (0: 1; players' MVC decoders stutter on more, so pictures are coded concurrently instead)
	Level   int // level_idc (0: 41)
	FPSNum  int // frame rate, for the VUI (0: none)
	FPSDen  int
	Color   *ColorInfo // colour description for the VUI, or nil
}

// ColorInfo is the VUI's colour description (H.273 code points).
type ColorInfo struct {
	Primaries, Transfer, Matrix int
	FullRange                   bool
}

// BaseEncoder encodes a view's frames.
//
// Pictures are coded concurrently, each on its own coder as soon as the
// pictures it predicts from are decoded: the B pictures between two
// references together, and the next reference picture along with them.
// One goroutine owns the decoder and decodes the pictures in decoding
// order as they are coded.
type BaseEncoder struct {
	o    BaseOptions
	w, h int // coded size
	dec  *Decoder
	sps  *sps
	hdrs []byte // SPS and PPS NAL units, Annex B

	// the frames waiting to be coded, in display order, and their count
	// so far
	queue []*baseFrame
	given int
	// the coded video sequence: pictures since the last IDR, frame_num
	// and idr_pic_id
	sinceIDR int
	frameNum int
	idrID    int
	idrIdx   int      // display index of the last IDR picture
	lastRef  *baseJob // the latest reference picture: the next pictures predict from it

	jobs    []*baseJob    // pictures queued, in decoding order, until returned
	submit  chan *baseJob // to the decoding goroutine
	coders  chan *depCoder
	stats   depStats
	statsMu sync.Mutex

	noDeblock bool // tests: leave the pictures unfiltered
}

type baseFrame struct {
	src   *DepSource
	index int // display order
	pts   int64
}

// baseJob is a picture to code.
type baseJob struct {
	f        *baseFrame
	typ      int // slice type as queued; a B picture without a reference on each side is coded as P
	idr      bool
	refIdc   int
	frameNum int
	idrID    int
	poc      int32
	after    *baseJob // decoded before this picture is coded: its references come from it (nil: none)

	list  [2][]*picture // its reference lists, set when it is launched
	coded chan struct{} // closed once au and err are set
	au    []byte
	err   error
	done  chan struct{} // closed once decoded, or failed
}

// NewBaseEncoder returns an encoder for frames of the options' size.
func NewBaseEncoder(o BaseOptions) (*BaseEncoder, error) {
	if o.Width <= 0 || o.Height <= 0 {
		return nil, errors.New("mvc: no frame size")
	}
	if o.Keyint <= 0 {
		o.Keyint = 250
	}
	if o.Refs <= 0 {
		o.Refs = 3
	}
	if o.Refs > 4 {
		o.Refs = 4
	}
	if o.Slices <= 0 {
		o.Slices = 1
	}
	if o.Level <= 0 {
		o.Level = 41
	}
	if o.QP < 3 || o.QP > 49 {
		return nil, fmt.Errorf("mvc: QP %d out of range (3-49)", o.QP)
	}
	e := &BaseEncoder{o: o, w: (o.Width + 15) &^ 15, h: (o.Height + 15) &^ 15, dec: NewDecoder(Options{})}
	spsRBSP := e.spsRBSP()
	s, err := parseSPS(spsRBSP)
	if err != nil {
		return nil, fmt.Errorf("mvc: the SPS written does not parse: %w", err)
	}
	e.sps = s
	e.hdrs = appendNAL(nil, []byte{3<<5 | nalSPS}, spsRBSP)
	e.hdrs = appendNAL(e.hdrs, []byte{3<<5 | nalPPS}, e.ppsRBSP())
	// coders for the pictures in flight at once: a group's B pictures
	// and the next reference
	n := o.BFrames + 2
	e.coders = make(chan *depCoder, n)
	for range n {
		e.coders <- newDepCoder(e.w/16, e.h/16, o.QP)
	}
	e.submit = make(chan *baseJob, 64)
	go e.run()
	return e, nil
}

// spsRBSP writes the sequence parameter set: High profile, 4:2:0 8-bit,
// progressive, POC type 0, with the VUI's timing and colour description.
func (e *BaseEncoder) spsRBSP() []byte {
	o := &e.o
	var w bitWriter
	w.u(8, 100)             // profile_idc: High
	w.u(8, 0)               // constraint flags
	w.u(8, uint32(o.Level)) // level_idc
	w.ue(0)                 // seq_parameter_set_id
	w.ue(1)                 // chroma_format_idc: 4:2:0
	w.ue(0)                 // bit_depth_luma_minus8
	w.ue(0)                 // bit_depth_chroma_minus8
	w.u(1, 0)               // qpprime_y_zero_transform_bypass_flag
	w.u(1, 0)               // seq_scaling_matrix_present_flag
	w.ue(4)                 // log2_max_frame_num_minus4: 8 bits
	w.ue(0)                 // pic_order_cnt_type
	w.ue(4)                 // log2_max_pic_order_cnt_lsb_minus4: 8 bits
	w.ue(uint32(o.Refs))    // max_num_ref_frames
	w.u(1, 0)               // gaps_in_frame_num_value_allowed_flag
	w.ue(uint32(e.w/16 - 1))
	w.ue(uint32(e.h/16 - 1))
	w.u(1, 1) // frame_mbs_only_flag
	w.u(1, 1) // direct_8x8_inference_flag
	if e.w != o.Width || e.h != o.Height {
		w.u(1, 1) // frame_cropping_flag
		w.ue(0)
		w.ue(uint32((e.w - o.Width) / 2))
		w.ue(0)
		w.ue(uint32((e.h - o.Height) / 2))
	} else {
		w.u(1, 0)
	}
	w.u(1, 1) // vui_parameters_present_flag
	w.u(1, 0) // aspect_ratio_info_present_flag
	w.u(1, 0) // overscan_info_present_flag
	if c := o.Color; c != nil {
		w.u(1, 1) // video_signal_type_present_flag
		w.u(3, 5) // video_format: unspecified
		w.flag(c.FullRange)
		w.u(1, 1) // colour_description_present_flag
		w.u(8, uint32(c.Primaries))
		w.u(8, uint32(c.Transfer))
		w.u(8, uint32(c.Matrix))
	} else {
		w.u(1, 0)
	}
	w.u(1, 0) // chroma_loc_info_present_flag
	if o.FPSNum > 0 && o.FPSDen > 0 {
		w.u(1, 1) // timing_info_present_flag
		w.u(32, uint32(o.FPSDen))
		w.u(32, uint32(2*o.FPSNum))
		w.u(1, 1) // fixed_frame_rate_flag
	} else {
		w.u(1, 0)
	}
	w.u(1, 0) // nal_hrd_parameters_present_flag
	w.u(1, 0) // vcl_hrd_parameters_present_flag
	w.u(1, 0) // pic_struct_present_flag
	w.u(1, 1) // bitstream_restriction_flag
	w.u(1, 1) // motion_vectors_over_pic_boundaries_flag
	w.ue(0)   // max_bytes_per_pic_denom
	w.ue(0)   // max_bits_per_mb_denom
	w.ue(16)  // log2_max_mv_length_horizontal
	w.ue(16)  // log2_max_mv_length_vertical
	reorder := 0
	if o.BFrames > 0 {
		reorder = 1
	}
	w.ue(uint32(reorder)) // max_num_reorder_frames
	w.ue(uint32(o.Refs))  // max_dec_frame_buffering
	return w.trailing()
}

// ppsRBSP writes the picture parameter set: CABAC, 8x8 transforms,
// deblocking control, no weighted prediction.
func (e *BaseEncoder) ppsRBSP() []byte {
	var w bitWriter
	w.ue(0)                    // pic_parameter_set_id
	w.ue(0)                    // seq_parameter_set_id
	w.flag(true)               // entropy_coding_mode_flag
	w.flag(false)              // bottom_field_pic_order_in_frame_present_flag
	w.ue(0)                    // num_slice_groups_minus1
	w.ue(uint32(e.o.Refs - 1)) // num_ref_idx_l0_default_active_minus1
	w.ue(0)                    // num_ref_idx_l1_default_active_minus1
	w.flag(false)              // weighted_pred_flag
	w.u(2, 0)                  // weighted_bipred_idc
	w.se(0)                    // pic_init_qp_minus26
	w.se(0)                    // pic_init_qs_minus26
	w.se(0)                    // chroma_qp_index_offset
	w.flag(true)               // deblocking_filter_control_present_flag
	w.flag(false)              // constrained_intra_pred_flag
	w.flag(false)              // redundant_pic_cnt_present_flag
	w.flag(true)               // transform_8x8_mode_flag
	w.flag(false)              // pic_scaling_matrix_present_flag
	w.se(0)                    // second_chroma_qp_index_offset
	return w.trailing()
}

// Encode takes the next frame (display order) and returns the access
// units coded so far, in decoding order: a reference picture once the
// frames before it are in, then the B pictures between it and the one
// before. The source is used until the frame is coded, so the caller must
// not touch it again. Nothing may be encoded after Flush.
func (e *BaseEncoder) Encode(src *DepSource, pts int64) ([][]byte, error) {
	e.queue = append(e.queue, &baseFrame{src: src, index: e.given, pts: pts})
	e.given++
	// A GOP's first frame is an IDR picture; a reference picture every
	// BFrames+1 frames, queued once its B pictures are in.
	if len(e.queue) >= e.o.BFrames+1 {
		e.queueGroup(false)
	}
	// hold back when the pictures pile up: the frames in flight hold
	// their sources
	return e.collect(4 * (e.o.BFrames + 1))
}

// Flush codes the frames still queued, returns their access units and
// ends the encoder.
func (e *BaseEncoder) Flush() ([][]byte, error) {
	for len(e.queue) > 0 {
		e.queueGroup(true)
	}
	close(e.submit)
	return e.collect(0)
}

// collect returns the access units of the pictures decoded so far, in
// decoding order, waiting for them while more than limit are in flight.
func (e *BaseEncoder) collect(limit int) ([][]byte, error) {
	var out [][]byte
	for len(e.jobs) > 0 {
		j := e.jobs[0]
		if len(e.jobs) > limit {
			<-j.done
		} else {
			select {
			case <-j.done:
			default:
				return out, nil
			}
		}
		if j.err != nil {
			return nil, j.err
		}
		out = append(out, j.au)
		e.jobs = e.jobs[1:]
	}
	return out, nil
}

// queueGroup queues the reference picture at the end of the queue (or an
// IDR picture at its start when one is due) then the B pictures before
// it.
func (e *BaseEncoder) queueGroup(flush bool) {
	// an IDR picture when due, on its own
	if e.sinceIDR == 0 || e.sinceIDR >= e.o.Keyint {
		e.queuePicture(e.queue[0], sliceI, true)
		e.queue = e.queue[1:]
		if !flush && len(e.queue) < e.o.BFrames+1 {
			return
		}
		if len(e.queue) == 0 {
			return
		}
	}
	// the reference, then the B pictures before it
	n := min(len(e.queue), e.o.BFrames+1)
	if e.sinceIDR+n > e.o.Keyint {
		n = e.o.Keyint - e.sinceIDR
	}
	e.queuePicture(e.queue[n-1], sliceP, false)
	for _, f := range e.queue[:n-1] {
		e.queuePicture(f, sliceB, false)
	}
	e.queue = e.queue[n:]
}

// queuePicture submits a picture to the pipeline, with its place in the
// coded video sequence.
func (e *BaseEncoder) queuePicture(f *baseFrame, typ int, idr bool) {
	j := &baseJob{f: f, typ: typ, idr: idr, after: e.lastRef, coded: make(chan struct{}), done: make(chan struct{})}
	switch typ {
	case sliceI:
		j.refIdc = 3
	case sliceP:
		j.refIdc = 2
	}
	if idr {
		e.sinceIDR, e.frameNum = 0, 0
		e.idrID = (e.idrID + 1) % 65536
		e.idrIdx = f.index
	}
	j.frameNum, j.idrID = e.frameNum, e.idrID
	// POC counts display order from the IDR picture, twice the frame's
	// distance from it.
	j.poc = int32(2 * (f.index - e.idrIdx))
	if j.refIdc != 0 {
		e.frameNum = (e.frameNum + 1) % 256
		e.lastRef = j
	}
	e.sinceIDR++
	e.jobs = append(e.jobs, j)
	e.submit <- j
}

// run is the pipeline: it launches each picture's coding once the picture
// it waits for is decoded, and decodes the pictures in decoding order as
// they are coded.
func (e *BaseEncoder) run() {
	var pending []*baseJob // submitted, in decoding order, until decoded
	waiting := map[*baseJob][]*baseJob{}
	decoded := map[*baseJob]bool{}
	launched := map[*baseJob]bool{}
	var failed error
	submit := e.submit
	launch := func(j *baseJob) {
		j.list = e.listsFor(j)
		launched[j] = true
		go func() {
			c := <-e.coders
			j.au, j.err = e.codePicture(j, c)
			e.coders <- c
			close(j.coded)
		}()
	}
	for submit != nil || len(pending) > 0 {
		var codedCh chan struct{}
		if len(pending) > 0 {
			if failed != nil && !launched[pending[0]] {
				// nothing to wait for: fail it
				codedCh = make(chan struct{})
				close(codedCh)
			} else {
				codedCh = pending[0].coded
			}
		}
		select {
		case j, ok := <-submit:
			if !ok {
				submit = nil
				continue
			}
			pending = append(pending, j)
			switch {
			case failed != nil:
			case j.after == nil || decoded[j.after]:
				launch(j)
			default:
				waiting[j.after] = append(waiting[j.after], j)
			}
		case <-codedCh:
			j := pending[0]
			pending = pending[1:]
			delete(launched, j)
			switch {
			case failed != nil:
				j.err = failed
			case j.err != nil:
				failed = j.err
			default:
				// decode it: the reference pictures, deblocked
				if err := e.dec.DecodeAU(j.au, j.f.pts); err != nil {
					j.err = fmt.Errorf("mvc: the picture written does not decode: %w", err)
					failed = j.err
					break
				}
				e.dec.finishPicture()
				for {
					fr, ok := e.dec.NextFrame()
					if !ok {
						break
					}
					fr.Release()
				}
			}
			close(j.done)
			if j.refIdc != 0 {
				// later pictures wait for this one or a later reference
				clear(decoded)
			}
			decoded[j] = true
			if failed == nil {
				for _, d := range waiting[j] {
					launch(d)
				}
			}
			delete(waiting, j)
		}
	}
}

// listsFor builds a picture's reference lists from the decoder's
// reference pictures: for P the past ones, nearest first; for B the two
// nearest past ones and the nearest future one. Called on the pipeline's
// goroutine, between decodes.
func (e *BaseEncoder) listsFor(j *baseJob) [2][]*picture {
	var past, future []*picture
	for _, r := range e.dec.views[0].refs {
		if !r.shortRef {
			continue
		}
		if r.poc < j.poc {
			past = append(past, r)
		} else {
			future = append(future, r)
		}
	}
	sortByPOC(past, true)
	sortByPOC(future, false)
	switch j.typ {
	case sliceI:
		return [2][]*picture{}
	case sliceP:
		return [2][]*picture{past[:min(len(past), e.o.Refs)], nil}
	default:
		if len(future) == 0 || len(past) == 0 {
			// without references either side a B picture is a P picture
			return [2][]*picture{append(past, future...)[:1], nil}
		}
		return [2][]*picture{past[:min(len(past), 2)], future[:1]}
	}
}

// codePicture codes one picture as an access unit on the coder.
func (e *BaseEncoder) codePicture(j *baseJob, c *depCoder) ([]byte, error) {
	o := &e.o
	typ := j.typ
	qp := o.QP
	switch typ {
	case sliceI:
		qp = max(o.QP-3, 0)
	default:
		if typ == sliceB && j.list[1] == nil {
			typ = sliceP
		}
		if typ == sliceB {
			qp = min(o.QP+2, 51)
		}
	}
	c.tag = j.f.index
	c.islice, c.bslice = typ == sliceI, typ == sliceB
	c.list = j.list
	// the references must be decoded in full (deblocked, their borders
	// extended) before predicting from them: the decoder's threads may
	// still be at the previous picture
	for _, l := range c.list {
		for _, r := range l {
			r.waitRows(r.mbH)
			r.halfPel()
		}
	}
	c.src = *j.f.src
	c.aq = o.AQ
	c.planQP(qp)
	var out []byte
	if j.idr {
		out = append(out, e.hdrs...)
	}
	hdr := []byte{byte(j.refIdc<<5 | nalSlice)}
	if j.idr {
		hdr[0] = byte(j.refIdc<<5 | nalSliceIDR)
	}
	n := min(o.Slices, c.mbH)
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
			e.sliceHeader(&w, j, wk, typ, wk.rowStart*c.mbW)
			var cab cabacEncoder
			initSet := 1 // cabac_init_idc 0
			if typ == sliceI {
				initSet = 0
			}
			cab.start(&w, initSet, c.qp)
			wk.codeSlice(&cab)
			w.alignZeros()
			parts[i] = w.bytes()
		}(i)
	}
	wg.Wait()
	e.statsMu.Lock()
	for i := range workers {
		e.stats.add(workers[i].stats)
	}
	e.statsMu.Unlock()
	for i := range workers {
		out = appendNAL(out, hdr, parts[i])
	}
	return out, nil
}

// sliceHeader writes a slice header up to cabac_alignment_one_bit.
func (e *BaseEncoder) sliceHeader(w *bitWriter, j *baseJob, c *depCoder, typ, firstMb int) {
	w.ue(uint32(firstMb))
	w.ue(uint32(5 + typ)) // slice_type, all slices the same
	w.ue(0)               // pic_parameter_set_id
	w.u(8, uint32(j.frameNum))
	if j.idr {
		w.ue(uint32(j.idrID))
	}
	w.u(8, uint32(j.poc)&255) // pic_order_cnt_lsb
	if typ == sliceB {
		w.flag(true) // direct_spatial_mv_pred_flag
	}
	if typ != sliceI {
		w.flag(true) // num_ref_idx_active_override_flag
		w.ue(uint32(len(c.list[0]) - 1))
		if typ == sliceB {
			w.ue(uint32(len(c.list[1]) - 1))
		}
		w.flag(false) // ref_pic_list_modification_flag_l0: the default order
		if typ == sliceB {
			w.flag(false)
		}
	}
	if j.refIdc != 0 {
		if j.idr {
			w.flag(false) // no_output_of_prior_pics_flag
			w.flag(false) // long_term_reference_flag
		} else {
			w.flag(false) // adaptive_ref_pic_marking_mode_flag
		}
	}
	if typ != sliceI {
		w.ue(0) // cabac_init_idc
	}
	w.se(int32(c.qp - 26)) // slice_qp_delta
	if e.noDeblock {
		w.ue(1) // disable_deblocking_filter_idc
	} else {
		w.ue(0) // disable_deblocking_filter_idc
		w.se(0) // slice_alpha_c0_offset_div2
		w.se(0) // slice_beta_offset_div2
	}
	w.alignOnes()
}

// sortByPOC orders pictures by POC, descending or ascending.
func sortByPOC(ps []*picture, desc bool) {
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0 && (ps[j].poc > ps[j-1].poc) == desc; j-- {
			ps[j], ps[j-1] = ps[j-1], ps[j]
		}
	}
}

// Stats returns how the macroblocks were coded so far.
func (e *BaseEncoder) Stats() DepStats {
	e.statsMu.Lock()
	st := e.stats
	e.statsMu.Unlock()
	return DepStats{Skip: st.skip, Direct: st.direct, InterView: st.interView, Temporal: st.temporal, Bi: st.bi, Partitioned: st.parts, Intra: st.intra}
}
