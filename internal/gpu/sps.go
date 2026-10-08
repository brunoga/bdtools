package gpu

import "errors"

// What a decoder that does not parse the stream itself needs from a
// sequence parameter set: the picture's displayed size (the coded size less
// the cropping) and its bit depth.

// spsInfo is a sequence parameter set's picture.
type spsInfo struct {
	// color is the VUI's colour description (2, unspecified, when there
	// is none).
	color ColorInfo
	// reorder is how many pictures can come before one in decoding order
	// that is shown after them (0 when the SPS does not say).
	reorder int
	// rateNum and rateDen are the VUI's frame rate, 0 when it has none.
	rateNum, rateDen           int
	codedW, codedH             int
	cropL, cropR, cropT, cropB int // in luma samples
	depth                      int
}

func (s spsInfo) width() int  { return s.codedW - s.cropL - s.cropR }
func (s spsInfo) height() int { return s.codedH - s.cropT - s.cropB }

var errSPS = errors.New("gpu: a sequence parameter set ends early")

// bits reads an RBSP's bits, emulation prevention removed.
type bits struct {
	b   []byte
	pos int
	err error
}

func newBits(nal []byte) *bits {
	out := make([]byte, 0, len(nal))
	zeros := 0
	for _, c := range nal {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return &bits{b: out}
}

func (r *bits) u(n int) int {
	v := 0
	for range n {
		if r.pos >= len(r.b)*8 {
			r.err = errSPS
			return 0
		}
		v = v<<1 | int(r.b[r.pos>>3]>>(7-r.pos&7)&1)
		r.pos++
	}
	return v
}

func (r *bits) ue() int {
	zeros := 0
	for r.u(1) == 0 {
		if r.err != nil || zeros > 31 {
			r.err = errSPS
			return 0
		}
		zeros++
	}
	return 1<<zeros - 1 + r.u(zeros)
}

func (r *bits) se() int {
	v := r.ue()
	if v&1 != 0 {
		return (v + 1) / 2
	}
	return -v / 2
}

// hevcSPS reads an HEVC SPS NAL unit (with its 2-byte header).
func hevcSPS(nal []byte) (spsInfo, error) {
	if len(nal) < 3 {
		return spsInfo{}, errSPS
	}
	r := newBits(nal[2:])
	r.u(4) // sps_video_parameter_set_id
	maxSub := r.u(3)
	r.u(1) // sps_temporal_id_nesting_flag
	// profile_tier_level(1, maxSub)
	r.u(2 + 1 + 5 + 32 + 4 + 43 + 1 + 8)
	subProfile, subLevel := make([]int, maxSub), make([]int, maxSub)
	for i := range maxSub {
		subProfile[i], subLevel[i] = r.u(1), r.u(1)
	}
	if maxSub > 0 {
		for i := maxSub; i < 8; i++ {
			r.u(2)
		}
	}
	for i := range maxSub {
		if subProfile[i] != 0 {
			r.u(2 + 1 + 5 + 32 + 4 + 43 + 1)
		}
		if subLevel[i] != 0 {
			r.u(8)
		}
	}
	r.ue() // sps_seq_parameter_set_id
	chroma := r.ue()
	if chroma == 3 {
		r.u(1) // separate_colour_plane_flag
	}
	var s spsInfo
	s.codedW, s.codedH = r.ue(), r.ue()
	if r.u(1) != 0 { // conformance_window_flag
		subW, subH := 1, 1
		if chroma == 1 || chroma == 2 {
			subW = 2
		}
		if chroma == 1 {
			subH = 2
		}
		s.cropL, s.cropR, s.cropT, s.cropB = r.ue()*subW, r.ue()*subW, r.ue()*subH, r.ue()*subH
	}
	s.depth = r.ue() + 8 // bit_depth_luma_minus8
	s.color = ColorInfo{Primaries: 2, Transfer: 2, Matrix: 2}
	r.ue() // bit_depth_chroma_minus8
	pocBits := r.ue() + 4
	first := maxSub
	if r.u(1) != 0 { // sps_sub_layer_ordering_info_present_flag
		first = 0
	}
	for i := first; i <= maxSub; i++ {
		r.ue()             // sps_max_dec_pic_buffering_minus1
		s.reorder = r.ue() // sps_max_num_reorder_pics
		r.ue()             // sps_max_latency_increase_plus1
	}
	for range 6 { // coding and transform block sizes, hierarchy depths
		r.ue()
	}
	scaling := r.u(1) != 0      // scaling_list_enabled_flag
	if scaling && r.u(1) != 0 { // sps_scaling_list_data_present_flag
		for size := range 4 {
			step := 1
			if size == 3 {
				step = 3
			}
			for m := 0; m < 6; m += step {
				if r.u(1) == 0 { // scaling_list_pred_mode_flag
					r.ue()
					continue
				}
				n := min(64, 1<<(4+size<<1))
				if size > 1 {
					r.se()
				}
				for range n {
					r.se()
				}
			}
		}
	}
	r.u(2)           // amp_enabled_flag, sample_adaptive_offset_enabled_flag
	if r.u(1) != 0 { // pcm_enabled_flag
		r.u(8)
		r.ue()
		r.ue()
		r.u(1)
	}
	sets := r.ue()
	deltas := make([]int, sets) // NumDeltaPocs of each set
	for i := range sets {
		if i != 0 && r.u(1) != 0 { // inter_ref_pic_set_prediction_flag
			r.u(1) // delta_rps_sign
			r.ue() // abs_delta_rps_minus1
			n := 0
			for range deltas[i-1] + 1 {
				used := r.u(1)
				delta := 1
				if used == 0 {
					delta = r.u(1) // use_delta_flag
				}
				if used != 0 || delta != 0 {
					n++
				}
			}
			deltas[i] = n
		} else {
			neg, pos := r.ue(), r.ue()
			for range neg + pos {
				r.ue()
				r.u(1)
			}
			deltas[i] = neg + pos
		}
		if r.err != nil {
			return s, r.err
		}
	}
	if r.u(1) != 0 { // long_term_ref_pics_present_flag
		for range r.ue() {
			r.u(pocBits + 1)
		}
	}
	r.u(2)           // sps_temporal_mvp_enabled_flag, strong_intra_smoothing_enabled_flag
	if r.u(1) != 0 { // vui_parameters_present_flag
		s.color = vuiColor(r)
		if r.u(1) != 0 { // chroma_loc_info_present_flag
			r.ue()
			r.ue()
		}
		r.u(3)           // neutral_chroma_indication_flag, field_seq_flag, frame_field_info_present_flag
		if r.u(1) != 0 { // default_display_window_flag
			for range 4 {
				r.ue()
			}
		}
		if r.u(1) != 0 { // vui_timing_info_present_flag
			tick, scale := r.u(32), r.u(32)
			if tick > 0 && r.err == nil {
				s.rateNum, s.rateDen = scale, tick
			}
		}
	}
	return s, r.err
}

// vuiColor reads the start of VUI parameters, the same in both codecs, up
// to the colour description.
func vuiColor(r *bits) ColorInfo {
	c := ColorInfo{Primaries: 2, Transfer: 2, Matrix: 2}
	if r.u(1) != 0 { // aspect_ratio_info_present_flag
		if r.u(8) == 255 {
			r.u(32)
		}
	}
	if r.u(1) != 0 { // overscan_info_present_flag
		r.u(1)
	}
	if r.u(1) != 0 { // video_signal_type_present_flag
		r.u(3)
		c.FullRange = r.u(1) != 0
		if r.u(1) != 0 { // colour_description_present_flag
			c.Primaries, c.Transfer, c.Matrix = r.u(8), r.u(8), r.u(8)
		}
	}
	return c
}

// h264SPS reads an H.264 SPS NAL unit (with its 1-byte header).
func h264SPS(nal []byte) (spsInfo, error) {
	if len(nal) < 4 {
		return spsInfo{}, errSPS
	}
	r := newBits(nal[1:])
	profile := r.u(8)
	r.u(16) // constraint flags, level_idc
	r.ue()  // seq_parameter_set_id
	chroma, depth := 1, 8
	switch profile {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		chroma = r.ue()
		if chroma == 3 {
			r.u(1)
		}
		depth = r.ue() + 8
		r.ue()           // bit_depth_chroma_minus8
		r.u(1)           // qpprime_y_zero_transform_bypass_flag
		if r.u(1) != 0 { // seq_scaling_matrix_present_flag
			n := 8
			if chroma == 3 {
				n = 12
			}
			for i := range n {
				if r.u(1) == 0 {
					continue
				}
				size := 16
				if i >= 6 {
					size = 64
				}
				last, next := 8, 8
				for range size {
					if next != 0 {
						next = (last + r.se() + 256) % 256
					}
					if next != 0 {
						last = next
					}
				}
			}
		}
	}
	r.ue() // log2_max_frame_num_minus4
	switch r.ue() {
	case 0:
		r.ue() // log2_max_pic_order_cnt_lsb_minus4
	case 1:
		r.u(1)
		r.se()
		r.se()
		for range r.ue() {
			r.se()
		}
	}
	r.ue() // max_num_ref_frames
	r.u(1) // gaps_in_frame_num_value_allowed_flag
	var s spsInfo
	s.depth = depth
	wMB, hMU := r.ue()+1, r.ue()+1
	frameMBsOnly := r.u(1)
	if frameMBsOnly == 0 {
		r.u(1) // mb_adaptive_frame_field_flag
	}
	r.u(1) // direct_8x8_inference_flag
	s.codedW, s.codedH = wMB*16, hMU*16*(2-frameMBsOnly)
	if r.u(1) != 0 { // frame_cropping_flag
		cx, cy := 1, 2-frameMBsOnly
		if chroma == 1 || chroma == 2 {
			cx = 2
		}
		if chroma == 1 {
			cy *= 2
		}
		s.cropL, s.cropR, s.cropT, s.cropB = r.ue()*cx, r.ue()*cx, r.ue()*cy, r.ue()*cy
	}
	s.color = ColorInfo{Primaries: 2, Transfer: 2, Matrix: 2}
	if r.u(1) != 0 { // vui_parameters_present_flag
		s.color = vuiColor(r)
		if r.u(1) != 0 { // chroma_loc_info_present_flag
			r.ue()
			r.ue()
		}
		if r.u(1) != 0 { // timing_info_present_flag
			tick, scale := r.u(32), r.u(32)
			if tick > 0 && r.err == nil {
				s.rateNum, s.rateDen = scale, 2*tick // a tick is a field
			}
		}
	}
	return s, r.err
}

// annexBNALs splits an Annex B stream into NAL units.
func annexBNALs(b []byte) [][]byte {
	var out [][]byte
	start := -1
	for i := 0; i+2 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			if start >= 0 {
				out = append(out, b[start:i])
			}
			start = i + 3
			i += 2
		}
	}
	if start >= 0 {
		out = append(out, b[start:])
	}
	return out
}
