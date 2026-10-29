package av1

// A block decided and coded: its luma mode (with its angle), its
// transform depth and the transform blocks' types, then its chroma mode,
// each by rate and distortion; the modes first narrowed down by the
// prediction's SATD.

import (
	"cmp"
	"math"
	"math/bits"
	"slices"
	"sort"
)

// Debugging switches.
var dbgDCTOnly, dbgNoAngles, dbgNoDepth, dbgSquare, dbgNoRDOQ bool
var dbgFixed int
var dbgNoPruneSkip, dbgNoPruneExt, dbgNoPruneIntra bool
var dbgNo4x4, dbgNoExt, dbgNoInter, dbgNoPrune, dbgFlatQ, dbgNoCompound, dbgNoSkipMode, dbgNoVarTx bool

// lumaCands is how many luma modes are coded in full.
var lumaCands = 3

// intraSkip skips an inter frame's block's intra search when its best
// luma estimate is more than intraSkip times the best inter one's (0
// never). (On the two 120-picture clips: 1.5 1.05 times faster for
// 0.2% BD-rate, 1.2 1.06 for 0.1%, 1.0 1.08 for 0.2%, 0.8 1.10 for
// 2.9%.)
var intraSkip = 1.2

// chromaCands is how many chroma modes are.
var chromaCands = 2

// setupBlock is decode_block's start: the block's availability.
func (t *tileEnc) setupBlock(r, c, size int) {
	b := &t.b
	*b = blockState{r: r, c: c, size: size, bw4: int(num4x4BlocksWide[size]), bh4: int(num4x4BlocksHigh[size]), filterMode: -1, refFrame2: NONE}
	b.hasChroma = (b.bh4 != 1 || r&1 != 0) && (b.bw4 != 1 || c&1 != 0)
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
}

// setModes records the block's modes in the mi grid.
func (t *tileEnc) setModes() {
	f, b := t.f, &t.b
	for y := range b.bh4 {
		for x := range b.bw4 {
			if b.r+y < f.miRows && b.c+x < f.miCols {
				m := &f.mis[f.mi(b.r+y, b.c+x)]
				m.yMode = uint8(b.yMode) //nolint:gosec // a mode
				if b.hasChroma && !b.isInter {
					m.uvMode = uint8(b.uvMode) //nolint:gosec // a mode
				}
				if b.isInter {
					m.refFrames[0], m.refFrames[1] = int8(b.refFrame), int8(b.refFrame2) //nolint:gosec // references
					if b.ii.on {
						m.refFrames[1] = INTRA_FRAME
					}
					m.mvs[0], m.mvs[1], m.isInter = b.mv, b.mv2, 1
					m.interp = uint8(b.interp) //nolint:gosec // a filter
					m.compIdx = uint8(b2i(!b.compDist || b.refFrame2 <= INTRA_FRAME))
					m.compGroup = uint8(b2i(b.masked && b.refFrame2 > INTRA_FRAME))
				} else {
					m.refFrames[0], m.refFrames[1] = INTRA_FRAME, NONE
					m.compIdx, m.compGroup = 1, 0
					m.mvs[0], m.isInter = mv{}, 0
				}
			}
		}
	}
}

// setInfo records the rest of the block's mode info.
func (t *tileEnc) setInfo() {
	f, b := t.f, &t.b
	for y := range b.bh4 {
		for x := range b.bw4 {
			if b.r+y < f.miRows && b.c+x < f.miCols {
				m := &f.mis[f.mi(b.r+y, b.c+x)]
				m.skip = uint8(b2i(b.skip))         //nolint:gosec // a flag
				m.skipMode = uint8(b2i(b.skipMode)) //nolint:gosec // a flag
				m.miSize = uint8(b.size)            //nolint:gosec // a size
				if !b.isInter || b.skip {
					m.txSize = uint8(b.txSz) //nolint:gosec // a size (else the transform tree's)
				}
			}
		}
	}
}

type modeCand struct {
	mode, angle int
	cost        float64
	cflU, cflV  int
	filterMode  int
}

// searchBlock decides a block at (r, c), intra or (in inter frames)
// inter, leaving the state as coding it leaves it.
func (t *tileEnc) searchBlock(r, c, size int) (float64, *blockDec) {
	if t.f.fp.key || dbgNoInter {
		return t.searchIntraBlock(r, c, size)
	}
	d := 5 + depthOf(min(size, BLOCK_64X64))
	start, intra := t.snap(d, 2), t.snap(d, 3)
	t.save(start, r, c, size, 3)
	// inter first: when it codes no residual, intra is not tried
	jn, dn := t.searchInterBlock(r, c, size)
	if dn != nil && (dbgForceInter || dn.skip && !dbgNoPrune && !dbgNoPruneIntra) {
		return jn, dn
	}
	t.save(intra, r, c, size, 3) // the inter's
	t.restore(start, 3)
	if dn != nil {
		t.intraLimit = t.f.tools.intraSkip * t.interEst
	}
	ji, di := t.searchIntraBlock(r, c, size)
	if dn != nil && jn < ji {
		t.restore(intra, 3)
		return jn, dn
	}
	return ji, di
}

// searchIntraBlock decides an intra block at (r, c).
func (t *tileEnc) searchIntraBlock(r, c, size int) (float64, *blockDec) {
	f := t.f
	t.setupBlock(r, c, size)
	b := &t.b
	d := 5 + depthOf(min(size, BLOCK_64X64))
	start, best := t.snap(d, 0), t.snap(d, 1)
	t.save(start, r, c, size, 3)
	dec := &blockDec{r: r, c: c, size: size}
	// luma (the candidates changing its reconstruction alone)
	cands := t.lumaCandidates()
	if lim := t.intraLimit; lim > 0 {
		t.intraLimit = 0
		if cands[0].cost > lim {
			return math.Inf(1), nil
		}
	}
	maxDepth := 0
	if !f.lossless && size > BLOCK_4X4 && f.fp.txSelect && !dbgNoDepth {
		maxDepth = min(int(maxTxDepth[size]), MAX_TX_DEPTH)
	}
	bestJ, bestZero := math.Inf(1), false
	var bestLuma blockDec
	n := 0
	try := func(d blockDec) {
		if n > 0 {
			t.restore(start, 1)
			t.setupBlock(r, c, size)
		}
		n++
		j, zero := t.codeLuma(&d, true)
		if j < bestJ {
			bestJ, bestZero, bestLuma = j, zero, d
			t.save(best, r, c, size, 1)
		}
	}
	// Every candidate at every depth with DCT alone, then the best few
	// with their transform types searched.
	type ranked struct {
		d blockDec
		j float64
	}
	var all []ranked
	t.dctOnly = true
	for _, cand := range cands {
		for depth := 0; depth <= maxDepth; depth++ {
			if n > 0 {
				t.restore(start, 1)
				t.setupBlock(r, c, size)
			}
			n++
			d := blockDec{r: r, c: c, size: size, yMode: cand.mode, angleY: cand.angle, filterMode: cand.filterMode, txDepth: depth}
			j, _ := t.codeLuma(&d, true)
			all = append(all, ranked{d, j})
		}
	}
	t.dctOnly = false
	sort.SliceStable(all, func(i, j int) bool { return all[i].j < all[j].j })
	for _, a := range all[:min(typeSearches, len(all))] {
		a.d.txTypes = nil
		try(a.d)
	}
	t.restore(best, 1)
	t.setupBlock(r, c, size)
	*dec = bestLuma
	b.yMode, b.angleY, b.filterMode, b.txSz = dec.yMode, dec.angleY, dec.filterMode, t.lumaTxSize(dec.txDepth)
	t.setMaxLuma()
	j := bestJ
	allZero := bestZero
	if b.hasChroma {
		t.save(start, r, c, size, 3)
		ucands := t.chromaCandidates()
		cj, czero := math.Inf(1), false
		var cb modeCand
		for i, cand := range ucands {
			if i > 0 {
				t.restore(start, 3)
			}
			b.uvMode, b.angleUV, b.cflU, b.cflV = cand.mode, cand.angle, cand.cflU, cand.cflV
			jj, zero := t.codeChroma(true)
			if jj < cj {
				cj, czero, cb = jj, zero, cand
				if i < len(ucands)-1 {
					t.save(best, r, c, size, 3)
				}
			}
		}
		if cb != ucands[len(ucands)-1] {
			t.restore(best, 3)
		}
		dec.uvMode, dec.angleUV, dec.cflU, dec.cflV = cb.mode, cb.angle, cb.cflU, cb.cflV
		b.uvMode, b.angleUV, b.cflU, b.cflV = cb.mode, cb.angle, cb.cflU, cb.cflV
		j += cj
		allZero = allZero && czero
	}
	b.skip = allZero
	t.cost.n = 0
	t.writeSkip(&t.cost)
	j += t.lambda * float64(t.cost.n) / costScale
	if b.skip {
		t.resetBlockContext()
	}
	t.setModes()
	t.setInfo()
	return j, dec
}

// finalBlock codes a decided block for real: its transform blocks again
// (as decided), then its mode info and coefficients written.
func (t *tileEnc) finalBlock(dec *blockDec) {
	if dec.isInter {
		t.finalInterBlock(dec)
		return
	}
	t.setupBlock(dec.r, dec.c, dec.size)
	b := &t.b
	var ctx snapshot
	t.save(&ctx, dec.r, dec.c, dec.size, 0)
	t.txs, t.levels = t.txs[:0], t.levels[:0]
	// decided as the search decided, with its rates
	t.cdf = t.sbCDF
	_, zero := t.codeLuma(dec, false)
	b.uvMode, b.angleUV, b.cflU, b.cflV = dec.uvMode, dec.angleUV, dec.cflU, dec.cflV
	if b.hasChroma {
		_, czero := t.codeChroma(false)
		zero = zero && czero
	}
	b.skip = zero
	ctx.decoded = t.decoded // the block's, which a decoder has after it too
	t.restore(&ctx, 0)
	t.cdf = t.live
	t.writeSkip(t.w)
	if f := t.f; f.seq.cdef && !b.skip && !f.lossless && !t.cdefDone {
		// read_cdef: the superblock's first block with coefficients
		i := (b.r>>4)*((f.miCols+15)>>4) + b.c>>4
		t.w.cdef(i)
		f.cdefIdx[i] = 0
		t.cdefDone = true
	}
	t.writeDeltaQ(t.w)
	t.writeModes(t.w, dec.txDepth)
	if b.skip {
		t.resetBlockContext()
	} else {
		for i := range t.txs {
			t.writeCoeffs(t.w, &t.txs[i], true)
		}
	}
	t.setInfo()
}

// setMaxLuma sets MaxLumaW and MaxLumaH as the block's luma transform
// blocks leave them: the last inside the frame's end.
func (t *tileEnc) setMaxLuma() {
	f, b := t.f, &t.b
	tw, th := int(txWidth[b.txSz]), int(txHeight[b.txSz])
	for y := 0; y < b.bh4*4; y += th {
		for x := 0; x < b.bw4*4; x += tw {
			if sx, sy := b.c*4+x, b.r*4+y; sx < f.miCols*4 && sy < f.miRows*4 {
				b.maxLumaW, b.maxLumaH = sx+tw, sy+th
			}
		}
	}
}

// lumaTxSize is the luma transform size at a depth.
func (t *tileEnc) lumaTxSize(depth int) int {
	if t.f.lossless {
		return TX_4X4
	}
	tx := int(maxTxSizeRect[t.b.size])
	for range depth {
		tx = int(splitTxSize[tx])
	}
	return tx
}

// codeLuma codes the block's luma as dec says (its transform types
// decided as it goes, when search), giving its cost (the modes' and the
// coefficients' rates and the distortion) and whether every coefficient
// is zero.
func (t *tileEnc) codeLuma(dec *blockDec, search bool) (float64, bool) {
	b := &t.b
	b.yMode, b.angleY, b.filterMode = dec.yMode, dec.angleY, dec.filterMode
	b.txSz = t.lumaTxSize(dec.txDepth)
	t.setModes()
	t.cost.n = 0
	t.writeIsInter(&t.cost, false)
	t.writeYMode(&t.cost)
	t.writeFilterIntra(&t.cost)
	t.writeTxDepth(&t.cost, dec.txDepth)
	j := t.lambda * float64(t.cost.n) / costScale
	if search {
		dec.txTypes = dec.txTypes[:0]
	}
	txSz := b.txSz
	stepX, stepY := int(txWidth[txSz])>>2, int(txHeight[txSz])>>2
	n4w, n4h := b.bw4, b.bh4
	zero := true
	k := 0
	for y := 0; y < n4h; y += stepY {
		for x := 0; x < n4w; x += stepX {
			tt := -1
			if !search && k < len(dec.txTypes) {
				tt = int(dec.txTypes[k])
			}
			jj, eob, chosen, done := t.transformBlock(0, b.c*4+4*x, b.r*4+4*y, txSz, tt)
			if !done {
				continue
			}
			k++
			if search {
				dec.txTypes = append(dec.txTypes, uint8(chosen)) //nolint:gosec // a type
			}
			j += jj
			zero = zero && eob == 0
		}
	}
	return j, zero
}

// codeChroma codes the block's chroma with its chroma mode.
func (t *tileEnc) codeChroma(search bool) (float64, bool) {
	b := &t.b
	t.setModes()
	t.cost.n = 0
	t.writeUVMode(&t.cost)
	j := t.lambda * float64(t.cost.n) / costScale
	_ = search
	zero := true
	txSz := t.planeTxSize(1)
	stepX, stepY := int(txWidth[txSz])>>2, int(txHeight[txSz])>>2
	planeSz := int(subsampledSize[b.size][1][1])
	n4w, n4h := int(num4x4BlocksWide[planeSz]), int(num4x4BlocksHigh[planeSz])
	baseX, baseY := (b.c>>1)*4, (b.r>>1)*4
	for plane := 1; plane <= 2; plane++ {
		for y := 0; y < n4h; y += stepY {
			for x := 0; x < n4w; x += stepX {
				jj, eob, _, done := t.transformBlock(plane, baseX+4*x, baseY+4*y, txSz, -1)
				if done {
					j += jj
					zero = zero && eob == 0
				}
			}
		}
	}
	return j, zero
}

// planeTxSize is get_tx_size.
func (t *tileEnc) planeTxSize(plane int) int {
	if plane == 0 {
		return t.b.txSz
	}
	if t.f.lossless {
		return TX_4X4
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

// intraBlockFor describes the prediction of a transform block of a plane
// at (x, y) (plane samples) of size txSz.
func (t *tileEnc) intraBlockFor(plane, x, y, txSz int) intraBlock {
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
		filterIntra:    b.filterMode,
		edgeFilter:     f.seq.edgeFilter,
		bitDepth:       f.bitDepth,
	}
	if plane == 0 {
		ib.mode, ib.angleDelta = b.yMode, b.angleY
	} else {
		ib.mode, ib.angleDelta = b.uvMode, b.angleUV
		if ib.mode == UV_CFL_PRED {
			ib.mode = DC_PRED
		}
	}
	if ib.edgeFilter {
		ib.smoothNeighbour = t.filterType(plane)
	}
	return ib
}

func isSmooth(m int) bool { return m == SMOOTH_PRED || m == SMOOTH_V_PRED || m == SMOOTH_H_PRED }

// filterType is get_filter_type.
func (t *tileEnc) filterType(plane int) bool {
	b := &t.b
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
	return above || left
}

func (t *tileEnc) smoothAt(r, c, plane int) bool {
	i := t.f.mi(min(r, t.f.miRows-1), min(c, t.f.miCols-1))
	if plane > 0 && t.f.mis[i].refFrames[0] > INTRA_FRAME {
		return false
	}
	if plane == 0 {
		return isSmooth(int(t.f.mis[i].yMode))
	}
	return isSmooth(int(t.f.mis[i].uvMode))
}

// typeSearches is how many of a block's luma candidates (mode and
// depth) have their transform types searched.
var typeSearches = 2

// angleModes is how many of the best directional modes (at their
// nominal angle) have their other angles tried.
var angleModes = 2

// lumaEstimate is a luma mode's estimated cost: its prediction's SATD
// (each transform block of the largest size predicted from the
// neighbours as they are) and its rate.
func (t *tileEnc) lumaEstimate(mode, angle, filterMode int) float64 {
	f, b := t.f, &t.b
	w, h := int(blockWidth[b.size]), int(blockHeight[b.size])
	txSz := int(maxTxSizeRect[b.size])
	tw, th := int(txWidth[txSz]), int(txHeight[txSz])
	b.yMode, b.angleY, b.filterMode = mode, angle, filterMode
	var satd int64
	for y := 0; y < h; y += th {
		for x := 0; x < w; x += tw {
			ib := t.intraBlockFor(0, b.c*4+x, b.r*4+y, txSz)
			t.edges.predictIntra(&ib, f.rec[0], f.stride[0], t.pred[:])
			satd += t.satd(0, b.c*4+x, b.r*4+y, tw, th, t.pred[:])
		}
	}
	t.cost.n = 0
	t.writeYMode(&t.cost)
	t.writeFilterIntra(&t.cost)
	b.filterMode = -1
	return float64(satd) + math.Sqrt(t.lambda)*float64(t.cost.n)/costScale
}

// chromaEstimate is a chroma mode's (not CFL).
func (t *tileEnc) chromaEstimate(mode, angle int) float64 {
	f, b := t.f, &t.b
	txSz := t.planeTxSize(1)
	planeSz := int(subsampledSize[b.size][1][1])
	w, h := int(blockWidth[planeSz]), int(blockHeight[planeSz])
	tw, th := int(txWidth[txSz]), int(txHeight[txSz])
	b.uvMode, b.angleUV = mode, angle
	var satd int64
	for p := 1; p <= 2; p++ {
		for y := 0; y < h; y += th {
			for x := 0; x < w; x += tw {
				ib := t.intraBlockFor(p, (b.c>>1)*4+x, (b.r>>1)*4+y, txSz)
				t.edges.predictIntra(&ib, f.rec[p], f.stride[p], t.pred[:])
				satd += t.satd(p, (b.c>>1)*4+x, (b.r>>1)*4+y, tw, th, t.pred[:])
			}
		}
	}
	t.cost.n = 0
	t.writeUVMode(&t.cost)
	b.angleUV = 0
	return float64(satd) + math.Sqrt(t.lambda)*float64(t.cost.n)/costScale
}

// withAngles adds the other angles of the best directional candidates.
func withAngles(cands []modeCand, n int, est func(mode, angle int) float64) []modeCand {
	slices.SortStableFunc(cands, func(a, b modeCand) int { return cmp.Compare(a.cost, b.cost) })
	var dirs []modeCand
	for _, c := range cands {
		if isDirectional(c.mode) && c.filterMode < 0 && len(dirs) < n {
			dirs = append(dirs, c)
		}
	}
	for _, d := range dirs {
		for a := -MAX_ANGLE_DELTA; a <= MAX_ANGLE_DELTA; a++ {
			if a != 0 {
				cands = append(cands, modeCand{mode: d.mode, angle: a, filterMode: -1, cost: est(d.mode, a)})
			}
		}
	}
	slices.SortStableFunc(cands, func(a, b modeCand) int { return cmp.Compare(a.cost, b.cost) })
	return cands
}

// lumaCandidates are the luma modes worth coding in full, ranked by
// their estimates.
func (t *tileEnc) lumaCandidates() []modeCand {
	b := &t.b
	var cands []modeCand
	for mode := DC_PRED; mode <= PAETH_PRED; mode++ {
		cands = append(cands, modeCand{mode: mode, filterMode: -1, cost: t.lumaEstimate(mode, 0, -1)})
	}
	if t.filterIntraAllowed() {
		for fm := range INTRA_FILTER_MODES {
			cands = append(cands, modeCand{mode: DC_PRED, filterMode: fm, cost: t.lumaEstimate(DC_PRED, 0, fm)})
		}
	}
	if b.size >= BLOCK_8X8 && !dbgNoAngles {
		cands = withAngles(cands, angleModes, func(m, a int) float64 { return t.lumaEstimate(m, a, -1) })
	} else {
		slices.SortStableFunc(cands, func(a, b modeCand) int { return cmp.Compare(a.cost, b.cost) })
	}
	return cands[:min(lumaCands, len(cands))]
}

// chromaCandidates are the chroma modes worth coding in full.
func (t *tileEnc) chromaCandidates() []modeCand {
	f, b := t.f, &t.b
	var cands []modeCand
	for mode := DC_PRED; mode <= PAETH_PRED; mode++ {
		cands = append(cands, modeCand{mode: mode, filterMode: -1, cost: t.chromaEstimate(mode, 0)})
	}
	if b.size >= BLOCK_8X8 && !dbgNoAngles {
		cands = withAngles(cands, 1, t.chromaEstimate)
	}
	if t.cflAllowed() {
		txSz := t.planeTxSize(1)
		tw, th := int(txWidth[txSz]), int(txHeight[txSz])
		au, av := t.cflAlphas()
		if au != 0 || av != 0 {
			b.uvMode, b.cflU, b.cflV = UV_CFL_PRED, au, av
			x0, y0 := (b.c>>1)*4, (b.r>>1)*4
			avg := t.cflLuma(x0, y0, txSz)
			var satd int64
			for p := 1; p <= 2; p++ {
				ib := t.intraBlockFor(p, x0, y0, txSz)
				t.edges.predictIntra(&ib, f.rec[p], f.stride[p], t.pred[:])
				alpha := au
				if p == 2 {
					alpha = av
				}
				t.cflApply(t.pred[:], tw, th, avg, alpha)
				satd += t.satd(p, x0, y0, tw, th, t.pred[:])
			}
			t.cost.n = 0
			t.writeUVMode(&t.cost)
			cands = append(cands, modeCand{mode: UV_CFL_PRED, filterMode: -1, cost: float64(satd) + math.Sqrt(t.lambda)*float64(t.cost.n)/costScale, cflU: au, cflV: av})
		}
	}
	slices.SortStableFunc(cands, func(a, b modeCand) int { return cmp.Compare(a.cost, b.cost) })
	return cands[:min(chromaCands, len(cands))]
}

// satd is the sum of the 4x4 Hadamard transforms' magnitudes of a
// prediction's difference from the source.
func (t *tileEnc) satd(plane, x0, y0, w, h int, pred []uint16) int64 {
	f := t.f
	st := f.stride[plane]
	src := f.src[plane]
	if w&7 == 0 && h&7 == 0 {
		return satd8s(src[y0*st+x0:], st, pred, w, w, h)
	}
	var sum int64
	var d [16]int32
	for by := 0; by < h; by += 4 {
		for bx := 0; bx < w; bx += 4 {
			for i := range 4 {
				srow := src[(y0+by+i)*st+x0+bx:]
				prow := pred[(by+i)*w+bx:]
				for j := range 4 {
					d[i*4+j] = int32(srow[j]) - int32(prow[j])
				}
			}
			for i := range 4 {
				a0, a1, a2, a3 := d[i*4]+d[i*4+1], d[i*4]-d[i*4+1], d[i*4+2]+d[i*4+3], d[i*4+2]-d[i*4+3]
				d[i*4], d[i*4+1], d[i*4+2], d[i*4+3] = a0+a2, a1+a3, a0-a2, a1-a3
			}
			for j := range 4 {
				a0, a1, a2, a3 := d[j]+d[4+j], d[j]-d[4+j], d[8+j]+d[12+j], d[8+j]-d[12+j]
				d[j], d[4+j], d[8+j], d[12+j] = a0+a2, a1+a3, a0-a2, a1-a3
			}
			for _, v := range d {
				if v < 0 {
					v = -v
				}
				sum += int64(v)
			}
		}
	}
	return sum / 2
}

// transformBlock is transform_block: a transform block predicted into
// the reconstruction, its residual transformed, quantized and
// reconstructed, with the type given (or, when txType is -1 for luma,
// the best of its set), its coefficients' rate counted and the contexts
// updated. It gives the block's cost, its end of block, the type and
// whether there was a block (one outside the frame is not).
func (t *tileEnc) transformBlock(plane, startX, startY, txSz, txType int) (float64, int, int, bool) {
	f := t.f
	sub := 0
	if plane > 0 {
		sub = 1
	}
	maxX, maxY := (f.miCols*4)>>uint(sub), (f.miRows*4)>>uint(sub)
	if startX >= maxX || startY >= maxY {
		return 0, 0, 0, false
	}
	row, col := (startY<<uint(sub))>>2, (startX<<uint(sub))>>2
	sbRow, sbCol := row&15, col&15
	stepX, stepY := int(txWidth[txSz])>>2, int(txHeight[txSz])>>2
	w, h := int(txWidth[txSz]), int(txHeight[txSz])
	stride := f.stride[plane]
	if t.b.isInter {
		// the block's prediction, made beforehand
		pw := t.interStride(plane)
		ox, oy := startX-(t.b.c>>uint(sub))*4, startY-(t.b.r>>uint(sub))*4
		for y := range h {
			copy(t.pred[y*w:(y+1)*w], t.interPred[plane][(oy+y)*pw+ox:])
		}
	} else {
		ib := t.intraBlockFor(plane, startX, startY, txSz)
		t.edges.predictIntra(&ib, f.rec[plane], stride, t.pred[:])
	}
	if plane > 0 && !t.b.isInter && t.b.uvMode == UV_CFL_PRED {
		alpha := t.b.cflU
		if plane == 2 {
			alpha = t.b.cflV
		}
		t.cflApply(t.pred[:], w, h, t.cflLuma(startX, startY, txSz), alpha)
	}
	if plane == 0 {
		t.b.maxLumaW, t.b.maxLumaH = startX+stepX*4, startY+stepY*4
	}
	src := f.src[plane][startY*stride+startX:]
	for y := range h {
		for x := range w {
			t.resid[y*w+x] = int32(src[y*stride+x]) - int32(t.pred[y*w+x])
		}
	}
	// the visible part, for the distortion
	vw, vh := w, h
	pw, ph := f.width, f.height
	if plane > 0 {
		pw, ph = (pw+1)>>1, (ph+1)>>1
	}
	vw, vh = min(vw, pw-startX), min(vh, ph-startY)
	t.txX4, t.txY4 = startX>>2, startY>>2
	types := []int{txType}
	if txType < 0 {
		types = t.txTypes(plane, txSz)
		if dbgDCTOnly || t.dctOnly {
			types = types[:1]
		}
	}
	t.txCoefOK = 0
	if k := f.tools.txCands; k > 0 && len(types) > k && !f.lossless {
		types = t.rankTxTypes(plane, txSz, types, k)
	}
	zeroStop := pruneTxZero && t.b.isInter && len(types) > 1 && types[0] == DCT_DCT
	tc := txCoded{plane: plane, x4: startX >> 2, y4: startY >> 2, txSz: txSz}
	bestJ := math.Inf(1)
	var best txCoded
	bestOff := -1
	for _, tt := range types {
		tc.txType = tt
		tc.off = len(t.levels)
		if t.txCoefOK>>uint(tt)&1 != 0 {
			copy(t.coef[:], t.txCoef[tt][:min(w, 32)*min(h, 32)])
		} else {
			forwardTransform(t.resid[:w*h], txSz, tt, f.bitDepth, f.lossless, t.coef[:])
		}
		tc.eob = t.quantize(plane, txSz, tt)
		copy(t.trial[:w*h], t.pred[:w*h])
		if tc.eob > 0 {
			dequantize(t.levels[tc.off:], txSz, t.dcQ[plane], t.acQ[plane], f.bitDepth, t.dq[:])
			inverseTransform(t.dq[:], txSz, tt, f.bitDepth, f.lossless, t.inv[:])
			reconstructAdd(t.trial[:], w, t.inv[:], txSz, tt, f.bitDepth)
		}
		d := sse(src, stride, t.trial[:], w, vw, vh)
		t.cost.n = 0
		t.writeCoeffs(&t.cost, &tc, false)
		j := float64(d) + t.lambda*float64(t.cost.n)/costScale
		if j < bestJ {
			if bestOff >= 0 {
				// keep the levels of the best only
				n := len(t.levels) - tc.off
				copy(t.levels[bestOff:], t.levels[tc.off:])
				t.levels = t.levels[:bestOff+n]
				tc.off = bestOff
			}
			bestJ, best, bestOff = j, tc, tc.off
			copy(t.best[:w*h], t.trial[:w*h])
		} else {
			t.levels = t.levels[:tc.off]
		}
		if zeroStop && tc.eob == 0 {
			break // DCT_DCT all zero: the others not tried
		}
	}
	if best.eob > 0 {
		// all zero: the prediction alone
		d := sse(src, stride, t.pred[:], w, vw, vh)
		zero := txCoded{plane: plane, x4: tc.x4, y4: tc.y4, txSz: txSz, txType: DCT_DCT, off: len(t.levels)}
		t.cost.n = 0
		t.writeCoeffs(&t.cost, &zero, false)
		if j := float64(d) + t.lambda*float64(t.cost.n)/costScale; j < bestJ {
			t.levels = t.levels[:bestOff]
			bestJ, best = j, zero
			best.off = bestOff
			copy(t.best[:w*h], t.pred[:w*h])
		}
	}
	rec := f.rec[plane][startY*stride+startX:]
	for y := range h {
		copy(rec[y*stride:y*stride+w], t.best[y*w:(y+1)*w])
	}
	t.writeCoeffs(&t.cost, &best, true) // the contexts
	if plane == 0 {
		tt := uint8(best.txType) //nolint:gosec // a type
		if best.eob == 0 {
			tt = DCT_DCT
		}
		for y := range stepY {
			for x := range stepX {
				if row+y < f.miRows && col+x < f.miCols {
					f.mis[f.mi(row+y, col+x)].txType = tt
				}
			}
		}
	}
	t.txs = append(t.txs, best)
	lw := f.miCols >> uint(sub)
	for i := range stepY {
		for j := range stepX {
			if y := (row >> uint(sub)) + i; y < f.miRows>>uint(sub) {
				if x := (col >> uint(sub)) + j; x < lw {
					f.lfTx[plane][y*lw+x] = uint8(txSz) //nolint:gosec // a size
				}
			}
		}
	}
	for i := range stepY {
		for j := range stepX {
			t.decoded[plane][(sbRow>>uint(sub))+i+1][(sbCol>>uint(sub))+j+1] = true
		}
	}
	return bestJ, best.eob, best.txType, true
}

// txTypes are the transform types to try: the luma's set's, the
// chroma's one.
func (t *tileEnc) txTypes(plane, txSz int) []int {
	if t.f.lossless || int(txSizeSqrUp[txSz]) > TX_32X32 {
		return []int{DCT_DCT}
	}
	if t.b.isInter {
		return t.interTxTypesFor(plane, txSz)
	}
	set := t.txSet(txSz)
	if plane == 0 {
		switch {
		case set == TX_SET_DCTONLY || t.f.fp.qIndex == 0:
			return []int{DCT_DCT}
		case set == TX_SET_INTRA_1:
			return []int{DCT_DCT, ADST_DCT, DCT_ADST, ADST_ADST, IDTX, V_DCT, H_DCT}
		}
		return []int{DCT_DCT, ADST_DCT, DCT_ADST, ADST_ADST, IDTX}
	}
	tt := int(modeToTxfm[t.b.uvMode])
	if txTypeInSetIntra[set][tt] == 0 {
		return []int{DCT_DCT}
	}
	return []int{tt}
}

// txSet is get_tx_set for an intra block.
func (t *tileEnc) txSet(txSz int) int {
	sq, up := int(txSizeSqr[txSz]), int(txSizeSqrUp[txSz])
	switch {
	case up >= TX_32X32:
		return TX_SET_DCTONLY
	case t.f.fp.reducedTx || sq == TX_16X16:
		return TX_SET_INTRA_2
	}
	return TX_SET_INTRA_1
}

// dqDenom is the dequantization's divisor by transform size.
// txCands is how many transform types a transform searches by rate and
// distortion when it has more to try, the others ranked out by
// rankTxTypes (0: all searched). (On the two 120-picture clips against
// all searched: 2 0.2% larger, 1.14 times faster; 3 0.6% smaller, 1.09
// times; 4 and 5 0.4% smaller, 1.06 and 1.02 times. The estimate ranks
// better than each transform's own rate and distortion decides.)
var txCands = 3

// rankTxTypes is the best k of a transform's types by an estimate from
// their forward transforms of t.resid (kept in t.txCoef): the distortion
// of rounding to the quantizer, in the samples' scale, and lambda times a
// rate of a few bits a nonzero level and a fraction of one per position
// to the last. DCT_DCT goes first when kept, and alone when it quantizes
// to all zero in an inter block (as pruneTxZero would stop).
func (t *tileEnc) rankTxTypes(plane, txSz int, types []int, k int) []int {
	w, h := int(txWidth[txSz]), int(txHeight[txSz])
	n := min(w, 32) * min(h, 32)
	den := int64(dqDenom(txSz))
	dsh := uint(bits.TrailingZeros64(uint64(den))) //nolint:gosec // 1, 2 or 4
	dcQ, acQ := int64(t.dcQ[plane]), int64(t.acQ[plane])
	scale := float64(den*den) / 64
	type ranked struct {
		tt  int
		est float64
	}
	var rs [TX_TYPES]ranked
	t.txCols.reset()
	for i, tt := range types {
		c := t.txCoef[tt][:n]
		forwardTypes(t.resid[:w*h], txSz, types[i:i+1], &t.txCoef, &t.txCols)
		t.txCoefOK |= 1 << uint(tt)
		inv := invScanFor(txSz, tt)[:n]
		var d int64
		nb, eob := 0, 0
		qs := dcQ
		for j, v := range c {
			if j == 1 {
				qs = acQ
			}
			a := int64(abs32(v))
			r := a<<dsh + qs/2
			if r < qs { // a level of 0: most are, and no division
				d += a * a
				continue
			}
			l := r / qs
			e := a - ((l*qs)&0xFFFFFF)>>dsh
			d += e * e
			nb += 3 + 2*(bits.Len64(uint64(l))-1) //nolint:gosec // l > 0
			eob = max(eob, int(inv[j])+1)
		}
		if tt == DCT_DCT && eob == 0 && pruneTxZero && t.b.isInter {
			return types[i : i+1]
		}
		rs[i] = ranked{tt, float64(d)*scale + t.lambda*(float64(nb)+0.5*float64(eob))}
	}
	r := rs[:len(types)]
	slices.SortStableFunc(r, func(a, b ranked) int { return cmp.Compare(a.est, b.est) })
	out := t.txTypeBuf[:0]
	for _, x := range r[:k] {
		if x.tt == DCT_DCT {
			out = append(out, DCT_DCT)
		}
	}
	for _, x := range r[:k] {
		if x.tt != DCT_DCT {
			out = append(out, x.tt)
		}
	}
	return out
}

func dqDenom(txSz int) int {
	switch txSz {
	case TX_32X32, TX_16X32, TX_32X16, TX_16X64, TX_64X16:
		return 2
	case TX_64X64, TX_32X64, TX_64X32:
		return 4
	}
	return 1
}

// writeSkipMode is read_skip_mode.
func (t *tileEnc) writeSkipMode(cd coder) {
	f, b := t.f, &t.b
	if !f.fp.skipModePresent || blockWidth[b.size] < 8 || blockHeight[b.size] < 8 {
		return
	}
	ctx := 0
	if b.availU {
		ctx += int(f.mis[f.mi(b.r-1, b.c)].skipMode)
	}
	if b.availL {
		ctx += int(f.mis[f.mi(b.r, b.c-1)].skipMode)
	}
	cd.symbol(b2i(b.skipMode), t.cdf.skipMode[ctx][:])
}

// writeSkip is read_skip_mode (no skip mode) and read_skip.
func (t *tileEnc) writeSkip(cd coder) {
	t.writeSkipMode(cd)
	f, b := t.f, &t.b
	ctx := 0
	if b.availU {
		ctx += int(f.mis[f.mi(b.r-1, b.c)].skip)
	}
	if b.availL {
		ctx += int(f.mis[f.mi(b.r, b.c-1)].skip)
	}
	cd.symbol(b2i(b.skip), t.cdf.skip[ctx][:])
}

// writeYMode writes the luma mode and its angle.
func (t *tileEnc) writeYMode(cd coder) {
	f, b := t.f, &t.b
	am, lm := DC_PRED, DC_PRED
	if b.availU {
		am = int(f.mis[f.mi(b.r-1, b.c)].yMode)
	}
	if b.availL {
		lm = int(f.mis[f.mi(b.r, b.c-1)].yMode)
	}
	if f.fp.key {
		cd.symbol(b.yMode, t.cdf.yMode[intraModeContext[am]][intraModeContext[lm]][:])
	} else {
		cd.symbol(b.yMode, t.cdf.yModeInter[sizeGroup[b.size]][:])
	}
	if b.size >= BLOCK_8X8 && isDirectional(b.yMode) {
		cd.symbol(b.angleY+MAX_ANGLE_DELTA, t.cdf.angleDelta[b.yMode-V_PRED][:])
	}
}

// writeUVMode writes the chroma mode and its angle.
func (t *tileEnc) writeUVMode(cd coder) {
	b := &t.b
	if t.cflAllowed() {
		cd.symbol(b.uvMode, t.cdf.uvCfl[b.yMode][:])
		if b.uvMode == UV_CFL_PRED {
			t.writeCflAlphas(cd)
		}
	} else {
		cd.symbol(b.uvMode, t.cdf.uvNoCfl[b.yMode][:])
	}
	if b.size >= BLOCK_8X8 && isDirectional(b.uvMode) {
		cd.symbol(b.angleUV+MAX_ANGLE_DELTA, t.cdf.angleDelta[b.uvMode-V_PRED][:])
	}
}

// writeIsInter is read_is_inter (in inter frames).
func (t *tileEnc) writeIsInter(cd coder, inter bool) {
	f, b := t.f, &t.b
	if f.fp.key {
		return
	}
	intra := func(r, c int) bool { return f.mis[f.mi(r, c)].refFrames[0] <= INTRA_FRAME }
	ctx := 0
	switch {
	case b.availU && b.availL:
		au, al := intra(b.r-1, b.c), intra(b.r, b.c-1)
		switch {
		case au && al:
			ctx = 3
		case au || al:
			ctx = 1
		}
	case b.availU:
		ctx = 2 * b2i(intra(b.r-1, b.c))
	case b.availL:
		ctx = 2 * b2i(intra(b.r, b.c-1))
	}
	cd.symbol(b2i(inter), t.cdf.isInter[ctx][:])
}

// writeModes writes the rest of an intra block's mode info and the
// transform size, after the skip flag (and cdef_idx).
func (t *tileEnc) writeModes(cd coder, depth int) {
	t.writeIsInter(cd, false)
	t.writeYMode(cd)
	if t.b.hasChroma {
		t.writeUVMode(cd)
	}
	t.writeFilterIntra(cd)
	t.writeTxDepth(cd, depth)
}

// filterIntraAllowed reports whether filter intra may be coded (for a DC
// block).
func (t *tileEnc) filterIntraAllowed() bool {
	b := &t.b
	return t.f.seq.filterIntra && max(blockWidth[b.size], blockHeight[b.size]) <= 32
}

// writeFilterIntra is filter_intra_mode_info.
func (t *tileEnc) writeFilterIntra(cd coder) {
	b := &t.b
	if b.yMode != DC_PRED || !t.filterIntraAllowed() {
		return
	}
	cd.symbol(b2i(b.filterMode >= 0), t.cdf.filterIntra[b.size][:])
	if b.filterMode >= 0 {
		cd.symbol(b.filterMode, t.cdf.filterIntraMode[:])
	}
}

// writeTxDepth is read_tx_size's tx_depth.
func (t *tileEnc) writeTxDepth(cd coder, depth int) {
	f, b := t.f, &t.b
	if f.lossless || b.size <= BLOCK_4X4 || !f.fp.txSelect {
		return
	}
	maxRect := int(maxTxSizeRect[b.size])
	maxW, maxH := int(txWidth[maxRect]), int(txHeight[maxRect])
	aboveW, leftH := 0, 0
	if b.availU {
		if i := f.mi(b.r-1, b.c); f.mis[i].isInter != 0 {
			aboveW = int(blockWidth[f.mis[i].miSize])
		} else {
			aboveW = t.aboveTxWidth(b.r, b.c)
		}
	}
	if b.availL {
		if i := f.mi(b.r, b.c-1); f.mis[i].isInter != 0 {
			leftH = int(blockHeight[f.mis[i].miSize])
		} else {
			leftH = t.leftTxHeight(b.r, b.c)
		}
	}
	ctx := b2i(aboveW >= maxW) + b2i(leftH >= maxH)
	switch maxTxDepth[b.size] {
	case 4:
		cd.symbol(depth, t.cdf.tx64[ctx][:])
	case 3:
		cd.symbol(depth, t.cdf.tx32[ctx][:])
	case 2:
		cd.symbol(depth, t.cdf.tx16[ctx][:])
	default:
		cd.symbol(depth, t.cdf.tx8[ctx][:])
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

// sseGo is sse a sample at a time.
func sseGo(a []uint16, as int, b []uint16, bs int, w, h int) int64 {
	var s int64
	if w <= 0 {
		return 0
	}
	for y := range h {
		ra, rb := a[y*as:y*as+w], b[y*bs:y*bs+w]
		for x, v := range ra {
			d := int64(v) - int64(rb[x])
			s += d * d
		}
	}
	return s
}
