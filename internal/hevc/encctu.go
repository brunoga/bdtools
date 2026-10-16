package hevc

import (
	"math"
	"sync"
	"sync/atomic"
)

// The picture coder: decides each CTU by rate-distortion over the
// decoder's own reconstruction, then writes it.

// Source is a picture to encode: 4:2:0 planes of samples (uint16 whatever
// the bit depth) of the encoder's picture size.
type Source struct {
	Y, Cb, Cr        []uint16
	StrideY, StrideC int
}

// cuNode is a coding quadtree node as decided: split into four, or a
// coding unit with its prediction and transform tree.
type cuNode struct {
	x, y, log2 int
	split      bool
	children   [4]*cuNode
	nxn        bool   // four prediction units (the minimum CU size)
	modes      [4]int // the luma intra mode of each PU
	chroma     int    // intra_chroma_pred_mode (4: the luma mode)
	tu         *tuNode
	// Inter.
	inter   bool
	skip    bool
	part    int       // part2Nx2N, part2NxN or partNx2N
	cand    interCand // the first (or only) prediction unit's
	cand1   interCand // the second's
	rootCbf bool
}

// tuNode is a transform tree node: split into four, or a transform unit
// with its levels. A split 8x8 node keeps the 4x4 chroma blocks its last
// child codes.
type tuNode struct {
	x, y, log2, depth int
	split             bool
	children          [4]*tuNode
	cbfLuma           bool
	cbfC              [2]bool
	luma              []int32
	chroma            [2][]int32
}

// region is a snapshot of a square of the reconstruction and the block
// state under it, for the alternatives rate-distortion decisions undo.
type region struct {
	x, y, size int
	y16        []uint16
	cb, cr     []uint16
	intraMode  []uint8
	predMode   []uint8
	ctDepth    []uint8
	mvf        []mvField
}

// picShared is a picture's state its row coders share: the slice header
// the model decodes by, the reconstruction, the block state, the source
// and the rate-distortion weights.
type picShared struct {
	e   *Encoder
	s   *sps
	p   *pps
	h   sliceHeader
	pic *picture  // the picture being coded, the job's
	f   frame     // its loop filters, run as its rows are coded
	ps  *picState // the frame's block state
	src *Source
	qp  int
	// lambda weighs bits against squared error; lambdaS against the SATD.
	lambda, lambdaS float64
	refs            [2][]*picture
	planes          [2][]*refPlanes
	ilr             int // the inter-layer reference's index in list 0, -1 for none
	wppCtx          [][numContexts]uint8
	// rows counts each CTB row's CTBs coded, for the wavefront: the row
	// below waits on its row's condition alone.
	rows   []rowWait
	coders []*picCoder
}

// picCoder codes CTB rows of a picture: the decoder's slice state over
// the shared picture, an arithmetic coder for the row's substream, and
// scratch.
type picCoder struct {
	*picShared
	sd  sliceDec
	enc cabacEncoder
	// Scratch.
	res     [32 * 32]int32
	res16   [32 * 32]int16
	coef    [32 * 32]int32
	orig    [32 * 32]int32 // coefficients before quantisation
	rq      rdoqScratch
	tscr    transformScratch
	pred    [64 * 64]uint16
	predCU  [64 * 64]uint16    // an inter CU's prediction, luma
	predCUC [2][32 * 32]uint16 // and chroma
	saved   []region
	refs    intraRefs
	bits    float64 // the picture's estimated bits
	arena   []int32 // the levels of a CTU's trials
}

// transform computes the coefficients of the residual in c.res (and,
// for the fast transform's sizes, c.res16) into c.coef.
func (c *picCoder) transform(log2, depth int, dst bool) []int32 {
	n := 1 << log2
	coef := c.coef[:n*n]
	if !dst && forwardTransformFast(c.res16[:n*n], coef, log2, depth, &c.tscr) {
		return coef
	}
	forwardTransform(c.res[:n*n], coef, log2, depth, dst)
	return coef
}

// levels gives n coefficient levels from the CTU's arena.
func (c *picCoder) levels(n int) []int32 {
	if cap(c.arena)-len(c.arena) < n {
		c.arena = make([]int32, 0, max(1<<16, n))
	}
	l := c.arena[len(c.arena) : len(c.arena)+n : len(c.arena)+n]
	c.arena = c.arena[:len(c.arena)+n]
	return l
}

func newPicShared(e *Encoder, workers int) *picShared {
	s, p := e.s, e.p
	sh := &picShared{e: e, s: s, p: p}
	sh.f.threads = 1
	sh.ps = &sh.f.ps
	sh.wppCtx = make([][numContexts]uint8, s.ctbH)
	sh.rows = make([]rowWait, s.ctbH)
	for i := range sh.rows {
		sh.rows[i].cond.L = &sh.rows[i].mu
	}
	for range max(1, min(workers, s.ctbH)) {
		sh.coders = append(sh.coders, newPicCoder(sh))
	}
	return sh
}

func newPicCoder(sh *picShared) *picCoder {
	s := sh.s
	c := &picCoder{picShared: sh}
	c.saved = make([]region, slotMode+1)
	for i := range c.saved {
		size := 1 << s.log2Ctb
		r := &c.saved[i]
		r.y16 = make([]uint16, size*size)
		r.cb = make([]uint16, size*size/4)
		r.cr = make([]uint16, size*size/4)
		r.intraMode = make([]uint8, (size/4)*(size/4))
		r.predMode = make([]uint8, (size/4)*(size/4))
		r.ctDepth = make([]uint8, (size>>s.log2MinCb)*(size>>s.log2MinCb))
		r.mvf = make([]mvField, (size/4)*(size/4))
	}
	return c
}

// start readies the coders for a picture at qp of slice type typ and POC
// poc with the reference lists given (the decoder's pictures): the slice
// header the model decodes by, and the block state cleared.
// ilr is the index in list 0 of the inter-layer reference (-1 for none),
// a long-term reference; tmvp says whether the slice may predict from
// the collocated picture's motion (not an IDR picture's).
func (sh *picShared) start(pic *picture, src *Source, qp, typ, poc int, refs [2][]*picture, planes [2][]*refPlanes, ilr int, tmvp bool) {
	s := sh.s
	sh.pic = pic
	sh.src = src
	sh.qp = qp
	sh.refs = refs
	sh.planes = planes
	sh.ilr = ilr
	sh.h = sliceHeader{firstSliceInPic: true, typ: typ, picOutput: true, sliceQP: qp,
		qpDelta: qp - sh.p.initQP, maxMergeCand: 5, poc: poc, colFromL0: true}
	if typ != sliceI {
		sh.h.numRefIdx = [2]int{len(refs[0]), len(refs[1])}
		sh.h.temporalMVP = s.temporalMVP && tmvp
	}
	sh.h.saoLuma, sh.h.saoChroma = s.sao, s.sao
	// The picture's identity (POC, reference POCs) was set when its job
	// was dispatched, before any picture predicted from it started.
	sh.f.begin(pic, s, sh.p)
	ps := sh.ps
	ps.slices = append(ps.slices[:0], &sh.h)
	sh.f.prepare() // sizes and clears the block state, the motion
	clear(ps.ctDepth)
	clear(ps.intraMode)
	clear(ps.predMode)
	clear(ps.ctbSlice)
	for i := range ps.qpY {
		ps.qpY[i] = int8(qp) //nolint:gosec // 0..51
	}
	for i := range sh.rows {
		sh.rows[i].n.Store(0)
	}
	// HM's lambda, in the sample domain of the bit depth.
	factor := 0.57
	if typ != sliceI {
		factor = 0.68
	}
	sh.lambda = factor * math.Pow(2, float64(qp-12)/3) * float64(int(1)<<(2*(s.bitDepth-8)))
	sh.lambdaS = math.Sqrt(sh.lambda)
	for _, c := range sh.coders {
		c.begin()
	}
}

// begin readies a row coder for the picture started.
func (c *picCoder) begin() {
	sh := c.picShared
	s := sh.s
	c.sd = sliceDec{d: sh.e.dec, h: &sh.h, p: sh.p, s: s, pic: sh.pic, ps: sh.ps, qpY: sh.qp, lastCUQpY: sh.qp, predMode: modeIntra}
	c.sd.log2MinCuQPDelta = s.log2Ctb
	for l := range 2 {
		c.sd.refList[l] = sh.refs[l]
		c.sd.refIsLT[l] = make([]bool, len(sh.refs[l]))
	}
	if sh.ilr >= 0 {
		c.sd.refIsLT[0][sh.ilr] = true
	}
	c.bits = 0
}

// codePicture decides and writes the picture's slice data: one slice, a
// substream per CTB row (WPP), the rows coded in parallel on a
// wavefront, each two CTBs behind the one above. It returns the
// substreams.
func (sh *picShared) codePicture() [][]byte {
	s := sh.s
	subs := make([][]byte, s.ctbH)
	var next atomic.Int32
	var wg sync.WaitGroup
	for _, c := range sh.coders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				r := int(next.Add(1) - 1)
				if r >= s.ctbH {
					return
				}
				subs[r] = c.codeRow(r)
			}
		}()
	}
	wg.Wait()
	// Every row is coded: what filtering is left, then the picture final.
	sh.f.advance()
	sh.pic.setAllFinal()
	return subs
}

// rowWait is a CTB row's progress, and the condition the row below
// waits on.
type rowWait struct {
	n       atomic.Int32
	waiting atomic.Bool
	mu      sync.Mutex
	cond    sync.Cond
}

// waitAbove blocks until the row above has coded n CTBs.
func (sh *picShared) waitAbove(row, n int) {
	if row == 0 {
		return
	}
	n = min(n, sh.s.ctbW)
	r := &sh.rows[row-1]
	if int(r.n.Load()) >= n {
		return
	}
	r.mu.Lock()
	r.waiting.Store(true)
	for int(r.n.Load()) < n {
		r.cond.Wait()
	}
	r.waiting.Store(false)
	r.mu.Unlock()
}

// advance counts the row's CTBs coded, waking the row below if it waits.
func (sh *picShared) advance(row, n int) {
	r := &sh.rows[row]
	r.n.Store(int32(n)) //nolint:gosec // a small count
	if r.waiting.Load() {
		r.mu.Lock()
		r.cond.Signal()
		r.mu.Unlock()
	}
}

// codeRow decides and writes CTB row ctbY as a substream.
func (c *picCoder) codeRow(ctbY int) []byte {
	sh, s := c.picShared, c.s
	w := &bitWriter{}
	for ctbX := range s.ctbW {
		x0, y0 := ctbX<<s.log2Ctb, ctbY<<s.log2Ctb
		addr := ctbY*s.ctbW + ctbX
		sh.waitAbove(ctbY, ctbX+2)
		c.sd.ctbAddrRS, c.sd.ctbAddrTS = addr, addr
		if ctbX == 0 {
			if ctbY > 0 && c.sd.available(x0, y0, x0+s.ctbSize, y0-s.ctbSize) {
				c.enc.resume(w, &sh.wppCtx[ctbY-1])
			} else {
				c.enc.start(w, c.sd.initType(), sh.h.sliceQP)
			}
		}
		node := c.decideCTU(x0, y0)
		if s.sao {
			c.decideSAO(ctbX, ctbY)
			c.writeSAO(&c.enc, ctbX, ctbY)
		}
		c.writeCU(&c.enc, node, 0)
		c.markCU(node)
		sh.ps.ctbDecoded[addr] = true
		if ctbX == 1 || s.ctbW == 1 {
			sh.wppCtx[ctbY] = c.enc.ctx
		}
		sh.advance(ctbY, ctbX+1)
		sh.f.ctbDone(ctbY) // filters what that allows
		last := ctbY == s.ctbH-1 && ctbX == s.ctbW-1
		if last {
			c.enc.terminate(1) // end_of_slice_segment_flag
			w.alignZeros()
			break
		}
		c.enc.terminate(0)
		if ctbX == s.ctbW-1 {
			c.enc.terminate(1) // end_of_subset_one_bit
			w.alignZeros()
		}
	}
	return w.bytes()
}

// decideCTU decides the CTU at (x0, y0) by rate-distortion, leaving its
// reconstruction in the picture.
func (c *picCoder) decideCTU(x0, y0 int) *cuNode {
	c.arena = c.arena[:0]
	ctx := c.enc.ctx
	node, _, _ := c.rdCU(x0, y0, c.s.log2Ctb, 0, &ctx)
	return node
}

// The saved regions: one per CU size for the split decisions, one per
// transform size for the transform split decisions, one for NxN.
const (
	slotCU    = 0 // + log2 - log2MinCb
	slotTU    = 4 // + log2 - log2MinTb
	slotNxN   = 8
	slotInter = 9  // the best inter trial of a CU
	slotMode  = 10 // the inter CU while intra is tried
)

// save snapshots the square at (x, y) of size 1 << log2 into the saved
// region slot.
func (c *picCoder) save(x, y, log2, slot int) *region {
	r := &c.saved[slot]
	size := 1 << log2
	r.x, r.y, r.size = x, y, size
	pic := c.pic
	for row := range size {
		copy(r.y16[row*size:row*size+size], pic.y[(y+row)*pic.strideY+x:])
	}
	for row := range size / 2 {
		copy(r.cb[row*size/2:row*size/2+size/2], pic.cb[(y/2+row)*pic.strideC+x/2:])
		copy(r.cr[row*size/2:row*size/2+size/2], pic.cr[(y/2+row)*pic.strideC+x/2:])
	}
	ps := c.ps
	n4 := size / 4
	for row := range n4 {
		i := (y/4+row)*ps.w4 + x/4
		copy(r.intraMode[row*n4:row*n4+n4], ps.intraMode[i:i+n4])
		copy(r.predMode[row*n4:row*n4+n4], ps.predMode[i:i+n4])
		copy(r.mvf[row*n4:row*n4+n4], pic.mvf[(y/4+row)*pic.mvfW+x/4:])
	}
	s := c.s
	nc := size >> s.log2MinCb
	cx, cy := x>>s.log2MinCb, y>>s.log2MinCb
	w := min(nc, s.minCbW-cx) // the part inside the picture
	for row := range min(nc, s.minCbH-cy) {
		i := (cy+row)*s.minCbW + cx
		copy(r.ctDepth[row*nc:row*nc+w], ps.ctDepth[i:i+w])
	}
	return r
}

// restore puts a saved region back.
func (c *picCoder) restore(r *region) {
	x, y, size := r.x, r.y, r.size
	pic := c.pic
	for row := range size {
		copy(pic.y[(y+row)*pic.strideY+x:], r.y16[row*size:row*size+size])
	}
	for row := range size / 2 {
		copy(pic.cb[(y/2+row)*pic.strideC+x/2:], r.cb[row*size/2:row*size/2+size/2])
		copy(pic.cr[(y/2+row)*pic.strideC+x/2:], r.cr[row*size/2:row*size/2+size/2])
	}
	ps := c.ps
	n4 := size / 4
	for row := range n4 {
		i := (y/4+row)*ps.w4 + x/4
		copy(ps.intraMode[i:i+n4], r.intraMode[row*n4:row*n4+n4])
		copy(ps.predMode[i:i+n4], r.predMode[row*n4:row*n4+n4])
		copy(pic.mvf[(y/4+row)*pic.mvfW+x/4:], r.mvf[row*n4:row*n4+n4])
	}
	s := c.s
	nc := size >> s.log2MinCb
	cx, cy := x>>s.log2MinCb, y>>s.log2MinCb
	w := min(nc, s.minCbW-cx)
	for row := range min(nc, s.minCbH-cy) {
		i := (cy+row)*s.minCbW + cx
		copy(ps.ctDepth[i:i+w], r.ctDepth[row*nc:row*nc+w])
	}
}

// skipCtxInc is the context increment of cu_skip_flag at (x0, y0): the
// neighbours skipped.
func (c *picCoder) skipCtxInc(x0, y0 int) int {
	ps, sd := c.ps, &c.sd
	inc := 0
	if sd.available(x0, y0, x0-1, y0) && ps.predMode[sd.blk4(x0-1, y0)] == modeSkip {
		inc++
	}
	if sd.available(x0, y0, x0, y0-1) && ps.predMode[sd.blk4(x0, y0-1)] == modeSkip {
		inc++
	}
	return inc
}

// splitCtxInc is the context increment of split_cu_flag at (x0, y0) for
// a CU of depth: the neighbours deeper than it.
func (c *picCoder) splitCtxInc(x0, y0, depth int) int {
	s, ps, sd := c.s, c.ps, &c.sd
	inc := 0
	if sd.available(x0, y0, x0-1, y0) && int(ps.ctDepth[(y0>>s.log2MinCb)*s.minCbW+(x0-1)>>s.log2MinCb]) > depth {
		inc++
	}
	if sd.available(x0, y0, x0, y0-1) && int(ps.ctDepth[((y0-1)>>s.log2MinCb)*s.minCbW+x0>>s.log2MinCb]) > depth {
		inc++
	}
	return inc
}

// rdCU decides the coding quadtree at (x0, y0): the CU as one, or split,
// whichever costs less, starting the bit estimates from ctxIn. It
// returns the node, its cost and the context states after it.
func (c *picCoder) rdCU(x0, y0, log2, depth int, ctxIn *[numContexts]uint8) (*cuNode, float64, [numContexts]uint8) {
	s := c.s
	size := 1 << log2
	fits := x0+size <= s.width && y0+size <= s.height
	canSplit := log2 > s.log2MinCb
	slot := slotCU + log2 - s.log2MinCb
	var best *cuNode
	bestCost := math.Inf(1)
	var bestCtx [numContexts]uint8
	// The CU whole: intra not at 64, whose intra prediction would be of
	// four transform blocks in turn; inter at any size.
	inter := c.h.typ != sliceI
	if fits && (log2 < 6 || inter) {
		node := &cuNode{x: x0, y: y0, log2: log2}
		var k cabacCost
		k.ctx = *ctxIn
		if canSplit {
			k.decision(ctxSplitCodingUnitFlag+c.splitCtxInc(x0, y0, depth), 0)
		}
		c.setDepth(x0, y0, size, depth)
		var cost float64
		if !inter {
			cost = c.rdIntraCU(node, &k)
		} else {
			kI := k
			cost = c.rdInterCU(node, &k)
			if log2 < 6 && c.h.typ != sliceB {
				c.save(x0, y0, log2, slotMode)
				nodeI := &cuNode{x: x0, y: y0, log2: log2}
				costI := c.rdIntraCU(nodeI, &kI)
				if costI < cost {
					node, cost, k = nodeI, costI, kI
				} else {
					c.restore(&c.saved[slotMode])
				}
			}
		}
		best, bestCost, bestCtx = node, cost, k.ctx
		// A skipped 16x16 is not split: 0.3% larger output for 18%
		// less work, measured (above 16 it costs far more).
		if node.inter && node.skip && log2 == 4 {
			canSplit = false
		}
		if canSplit {
			c.save(x0, y0, log2, slot)
		}
	}
	if canSplit {
		node := &cuNode{x: x0, y: y0, log2: log2, split: true}
		ctx := *ctxIn
		cost := 0.0
		if fits {
			var k cabacCost
			k.ctx = ctx
			k.decision(ctxSplitCodingUnitFlag+c.splitCtxInc(x0, y0, depth), 1)
			cost = c.lambda * k.bits
			ctx = k.ctx
		}
		half := size >> 1
		for i := range 4 {
			x, y := x0+(i&1)*half, y0+(i>>1)*half
			if x >= s.width || y >= s.height {
				continue
			}
			var cc float64
			node.children[i], cc, ctx = c.rdCU(x, y, log2-1, depth+1, &ctx)
			cost += cc
			if cost >= bestCost {
				break
			}
		}
		if cost < bestCost {
			best, bestCost, bestCtx = node, cost, ctx
		} else if best != nil {
			c.restore(&c.saved[slot])
		}
	}
	return best, bestCost, bestCtx
}

// setDepth records a CU's depth for the split flags' contexts.
func (c *picCoder) setDepth(x0, y0, size, depth int) {
	s, ps := c.s, c.ps
	minCb := 1 << s.log2MinCb
	for y := y0; y < y0+size && y < s.height; y += minCb {
		for x := x0; x < x0+size && x < s.width; x += minCb {
			ps.ctDepth[(y>>s.log2MinCb)*s.minCbW+x>>s.log2MinCb] = uint8(depth) //nolint:gosec // 0..3
		}
	}
}

// setModes records a PU's intra mode (and that it is intra) in the block
// state the predictions of its neighbours read.
func (c *picCoder) setModes(x0, y0, size, mode int) {
	s, ps := c.s, c.ps
	for y := y0; y < y0+size && y < s.height; y += 4 {
		for x := x0; x < x0+size && x < s.width; x += 4 {
			i := c.sd.blk4(x, y)
			ps.intraMode[i] = uint8(mode) //nolint:gosec // 0..34
			ps.predMode[i] = modeIntra
			c.pic.mvf[(y>>2)*c.pic.mvfW+x>>2] = mvField{} // intra: no motion
		}
	}
}

// mpm derives a PU's most probable modes (8.4.2), unsorted as the
// mpm_idx indexes them.
func (c *picCoder) mpm(x, y int) [3]int {
	s, ps, sd := c.s, c.ps, &c.sd
	candA, candB := 1, 1
	if sd.available(x, y, x-1, y) {
		if i := sd.blk4(x-1, y); ps.predMode[i] == modeIntra {
			candA = int(ps.intraMode[i])
		}
	}
	if y-1 >= (y>>s.log2Ctb)<<s.log2Ctb && sd.available(x, y, x, y-1) {
		if i := sd.blk4(x, y-1); ps.predMode[i] == modeIntra {
			candB = int(ps.intraMode[i])
		}
	}
	if candA == candB {
		if candA < 2 {
			return [3]int{0, 1, 26}
		}
		return [3]int{candA, 2 + (candA+29)%32, 2 + (candA-2+1)%32}
	}
	cand := [3]int{candA, candB, 0}
	switch {
	case candA != 0 && candB != 0:
		cand[2] = 0
	case candA != 1 && candB != 1:
		cand[2] = 1
	default:
		cand[2] = 26
	}
	return cand
}

// modeBits is the bits coding a luma mode takes, roughly: a flag and the
// index of a most probable mode, or a flag and five bits.
func modeBits(mode int, cand [3]int) float64 {
	for i, m := range cand {
		if m == mode {
			return 1 + float64(min(i+1, 2))
		}
	}
	return 6
}

// rdIntraCU decides the CU's prediction and transform tree, reconstructs
// it, and returns its cost; k estimates its bits (continuing from the
// states given).
func (c *picCoder) rdIntraCU(node *cuNode, k *cabacCost) float64 {
	s := c.s
	x0, y0, log2 := node.x, node.y, node.log2
	size := 1 << log2
	node.chroma = 4
	c.sd.predMode = modeIntra
	var ssd float64
	// Four PUs at the minimum size in I pictures only: in inter pictures
	// they cost 0.6% less output for 10% more work, measured.
	if log2 == s.log2MinCb && c.h.typ == sliceI {
		// 2Nx2N against NxN: four PUs with modes of their own, decided
		// in turn on the reconstruction of the ones before.
		ctx := k.ctx
		node.modes[0] = c.bestMode(x0, y0, log2)
		node.nxn = false
		node.tu = c.rdTree(x0, y0, log2, 0, node, &ctx)
		ssd = c.ssdCU(x0, y0, size)
		var k1 cabacCost
		k1.ctx = k.ctx
		c.codeCU(&k1, node)
		cost1 := ssd + c.lambda*k1.bits
		c.save(x0, y0, log2, slotNxN)
		four := &cuNode{x: x0, y: y0, log2: log2, nxn: true, chroma: 4}
		half := size >> 1
		tree := &tuNode{x: x0, y: y0, log2: log2, split: true}
		for i := range 4 {
			x, y := x0+(i&1)*half, y0+(i>>1)*half
			four.modes[i] = c.bestMode(x, y, log2-1)
			c.setModes(x, y, half, four.modes[i])
			tree.children[i] = c.codeLeaf(x, y, log2-1, 1, four.modes[i], 0, false)
		}
		c.codeChroma4(tree, four.modes[0])
		four.tu = tree
		ssd4 := c.ssdCU(x0, y0, size)
		var k4 cabacCost
		k4.ctx = k.ctx
		c.codeCU(&k4, four)
		cost4 := ssd4 + c.lambda*k4.bits
		if cost4 < cost1 {
			*node = *four
			k.ctx, k.bits = k4.ctx, k.bits+k4.bits
			return cost4
		}
		c.restore(&c.saved[slotNxN])
		k.ctx, k.bits = k1.ctx, k.bits+k1.bits
		return cost1
	}
	node.modes[0] = c.bestMode(x0, y0, log2)
	ctx := k.ctx
	node.tu = c.rdTree(x0, y0, log2, 0, node, &ctx)
	ssd = c.ssdCU(x0, y0, size)
	var k1 cabacCost
	k1.ctx = k.ctx
	c.codeCU(&k1, node)
	k.ctx, k.bits = k1.ctx, k.bits+k1.bits
	return ssd + c.lambda*k1.bits
}

// bestMode picks the luma intra mode of the PU at (x0, y0) of size
// 1 << log2 by the SATD of its prediction plus the bits of the mode,
// then records it.
func (c *picCoder) bestMode(x0, y0, log2 int) int {
	s := c.s
	n := 1 << log2
	cand := c.mpm(x0, y0)
	// Prediction at the PU's size reads the neighbours as a transform
	// block of that size would; a 64 PU is never searched (CUs split).
	c.sd.intraRefs(x0, y0, log2, 0, &c.refs)
	src := c.src.Y[y0*c.src.StrideY+x0:]
	best, bestCost := 1, math.Inf(1)
	var tried [35]bool
	try := func(mode int) {
		if tried[mode] {
			return
		}
		tried[mode] = true
		intraPredBlock(&c.refs, mode, log2, 0, s.bitDepth, c.pred[:], n)
		cost := float64(satd(src, c.src.StrideY, c.pred[:], n, n, n)) + c.lambdaS*modeBits(mode, cand)
		if cost < bestCost {
			best, bestCost = mode, cost
		}
	}
	// Planar, DC and every other angle, then the angles beside the best,
	// and the most probable modes.
	try(0)
	try(1)
	step := 2
	if c.h.typ != sliceI {
		step = 8 // in inter pictures, every eighth angle, then refined
	}
	for mode := 2; mode < 35; mode += step {
		try(mode)
	}
	if step > 2 && best >= 2 {
		for d := step / 2; d >= 1; d /= 2 {
			center := best
			if center-d >= 2 {
				try(center - d)
			}
			if center+d <= 34 {
				try(center + d)
			}
		}
	}
	if best >= 2 {
		if best > 2 {
			try(best - 1)
		}
		if best < 34 {
			try(best + 1)
		}
	}
	for _, m := range cand {
		try(m)
	}
	c.setModes(x0, y0, n, best)
	return best
}

// rdTree decides the transform tree at (x, y) of a CU with one PU: the
// node as one transform unit, or split, whichever costs less.
func (c *picCoder) rdTree(x, y, log2, depth int, node *cuNode, ctx *[numContexts]uint8) *tuNode {
	s := c.s
	mode := node.modes[0]
	maxDepth := s.maxTrDepthIntra
	canSplit := log2 <= s.log2MaxTb && log2 > s.log2MinTb && depth < maxDepth
	forced := log2 > s.log2MaxTb
	if !canSplit && !forced {
		return c.codeLeaf(x, y, log2, depth, mode, node.modes[0], true)
	}
	size := 1 << log2
	slot := slotTU + log2 - s.log2MinTb
	var one *tuNode
	costOne := math.Inf(1)
	var ctxOne [numContexts]uint8
	if !forced {
		one = c.codeLeaf(x, y, log2, depth, mode, node.modes[0], true)
		var k cabacCost
		k.ctx = *ctx
		c.codeTree(&k, one, nil, node, 0)
		costOne = c.ssdCU(x, y, size) + c.lambda*k.bits
		ctxOne = k.ctx
		c.save(x, y, log2, slot)
	}
	split := &tuNode{x: x, y: y, log2: log2, depth: depth, split: true}
	half := size >> 1
	for i := range 4 {
		split.children[i] = c.rdTreeChild(x+(i&1)*half, y+(i>>1)*half, log2-1, depth+1, node, ctx)
	}
	if log2 == 3 {
		c.codeChroma4(split, node.modes[0])
	}
	var k cabacCost
	k.ctx = *ctx
	c.codeTree(&k, split, nil, node, 0)
	costSplit := c.ssdCU(x, y, size) + c.lambda*k.bits
	if costSplit < costOne {
		*ctx = k.ctx
		return split
	}
	c.restore(&c.saved[slot])
	*ctx = ctxOne
	return one
}

// rdTreeChild decides a child node of a split: a leaf at the minimum
// size, or a tree of its own.
func (c *picCoder) rdTreeChild(x, y, log2, depth int, node *cuNode, ctx *[numContexts]uint8) *tuNode {
	s := c.s
	if log2 <= s.log2MinTb || depth >= s.maxTrDepthIntra && log2 <= s.log2MaxTb {
		return c.codeLeaf(x, y, log2, depth, node.modes[0], node.modes[0], log2 > 2)
	}
	return c.rdTree(x, y, log2, depth, node, ctx)
}

// codeLeaf predicts, transforms, quantises and reconstructs the transform
// unit at (x, y) of size 1 << log2 with the luma mode, and its chroma
// blocks when it has them (chroma reports the chroma mode's luma mode;
// a 4x4 luma block has none of its own).
func (c *picCoder) codeLeaf(x, y, log2, depth, mode, chromaMode int, chroma bool) *tuNode {
	n := &tuNode{x: x, y: y, log2: log2, depth: depth}
	n.cbfLuma, n.luma = c.codeBlock(x, y, log2, 0, mode)
	if chroma && log2 > 2 {
		for ci := 1; ci <= 2; ci++ {
			n.cbfC[ci-1], n.chroma[ci-1] = c.codeBlock(x/2, y/2, log2-1, ci, chromaMode)
		}
	}
	return n
}

// codeChroma4 codes the 4x4 chroma blocks of a split 8x8 node, after its
// four luma blocks.
func (c *picCoder) codeChroma4(n *tuNode, chromaMode int) {
	for ci := 1; ci <= 2; ci++ {
		n.cbfC[ci-1], n.chroma[ci-1] = c.codeBlock(n.x/2, n.y/2, 2, ci, chromaMode)
	}
}

// codeBlock predicts the block of component ci at (x, y) (its samples)
// with mode, codes its residual and reconstructs it in the picture,
// returning whether it has levels and them.
func (c *picCoder) codeBlock(x, y, log2, ci, mode int) (bool, []int32) {
	s, sd := c.s, &c.sd
	n := 1 << log2
	sd.intraPredict(x, y, log2, ci, mode)
	pl, stride := c.pic.y, c.pic.strideY
	src, sstride := c.src.Y, c.src.StrideY
	depth := s.bitDepth
	if ci > 0 {
		pl, stride = c.pic.cb, c.pic.strideC
		src, sstride = c.src.Cb, c.src.StrideC
		if ci == 2 {
			pl, src = c.pic.cr, c.src.Cr
		}
		depth = s.bitDepthC
	}
	res, res16 := c.res[:n*n], c.res16[:n*n]
	for j := range n {
		p := pl[(y+j)*stride+x:]
		o := src[(y+j)*sstride+x:]
		for i := range n {
			d := int32(o[i]) - int32(p[i])
			res[j*n+i], res16[j*n+i] = d, int16(d) //nolint:gosec // a residual of at most 10 bits
		}
	}
	coef := c.transform(log2, depth, ci == 0 && log2 == 2)
	qp := c.qp + s.qpBdOffset
	if ci > 0 {
		qpi := min(max(c.qp, -s.qpBdOffsetC), 57)
		qp = chromaQP(qpi) + s.qpBdOffsetC
	}
	q := newQuantizer(qp, log2, depth)
	levels := c.quantize(&q, coef, log2, ci, scanIdxFor(true, mode, log2, ci))
	if levels == nil {
		return false, nil
	}
	copy(sd.coeffs[:n*n], levels)
	sd.reconstruct(x, y, log2, ci, false)
	return true, levels
}

// quantize quantises a block's coefficients (with sign data hiding when
// the stream has it, along the block's scan), returning its levels from
// the CTU's arena, or nil when they are all zero.
func (c *picCoder) quantize(q *quantizer, coef []int32, log2, ci, scanIdx int) []int32 {
	n := 1 << log2
	hide := c.e.c.signHiding
	copy(c.orig[:n*n], coef) // RDOQ and sign hiding read them
	nz := c.rdoq(q, coef, log2, ci, scanIdx)
	if nz == 0 {
		return nil
	}
	if hide {
		signHide(coef, c.orig[:n*n], log2, scanIdx, q)
	}
	levels := c.levels(n * n)
	copy(levels, coef)
	return levels
}

// pictureSSE is the squared error of the picture's reconstruction per
// plane.
func (sh *picShared) pictureSSE() [3]float64 {
	pic, src := sh.pic, sh.src
	w, h := sh.e.o.Width, sh.e.o.Height // the picture, not its padding
	return [3]float64{
		float64(ssdBlock(src.Y, src.StrideY, pic.y, pic.strideY, w, h)),
		float64(ssdBlock(src.Cb, src.StrideC, pic.cb, pic.strideC, w/2, h/2)),
		float64(ssdBlock(src.Cr, src.StrideC, pic.cr, pic.strideC, w/2, h/2)),
	}
}

// ssdCU is the squared error of the reconstruction of the square at
// (x0, y0), luma and chroma.
func (c *picCoder) ssdCU(x0, y0, size int) float64 {
	pic, src := c.pic, c.src
	ssd := ssdBlock(src.Y[y0*src.StrideY+x0:], src.StrideY, pic.y[y0*pic.strideY+x0:], pic.strideY, size, size)
	h := size / 2
	ssd += ssdBlock(src.Cb[y0/2*src.StrideC+x0/2:], src.StrideC, pic.cb[y0/2*pic.strideC+x0/2:], pic.strideC, h, h)
	ssd += ssdBlock(src.Cr[y0/2*src.StrideC+x0/2:], src.StrideC, pic.cr[y0/2*pic.strideC+x0/2:], pic.strideC, h, h)
	return float64(ssd)
}

// ssdBlockGo is the sum of squared differences of two w x h blocks.
func ssdBlockGo(a []uint16, as int, b []uint16, bs, w, h int) int64 {
	var ssd int64
	for y := range h {
		ra, rb := a[y*as:y*as+w], b[y*bs:y*bs+w]
		for x := range ra {
			d := int64(ra[x]) - int64(rb[x])
			ssd += d * d
		}
	}
	return ssd
}

// markCU records a CTU's final coding in the block state the loop
// filters read, as the decoder's parse would: the edges of its CUs,
// prediction units and transform units, and which transform blocks have
// luma levels.
func (c *picCoder) markCU(n *cuNode) {
	if n.split {
		for _, ch := range n.children {
			if ch != nil {
				c.markCU(ch)
			}
		}
		return
	}
	size := 1 << n.log2
	c.markEdges(n.x, n.y, size, size, 3)
	if n.inter && n.part != part2Nx2N {
		x, y, w, h := puRect(n.x, n.y, size, n.part, 1)
		c.markEdges(x, y, w, h, 2)
	}
	if n.tu != nil && (!n.inter || n.rootCbf) {
		c.markTU(n.tu)
	}
}

// markTU records a transform tree's unit edges and luma flags.
func (c *picCoder) markTU(t *tuNode) {
	if t.split {
		for _, ch := range t.children {
			c.markTU(ch)
		}
		return
	}
	size := 1 << t.log2
	c.markEdges(t.x, t.y, size, size, 1)
	if t.cbfLuma {
		c.ps.markCbf(&c.sd, t.x, t.y, size, true)
	}
}

// markEdges sets flags on the left and top edges of a block.
func (c *picCoder) markEdges(x0, y0, w, h int, flags uint8) {
	s, ps, sd := c.s, c.ps, &c.sd
	for y := y0; y < y0+h && y < s.height; y += 4 {
		ps.tuEdgeV[sd.blk4(x0, y)] |= flags
	}
	for x := x0; x < x0+w && x < s.width; x += 4 {
		ps.tuEdgeH[sd.blk4(x, y0)] |= flags
	}
}

// writeCU writes a coding quadtree node (split flags included) to bc.
func (c *picCoder) writeCU(bc binCoder, node *cuNode, depth int) {
	s := c.s
	size := 1 << node.log2
	fits := node.x+size <= s.width && node.y+size <= s.height
	if fits && node.log2 > s.log2MinCb {
		bc.decision(ctxSplitCodingUnitFlag+c.splitCtxInc(node.x, node.y, depth), boolInt(node.split))
	}
	if node.split {
		for _, ch := range node.children {
			if ch != nil {
				c.writeCU(bc, ch, depth+1)
			}
		}
		return
	}
	c.codeCU(bc, node)
}

// codeCU writes a coding unit's syntax (7.3.8.5) to bc, intra in an I
// slice: part_mode at the minimum size, the luma modes, the chroma mode,
// and the transform tree.
func (c *picCoder) codeCU(bc binCoder, node *cuNode) {
	s := c.s
	x0, y0, log2 := node.x, node.y, node.log2
	if c.h.typ != sliceI {
		bc.decision(ctxSkipFlag+c.skipCtxInc(x0, y0), boolInt(node.skip))
		if node.inter {
			c.codeInterCU(bc, node)
			return
		}
		bc.decision(ctxPredModeFlag, 1) // intra
	}
	if log2 == s.log2MinCb {
		bc.decision(ctxPartMode, boolInt(!node.nxn))
	}
	n, pb := 1, 1<<log2
	if node.nxn {
		n, pb = 4, pb/2
	}
	var cands [4][3]int
	var inMPM [4]int
	for i := range n {
		x, y := x0+(i&1)*pb, y0+(i>>1)*pb
		cands[i] = c.mpm(x, y)
		inMPM[i] = -1
		for j, m := range cands[i] {
			if m == node.modes[i] {
				inMPM[i] = j
			}
		}
		bc.decision(ctxPrevIntraLumaPredFlag, boolInt(inMPM[i] >= 0))
	}
	for i := range n {
		if j := inMPM[i]; j >= 0 {
			bc.bypass(boolInt(j > 0))
			if j > 0 {
				bc.bypass(boolInt(j > 1))
			}
			continue
		}
		rem := node.modes[i]
		for _, m := range cands[i] {
			if m < node.modes[i] {
				rem--
			}
		}
		bc.bypassBits(uint32(rem), 5) //nolint:gosec // 0..31
	}
	bc.decision(ctxIntraChromaPredMode, boolInt(node.chroma != 4))
	if node.chroma != 4 {
		bc.bypassBits(uint32(node.chroma), 2) //nolint:gosec // 0..3
	}
	c.codeTree(bc, node.tu, nil, node, 0)
}

// codeTree writes a transform tree node (7.3.8.8) and its units to bc;
// parent is the node it is a child of (nil at the root), blkIdx which.
func (c *picCoder) codeTree(bc binCoder, n, parent *tuNode, cu *cuNode, blkIdx int) {
	s := c.s
	maxDepth := s.maxTrDepthIntra
	if cu.nxn {
		maxDepth++
	}
	if cu.inter {
		maxDepth = s.maxTrDepthInter
	}
	parentCbf := [2]bool{true, true}
	if parent != nil {
		parentCbf = c.nodeCbfC(parent)
	}
	log2, depth := n.log2, n.depth
	if log2 <= s.log2MaxTb && log2 > s.log2MinTb && depth < maxDepth && (!cu.nxn || depth != 0) {
		bc.decision(ctxSplitTransformFlag+5-log2, boolInt(n.split))
	}
	var cbf [2]bool
	if log2 > 2 {
		if n.split {
			cbf = c.treeCbfC(n)
		} else {
			cbf = n.cbfC
		}
		for ci := range 2 {
			if depth == 0 || parentCbf[ci] {
				bc.decision(ctxCbfCbCr+depth, boolInt(cbf[ci]))
			}
		}
	} else {
		cbf = parentCbf
		if depth == 0 {
			cbf = [2]bool{false, false}
		}
	}
	if n.split {
		for i, ch := range n.children {
			c.codeTree(bc, ch, n, cu, i)
		}
		return
	}
	cbfLuma := n.cbfLuma
	if !cu.inter || depth != 0 || cbf[0] || cbf[1] {
		bc.decision(ctxCbfLuma+boolInt(depth == 0), boolInt(cbfLuma))
	}
	// The transform unit (7.3.8.10): luma, then chroma of its own or, for
	// a 4x4 block, the parent's with the last block.
	mode := cu.modes[0]
	if cu.nxn {
		mode = cu.modes[blkIdx]
		if log2 > 2 {
			mode = cu.modes[0]
		}
	}
	intra := !cu.inter
	if cbfLuma {
		writeResidual(bc, n.luma, log2, 0, scanIdxFor(intra, mode, log2, 0), c.e.c.signHiding)
	}
	chromaMode := cu.modes[0]
	if log2 > 2 {
		for ci := range 2 {
			if cbf[ci] {
				writeResidual(bc, n.chroma[ci], log2-1, ci+1, scanIdxFor(intra, chromaMode, log2-1, ci+1), c.e.c.signHiding)
			}
		}
	} else if blkIdx == 3 {
		for ci := range 2 {
			if cbf[ci] {
				writeResidual(bc, parent.chroma[ci], 2, ci+1, scanIdxFor(intra, chromaMode, 2, ci+1), c.e.c.signHiding)
			}
		}
	}
}

// nodeCbfC is a node's chroma cbfs, as coded or inferred: its own blocks'
// (a unit, or a split 8x8 node's 4x4 blocks), else its children's.
func (c *picCoder) nodeCbfC(n *tuNode) [2]bool {
	if n.split {
		return c.treeCbfC(n)
	}
	return n.cbfC
}

// treeCbfC is a split node's chroma cbfs: of its own 4x4 chroma blocks
// when its children are 4x4, else of its children.
func (c *picCoder) treeCbfC(n *tuNode) [2]bool {
	if n.log2 == 3 {
		return n.cbfC
	}
	var cbf [2]bool
	for _, ch := range n.children {
		var cc [2]bool
		if ch.split {
			cc = c.treeCbfC(ch)
		} else {
			cc = ch.cbfC
		}
		cbf[0] = cbf[0] || cc[0]
		cbf[1] = cbf[1] || cc[1]
	}
	return cbf
}

// satd is the sum of absolute Hadamard-transformed differences of two
// w x h blocks (multiples of 4; 8x8 transforms where both are multiples
// of 8).
func satd(a []uint16, as int, b []uint16, bs int, w, h int) int {
	sum := 0
	if w%8 == 0 && h%8 == 0 {
		for y := 0; y < h; y += 8 {
			x := 0
			for ; x+16 <= w; x += 16 {
				sum += satd16x8(a[y*as+x:], as, b[y*bs+x:], bs)
			}
			for ; x < w; x += 8 {
				sum += satd8(a[y*as+x:], as, b[y*bs+x:], bs)
			}
		}
		return sum
	}
	for y := 0; y < h; y += 4 {
		for x := 0; x < w; x += 4 {
			sum += satd4(a[y*as+x:], as, b[y*bs+x:], bs)
		}
	}
	return sum
}

func satd4(a []uint16, as int, b []uint16, bs int) int {
	var m [16]int32
	for y := range 4 {
		for x := range 4 {
			m[y*4+x] = int32(a[y*as+x]) - int32(b[y*bs+x])
		}
	}
	for y := range 4 {
		r := m[y*4 : y*4+4]
		s0, s1 := r[0]+r[1], r[0]-r[1]
		s2, s3 := r[2]+r[3], r[2]-r[3]
		r[0], r[1], r[2], r[3] = s0+s2, s1+s3, s0-s2, s1-s3
	}
	sum := int32(0)
	for x := range 4 {
		s0, s1 := m[x]+m[4+x], m[x]-m[4+x]
		s2, s3 := m[8+x]+m[12+x], m[8+x]-m[12+x]
		sum += iabs32(s0+s2) + iabs32(s1+s3) + iabs32(s0-s2) + iabs32(s1-s3)
	}
	return int((sum + 1) >> 1)
}

// sadBlockGo is the sum of absolute differences of two w x h blocks.
func sadBlockGo(a []uint16, as int, b []uint16, bs, w, h int) int {
	sum := 0
	for y := range h {
		ra, rb := a[y*as:y*as+w], b[y*bs:y*bs+w]
		for x := range ra {
			d := int(ra[x]) - int(rb[x])
			if d < 0 {
				d = -d
			}
			sum += d
		}
	}
	return sum
}

func satd8Go(a []uint16, as int, b []uint16, bs int) int {
	var m [64]int32
	// Rows: the differences, transformed.
	for y := range 8 {
		ar, br := a[y*as:y*as+8], b[y*bs:y*bs+8]
		d0 := int32(ar[0]) - int32(br[0])
		d1 := int32(ar[1]) - int32(br[1])
		d2 := int32(ar[2]) - int32(br[2])
		d3 := int32(ar[3]) - int32(br[3])
		d4 := int32(ar[4]) - int32(br[4])
		d5 := int32(ar[5]) - int32(br[5])
		d6 := int32(ar[6]) - int32(br[6])
		d7 := int32(ar[7]) - int32(br[7])
		s0, s1, s2, s3 := d0+d1, d0-d1, d2+d3, d2-d3
		s4, s5, s6, s7 := d4+d5, d4-d5, d6+d7, d6-d7
		t0, t1, t2, t3 := s0+s2, s1+s3, s0-s2, s1-s3
		t4, t5, t6, t7 := s4+s6, s5+s7, s4-s6, s5-s7
		r := m[y*8 : y*8+8]
		r[0], r[1], r[2], r[3] = t0+t4, t1+t5, t2+t6, t3+t7
		r[4], r[5], r[6], r[7] = t0-t4, t1-t5, t2-t6, t3-t7
	}
	// Columns, summed.
	sum := int32(0)
	for x := range 8 {
		d0, d1, d2, d3 := m[x], m[8+x], m[16+x], m[24+x]
		d4, d5, d6, d7 := m[32+x], m[40+x], m[48+x], m[56+x]
		s0, s1, s2, s3 := d0+d1, d0-d1, d2+d3, d2-d3
		s4, s5, s6, s7 := d4+d5, d4-d5, d6+d7, d6-d7
		t0, t1, t2, t3 := s0+s2, s1+s3, s0-s2, s1-s3
		t4, t5, t6, t7 := s4+s6, s5+s7, s4-s6, s5-s7
		sum += iabs32(t0+t4) + iabs32(t1+t5) + iabs32(t2+t6) + iabs32(t3+t7)
		sum += iabs32(t0-t4) + iabs32(t1-t5) + iabs32(t2-t6) + iabs32(t3-t7)
	}
	return int((sum + 2) >> 2)
}
