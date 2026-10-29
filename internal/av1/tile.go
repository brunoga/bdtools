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
	"sync"
)

var dbgCompare bool

// frameEnc is the frame being coded, shared by its tiles.
type frameEnc struct {
	tools          *tools // the encoder's
	seq            *seqParams
	fp             *frameParams
	bitDepth       int
	miCols, miRows int
	width, height  int
	// The source and the reconstruction, padded to whole superblocks
	// (the source by its edges).
	src, rec [3][]uint16
	stride   [3]int
	// mis is the mode info per 4x4 luma block (the mi grid, miCols
	// wide).
	mis   []modeInfo
	inter *interFrameInfo                      // inter frames' header values (nil for key frames)
	refs  [TOTAL_REFS_PER_FRAME]*refFrameState // by LAST_FRAME..ALTREF_FRAME
	filt  [2]int                               // the frame's interpolation filter
	// cdefIdx is each 64x64's cdef_idx: -1 where none is coded (all its
	// blocks skipped), else what the frame's CDEF search chose.
	cdefIdx []int8
	// lfTx is LoopfilterTxSizes: each plane's transform size by 4x4 block
	// of the plane, (miCols >> sub) wide.
	lfTx [3][]uint8
	// lfEdges are the loop filter's edges by plane, pass and band, found
	// once the frame is coded (the levels' search filters many times)
	lfEdges  [3][2][][]lfEdge
	dcQ, acQ [3]int
	lossless bool
	lambda   float64 // distortion per bit (the frame's q's)
	// sbQ is each superblock's target q index (nil: the frame's), with
	// delta_q_present; sbLambda each one's lambda's scale (nil: 1).
	sbQ      []int
	sbLambda []float64
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
	// txCoef are the forward transforms of the residual by type that
	// rankTxTypes made, txCoefOK which (bit by type) are there
	txCoef              [TX_TYPES][32 * 32]int32
	txCoefOK            uint32
	eobGain             [32 * 32]float64  // quantize's, by scan position
	jointPred, jointTgt [128 * 128]uint16 // jointRefine's
	txCols              fwdColCache
	txTypeBuf           [TX_TYPES]int
	txs                 []txCoded
	levels              []int32
	lv                  [(32 + lvPad) * (32 + lvPad)]uint8
	cflL                [32 * 32]int32
	snaps               [10][9]*snapshot // by depth, a few each
	sbStart             snapshot
	cdefDone            bool
	// The quantizers and lambda the tile codes with now (its
	// CurrentQIndex's, a superblock's), and the delta_q to code on the
	// superblock's first block.
	dcQ, acQ   [3]int
	lambda     float64
	curQ, sbQ  int
	readDeltas bool
	sbDelta    int
	dctOnly    bool // luma transform types not searched
	fastTx     bool // inter candidates costed with their largest transforms, DCT_DCT
	// retype codes a decided transform tree searching its types
	retype     bool
	stack      mvStack
	txX4, txY4 int // the transform block being coded, in 4x4s of its plane
	ms         motionSearch
	// meSeen is the superblock's motion searches (interCandsFor's), by
	// reference, block and size: the partitions search the same blocks
	meSeen    map[meKey]meResult
	is        interScratch
	interPred [3][64 * 64]uint16 // an inter block's prediction, by plane // the superblock's cdef_idx is coded
	// maskP are the luma predictions masked variants are blended from
	maskP [2][]int32
	// interEst is the best inter candidate's estimate, intraLimit the
	// intra estimate above which the intra search stops (0 none)
	interEst, intraLimit float64
	// mvCosts are the motion vector components' rates mvDiffCost keeps,
	// of generation mvGen
	mvCosts [2][2*mvCostRange + 1]mvCompCost
	// the block searches' lists, kept for their room
	interCands  []interCand
	lumaCands   []modeCand
	chromaCands []modeCand
	meStarts    []mv
	intraRanked []rankedDec
	mvGen       uint32
	// iiPred is a plane's intra prediction of an inter-intra block,
	// iiIntra the block's luma ones by mode, iiInter its luma inter one
	iiPred  [32 * 32]uint16
	iiIntra [INTERINTRA_MODES][32 * 32]uint16
	iiInter [32 * 32]uint16
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
	compDist         bool // a compound block's compound_idx 0
	cflU, cflV       int  // the CFL alphas
	// An inter block's: its reference, its motion vector and RefMvIdx (its
	// mode is yMode).
	isInter  bool
	refFrame int
	mv       mv
	refMvIdx int
	// a compound block's second reference (NONE for single prediction)
	// and its motion vector
	refFrame2 int
	mv2       mv
	interp    int  // its interpolation filter
	obmc      bool // motion_mode OBMC (else SIMPLE)
	// warp is motion_mode LOCALWARP; warpParams LocalWarpParams, and
	// warpValid LocalValid after the setup shear process
	warp       bool
	warpParams [6]int32
	warpValid  bool
	// masked is comp_group_idx 1, mask its mask (maskBuf a difference
	// mask's samples)
	masked     bool
	mask       compMask
	maskBuf    []uint8
	ii         interIntra // inter-intra
	filterMode int        // filter_intra_mode, or -1
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
	interp          int
	mv, mv2         mv
	refFrame2       int
	refMvIdx        int
	skip            bool // an inter block coded without residual
	skipMode        bool // coded as skip_mode
	compDist        bool // compound_idx 0: the predictions weighted by distance
	obmc            bool // motion_mode OBMC
	warp            bool // motion_mode LOCALWARP
	masked          bool // comp_group_idx 1, by mask
	mask            compMask
	ii              interIntra
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
	t := tilePool.Get().(*tileEnc) //nolint:errcheck // always a *tileEnc
	t.reset()
	t.f, t.rowStart, t.rowEnd, t.colStart, t.colEnd, t.cdf = f, rowStart, rowEnd, colStart, colEnd, f.startCDF()
	t.w = &recorder{base: t.cdf}
	t.live, t.sbCDF = t.cdf, new(cdfs)
	t.curQ = f.fp.qIndex
	t.setQ(t.curQ)
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

// tilePool keeps tile encoders (a superblock row's, in the rows'
// wavefront) for later rows and frames: their buffers' room.
var tilePool = sync.Pool{New: func() any { return new(tileEnc) }}

// reset makes t as new, keeping its scratch buffers' room.
func (t *tileEnc) reset() {
	txs, levels, snaps := t.txs[:0], t.levels[:0], t.snaps
	ic, lc, cc, st, ir := t.interCands[:0], t.lumaCands[:0], t.chromaCands[:0], t.meStarts[:0], t.intraRanked[:0]
	maskP, maskBuf := t.maskP, t.b.maskBuf
	hmid, hmid16, msComp, isComp := t.ms.hmid, t.ms.hmid16, t.ms.s.comp, t.is.comp
	predCache := t.is.predCache
	*t = tileEnc{}
	t.txs, t.levels, t.snaps = txs, levels, snaps
	t.interCands, t.lumaCands, t.chromaCands, t.meStarts, t.intraRanked = ic, lc, cc, st, ir
	t.maskP, t.b.maskBuf = maskP, maskBuf
	t.ms.hmid, t.ms.hmid16, t.ms.s.comp, t.is.comp = hmid, hmid16, msComp, isComp
	t.is.predCache = predCache
	t.is.clearCache() // the frame's references may be new
}

func (t *tileEnc) inside(r, c int) bool {
	return c >= t.colStart && c < t.colEnd && r >= t.rowStart && r < t.rowEnd
}

// code codes the tile.
func (t *tileEnc) code() {
	for r := t.rowStart; r < t.rowEnd; r += 16 {
		t.codeRow(r, nil)
	}
}

// codeRow codes a superblock row of the tile; with wait, each superblock
// once wait says the rows above allow it (and done after it).
func (t *tileEnc) codeRow(r int, sync *rowSync) {
	for p := range 3 {
		clear(t.leftLevel[p])
		clear(t.leftDc[p])
	}
	k := 0
	for c := t.colStart; c < t.colEnd; c += 16 {
		if sync != nil {
			sync.wait(k)
		}
		{
			t.startSB(r, c)
			t.clearDecoded(r, c)
			t.cdefDone = false
			t.save(&t.sbStart, r, c, BLOCK_64X64, 0)
			*t.sbCDF = *t.live
			_, node := t.searchPartition(r, c, BLOCK_64X64, 0)
			t.restore(&t.sbStart, 0)
			var keep [3][]uint16
			if dbgCompare {
				for p := range 3 {
					keep[p] = append([]uint16(nil), t.f.rec[p]...)
				}
			}
			t.w.lr(r, c) // read_lr, filled in at the pack
			t.finalPartition(r, c, BLOCK_64X64, node)
			if dbgCompare {
				// the superblock only: the rows below code theirs at once
				for p := range 3 {
					sh := min(p, 1)
					st := t.f.stride[p]
					for y := (r * 4) >> sh; y < min((r+16)*4>>sh, len(keep[p])/st); y++ {
						for x := (c * 4) >> sh; x < min((c+16)*4>>sh, st); x++ {
							if i := y*st + x; t.f.rec[p][i] != keep[p][i] {
								panic(fmt.Sprintf("SB %d,%d: plane %d (%d,%d): search %d final %d", r, c, p, x, y, keep[p][i], t.f.rec[p][i]))
							}
						}
					}
				}
			}
		}
		if sync != nil {
			sync.done(k, t.live)
		}
		k++
	}
}

// rowSync is a superblock row's place in a tile coded a row a worker,
// each row two superblocks behind the one above (it needs the above
// right superblock coded).
type rowSync struct {
	mu    *sync.Mutex
	cond  *sync.Cond
	above *rowSync // nil for the first row
	n     int      // superblocks done
	cols  int
	// cdf is a copy of the row's CDFs after its second superblock (or its
	// last): the row below decides from it.
	cdf *cdfs
}

func (s *rowSync) wait(k int) {
	if s.above == nil {
		return
	}
	s.mu.Lock()
	for s.above.n < min(k+2, s.above.cols) {
		s.cond.Wait()
	}
	s.mu.Unlock()
}

func (s *rowSync) done(k int, live *cdfs) {
	s.mu.Lock()
	s.n = k + 1
	if k == min(1, s.cols-1) {
		c := *live
		s.cdf = &c
	}
	s.cond.Broadcast()
	s.mu.Unlock()
}

// codeTile codes a tile, its superblock rows at once (a wavefront) unless
// a superblock's q chains from the one before (delta_q): the rows decide
// from the CDFs of the row above as its second superblock ends (the
// symbols are coded later, in order, by pack), so the decisions do not
// depend on the timing. It gives the tile's symbols.
func codeTile(f *frameEnc, rowStart, rowEnd, colStart, colEnd int) []symOp {
	if f.fp.deltaQ || dbgSerialRows {
		t := newTileEnc(f, rowStart, rowEnd, colStart, colEnd)
		t.code()
		ops := t.w.ops
		tilePool.Put(t)
		return ops
	}
	var rows []int
	for r := rowStart; r < rowEnd; r += 16 {
		rows = append(rows, r)
	}
	cols := (colEnd - colStart + 15) / 16
	var mu sync.Mutex
	cond := sync.NewCond(&mu)
	syncs := make([]*rowSync, len(rows))
	for i := range rows {
		syncs[i] = &rowSync{mu: &mu, cond: cond, cols: cols}
		if i > 0 {
			syncs[i].above = syncs[i-1]
		}
	}
	// the tile's above contexts are shared: each row reads the columns
	// the row above has coded
	var aboveLevel, aboveDc [3][]uint8
	recs := make([][]symOp, len(rows))
	var wg sync.WaitGroup
	for i, r := range rows {
		wg.Go(func() {
			var c cdfs
			if i > 0 {
				s := syncs[i-1]
				mu.Lock()
				for s.cdf == nil {
					cond.Wait()
				}
				c = *s.cdf
				mu.Unlock()
			}
			// made once the row may start (and dropped as it ends): only
			// the rows coding hold their encoders
			t := newTileEnc(f, rowStart, rowEnd, colStart, colEnd)
			if i == 0 {
				aboveLevel, aboveDc = t.aboveLevel, t.aboveDc
			} else {
				t.aboveLevel, t.aboveDc = aboveLevel, aboveDc
				*t.cdf = c
			}
			t.codeRow(r, syncs[i])
			recs[i] = t.w.ops
			tilePool.Put(t)
		})
	}
	wg.Wait()
	var ops []symOp
	for _, rec := range recs {
		ops = append(ops, rec...)
	}
	return ops
}

// dbgSerialRows codes a tile's superblock rows one at a time.
var dbgSerialRows bool

// startSB sets the superblock's quantizer: the frame's, or with
// delta_q_present its own (as near the target as the delta's steps get
// from CurrentQIndex), its delta to code.
func (t *tileEnc) startSB(r, c int) {
	f := t.f
	q := f.fp.qIndex
	if f.fp.deltaQ {
		target := q
		if f.sbQ != nil {
			target = f.sbQ[(r>>4)*((f.miCols+15)>>4)+c>>4]
		}
		step := 1 << deltaQRes
		d := (target - t.curQ) / step
		if r := (target - t.curQ) % step; 2*abs(r) >= step {
			d += r / abs(r)
		}
		q = min(max(t.curQ+d*step, 1), 255)
		t.sbDelta, t.sbQ, t.readDeltas = d, q, true
	}
	t.setQ(q)
	clear(t.meSeen)
	if f.sbLambda != nil {
		t.lambda *= f.sbLambda[(r>>4)*((f.miCols+15)>>4)+c>>4]
	}
}

// setQ sets the quantizers and lambda of a q index.
func (t *tileEnc) setQ(q int) {
	f := t.f
	fp := f.fp
	bd := (f.bitDepth - 8) >> 1
	qi := func(v int) int { return min(max(v, 0), 255) }
	t.dcQ = [3]int{int(dcQlookup[bd][qi(q+fp.deltaQYDc)]), int(dcQlookup[bd][qi(q+fp.deltaQUVDc)]), int(dcQlookup[bd][qi(q+fp.deltaQUVDc)])}
	t.acQ = [3]int{int(acQlookup[bd][qi(q)]), int(acQlookup[bd][qi(q+fp.deltaQUVAc)]), int(acQlookup[bd][qi(q+fp.deltaQUVAc)])}
	t.lambda = lambdaFor(t.dcQ[0])
}

// lambdaFor is libaom's lambda for a DC quantizer: 88/24 q^2 per bit,
// its distortion 2048 times the sum of squared errors.
func lambdaFor(dcQ int) float64 { return lambdaScale * 88.0 / 24 * float64(dcQ) * float64(dcQ) / 2048 }

// lambdaScale scales lambda. (On the 120-picture clips, with the end of
// block searched: 1 1.4% better than 0.6 and 1.14 times faster, 0.9 1.2%,
// 1.15 1.1%, 0.7 0.7%, 0.5 1.3% worse; before it, 0.6 was 0.3% better
// than 0.7.)
var lambdaScale = 1.0

// writeDeltaQ is read_delta_qindex, on a superblock's first block.
func (t *tileEnc) writeDeltaQ(cd coder) {
	b := &t.b
	if !t.readDeltas {
		return
	}
	t.readDeltas = false
	if b.size == BLOCK_64X64 && b.skip {
		return // CurrentQIndex as it was
	}
	d := t.sbDelta
	a := abs(d)
	cd.symbol(min(a, DELTA_Q_SMALL), t.cdf.deltaQ[:])
	if a >= DELTA_Q_SMALL {
		n := 0
		for (a-1)>>uint(n+1) != 0 {
			n++
		}
		cd.literal(uint32(n-1), 3)            //nolint:gosec // 0..7
		cd.literal(uint32(a-1-1<<uint(n)), n) //nolint:gosec // n bits
	}
	if a != 0 {
		cd.boolean(d < 0)
	}
	t.curQ = t.sbQ
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

// modeInfo is what the mi grid keeps of a 4x4 luma block, together so
// a block's can be saved and put back a row at a time. Of inter state:
// the reference frames (RefFrames: INTRA_FRAME and NONE for intra
// blocks), the motion vectors (Mvs), whether a block is inter (IsInters)
// and skip_mode (SkipModes).
type modeInfo struct {
	mvs                                 [2]mv
	refFrames                           [2]int8
	miSize, yMode, uvMode, skip, txSize uint8
	txType                              uint8 // TxTypes
	isInter, skipMode                   uint8
	interp                              uint8 // InterpFilters (both directions: no dual filter)
	compIdx                             uint8 // compound_idx (1 where not coded)
	compGroup                           uint8 // comp_group_idx
}

// snapshot is what coding a block changes: its reconstruction, its mi
// grid, the contexts along its edges and the decoded flags.
type snapshot struct {
	r, c, size int
	rec        [3][]uint16
	mi         []modeInfo
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

// save saves what coding a block at (r, c) changes: its contexts and,
// with planes, the reconstruction of the first planes and the mi grid.
func (t *tileEnc) save(s *snapshot, r, c, size, planes int) {
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
		if p < planes {
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
	if planes > 0 {
		cols, rows := min(bw4, f.miCols-c), min(bh4, f.miRows-r)
		if cap(s.mi) < 256 {
			s.mi = make([]modeInfo, 256)
		}
		s.mi = s.mi[:cols*rows]
		for y := range rows {
			copy(s.mi[y*cols:(y+1)*cols], f.mis[f.mi(r+y, c):])
		}
	}
	s.decoded = t.decoded
}

// restore puts a snapshot back (of its planes, the first planes).
func (t *tileEnc) restore(s *snapshot, planes int) {
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
		if p < planes {
			w, h := w4*4, h4*4
			st := f.stride[p]
			for y := range h {
				copy(f.rec[p][(y0*4+y)*st+x0*4:(y0*4+y)*st+x0*4+w], s.rec[p][y*w:(y+1)*w])
			}
		}
	}
	if planes > 0 {
		cols, rows := min(bw4, f.miCols-c), min(bh4, f.miRows-r)
		for y := range rows {
			i := f.mi(r+y, c)
			copy(f.mis[i:i+cols], s.mi[y*cols:(y+1)*cols])
		}
	}
	t.decoded = s.decoded
}

// partitionCDF is the partition's CDF at (r, c).
func (t *tileEnc) partitionCDF(r, c, bSize int) []uint16 {
	f := t.f
	bsl := int(miWidthLog2[bSize])
	ctx := 0
	if t.inside(r-1, c) && int(miWidthLog2[f.mis[f.mi(r-1, c)].miSize]) < bsl {
		ctx++
	}
	if t.inside(r, c-1) && int(miHeightLog2[f.mis[f.mi(r, c-1)].miSize]) < bsl {
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

// extEst prunes an extended partition whose estimate (the split's
// quarters' and the halves' costs, as searched) is above extEst times the
// best partition's cost; 0 tries them all.
var extEst = 1.0

// splitSkipRatio is how much cheaper than a skipped whole block's its
// quarters' motion must be for the split to be tried after it (motion
// searched skipSplitRange samples around the block's vector, on its first
// reference). (On the six clips at speed 2: 1.5% smaller by PSNR-YUV,
// 2.4% by SSIM, 2.0% by VMAF, for 8% more CPU; at speed 0 1.4%, 2.3%,
// 2.4% for 12%. At speed 2, 0.9 0.35% for 2%, 0.95 0.7% for 3%, 1.1 2.0%
// for 52%; trying the split after every skipped whole block 2.1% for
// 46%; by the block's error rather than its motion nothing.)
const (
	splitSkipRatio = 1.0
	skipSplitRange = 16
)

// quartersMove reports whether a skipped inter whole block d at (r, c)
// should still try the split: its quarters' motion searches' costs
// (each its own vector) come to less than splitSkipRatio times the whole
// block's.
func (t *tileEnc) quartersMove(r, c, size int, d *blockDec) bool {
	f := t.f
	x, y := c*4, r*4
	w, h := int(blockWidth[size]), int(blockHeight[size])
	if size <= BLOCK_8X8 || x+w > f.width || y+h > f.height {
		return false
	}
	sl := math.Sqrt(t.lambda)
	m0 := d.mv
	me := meBlock{ref: f.refs[d.refFrame], src: f.src[0], srcStride: f.stride[0], x: x, y: y, w: w, h: h,
		bitDepth: f.bitDepth, filt: f.filt, allowHP: f.fp.highPrecisionMV, searchRange: skipSplitRange,
		cost: func(m mv) float64 {
			return sl * float64(t.mvDiffCost(mv{m.row - m0.row, m.col - m0.col})) / costScale
		}}
	t.mvGen++
	_, whole := t.ms.search(&me, []mv{m0})
	var quarters float64
	me.w, me.h = w/2, h/2
	for _, o := range [4][2]int{{0, 0}, {0, w / 2}, {h / 2, 0}, {h / 2, w / 2}} {
		me.x, me.y = x+o[1], y+o[0]
		_, q := t.ms.search(&me, []mv{m0, {}})
		quarters += q
	}
	return quarters < splitSkipRatio*whole
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
		cands = []int{PARTITION_NONE, PARTITION_SPLIT, PARTITION_HORZ, PARTITION_VERT}
	case hasRows && hasCols:
		cands = []int{PARTITION_NONE, PARTITION_SPLIT, PARTITION_HORZ, PARTITION_VERT,
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
		t.save(start, r, c, bSize, 3)
	}
	bestJ := math.Inf(1)
	var bestNode *partNode
	jOf := map[int]float64{}
	// the blocks' costs of the split (its quarters') and of the
	// horizontal and vertical halves, for the extended partitions'
	// estimates
	var kidJ, horzJ, vertJ [4]float64
	bestI, last := -1, -1
	for i, part := range cands {
		// Pruned: everything else after a skipped inter block that is
		// the whole partition (but the split where its quarters' motion
		// is better than its own); the extended partitions in a direction
		// splitting it that way did not help.
		if jn, ok := jOf[PARTITION_NONE]; ok && !dbgNoPrune {
			if bestNode != nil && bestNode.part == PARTITION_NONE && bestNode.blocks[0].isInter && bestNode.blocks[0].skip && t.f.tools.pruneSkip {
				if part != PARTITION_SPLIT || !t.quartersMove(r, c, bSize, bestNode.blocks[0]) {
					break
				}
				goto search
			}
			if !t.f.tools.pruneExt {
				goto search
			}
			// an extended partition whose blocks, costed as the split's
			// quarters and the halves were, are worse than the best
			if r := t.f.tools.extEst; r > 0 {
				_, okS := jOf[PARTITION_SPLIT]
				_, okH := jOf[PARTITION_HORZ]
				_, okV := jOf[PARTITION_VERT]
				est := math.Inf(-1)
				switch {
				case part == PARTITION_HORZ_A && okS && okH:
					est = kidJ[0] + kidJ[1] + horzJ[1]
				case part == PARTITION_HORZ_B && okS && okH:
					est = horzJ[0] + kidJ[2] + kidJ[3]
				case part == PARTITION_VERT_A && okS && okV:
					est = kidJ[0] + kidJ[2] + vertJ[1]
				case part == PARTITION_VERT_B && okS && okV:
					est = vertJ[0] + kidJ[1] + kidJ[3]
				}
				if est > r*bestJ {
					continue
				}
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
	search:
		if last >= 0 {
			t.restore(start, 3)
		}
		last = i
		t.cost.n = 0
		t.writePartition(&t.cost, r, c, bSize, part)
		j := t.lambda * float64(t.cost.n) / costScale
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
				switch k := len(node.blocks); part {
				case PARTITION_HORZ:
					horzJ[k] = bj
				case PARTITION_VERT:
					vertJ[k] = bj
				}
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
				kidJ[k] = kj
				node.kids[k] = kn
			}
		}
		if complete {
			jOf[part] = j
		}
		if j < bestJ {
			bestJ, bestNode, bestI = j, node, i
			if i < len(cands)-1 {
				t.save(best, r, c, bSize, 3)
			}
		}
	}
	if bestI != last {
		t.restore(best, 3)
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
