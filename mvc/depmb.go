package mvc

import (
	"math"
	"runtime"
	"sync"
	"sync/atomic"
)

// The dependent view's macroblock coder. Its pictures are macroblocks
// predicted from two kinds of reference: the base view picture of the
// access unit (inter-view) and the dependent view's own earlier pictures
// (temporal). Anchor pictures are P slices predicting from the base view
// alone. Other pictures are B slices: list 0 holds the latest temporal
// reference then the base view picture, list 1 the next temporal
// reference when one is decoded already (the base view's picture being a
// B picture between two of its references), else the latest again. A
// macroblock is then skipped or direct (its motion derived from its
// neighbours), or predicted in one 16x16 partition or two (16x8, 8x16),
// each from one reference or from two averaged: the base view with a
// temporal reference, or the temporal references either side. The
// residual is coded in 4x4 or 8x8 transforms. Every choice is made by its
// rate-distortion cost: the squared error of the reconstruction, which
// the coder models as the decoder makes it, plus the bits the arithmetic
// coder would spend. The reference pictures are the decoder's own
// reconstructions, so prediction here is the decoder's prediction sample
// for sample.

// DepSource is a dependent view picture to encode: 4:2:0 8-bit planes of
// the coded size (whole macroblocks).
type DepSource struct {
	Y, Cb, Cr        []byte
	StrideY, StrideC int
}

// depMB is what neighbouring macroblocks need to know of a coded one: its
// motion per list and 8x8 quadrant (partitions and direct prediction
// differ between quadrants), as the decoder keeps it per 4x4 block.
type depMB struct {
	skip   bool           // P_Skip or B_Skip
	direct bool           // B_Skip or B_Direct_16x16
	ref    [2][4]int8     // reference index per list and quadrant; -1 for none
	mv     [2][4]mv       // vector per list and quadrant
	amvd   [2][4][2]uint8 // |mvd| per list, quadrant and component, capped at 64 as the decoder does
	t8     bool           // transform_size_8x8_flag
	cbp    uint8          // bits 0-3 luma 8x8, bits 4-5 chroma (0, 1, 2)
	cbf    uint32         // coded_block_flag bits in the decoder's layout
	intra  int            // intraNone, intra16x16, intra4x4 or intra8x8
	chroma uint8          // intra_chroma_pred_mode
}

// B macroblock types coded here (Table 7-14): the 16x16 ones; the two
// partition ones are looked up in bMBTypeInfo.
const (
	bDirect16x16 = 0
	bL016x16     = 1
	bL116x16     = 2
	bBi16x16     = 3
)

// depCoder codes the macroblocks of one dependent view picture.
type depCoder struct {
	trellis  bool         // trellis quantisation of the residual
	tc       trellisCosts // its costs and scratch
	mbW, mbH int
	qp, qpc  int
	mfT      [4][8]int32 // the luma quantiser's multipliers, for fdctQuantLuma
	mfTC     [4][8]int32 // the chroma's
	// Adaptive quantisation: aq is its strength (0: off), picQP the
	// picture's quantiser (the slice's), mbQP each macroblock's from it
	// and the block's energy (planQP). The writers track qpY, the
	// quantiser in force after the last macroblock written, and lastDQ,
	// whether it wrote a nonzero mb_qp_delta, for the next one's delta
	// and context; only the slice's coder (not an estimate) moves them.
	aq     float64
	picQP  int
	mbQP   []int8
	treeQP []float32 // the macroblock tree's offsets for the picture (nil: none)
	// the lookahead's motion against each reference, by list (nil: none)
	lowMV    [2][4]*lowMotion
	mbEnergy []float64
	qpY      int
	lastDQ   int
	curQP    int     // the quantiser of the macroblock being written
	lambda   float64 // SAD domain
	lambda2  float64 // SSD domain
	src      DepSource

	// The reference lists. A P slice's list 0 is the base view picture
	// and, when there is one, the latest temporal reference; a B slice's
	// list 0 is the latest temporal reference then the base view picture
	// and its list 1 the next temporal reference, or the latest again.
	list   [2][]*picture
	bslice bool
	islice bool
	// interView marks a dependent view coder: a P slice's first
	// reference, a B slice's second of list 0, is the base view picture
	// (searched for a disparity).
	interView bool

	// the picture's reconstruction so far, before deblocking, for intra
	// prediction, and the intra 4x4 prediction modes per 4x4 block (2
	// where there are none)
	rec    *picture
	imodes []int8

	cur []depMB
	// the previous picture's vectors per reference kind (0: base view,
	// 1: the latest temporal reference, 2: the next), as search starting
	// points
	prev [3][]mv
	mc   mcBuf
	e    binCoder      // what the macroblock writers code into
	enc  *cabacEncoder // the slice's arithmetic coder
	cost cabacCost     // and the estimate of candidates' bits
	// a wavefront worker's model of the coder's context states, the
	// slice's wavefront, and the writer's own grids
	model   cabacCost
	wf      *wavefront
	wfBuf   *wavefront
	wcur    []depMB
	wimodes []int8
	pred    [256]byte
	predCb  [64]byte
	predCr  [64]byte
	// the candidates of the macroblock being decided, and the skip
	// probe's residual
	cands [24]cand
	ncand int
	probe residual

	// the slice being coded: macroblock rows [rowStart, rowEnd)
	rowStart, rowEnd int
	tag              int // the picture, for the hooks: the base view's display index, the dependent view's POC

	stats depStats
}

// depStats counts macroblocks by how they were predicted.
type depStats struct {
	skip, direct, interView, temporal, bi, parts, intra int
}

func (s *depStats) add(t depStats) {
	s.skip += t.skip
	s.direct += t.direct
	s.interView += t.interView
	s.temporal += t.temporal
	s.bi += t.bi
	s.parts += t.parts
	s.intra += t.intra
}

// Quantisation multipliers (MF) and the decoder's level scales (8.5.9,
// flat scaling), raster order, by qp%6.
var (
	quantMF  [6][16]int32
	quant8MF [6][64]int32
	scale4   [6][16]int32
	scale8   [6][64]int32
)

func init() {
	v4 := [6][3]int32{{10, 16, 13}, {11, 18, 14}, {13, 20, 16}, {14, 23, 18}, {16, 25, 20}, {18, 29, 23}}
	mf4 := [6][3]int32{{13107, 5243, 8066}, {11916, 4660, 7490}, {10082, 4194, 6554}, {9362, 3647, 5825}, {8192, 3355, 5243}, {7282, 2893, 4559}}
	for q := 0; q < 6; q++ {
		for i := 0; i < 16; i++ {
			x, y := i&3, i>>2
			k := 2
			switch {
			case x&1 == 0 && y&1 == 0:
				k = 0
			case x&1 == 1 && y&1 == 1:
				k = 1
			}
			quantMF[q][i] = mf4[q][k]
			scale4[q][i] = 16 * v4[q][k]
		}
	}
	v8 := [6][6]int32{{20, 18, 32, 19, 25, 24}, {22, 19, 35, 21, 28, 26}, {26, 23, 42, 24, 33, 31}, {28, 25, 45, 26, 35, 33}, {32, 28, 51, 30, 40, 38}, {36, 32, 58, 34, 46, 43}}
	mf8 := [6][6]int32{{13107, 11428, 20972, 12222, 16777, 15481}, {11916, 10826, 19174, 11058, 14980, 14290}, {10082, 8943, 15978, 9675, 12710, 11985}, {9362, 8228, 14913, 8931, 11984, 11259}, {8192, 7346, 13159, 7740, 10486, 9777}, {7282, 6428, 11570, 6830, 9118, 8640}}
	for q := 0; q < 6; q++ {
		for i := 0; i < 64; i++ {
			x, y := i&7, i>>3
			var k int
			switch {
			case x&3 == 0 && y&3 == 0:
				k = 0
			case x&1 == 1 && y&1 == 1:
				k = 1
			case x&3 == 2 && y&3 == 2:
				k = 2
			case x&3 == 0 && y&1 == 1 || x&1 == 1 && y&3 == 0:
				k = 3
			case x&3 == 0 && y&3 == 2 || x&3 == 2 && y&3 == 0:
				k = 4
			default:
				k = 5
			}
			quant8MF[q][i] = mf8[q][k]
			scale8[q][i] = 16 * v8[q][k]
		}
	}
	initTrellis()
}

func newDepCoder(mbW, mbH, qp int) *depCoder {
	c := &depCoder{mbW: mbW, mbH: mbH, trellis: true}
	c.planQP(qp)
	c.cur = make([]depMB, mbW*mbH)
	for k := range c.prev {
		c.prev[k] = make([]mv, mbW*mbH)
	}
	c.rec = allocNewPicture(mbW, mbH)
	c.imodes = make([]int8, mbW*4*mbH*4)
	for i := range c.imodes {
		c.imodes[i] = 2
	}
	c.rowEnd = mbH
	return c
}

// refKind maps a list and reference index to a reference kind: 0 the
// base view picture, 1 a temporal reference of list 0, 2 of list 1.
func (c *depCoder) refKind(l, r int) int {
	if l == 1 {
		return 2
	}
	if !c.interView {
		return 1
	}
	if c.bslice {
		return 1 - r
	}
	return r
}

// setQP sets the quantiser of the pictures to come.
func (c *depCoder) setQP(qp int) {
	c.qp, c.qpc = qp, int(chromaQPTable[qp])
	c.mfT = mfTranspose(&quantMF[qp%6])
	c.mfTC = mfTranspose(&quantMF[c.qpc%6])
	c.lambda2 = lambda2Tab[qp]
	if c.bslice {
		// B pictures' errors propagate less (into none but the
		// pyramid's), so they buy fewer bits with them
		c.lambda2 *= 1.4
	}
	c.lambda = math.Sqrt(c.lambda2)
}

// lambda2Tab is the rate-distortion multiplier (squared error against
// bits) by quantiser.
var lambda2Tab = func() (t [52]float64) {
	for qp := range t {
		t[qp] = 0.85 * math.Pow(2, float64(qp-12)/3)
	}
	return t
}()

// planQP sets the picture's quantiser and each macroblock's: with
// adaptive quantisation, the flat and dark ones lower (they show
// quantisation noise, and are cheap), the busy ones higher, by the
// energy of the block as x264's variance mode does.
func (c *depCoder) planQP(qp int) {
	c.picQP = qp
	c.setQP(qp)
	if len(c.mbQP) < c.mbW*c.mbH {
		c.mbQP = make([]int8, c.mbW*c.mbH)
	}
	for i := range c.mbQP[:c.mbW*c.mbH] {
		q := qp
		if c.treeQP != nil {
			q = max(0, min(51, qp+int(math.Round(float64(c.treeQP[i])))))
		}
		c.mbQP[i] = int8(q)
	}
	if c.aq <= 0 {
		return
	}
	// Each macroblock's log energy, and the offsets about the picture's
	// mean, so that the picture's quantiser stays its average and the
	// bits move within the picture rather than growing with its
	// flatness.
	n := c.mbW * c.mbH
	if len(c.mbEnergy) < n {
		c.mbEnergy = make([]float64, n)
	}
	mean := 0.0
	for mbY := 0; mbY < c.mbH; mbY++ {
		for mbX := 0; mbX < c.mbW; mbX++ {
			var energy int64
			for b := 0; b < 4; b++ {
				energy += blockVariance(c.src.Y, (mbY*16+(b>>1)*8)*c.src.StrideY+mbX*16+(b&1)*8, c.src.StrideY)
			}
			co := mbY*8*c.src.StrideC + mbX*8
			energy += blockVariance(c.src.Cb, co, c.src.StrideC) + blockVariance(c.src.Cr, co, c.src.StrideC)
			e := math.Log2(float64(max(energy, 1)))
			c.mbEnergy[mbY*c.mbW+mbX] = e
			mean += e
		}
	}
	mean /= float64(n)
	if AQAbsolute {
		mean = 14.427 // x264's constant: a picture's flatness lowers its quantiser
	}
	strength := c.aq * 1.0397
	for i, e := range c.mbEnergy[:n] {
		adj := max(-6, min(6, strength*(e-mean)))
		if c.treeQP != nil {
			adj += float64(c.treeQP[i])
		}
		c.mbQP[i] = int8(max(0, min(51, qp+int(math.Round(adj)))))
	}
}

// AQAbsolute makes adaptive quantisation offset each macroblock from a
// fixed energy (x264's), not the picture's mean: a measurement switch.
var AQAbsolute bool

// blockVariance is the energy of an 8x8 block about its mean, times 64:
// the sum of squares less the square of the sum over 64.
func blockVariance(p []byte, o, stride int) int64 {
	var sum, sq int64
	for y := 0; y < 8; y++ {
		for _, v := range p[o+y*stride : o+y*stride+8] {
			sum += int64(v)
			sq += int64(v) * int64(v)
		}
	}
	return sq - sum*sum/64
}

// neighbour returns the macroblock at (x,y) if it is inside the slice (and
// so, in raster order above or left of the current one, coded).
func (c *depCoder) neighbour(x, y int) (*depMB, bool) {
	if x < 0 || y < c.rowStart || x >= c.mbW || y >= c.rowEnd {
		return nil, false
	}
	return &c.cur[y*c.mbW+x], true
}

// quadrant is the 8x8 quadrant holding the 4x4 block (x,y) of a
// macroblock.
func quadrant(x, y int) int { return (y&3)>>1*2 + (x&3)>>1 }

// nb returns list l's reference index and vector of the 4x4 block (x,y)
// relative to macroblock (mbX,mbY), in 4x4 units, as the decoder's nb
// does: a block of the macroblock itself (its partitions coded so far),
// or of a neighbour; -1 and unavailable outside the slice or to the
// right.
func (c *depCoder) nb(l, mbX, mbY, x, y int) (int8, mv, bool) {
	if y >= 0 && x >= 4 {
		return -1, mv{}, false
	}
	m, ok := c.neighbour(mbX+x>>2, mbY+y>>2)
	if !ok {
		return -1, mv{}, false
	}
	q := quadrant(x, y)
	return m.ref[l][q], m.mv[l][q], true
}

// nbC is a partition's neighbour C (above right), or D (above left) in
// its place, for the partition at (x,y) of width w in 4x4 units.
func (c *depCoder) nbC(l, mbX, mbY, x, y, w int) (int8, mv, bool) {
	cx, cy := x+w, y-1
	if cy >= 0 && (cx >= 4 || blkOrder[cy*4+cx] > blkOrder[y*4+x]) {
		return c.nb(l, mbX, mbY, x-1, y-1)
	}
	if r, m, ok := c.nb(l, mbX, mbY, cx, cy); ok {
		return r, m, ok
	}
	return c.nb(l, mbX, mbY, x-1, y-1)
}

// mvPred is the motion vector predictor in list l of the partition at
// (x,y) of size w x h, in 4x4 units (8.4.1.3).
func (c *depCoder) mvPred(l int, ref int8, mbX, mbY, x, y, w, h int) mv {
	rA, mA, aA := c.nb(l, mbX, mbY, x-1, y)
	rB, mB, aB := c.nb(l, mbX, mbY, x, y-1)
	rC, mC, aC := c.nbC(l, mbX, mbY, x, y, w)
	if w == 4 && h == 2 {
		if y == 0 {
			if rB == ref {
				return mB
			}
		} else if rA == ref {
			return mA
		}
	} else if w == 2 && h == 4 {
		if x == 0 {
			if rA == ref {
				return mA
			}
		} else if rC == ref {
			return mC
		}
	}
	if !aB && !aC && aA {
		return mA
	}
	eq := 0
	var m mv
	if rA == ref {
		eq++
		m = mA
	}
	if rB == ref {
		eq++
		m = mB
	}
	if rC == ref {
		eq++
		m = mC
	}
	if eq == 1 {
		return m
	}
	return mv{median3(mA.x, mB.x, mC.x), median3(mA.y, mB.y, mC.y)}
}

// skipMV is the motion vector of a P_Skip macroblock (8.4.1.1).
func (c *depCoder) skipMV(mbX, mbY int) mv {
	rA, mA, aA := c.nb(0, mbX, mbY, -1, 0)
	rB, mB, aB := c.nb(0, mbX, mbY, 0, -1)
	if !aA || !aB || rA == 0 && mA == (mv{}) || rB == 0 && mB == (mv{}) {
		return mv{}
	}
	return c.mvPred(0, 0, mbX, mbY, 0, 0, 4, 4)
}

// directMotion derives the spatial direct prediction of the macroblock
// (8.4.1.2.2) per 8x8 quadrant, as the decoder does with
// direct_8x8_inference_flag: each quadrant's colocated block is the
// corner 4x4 block of the quadrant in RefPicList1[0].
func (c *depCoder) directMotion(mbX, mbY int, ref *[2][4]int8, m *[2][4]mv) {
	var pref [2]int8
	var pmv [2]mv
	for l := 0; l < 2; l++ {
		rA, _, _ := c.nb(l, mbX, mbY, -1, 0)
		rB, _, _ := c.nb(l, mbX, mbY, 0, -1)
		rC, _, _ := c.nbC(l, mbX, mbY, 0, 0, 4)
		pref[l] = minPositive(rA, minPositive(rB, rC))
		if pref[l] >= 0 {
			pmv[l] = c.mvPred(l, pref[l], mbX, mbY, 0, 0, 4, 4)
		}
	}
	directZero := pref[0] < 0 && pref[1] < 0
	if directZero {
		pref = [2]int8{0, 0}
	}
	col := c.list[1][0]
	for q := 0; q < 4; q++ {
		colZero := false
		if !directZero {
			bx, by := (q&1)*3, (q>>1)*3
			i := (mbY*4+by)*col.mbW*4 + mbX*4 + bx
			rc, mc := col.refs[0][i], col.mvs[0][i]
			if rc < 0 {
				rc, mc = col.refs[1][i], col.mvs[1][i]
			}
			colZero = rc == 0 && mc.x >= -1 && mc.x <= 1 && mc.y >= -1 && mc.y <= 1
		}
		for l := 0; l < 2; l++ {
			ref[l][q] = pref[l]
			if directZero || pref[l] < 0 || pref[l] == 0 && colZero {
				m[l][q] = mv{}
			} else {
				m[l][q] = pmv[l]
			}
		}
	}
}

// seBits estimates the bits of a signed value's code.
func seBits(v int) int {
	if v < 0 {
		v = -v
	}
	n := 1
	for x := 2*v + 1; x > 1; x >>= 1 {
		n += 2
	}
	return n
}

func (c *depCoder) mvCost(m, p mv) float64 {
	return c.lambda * float64(seBits(int(m.x-p.x))+seBits(int(m.y-p.y)))
}

// sadInt is the luma SAD of the pw x ph block at (px,py) against ref at an
// integer offset, every step-th row.
func (c *depCoder) sadInt(ref *picture, px, py, pw, ph, dx, dy, step int) int {
	ss, rs := c.src.StrideY, ref.stride[0]
	return sadBlock(c.src.Y, py*ss+px, ss*step, ref.planes[0], ref.origin[0]+(py+dy)*rs+px+dx, rs*step, pw, ph/step) * step
}

// sadGeneric is the sum of absolute differences between the w x h blocks
// at src[so] and ref[ro].
func sadGeneric(src []byte, so, ss int, ref []byte, ro, rs, w, h int) int {
	sum := 0
	for y := 0; y < h; y++ {
		s := src[so+y*ss : so+y*ss+w]
		r := ref[ro+y*rs : ro+y*rs+w]
		for i := range s {
			d := int(s[i]) - int(r[i])
			if d < 0 {
				d = -d
			}
			sum += d
		}
	}
	return sum
}

// satd is the sum of 4x4 Hadamard transformed differences between the
// pw x ph source block at (px,py) and a prediction of stride 16.
// satdAvgGeneric is satdGeneric against the rounded average of two
// blocks.
func satdAvgGeneric(src []byte, so, ss int, a []byte, ao, as int, b []byte, bo, bs, w, h int) int {
	var pred [256]byte
	avgIntoGeneric(pred[:], 16, a[ao:], as, b[bo:], bs, w, h)
	return satdGeneric(src, so, ss, pred[:], 0, 16, w, h)
}

// satdAt is the SATD of the source block at (px,py) of size pw x ph
// against the prediction at quarter-sample position (qx,qy) of ref, read
// from its half-sample planes.
func (c *depCoder) satdAt(ref *picture, px, py, pw, ph, qx, qy int) int {
	ix, iy := qx>>2, qy>>2
	fx, fy := qx&3, qy&3
	if ix < -pw-3 {
		ix = -pw - 3
	} else if ix > ref.width+2 {
		ix = ref.width + 2
	}
	if iy < -ph-3 {
		iy = -ph - 3
	} else if iy > ref.height+2 {
		iy = ref.height + 2
	}
	g, b, v, j := ref.planes[0], ref.hpel[0], ref.hpel[1], ref.hpel[2]
	ss := ref.stride[0]
	so := ref.origin[0] + iy*ss + ix
	src, o, st := c.src.Y, py*c.src.StrideY+px, c.src.StrideY
	switch fx | fy<<2 {
	case 0:
		return satdBlock(src, o, st, g, so, ss, pw, ph)
	case 2:
		return satdBlock(src, o, st, b, so, ss, pw, ph)
	case 1, 3:
		return satdAvgBlock(src, o, st, b, so, ss, g, so+fx>>1, ss, pw, ph)
	case 8:
		return satdBlock(src, o, st, v, so, ss, pw, ph)
	case 4, 12:
		return satdAvgBlock(src, o, st, v, so, ss, g, so+(fy>>1)*ss, ss, pw, ph)
	case 10:
		return satdBlock(src, o, st, j, so, ss, pw, ph)
	case 6, 14:
		return satdAvgBlock(src, o, st, j, so, ss, b, so+(fy>>1)*ss, ss, pw, ph)
	case 9, 11:
		return satdAvgBlock(src, o, st, j, so, ss, v, so+fx>>1, ss, pw, ph)
	default:
		return satdAvgBlock(src, o, st, b, so+(fy>>1)*ss, ss, v, so+fx>>1, ss, pw, ph)
	}
}

func (c *depCoder) satd(px, py, pw, ph int, pred []byte) int {
	return satdBlock(c.src.Y, py*c.src.StrideY+px, c.src.StrideY, pred, 0, 16, pw, ph)
}

// satdGeneric is the sum of 4x4 Hadamard transformed differences between
// the w x h blocks at src[so] and pred[po], halved.
func satdGeneric(src []byte, so, ss int, pred []byte, po, ps, w, h int) int {
	sum := 0
	var d [16]int
	for by := 0; by < h; by += 4 {
		for bx := 0; bx < w; bx += 4 {
			for j := 0; j < 4; j++ {
				o := so + (by+j)*ss + bx
				p := po + (by+j)*ps + bx
				for i := 0; i < 4; i++ {
					d[j*4+i] = int(src[o+i]) - int(pred[p+i])
				}
			}
			for j := 0; j < 4; j++ {
				a0, a1 := d[j*4]+d[j*4+1], d[j*4]-d[j*4+1]
				a2, a3 := d[j*4+2]+d[j*4+3], d[j*4+2]-d[j*4+3]
				d[j*4], d[j*4+1], d[j*4+2], d[j*4+3] = a0+a2, a1+a3, a0-a2, a1-a3
			}
			for i := 0; i < 4; i++ {
				a0, a1 := d[i]+d[4+i], d[i]-d[4+i]
				a2, a3 := d[8+i]+d[12+i], d[8+i]-d[12+i]
				for _, v := range [4]int{a0 + a2, a1 + a3, a0 - a2, a1 - a3} {
					if v < 0 {
						v = -v
					}
					sum += v
				}
			}
		}
	}
	return sum / 2
}

// clampMV keeps a pw x ph block at (px,py) within the reference's padded
// area.
func (c *depCoder) clampMV(px, py, pw, ph int, m mv) mv {
	w, h := c.mbW*16, c.mbH*16
	minX, maxX := (-px-20)*4, (w-px-pw+20)*4
	minY, maxY := (-py-20)*4, (h-py-ph+20)*4
	m.x = int16(max(minX, min(maxX, int(m.x))))
	m.y = int16(max(minY, min(maxY, int(m.y))))
	return m
}

// search finds a vector in reference r of list l for the partition at
// (x,y) of size w x h (4x4 units) of macroblock (mbX,mbY): candidates, a
// wide horizontal scan for the base view when asked, an integer diamond,
// then half and quarter sample refinement by SATD. It returns the vector
// and its cost (SATD plus vector bits).
func (c *depCoder) search(mbX, mbY, x, y, w, h, l, r int, extra []mv, wide bool) (mv, float64) {
	ref := c.list[l][r]
	px, py, pw, ph := mbX*16+x*4, mbY*16+y*4, w*4, h*4
	pmv := c.mvPred(l, int8(r), mbX, mbY, x, y, w, h)
	best := mv{}
	bestCost := math.MaxFloat64
	tryInt := func(dx, dy, step int) {
		m := c.clampMV(px, py, pw, ph, mv{int16(dx * 4), int16(dy * 4)})
		dx, dy = int(m.x)/4, int(m.y)/4
		cost := float64(c.sadInt(ref, px, py, pw, ph, dx, dy, step)) + c.mvCost(m, pmv)
		if cost < bestCost {
			bestCost, best = cost, m
		}
	}
	round := func(v int16) int { return (int(v) + 2) >> 2 }
	tryInt(0, 0, 1)
	tryInt(round(pmv.x), round(pmv.y), 1)
	for _, m := range extra {
		tryInt(round(m.x), round(m.y), 1)
	}
	if wide && c.refKind(l, r) == 0 {
		// The inter-view disparity is mostly horizontal: scan a wide range
		// on every other row, then refine from the best.
		coarse, cbest := best, math.MaxFloat64
		for dy := -2; dy <= 2; dy += 2 {
			for dx := -96; dx <= 96; dx += 4 {
				m := c.clampMV(px, py, pw, ph, mv{int16(dx * 4), int16(dy * 4)})
				cost := float64(c.sadInt(ref, px, py, pw, ph, int(m.x)/4, int(m.y)/4, 2)) + c.mvCost(m, pmv)
				if cost < cbest {
					cbest, coarse = cost, m
				}
			}
		}
		tryInt(int(coarse.x)/4, int(coarse.y)/4, 1)
	}
	// integer diamonds, coarse to fine: past a wrong start's local minimum
	for _, step := range [3]int{4, 2, 1} {
		for it := 0; it < 32; it++ {
			cx, cy := int(best.x)/4, int(best.y)/4
			prev := best
			for _, d := range [4][2]int{{-step, 0}, {step, 0}, {0, -step}, {0, step}} {
				tryInt(cx+d[0], cy+d[1], 1)
			}
			if best == prev {
				break
			}
		}
	}
	// sub-sample refinement on SATD
	bestCost = math.MaxFloat64
	trySub := func(m mv) {
		m = c.clampMV(px, py, pw, ph, m)
		cost := float64(c.satdAt(ref, px, py, pw, ph, px*4+int(m.x), py*4+int(m.y))) + c.mvCost(m, pmv)
		if cost < bestCost {
			bestCost, best = cost, m
		}
	}
	center := best
	trySub(center)
	for _, step := range [2]int16{2, 1} {
		center = best
		for _, d := range [8][2]int16{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}} {
			trySub(mv{center.x + d[0]*step, center.y + d[1]*step})
		}
	}
	return best, bestCost
}

// residual holds a macroblock's quantised coefficients in scan order:
// the luma in 4x4 or, with t8, 8x8 transforms.
type residual struct {
	luma   [16][16]int32 // [raster 4x4 block][zigzag]
	luma8  [4][64]int32  // [8x8 block][zigzag]
	t8     bool
	i16    bool      // I_16x16: the DC coefficients in dcY, luma holds AC levels from index 1
	dcY    [16]int32 // [zigzag]
	dc     [2][4]int32
	ac     [2][4][15]int32 // [comp][raster block][zigzag 1..15]
	cbp    uint8
	nonzer int // number of nonzero levels
}

// fdct8 is the forward 8x8 transform of a block (raster), rows then
// columns: the integer transform whose inverse is 8.5.13.
func fdct8(d *[64]int32) {
	for j := 0; j < 8; j++ {
		r := d[j*8 : j*8+8]
		dct8Pass(r[0], r[1], r[2], r[3], r[4], r[5], r[6], r[7], r, 1)
	}
	for i := 0; i < 8; i++ {
		dct8Pass(d[i], d[8+i], d[16+i], d[24+i], d[32+i], d[40+i], d[48+i], d[56+i], d[i:], 8)
	}
}

// dct8Pass is one dimension of fdct8, writing its outputs to out[0],
// out[step], ...
func dct8Pass(s0, s1, s2, s3, s4, s5, s6, s7 int32, out []int32, step int) {
	s07, s16, s25, s34 := s0+s7, s1+s6, s2+s5, s3+s4
	a0, a1, a2, a3 := s07+s34, s16+s25, s07-s34, s16-s25
	d07, d16, d25, d34 := s0-s7, s1-s6, s2-s5, s3-s4
	a4 := d16 + d25 + (d07 + d07>>1)
	a5 := d07 - d34 - (d25 + d25>>1)
	a6 := d07 + d34 - (d16 + d16>>1)
	a7 := d16 - d25 + (d34 + d34>>1)
	out[0] = a0 + a1
	out[step] = a4 + a7>>2
	out[2*step] = a2 + a3>>1
	out[3*step] = a5 + a6>>2
	out[4*step] = a0 - a1
	out[5*step] = a6 - a5>>2
	out[6*step] = a2>>1 - a3
	out[7*step] = a4>>2 - a7
}

// encIDCT4 is the inverse 4x4 transform (8.5.12) of dequantised coefficients
// (raster), without the final rounding.
func encIDCT4(d *[16]int32) {
	for j := 0; j < 4; j++ {
		r := d[j*4 : j*4+4]
		e, f := r[0]+r[2], r[0]-r[2]
		g, h := r[1]>>1-r[3], r[1]+r[3]>>1
		r[0], r[1], r[2], r[3] = e+h, f+g, f-g, e-h
	}
	for i := 0; i < 4; i++ {
		e, f := d[i]+d[8+i], d[i]-d[8+i]
		g, h := d[4+i]>>1-d[12+i], d[4+i]+d[12+i]>>1
		d[i], d[4+i], d[8+i], d[12+i] = e+h, f+g, f-g, e-h
	}
}

// encIDCT8 is the inverse 8x8 transform (8.5.13) of dequantised
// coefficients (raster), without the final rounding.
func encIDCT8(d *[64]int32) {
	for j := 0; j < 8; j++ {
		r := d[j*8 : j*8+8]
		idct8Pass(r[0], r[1], r[2], r[3], r[4], r[5], r[6], r[7], r, 1)
	}
	for i := 0; i < 8; i++ {
		idct8Pass(d[i], d[8+i], d[16+i], d[24+i], d[32+i], d[40+i], d[48+i], d[56+i], d[i:], 8)
	}
}

// idct8Pass is one dimension of encIDCT8.
func idct8Pass(d0, d1, d2, d3, d4, d5, d6, d7 int32, out []int32, step int) {
	a0, a4 := d0+d4, d0-d4
	a2, a6 := d2>>1-d6, d2+d6>>1
	b0, b2, b4, b6 := a0+a6, a4+a2, a4-a2, a0-a6
	a1 := -d3 + d5 - d7 - d7>>1
	a3 := d1 + d7 - d3 - d3>>1
	a5 := -d1 + d7 + d5 + d5>>1
	a7 := d3 + d5 + d1 + d1>>1
	b1, b7 := a1+a7>>2, a7-a1>>2
	b3, b5 := a3+a5>>2, a3>>2-a5
	out[0] = b0 + b7
	out[step] = b2 + b5
	out[2*step] = b4 + b3
	out[3*step] = b6 + b1
	out[4*step] = b6 - b1
	out[5*step] = b4 - b3
	out[6*step] = b2 - b5
	out[7*step] = b0 - b7
}

func encClip(v int32) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}

// reconLuma reconstructs the macroblock's luma from its prediction and
// residual as the decoder will, into dst (stride 16).
func (c *depCoder) reconLuma(res *residual, dst *[256]byte) {
	*dst = c.pred
	qp := c.qp
	if res.t8 {
		for b8 := 0; b8 < 4; b8++ {
			if res.cbp>>b8&1 == 0 {
				continue
			}
			var d [64]int32
			ls := &scale8[qp%6]
			for k, l := range res.luma8[b8] {
				if l == 0 {
					continue
				}
				p := int(zigzag8x8[k])
				if qp >= 36 {
					d[p] = l * ls[p] << (qp/6 - 6)
				} else {
					d[p] = (l*ls[p] + 1<<(5-qp/6)) >> (6 - qp/6)
				}
			}
			encIDCT8(&d)
			x0, y0 := (b8&1)*8, (b8>>1)*8
			for y := 0; y < 8; y++ {
				for x := 0; x < 8; x++ {
					o := (y0+y)*16 + x0 + x
					dst[o] = encClip(int32(c.pred[o]) + (d[y*8+x]+32)>>6)
				}
			}
		}
		return
	}
	var dcY [16]int32
	if res.i16 {
		// the DC block, inverse transformed and scaled (8.5.10)
		var t [16]int32
		for k, l := range res.dcY {
			t[zigzag4x4[k]] = l
		}
		lumaDCDequant(&t, qp, scale4[qp%6][0])
		dcY = t
	}
	for r := 0; r < 16; r++ {
		if res.cbp>>((r>>3)*2+(r&3)>>1)&1 == 0 && !res.i16 {
			continue
		}
		var d [16]int32
		ls := &scale4[qp%6]
		nz := false
		if res.i16 {
			d[0] = dcY[r]
			nz = d[0] != 0
		}
		for k, l := range res.luma[r] {
			if l == 0 || res.i16 && (k == 0 || res.cbp&15 == 0) {
				continue
			}
			nz = true
			p := int(zigzag4x4[k])
			d[p] = encDequant4(l, ls[p], qp)
		}
		if !nz {
			continue
		}
		encIDCT4(&d)
		x0, y0 := (r&3)*4, (r>>2)*4
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				o := (y0+y)*16 + x0 + x
				dst[o] = encClip(int32(c.pred[o]) + (d[y*4+x]+32)>>6)
			}
		}
	}
}

// ssdLuma is the squared error of a luma reconstruction (stride 16)
// against the source macroblock.
func (c *depCoder) ssdLuma(mbX, mbY int, rec *[256]byte) int64 {
	var sum int64
	for y := 0; y < 16; y++ {
		so := (mbY*16+y)*c.src.StrideY + mbX*16
		s := c.src.Y[so : so+16]
		r := rec[y*16 : y*16+16]
		for i := range 16 {
			d := int64(s[i]) - int64(r[i])
			sum += d * d
		}
	}
	return sum
}

// levelBits estimates the bits of a block's levels in scan order: the
// significance map to the last one and each level's size.
func levelBits(lv []int32) int {
	bits, last := 0, -1
	for i, l := range lv {
		if l == 0 {
			continue
		}
		last = i
		if l < 0 {
			l = -l
		}
		bits += 2
		for l > 1 {
			l >>= 1
			bits += 2
		}
	}
	if last < 0 {
		return 1
	}
	return bits + last + 2
}

// fdct4 is the forward core transform of a 4x4 block (raster).
func fdct4(d *[16]int32) {
	for j := 0; j < 4; j++ {
		r := d[j*4 : j*4+4]
		a0, a1 := r[0]+r[3], r[1]+r[2]
		a2, a3 := r[1]-r[2], r[0]-r[3]
		r[0], r[2] = a0+a1, a0-a1
		r[1], r[3] = 2*a3+a2, a3-2*a2
	}
	for i := 0; i < 4; i++ {
		a0, a1 := d[i]+d[12+i], d[4+i]+d[8+i]
		a2, a3 := d[4+i]-d[8+i], d[i]-d[12+i]
		d[i], d[8+i] = a0+a1, a0-a1
		d[4+i], d[12+i] = 2*a3+a2, a3-2*a2
	}
}

func quantLevel(v, mf int32, qbits uint, f int32) int32 {
	if v < 0 {
		return -((-v*mf + f) >> qbits)
	}
	return (v*mf + f) >> qbits
}

// transformQuant computes the residual of the source macroblock at
// (mbX,mbY) against the prediction in c.pred / c.predCb / c.predCr, the
// luma in 4x4 transforms (transformQuant8 tries the 8x8 ones on the
// candidate that wins).
func (c *depCoder) transformQuant(mbX, mbY int, res *residual) {
	res.t8, res.i16 = false, false
	qbits := uint(15 + c.qp/6)
	f := int32(1<<qbits) / 6
	mask, nz4 := fdctQuantLuma(c.src.Y, mbY*16*c.src.StrideY+mbX*16, c.src.StrideY, &c.pred, &quantMF[c.qp%6], &c.mfT, qbits, f, &res.luma)
	if c.trellising() && mask != 0 {
		mask, nz4 = c.trellisLuma4(mbX, mbY, mask, qbits, res)
	}
	// the coded block pattern: an 8x8 quadrant has a level in one of its
	// four blocks
	var cbp4 uint8
	for r := 0; r < 16; r++ {
		if mask>>r&1 != 0 {
			cbp4 |= 1 << ((r>>3)*2 + (r&3)>>1)
		}
	}
	res.cbp, res.nonzer = cbp4, nz4
	c.chromaQuant(mbX, mbY, res)
}

// transformQuant8 recodes the luma residual of res (coded by
// transformQuant against the prediction now in c.pred) in 8x8 transforms,
// and reports whether that codes anything: the candidate then has both,
// and rdEval says which costs less.
func (c *depCoder) transformQuant8(mbX, mbY int, res *residual) bool {
	x0, y0 := mbX*16, mbY*16
	var cbp8 uint8
	nz8 := 0
	qbits8 := uint(16 + c.qp/6)
	f8 := int32(1<<qbits8) / 6
	mf8 := &quant8MF[c.qp%6]
	var t8 *trellisCosts
	for b8 := 0; b8 < 4; b8++ {
		bx, by := (b8&1)*8, (b8>>1)*8
		var d [64]int32
		for j := 0; j < 8; j++ {
			so := (y0+by+j)*c.src.StrideY + x0 + bx
			po := (by+j)*16 + bx
			for i := 0; i < 8; i++ {
				d[j*8+i] = int32(c.src.Y[so+i]) - int32(c.pred[po+i])
			}
		}
		fdct8(&d)
		for k := 0; k < 64; k++ {
			p := zigzag8x8[k]
			l := quantLevel(d[p], mf8[p], qbits8, f8)
			res.luma8[b8][k] = l
			if l != 0 {
				cbp8 |= 1 << b8
			}
		}
		if c.trellising() && cbp8>>b8&1 != 0 {
			if t8 == nil {
				t8 = &c.tc
				t8.load(c.e.contexts(), catLuma8x8, 64)
			}
			if !trellisBlock(t8, d[:], zigzag8x8[:], mf8[:], qbits8, trellisW8[c.qp][:], c.lambda2, 0, 0, res.luma8[b8][:]) {
				cbp8 &^= 1 << b8
			}
		}
		for _, l := range res.luma8[b8] {
			if l != 0 {
				nz8++
			}
		}
	}
	if cbp8 == 0 {
		return false
	}
	// the chroma stays; nonzer counted the 4x4 luma levels
	chroma := res.nonzer
	for r := 0; r < 16; r++ {
		for _, l := range res.luma[r] {
			if l != 0 {
				chroma--
			}
		}
	}
	res.t8, res.cbp, res.nonzer = true, res.cbp&0xf0|cbp8, chroma+nz8
	return true
}

// predictPart predicts the w x h block at (x,y) of macroblock (mbX,mbY)
// (pixels) into c.pred / c.predCb / c.predCr from the references and
// vectors per list (-1: list unused), averaging two.
func (c *depCoder) predictPart(mbX, mbY, x, y, w, h int, ref [2]int8, m [2]mv) {
	px, py := mbX*16+x, mbY*16+y
	ly := c.pred[y*16+x:]
	cb, cr := c.predCb[(y/2)*8+x/2:], c.predCr[(y/2)*8+x/2:]
	if ref[0] < 0 || ref[1] < 0 {
		l := 0
		if ref[0] < 0 {
			l = 1
		}
		pic := c.list[l][ref[l]]
		lumaMCPlanes(ly, 16, pic, px*4+int(m[l].x), py*4+int(m[l].y), w, h)
		chromaMC2(cb, cr, 8, pic, (px/2)*8+int(m[l].x), (py/2)*8+int(m[l].y), w/2, h/2)
		return
	}
	for l := 0; l < 2; l++ {
		pic := c.list[l][ref[l]]
		lumaMCPlanes(c.mc.predY[l][:], 16, pic, px*4+int(m[l].x), py*4+int(m[l].y), w, h)
		chromaMC2(c.mc.predC[l][0][:], c.mc.predC[l][1][:], 8, pic, (px/2)*8+int(m[l].x), (py/2)*8+int(m[l].y), w/2, h/2)
	}
	storeAvg(ly, 16, c.mc.predY[0][:], c.mc.predY[1][:], 16, w, h)
	storeAvg(cb, 8, c.mc.predC[0][0][:], c.mc.predC[1][0][:], 8, w/2, h/2)
	storeAvg(cr, 8, c.mc.predC[0][1][:], c.mc.predC[1][1][:], 8, w/2, h/2)
}

// predictDirect predicts the macroblock from its direct motion, quadrant
// by quadrant.
func (c *depCoder) predictDirect(mbX, mbY int, ref *[2][4]int8, m *[2][4]mv) {
	for q := 0; q < 4; q++ {
		c.predictPart(mbX, mbY, (q&1)*8, (q>>1)*8, 8, 8, [2]int8{ref[0][q], ref[1][q]}, [2]mv{m[0][q], m[1][q]})
	}
}

// codeSlice codes the macroblocks of the slice.
func (c *depCoder) codeSlice(e *cabacEncoder) {
	if n := wavefrontWorkers(); n > 1 && c.rowEnd-c.rowStart > 1 {
		c.codeSliceWavefront(e, n)
		return
	}
	c.enc, c.e = e, e
	c.qpY, c.lastDQ = c.picQP, 0
	lastIdx := c.rowEnd*c.mbW - 1
	for mbY := c.rowStart; mbY < c.rowEnd; mbY++ {
		for mbX := 0; mbX < c.mbW; mbX++ {
			c.decideMB(mbX, mbY)
			if mbY*c.mbW+mbX == lastIdx {
				e.terminate(1)
			} else {
				e.terminate(0)
			}
		}
	}
}

// decideMB chooses how to code the macroblock and writes it into c.e.
func (c *depCoder) decideMB(mbX, mbY int) {
	c.ncand = 0
	if q := int(c.mbQP[mbY*c.mbW+mbX]); q != c.qp {
		c.setQP(q)
	}
	switch {
	case c.islice:
		c.codeMBI(mbX, mbY)
	case c.bslice:
		c.codeMBB(mbX, mbY)
	default:
		c.codeMBP(mbX, mbY)
	}
}

// Wavefront: the one slice a player's MVC decoder wants per picture has
// its macroblocks decided by several workers, a row each, each row two
// macroblocks behind the one above it (a macroblock predicts from its
// left, upper and upper-right neighbours), while the slice's coder
// writes the decisions in order behind them. A worker estimates bits
// with its own model of the context states, started from the row
// above's after its second macroblock (as HEVC's wavefront does), since
// the coder's own states are not known until the write.

// wavefrontDisabled turns the wavefront off (one worker decides and
// writes, as a test or a measurement wants).
var wavefrontDisabled atomic.Bool

// SetWavefront turns the encoders' wavefront (a slice's macroblock rows
// decided in parallel) on or off, for measurements. It is on.
func SetWavefront(on bool) { wavefrontDisabled.Store(!on) }

func wavefrontWorkers() int {
	if wavefrontDisabled.Load() {
		return 1
	}
	return runtime.NumCPU()
}

// wavefront is a slice's decisions as the workers make them.
type wavefront struct {
	chosen []cand       // per macroblock of the picture, by index
	rows   []wfRow      // per row: progress
	rowCtx [][512]uint8 // per row: the model's context states after its second macroblock
}

// wfRow is a row's progress: the macroblocks decided, and who sleeps for
// more (each row its own, so an advance wakes only the row below).
type wfRow struct {
	done    atomic.Int32
	waiters atomic.Int32
	mu      sync.Mutex
	cond    sync.Cond
}

// wait blocks until row y has decided at least n macroblocks. (Spinning
// first, yielding, gained nothing and cost the scheduler's lock.)
func (wf *wavefront) wait(y, n int) {
	r := &wf.rows[y]
	if int(r.done.Load()) >= n {
		return
	}
	r.waiters.Add(1)
	r.mu.Lock()
	for int(r.done.Load()) < n {
		r.cond.Wait()
	}
	r.mu.Unlock()
	r.waiters.Add(-1)
}

// advance records that row y has decided n macroblocks, waking anyone
// asleep for it.
func (wf *wavefront) advance(y, n int) {
	r := &wf.rows[y]
	if r.waiters.Load() == 0 {
		r.done.Store(int32(n))
		// a waiter that arrived between the check and the store sees
		// the store before it sleeps or finds it after; the lock orders
		// the two
		if r.waiters.Load() == 0 {
			return
		}
	}
	r.mu.Lock()
	r.done.Store(int32(n))
	r.mu.Unlock()
	r.cond.Broadcast()
}

// wavefrontBuf gives the coder's wavefront buffers, made on the first
// call: a coder copied for a slice shares them with its original, so the
// original makes them.
func (c *depCoder) wavefrontBuf() *wavefront {
	wf := c.wfBuf
	if wf == nil || len(wf.chosen) < c.mbW*c.mbH || len(wf.rows) < c.mbH {
		wf = &wavefront{chosen: make([]cand, c.mbW*c.mbH), rows: make([]wfRow, c.mbH), rowCtx: make([][512]uint8, c.mbH)}
		for i := range wf.rows {
			wf.rows[i].cond.L = &wf.rows[i].mu
		}
		c.wfBuf = wf
	}
	if len(c.wcur) < c.mbW*c.mbH {
		c.wcur = make([]depMB, c.mbW*c.mbH)
		c.wimodes = make([]int8, c.mbW*4*c.mbH*4)
	}
	return wf
}

func (c *depCoder) codeSliceWavefront(e *cabacEncoder, workers int) {
	mbW, rows := c.mbW, c.rowEnd-c.rowStart
	wf := c.wavefrontBuf()
	for y := c.rowStart; y < c.rowEnd; y++ {
		wf.rows[y].done.Store(0)
	}
	workers = min(workers, rows)
	var next atomic.Int32
	next.Store(int32(c.rowStart))
	var wg sync.WaitGroup
	stats := make([]depStats, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			wk := *c
			wk.wf = wf
			wk.stats = depStats{}
			wk.e = &wk.model
			for {
				y := int(next.Add(1)) - 1
				if y >= c.rowEnd {
					break
				}
				if y == c.rowStart {
					wk.model.ctx = e.ctx
				} else {
					wf.wait(y-1, min(2, mbW))
					wk.model.ctx = wf.rowCtx[y-1]
				}
				wk.qpY, wk.lastDQ = c.picQP, 0
				for x := 0; x < mbW; x++ {
					if y > c.rowStart {
						wf.wait(y-1, min(x+2, mbW))
					}
					wk.decideMB(x, y)
					if x == min(1, mbW-1) {
						wf.rowCtx[y] = wk.model.ctx
					}
					wf.advance(y, x+1)
				}
			}
			stats[i] = wk.stats
		}(i)
	}
	// The writer: the slice's coder, with its own view of what the
	// neighbours were (the workers' grids are theirs to change). The
	// slices of a picture share the grids, each its own rows.
	w := *c
	w.enc, w.e, w.wf = e, e, nil
	w.qpY, w.lastDQ = c.picQP, 0
	w.cur, w.imodes = c.wcur, c.wimodes
	for i := c.rowStart * 4 * mbW * 4; i < c.rowEnd*4*mbW*4; i++ {
		w.imodes[i] = 2
	}
	lastIdx := c.rowEnd*mbW - 1
	for y := c.rowStart; y < c.rowEnd; y++ {
		for x := 0; x < mbW; x++ {
			wf.wait(y, x+1)
			k := &wf.chosen[y*mbW+x]
			w.noteIModes(x, y, k)
			w.writeMB(x, y, k)
			if y*mbW+x == lastIdx {
				e.terminate(1)
			} else {
				e.terminate(0)
			}
		}
	}
	wg.Wait()
	for i := range stats {
		c.stats.add(stats[i])
	}
}

// depReconHook, when set (by tests), receives each coded macroblock's
// reconstruction as the coder models it; depKindHook how it was coded.
// Pictures may be coded concurrently: pic tells them apart (the coder's
// tag).
var (
	depReconHook func(pic, mbX, mbY int, luma *[256]byte, chroma *[2][64]byte)
	depKindHook  func(pic, mbX, mbY int, kind string)
)

// kind names how the candidate codes the macroblock.
func (k *cand) kind() string {
	switch {
	case k.skip:
		return "skip"
	case k.direct:
		return "direct"
	case k.intra == intra16x16:
		return "I16"
	case k.intra == intra4x4:
		return "I4"
	case k.intra == intra8x8:
		return "I8"
	case k.shape == part16x8:
		return "16x8"
	case k.shape == part8x16:
		return "8x16"
	case k.part[0].pred() == predBi:
		return "bi"
	}
	return "L" + string(rune('0'+k.part[0].pred()-1)) + string(rune('0'+max(k.part[0].ref[0], k.part[0].ref[1])))
}

// partition is one of a macroblock's prediction partitions: its reference
// and vector per list (-1: list unused).
type partition struct {
	ref [2]int8
	m   [2]mv
}

// pred is the partition's prediction flags (predL0, predL1 or predBi).
func (p *partition) pred() uint8 {
	var f uint8
	if p.ref[0] >= 0 {
		f |= predL0
	}
	if p.ref[1] >= 0 {
		f |= predL1
	}
	return f
}

// partGeom is the position and size of partition p of a shape, in 4x4
// units.
func partGeom(shape, p int) (x, y, w, h int) {
	switch shape {
	case part16x8:
		return 0, p * 2, 4, 2
	case part8x16:
		return p * 2, 0, 2, 4
	}
	return 0, 0, 4, 4
}

// cand is a way of coding the macroblock under evaluation.
type cand struct {
	candHdr
	// the residual (transformQuant writes every level it codes; the rest
	// is whatever an earlier candidate left), an intra candidate's
	// prediction, and the reconstruction after the evaluation
	res   residual
	pred  [256]byte
	predC [2][64]byte
	rec   [256]byte
	recC  [2][64]byte
}

// candHdr is what a candidate starts from: the small part of it.
type candHdr struct {
	skip   bool         // P_Skip or B_Skip
	direct bool         // B_Direct_16x16
	shape  int          // part16x16, part16x8 or part8x16
	part   [2]partition // per partition (one for 16x16)
	dref   [2][4]int8   // direct motion per quadrant (B_Skip, B_Direct_16x16)
	dmv    [2][4]mv
	sad    float64 // SATD plus an estimate of the bits, for ranking before the full evaluation
	cost   float64 // rate-distortion cost

	// intra candidates: the kind, the I_16x16 mode or the I_NxN modes per
	// block, the chroma mode
	intra  int
	imode  int
	imodes [16]int8
	chroma int

	qp int // the quantiser it was coded with
}

// newCand gives a candidate for the macroblock being decided, from the
// coder's slab: its header cleared, its arrays as the last use left
// them.
func (c *depCoder) newCand() *cand {
	if c.ncand == len(c.cands) {
		return &cand{candHdr: candHdr{qp: c.qp}}
	}
	k := &c.cands[c.ncand]
	c.ncand++
	k.candHdr = candHdr{qp: c.qp}
	return k
}

func (k *cand) nparts() int {
	if k.shape == part16x16 {
		return 1
	}
	return 2
}

// setMotion gives the quadrants of partition p of a shape list l's
// reference and vector.
func (m *depMB) setMotion(shape, p, l int, ref int8, v mv) {
	x, y, w, h := partGeom(shape, p)
	for q := 0; q < 4; q++ {
		qx, qy := (q&1)*2, (q>>1)*2
		if qx >= x && qx < x+w && qy >= y && qy < y+h {
			m.ref[l][q] = ref
			m.mv[l][q] = v
		}
	}
}

// setCand records a candidate's motion (not its direct motion) in the
// macroblock, as the neighbours and its own later partitions see it.
func (m *depMB) setCand(k *cand) {
	*m = depMB{}
	for p := 0; p < k.nparts(); p++ {
		for l := 0; l < 2; l++ {
			m.setMotion(k.shape, p, l, k.part[p].ref[l], k.part[p].m[l])
		}
	}
}

// searchExtra lists a reference's search candidates besides the predictor:
// the previous picture's vector here and the neighbours' vectors to the
// same reference.
func (c *depCoder) searchExtra(mbX, mbY, l, r int) []mv {
	idx := mbY*c.mbW + mbX
	extra := []mv{c.prev[c.refKind(l, r)][idx]}
	if m := c.lowMV[l][r]; m != nil {
		// the lookahead's, at half resolution: found by a wider search
		v := m.mv[idx]
		extra = append(extra, mv{v.x * 8, v.y * 8})
	}
	if a, ok := c.neighbour(mbX-1, mbY); ok && a.ref[l][1] == int8(r) {
		extra = append(extra, a.mv[l][1])
	}
	if b, ok := c.neighbour(mbX, mbY-1); ok && b.ref[l][2] == int8(r) {
		extra = append(extra, b.mv[l][2])
	}
	return extra
}

// predictCand predicts the macroblock as the candidate would.
func (c *depCoder) predictCand(mbX, mbY int, k *cand) {
	if k.skip && c.bslice || k.direct {
		c.predictDirect(mbX, mbY, &k.dref, &k.dmv)
		return
	}
	for p := 0; p < k.nparts(); p++ {
		x, y, w, h := partGeom(k.shape, p)
		c.predictPart(mbX, mbY, x*4, y*4, w*4, h*4, k.part[p].ref, k.part[p].m)
	}
}

// rdEval evaluates a candidate whose prediction is in c.pred and residual
// (unless it skips) in k.res: the squared error of its reconstruction
// plus lambda times the bits it would take.
func (c *depCoder) rdEval(mbX, mbY int, k *cand) {
	if k.intra != intraNone {
		c.pred, c.predCb, c.predCr = k.pred, k.predC[0], k.predC[1]
	}
	if k.skip {
		k.rec, k.recC[0], k.recC[1] = c.pred, c.predCb, c.predCr
	} else {
		c.reconLuma(&k.res, &k.rec)
		c.reconChroma(&k.res, &k.recC)
	}
	d := c.ssdLuma(mbX, mbY, &k.rec) + c.ssdChroma(mbX, mbY, &k.recC)
	coder := c.e
	c.cost.reset(coder)
	c.e = &c.cost
	c.writeMB(mbX, mbY, k)
	c.e = coder
	k.cost = float64(d) + c.lambda2*c.cost.bits
}

// rdPick evaluates the candidates and writes the best.
func (c *depCoder) rdPick(mbX, mbY int, cands []*cand) {
	var best *cand
	for _, k := range cands {
		if k.intra == intraNone {
			c.predictCand(mbX, mbY, k)
			if !k.skip {
				c.transformQuant(mbX, mbY, &k.res)
			}
		}
		c.rdEval(mbX, mbY, k)
		if best == nil || k.cost < best.cost {
			best = k
		}
		// the 8x8 transform too, on the candidates near the best: the
		// choice between them may turn on it
		if k.intra == intraNone && !k.skip && k.res.cbp&15 != 0 && k.cost <= best.cost*1.3 {
			alt := c.newCand()
			*alt = *k
			if c.transformQuant8(mbX, mbY, &alt.res) {
				c.rdEval(mbX, mbY, alt)
				if alt.cost < best.cost {
					best = alt
				}
			}
		}
	}
	c.storeRec(mbX, mbY, &best.rec, &best.recC)
	c.noteIModes(mbX, mbY, best)
	if depReconHook != nil {
		depReconHook(c.tag, mbX, mbY, &best.rec, &best.recC)
	}
	if depKindHook != nil {
		depKindHook(c.tag, mbX, mbY, best.kind())
	}
	// Written into the slice's coder, or, deciding a row of a wavefront,
	// into the row's model of it; the writer then codes the choice.
	c.writeMB(mbX, mbY, best)
	if c.wf != nil {
		// what the writer needs of it: the header, the residual unless
		// skipped, an intra candidate's prediction
		ch := &c.wf.chosen[mbY*c.mbW+mbX]
		ch.candHdr = best.candHdr
		if !best.skip {
			ch.res = best.res
		}
		if best.intra != intraNone {
			ch.pred, ch.predC = best.pred, best.predC
		}
	}
	switch {
	case best.intra != intraNone:
		c.stats.intra++
	case best.skip:
		c.stats.skip++
	case best.direct:
		c.stats.direct++
	case best.shape != part16x16:
		c.stats.parts++
	case best.part[0].pred() == predBi:
		c.stats.bi++
	case c.refKind(0, int(best.part[0].ref[0])) == 0 && best.part[0].ref[1] < 0:
		c.stats.interView++
	default:
		c.stats.temporal++
	}
}

// noteIModes records the intra 4x4 prediction modes the macroblocks
// after this one see.
func (c *depCoder) noteIModes(mbX, mbY int, k *cand) {
	gst := c.mbW * 4
	for by := 0; by < 4; by++ {
		for bx := 0; bx < 4; bx++ {
			m := int8(2)
			switch k.intra {
			case intra4x4:
				m = k.imodes[blkOrder[by*4+bx]]
			case intra8x8:
				m = k.imodes[(by>>1)*2+bx>>1]
			}
			c.imodes[(mbY*4+by)*gst+mbX*4+bx] = m
		}
	}
}

// partCands adds the two-partition candidates: for each shape, each
// partition's best prediction by SATD among the references and, in a B
// slice, the base view averaged with the temporal reference, searched
// from the macroblock's 16x16 vectors.
func (c *depCoder) partCands(mbX, mbY int, uni [2][4]mv, costs [2][4]float64, sameT bool, cands []*cand) []*cand {
	cur := &c.cur[mbY*c.mbW+mbX]
	saved := *cur
	// the references searched: those within a quarter of the list's best
	// at 16x16; the others would rarely win a partition
	var try [2][4]bool
	for l := 0; l < 2; l++ {
		best := math.MaxFloat64
		for r := range c.list[l] {
			best = min(best, costs[l][r])
		}
		for r := range c.list[l] {
			try[l][r] = costs[l][r] <= best*1.25
		}
	}
	for _, shape := range [2]int{part16x8, part8x16} {
		k := c.newCand()
		k.shape = shape
		*cur = depMB{}
		for p := 0; p < 2; p++ {
			x, y, w, h := partGeom(shape, p)
			var best partition
			bestCost := math.MaxFloat64
			var pm [2][4]mv
			for l := 0; l < 2; l++ {
				for r := range c.list[l] {
					if l == 1 && sameT {
						continue
					}
					pm[l][r] = uni[l][r]
					if !try[l][r] {
						continue
					}
					m, cost := c.search(mbX, mbY, x, y, w, h, l, r, []mv{uni[l][r]}, false)
					pm[l][r] = m
					bins := 5 + r
					part := partition{ref: [2]int8{-1, -1}}
					part.ref[l], part.m[l] = int8(r), m
					if cost += c.lambda * float64(bins); cost < bestCost {
						bestCost, best = cost, part
					}
				}
			}
			if r0 := len(c.list[0]) - 1; c.bslice && c.list[0][r0] != c.list[1][0] {
				// list 0's last reference (the base view picture of a
				// dependent view) with list 1's
				part := partition{ref: [2]int8{int8(r0), 0}, m: [2]mv{pm[0][r0], pm[1][0]}}
				if sameT {
					part.m[1] = pm[0][0]
				}
				c.predictPart(mbX, mbY, x*4, y*4, w*4, h*4, part.ref, part.m)
				cost := float64(c.satd(mbX*16+x*4, mbY*16+y*4, w*4, h*4, c.pred[y*4*16+x*4:])) +
					c.mvCost(part.m[0], c.mvPred(0, 1, mbX, mbY, x, y, w, h)) +
					c.mvCost(part.m[1], c.mvPred(1, 0, mbX, mbY, x, y, w, h)) + c.lambda*9
				if cost < bestCost {
					bestCost, best = cost, part
				}
			}
			k.part[p] = best
			k.sad += bestCost
			// its own later partition predicts from it
			for l := 0; l < 2; l++ {
				cur.setMotion(shape, p, l, best.ref[l], best.m[l])
			}
		}
		cands = append(cands, k)
	}
	*cur = saved
	return cands
}

// codeMBP codes a P slice macroblock: P_Skip, or P_L0 in one partition
// or two from the references' best vectors, whichever costs least.
func (c *depCoder) codeMBP(mbX, mbY int) {
	idx := mbY*c.mbW + mbX
	smv := c.skipMV(mbX, mbY)
	skip := c.newCand()
	skip.skip = true
	skip.part[0] = partition{ref: [2]int8{0, -1}, m: [2]mv{smv}}
	if c.probeSkip(mbX, mbY, skip) {
		c.prev[c.refKind(0, 0)][idx] = smv
		c.rdPick(mbX, mbY, []*cand{skip})
		return
	}
	cands := []*cand{skip}
	var uni [2][4]mv
	var costs [2][4]float64
	for r := range c.list[0] {
		extra := c.searchExtra(mbX, mbY, 0, r)
		if r == 0 {
			extra = append(extra, smv)
		}
		m, cost := c.search(mbX, mbY, 0, 0, 4, 4, 0, r, extra, true)
		c.prev[c.refKind(0, r)][idx] = m
		uni[0][r] = m
		costs[0][r] = cost
		k := c.newCand()
		k.sad = cost
		k.part[0] = partition{ref: [2]int8{int8(r), -1}, m: [2]mv{m}}
		cands = append(cands, k)
	}
	if c.wantParts(cands[1:]) {
		cands = c.partCands(mbX, mbY, uni, costs, true, cands)
	}
	if c.wantIntra(mbX, mbY, cands[1:]) {
		cands = append(cands, c.intraCands(mbX, mbY)...)
	}
	c.rdPick(mbX, mbY, cands)
}

// probeSkip reports whether the skip candidate codes the macroblock on
// its own: its prediction leaves a residual the quantiser drops entirely,
// so no search would code fewer bits and the distortion is within a
// quantiser step of the best anyway (x264's fast P-skip).
func (c *depCoder) probeSkip(mbX, mbY int, skip *cand) bool {
	c.predictCand(mbX, mbY, skip)
	c.transformQuant(mbX, mbY, &c.probe)
	return c.probe.nonzer == 0
}

// wantIntra reports whether intra prediction is worth trying in an inter
// slice: the best inter prediction is poor, or about what I_16x16's
// would be.
func (c *depCoder) wantIntra(mbX, mbY int, motion []*cand) bool {
	best := math.MaxFloat64
	for _, k := range motion {
		best = min(best, k.sad)
	}
	if best > c.lambda*320 {
		return true
	}
	// a smooth area the motion missed: I_16x16 alone would do about as
	// well (the full intra candidates, I_4x4 and I_8x8 too, are dear)
	return c.intraEstimate(mbX, mbY) < best*1.3
}

// codeMBI codes an I slice macroblock: the intra candidate that costs
// least.
func (c *depCoder) codeMBI(mbX, mbY int) {
	c.rdPick(mbX, mbY, c.intraCands(mbX, mbY))
}

// wantParts reports whether partitions are worth trying: the best 16x16
// prediction leaves a residual of some size.
func (c *depCoder) wantParts(motion []*cand) bool {
	best := math.MaxFloat64
	for _, k := range motion {
		best = min(best, k.sad)
	}
	return best > c.lambda*48
}

// codeMBB codes a B slice macroblock: B_Skip, B_Direct_16x16, or a
// prediction in one partition or two from one reference or two,
// whichever costs least among the direct ones, the best few 16x16 ones by
// SATD and the partitioned ones.
func (c *depCoder) codeMBB(mbX, mbY int) {
	idx := mbY*c.mbW + mbX
	sameT := c.list[1][0] == c.list[0][0] // list 1 repeats the latest temporal reference
	direct := c.newCand()
	direct.direct = true
	c.directMotion(mbX, mbY, &direct.dref, &direct.dmv)
	skip := c.newCand()
	skip.skip, skip.dref, skip.dmv = true, direct.dref, direct.dmv
	if c.probeSkip(mbX, mbY, skip) {
		for l := 0; l < 2; l++ {
			if r := skip.dref[l][0]; r >= 0 {
				c.prev[c.refKind(l, int(r))][idx] = skip.dmv[l][0]
			}
		}
		c.rdPick(mbX, mbY, []*cand{skip})
		return
	}
	var uni [2][4]mv
	var costs [2][4]float64
	var motion []*cand
	for l := 0; l < 2; l++ {
		for r := range c.list[l] {
			if l == 1 && sameT {
				uni[1][0], costs[1][0] = uni[0][0], costs[0][0]
				continue
			}
			m, cost := c.search(mbX, mbY, 0, 0, 4, 4, l, r, c.searchExtra(mbX, mbY, l, r), true)
			c.prev[c.refKind(l, r)][idx] = m
			uni[l][r] = m
			costs[l][r] = cost
			k := c.newCand()
			k.sad = cost + c.lambda*float64(4+r)
			k.part[0] = partition{ref: [2]int8{-1, -1}}
			k.part[0].ref[l], k.part[0].m[l] = int8(r), m
			motion = append(motion, k)
		}
	}
	// two references averaged: the base view with a temporal one, and
	// the temporal ones either side
	bi := func(r0 int) {
		ref := [2]int8{int8(r0), 0}
		m, cost := c.refineBi(mbX, mbY, ref, [2]mv{uni[0][r0], uni[1][0]})
		k := c.newCand()
		k.sad = cost + c.lambda*float64(8+r0)
		k.part[0] = partition{ref: ref, m: m}
		motion = append(motion, k)
	}
	for r := range c.list[0] {
		if c.list[0][r] != c.list[1][0] {
			bi(r)
		}
	}
	// the best few by SATD go to the full evaluation, with the direct
	// prediction with and without a residual, and the partitioned ones
	for i := 1; i < len(motion); i++ {
		for j := i; j > 0 && motion[j].sad < motion[j-1].sad; j-- {
			motion[j], motion[j-1] = motion[j-1], motion[j]
		}
	}
	motion = motion[:min(len(motion), 2)]
	cands := append([]*cand{skip, direct}, motion...)
	if c.wantParts(motion) {
		cands = c.partCands(mbX, mbY, uni, costs, sameT, cands)
	}
	if c.wantIntra(mbX, mbY, motion) {
		cands = append(cands, c.intraCands(mbX, mbY)...)
	}
	c.rdPick(mbX, mbY, cands)
}

// bMBType is the B macroblock type of a partitioned candidate (Table
// 7-14).
func bMBType(k *cand) int {
	if k.shape == part16x16 {
		return int(k.part[0].pred()) // 1, 2, 3
	}
	for t := 4; t <= 21; t++ {
		if info := bMBTypeInfo[t]; int(info.shape) == k.shape && info.p0 == k.part[0].pred() && info.p1 == k.part[1].pred() {
			return t
		}
	}
	panic("mvc: no B macroblock type for the partitions")
}

// writeBType writes mb_type of a B slice (9.3.2.5, Table 9-37) after its
// first bin's context increment is known.
func (c *depCoder) writeBType(inc, t int) {
	e := c.e
	if t == 0 {
		e.decision(27+inc, 0)
		return
	}
	e.decision(27+inc, 1)
	if t <= 2 {
		e.decision(27+3, 0)
		e.decision(27+5, t-1)
		return
	}
	e.decision(27+3, 1)
	bits, n := t-3, 4
	switch {
	case t == 11:
		bits = 14
	case t == 22:
		bits = 15
	case t == 23: // the intra prefix
		bits = 13
	case t >= 12:
		bits, n = t+4, 5
	}
	for i := n - 1; i >= 0; i-- {
		ctx := 27 + 5
		if i == n-1 {
			ctx = 27 + 4
		}
		e.decision(ctx, bits>>i&1)
	}
}

// writeMB writes the macroblock as the candidate codes it, recording it
// for its neighbours.
func (c *depCoder) writeMB(mbX, mbY int, k *cand) {
	e := c.e
	cur := &c.cur[mbY*c.mbW+mbX]
	c.curQP = k.qp
	if k.skip {
		if c.bslice {
			*cur = depMB{skip: true, direct: true, ref: k.dref, mv: k.dmv}
		} else {
			cur.setCand(k)
			cur.skip = true
		}
		c.writeSkip(mbX, mbY, true)
		if c.writesStream() {
			c.lastDQ = 0
		}
		return
	}
	if !c.islice {
		c.writeSkip(mbX, mbY, false)
	}
	if k.intra != intraNone {
		switch {
		case c.islice:
			c.writeIType(k, mbX, mbY, 0)
		case c.bslice:
			a, okA := c.neighbour(mbX-1, mbY)
			b, okB := c.neighbour(mbX, mbY-1)
			inc := 0
			if okA && !a.direct {
				inc++
			}
			if okB && !b.direct {
				inc++
			}
			c.writeBType(inc, 23)
			c.writeIType(k, mbX, mbY, 32)
		default:
			e.decision(14, 1)
			c.writeIType(k, mbX, mbY, 17)
		}
		*cur = depMB{intra: k.intra}
		cur.setMotion(part16x16, 0, 0, -1, mv{})
		cur.setMotion(part16x16, 0, 1, -1, mv{})
		c.writeIntraMB(mbX, mbY, k)
		return
	}
	if !c.bslice {
		// mb_type: P_L0_16x16 0 0 0, P_L0_L0_16x8 0 1 1, P_L0_L0_8x16 0 1 0
		e.decision(14, 0)
		if k.shape == part16x16 {
			e.decision(15, 0)
			e.decision(16, 0)
		} else {
			e.decision(15, 1)
			if k.shape == part16x8 {
				e.decision(17, 1)
			} else {
				e.decision(17, 0)
			}
		}
	} else {
		a, okA := c.neighbour(mbX-1, mbY)
		b, okB := c.neighbour(mbX, mbY-1)
		inc := 0
		if okA && !a.direct {
			inc++
		}
		if okB && !b.direct {
			inc++
		}
		if k.direct {
			c.writeBType(inc, bDirect16x16)
			*cur = depMB{direct: true, ref: k.dref, mv: k.dmv}
			c.writeResidual(mbX, mbY, &k.res)
			return
		}
		c.writeBType(inc, bMBType(k))
	}
	cur.setCand(k)
	n := k.nparts()
	for l := 0; l < 2; l++ {
		for p := 0; p < n; p++ {
			if k.part[p].ref[l] >= 0 {
				x, y, _, _ := partGeom(k.shape, p)
				c.writeRefIdx(mbX, mbY, l, x, y, k.part[p].ref[l])
			}
		}
	}
	for l := 0; l < 2; l++ {
		for p := 0; p < n; p++ {
			if r := k.part[p].ref[l]; r >= 0 {
				x, y, w, h := partGeom(k.shape, p)
				c.writeMvd(mbX, mbY, l, x, y, w, h, k.part[p].m[l], c.mvPred(l, r, mbX, mbY, x, y, w, h))
			}
		}
	}
	c.writeResidual(mbX, mbY, &k.res)
}

// reconChroma reconstructs the macroblock's chroma from its prediction
// and residual as the decoder will (stride 8).
func (c *depCoder) reconChroma(res *residual, dst *[2][64]byte) {
	dst[0], dst[1] = c.predCb, c.predCr
	chroma := res.cbp >> 4
	if chroma == 0 {
		return
	}
	qp := c.qpc
	ls := &scale4[qp%6]
	for comp := 0; comp < 2; comp++ {
		pred := &dst[comp]
		l := &res.dc[comp]
		f := [4]int32{l[0] + l[1] + l[2] + l[3], l[0] - l[1] + l[2] - l[3], l[0] + l[1] - l[2] - l[3], l[0] - l[1] - l[2] + l[3]}
		for b := 0; b < 4; b++ {
			var d [16]int32
			d[0] = (f[b] * ls[0] << (qp / 6)) >> 5
			if chroma == 2 {
				for k := 1; k < 16; k++ {
					v := res.ac[comp][b][k-1]
					if v == 0 {
						continue
					}
					p := int(zigzag4x4[k])
					if qp >= 24 {
						d[p] = v * ls[p] << (qp/6 - 4)
					} else {
						d[p] = (v*ls[p] + 1<<(3-qp/6)) >> (4 - qp/6)
					}
				}
			}
			encIDCT4(&d)
			x0, y0 := (b&1)*4, (b>>1)*4
			for y := 0; y < 4; y++ {
				for x := 0; x < 4; x++ {
					o := (y0+y)*8 + x0 + x
					pred[o] = encClip(int32(pred[o]) + (d[y*4+x]+32)>>6)
				}
			}
		}
	}
}

// ssdChroma is the squared error of a chroma reconstruction (stride 8)
// against the source macroblock.
func (c *depCoder) ssdChroma(mbX, mbY int, rec *[2][64]byte) int64 {
	var sum int64
	for comp := 0; comp < 2; comp++ {
		src := c.src.Cb
		if comp == 1 {
			src = c.src.Cr
		}
		for y := 0; y < 8; y++ {
			so := (mbY*8+y)*c.src.StrideC + mbX*8
			s := src[so : so+8]
			r := rec[comp][y*8 : y*8+8]
			for i := range 8 {
				d := int64(s[i]) - int64(r[i])
				sum += d * d
			}
		}
	}
	return sum
}

// refineBi refines a pair of vectors for an averaged prediction, each in
// turn around its own with the other fixed, by the SATD of the average
// plus both vectors' bits. It returns the vectors and that cost.
func (c *depCoder) refineBi(mbX, mbY int, ref [2]int8, m [2]mv) ([2]mv, float64) {
	x0, y0 := mbX*16, mbY*16
	pmv := [2]mv{c.mvPred(0, ref[0], mbX, mbY, 0, 0, 4, 4), c.mvPred(1, ref[1], mbX, mbY, 0, 0, 4, 4)}
	cost := func(m [2]mv) float64 {
		return float64(c.satd(x0, y0, 16, 16, c.predOf(mbX, mbY, ref, m))) + c.mvCost(m[0], pmv[0]) + c.mvCost(m[1], pmv[1])
	}
	best := cost(m)
	for round := 0; round < 1; round++ {
		improved := false
		for l := 0; l < 2; l++ {
			center := m
			for _, d := range [8][2]int16{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}} {
				t := center
				t[l] = c.clampMV(x0, y0, 16, 16, mv{center[l].x + d[0], center[l].y + d[1]})
				if k := cost(t); k < best {
					best, m, improved = k, t, true
				}
			}
		}
		if !improved {
			break
		}
	}
	return m, best
}

// predOf predicts the whole macroblock and returns the luma prediction.
func (c *depCoder) predOf(mbX, mbY int, ref [2]int8, m [2]mv) []byte {
	c.predictPart(mbX, mbY, 0, 0, 16, 16, ref, m)
	return c.pred[:]
}

func (c *depCoder) writeSkip(mbX, mbY int, skip bool) {
	inc := 0
	if a, ok := c.neighbour(mbX-1, mbY); ok && !a.skip {
		inc++
	}
	if b, ok := c.neighbour(mbX, mbY-1); ok && !b.skip {
		inc++
	}
	base := 11
	if c.bslice {
		base = 24
	}
	bin := 0
	if skip {
		bin = 1
	}
	c.e.decision(base+inc, bin)
}

// refIdxCond is a neighbouring block's term of ref_idx's context
// (9.3.3.1.1.6): it predicts from list l with a reference index above 0.
// The block (x,y) is relative to the macroblock, in 4x4 units: one of its
// own earlier partitions, or a neighbour's.
func (c *depCoder) refIdxCond(l, mbX, mbY, x, y int) int {
	m, ok := c.neighbour(mbX+x>>2, mbY+y>>2)
	if !ok || m.skip || m.direct || m.intra != intraNone {
		return 0
	}
	if m.ref[l][quadrant(x, y)] > 0 {
		return 1
	}
	return 0
}

// writeRefIdx writes ref_idx_lX of the partition at (x,y) when the list
// has more than one reference.
func (c *depCoder) writeRefIdx(mbX, mbY, l, x, y int, ref int8) {
	if len(c.list[l]) < 2 {
		return
	}
	e := c.e
	inc := c.refIdxCond(l, mbX, mbY, x-1, y) + 2*c.refIdxCond(l, mbX, mbY, x, y-1)
	if ref == 0 {
		e.decision(54+inc, 0)
		return
	}
	e.decision(54+inc, 1)
	for i := 1; i < int(ref); i++ {
		ctx := 58
		if i > 1 {
			ctx = 59
		}
		e.decision(ctx, 1)
	}
	ctx := 58
	if ref > 1 {
		ctx = 59
	}
	e.decision(ctx, 0)
}

// amvdAt is the stored |mvd| component of the 4x4 block (x,y) relative to
// the macroblock, for the context of an mvd (9.3.3.1.1.7): 0 outside the
// slice.
func (c *depCoder) amvdAt(l, comp, mbX, mbY, x, y int) int {
	m, ok := c.neighbour(mbX+x>>2, mbY+y>>2)
	if !ok {
		return 0
	}
	return int(m.amvd[l][quadrant(x, y)][comp])
}

// writeMvd writes mvd_lX of the partition at (x,y) of size w x h and
// records its size for the contexts of the mvds after it.
func (c *depCoder) writeMvd(mbX, mbY, l, x, y, w, h int, m, p mv) {
	cur := &c.cur[mbY*c.mbW+mbX]
	mvd := [2]int{int(m.x - p.x), int(m.y - p.y)}
	for comp := 0; comp < 2; comp++ {
		sum := c.amvdAt(l, comp, mbX, mbY, x-1, y) + c.amvdAt(l, comp, mbX, mbY, x, y-1)
		inc := 0
		if sum > 32 {
			inc = 2
		} else if sum >= 3 {
			inc = 1
		}
		c.writeMvdComp(40+comp*7, inc, mvd[comp])
		av := mvd[comp]
		if av < 0 {
			av = -av
		}
		for q := 0; q < 4; q++ {
			qx, qy := (q&1)*2, (q>>1)*2
			if qx >= x && qx < x+w && qy >= y && qy < y+h {
				cur.amvd[l][q][comp] = uint8(min(av, 64))
			}
		}
	}
}

// writeResidual writes the macroblock's coded_block_pattern, mb_qp_delta
// and residual blocks, recording them for the neighbours' contexts.
func (c *depCoder) writeResidual(mbX, mbY int, res *residual) {
	e := c.e
	cur := &c.cur[mbY*c.mbW+mbX]
	a, okA := c.neighbour(mbX-1, mbY)
	b, okB := c.neighbour(mbX, mbY-1)
	cbp := int(res.cbp)
	lumaA, lumaB := 15, 15
	chromaA, chromaB := 0, 0
	if okA {
		lumaA, chromaA = int(a.cbp&15), int(a.cbp>>4)
	}
	if okB {
		lumaB, chromaB = int(b.cbp&15), int(b.cbp>>4)
	}
	// coded_block_pattern, unless mb_type carried it (I_16x16)
	for b8 := 0; b8 < 4 && !res.i16; b8++ {
		var la, lb int
		if b8&1 == 0 {
			la = lumaA >> (b8 + 1) & 1
		} else {
			la = cbp >> (b8 - 1) & 1
		}
		if b8 < 2 {
			lb = lumaB >> (b8 + 2) & 1
		} else {
			lb = cbp >> (b8 - 2) & 1
		}
		e.decision(73+(la^1)+2*(lb^1), cbp>>b8&1)
	}
	chroma := cbp >> 4
	if !res.i16 {
		inc := 0
		if chromaA != 0 {
			inc++
		}
		if chromaB != 0 {
			inc += 2
		}
		if chroma == 0 {
			e.decision(77+inc, 0)
		} else {
			e.decision(77+inc, 1)
			inc = 4
			if chromaA == 2 {
				inc++
			}
			if chromaB == 2 {
				inc += 2
			}
			e.decision(77+inc, chroma-1)
		}
	}
	cur.cbp = uint8(cbp)
	if cbp == 0 && !res.i16 {
		if c.writesStream() {
			c.lastDQ = 0
		}
		return
	}
	intra := cur.intra != intraNone
	if cbp&15 != 0 && !intra {
		// transform_size_8x8_flag
		inc := 0
		if okA && a.t8 {
			inc++
		}
		if okB && b.t8 {
			inc++
		}
		bin := 0
		if res.t8 {
			bin = 1
		}
		e.decision(399+inc, bin)
		cur.t8 = res.t8
	}
	c.writeQPDelta()
	// An unavailable neighbour's block counts as coded for an intra
	// macroblock (9.3.3.1.1.9).
	var una uint32
	if intra {
		una = 1
	}
	if res.i16 {
		ca, cb := una, una
		if okA {
			ca = a.cbf >> 24 & 1
		}
		if okB {
			cb = b.cbf >> 24 & 1
		}
		if c.writeBlock(cbfCtxIdx(catLumaDC, ca, cb), catLumaDC, res.dcY[:]) {
			cur.cbf |= cbfLumaDC
		}
	}
	if res.t8 {
		// luma 8x8 blocks: no coded_block_flag, and their 4x4 blocks
		// count as coded for the neighbours' contexts
		for b8 := 0; b8 < 4; b8++ {
			if cbp>>b8&1 == 0 {
				continue
			}
			c.writeBlock(-1, catLuma8x8, res.luma8[b8][:])
			cur.cbf |= 0x33 << ((b8>>1)*8 + (b8&1)*2)
		}
	}
	// luma 4x4 blocks, 8x8 by 8x8: AC blocks for I_16x16
	for blk := 0; blk < 16 && !res.t8; blk++ {
		bx, by := int(blkX[blk]), int(blkY[blk])
		if cbp>>((by>>1)*2+bx>>1)&1 == 0 {
			continue
		}
		r := by*4 + bx
		ca, cb := una, una
		if bx > 0 {
			ca = cur.cbf >> (r - 1) & 1
		} else if okA {
			ca = a.cbf >> (r + 3) & 1
		}
		if by > 0 {
			cb = cur.cbf >> (r - 4) & 1
		} else if okB {
			cb = b.cbf >> (r + 12) & 1
		}
		if res.i16 {
			if c.writeBlock(cbfCtxIdx(catLumaAC, ca, cb), catLumaAC, res.luma[r][1:]) {
				cur.cbf |= 1 << r
			}
		} else if c.writeBlock(cbfCtxIdx(catLuma4x4, ca, cb), catLuma4x4, res.luma[r][:]) {
			cur.cbf |= 1 << r
		}
	}
	if chroma == 0 {
		return
	}
	for comp := 0; comp < 2; comp++ {
		bit := uint32(cbfCbDC) << comp
		ca, cb := una, una
		if okA {
			ca = 0
			if a.cbf&bit != 0 {
				ca = 1
			}
		}
		if okB {
			cb = 0
			if b.cbf&bit != 0 {
				cb = 1
			}
		}
		if c.writeBlock(cbfCtxIdx(catChromaDC, ca, cb), catChromaDC, res.dc[comp][:]) {
			cur.cbf |= bit
		}
	}
	if chroma != 2 {
		return
	}
	for comp := 0; comp < 2; comp++ {
		for blk := 0; blk < 4; blk++ {
			bx, by := blk&1, blk>>1
			bit := 16 + comp*4 + blk
			ca, cb := una, una
			if bx > 0 {
				ca = cur.cbf >> (bit - 1) & 1
			} else if okA {
				ca = a.cbf >> (bit + 1) & 1
			}
			if by > 0 {
				cb = cur.cbf >> (bit - 2) & 1
			} else if okB {
				cb = b.cbf >> (bit + 2) & 1
			}
			if c.writeBlock(cbfCtxIdx(catChromaAC, ca, cb), catChromaAC, res.ac[comp][blk][:]) {
				cur.cbf |= 1 << bit
			}
		}
	}
}

// writesStream reports whether the coder writes the slice (not an
// estimate of a candidate's bits).
func (c *depCoder) writesStream() bool {
	_, ok := c.e.(*cabacEncoder)
	return ok
}

// writeQPDelta writes mb_qp_delta (9.3.2.7, 9.3.3.1.1.5): the step from
// the quantiser in force to the macroblock's, as a unary code of the
// mapped value, the first bin's context by whether the macroblock before
// had a step.
func (c *depCoder) writeQPDelta() {
	e := c.e
	d := (c.curQP-c.qpY+26+52)%52 - 26
	inc := 0
	if c.lastDQ != 0 {
		inc = 1
	}
	if d == 0 {
		e.decision(60+inc, 0)
	} else {
		k := 2*d - 1
		if d < 0 {
			k = -2 * d
		}
		e.decision(60+inc, 1)
		ctx := 62
		for i := 1; i < k; i++ {
			e.decision(ctx, 1)
			ctx = 63
		}
		e.decision(ctx, 0)
	}
	if c.writesStream() {
		c.qpY, c.lastDQ = c.curQP, d
	}
}

// writeMvdComp writes one mvd component (UEG3, signed, uCoff 9).
func (c *depCoder) writeMvdComp(base, inc, v int) {
	e := c.e
	a := v
	if a < 0 {
		a = -a
	}
	for b := 0; b < 9; b++ {
		ctx := base + inc
		if b > 0 {
			ctx = base + int(mvdCtxInc[b])
		}
		if b < a {
			e.decision(ctx, 1)
		} else {
			e.decision(ctx, 0)
			break
		}
	}
	if a >= 9 {
		c.writeExpGolomb(a-9, 3)
	}
	if a != 0 {
		if v < 0 {
			e.bypass(1)
		} else {
			e.bypass(0)
		}
	}
}

// writeExpGolomb writes the k-th order Exp-Golomb suffix in bypass bins.
func (c *depCoder) writeExpGolomb(v, k int) {
	for v >= 1<<k {
		c.e.bypass(1)
		v -= 1 << k
		k++
	}
	c.e.bypass(0)
	for k > 0 {
		k--
		c.e.bypass(v >> k & 1)
	}
}

// writeBlock writes coded_block_flag (unless cbfCtx is negative: an 8x8
// block has none) and, for a coded block, its significance map and levels
// (7.3.5.3.3). lv holds the levels in scan order. It reports whether the
// block is coded.
func (c *depCoder) writeBlock(cbfCtx, cat int, lv []int32) bool {
	e := c.e
	last := -1
	for i := len(lv) - 1; i >= 0; i-- {
		if lv[i] != 0 {
			last = i
			break
		}
	}
	if cbfCtx < 0 {
		if last < 0 {
			panic("mvc: an 8x8 block without coefficients")
		}
	} else if last < 0 {
		e.decision(cbfCtx, 0)
		return false
	} else {
		e.decision(cbfCtx, 1)
	}
	tab := &sigLastCtx[cat]
	for i := 0; i < len(lv)-1; i++ {
		if lv[i] == 0 {
			e.decision(int(tab[i][0]), 0)
			continue
		}
		e.decision(int(tab[i][0]), 1)
		if i == last {
			e.decision(int(tab[i][1]), 1)
			break
		}
		e.decision(int(tab[i][1]), 0)
	}
	absB := absBaseTab[cat]
	gt1Max := 4
	if cat == catChromaDC {
		gt1Max = 3
	}
	numGt1, numEq1 := 0, 0
	for k := last; k >= 0; k-- {
		v := lv[k]
		if v == 0 {
			continue
		}
		a := int(v)
		if a < 0 {
			a = -a
		}
		am1 := a - 1
		ctx0 := absB
		if numGt1 == 0 {
			ctx0 = absB + min(1+numEq1, 4)
		}
		ctxN := absB + 5 + min(numGt1, gt1Max)
		if am1 == 0 {
			e.decision(ctx0, 0)
		} else {
			e.decision(ctx0, 1)
			n := min(am1, 14)
			for b := 1; b < n; b++ {
				e.decision(ctxN, 1)
			}
			if am1 < 14 {
				e.decision(ctxN, 0)
			} else {
				c.writeExpGolomb(am1-14, 0)
			}
		}
		if v < 0 {
			e.bypass(1)
		} else {
			e.bypass(0)
		}
		if am1 == 0 {
			numEq1++
		} else {
			numGt1++
		}
	}
	return true
}
