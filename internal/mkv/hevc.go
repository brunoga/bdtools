package mkv

import "errors"

// HEVC parameter sets, slice POC and hvcC (ISO/IEC 14496-15 8.3.3).

type hevcSPS struct {
	ptl             []byte // the 12-byte general profile_tier_level
	chromaFormat    uint32
	bitDepthLuma    uint32
	bitDepthChroma  uint32
	log2MaxPOCLsb   int
	separateColour  bool
	subLayers       uint32
	temporalNesting bool
}

type hevcPPS struct {
	sps             uint32
	dependentSlices bool
	outputFlag      bool
	extraSliceBits  int
}

func (p *paramSets) addHEVC(kind int, nal []byte) {
	if kind != 0 {
		return
	}
	if len(nal) < 3 {
		return
	}
	id := uint32(nal[2] >> 4) // vps_video_parameter_set_id
	if p.hvps == nil {
		p.hvps = map[uint32][]byte{}
	}
	p.hvps[id] = append([]byte(nil), nal...)
	remember(&p.hvpsIDs, id)
}

// skipPTL skips profile_tier_level after its 12 general bytes.
func skipSubLayers(r *bitReader, maxSub uint32) {
	present := make([][2]bool, maxSub)
	for i := range present {
		present[i] = [2]bool{r.flag(), r.flag()}
	}
	if maxSub > 0 {
		for i := maxSub; i < 8; i++ {
			r.u(2)
		}
	}
	for _, p := range present {
		if p[0] {
			r.u(32)
			r.u(32)
			r.u(24) // 88 bits of sub-layer profile
		}
		if p[1] {
			r.u(8)
		}
	}
}

func (p *paramSets) addHEVCSPS(nal []byte) {
	rb := rbsp(nal[2:])
	r := &bitReader{b: rb}
	r.u(4) // sps_video_parameter_set_id
	s := &hevcSPS{}
	s.subLayers = r.u(3)
	s.temporalNesting = r.flag()
	if len(rb) < 13 {
		return
	}
	s.ptl = append([]byte(nil), rb[1:13]...)
	r.pos += 96
	skipSubLayers(r, s.subLayers)
	id := r.ue()
	s.chromaFormat = r.ue()
	if s.chromaFormat == 3 {
		s.separateColour = r.flag()
	}
	w := r.ue()
	h := r.ue()
	if r.flag() { // conformance window
		cl, cr, ct, cb := r.ue(), r.ue(), r.ue(), r.ue()
		sx, sy := uint32(1), uint32(1)
		if s.chromaFormat == 1 || s.chromaFormat == 2 {
			sx = 2
		}
		if s.chromaFormat == 1 {
			sy = 2
		}
		w -= sx * (cl + cr)
		h -= sy * (ct + cb)
	}
	s.bitDepthLuma = r.ue()
	s.bitDepthChroma = r.ue()
	s.log2MaxPOCLsb = int(r.ue()) + 4
	if r.bad {
		return
	}
	if p.hsps == nil {
		p.hsps, p.hrawSPS = map[uint32]*hevcSPS{}, map[uint32][]byte{}
	}
	p.hsps[id] = s
	p.hrawSPS[id] = append([]byte(nil), nal...)
	remember(&p.hspsIDs, id)
	p.width, p.height = int(w), int(h)
}

func (p *paramSets) addHEVCPPS(nal []byte) {
	r := &bitReader{b: rbsp(nal[2:])}
	id := r.ue()
	pp := &hevcPPS{sps: r.ue()}
	pp.dependentSlices = r.flag()
	pp.outputFlag = r.flag()
	pp.extraSliceBits = int(r.u(3))
	if r.bad {
		return
	}
	if p.hpps == nil {
		p.hpps, p.hrawPPS = map[uint32]*hevcPPS{}, map[uint32][]byte{}
	}
	p.hpps[id] = pp
	p.hrawPPS[id] = append([]byte(nil), nal...)
	remember(&p.hppsIDs, id)
}

// hevcPOC is the HEVC picture order count derivation (8.3.1).
type hevcPOC struct {
	prevTid0POC int64
	first       bool
}

// poc returns a picture's POC, whether it is a random access point, and
// whether it starts a new coded video sequence (POC reset).
func (st *hevcPOC) poc(nal []byte, p *paramSets) (poc int64, irap, reset bool, err error) {
	t := nal[0] >> 1 & 0x3f
	tid := nal[1]&7 - 1
	irap = t >= 16 && t <= 23
	r := &bitReader{b: rbsp(nal[2:])}
	r.u(1) // first_slice_segment_in_pic_flag (checked by the caller)
	if irap {
		r.u(1) // no_output_of_prior_pics_flag
	}
	pp := p.hpps[r.ue()]
	if pp == nil {
		return 0, irap, false, errors.New("mkv: slice refers to an unknown PPS")
	}
	s := p.hsps[pp.sps]
	if s == nil {
		return 0, irap, false, errors.New("mkv: PPS refers to an unknown SPS")
	}
	r.u(pp.extraSliceBits)
	r.ue() // slice_type
	if pp.outputFlag {
		r.u(1)
	}
	if s.separateColour {
		r.u(2)
	}
	idr := t == 19 || t == 20
	// IDR, BLA, and the first picture (a CRA there) start afresh.
	noRASLOutput := idr || (t >= 16 && t <= 18) || (irap && !st.first)
	if idr {
		poc = 0
	} else {
		lsb := int64(r.u(s.log2MaxPOCLsb))
		max := int64(1) << s.log2MaxPOCLsb
		var msb int64
		if irap && noRASLOutput {
			msb = 0
		} else {
			prevLsb := st.prevTid0POC & (max - 1)
			prevMsb := st.prevTid0POC - prevLsb
			switch {
			case lsb < prevLsb && prevLsb-lsb >= max/2:
				msb = prevMsb + max
			case lsb > prevLsb && lsb-prevLsb > max/2:
				msb = prevMsb - max
			default:
				msb = prevMsb
			}
		}
		poc = msb + lsb
	}
	st.first = true
	// RASL, RADL and sub-layer non-reference pictures do not update the
	// reference for the next POC.
	rasl, radl := t == 8 || t == 9, t == 6 || t == 7
	subLayerNonRef := t <= 14 && t%2 == 0
	if tid == 0 && !rasl && !radl && !subLayerNonRef {
		st.prevTid0POC = poc
	}
	if r.bad {
		return 0, irap, false, errors.New("mkv: truncated slice header")
	}
	return poc, irap, irap && noRASLOutput, nil
}

// hvcC is the HEVC codec private data.
func (p *paramSets) hvcC() ([]byte, error) {
	if len(p.hspsIDs) == 0 {
		return nil, errors.New("mkv: no SPS")
	}
	s := p.hsps[p.hspsIDs[0]]
	b := []byte{1}
	b = append(b, s.ptl...)   // profile space/tier/idc, compatibility, constraints, level
	b = append(b, 0xf0, 0x00, // min_spatial_segmentation_idc
		0xfc,                          // parallelismType
		0xfc|byte(s.chromaFormat&3),   // chroma_format_idc
		0xf8|byte(s.bitDepthLuma&7),   // bit_depth_luma_minus8
		0xf8|byte(s.bitDepthChroma&7), // bit_depth_chroma_minus8
		0, 0)                          // avgFrameRate
	nest := byte(0)
	if s.temporalNesting {
		nest = 1
	}
	b = append(b, byte(s.subLayers+1)<<3|nest<<2|3) // 4-byte NAL lengths
	type arr struct {
		t   byte
		ids []uint32
		raw map[uint32][]byte
	}
	arrays := []arr{{32, p.hvpsIDs, p.hvps}, {33, p.hspsIDs, p.hrawSPS}, {34, p.hppsIDs, p.hrawPPS}}
	b = append(b, byte(len(arrays)))
	for _, a := range arrays {
		b = append(b, 0x80|a.t, byte(len(a.ids)>>8), byte(len(a.ids)))
		for _, id := range a.ids {
			raw := a.raw[id]
			b = append(b, byte(len(raw)>>8), byte(len(raw)))
			b = append(b, raw...)
		}
	}
	return b, nil
}
