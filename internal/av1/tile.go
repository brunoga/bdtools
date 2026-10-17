package av1

// A key frame's tiles coded: the partition of each superblock, each
// block's intra mode info and its transform blocks' coefficients, as
// decode_tile reads them (spec 5.11), with the contexts it derives; the
// reconstruction follows, exactly as a decoder makes it.

// frameEnc is the frame being coded, shared by its tiles.
type frameEnc struct {
	seq            *seqParams
	fp             *frameParams
	bitDepth       int
	miCols, miRows int
	// The source and the reconstruction, padded to whole superblocks
	// (the source by its edges).
	src, rec [3][]uint16
	stride   [3]int
	// Per 4x4 luma block (the mi grid, miCols wide).
	miSize, yMode, uvMode, skip, txSize []uint8
	txType                              []uint8 // TxTypes
	dcQ, acQ                            [3]int
	lossless                            bool
}

func (f *frameEnc) mi(r, c int) int { return r*f.miCols + c }

// tileEnc codes one tile.
type tileEnc struct {
	f                    *frameEnc
	rowStart, rowEnd     int // in mi
	colStart, colEnd     int
	cdf                  *cdfs
	w                    *symbolWriter
	aboveLevel, aboveDc  [3][]uint8 // by x4 of the plane
	leftLevel, leftDc    [3][]uint8 // by y4 of the plane
	decoded              [3][18][18]bool
	edges                intraEdges
	b                    blockState
	pred                 [64 * 64]uint16
	resid, coef, dq, inv [64 * 64]int32
	txs                  []txCoded
	lv                   [32 * 32]uint8
	levels               []int32
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
	skip             bool
}

// txCoded is a transform block's coefficients, kept for writing.
type txCoded struct {
	plane, x4, y4 int
	txSz, txType  int
	eob           int
	off           int // into levels
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
			t.partition(r, c, BLOCK_64X64)
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

// targetSize is the block size the first encoder codes with.
const targetSize = 6 // BLOCK_16X16

// partition is decode_partition: a partition decided, written, and its
// blocks coded.
func (t *tileEnc) partition(r, c, bSize int) {
	f := t.f
	if r >= f.miRows || c >= f.miCols {
		return
	}
	availU, availL := t.inside(r-1, c), t.inside(r, c-1)
	half := int(num4x4BlocksWide[bSize]) >> 1
	hasRows, hasCols := r+half < f.miRows, c+half < f.miCols
	part := PARTITION_NONE
	if bSize > targetSize || !hasRows || !hasCols {
		part = PARTITION_SPLIT
	}
	if bSize == BLOCK_8X8 && hasRows && hasCols {
		part = PARTITION_NONE
	}
	if bSize >= BLOCK_8X8 {
		bsl := int(miWidthLog2[bSize])
		ctx := 0
		if availU && int(miWidthLog2[f.miSize[f.mi(r-1, c)]]) < bsl {
			ctx++
		}
		if availL && int(miHeightLog2[f.miSize[f.mi(r, c-1)]]) < bsl {
			ctx += 2
		}
		var cdf []uint16
		switch bsl {
		case 1:
			cdf = t.cdf.partW8[ctx][:]
		case 2:
			cdf = t.cdf.partW16[ctx][:]
		case 3:
			cdf = t.cdf.partW32[ctx][:]
		case 4:
			cdf = t.cdf.partW64[ctx][:]
		default:
			cdf = t.cdf.partW128[ctx][:]
		}
		switch {
		case hasRows && hasCols:
			t.w.symbol(part, cdf)
		case hasCols:
			t.w.encode(b2i(part == PARTITION_SPLIT), splitOrHorz(cdf, bSize), 2)
		case hasRows:
			t.w.encode(b2i(part == PARTITION_SPLIT), splitOrVert(cdf, bSize), 2)
		}
	}
	sub := int(partitionSubsize[part][bSize])
	switch part {
	case PARTITION_NONE:
		t.block(r, c, sub)
	case PARTITION_SPLIT:
		t.partition(r, c, sub)
		t.partition(r, c+half, sub)
		t.partition(r+half, c, sub)
		t.partition(r+half, c+half, sub)
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

// block is decode_block for an intra block: its modes decided, its
// transform blocks predicted, transformed, quantized and reconstructed,
// then everything written.
func (t *tileEnc) block(r, c, size int) {
	f := t.f
	b := &t.b
	*b = blockState{r: r, c: c, size: size, bw4: int(num4x4BlocksWide[size]), bh4: int(num4x4BlocksHigh[size])}
	b.hasChroma = !(b.bh4 == 1 && r&1 == 0) && !(b.bw4 == 1 && c&1 == 0)
	b.availU, b.availL = t.inside(r-1, c), t.inside(r, c-1)
	b.availUC, b.availLC = b.availU, b.availL
	if b.hasChroma {
		if b.bh4 == 1 {
			b.availUC = t.inside(r-2, c)
		}
		if b.bw4 == 1 {
			b.availLC = t.inside(r, c-2)
		}
	} else {
		b.availUC, b.availLC = false, false
	}
	b.yMode = t.chooseMode(0)
	b.uvMode = DC_PRED
	if b.hasChroma {
		b.uvMode = t.chooseMode(1)
	}
	// The block's modes, before its residual (neighbours read them).
	for y := range b.bh4 {
		for x := range b.bw4 {
			if r+y < f.miRows && c+x < f.miCols {
				i := f.mi(r+y, c+x)
				f.yMode[i] = uint8(b.yMode) //nolint:gosec // a mode
				if b.hasChroma {
					f.uvMode[i] = uint8(b.uvMode) //nolint:gosec // a mode
				}
			}
		}
	}
	t.txs = t.txs[:0]
	t.levels = t.levels[:0]
	t.residual()
	b.skip = true
	for _, tx := range t.txs {
		b.skip = b.skip && tx.eob == 0
	}
	t.writeModeInfo()
	if b.skip {
		t.resetBlockContext()
	} else {
		for i := range t.txs {
			t.writeCoeffs(&t.txs[i])
		}
	}
	txSz := t.txSizeFor()
	for y := range b.bh4 {
		for x := range b.bw4 {
			if r+y < f.miRows && c+x < f.miCols {
				i := f.mi(r+y, c+x)
				f.skip[i] = uint8(b2i(b.skip)) //nolint:gosec // a flag
				f.miSize[i] = uint8(size)      //nolint:gosec // a size
				f.txSize[i] = uint8(txSz)      //nolint:gosec // a size
			}
		}
	}
}

// txSizeFor is the block's luma transform size (read_tx_size, the
// largest).
func (t *tileEnc) txSizeFor() int {
	if t.f.lossless {
		return TX_4X4
	}
	return int(maxTxSizeRect[t.b.size])
}

// planeTxSize is get_tx_size.
func (t *tileEnc) planeTxSize(plane int) int {
	tx := t.txSizeFor()
	if plane == 0 || t.f.lossless {
		return tx
	}
	uv := int(maxTxSizeRect[subsampledSize[t.b.size][1][1]])
	if txWidth[uv] == 64 || txHeight[uv] == 64 {
		switch {
		case txWidth[uv] == 16:
			return TX_16X32
		case txHeight[uv] == 16:
			return TX_32X16
		}
		return TX_32X32
	}
	return uv
}

// chooseMode picks a block's luma (plane 0) or chroma mode, by the sum of
// absolute differences of its prediction (a whole-block prediction, from
// the neighbours as they are).
func (t *tileEnc) chooseMode(plane int) int {
	f, b := t.f, &t.b
	best, bestCost := DC_PRED, int64(-1)
	sub := 0
	if plane > 0 {
		sub = 1
	}
	planeSz := int(subsampledSize[b.size][sub][sub])
	w, h := int(blockWidth[planeSz]), int(blockHeight[planeSz])
	if w > 64 || h > 64 {
		return DC_PRED
	}
	x0, y0 := (b.c*4)>>sub, (b.r*4)>>sub
	ib := t.intraBlockFor(plane, x0, y0, b.size, int(maxTxSizeRect[planeSz]), true)
	ib.log2W, ib.log2H = log2(w), log2(h)
	planes := []int{plane}
	if plane > 0 {
		planes = []int{1, 2}
	}
	for mode := DC_PRED; mode <= PAETH_PRED; mode++ {
		ib.mode = mode
		var cost int64
		for _, p := range planes {
			ib.plane = p
			t.edges.predictIntra(&ib, f.rec[p], f.stride[p], t.pred[:])
			src := f.src[p]
			for y := range h {
				row := src[(y0+y)*f.stride[p]+x0:]
				for x := range w {
					d := int64(row[x]) - int64(t.pred[y*w+x])
					if d < 0 {
						d = -d
					}
					cost += d
				}
			}
		}
		if bestCost < 0 || cost < bestCost {
			best, bestCost = mode, cost
		}
	}
	return best
}

func log2(v int) int {
	n := 0
	for 1<<uint(n) < v {
		n++
	}
	return n
}

// intraBlockFor describes the prediction of a transform block of a plane
// at (x, y) (plane samples), its size txSz, the block's first if first.
func (t *tileEnc) intraBlockFor(plane, x, y, size, txSz int, first bool) intraBlock {
	f, b := t.f, &t.b
	sub := 0
	if plane > 0 {
		sub = 1
	}
	row, col := (y<<uint(sub))>>2, (x<<uint(sub))>>2
	sbRow, sbCol := row&15, col&15
	stepX, stepY := int(txWidth[txSz])>>2, int(txHeight[txSz])>>2
	availL, availU := b.availL, b.availU
	if plane > 0 {
		availL, availU = b.availLC, b.availUC
	}
	bx, by := (b.c>>uint(sub))*4, (b.r>>uint(sub))*4
	ib := intraBlock{
		plane: plane, x: x, y: y,
		log2W: int(txWidthLog2[txSz]), log2H: int(txHeightLog2[txSz]),
		haveLeft:       availL || x > bx,
		haveAbove:      availU || y > by,
		haveAboveRight: t.decoded[plane][(sbRow>>uint(sub))-1+1][(sbCol>>uint(sub))+stepX+1],
		haveBelow:      t.decoded[plane][(sbRow>>uint(sub))+stepY+1][(sbCol>>uint(sub))-1+1],
		maxX:           ((f.miCols * 4) >> uint(sub)) - 1,
		maxY:           ((f.miRows * 4) >> uint(sub)) - 1,
		angleDelta:     0,
		filterIntra:    -1,
		edgeFilter:     f.seq.edgeFilter,
		bitDepth:       f.bitDepth,
	}
	_ = first
	if plane == 0 {
		ib.mode, ib.angleDelta = b.yMode, b.angleY
	} else {
		ib.mode, ib.angleDelta = b.uvMode, b.angleUV
	}
	if ib.edgeFilter {
		ib.smoothNeighbour = t.filterType(plane)
	}
	return ib
}

func isSmooth(m int) bool { return m == SMOOTH_PRED || m == SMOOTH_V_PRED || m == SMOOTH_H_PRED }

// filterType is get_filter_type.
func (t *tileEnc) filterType(plane int) bool {
	f, b := t.f, &t.b
	above, left := false, false
	au, al := b.availU, b.availL
	if plane > 0 {
		au, al = b.availUC, b.availLC
	}
	if au {
		r, c := b.r-1, b.c
		if plane > 0 {
			if c&1 == 0 {
				c++
			}
			if b.r&1 == 1 {
				r--
			}
		}
		above = t.smoothAt(r, c, plane)
	}
	if al {
		r, c := b.r, b.c-1
		if plane > 0 {
			if b.c&1 == 1 {
				c--
			}
			if r&1 == 0 {
				r++
			}
		}
		left = t.smoothAt(r, c, plane)
	}
	_ = f
	return above || left
}

func (t *tileEnc) smoothAt(r, c, plane int) bool {
	i := t.f.mi(min(r, t.f.miRows-1), min(c, t.f.miCols-1))
	if plane == 0 {
		return isSmooth(int(t.f.yMode[i]))
	}
	return isSmooth(int(t.f.uvMode[i]))
}

// residual is residual() for an intra block of at most 64x64: each
// plane's transform blocks in raster order, predicted, coded and
// reconstructed.
func (t *tileEnc) residual() {
	b := &t.b
	planes := 1
	if b.hasChroma {
		planes = 3
	}
	for plane := range planes {
		txSz := t.planeTxSize(plane)
		stepX, stepY := int(txWidth[txSz])>>2, int(txHeight[txSz])>>2
		sub := 0
		if plane > 0 {
			sub = 1
		}
		planeSz := int(subsampledSize[b.size][sub][sub])
		n4w, n4h := int(num4x4BlocksWide[planeSz]), int(num4x4BlocksHigh[planeSz])
		baseX, baseY := (b.c>>uint(sub))*4, (b.r>>uint(sub))*4
		for y := 0; y < n4h; y += stepY {
			for x := 0; x < n4w; x += stepX {
				t.transformBlock(plane, baseX+4*x, baseY+4*y, txSz)
			}
		}
	}
}

// transformBlock is transform_block: a transform block predicted, its
// residual transformed and quantized, and reconstructed.
func (t *tileEnc) transformBlock(plane, startX, startY, txSz int) {
	f := t.f
	sub := 0
	if plane > 0 {
		sub = 1
	}
	maxX, maxY := (f.miCols*4)>>uint(sub), (f.miRows*4)>>uint(sub)
	if startX >= maxX || startY >= maxY {
		return
	}
	row, col := (startY<<uint(sub))>>2, (startX<<uint(sub))>>2
	sbRow, sbCol := row&15, col&15
	stepX, stepY := int(txWidth[txSz])>>2, int(txHeight[txSz])>>2
	ib := t.intraBlockFor(plane, startX, startY, t.b.size, txSz, false)
	w, h := int(txWidth[txSz]), int(txHeight[txSz])
	stride := f.stride[plane]
	t.edges.predictIntra(&ib, f.rec[plane], stride, t.pred[:])
	rec := f.rec[plane][startY*stride+startX:]
	src := f.src[plane][startY*stride+startX:]
	for y := range h {
		copy(rec[y*stride:y*stride+w], t.pred[y*w:(y+1)*w])
		for x := range w {
			t.resid[y*w+x] = int32(src[y*stride+x]) - int32(t.pred[y*w+x])
		}
	}
	txType := t.chooseTxType(plane, txSz)
	tc := txCoded{plane: plane, x4: startX >> 2, y4: startY >> 2, txSz: txSz, txType: txType, off: len(t.levels)}
	forwardTransform(t.resid[:w*h], txSz, txType, f.bitDepth, f.lossless, t.coef[:])
	tc.eob = t.quantize(plane, txSz, txType)
	if tc.eob > 0 {
		q := t.levels[tc.off:]
		dequantize(q, txSz, f.dcQ[plane], f.acQ[plane], f.bitDepth, t.dq[:])
		inverseTransform(t.dq[:], txSz, txType, f.bitDepth, f.lossless, t.inv[:])
		reconstructAdd(rec, stride, t.inv[:], txSz, txType, f.bitDepth)
	}
	if plane == 0 {
		tt := uint8(txType) //nolint:gosec // a type
		if tc.eob == 0 {
			tt = DCT_DCT
		}
		for y := range stepY {
			for x := range stepX {
				if row+y < f.miRows && col+x < f.miCols {
					f.txType[f.mi(row+y, col+x)] = tt
				}
			}
		}
	}
	t.txs = append(t.txs, tc)
	for i := range stepY {
		for j := range stepX {
			t.decoded[plane][(sbRow>>uint(sub))+i+1][(sbCol>>uint(sub))+j+1] = true
		}
	}
}

// txSet is get_tx_set for an intra block.
func (t *tileEnc) txSet(txSz int) int {
	sq, up := int(txSizeSqr[txSz]), int(txSizeSqrUp[txSz])
	switch {
	case up > TX_32X32 || up == TX_32X32:
		return TX_SET_DCTONLY
	case t.f.fp.reducedTx || sq == TX_16X16:
		return TX_SET_INTRA_2
	}
	return TX_SET_INTRA_1
}

// chooseTxType is the transform type: the luma's chosen (DCT for now),
// the chroma's compute_tx_type's.
func (t *tileEnc) chooseTxType(plane, txSz int) int {
	if t.f.lossless || int(txSizeSqrUp[txSz]) > TX_32X32 {
		return DCT_DCT
	}
	if plane == 0 {
		return DCT_DCT
	}
	tt := int(modeToTxfm[t.b.uvMode])
	if txTypeInSetIntra[t.txSet(txSz)][tt] == 0 {
		return DCT_DCT
	}
	return tt
}

// dqDenom is the dequantization's divisor by transform size.
func dqDenom(txSz int) int {
	switch txSz {
	case TX_32X32, TX_16X32, TX_32X16, TX_16X64, TX_64X16:
		return 2
	case TX_64X64, TX_32X64, TX_64X32:
		return 4
	}
	return 1
}

// quantize quantizes t.coef (the spec's layout, min(32, w) wide) into
// levels appended to t.levels, giving the end of block (in scan order).
func (t *tileEnc) quantize(plane, txSz, txType int) int {
	f := t.f
	tw, th := min(32, int(txWidth[txSz])), min(32, int(txHeight[txSz]))
	n := tw * th
	off := len(t.levels)
	t.levels = append(t.levels, make([]int32, n)...)
	q := t.levels[off:]
	den := int64(dqDenom(txSz))
	for i := range n {
		qs := int64(f.acQ[plane])
		rnd := qs * 3 / 8
		if i == 0 {
			qs = int64(f.dcQ[plane])
			rnd = qs / 2
		}
		c := int64(t.coef[i])
		neg := c < 0
		if neg {
			c = -c
		}
		l := (c*den + rnd) / qs
		l = min(l, 1<<18)
		if neg {
			l = -l
		}
		q[i] = int32(l) //nolint:gosec // clamped
	}
	scan := scanFor(txSz, txType)
	eob := 0
	for k, pos := range scan[:n] {
		if q[pos] != 0 {
			eob = k + 1
		}
	}
	return eob
}

// writeModeInfo is intra_frame_mode_info.
func (t *tileEnc) writeModeInfo() {
	f, b := t.f, &t.b
	ctx := 0
	if b.availU {
		ctx += int(f.skip[f.mi(b.r-1, b.c)])
	}
	if b.availL {
		ctx += int(f.skip[f.mi(b.r, b.c-1)])
	}
	t.w.symbol(b2i(b.skip), t.cdf.skip[ctx][:])
	am, lm := DC_PRED, DC_PRED
	if b.availU {
		am = int(f.yMode[f.mi(b.r-1, b.c)])
	}
	if b.availL {
		lm = int(f.yMode[f.mi(b.r, b.c-1)])
	}
	t.w.symbol(b.yMode, t.cdf.yMode[intraModeContext[am]][intraModeContext[lm]][:])
	if b.size >= BLOCK_8X8 && isDirectional(b.yMode) {
		t.w.symbol(b.angleY+MAX_ANGLE_DELTA, t.cdf.angleDelta[b.yMode-V_PRED][:])
	}
	if b.hasChroma {
		if !f.lossless && max(blockWidth[b.size], blockHeight[b.size]) <= 32 {
			t.w.symbol(b.uvMode, t.cdf.uvCfl[b.yMode][:])
		} else {
			t.w.symbol(b.uvMode, t.cdf.uvNoCfl[b.yMode][:])
		}
		if b.size >= BLOCK_8X8 && isDirectional(b.uvMode) {
			t.w.symbol(b.angleUV+MAX_ANGLE_DELTA, t.cdf.angleDelta[b.uvMode-V_PRED][:])
		}
	}
}

// resetBlockContext is reset_block_context.
func (t *tileEnc) resetBlockContext() {
	b := &t.b
	planes := 1
	if b.hasChroma {
		planes = 3
	}
	for p := range planes {
		sub := 0
		if p > 0 {
			sub = 1
		}
		for i := b.c >> uint(sub); i < (b.c+b.bw4)>>uint(sub); i++ {
			t.aboveLevel[p][i], t.aboveDc[p][i] = 0, 0
		}
		for i := b.r >> uint(sub); i < (b.r+b.bh4)>>uint(sub); i++ {
			t.leftLevel[p][i], t.leftDc[p][i] = 0, 0
		}
	}
}
