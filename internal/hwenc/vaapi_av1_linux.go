//go:build linux && (amd64 || arm64)

package hwenc

// AV1 through VAAPI. Intel's driver codes the tiles but not the headers:
// the sequence header OBU and every frame header OBU are written here and
// passed as packed headers, and the picture parameters say where in the
// frame header the driver is to write the fields its rate control and
// filters decide (the quantiser index, the loop filter and CDEF
// parameters) and where the OBU's size goes, which it patches.
//
// The structure is the simplest that codes well: a key frame every GOP,
// then P frames each predicted from the one before (every reference slot
// pointing at it), every frame shown, so each picture is one temporal
// unit. The temporal delimiter that starts it is added on output.

// av1Pic is what the AV1 headers need about a picture.
const (
	av1SB              = 64 // superblock size (no 128x128)
	av1OrderHintBits   = 8
	av1PrimaryRefNone  = 7
	av1AllFrames       = 0xff
	av1OBUSequence     = 1
	av1OBUFrameHeader  = 3
	av1TemporalDelimit = 2
)

// av1TD is a temporal delimiter OBU with its (empty) size.
var av1TD = []byte{av1TemporalDelimit<<3 | 2, 0}

// av1Level is the lowest AV1 level (Annex A) whose picture size and
// display rate take the stream, as seq_level_idx; at least 4.0.
func av1Level(cfg Config) int {
	ps := cfg.Width * cfg.Height
	rate := float64(ps) * float64(cfg.FPSNum) / float64(max(cfg.FPSDen, 1))
	for _, l := range []struct {
		idx, maxPS, maxW, maxH int
		maxRate                float64
	}{
		{8, 2359296, 6144, 3456, 70778880},
		{9, 2359296, 6144, 3456, 141557760},
		{12, 8912896, 8192, 4352, 267386880},
		{13, 8912896, 8192, 4352, 534773760},
		{14, 8912896, 8192, 4352, 1069547520},
		{16, 35651584, 16384, 8704, 1069547520},
		{17, 35651584, 16384, 8704, 2139095040},
	} {
		if ps <= l.maxPS && rate <= l.maxRate && cfg.Width <= l.maxW && cfg.Height <= l.maxH {
			return l.idx
		}
	}
	return 31 // no level constraint
}

func bitLen(v int) int {
	n := 0
	for ; v > 0; v >>= 1 {
		n++
	}
	return max(n, 1)
}

// av1TileLog2 is tile_log2: the smallest k with blkSize<<k >= target.
func av1TileLog2(blkSize, target int) int {
	k := 0
	for blkSize<<k < target {
		k++
	}
	return k
}

// av1SequenceHeader writes the sequence header OBU (AV1 5.5): one
// operating point, order hints, CDEF, nothing else optional.
func (e *vaapi) av1SequenceHeader() []byte {
	w, h := e.cfg.Width, e.cfg.Height
	wb, hb := bitLen(w-1), bitLen(h-1)
	s := &bitWriter{}
	s.u(3, 0)     // seq_profile: Main
	s.flag(false) // still_picture
	s.flag(false) // reduced_still_picture_header
	s.flag(false) // timing_info_present_flag
	s.flag(false) // initial_display_delay_present_flag
	s.u(5, 0)     // operating_points_cnt_minus_1
	s.u(12, 0)    // operating_point_idc[0]
	level := av1Level(e.cfg)
	s.u(5, uint32(level)) //nolint:gosec // a level index
	if level > 7 {
		s.flag(false) // seq_tier: Main
	}
	s.u(4, uint32(wb-1)) //nolint:gosec // small
	s.u(4, uint32(hb-1)) //nolint:gosec // small
	s.u(wb, uint32(w-1)) //nolint:gosec // frame size
	s.u(hb, uint32(h-1)) //nolint:gosec // frame size
	s.flag(false)        // frame_id_numbers_present_flag
	s.flag(false)        // use_128x128_superblock
	s.flag(false)        // enable_filter_intra
	s.flag(false)        // enable_intra_edge_filter
	s.flag(false)        // enable_interintra_compound
	s.flag(false)        // enable_masked_compound
	s.flag(false)        // enable_warped_motion
	s.flag(false)        // enable_dual_filter
	s.flag(true)         // enable_order_hint
	s.flag(false)        // enable_jnt_comp
	s.flag(false)        // enable_ref_frame_mvs
	s.flag(false)        // seq_choose_screen_content_tools
	s.flag(false)        // seq_force_screen_content_tools
	s.u(3, av1OrderHintBits-1)
	s.flag(false) // enable_superres
	s.flag(true)  // enable_cdef
	s.flag(false) // enable_restoration
	// color_config: 8-bit 4:2:0, no colour description.
	s.flag(false) // high_bitdepth
	s.flag(false) // mono_chrome
	s.flag(false) // color_description_present_flag
	s.flag(false) // color_range: studio
	s.u(2, 0)     // chroma_sample_position: unknown
	s.flag(false) // separate_uv_delta_q
	s.flag(false) // film_grain_params_present
	return av1OBU(av1OBUSequence, s.trailing())
}

// av1OBU wraps a payload in an OBU header and a one-byte (or longer)
// size.
func av1OBU(typ byte, payload []byte) []byte {
	out := []byte{typ<<3 | 2}
	n := len(payload)
	for {
		c := byte(n & 0x7f)
		n >>= 7
		if n == 0 {
			out = append(out, c)
			break
		}
		out = append(out, c|0x80)
	}
	return append(out, payload...)
}

// av1FrameHeader is a frame header OBU and the bit offsets (from its start)
// of the fields the driver rewrites.
type av1FrameHeader struct {
	data                            []byte
	qindex, seg, lf, cdef, cdefBits int
}

// av1FrameHeaderOBU writes a frame header OBU (AV1 5.9) for a shown key
// frame or a P frame predicted from reference slot 0, its size in a padded
// four-byte field the driver patches.
func (e *vaapi) av1FrameHeaderOBU(p vaPic, qindex int) av1FrameHeader {
	key := p.typ == picI
	w, h := e.cfg.Width, e.cfg.Height
	s := &bitWriter{}
	const hdrBits = 40 // the OBU header byte and the four-byte size
	pos := func() int { return hdrBits + 8*len(s.b) + s.nbit }
	var fh av1FrameHeader

	s.flag(false) // show_existing_frame
	if key {
		s.u(2, 0) // KEY_FRAME
	} else {
		s.u(2, 1) // INTER_FRAME
	}
	s.flag(true) // show_frame
	if !key {
		s.flag(false) // error_resilient_mode (implied for a shown key frame)
	}
	s.flag(false)                              // disable_cdf_update
	s.flag(false)                              // frame_size_override_flag
	s.u(av1OrderHintBits, uint32(p.disp&0xff)) //nolint:gosec // masked
	if !key {
		s.u(3, 0)    // primary_ref_frame: slot 0's
		s.u(8, 0x01) // refresh_frame_flags: this picture into slot 0
		s.flag(false)
		for range 7 {
			s.u(3, 0) // ref_frame_idx: every reference is slot 0
		}
	}
	s.flag(false) // render_and_frame_size_different
	if !key {
		s.flag(false) // allow_high_precision_mv
		s.flag(false) // is_filter_switchable
		s.u(2, 0)     // interpolation_filter: EIGHTTAP
		s.flag(false) // is_motion_mode_switchable
	}
	s.flag(false) // disable_frame_end_update_cdf
	// tile_info: one tile, uniformly spaced.
	sbCols, sbRows := (w+av1SB-1)/av1SB, (h+av1SB-1)/av1SB
	s.flag(true)                       // uniform_tile_spacing_flag
	minCols := av1TileLog2(64, sbCols) // MAX_TILE_WIDTH_SB
	if minCols < av1TileLog2(1, min(sbCols, 64)) {
		s.flag(false) // increment_tile_cols_log2
	}
	minTiles := max(minCols, av1TileLog2(4096*2304>>12, sbRows*sbCols))
	if max(minTiles-minCols, 0) < av1TileLog2(1, min(sbRows, 64)) {
		s.flag(false) // increment_tile_rows_log2
	}
	// quantization_params
	fh.qindex = pos()
	s.u(8, uint32(qindex)) //nolint:gosec // 1..255
	s.flag(false)          // DeltaQYDc delta_coded
	s.flag(false)          // DeltaQUDc delta_coded
	s.flag(false)          // DeltaQUAc delta_coded
	s.flag(false)          // using_qmatrix
	fh.seg = pos()
	s.flag(false) // segmentation_enabled
	s.flag(false) // delta_q_present
	// loop_filter_params, which the driver fills in.
	fh.lf = pos()
	s.u(6, 0)     // loop_filter_level[0]
	s.u(6, 0)     // loop_filter_level[1]
	s.u(3, 0)     // loop_filter_sharpness
	s.flag(false) // loop_filter_delta_enabled
	// cdef_params, likewise.
	fh.cdef = pos()
	s.u(2, 0) // cdef_damping_minus_3
	s.u(2, 0) // cdef_bits
	s.u(4, 0) // cdef_y_pri_strength[0]
	s.u(2, 0) // cdef_y_sec_strength[0]
	s.u(4, 0) // cdef_uv_pri_strength[0]
	s.u(2, 0) // cdef_uv_sec_strength[0]
	fh.cdefBits = pos() - fh.cdef
	s.flag(true) // tx_mode_select: TX_MODE_SELECT
	if !key {
		s.flag(false) // reference_select: single reference
	}
	s.flag(false) // reduced_tx_set
	if !key {
		for range 7 {
			s.flag(false) // is_global
		}
	}
	payload := s.trailing()
	n := len(payload)
	fh.data = append([]byte{av1OBUFrameHeader<<3 | 2,
		byte(n&0x7f) | 0x80, byte(n>>7&0x7f) | 0x80, byte(n>>14&0x7f) | 0x80, byte(n >> 21 & 0x7f)}, payload...)
	return fh
}

// av1Params adds the buffers for one AV1 picture: on a key frame the
// sequence parameters and header; then the picture parameters, the frame
// header and the one tile group.
func (e *vaapi) av1Params(p vaPic, l0 []vaPic, add addBuf) error {
	key := p.typ == picI
	w, h := e.cfg.Width, e.cfg.Height
	qindex := AV1QIndex(e.qpP)
	if key {
		qindex = AV1QIndex(e.qpI)
	}
	var seqHdr []byte
	if key {
		s := newStruct(vaSizeAV1Seq)
		s.u8(vaAV1SeqProfile, 0)
		s.u8(vaAV1SeqLevel, uint8(av1Level(e.cfg)))   //nolint:gosec // a level index
		s.u32(vaAV1SeqIntraPeriod, uint32(e.cfg.GOP)) //nolint:gosec // small
		s.u32(vaAV1SeqIPPeriod, 1)
		s.u32(vaAV1SeqFields, 1<<vaAV1SeqOrderHintBit|1<<vaAV1SeqCDEFBit)
		s.u8(vaAV1SeqOrderHintBits, av1OrderHintBits-1)
		if err := add(vaEncSequenceParameterBufferType, s); err != nil {
			return err
		}
		seqHdr = e.av1SequenceHeader()
	}
	fh := e.av1FrameHeaderOBU(p, qindex)

	pp := newStruct(vaSizeAV1Pic)
	pp.u16(vaAV1PicWidth, uint16(w-1))  //nolint:gosec // frame size
	pp.u16(vaAV1PicHeight, uint16(h-1)) //nolint:gosec // frame size
	pp.u32(vaAV1PicRecon, e.recs[p.rec])
	pp.u32(vaAV1PicCodedBuf, e.codeds[p.coded])
	for i := range 8 {
		pp.u32(vaAV1PicRefs+4*i, vaInvalidID)
	}
	flags := uint32(1) << vaAV1PicErrorResilientBit // key frame (frame_type 0)
	if key {
		pp.u8(vaAV1PicPrimaryRef, av1PrimaryRefNone)
		pp.u8(vaAV1PicRefresh, av1AllFrames)
	} else {
		pp.u32(vaAV1PicRefs, e.recs[l0[0].rec])
		pp.u8(vaAV1PicPrimaryRef, 0)
		pp.u8(vaAV1PicRefresh, 0x01)
		pp.u32(vaAV1PicRefCtrlL0, 1<<vaAV1SearchIdx0Bit) // LAST_FRAME
		flags = 1 << vaAV1PicFrameTypeBit                // INTER_FRAME
	}
	pp.u8(vaAV1PicOrderHint, uint8(p.disp&0xff)) //nolint:gosec // masked
	pp.u32(vaAV1PicFlags, flags)
	pp.u8(vaAV1PicBaseQIndex, uint8(qindex)) //nolint:gosec // 1..255
	pp.u8(vaAV1PicMinQIndex, 1)
	pp.u8(vaAV1PicMaxQIndex, 255)
	pp.u32(vaAV1PicModeControl, 2<<vaAV1PicTxModeBit) // TX_MODE_SELECT
	pp.u8(vaAV1PicTileCols, 1)
	pp.u8(vaAV1PicTileRows, 1)
	pp.u16(vaAV1PicWidthSBs, uint16((w+av1SB-1)/av1SB-1))  //nolint:gosec // frame size
	pp.u16(vaAV1PicHeightSBs, uint16((h+av1SB-1)/av1SB-1)) //nolint:gosec // frame size
	pp.u32(vaAV1PicBitOffsetQIndex, uint32(fh.qindex))     //nolint:gosec // small
	pp.u32(vaAV1PicBitOffsetSeg, uint32(fh.seg))           //nolint:gosec // small
	pp.u32(vaAV1PicBitOffsetLF, uint32(fh.lf))             //nolint:gosec // small
	pp.u32(vaAV1PicBitOffsetCDEF, uint32(fh.cdef))         //nolint:gosec // small
	pp.u32(vaAV1PicCDEFBits, uint32(fh.cdefBits))          //nolint:gosec // small
	// The size field's place counts the packed headers before it.
	pp.u32(vaAV1PicOBUSizeOffset, uint32(len(seqHdr)+1)) //nolint:gosec // small
	pp.u32(vaAV1PicFrameHdrBits, uint32(8*len(fh.data))) //nolint:gosec // small
	pp.u8(vaAV1PicTGHeader, 1<<vaAV1TGHasSizeBit)
	if err := add(vaEncPictureParameterBufferType, pp); err != nil {
		return err
	}
	if key {
		if err := e.packedRaw(add, vaEncPackedHeaderSequence, seqHdr); err != nil {
			return err
		}
	}
	if err := e.packedRaw(add, vaEncPackedHeaderPicture, fh.data); err != nil {
		return err
	}
	return add(vaEncSliceParameterBufferType, newStruct(vaSizeAV1TileGroup)) // tiles 0 to 0
}

// packedRaw adds a packed header as is: AV1 has no emulation prevention.
func (e *vaapi) packedRaw(add addBuf, typ uint32, data []byte) error {
	ph := newStruct(vaSizePackedHeaderParam)
	ph.u32(vaPackedType, typ)
	ph.u32(vaPackedBitLength, uint32(8*len(data))) //nolint:gosec // small
	ph.u8(vaPackedHasEmulation, 1)
	if err := add(vaEncPackedHeaderParameterBufferType, ph); err != nil {
		return err
	}
	buf := newStruct(len(data))
	copy(buf, data)
	return add(vaEncPackedHeaderDataBufferType, buf)
}
