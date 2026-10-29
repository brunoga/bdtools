package av1

// A temporal dependency model (libaom's TPL): before a mini-GOP is coded,
// each of its frames' 16x16 luma blocks is coded in the model, in coding
// order, at the frame's q: predicted the cheaper way by SATD (intra from
// the source's neighbouring samples, or a motion search's vector into a
// reference), its residual transformed (DCT_DCT), quantized, its rate
// estimated from the levels and its distortion measured on the
// reconstruction, which the later frames predict from. An inter block is
// coded twice: from its reference's reconstruction in the model and from
// the reference's source. What the reference's coding loses it (the
// distortion and rate the source would have saved), and its share of what
// the frames after it lose through it, is handed, from the last frame
// coded back to the first, to the reference's blocks it predicts from. A
// block's importance is its own distortion against that plus what depends
// on it: a superblock much depended on codes finer (its lambda scaled, or
// its q index, as libaom's delta q does), as does a frame (its q).

import (
	"math"
	"math/bits"
	"runtime"
	"sync"
	"sync/atomic"
)

const tplBlock = 16

// tplFrame is a frame's block statistics.
type tplFrame struct {
	job        *frameJob
	cols, rows int
	// recDist and recRate are a block's distortion (the sum of squared
	// errors) and rate (bits) predicted from the references'
	// reconstructions, srcDist and srcRate from their sources (the same
	// for intra blocks)
	recDist, srcDist []float64
	recRate, srcRate []float64
	// depDist and depRate are what the frames after lose through the
	// block
	depDist, depRate []float64
	refDisp          []int    // the reference predicted from, -1 none
	mvx, mvy         []int    // its integer vector, in samples
	rec              []uint16 // the luma reconstruction (width by height)
}

// tplLambda has the model scale the superblocks' lambdas (by
// 1/beta^tplLambdaPow, within 0.5 and 1.5, as libaom's adaptive rdmult
// does) and not their q indexes: nothing coded, the rows still coded at
// once.
// (On the six clips, against the model by SATD ratios: 0.9% smaller by
// PSNR-YUV, 2.5% by SSIM, Blade Runner 2049 3.8%. Tried: tplLambdaPow 0.3
// and 0.7 trade PSNR and SSIM (0.8% and 2.9%, 0.9% and 2.0%); the
// lambdas within 0.33 and 2 or 0.25 and 3, the frames' q to depth 3,
// tplRateK 0.5 or 1: the same.)
var (
	tplLambda    = true
	tplLambdaPow = 0.5
	// tplFrameK moves an anchor's or a depth 2 frame's q by k log2(0.5/r0)
	// (r0 its own share of its importance): finer as more of it is taken.
	// (4 was 1.7% larger, 16 0.4% smaller by PSNR-YUV but 1.9% larger by
	// SSIM and 6.2% by VMAF; centred on 0.25, or within 8 of the frame's
	// q, 0.8% and 2.5% larger: the anchors want their q lowered.)
	tplFrameK = 8.0
	// tplKeyK moves a key frame's q by k log2(0.5/r0), as tplFrameK an
	// anchor's (the key frame costed with the mini-GOP after it). (On the
	// six clips, against the key frame's q as planned: 12 1.5% smaller by
	// PSNR-YUV, Free Solo 8.3%, a static shot the key frame is most of;
	// 8 1.0%, 16 0.7% (Blade Runner 2049 3.3% larger), 8 centred on 0.7
	// 1.1%; costed but its q as planned 0.1%.)
	tplKeyK = 12.0
	// tplRateK is the model's lambda, the coder's times this (libaom's
	// TPL rdmult is a sixth of its coder's).
	tplRateK = 1.0 / 6
)

// tplMaxDelta bounds a superblock's q index offset.
const tplMaxDelta = 48

// tpl sets the coded frames of a mini-GOP's steps' superblock lambdas (or
// q indexes) and the anchors' q.
func (e *Encoder) tpl(steps []step) {
	if dbgNoTPL || e.o.QIndex == 0 {
		return
	}
	w, h := e.o.Width, e.o.Height
	cols, rows := (w+tplBlock-1)/tplBlock, (h+tplBlock-1)/tplBlock
	var frames []*tplFrame
	byDisp := map[int]*tplFrame{}
	for _, s := range steps {
		if s.job == nil {
			continue
		}
		n := cols * rows
		tf := &tplFrame{job: s.job, cols: cols, rows: rows,
			recDist: make([]float64, n), srcDist: make([]float64, n), recRate: make([]float64, n), srcRate: make([]float64, n),
			depDist: make([]float64, n), depRate: make([]float64, n),
			refDisp: make([]int, n), mvx: make([]int, n), mvy: make([]int, n), rec: make([]uint16, w*h)}
		frames = append(frames, tf)
		if _, dup := byDisp[s.job.disp]; !dup {
			byDisp[s.job.disp] = tf
		}
	}
	// the references' sources (as coded) and reconstructions (the
	// model's, of this mini-GOP's frames or an earlier one's, else the
	// source)
	srcOf := func(disp int) *Source {
		if tf, ok := byDisp[disp]; ok {
			return tf.job.src
		}
		return e.recent[disp]
	}
	done := map[int]bool{}
	recOf := func(disp int) []uint16 {
		if tf, ok := byDisp[disp]; ok {
			if done[disp] {
				return tf.rec
			}
			return nil
		}
		return e.tplRec[disp]
	}
	// in waves: the frames whose references in the mini-GOP are coded,
	// at once
	for left := frames; len(left) > 0; {
		var wave, rest []*tplFrame
		for _, tf := range left {
			ready := true
			for _, r := range tf.job.refs {
				if r == nil {
					continue
				}
				if _, in := byDisp[r.disp]; in && !done[r.disp] {
					ready = false
				}
			}
			if ready {
				wave = append(wave, tf)
			} else {
				rest = append(rest, tf)
			}
		}
		if len(wave) == 0 {
			panic("av1: a temporal model's frame waits on itself")
		}
		var wg sync.WaitGroup
		for _, tf := range wave {
			wg.Go(func() { e.tplCode(tf, srcOf, recOf) })
		}
		wg.Wait()
		for _, tf := range wave {
			done[tf.job.disp] = true
		}
		left = rest
	}
	// propagation, last coded first
	for k := len(frames) - 1; k >= 0; k-- {
		tf := frames[k]
		for i := range tf.recDist {
			ref, ok := byDisp[tf.refDisp[i]]
			if tf.refDisp[i] < 0 || !ok || tf.recDist[i] <= 0 {
				continue
			}
			recD, srcD := tf.recDist[i], min(tf.srcDist[i], tf.recDist[i])
			dist := recD - srcD + tf.depDist[i]*(recD-srcD)/recD
			rate := max(tf.recRate[i]-tf.srcRate[i], 0) + tplDeltaRate(tf.depRate[i], recD, srcD, tplBlock*tplBlock)
			bx, by := i%cols*tplBlock+tf.mvx[i], i/cols*tplBlock+tf.mvy[i]
			// spread over the four blocks the displaced block covers
			x0, y0 := floorDiv(bx, tplBlock), floorDiv(by, tplBlock)
			fx, fy := bx-x0*tplBlock, by-y0*tplBlock
			for _, o := range [4][3]int{{0, 0, (tplBlock - fx) * (tplBlock - fy)}, {1, 0, fx * (tplBlock - fy)}, {0, 1, (tplBlock - fx) * fy}, {1, 1, fx * fy}} {
				xx, yy := x0+o[0], y0+o[1]
				if o[2] == 0 || xx < 0 || yy < 0 || xx >= cols || yy >= rows {
					continue
				}
				a := float64(o[2]) / (tplBlock * tplBlock)
				ref.depDist[yy*cols+xx] += dist * a
				ref.depRate[yy*cols+xx] += rate * a
			}
		}
	}
	// the frames' and superblocks' q
	bd := (e.o.BitDepth - 8) >> 1
	for _, tf := range frames {
		j := tf.job
		lambda := lambdaFor(int(dcQlookup[bd][j.q])) * tplRateK
		own := func(i int) float64 { return tf.recDist[i] }
		all := func(i int) float64 { return tf.recDist[i] + tf.depDist[i] + lambda*tf.depRate[i] }
		var sumOwn, sumAll float64
		for i := range tf.recDist {
			sumOwn += own(i)
			sumAll += all(i)
		}
		if sumOwn <= 0 || sumAll <= sumOwn {
			continue
		}
		r0 := sumOwn / sumAll
		switch {
		case j.key && tplKeyK != 0:
			j.q = min(max(j.q-int(math.Round(tplKeyK*math.Log2(0.5/r0))), 1), 255)
		case !j.key && tplFrameK != 0 && j.depth <= 2:
			// the frame's own q by how much the others take from it
			j.q = min(max(j.q-int(math.Round(tplFrameK*math.Log2(0.5/r0))), 1), 255)
		}
		sbCols, sbRows := (2*((w+7)>>3)+15)>>4, (2*((h+7)>>3)+15)>>4
		qs := make([]int, sbCols*sbRows)
		ls := make([]float64, sbCols*sbRows)
		any := false
		for sy := range sbRows {
			for sx := range sbCols {
				var in, al float64
				for y := sy * 4; y < min(sy*4+4, rows); y++ {
					for x := sx * 4; x < min(sx*4+4, cols); x++ {
						in += own(y*cols + x)
						al += all(y*cols + x)
					}
				}
				q, l := j.q, 1.0
				if in > 0 {
					beta := r0 / (in / al)
					q = deltaQFor(j.q, beta, bd)
					l = min(max(math.Pow(beta, -tplLambdaPow), 0.5), 1.5)
				}
				qs[sy*sbCols+sx], ls[sy*sbCols+sx] = q, l
				any = any || q != j.q || l != 1
			}
		}
		switch {
		case !any:
		case tplLambda:
			j.sbLambda = ls
		default:
			j.sbQ = qs
		}
	}
	// the reconstructions the next mini-GOP may predict from
	for _, tf := range frames {
		if tf.job.refresh != 0 {
			e.tplRec[tf.job.disp] = tf.rec
		}
	}
	for d := range e.tplRec {
		if d < e.nIn-recentSources {
			delete(e.tplRec, d)
		}
	}
}

// tplDeltaRate is the rate the frames after a block lose through it
// (av1_delta_rate_cost): of rate bits lost through its reference by a
// block of pix samples, the part its own coding loss (from srcDist to
// recDist) accounts for.
func tplDeltaRate(rate, recDist, srcDist float64, pix int) float64 {
	if srcDist <= 8 || recDist <= 0 {
		return rate
	}
	beta := srcDist / recDist
	logDen := math.Log2(beta) + 2*rate/float64(pix)
	if logDen > math.Log2(10) {
		return math.Log2(1/beta) * float64(pix) / 2
	}
	num := math.Exp2(logDen)
	den := num*beta + (1-beta)*beta
	return float64(pix) * math.Log2(num/den) / 2
}

// deltaQFor is the q index whose DC quantizer is base's over sqrt(beta)
// (av1_get_deltaq_offset), within tplMaxDelta of base.
func deltaQFor(base int, beta float64, bd int) int {
	target := float64(dcQlookup[bd][base]) / math.Sqrt(beta)
	best, bestD := base, math.Inf(1)
	for q := max(base-tplMaxDelta, 1); q <= min(base+tplMaxDelta, 255); q++ {
		if d := math.Abs(float64(dcQlookup[bd][q]) - target); d < bestD {
			best, bestD = q, d
		}
	}
	return best
}

func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// tplScratch is a worker's buffers for tplCode.
type tplScratch struct {
	ms               motionSearch
	is               interScratch
	pred, recP, srcP [tplBlock * tplBlock]uint16
	res, coef, lv    [tplBlock * tplBlock]int32
}

// tplPool keeps the model's workers' buffers for later frames.
var tplPool = sync.Pool{New: func() any { return new(tplScratch) }}

// tplCode codes a frame's blocks in the model, its rows at once.
func (e *Encoder) tplCode(tf *tplFrame, srcOf func(int) *Source, recOf func(int) []uint16) {
	j := tf.job
	cur := j.src
	w, h := e.o.Width, e.o.Height
	type ref struct {
		disp     int
		src, rec *refFrameState
	}
	var refs []ref
	seen := map[int]bool{}
	for _, r := range j.refs {
		if r == nil || seen[r.disp] {
			continue
		}
		seen[r.disp] = true
		s := srcOf(r.disp)
		if s == nil {
			continue
		}
		rs := &refFrameState{rec: [3][]uint16{s.Y, s.Cb, s.Cr}, stride: [3]int{s.StrideY, s.StrideC, s.StrideC}, width: w, height: h}
		rr := rs
		if rec := recOf(r.disp); rec != nil {
			rr = &refFrameState{rec: [3][]uint16{rec}, stride: [3]int{w}, width: w, height: h}
		}
		refs = append(refs, ref{r.disp, rs, rr})
	}
	bd := (e.o.BitDepth - 8) >> 1
	dcQ, acQ := int(dcQlookup[bd][j.q]), int(acQlookup[bd][j.q])
	var next atomic.Int32
	var wg sync.WaitGroup
	for range min(runtime.GOMAXPROCS(0), tf.rows) {
		wg.Go(func() {
			sc := tplPool.Get().(*tplScratch) //nolint:errcheck // always a *tplScratch
			defer tplPool.Put(sc)
			sc.ms.reset()
			for {
				by := int(next.Add(1)) - 1
				if by >= tf.rows {
					return
				}
				for bx := range tf.cols {
					i := by*tf.cols + bx
					x0, y0 := bx*tplBlock, by*tplBlock
					bw, bh := min(tplBlock, w-x0)&^3, min(tplBlock, h-y0)&^3
					tf.refDisp[i] = -1
					if bw == 0 || bh == 0 {
						continue
					}
					blk := cur.Y[y0*cur.StrideY+x0:]
					intra := e.tplIntra(cur, x0, y0, bw, bh, sc.pred[:])
					// the motion search on the sources, the best reference
					// costed from its reconstruction
					best, bestK, bestMv := math.Inf(1), -1, mv{}
					for k, r := range refs {
						b := meBlock{ref: r.src, src: cur.Y, srcStride: cur.StrideY, x: x0, y: y0, w: bw, h: bh,
							bitDepth: e.o.BitDepth, searchRange: 32, cost: func(mv) float64 { return 0 }}
						var m mv
						var c float64
						if !e.tools.tplFull {
							m, c = sc.ms.search(&b, []mv{{}})
						} else {
							m, _ = sc.ms.fullPel(&b, []mv{{}})
							c = sc.ms.subCost(&b, m)
						}
						if c < best {
							best, bestK, bestMv = c, k, m
						}
					}
					var recP []uint16
					if bestK >= 0 {
						filt := [2]int{EIGHTTAP, EIGHTTAP}
						predictInter(refs[bestK].rec, 0, x0, y0, bw, bh, bestMv, filt, e.o.BitDepth, sc.recP[:], tplBlock, &sc.is)
						if satdBlock(blk, cur.StrideY, sc.recP[:], tplBlock, bw, bh) < intra {
							recP = sc.recP[:]
							predictInter(refs[bestK].src, 0, x0, y0, bw, bh, bestMv, filt, e.o.BitDepth, sc.srcP[:], tplBlock, &sc.is)
						}
					}
					rec := tf.rec[y0*w+x0:]
					if recP == nil {
						d, r := e.tplBlockCode(sc, blk, cur.StrideY, sc.pred[:], bw, bh, dcQ, acQ, rec, w)
						tf.recDist[i], tf.srcDist[i], tf.recRate[i], tf.srcRate[i] = d, d, r, r
						continue
					}
					tf.recDist[i], tf.recRate[i] = e.tplBlockCode(sc, blk, cur.StrideY, recP, bw, bh, dcQ, acQ, rec, w)
					tf.srcDist[i], tf.srcRate[i] = e.tplBlockCode(sc, blk, cur.StrideY, sc.srcP[:], bw, bh, dcQ, acQ, nil, 0)
					tf.refDisp[i] = refs[bestK].disp
					tf.mvx[i], tf.mvy[i] = int(math.Round(float64(bestMv.col)/8)), int(math.Round(float64(bestMv.row)/8))
				}
			}
		})
	}
	wg.Wait()
}

// tplBlockCode codes a block predicted by pred (tplBlock wide) in the
// model: its residual's DCT_DCT quantized to the nearest level, the
// levels' rate (libaom's estimate: for each coefficient to the end of
// block, its level's bit length and its sign), the reconstruction's
// distortion. The reconstruction goes to rec (stride recStride) unless it
// is nil.
func (e *Encoder) tplBlockCode(sc *tplScratch, src []uint16, srcStride int, pred []uint16, bw, bh, dcQ, acQ int, rec []uint16, recStride int) (dist, rate float64) {
	const n = tplBlock * tplBlock
	res, coef, lv := sc.res[:], sc.coef[:], sc.lv[:]
	clear(res)
	for y := range bh {
		for x := range bw {
			res[y*tplBlock+x] = int32(src[y*srcStride+x]) - int32(pred[y*tplBlock+x])
		}
	}
	forwardTransform(res, TX_16X16, DCT_DCT, e.o.BitDepth, false, coef)
	inv := invScanFor(TX_16X16, DCT_DCT)
	eob := 0
	for i := range n {
		qs := int64(acQ)
		if i == 0 {
			qs = int64(dcQ)
		}
		c := int64(coef[i])
		a := c
		if a < 0 {
			a = -a
		}
		l := (a + qs/2) / qs
		if c < 0 {
			l = -l
		}
		lv[i] = int32(l) //nolint:gosec // a transform coefficient's level
		if l != 0 {
			eob = max(eob, int(inv[i])+1)
		}
	}
	rate = 1
	scan := scanFor(TX_16X16, DCT_DCT)
	for k := range eob {
		a := uint32(abs32(lv[scan[k]]))
		rate += float64(bits.Len32(a+1) - 1 + 1 + b2i(a > 0))
	}
	dequantize(lv, TX_16X16, dcQ, acQ, e.o.BitDepth, coef)
	inverseTransform(coef, TX_16X16, DCT_DCT, e.o.BitDepth, false, res)
	maxV := int32(1)<<e.o.BitDepth - 1
	for y := range bh {
		for x := range bw {
			v := clip(int32(pred[y*tplBlock+x])+res[y*tplBlock+x], maxV)
			d := float64(int32(src[y*srcStride+x]) - v)
			dist += d * d
			if rec != nil {
				rec[y*recStride+x] = uint16(v) //nolint:gosec // clipped to the bit depth
			}
		}
	}
	return dist, rate
}

// tplIntra is a block's intra cost by SATD, the best of DC, vertical,
// horizontal and a bilinear smooth prediction from the source's
// neighbouring samples, its prediction in pred (tplBlock wide).
func (e *Encoder) tplIntra(cur *Source, x0, y0, bw, bh int, pred []uint16) int64 {
	var above, left [tplBlock]int32
	var try [tplBlock * tplBlock]uint16
	mid := int32(1) << (e.o.BitDepth - 1)
	at := func(x, y int) int32 { return int32(cur.Y[y*cur.StrideY+x]) }
	for k := range bw {
		above[k] = mid
		if y0 > 0 {
			above[k] = at(x0+k, y0-1)
		}
	}
	for k := range bh {
		left[k] = mid
		if x0 > 0 {
			left[k] = at(x0-1, y0+k)
		}
	}
	var dc int32
	switch {
	case x0 > 0 && y0 > 0:
		var s int32
		for k := range bw {
			s += above[k]
		}
		for k := range bh {
			s += left[k]
		}
		dc = s / int32(bw+bh)
	case y0 > 0:
		var s int32
		for k := range bw {
			s += above[k]
		}
		dc = s / int32(bw)
	case x0 > 0:
		var s int32
		for k := range bh {
			s += left[k]
		}
		dc = s / int32(bh)
	default:
		dc = mid
	}
	corner := mid
	if x0 > 0 && y0 > 0 {
		corner = at(x0-1, y0-1)
	}
	blk := cur.Y[y0*cur.StrideY+x0:]
	best := int64(math.MaxInt64)
	for mode := range 4 {
		for y := range bh {
			for x := range bw {
				var v int32
				switch mode {
				case 0:
					v = dc
				case 1:
					v = above[x]
				case 2:
					v = left[y]
				default: // the plane through the corner and both edges
					v = above[x] + left[y] - corner
					v = min(max(v, 0), 2*mid-1)
				}
				try[y*tplBlock+x] = uint16(v) //nolint:gosec // within the bit depth
			}
		}
		if c := satdBlock(blk, cur.StrideY, try[:], tplBlock, bw, bh); c < best {
			best = c
			copy(pred, try[:])
		}
	}
	return best
}
