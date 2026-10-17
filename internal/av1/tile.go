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
	// lfTx is LoopfilterTxSizes: each plane's transform size by 4x4 block
	// of the plane, (miCols >> sub) wide.
	lfTx     [3][]uint8
	dcQ, acQ [3]int
	lossless bool
	lambda   float64 // distortion per bit
}

func (f *frameEnc) mi(r, c int) int { return r*f.miCols + c }

// tileEnc codes one tile.
type tileEnc struct {
	f                    *frameEnc
	rowStart, rowEnd     int // in mi
	colStart, colEnd     int
	cdf                  *cdfs
	w                    *symbolWriter
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
	snaps                [8][4]*snapshot // by depth, a few each
	sbStart              snapshot
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
	cflU, cflV       int // the CFL alphas
	filterMode       int // filter_intra_mode, or -1
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
	txDepth         int
	txTypes         []uint8 // the luma transform blocks', in order
}

// partNode is a partition's decisions.
type partNode struct {
	part   int
	blocks []*blockDec
	kids   [4]*partNode
}

func newTileEnc(f *frameEnc, rowStart, rowEnd, colStart, colEnd int, w *symbolWriter) *tileEnc {
	t := &tileEnc{f: f, rowStart: rowStart, rowEnd: rowEnd, colStart: colStart, colEnd: colEnd, cdf: newCDFs(f.fp.qIndex), w: w}
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
			t.save(&t.sbStart, r, c, BLOCK_64X64, false)
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
	mi         [6][]uint8
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
	}
	s.decoded = t.decoded
}

func (t *tileEnc) miArrays() [6][]uint8 {
	f := t.f
	return [6][]uint8{f.miSize, f.yMode, f.uvMode, f.skip, f.txSize, f.txType}
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
	case hasCols:
		cd.encode(b2i(part == PARTITION_SPLIT), splitOrHorz(cdf, bSize), 2)
	case hasRows:
		cd.encode(b2i(part == PARTITION_SPLIT), splitOrVert(cdf, bSize), 2)
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
	}
	return 3
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
	case bSize == BLOCK_8X8:
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
	case hasRows && hasCols:
		cands = []int{PARTITION_NONE, PARTITION_HORZ, PARTITION_VERT, PARTITION_SPLIT}
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
	bestLast := false
	for i, part := range cands {
		if i > 0 {
			t.restore(start, true)
		}
		t.cost.n = 0
		t.writePartition(&t.cost, r, c, bSize, part)
		j := f.lambda * float64(t.cost.n) / costScale
		node := &partNode{part: part}
		sub := int(partitionSubsize[part][bSize])
		switch part {
		case PARTITION_NONE:
			bj, bd := t.searchBlock(r, c, sub)
			j += bj
			node.blocks = []*blockDec{bd}
		case PARTITION_HORZ, PARTITION_VERT:
			r2, c2, has := r+half, c, hasRows
			if part == PARTITION_VERT {
				r2, c2, has = r, c+half, hasCols
			}
			bj, bd := t.searchBlock(r, c, sub)
			j += bj
			node.blocks = []*blockDec{bd}
			if has && j < bestJ {
				bj, bd = t.searchBlock(r2, c2, sub)
				j += bj
				node.blocks = append(node.blocks, bd)
			}
		case PARTITION_SPLIT:
			for k, o := range [4][2]int{{0, 0}, {0, half}, {half, 0}, {half, half}} {
				if j >= bestJ {
					break
				}
				kj, kn := t.searchPartition(r+o[0], c+o[1], sub, 0)
				j += kj
				node.kids[k] = kn
			}
		}
		if j < bestJ {
			bestJ, bestNode = j, node
			bestLast = i == len(cands)-1
			if !bestLast {
				t.save(best, r, c, bSize, true)
			}
		}
	}
	if !bestLast {
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
	case PARTITION_NONE, PARTITION_HORZ, PARTITION_VERT:
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
