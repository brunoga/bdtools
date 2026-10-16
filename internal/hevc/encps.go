package hevc

import "fmt"

// The encoder's parameter sets and slice headers.

// encParams is the stream's configuration: what the parameter sets say.
type encParams struct {
	width, height int
	bitDepth      int
	fpsNum        int
	fpsDen        int
	// Colour description (H.273), 2 for unspecified.
	primaries, transfer, matrix int
	fullRange                   bool

	log2Ctb, log2MinCb          int
	log2MinTb, log2MaxTb        int
	maxTrDepthIntra             int
	maxTrDepthInter             int
	maxDecPicBuf, maxReorder    int
	log2MaxPOCLsb               int
	sao, amp, tmvp, strongIntra bool
	signHiding                  bool
	wpp                         bool
	initQP                      int
}

// hevcLevelIdc picks the lowest level (A.4) the picture size, the rate and
// the decoded picture buffer fit.
func hevcLevelIdc(width, height, fpsNum, fpsDen, dpb int) int {
	ps := width * height
	sr := 0.0
	if fpsDen > 0 {
		sr = float64(ps) * float64(fpsNum) / float64(fpsDen)
	}
	levels := []struct {
		idc   int
		maxPs int
		maxSr float64
	}{
		{30, 36864, 552960}, {60, 122880, 3686400}, {63, 245760, 7372800},
		{90, 552960, 16588800}, {93, 983040, 33177600}, {120, 2228224, 66846720},
		{123, 2228224, 133693440}, {150, 8912896, 267386880}, {153, 8912896, 534773760},
		{156, 8912896, 1069547520}, {180, 35651584, 1069547520}, {183, 35651584, 2139095040},
		{186, 35651584, 4278190080},
	}
	for _, l := range levels {
		// MaxDpbSize (A-2): more pictures the smaller they are.
		maxDpb := 6
		switch {
		case ps <= l.maxPs>>2:
			maxDpb = 16
		case ps <= l.maxPs>>1:
			maxDpb = 12
		case ps <= 3*l.maxPs>>2:
			maxDpb = 8
		}
		if ps <= l.maxPs && sr <= l.maxSr && dpb <= maxDpb {
			return l.idc
		}
	}
	return 186
}

// profileTierLevel writes profile_tier_level for the general layer.
func (c *encParams) profileTierLevel(w *bitWriter) {
	w.u(2, 0)     // general_profile_space
	w.flag(false) // general_tier_flag
	if c.bitDepth == 10 {
		w.u(5, 2)           // Main 10
		w.u(32, 0x20000000) // compatible with Main 10
	} else {
		w.u(5, 1)           // Main
		w.u(32, 0x60000000) // compatible with Main and Main 10
	}
	w.flag(true)  // progressive_source
	w.flag(false) // interlaced_source
	w.flag(false) // non_packed_constraint
	w.flag(true)  // frame_only_constraint
	w.u(32, 0)    // 43 reserved bits and general_inbld_flag
	w.u(12, 0)
	w.u(8, uint32(hevcLevelIdc(c.width, c.height, c.fpsNum, c.fpsDen, c.maxDecPicBuf))) //nolint:gosec // a level
}

// headers writes the VPS, SPS and PPS as Annex B NAL units.
func (c *encParams) headers() []byte {
	v := &bitWriter{}
	v.u(4, 0)    // vps_video_parameter_set_id
	v.flag(true) // vps_base_layer_internal
	v.flag(true) // vps_base_layer_available
	v.u(6, 0)    // vps_max_layers_minus1
	v.u(3, 0)    // vps_max_sub_layers_minus1
	v.flag(true) // vps_temporal_id_nesting
	v.u(16, 0xffff)
	c.profileTierLevel(v)
	v.flag(false)                    // vps_sub_layer_ordering_info_present
	v.ue(uint32(c.maxDecPicBuf - 1)) //nolint:gosec // small
	v.ue(uint32(c.maxReorder))       //nolint:gosec // small
	v.ue(0)                          // max_latency_increase_plus1
	v.u(6, 0)                        // vps_max_layer_id
	v.ue(0)                          // vps_num_layer_sets_minus1
	v.flag(false)                    // vps_timing_info_present
	v.flag(false)                    // vps_extension
	out := writeNAL(nil, nalVPS, v.trailing())

	s := &bitWriter{}
	s.u(4, 0)    // sps_video_parameter_set_id
	s.u(3, 0)    // sps_max_sub_layers_minus1
	s.flag(true) // sps_temporal_id_nesting
	c.profileTierLevel(s)
	s.ue(0)                                 // sps_seq_parameter_set_id
	s.ue(1)                                 // 4:2:0
	s.ue(uint32(c.width))                   //nolint:gosec // picture size
	s.ue(uint32(c.height))                  //nolint:gosec // picture size
	s.flag(false)                           // conformance_window
	s.ue(uint32(c.bitDepth - 8))            //nolint:gosec // 0 or 2
	s.ue(uint32(c.bitDepth - 8))            //nolint:gosec // 0 or 2
	s.ue(uint32(c.log2MaxPOCLsb - 4))       //nolint:gosec // 0..12
	s.flag(false)                           // sps_sub_layer_ordering_info_present
	s.ue(uint32(c.maxDecPicBuf - 1))        //nolint:gosec // small
	s.ue(uint32(c.maxReorder))              //nolint:gosec // small
	s.ue(0)                                 // max_latency_increase_plus1
	s.ue(uint32(c.log2MinCb - 3))           //nolint:gosec // 0..3
	s.ue(uint32(c.log2Ctb - c.log2MinCb))   //nolint:gosec // 0..3
	s.ue(uint32(c.log2MinTb - 2))           //nolint:gosec // 0..3
	s.ue(uint32(c.log2MaxTb - c.log2MinTb)) //nolint:gosec // 0..3
	s.ue(uint32(c.maxTrDepthInter))         //nolint:gosec // 0..3
	s.ue(uint32(c.maxTrDepthIntra))         //nolint:gosec // 0..3
	s.flag(false)                           // scaling_list_enabled
	s.flag(c.amp)
	s.flag(c.sao)
	s.flag(false) // pcm_enabled
	s.ue(0)       // num_short_term_ref_pic_sets: each slice carries its own
	s.flag(false) // long_term_ref_pics_present
	s.flag(c.tmvp)
	s.flag(c.strongIntra)
	s.flag(true)  // vui_parameters_present
	s.flag(true)  // aspect_ratio_info_present
	s.u(8, 1)     // square samples
	s.flag(false) // overscan_info_present
	colour := c.primaries != 2 || c.transfer != 2 || c.matrix != 2
	s.flag(colour || c.fullRange) // video_signal_type_present
	if colour || c.fullRange {
		s.u(3, 5) // video_format unspecified
		s.flag(c.fullRange)
		s.flag(colour)
		if colour {
			s.u(8, uint32(c.primaries)) //nolint:gosec // a code point
			s.u(8, uint32(c.transfer))  //nolint:gosec // a code point
			s.u(8, uint32(c.matrix))    //nolint:gosec // a code point
		}
	}
	s.flag(false) // chroma_loc_info_present
	s.flag(false) // neutral_chroma_indication
	s.flag(false) // field_seq
	s.flag(false) // frame_field_info_present
	s.flag(false) // default_display_window
	timing := c.fpsNum > 0 && c.fpsDen > 0
	s.flag(timing) // vui_timing_info_present
	if timing {
		s.u(32, uint32(c.fpsDen)) //nolint:gosec // a frame rate
		s.u(32, uint32(c.fpsNum)) //nolint:gosec // a frame rate
		s.flag(false)             // vui_poc_proportional_to_timing
		s.flag(false)             // vui_hrd_parameters_present
	}
	s.flag(false) // bitstream_restriction
	s.flag(false) // sps_extension_present
	out = writeNAL(out, nalSPS, s.trailing())

	p := &bitWriter{}
	p.ue(0)       // pps_pic_parameter_set_id
	p.ue(0)       // pps_seq_parameter_set_id
	p.flag(false) // dependent_slice_segments_enabled
	p.flag(false) // output_flag_present
	p.u(3, 0)     // num_extra_slice_header_bits
	p.flag(c.signHiding)
	p.flag(false)              // cabac_init_present
	p.ue(0)                    // num_ref_idx_l0_default_active_minus1
	p.ue(0)                    // num_ref_idx_l1_default_active_minus1
	p.se(int32(c.initQP - 26)) //nolint:gosec // a QP
	p.flag(false)              // constrained_intra_pred
	p.flag(false)              // transform_skip
	p.flag(false)              // cu_qp_delta_enabled
	p.se(0)                    // pps_cb_qp_offset
	p.se(0)                    // pps_cr_qp_offset
	p.flag(false)              // pps_slice_chroma_qp_offsets_present
	p.flag(false)              // weighted_pred
	p.flag(false)              // weighted_bipred
	p.flag(false)              // transquant_bypass_enabled
	p.flag(false)              // tiles_enabled
	p.flag(c.wpp)              // entropy_coding_sync_enabled
	p.flag(false)              // pps_loop_filter_across_slices_enabled
	p.flag(false)              // deblocking_filter_control_present
	p.flag(false)              // pps_scaling_list_data_present
	p.flag(false)              // lists_modification_present
	p.ue(0)                    // log2_parallel_merge_level_minus2
	p.flag(false)              // slice_segment_header_extension_present
	p.flag(false)              // pps_extension_present
	return writeNAL(out, nalPPS, p.trailing())
}

// parameterSets parses the headers back with the decoder's parsers: the
// encoder's model of the stream is then exactly the decoder's.
func (c *encParams) parameterSets() (*sps, *pps, error) {
	d := &Decoder{}
	for _, n := range nalUnits(c.headers()) {
		r := &bits{b: n.rbsp}
		var err error
		switch n.typ {
		case nalSPS:
			err = d.parseSPS(r)
		case nalPPS:
			err = d.parsePPS(r)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("hevc: the encoder's parameter sets do not parse: %w", err)
		}
	}
	s, p := d.spss[0], d.ppss[0]
	if s == nil || p == nil {
		return nil, nil, fmt.Errorf("hevc: the encoder's parameter sets do not parse")
	}
	if err := p.activate(s, nil); err != nil {
		return nil, nil, err
	}
	return s, p, nil
}

// sliceSegment assembles a slice segment NAL unit of type typ: the header
// written by hdr, given each substream's escaped size but the last's, then
// the substreams.
func sliceSegment(dst []byte, typ int, hdr func(w *bitWriter, offsets []int), subs [][]byte) []byte {
	var data []byte
	sizes := make([]int, 0, len(subs))
	zeros := 0
	for _, sub := range subs {
		before := len(data)
		data, zeros = escapeNAL(data, sub, zeros)
		sizes = append(sizes, len(data)-before)
	}
	w := &bitWriter{}
	hdr(w, sizes[:len(sizes)-1])
	w.u(1, 1) // byte_alignment: alignment_bit_equal_to_one
	w.alignZeros()
	dst = append(dst, 0, 0, 0, 1, byte(typ<<1), 1)
	dst, _ = escapeNAL(dst, w.bytes(), 0)
	// The header ends in a byte with its alignment one bit: no run of
	// zeros carries into the data, so its escaping stands as computed.
	dst = append(dst, data...)
	if len(data) > 0 && data[len(data)-1] == 0 {
		dst = append(dst, 3)
	}
	return dst
}

// writeEntryPoints writes a slice header's entry points for the
// substreams' sizes.
func writeEntryPoints(w *bitWriter, sizes []int) {
	w.ue(uint32(len(sizes))) //nolint:gosec // a count
	if len(sizes) == 0 {
		return
	}
	maxSize := 1
	for _, s := range sizes {
		maxSize = max(maxSize, s)
	}
	n := 1
	for 1<<n < maxSize { // offset_len_minus1 + 1 bits hold size - 1
		n++
	}
	w.ue(uint32(n - 1)) //nolint:gosec // a bit count
	for _, s := range sizes {
		w.u(uint(n), uint32(s-1)) //nolint:gosec // a size
	}
}
