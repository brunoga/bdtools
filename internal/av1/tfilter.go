package av1

// The temporal filter: the source of a mini-GOP's anchor (the ALTREF,
// coded first and predicted from by every other frame of the mini-GOP)
// replaced by a motion compensated average of it and its neighbours, so
// that the frames predicting from it find less noise to code. It is
// libaom's (av1/encoder/temporal_filter.c: av1_temporal_filter and what
// it calls, Copyright the Alliance for Open Media, BSD-2-Clause, see
// LICENSE.libaom), ported: the picture in 64x64 blocks; each of the other
// pictures searched for each block (the 64x64 whole, then its four
// 32x32s, then each one's four 16x16s, the vectors 1/8 sample, the
// errors the variance of the difference per sample) and split into the
// 16x16s or the 32x32s or kept whole as their errors compare; each
// predicted by its vectors (chroma too, by the luma vectors) and every
// sample's prediction weighted, non-local means: the squared differences
// of a 5x5 window around it (and, for chroma, those of the luma samples
// it covers) and the block's error combined, scaled by a decay from the
// q, the picture's noise level (av1_estimate_noise_level) and the filter
// strength and by how far the block moved, and taken through exp(-x);
// the picture itself weighted 1000 everywhere. The weighted sum over the
// pictures, divided by the weights, is the filtered sample.
//
// The motion search is the package's (an integer search from the vector
// the previous picture found for the block, then a subpel refinement by
// SATD), not libaom's NSTEP and mesh search, and the predictions use the
// 8-tap sharp filter, not libaom's 12-tap MULTITAP_SHARP2; the weighting
// and the normalisation are libaom's exactly.

import (
	"math"
	"runtime"
	"sync"
)

// libaom's temporal filter constants (temporal_filter.h) and its
// defaults (arnr_strength, arnr_max_frames).
const (
	tfBlock                    = 64 // TF_BLOCK_SIZE
	tfWindowLength             = 5
	tfWeightScale              = 1000
	tfWindowBlockBalanceWeight = 5
	tfQDecayThreshold          = 20
	tfSearchErrorNormWeight    = 20
	tfStrengthThreshold        = 4
	tfSearchDistanceThreshold  = 0.1
	tfQIndexCutoff             = 128
	noiseEdgeThreshold         = 50
	tfStrength                 = 5 // arnr_strength
	tfMaxFrames                = 7 // arnr_max_frames
	sqrtPiBy2                  = 1.25331413732
)

// dbgNoTF codes the anchors' sources unfiltered.
var dbgNoTF bool

// tfWindowSize is how many pictures before and after the one filtered
// the filter takes, of maxBefore and maxAfter available (the key frame
// before it at most, none past the next): libaom's
// tf_setup_filtering_buffer for an ARF, at its default arnr_max_frames
// (half on each side, the rest of a side short of pictures to the other;
// without first pass statistics, no correlation based asymmetry).
func tfWindowSize(maxBefore, maxAfter int) (before, after int) {
	n := tfMaxFrames
	if min(maxBefore, maxAfter) >= n/2 {
		return n / 2, n / 2
	}
	if maxAfter < n/2 {
		after = maxAfter
		before = min(n-1-after, maxBefore)
	} else {
		before = maxBefore
		after = min(n-1-before, maxAfter)
	}
	return before, after
}

// tfQFactor is libaom's get_q: the q (av1_convert_qindex_to_q) of a q
// index, the AC quantizer scaled to 8 bits' range and quartered.
func tfQFactor(qIndex, bitDepth int) int {
	bd := (bitDepth - 8) >> 1
	q := float64(acQlookup[bd][min(max(qIndex, 0), 255)])
	if bitDepth == 10 {
		return int(q / 16)
	}
	return int(q / 4)
}

// tfCtx is a filtering's state shared by its rows.
type tfCtx struct {
	// pics are the window's pictures, their planes padded to whole
	// 64x64 blocks (edge samples repeated, as libaom's extended borders),
	// as references to predict from (clamped to the pictures' real size)
	pics             []refFrameState
	center           int
	bitDepth         int
	width, height    int
	mbCols, mbRows   int
	noise            [3]float64
	qFactor          int
	out              [3][]uint16 // padded as the pictures
	cost             func(m mv) float64
	refMVThresh      int
	distanceThresh   float64
	decay            [3]float64
	sharp            [2]int
	allowSubBlocksME bool
}

// temporalFilter filters window[center] (window in display order, each
// a width by height 4:2:0 picture of bitDepth bits) with the others of
// the window, as libaom filters an ARF coded at qIndex, giving a new
// picture.
func temporalFilter(window []*Source, center, qIndex, bitDepth, width, height int) *Source {
	t := &tfCtx{center: center, bitDepth: bitDepth, width: width, height: height,
		mbCols: (width + tfBlock - 1) / tfBlock, mbRows: (height + tfBlock - 1) / tfBlock,
		qFactor: tfQFactor(qIndex, bitDepth), sharp: [2]int{EIGHTTAP_SHARP, EIGHTTAP_SHARP},
		allowSubBlocksME: true}
	pw, ph := t.mbCols*tfBlock, t.mbRows*tfBlock
	cw, ch := (width+1)/2, (height+1)/2
	t.pics = make([]refFrameState, len(window))
	for i, s := range window {
		p := &t.pics[i]
		p.width, p.height = width, height
		p.rec[0], p.stride[0] = padPlane(s.Y, s.StrideY, width, height, pw, ph), pw
		p.rec[1], p.stride[1] = padPlane(s.Cb, s.StrideC, cw, ch, pw/2, ph/2), pw/2
		p.rec[2], p.stride[2] = padPlane(s.Cr, s.StrideC, cw, ch, pw/2, ph/2), pw/2
	}
	for p := range 3 {
		t.out[p] = make([]uint16, len(t.pics[center].rec[p]))
	}

	// av1_estimate_noise_level, on the picture filtered
	src := window[center]
	t.noise[0] = estimateNoise(src.Y, src.StrideY, width, height, bitDepth, noiseEdgeThreshold)
	t.noise[1] = estimateNoise(src.Cb, src.StrideC, cw, ch, bitDepth, noiseEdgeThreshold)
	t.noise[2] = estimateNoise(src.Cr, src.StrideC, cw, ch, bitDepth, noiseEdgeThreshold)

	// tf_motion_search's vector cost (MV_COST_L1_*: SAD_LAMBDA_* per
	// sample of the vector's L1 length) and its threshold for passing the
	// vector on to the next picture
	minSize := min(width, height)
	lambda := int32(32) // SAD_LAMBDA_LOWRES
	t.refMVThresh = 3
	switch {
	case minSize >= 720:
		lambda, t.refMVThresh = 8, 12
	case minSize >= 480:
		lambda = 15
	}
	t.cost = func(m mv) float64 { return float64((lambda * (abs32(m.row) + abs32(m.col))) >> 3) }
	t.distanceThresh = max(float64(minSize)*tfSearchDistanceThreshold, 1)

	// av1_apply_temporal_filter_c's decay factors: larger q, noise and
	// strength filter harder
	qDecay := math.Pow(float64(t.qFactor)/tfQDecayThreshold, 2)
	qDecay = min(max(qDecay, 1e-5), 1)
	if t.qFactor >= tfQIndexCutoff {
		qDecay = 0.5 * math.Pow(float64(t.qFactor)/64, 2)
	}
	sDecay := math.Pow(float64(tfStrength)/tfStrengthThreshold, 2)
	sDecay = min(max(sDecay, 1e-5), 1)
	for p := range 3 {
		nDecay := 0.5 + math.Log(2*t.noise[p]+5.0)
		t.decay[p] = 1 / (nDecay * qDecay * sDecay)
	}

	// the rows are independent (each block column starts its vector
	// afresh): a few at once
	var wg sync.WaitGroup
	rows := make(chan int, t.mbRows)
	for r := range t.mbRows {
		rows <- r
	}
	close(rows)
	for range min(runtime.GOMAXPROCS(0), t.mbRows) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := newTFWorker()
			for r := range rows {
				t.filterRow(w, r)
			}
		}()
	}
	wg.Wait()

	out := &Source{Y: make([]uint16, width*height), Cb: make([]uint16, cw*ch), Cr: make([]uint16, cw*ch), StrideY: width, StrideC: cw}
	for y := range height {
		copy(out.Y[y*width:(y+1)*width], t.out[0][y*pw:])
	}
	for y := range ch {
		copy(out.Cb[y*cw:(y+1)*cw], t.out[1][y*pw/2:])
		copy(out.Cr[y*cw:(y+1)*cw], t.out[2][y*pw/2:])
	}
	return out
}

// padPlane copies a w by h plane into a pw by ph one, the edge samples
// repeated.
func padPlane(in []uint16, stride, w, h, pw, ph int) []uint16 {
	out := make([]uint16, pw*ph)
	for y := range ph {
		row := in[min(y, h-1)*stride:]
		dst := out[y*pw : (y+1)*pw]
		copy(dst[:w], row[:w])
		for x := w; x < pw; x++ {
			dst[x] = row[w-1]
		}
	}
	return out
}

// tfWorker is a row's working memory.
type tfWorker struct {
	ms         motionSearch
	pred       [3][]uint16 // a block's prediction, by plane
	accum      [3][]uint32
	count      [3][]uint16
	squareDiff []uint32
	lumaSSESum []uint32
}

func newTFWorker() *tfWorker {
	w := &tfWorker{squareDiff: make([]uint32, tfBlock*tfBlock), lumaSSESum: make([]uint32, tfBlock*tfBlock)}
	for p := range 3 {
		n := tfBlock * tfBlock
		if p > 0 {
			n /= 4
		}
		w.pred[p], w.accum[p], w.count[p] = make([]uint16, n), make([]uint32, n), make([]uint16, n)
	}
	return w
}

// filterRow is av1_tf_do_filtering_row: each block of row r filtered
// with every picture of the window, then normalised into the output.
func (t *tfCtx) filterRow(w *tfWorker, r int) {
	for c := range t.mbCols {
		for p := range 3 {
			clear(w.accum[p])
			clear(w.count[p])
		}
		var refMV mv // passed down along the pictures
		for f := range t.pics {
			if f == t.center {
				// the vector, for the pictures after, points the other way
				refMV = mv{row: -refMV.row, col: -refMV.col}
				t.applySelf(w, r, c)
				continue
			}
			ref := &t.pics[f]
			mvs, mses := t.motionSearch(w, ref, r, c, &refMV)
			t.buildPredictor(w, ref, r, c, &mvs)
			t.apply(w, r, c, &mvs, &mses)
		}
		t.normalize(w, r, c)
	}
}

// motionSearch is tf_motion_search: block (r, c) of the picture filtered
// searched in ref, the 64x64 from *refMV, each 32x32 from the 64x64's
// vector, each 16x16 from its 32x32's, then the partition decided. It
// gives the 16x16s' vectors and errors (each the variance of the
// difference per sample, rounded) and leaves the 64x64's vector in
// *refMV unless its error is too large.
func (t *tfCtx) motionSearch(w *tfWorker, ref *refFrameState, r, c int, refMV *mv) (mvs [16]mv, mses [16]int) {
	bx, by := c*tfBlock, r*tfBlock
	block := func(x, y, size int, start mv) (mv, int) {
		b := &meBlock{ref: ref, src: t.pics[t.center].rec[0], srcStride: t.pics[t.center].stride[0],
			x: x, y: y, w: size, h: size, bitDepth: t.bitDepth, filt: t.sharp, allowHP: true, cost: t.cost}
		m, _ := w.ms.fullPel(b, []mv{start, {}})
		// the subpel refinement only refines: no vector cost
		b.cost = func(mv) float64 { return 0 }
		m, _ = w.ms.subPel(b, m)
		return m, t.blockError(w, b, m)
	}
	blockMV, blockMSE := block(bx, by, tfBlock, *refMV)
	*refMV = blockMV
	var midMVs [4]mv
	var midMSEs [4]int
	if t.allowSubBlocksME {
		for k := range 4 {
			x, y := bx+(k&1)*32, by+(k>>1)*32
			midMVs[k], midMSEs[k] = block(x, y, 32, *refMV)
			for s := range 4 {
				mvs[k*4+s], mses[k*4+s] = block(x+(s&1)*16, y+(s>>1)*16, 16, midMVs[k])
			}
		}
		tfBlockPartition(blockMV, blockMSE, &midMVs, &midMSEs, &mvs, &mses)
	} else {
		for i := range mvs {
			mvs[i], mses[i] = blockMV, blockMSE
		}
	}
	if blockMSE > t.refMVThresh<<uint(t.bitDepth-8) {
		*refMV = mv{}
	}
	return
}

// blockError is the variance of the difference between b's source and
// its prediction by m, per sample, rounded (libaom's subpel search's
// error, DIVIDE_AND_ROUND by the block's samples), at 8 bits' scale.
func (t *tfCtx) blockError(w *tfWorker, b *meBlock, m mv) int {
	pred := w.ms.pred[:]
	predictInter(b.ref, 0, b.x, b.y, b.w, b.h, m, b.filt, b.bitDepth, pred, b.w, &w.ms.s)
	var sum, sse int64
	for i := range b.h {
		s := b.src[(b.y+i)*b.srcStride+b.x:][:b.w]
		p := pred[i*b.w:][:b.w]
		for j, v := range s {
			d := int64(v) - int64(p[j])
			sum += d
			sse += d * d
		}
	}
	if sh := uint(b.bitDepth - 8); sh > 0 {
		// libaom's high bit depth variances are at 8 bits' scale
		sse = (sse + 1<<(2*sh)>>1) >> (2 * sh)
		sum = (sum + 1<<sh>>1) >> sh
	}
	n := int64(b.w * b.h)
	variance := max(sse-sum*sum/n, 0)
	return int((variance + n/2) / n)
}

// tfBlockPartition is tf_determine_block_partition: each 32x32's four
// 16x16s merged when the 32x32's error is not much above theirs and
// theirs are close; then all sixteen merged into the 64x64 so.
func tfBlockPartition(blockMV mv, blockMSE int, midMVs *[4]mv, midMSEs *[4]int, mvs *[16]mv, mses *[16]int) {
	for k := range 4 {
		lo, hi, sum := math.MaxInt, math.MinInt, 0
		for i := k * 4; i < k*4+4; i++ {
			sum += mses[i]
			lo, hi = min(lo, mses[i]), max(hi, mses[i])
		}
		if (midMSEs[k]*15 <= sum*4 && hi-lo < 48) || (midMSEs[k]*14 <= sum*4 && hi-lo < 24) {
			for i := k * 4; i < k*4+4; i++ {
				mvs[i], mses[i] = midMVs[k], midMSEs[k]
			}
		}
	}
	lo, hi, sum := math.MaxInt, math.MinInt, 0
	for _, e := range mses {
		sum += e
		lo, hi = min(lo, e), max(hi, e)
	}
	if (blockMSE*15 <= sum && (hi-lo)*16 < sum*3) || (blockMSE*14 <= sum && (hi-lo)*8 < sum) {
		for i := range mvs {
			mvs[i], mses[i] = blockMV, blockMSE
		}
	}
}

// buildPredictor is tf_build_predictor: block (r, c) of every plane
// predicted from ref, each 16x16 (8x8 in chroma) by its vector.
func (t *tfCtx) buildPredictor(w *tfWorker, ref *refFrameState, r, c int, mvs *[16]mv) {
	for p := range 3 {
		size := tfBlock >> uint(min(p, 1))
		s16 := size / 4
		x0, y0 := c*size, r*size
		for i := range 16 {
			// the 16x16s in order: 32x32 by 32x32, then within each
			k, sub := i/4, i%4
			ox := (k&1)*2*s16 + (sub&1)*s16
			oy := (k>>1)*2*s16 + (sub>>1)*s16
			predictInter(ref, p, x0+ox, y0+oy, s16, s16, mvs[i], t.sharp, t.bitDepth, w.pred[p][oy*size+ox:], size, &w.ms.s)
		}
	}
}

// applySelf is tf_apply_temporal_filter_self: the picture filtered, at
// full weight.
func (t *tfCtx) applySelf(w *tfWorker, r, c int) {
	for p := range 3 {
		ref := &t.pics[t.center]
		size := tfBlock >> uint(min(p, 1))
		src, stride := ref.rec[p], ref.stride[p]
		for i := range size {
			row := src[(r*size+i)*stride+c*size:][:size]
			for j, v := range row {
				w.accum[p][i*size+j] += tfWeightScale * uint32(v)
				w.count[p][i*size+j] += tfWeightScale
			}
		}
	}
}

// apply is av1_apply_temporal_filter_c: each sample of the block's
// prediction weighted by its window's error (chroma's with the luma
// samples' it covers), the block's, the decay and how far it moved, and
// accumulated.
func (t *tfCtx) apply(w *tfWorker, r, c int, mvs *[16]mv, mses *[16]int) {
	const invFactor = 1.0 / ((tfWindowBlockBalanceWeight + 1) * tfSearchErrorNormWeight)
	const weightFactor = tfWindowBlockBalanceWeight * invFactor
	const half = tfWindowLength >> 1

	var dFactor [16]float64
	for i, m := range mvs {
		distance := math.Sqrt(math.Pow(float64(m.row), 2) + math.Pow(float64(m.col), 2))
		dFactor[i] = max(distance/t.distanceThresh, 1)
	}
	clear(w.lumaSSESum)
	cur := &t.pics[t.center]
	for p := range 3 {
		ss := min(p, 1)
		size := tfBlock >> uint(ss)
		numRef := tfWindowLength * tfWindowLength
		if p > 0 {
			numRef += 4 // 1 << (ss_x + ss_y)
		}
		invNumRef := 1.0 / float64(numRef)
		if p == 1 {
			// the luma squared differences each chroma sample covers
			// (kept for both chroma planes)
			for i := range size {
				for j := range size {
					d := w.squareDiff[(2*i)*tfBlock+2*j:]
					e := w.squareDiff[(2*i+1)*tfBlock+2*j:]
					w.lumaSSESum[i*size+j] += d[0] + d[1] + e[0] + e[1]
				}
			}
		}
		src, stride := cur.rec[p], cur.stride[p]
		pred := w.pred[p]
		for i := range size {
			row := src[(r*size+i)*stride+c*size:][:size]
			for j, v := range row {
				d := int32(v) - int32(pred[i*size+j])
				w.squareDiff[i*size+j] = uint32(d * d) //nolint:gosec // a square
			}
		}
		for i := range size {
			for j := range size {
				var sum uint64
				for wi := -half; wi <= half; wi++ {
					y := min(max(i+wi, 0), size-1)
					for wj := -half; wj <= half; wj++ {
						x := min(max(j+wj, 0), size-1)
						sum += uint64(w.squareDiff[y*size+x])
					}
				}
				sum += uint64(w.lumaSSESum[i*size+j])
				if t.bitDepth > 8 {
					sum >>= uint((t.bitDepth - 8) * 2)
				}
				windowError := float64(sum) * invNumRef
				h2 := size / 2
				sub := (i/h2*2+j/h2)*4 + (i%h2)/(size/4)*2 + (j%h2)/(size/4)
				combined := weightFactor*windowError + float64(mses[sub])*invFactor
				scaled := min(combined*dFactor[sub]*t.decay[p], 7)
				weight := uint32(math.Exp(-scaled) * tfWeightScale)
				w.accum[p][i*size+j] += weight * uint32(pred[i*size+j])
				w.count[p][i*size+j] += uint16(weight) //nolint:gosec // at most 1000
			}
		}
	}
}

// normalize is tf_normalize_filtered_frame: the block's sums divided by
// their weights, rounded, into the output.
func (t *tfCtx) normalize(w *tfWorker, r, c int) {
	for p := range 3 {
		size := tfBlock >> uint(min(p, 1))
		stride := t.pics[t.center].stride[p]
		for i := range size {
			dst := t.out[p][(r*size+i)*stride+c*size:][:size]
			for j := range dst {
				n := uint32(w.count[p][i*size+j])
				dst[j] = uint16((w.accum[p][i*size+j] + n>>1) / n) //nolint:gosec // a weighted mean of samples
			}
		}
	}
}

// estimateNoise is av1_estimate_noise_from_single_plane (its high bit
// depth form, the same at 8 bits): the mean absolute Laplacian of the
// plane's smooth samples (those whose Sobel gradient is below
// edgeThresh), both at 8 bits' scale, as a noise standard deviation; -1
// when fewer than 16 samples are smooth.
func estimateNoise(src []uint16, stride, w, h, bitDepth, edgeThresh int) float64 {
	sh := uint(bitDepth - 8)
	rnd := (1 << sh) >> 1
	var accum int64
	count := 0
	for i := 1; i < h-1; i++ {
		for j := 1; j < w-1; j++ {
			a, b, cc := src[(i-1)*stride+j-1:], src[i*stride+j-1:], src[(i+1)*stride+j-1:]
			m00, m01, m02 := int(a[0]), int(a[1]), int(a[2])
			m10, m11, m12 := int(b[0]), int(b[1]), int(b[2])
			m20, m21, m22 := int(cc[0]), int(cc[1]), int(cc[2])
			gx := (m00 - m02) + (m20 - m22) + 2*(m10-m12)
			gy := (m00 - m20) + (m02 - m22) + 2*(m01-m21)
			ga := (abs(gx) + abs(gy) + rnd) >> sh
			if ga < edgeThresh {
				v := 4*m11 - 2*(m01+m21+m10+m12) + (m00 + m02 + m20 + m22)
				accum += int64((abs(v) + rnd) >> sh)
				count++
			}
		}
	}
	if count < 16 {
		return -1
	}
	return float64(accum) / float64(6*count) * sqrtPiBy2
}

// filterAnchor replaces the source of a mini-GOP's anchor (its last
// picture, coded first and hidden) by the temporally filtered one, the
// window the pictures of the mini-GOP and those kept from before it (to
// the last key frame); libaom would take pictures after it too, from
// its lookahead, which the encoder does not hold. A mini-GOP of one
// picture is not filtered (the picture is shown as coded). It keeps the
// mini-GOP's sources, as they came, for the next one's window.
func (e *Encoder) filterAnchor(gop []*frameJob) {
	srcs := make([]*Source, 0, len(e.tfPast)+len(gop))
	srcs = append(srcs, e.tfPast...)
	for _, j := range gop {
		srcs = append(srcs, j.src)
	}
	e.tfPast = srcs[max(len(srcs)-(tfMaxFrames-1), 0):]
	if dbgNoTF || len(gop) < 2 || e.o.QIndex == 0 {
		return
	}
	// libaom's q for the filter (get_q at a constant q) is the run's
	// cq_level, not the anchor's own
	before, _ := tfWindowSize(len(srcs)-1, 0)
	window := srcs[len(srcs)-1-before:]
	gop[len(gop)-1].src = temporalFilter(window, before, e.o.QIndex, e.o.BitDepth, e.o.Width, e.o.Height)
}
