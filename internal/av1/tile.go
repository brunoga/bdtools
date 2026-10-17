package av1

// A key frame's tiles coded: the partition of each superblock, each
// block's intra mode info and its transform blocks' coefficients, as
// decode_tile reads them (spec 5.11), with the contexts it derives; the
// reconstruction follows, exactly as a decoder makes it.
//
// Each superblock is decided first, by rate and distortion (the rates
// from the CDFs as the superblock starts, a costCoder adding them up),
// every alternative tried from the same state (snapshots of what coding
// a block changes); then the decisions are coded for real, from the
// superblock's starting state, and the CDFs adapt.

import (
	"fmt"
	"math"
)

var dbgCompare bool

// frameEnc is the frame being coded, shared by its tiles.
type frameEnc struct {
	seq            *seqParams
	fp             *frameParams
	bitDepth       int
	miCols, miRows int
	width, height  int
	// The source and the reconstruction, padded to whole superblocks
	// (the source by its edges).
	src, rec [3][]uint16
	stride   [3]int
	// Per 4x4 luma block (the mi grid, miCols wide).
	miSize, yMode, uvMode, skip, txSize []uint8
	txType                              []uint8 // TxTypes
	// Inter state per mi: the reference frames (RefFrames: INTRA_FRAME
	// and NONE for intra blocks), the motion vectors (Mvs), whether a block
	// is inter (IsInters) and skip_mode (SkipModes).
	refFrames [2][]int8
	mvs       [2][]mv
	isInter   []uint8
	skipMode  []uint8
	inter     *interFrameInfo                      // inter frames' header values (nil for key frames)
	refs      [TOTAL_REFS_PER_FRAME]*refFrameState // by LAST_FRAME..ALTREF_FRAME
	filt      [2]int                               // the frame's interpolation filter
	// cdefIdx is each 64x64's cdef_idx: -1 where none is coded (all its
	// blocks skipped), else what the frame's CDEF search chose.
	cdefIdx []int8
	// lfTx is LoopfilterTxSizes: each plane's transform size by 4x4 block
	// of the plane, (miCols >> sub) wide.
	lfTx     [3][]uint8
	dcQ, acQ [3]int
	lossless bool
	lambda   float64 // distortion per bit
	// cdf0 is the CDFs every tile starts from: the defaults, or those a
	// reference kept (load_cdfs) with primary_ref_frame; savedCDF what the
	// frame keeps for later frames.
	cdf0, savedCDF *cdfs
}

func (f *frameEnc) mi(r, c int) int { return r*f.miCols + c }

// startCDF is a copy of the CDFs the frame's tiles start from.
func (f *frameEnc) startCDF() *cdfs {
	c := *f.cdf0
	return &c
}

// tileEnc codes one tile.
type tileEnc struct {
	f                    *frameEnc
	rowStart, rowEnd     int // in mi
	colStart, colEnd     int
	cdf                  *cdfs
	live, sbCDF          *cdfs // the coded CDFs, and a copy as the superblock starts
	w                    *recorder
	cost                 costCoder
	aboveLevel, aboveDc  [3][]uint8 // by x4 of the plane
	leftLevel, leftDc    [3][]uint8 // by y4 of the plane
	decoded              [3][18][18]bool
	edges                intraEdges
	b                    blockState
	pred, trial, best    [64 * 64]uint16
	resid, coef, dq, inv [64 * 64]int32
	txs                  []txCoded
	levels               []int32
	lv                   [32 * 32]uint8
	cflL                 [32 * 32]int32
	snaps                [10][8]*snapshot // by depth, a few each
	sbStart              snapshot
	cdefDone             bool
	dctOnly              bool // luma transform types not searched
	stack                mvStack
	txX4, txY4           int // the transform block being coded, in 4x4s of its plane
	ms                   motionSearch
	is                   interScratch
	interPred            [3][64 * 64]uint16 // an inter block's prediction, by plane // the superblock's cdef_idx is coded
}

// blockState is the block being coded (the spec's MiRow, MiSize, ...).
type blockState struct {
	r, c, size       int
	bw4, bh4         int
	hasChroma        bool
	availU, availL   bool
	availUC, availLC bool
	yMode, uvMode    int
	angleY, angleUV  int
	txSz             int // the luma's
	skip             bool
	skipMode         bool // skip_mode: the frame's SkipModeFrame, nothing coded
	cflU, cflV       int  // the CFL alphas
	// An inter block's: its reference, its motion vector and RefMvIdx (its
	// mode is yMode).
	isInter  bool
	refFrame int
	mv       mv
	refMvIdx int
	// a compound block's second reference (NONE for single prediction)
	// and its motion vector
	refFrame2  int
	mv2        mv
	filterMode int // filter_intra_mode, or -1
	// maxLumaW and maxLumaH are MaxLumaW and MaxLumaH: where the last
	// luma transform block coded ends.
	maxLumaW, maxLumaH int
}

// txCoded is a transform block's coefficients, kept for writing.
type txCoded struct {
	plane, x4, y4 int
	txSz, txType  int
	eob           int
	off           int // into levels
}

// blockDec is a block's decisions.
type blockDec struct {
	r, c, size      int
	yMode, uvMode   int
	angleY, angleUV int
	cflU, cflV      int
	filterMode      int
	isInter         bool
	refFrame        int
	refFrame2       int
	mv, mv2         mv
	refMvIdx        int
	skip            bool   // an inter block coded without residual
	skipMode        bool   // coded as skip_mode
	txSplit         []bool // an inter block's txfm_split, in read_var_tx_size's order
	txDepth         int
	txTypes         []uint8 // the luma transform blocks', in order
}

// partNode is a partition's decisions.
type partNode struct {
	part   int
	blocks []*blockDec
	kids   [4]*partNode
}

func newTileEnc(f *frameEnc, rowStart, rowEnd, colStart, colEnd int) *tileEnc {
	t := &tileEnc{f: f, rowStart: rowStart, rowEnd: rowEnd, colStart: colStart, colEnd: colEnd, cdf: f.startCDF()}
	t.w = &recorder{base: t.cdf}
	t.live, t.sbCDF = t.cdf, new(cdfs)
	for p := range 3 {
		sub := 0
		if p > 0 {
			sub = 1
		}
		t.aboveLevel[p] = make([]uint8, (f.miCols>>sub)+32)
		t.aboveDc[p] = make([]uint8, (f.miCols>>sub)+32)
		t.leftLevel[p] = make([]uint8, (f.miRows>>sub)+32)
		t.leftDc[p] = make([]uint8, (f.miRows>>sub)+32)
	}
	return t
}

func (t *tileEnc) inside(r, c int) bool {
	return c >= t.colStart && c < t.colEnd && r >= t.rowStart && r < t.rowEnd
}

// code codes the tile.
func (t *tileEnc) code() {
	for r := t.rowStart; r < t.rowEnd; r += 16 {
		for p := range 3 {
			clear(t.leftLevel[p])
			clear(t.leftDc[p])
		}
		for c := t.colStart; c < t.colEnd; c += 16 {
			t.clearDecoded(r, c)
			t.cdefDone = false
			t.save(&t.sbStart, r, c, BLOCK_64X64, false)
			*t.sbCDF = *t.live
			_, node := t.searchPartition(r, c, BLOCK_64X64, 0)
			t.restore(&t.sbStart, false)
			var keep [3][]uint16
			if dbgCompare {
				for p := range 3 {
					keep[p] = append([]uint16(nil), t.f.rec[p]...)
				}
			}
			t.finalPartition(r, c, BLOCK_64X64, node)
			if dbgCompare {
				for p := range 3 {
					for i, v := range keep[p] {
						if t.f.rec[p][i] != v {
							panic(fmt.Sprintf("SB %d,%d: plane %d (%d,%d): search %d final %d", r, c, p, i%t.f.stride[p], i/t.f.stride[p], v, t.f.rec[p][i]))
						}
					}
				}
			}
		}
	}
}

// clearDecoded is clear_block_decoded_flags; decoded[p][y+1][x+1] is
// BlockDecoded[p][y][x].
func (t *tileEnc) clearDecoded(r, c int) {
	for p := range 3 {
		sub := 0
		if p > 0 {
			sub = 1
		}
		sbW4, sbH4 := (t.colEnd-c)>>sub, (t.rowEnd-r)>>sub
		for y := -1; y <= 16>>sub; y++ {
			for x := -1; x <= 16>>sub; x++ {
				t.decoded[p][y+1][x+1] = (y < 0 && x < sbW4) || (x < 0 && y < sbH4)
			}
		}
		t.decoded[p][(16>>sub)+1][0] = false
	}
}

// snapshot is what coding a block changes: its reconstruction, its mi
// grid, the contexts along its edges and the decoded flags.
type snapshot struct {
	r, c, size int
	rec        [3][]uint16
	mi         [8][]uint8
	refs       [2][]int8
	mvs        [2][]mv
	above, aDc [3][16]uint8
	left, lDc  [3][16]uint8
	decoded    [3][18][18]bool
}

func (t *tileEnc) snap(depth, i int) *snapshot {
	if t.snaps[depth][i] == nil {
		t.snaps[depth][i] = &snapshot{}
	}
	return t.snaps[depth][i]
}

// save saves what coding a block at (r, c) changes; with pixels, its
// reconstruction and mi grid too (else only the contexts).
func (t *tileEnc) save(s *snapshot, r, c, size int, pixels bool) {
	f := t.f
	s.r, s.c, s.size = r, c, size
	bw4, bh4 := int(num4x4BlocksWide[size]), int(num4x4BlocksHigh[size])
	for p := range 3 {
		sub := 0
		if p > 0 {
			sub = 1
		}
		x0, y0 := c>>sub, r>>sub
		w4, h4 := max(bw4>>sub, 1), max(bh4>>sub, 1)
		copy(s.above[p][:w4], t.aboveLevel[p][x0:])
		copy(s.aDc[p][:w4], t.aboveDc[p][x0:])
		copy(s.left[p][:h4], t.leftLevel[p][y0:])
		copy(s.lDc[p][:h4], t.leftDc[p][y0:])
		if pixels {
			w, h := w4*4, h4*4
			st := f.stride[p]
			if cap(s.rec[p]) < w*h {
				s.rec[p] = make([]uint16, 64*64)
			}
			s.rec[p] = s.rec[p][:w*h]
			for y := range h {
				copy(s.rec[p][y*w:(y+1)*w], f.rec[p][(y0*4+y)*st+x0*4:])
			}
		}
	}
	if pixels {
		cols, rows := min(bw4, f.miCols-c), min(bh4, f.miRows-r)
		for k, a := range t.miArrays() {
			if cap(s.mi[k]) < 256 {
				s.mi[k] = make([]uint8, 256)
			}
			s.mi[k] = s.mi[k][:cols*rows]
			for y := range rows {
				copy(s.mi[k][y*cols:(y+1)*cols], a[f.mi(r+y, c):])
			}
		}
		for k := range 2 {
			if cap(s.refs[k]) < 256 {
				s.refs[k] = make([]int8, 256)
			}
			s.refs[k] = s.refs[k][:cols*rows]
			for y := range rows {
				copy(s.refs[k][y*cols:(y+1)*cols], f.refFrames[k][f.mi(r+y, c):])
			}
		}
		for k := range 2 {
			if cap(s.mvs[k]) < 256 {
				s.mvs[k] = make([]mv, 256)
			}
			s.mvs[k] = s.mvs[k][:cols*rows]
			for y := range rows {
				copy(s.mvs[k][y*cols:(y+1)*cols], f.mvs[k][f.mi(r+y, c):])
			}
		}
	}
	s.decoded = t.decoded
}

func (t *tileEnc) miArrays() [8][]uint8 {
	f := t.f
	return [8][]uint8{f.miSize, f.yMode, f.uvMode, f.skip, f.txSize, f.txType, f.isInter, f.skipMode}
}

// restore puts a snapshot back.
func (t *tileEnc) restore(s *snapshot, pixels bool) {
	f := t.f
	r, c := s.r, s.c
	bw4, bh4 := int(num4x4BlocksWide[s.size]), int(num4x4BlocksHigh[s.size])
	for p := range 3 {
		sub := 0
		if p > 0 {
			sub = 1
		}
		x0, y0 := c>>sub, r>>sub
		w4, h4 := max(bw4>>sub, 1), max(bh4>>sub, 1)
		copy(t.aboveLevel[p][x0:x0+w4], s.above[p][:w4])
		copy(t.aboveDc[p][x0:x0+w4], s.aDc[p][:w4])
		copy(t.leftLevel[p][y0:y0+h4], s.left[p][:h4])
		copy(t.leftDc[p][y0:y0+h4], s.lDc[p][:h4])
		if pixels {
			w, h := w4*4, h4*4
			st := f.stride[p]
			for y := range h {
				copy(f.rec[p][(y0*4+y)*st+x0*4:(y0*4+y)*st+x0*4+w], s.rec[p][y*w:(y+1)*w])
			}
		}
	}
	if pixels {
		cols, rows := min(bw4, f.miCols-c), min(bh4, f.miRows-r)
		for k, a := range t.miArrays() {
			for y := range rows {
				copy(a[f.mi(r+y, c):f.mi(r+y, c)+cols], s.mi[k][y*cols:(y+1)*cols])
			}
		}
		for k := range 2 {
			for y := range rows {
				copy(f.refFrames[k][f.mi(r+y, c):f.mi(r+y, c)+cols], s.refs[k][y*cols:(y+1)*cols])
			}
		}
		for k := range 2 {
			for y := range rows {
				copy(f.mvs[k][f.mi(r+y, c):f.mi(r+y, c)+cols], s.mvs[k][y*cols:(y+1)*cols])
			}
		}
	}
	t.decoded = s.decoded
}

// partitionCDF is the partition's CDF at (r, c).
func (t *tileEnc) partitionCDF(r, c, bSize int) []uint16 {
	f := t.f
	bsl := int(miWidthLog2[bSize])
	ctx := 0
	if t.inside(r-1, c) && int(miWidthLog2[f.miSize[f.mi(r-1, c)]]) < bsl {
		ctx++
	}
	if t.inside(r, c-1) && int(miHeightLog2[f.miSize[f.mi(r, c-1)]]) < bsl {
		ctx += 2
	}
	switch bsl {
	case 1:
		return t.cdf.partW8[ctx][:]
	case 2:
		return t.cdf.partW16[ctx][:]
	case 3:
		return t.cdf.partW32[ctx][:]
	case 4:
		return t.cdf.partW64[ctx][:]
	}
	return t.cdf.partW128[ctx][:]
}

// writePartition codes a partition (or what of it is coded at the
// frame's edges).
func (t *tileEnc) writePartition(cd coder, r, c, bSize, part int) {
	if bSize < BLOCK_8X8 {
		return
	}
	half := int(num4x4BlocksWide[bSize]) >> 1
	hasRows, hasCols := r+half < t.f.miRows, c+half < t.f.miCols
	cdf := t.partitionCDF(r, c, bSize)
	switch {
	case hasRows && hasCols:
		cd.symbol(part, cdf)
	case hasCols || hasRows:
		if r, ok := cd.(*recorder); ok {
			r.split(part == PARTITION_SPLIT, cdf, bSize, hasCols)
		} else if hasCols {
			cd.encode(b2i(part == PARTITION_SPLIT), splitOrHorz(cdf, bSize), 2)
		} else {
			cd.encode(b2i(part == PARTITION_SPLIT), splitOrVert(cdf, bSize), 2)
		}
	}
}

// depthOf is a square size's depth below the superblock.
func depthOf(bSize int) int {
	switch bSize {
	case BLOCK_64X64:
		return 0
	case BLOCK_32X32:
		return 1
	case BLOCK_16X16:
		return 2
	case BLOCK_8X8:
		return 3
	}
	return 4
}

// partBlock is a block of a partition: where it is from the partition's
// corner, in mi, and its size.
type partBlock struct{ dr, dc, size int }

// partitionBlocks are the blocks a partition (not a split) has, as
// decode_partition codes them, those outside the frame left out.
func (t *tileEnc) partitionBlocks(part, r, c, bSize int) []partBlock {
	f := t.f
	half := int(num4x4BlocksWide[bSize]) >> 1
	q := half >> 1
	sub := int(partitionSubsize[part][bSize])
	split := int(partitionSubsize[PARTITION_SPLIT][bSize])
	hasRows, hasCols := r+half < f.miRows, c+half < f.miCols
	switch part {
	case PARTITION_NONE:
		return []partBlock{{0, 0, sub}}
	case PARTITION_HORZ:
		if hasRows {
			return []partBlock{{0, 0, sub}, {half, 0, sub}}
		}
		return []partBlock{{0, 0, sub}}
	case PARTITION_VERT:
		if hasCols {
			return []partBlock{{0, 0, sub}, {0, half, sub}}
		}
		return []partBlock{{0, 0, sub}}
	case PARTITION_HORZ_A:
		return []partBlock{{0, 0, split}, {0, half, split}, {half, 0, sub}}
	case PARTITION_HORZ_B:
		return []partBlock{{0, 0, sub}, {half, 0, split}, {half, half, split}}
	case PARTITION_VERT_A:
		return []partBlock{{0, 0, split}, {half, 0, split}, {0, half, sub}}
	case PARTITION_VERT_B:
		return []partBlock{{0, 0, sub}, {0, half, split}, {half, half, split}}
	case PARTITION_HORZ_4:
		out := []partBlock{{0, 0, sub}, {q, 0, sub}, {2 * q, 0, sub}}
		if r+3*q < f.miRows {
			out = append(out, partBlock{3 * q, 0, sub})
		}
		return out
	}
	out := []partBlock{{0, 0, sub}, {0, q, sub}, {0, 2 * q, sub}}
	if c+3*q < f.miCols {
		out = append(out, partBlock{0, 3 * q, sub})
	}
	return out
}

// searchPartition decides the partition at (r, c) by rate and distortion,
// leaving the state as the best leaves it.
func (t *tileEnc) searchPartition(r, c, bSize, _ int) (float64, *partNode) {
	f := t.f
	if r >= f.miRows || c >= f.miCols {
		return 0, nil
	}
	half := int(num4x4BlocksWide[bSize]) >> 1
	hasRows, hasCols := r+half < f.miRows, c+half < f.miCols
	var cands []int
	switch {
	case bSize < BLOCK_8X8:
		cands = []int{PARTITION_NONE}
	case bSize == BLOCK_8X8 && dbgNo4x4:
		cands = []int{PARTITION_NONE}
	case dbgFixed > 0 && bSize > dbgFixed:
		if hasRows && hasCols {
			cands = []int{PARTITION_SPLIT}
		} else if hasCols {
			cands = []int{PARTITION_SPLIT}
		} else {
			cands = []int{PARTITION_SPLIT}
		}
	case dbgFixed > 0 && hasRows && hasCols:
		cands = []int{PARTITION_NONE}
	case hasRows && hasCols && dbgSquare:
		cands = []int{PARTITION_NONE, PARTITION_SPLIT}
	case hasRows && hasCols && (bSize == BLOCK_8X8 || dbgNoExt):
		cands = []int{PARTITION_NONE, PARTITION_HORZ, PARTITION_VERT, PARTITION_SPLIT}
	case hasRows && hasCols:
		cands = []int{PARTITION_NONE, PARTITION_HORZ, PARTITION_VERT, PARTITION_SPLIT,
			PARTITION_HORZ_A, PARTITION_HORZ_B, PARTITION_VERT_A, PARTITION_VERT_B, PARTITION_HORZ_4, PARTITION_VERT_4}
	case hasCols:
		cands = []int{PARTITION_HORZ, PARTITION_SPLIT}
	case hasRows:
		cands = []int{PARTITION_VERT, PARTITION_SPLIT}
	default:
		cands = []int{PARTITION_SPLIT}
	}
	d := depthOf(bSize)
	start, best := t.snap(d, 0), t.snap(d, 1)
	if len(cands) > 1 {
		t.save(start, r, c, bSize, true)
	}
	bestJ := math.Inf(1)
	var bestNode *partNode
	jOf := map[int]float64{}
	bestI, last := -1, -1
	for i, part := range cands {
		// Pruned: everything else after a skipped inter block that is
		// the whole partition; the extended partitions in a direction
		// splitting it that way did not help.
		if jn, ok := jOf[PARTITION_NONE]; ok && !dbgNoPrune {
			if bestNode != nil && bestNode.part == PARTITION_NONE && bestNode.blocks[0].isInter && bestNode.blocks[0].skip {
				break
			}
			switch part {
			case PARTITION_HORZ_A, PARTITION_HORZ_B, PARTITION_HORZ_4:
				if jh, ok := jOf[PARTITION_HORZ]; ok && jh >= jn {
					continue
				}
			case PARTITION_VERT_A, PARTITION_VERT_B, PARTITION_VERT_4:
				if jv, ok := jOf[PARTITION_VERT]; ok && jv >= jn {
					continue
				}
			}
		}
		if last >= 0 {
			t.restore(start, true)
		}
		last = i
		t.cost.n = 0
		t.writePartition(&t.cost, r, c, bSize, part)
		j := f.lambda * float64(t.cost.n) / costScale
		node := &partNode{part: part}
		sub := int(partitionSubsize[part][bSize])
		complete := true
		switch part {
		default:
			for _, pb := range t.partitionBlocks(part, r, c, bSize) {
				if j >= bestJ {
					complete = false
					break
				}
				bj, bd := t.searchBlock(r+pb.dr, c+pb.dc, pb.size)
				j += bj
				node.blocks = append(node.blocks, bd)
			}
		case PARTITION_SPLIT:
			for k, o := range [4][2]int{{0, 0}, {0, half}, {half, 0}, {half, half}} {
				if j >= bestJ {
					complete = false
					break
				}
				kj, kn := t.searchPartition(r+o[0], c+o[1], sub, 0)
				j += kj
				node.kids[k] = kn
			}
		}
		if complete {
			jOf[part] = j
		}
		if j < bestJ {
			bestJ, bestNode, bestI = j, node, i
			if i < len(cands)-1 {
				t.save(best, r, c, bSize, true)
			}
		}
	}
	if bestI != last {
		t.restore(best, true)
	}
	return bestJ, bestNode
}

// finalPartition codes a superblock's decisions for real.
func (t *tileEnc) finalPartition(r, c, bSize int, node *partNode) {
	f := t.f
	if r >= f.miRows || c >= f.miCols {
		return
	}
	half := int(num4x4BlocksWide[bSize]) >> 1
	t.writePartition(t.w, r, c, bSize, node.part)
	sub := int(partitionSubsize[node.part][bSize])
	switch node.part {
	default:
		for _, bd := range node.blocks {
			t.finalBlock(bd)
		}
	case PARTITION_SPLIT:
		for k, o := range [4][2]int{{0, 0}, {0, half}, {half, 0}, {half, half}} {
			t.finalPartition(r+o[0], c+o[1], sub, node.kids[k])
		}
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// pdf is a CDF's probability of symbol s.
func pdf(cdf []uint16, s int) int {
	if s == 0 {
		return int(cdf[0])
	}
	return int(cdf[s]) - int(cdf[s-1])
}

// splitOrHorz is split_or_horz's CDF, made from the partition's.
func splitOrHorz(cdf []uint16, bSize int) []uint16 {
	psum := pdf(cdf, PARTITION_VERT) + pdf(cdf, PARTITION_SPLIT) + pdf(cdf, PARTITION_HORZ_A) +
		pdf(cdf, PARTITION_VERT_A) + pdf(cdf, PARTITION_VERT_B)
	if bSize != BLOCK_128X128 {
		psum += pdf(cdf, PARTITION_VERT_4)
	}
	return []uint16{uint16(1<<15 - psum), 1 << 15, 0} //nolint:gosec // a probability
}

// splitOrVert is split_or_vert's.
func splitOrVert(cdf []uint16, bSize int) []uint16 {
	psum := pdf(cdf, PARTITION_HORZ) + pdf(cdf, PARTITION_SPLIT) + pdf(cdf, PARTITION_HORZ_A) +
		pdf(cdf, PARTITION_HORZ_B) + pdf(cdf, PARTITION_VERT_A)
	if bSize != BLOCK_128X128 {
		psum += pdf(cdf, PARTITION_HORZ_4)
	}
	return []uint16{uint16(1<<15 - psum), 1 << 15, 0} //nolint:gosec // a probability
}
