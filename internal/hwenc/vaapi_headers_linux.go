//go:build linux && (amd64 || arm64)

package hwenc

import "slices"

// Parameter sets for drivers that write slice headers but not the
// sequence's (Intel's do neither for the parameter sets): written here from
// the same values the parameter buffers carry, and sent as a packed
// sequence header on every IDR.

type bitWriter struct {
	b    []byte
	cur  uint64
	nbit int
}

func (w *bitWriter) u(n int, v uint32) {
	for i := n - 1; i >= 0; i-- {
		w.cur = w.cur<<1 | uint64(v>>i&1)
		w.nbit++
		if w.nbit == 8 {
			w.b = append(w.b, byte(w.cur))
			w.cur, w.nbit = 0, 0
		}
	}
}

func (w *bitWriter) flag(b bool) { w.u(1, b2u(b)) }

func (w *bitWriter) ue(v uint32) {
	v++
	n := 0
	for t := v; t > 1; t >>= 1 {
		n++
	}
	w.u(n, 0)
	w.u(n+1, v)
}

func (w *bitWriter) se(v int) {
	if v > 0 {
		w.ue(uint32(2*v - 1)) //nolint:gosec // positive
	} else {
		w.ue(uint32(-2 * v)) //nolint:gosec // non-negative
	}
}

// trailing ends the RBSP: a one bit, then zeros to the byte boundary.
func (w *bitWriter) trailing() []byte {
	w.u(1, 1)
	for w.nbit != 0 {
		w.u(1, 0)
	}
	return w.b
}

// nal wraps an RBSP in a start code and NAL header, with emulation
// prevention bytes.
func nal(header []byte, rbsp []byte) []byte {
	out := append([]byte{0, 0, 0, 1}, header...)
	zeros := 0
	for _, c := range rbsp {
		if zeros >= 2 && c <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, c)
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

func (e *vaapi) h264Headers() []byte {
	w, h := e.cfg.Width, e.cfg.Height
	mbw, mbh := (w+15)/16, (h+15)/16
	s := &bitWriter{}
	s.u(8, 100) // High
	s.u(8, 0)
	s.u(8, uint32(h264Level(mbw, mbh, e.cfg))) //nolint:gosec // a level
	s.ue(0)                                    // seq_parameter_set_id
	s.ue(1)                                    // 4:2:0
	s.ue(0)                                    // bit depths
	s.ue(0)
	s.flag(false)                         // qpprime_y_zero_transform_bypass
	s.flag(false)                         // seq_scaling_matrix_present
	s.ue(uint32(e.log2MaxFrameNum() - 4)) //nolint:gosec // 0..12
	s.ue(0)                               // pic_order_cnt_type
	s.ue(uint32(e.log2MaxPOCLsb() - 4))   //nolint:gosec // 0..12
	s.ue(uint32(e.maxRefs))               //nolint:gosec // small
	s.flag(false)                         // gaps_in_frame_num_value_allowed
	s.ue(uint32(mbw - 1))                 //nolint:gosec // frame size
	s.ue(uint32(mbh - 1))                 //nolint:gosec // frame size
	s.flag(true)                          // frame_mbs_only
	s.flag(true)                          // direct_8x8_inference
	right, bottom := mbw*16-w, mbh*16-h
	s.flag(right != 0 || bottom != 0)
	if right != 0 || bottom != 0 {
		s.ue(0)
		s.ue(uint32(right / 2)) //nolint:gosec // < 8
		s.ue(0)
		s.ue(uint32(bottom / 2)) //nolint:gosec // < 8
	}
	s.flag(true) // vui_parameters_present
	s.flag(true) // aspect_ratio_info_present
	s.u(8, 1)    // square pixels
	s.flag(false)
	s.flag(false)
	s.flag(false)
	s.flag(true)                    // timing_info_present
	s.u(32, uint32(e.cfg.FPSDen))   //nolint:gosec // frame rate
	s.u(32, uint32(2*e.cfg.FPSNum)) //nolint:gosec // frame rate
	s.flag(true)                    // fixed_frame_rate
	s.flag(false)                   // nal_hrd_parameters_present
	s.flag(false)                   // vcl_hrd_parameters_present
	s.flag(false)                   // pic_struct_present
	s.flag(true)                    // bitstream_restriction
	s.flag(true)                    // motion_vectors_over_pic_boundaries
	s.ue(0)
	s.ue(0)
	s.ue(15)
	s.ue(15)
	s.ue(b2u(e.bFrames > 0)) // max_num_reorder_frames
	s.ue(uint32(e.maxRefs))  //nolint:gosec // max_dec_frame_buffering
	out := nal([]byte{0x67}, s.trailing())

	p := &bitWriter{}
	p.ue(0)       // pic_parameter_set_id
	p.ue(0)       // seq_parameter_set_id
	p.flag(true)  // CABAC
	p.flag(false) // bottom_field_pic_order_in_frame_present
	p.ue(0)       // num_slice_groups_minus1
	p.ue(0)       // num_ref_idx_l0_default_active_minus1
	p.ue(0)
	p.flag(false) // weighted_pred
	p.u(2, 0)     // weighted_bipred_idc
	p.se(e.qpP - 26)
	p.se(0)
	p.se(0)       // chroma_qp_index_offset
	p.flag(true)  // deblocking_filter_control_present
	p.flag(false) // constrained_intra_pred
	p.flag(false) // redundant_pic_cnt_present
	p.flag(true)  // transform_8x8_mode
	p.flag(false) // pic_scaling_matrix_present
	p.se(0)       // second_chroma_qp_index_offset
	return append(out, nal([]byte{0x68}, p.trailing())...)
}

// hevcPTL writes profile_tier_level for Main with no sub-layers.
func (e *vaapi) hevcPTL(w *bitWriter) {
	w.u(2, 0) // profile space
	w.flag(false)
	w.u(5, 1)           // Main
	w.u(32, 0x60000000) // compatible with Main and Main 10
	w.flag(true)        // progressive_source
	w.flag(false)       // interlaced_source
	w.flag(false)       // non_packed_constraint
	w.flag(true)        // frame_only_constraint
	w.u(32, 0)          // 43 reserved bits and general_inbld_flag
	w.u(12, 0)
	w.u(8, uint32(hevcLevel(e.cfg))) //nolint:gosec // a level
}

func (e *vaapi) hevcHeaders() []byte {
	c := e.hevc
	reorder := b2u(e.bFrames > 0)
	v := &bitWriter{}
	v.u(4, 0)    // vps_video_parameter_set_id
	v.flag(true) // vps_base_layer_internal
	v.flag(true) // vps_base_layer_available
	v.u(6, 0)    // vps_max_layers_minus1
	v.u(3, 0)    // vps_max_sub_layers_minus1
	v.flag(true) // vps_temporal_id_nesting
	v.u(16, 0xffff)
	e.hevcPTL(v)
	v.flag(false)           // vps_sub_layer_ordering_info_present
	v.ue(uint32(e.maxRefs)) //nolint:gosec // max_dec_pic_buffering_minus1
	v.ue(reorder)           // max_num_reorder_pics
	v.ue(0)                 // max_latency_increase_plus1
	v.u(6, 0)               // vps_max_layer_id
	v.ue(0)                 // vps_num_layer_sets_minus1
	v.flag(false)           // vps_timing_info_present
	v.flag(false)           // vps_extension
	out := nal([]byte{0x40, 0x01}, v.trailing())

	s := &bitWriter{}
	s.u(4, 0)    // sps_video_parameter_set_id
	s.u(3, 0)    // sps_max_sub_layers_minus1
	s.flag(true) // sps_temporal_id_nesting
	e.hevcPTL(s)
	s.ue(0)                    // sps_seq_parameter_set_id
	s.ue(1)                    // 4:2:0
	s.ue(uint32(e.cfg.Width))  //nolint:gosec // frame size
	s.ue(uint32(e.cfg.Height)) //nolint:gosec // frame size
	s.flag(false)              // conformance_window
	s.ue(0)                    // bit depths
	s.ue(0)
	s.ue(uint32(e.log2MaxPOCLsb() - 4)) //nolint:gosec // 0..12
	s.flag(false)                       // sps_sub_layer_ordering_info_present
	s.ue(uint32(e.maxRefs))             //nolint:gosec // max_dec_pic_buffering_minus1
	s.ue(reorder)
	s.ue(0)
	s.ue(uint32(c.log2MinCB - 3))            //nolint:gosec // 0..3
	s.ue(uint32(c.log2MaxCTB - c.log2MinCB)) //nolint:gosec // 0..3
	s.ue(uint32(c.log2MinTB - 2))            //nolint:gosec // 0..3
	s.ue(uint32(c.log2MaxTB - c.log2MinTB))  //nolint:gosec // 0..3
	s.ue(uint32(c.thdInter))                 //nolint:gosec // 0..3
	s.ue(uint32(c.thdIntra))                 //nolint:gosec // 0..3
	s.flag(false)                            // scaling_list_enabled
	s.flag(c.amp)
	s.flag(c.sao)
	s.flag(false) // pcm_enabled
	s.ue(0)       // num_short_term_ref_pic_sets: each slice carries its own
	s.flag(false) // long_term_ref_pics_present
	s.flag(c.tmvp)
	s.flag(c.strongIntra)
	s.flag(true) // vui_parameters_present
	s.flag(true) // aspect_ratio_info_present
	s.u(8, 1)    // square pixels
	s.flag(false)
	s.flag(false)
	s.flag(false)
	s.flag(false)                 // neutral_chroma_indication
	s.flag(false)                 // field_seq
	s.flag(false)                 // frame_field_info_present
	s.flag(false)                 // default_display_window
	s.flag(true)                  // vui_timing_info_present
	s.u(32, uint32(e.cfg.FPSDen)) //nolint:gosec // frame rate
	s.u(32, uint32(e.cfg.FPSNum)) //nolint:gosec // frame rate
	s.flag(false)                 // vui_poc_proportional_to_timing
	s.flag(false)                 // vui_hrd_parameters_present
	s.flag(false)                 // bitstream_restriction
	s.flag(false)                 // sps_extension_present
	out = append(out, nal([]byte{0x42, 0x01}, s.trailing())...)

	p := &bitWriter{}
	p.ue(0)       // pps_pic_parameter_set_id
	p.ue(0)       // pps_seq_parameter_set_id
	p.flag(false) // dependent_slice_segments_enabled
	p.flag(false) // output_flag_present
	p.u(3, 0)     // num_extra_slice_header_bits
	p.flag(c.signHiding)
	p.flag(false) // cabac_init_present
	p.ue(0)       // num_ref_idx_l0_default_active_minus1
	p.ue(0)
	p.se(e.qpP - 26)
	p.flag(false) // constrained_intra_pred
	p.flag(c.transformSkip)
	p.flag(c.cuQPDelta)
	if c.cuQPDelta {
		p.ue(0) // diff_cu_qp_delta_depth
	}
	p.se(0) // pps_cb_qp_offset
	p.se(0)
	p.flag(false) // pps_slice_chroma_qp_offsets_present
	p.flag(false) // weighted_pred
	p.flag(false) // weighted_bipred
	p.flag(false) // transquant_bypass_enabled
	p.flag(false) // tiles_enabled
	p.flag(false) // entropy_coding_sync_enabled
	p.flag(false) // pps_loop_filter_across_slices_enabled
	p.flag(false) // deblocking_filter_control_present
	p.flag(false) // pps_scaling_list_data_present
	p.flag(false) // lists_modification_present
	p.ue(0)       // log2_parallel_merge_level_minus2
	p.flag(false) // slice_segment_header_extension_present
	p.flag(false) // pps_extension_present
	return append(out, nal([]byte{0x44, 0x01}, p.trailing())...)
}

// packedSequence adds the parameter sets as a packed sequence header.
func (e *vaapi) packedSequence(add addBuf) error {
	var data []byte
	if e.cfg.Codec == HEVC {
		data = e.hevcHeaders()
	} else {
		data = e.h264Headers()
	}
	ph := newStruct(vaSizePackedHeaderParam)
	ph.u32(vaPackedType, vaEncPackedHeaderSequence)
	ph.u32(vaPackedBitLength, uint32(8*len(data))) //nolint:gosec // small
	ph.u8(vaPackedHasEmulation, 1)
	if err := add(vaEncPackedHeaderParameterBufferType, ph); err != nil {
		return err
	}
	buf := newStruct(len(data))
	copy(buf, data)
	return add(vaEncPackedHeaderDataBufferType, buf)
}

// hevcSliceHeader writes the slice segment header, byte alignment included:
// the driver appends the slice data. Each slice
// carries its reference picture set: the pictures before and after this
// one that it and later pictures still use.
func (e *vaapi) hevcSliceHeader(p vaPic, sliceType, qpDelta int, refs []vaPic) []byte {
	c := e.hevc
	nalType := byte(1)
	switch {
	case p.idr:
		nalType = 19
	case p.typ == picB:
		nalType = 0
	}
	w := &bitWriter{}
	w.flag(true) // first_slice_segment_in_pic
	if p.idr {
		w.flag(false) // no_output_of_prior_pics
	}
	w.ue(0)                 // slice_pic_parameter_set_id
	w.ue(uint32(sliceType)) //nolint:gosec // 0..2
	if !p.idr {
		w.u(e.log2MaxPOCLsb(), uint32(p.disp)) //nolint:gosec // within the POC range
		w.flag(false)                          // short_term_ref_pic_set_sps
		var before, after []int
		for _, r := range refs {
			if r.disp < p.disp {
				before = append(before, r.disp)
			} else {
				after = append(after, r.disp)
			}
		}
		// Closest first.
		slices.Sort(before)
		slices.Reverse(before)
		slices.Sort(after)
		w.ue(uint32(len(before))) //nolint:gosec // small
		w.ue(uint32(len(after)))  //nolint:gosec // small
		prev := p.disp
		for _, d := range before {
			w.ue(uint32(prev - d - 1)) //nolint:gosec // positive
			w.flag(true)               // used_by_curr_pic
			prev = d
		}
		prev = p.disp
		for _, d := range after {
			w.ue(uint32(d - prev - 1)) //nolint:gosec // positive
			w.flag(true)
			prev = d
		}
		if c.tmvp {
			w.flag(true) // slice_temporal_mvp_enabled
		}
	}
	if c.sao {
		w.flag(true) // slice_sao_luma
		w.flag(true) // slice_sao_chroma
	}
	if sliceType != 2 {
		w.flag(false) // num_ref_idx_active_override
		if sliceType == 0 {
			w.flag(false) // mvd_l1_zero
		}
		if !p.idr && c.tmvp && sliceType == 0 {
			w.flag(true) // collocated_from_l0
		}
		w.ue(0) // five_minus_max_num_merge_cand
	}
	w.se(qpDelta)
	return nal([]byte{nalType << 1, 1}, w.trailing()) // byte_alignment()
}

func (e *vaapi) packedSlice(add addBuf, data []byte) error {
	ph := newStruct(vaSizePackedHeaderParam)
	ph.u32(vaPackedType, vaEncPackedHeaderSlice)
	ph.u32(vaPackedBitLength, uint32(8*len(data))) //nolint:gosec // small
	ph.u8(vaPackedHasEmulation, 1)
	if err := add(vaEncPackedHeaderParameterBufferType, ph); err != nil {
		return err
	}
	buf := newStruct(len(data))
	copy(buf, data)
	return add(vaEncPackedHeaderDataBufferType, buf)
}
