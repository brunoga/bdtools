package av1

// The sequence header and the frame header, for the streams this encoder
// writes: profile 0 (4:2:0, 8 or 10 bits), 64x64 superblocks, one
// operating point, order hints of 7 bits; the tools the encoder uses are
// switched on here.

import "math/bits"

// seqParams is a sequence header's settings.
type seqParams struct {
	width, height int
	bitDepth      int
	// colour description, when known (0 unknown: CP/TC/MC 2)
	primaries, transfer, matrix int
	fullRange                   bool
	chromaPos                   int // 0 unknown, 1 vertical (left), 2 colocated
	filterIntra, edgeFilter     bool
	cdef, restoration           bool
	// timing, when the rate is known
	num, den int
}

// level is the sequence's seq_level_idx (4*(major-2)+minor), by its
// picture size: 4.1 to 1080p, 5.1 to 4K, 6.1 above.
func (s *seqParams) level() int {
	switch n := s.width * s.height; {
	case n <= 2228224 && s.width <= 4096:
		return 9
	case n <= 8912896 && s.width <= 8192:
		return 13
	}
	return 17
}

// widthBits and heightBits are the bits frame sizes are written in.
func (s *seqParams) widthBits() int  { return max(bits.Len(uint(s.width-1)), 1) }
func (s *seqParams) heightBits() int { return max(bits.Len(uint(s.height-1)), 1) }

const orderHintBits = 7

// sequenceHeader is the sequence header OBU's payload.
func (s *seqParams) sequenceHeader() []byte {
	var w bitWriter
	w.f(0, 3)     // seq_profile
	w.flag(false) // still_picture
	w.flag(false) // reduced_still_picture_header
	timing := s.num > 0 && s.den > 0
	w.flag(timing) // timing_info_present_flag
	if timing {
		w.f(uint32(s.den), 32) //nolint:gosec // num_units_in_display_tick
		w.f(uint32(s.num), 32) //nolint:gosec // time_scale
		w.flag(false)          // equal_picture_interval
		w.flag(false)          // decoder_model_info_present_flag
	}
	w.flag(false) // initial_display_delay_present_flag
	w.f(0, 5)     // operating_points_cnt_minus_1
	w.f(0, 12)    // operating_point_idc[0]
	lv := s.level()
	w.f(uint32(lv), 5) //nolint:gosec // small
	if lv > 7 {
		w.flag(false) // seq_tier: main
	}
	w.f(uint32(s.widthBits()-1), 4)         //nolint:gosec // small
	w.f(uint32(s.heightBits()-1), 4)        //nolint:gosec // small
	w.f(uint32(s.width-1), s.widthBits())   //nolint:gosec // a size
	w.f(uint32(s.height-1), s.heightBits()) //nolint:gosec // a size
	w.flag(false)                           // frame_id_numbers_present_flag
	w.flag(false)                           // use_128x128_superblock
	w.flag(s.filterIntra)
	w.flag(s.edgeFilter)
	w.flag(false) // enable_interintra_compound
	w.flag(false) // enable_masked_compound
	w.flag(false) // enable_warped_motion
	w.flag(false) // enable_dual_filter
	w.flag(true)  // enable_order_hint
	w.flag(false) // enable_jnt_comp
	w.flag(false) // enable_ref_frame_mvs
	w.flag(false) // seq_choose_screen_content_tools
	w.flag(false) // seq_force_screen_content_tools: off
	w.f(orderHintBits-1, 3)
	w.flag(false) // enable_superres
	w.flag(s.cdef)
	w.flag(s.restoration)
	// color_config
	w.flag(s.bitDepth > 8) // high_bitdepth
	w.flag(false)          // mono_chrome
	desc := s.primaries != 0 || s.transfer != 0 || s.matrix != 0
	w.flag(desc)
	if desc {
		w.f(uint32(orUnspecified(s.primaries)), 8) //nolint:gosec // a code
		w.f(uint32(orUnspecified(s.transfer)), 8)  //nolint:gosec // a code
		w.f(uint32(orUnspecified(s.matrix)), 8)    //nolint:gosec // a code
	}
	w.flag(s.fullRange)
	w.f(uint32(s.chromaPos), 2) //nolint:gosec // a code
	w.flag(false)               // separate_uv_delta_q
	w.flag(false)               // film_grain_params_present
	w.trailing()
	return w.buf
}

func orUnspecified(c int) int {
	if c == 0 {
		return 2
	}
	return c
}

// tileLog2 is the smallest k with blk<<k at least target.
func tileLog2(blk, target int) int {
	k := 0
	for blk<<uint(k) < target {
		k++
	}
	return k
}

// Tile limits (spec A.3 and 6.8.14).
const (
	maxTileWidth = 4096
	maxTileArea  = 4096 * 2304
	maxTileRows  = 64
	maxTileCols  = 64
)

// tiling is a frame's tiles: uniform, 1<<colsLog2 by 1<<rowsLog2 at most,
// in superblocks (64x64).
type tiling struct {
	colsLog2, rowsLog2 int
	// the superblock each tile column and row starts at, and the end
	colStarts, rowStarts []int
}

// newTiling divides a frame of mi columns and rows into about wantCols
// by wantRows tiles, as the limits allow.
func newTiling(miCols, miRows, wantColsLog2, wantRowsLog2 int) tiling {
	sbCols, sbRows := (miCols+15)>>4, (miRows+15)>>4
	minCols := tileLog2(maxTileWidth>>6, sbCols)
	maxCols := tileLog2(1, min(sbCols, maxTileCols))
	maxRows := tileLog2(1, min(sbRows, maxTileRows))
	minTiles := max(minCols, tileLog2(maxTileArea>>12, sbRows*sbCols))
	var t tiling
	t.colsLog2 = min(max(wantColsLog2, minCols), maxCols)
	t.rowsLog2 = min(max(wantRowsLog2, max(minTiles-t.colsLog2, 0)), maxRows)
	w := (sbCols + 1<<uint(t.colsLog2) - 1) >> uint(t.colsLog2)
	for s := 0; s < sbCols; s += w {
		t.colStarts = append(t.colStarts, s)
	}
	t.colStarts = append(t.colStarts, sbCols)
	h := (sbRows + 1<<uint(t.rowsLog2) - 1) >> uint(t.rowsLog2)
	for s := 0; s < sbRows; s += h {
		t.rowStarts = append(t.rowStarts, s)
	}
	t.rowStarts = append(t.rowStarts, sbRows)
	return t
}

func (t *tiling) cols() int { return len(t.colStarts) - 1 }
func (t *tiling) rows() int { return len(t.rowStarts) - 1 }

// tileSizeBytes is how many bytes a tile's size is written in.
const tileSizeBytes = 4

// write writes the tile_info.
func (t *tiling) write(w *bitWriter, miCols, miRows int) {
	sbCols, sbRows := (miCols+15)>>4, (miRows+15)>>4
	minCols := tileLog2(maxTileWidth>>6, sbCols)
	maxCols := tileLog2(1, min(sbCols, maxTileCols))
	maxRows := tileLog2(1, min(sbRows, maxTileRows))
	minTiles := max(minCols, tileLog2(maxTileArea>>12, sbRows*sbCols))
	w.flag(true) // uniform_tile_spacing_flag
	for k := minCols; k < maxCols; k++ {
		w.flag(k < t.colsLog2) // increment_tile_cols_log2
		if k >= t.colsLog2 {
			break
		}
	}
	for k := max(minTiles-t.colsLog2, 0); k < maxRows; k++ {
		w.flag(k < t.rowsLog2)
		if k >= t.rowsLog2 {
			break
		}
	}
	if t.colsLog2 > 0 || t.rowsLog2 > 0 {
		w.f(0, t.colsLog2+t.rowsLog2) // context_update_tile_id
		w.f(tileSizeBytes-1, 2)
	}
}

// frameParams is a frame header's settings.
type frameParams struct {
	key bool
	// Inter frames: the slots refreshed, the slot each of the seven
	// references is, mv precision and the frame's interpolation filter.
	refresh         uint8
	primaryRef      int // primary_ref_frame (PRIMARY_REF_NONE, or a reference)
	refIdx          [REFS_PER_FRAME]int
	highPrecisionMV bool
	interpFilter    int
	orderHint       int
	adapt           bool // the CDFs adapt (disable_cdf_update 0)
	qIndex          int
	deltaQYDc       int
	deltaQUVDc      int
	deltaQUVAc      int
	lfLevel         [4]int
	lfSharp         int
	txSelect        bool
	// CDEF (when the sequence enables it): cdef_damping_minus_3 + 3,
	// cdef_bits, and each index's primary and secondary strengths (the
	// secondary 0, 1, 2 or 4), luma's and chroma's.
	cdefDamping int
	cdefBits    int
	cdefY       [8][2]int
	cdefUV      [8][2]int
	reducedTx   bool
	tiles       tiling
}

// lossless reports whether the frame is coded losslessly.
func (f *frameParams) lossless() bool {
	return f.qIndex == 0 && f.deltaQYDc == 0 && f.deltaQUVDc == 0 && f.deltaQUVAc == 0
}

// frameHeader writes a shown frame's uncompressed header, a key frame's
// or an inter frame's (for an OBU_FRAME: the tile group follows from the
// next byte).
func (s *seqParams) frameHeader(f *frameParams) []byte {
	var w bitWriter
	miCols, miRows := 2*((s.width+7)>>3), 2*((s.height+7)>>3)
	w.flag(false) // show_existing_frame
	if f.key {
		w.f(KEY_FRAME, 2)
	} else {
		w.f(INTER_FRAME, 2)
	}
	w.flag(true) // show_frame
	if !f.key {
		w.flag(false) // error_resilient_mode
	}
	w.flag(!f.adapt)                                             // disable_cdf_update
	w.flag(false)                                                // frame_size_override_flag
	w.f(uint32(f.orderHint)&(1<<orderHintBits-1), orderHintBits) //nolint:gosec // its bits
	if !f.key {
		w.f(uint32(f.primaryRef), 3) //nolint:gosec // primary_ref_frame
		w.f(uint32(f.refresh), 8)
		w.flag(false) // frame_refs_short_signaling
		for _, i := range f.refIdx {
			w.f(uint32(i), 3) //nolint:gosec // a slot
		}
	}
	// frame_size: the sequence's; render_size the same
	w.flag(false) // render_and_frame_size_different
	if !f.key {
		w.flag(f.highPrecisionMV)
		w.flag(false)                  // is_filter_switchable
		w.f(uint32(f.interpFilter), 2) //nolint:gosec // a filter
		w.flag(false)                  // is_motion_mode_switchable
	}
	if f.adapt {
		w.flag(false) // disable_frame_end_update_cdf
	}
	f.tiles.write(&w, miCols, miRows)
	// quantization_params
	w.f(uint32(f.qIndex), 8) //nolint:gosec // a q index
	deltaQ := func(d int) {
		w.flag(d != 0)
		if d != 0 {
			w.su(d, 7)
		}
	}
	deltaQ(f.deltaQYDc)
	deltaQ(f.deltaQUVDc)
	deltaQ(f.deltaQUVAc)
	w.flag(false) // using_qmatrix
	w.flag(false) // segmentation_enabled
	if f.qIndex > 0 {
		w.flag(false) // delta_q_present
	}
	if !f.lossless() {
		// loop_filter_params
		w.f(uint32(f.lfLevel[0]), 6) //nolint:gosec // a level
		w.f(uint32(f.lfLevel[1]), 6) //nolint:gosec // a level
		if f.lfLevel[0] != 0 || f.lfLevel[1] != 0 {
			w.f(uint32(f.lfLevel[2]), 6) //nolint:gosec // a level
			w.f(uint32(f.lfLevel[3]), 6) //nolint:gosec // a level
		}
		w.f(uint32(f.lfSharp), 3) //nolint:gosec // small
		w.flag(false)             // loop_filter_delta_enabled
		if s.cdef {
			w.f(uint32(f.cdefDamping-3), 2) //nolint:gosec // 3..6
			w.f(uint32(f.cdefBits), 2)      //nolint:gosec // 0..3
			sec := func(v int) uint32 {
				if v == 4 {
					return 3
				}
				return uint32(v) //nolint:gosec // 0..2
			}
			for i := range 1 << uint(f.cdefBits) {
				w.f(uint32(f.cdefY[i][0]), 4) //nolint:gosec // 0..15
				w.f(sec(f.cdefY[i][1]), 2)
				w.f(uint32(f.cdefUV[i][0]), 4) //nolint:gosec // 0..15
				w.f(sec(f.cdefUV[i][1]), 2)
			}
		}
	}
	if s.restoration && !f.lossless() {
		panic("av1: lr_params")
	}
	if !f.lossless() {
		w.flag(f.txSelect)
	}
	if !f.key {
		w.flag(false) // reference_select
	}
	w.flag(f.reducedTx)
	if !f.key {
		for range REFS_PER_FRAME {
			w.flag(false) // is_global
		}
	}
	return w.buf
}

// tileGroup is a frame OBU's tile group: its tiles, all but the last
// after their size.
func (f *frameParams) tileGroup(tiles [][]byte) []byte {
	var w bitWriter
	if len(tiles) > 1 {
		w.flag(false) // tile_start_and_end_present_flag
	}
	w.align()
	b := w.buf
	for i, t := range tiles {
		if i < len(tiles)-1 {
			n := len(t) - 1
			b = append(b, byte(n), byte(n>>8), byte(n>>16), byte(n>>24))
		}
		b = append(b, t...)
	}
	return b
}
