package hevc

import (
	"cmp"
	"math"
	"slices"
)

// Inter coding: motion search, merge candidates, the inter CU's
// rate-distortion decision and its syntax.

// interCand is an inter prediction the encoder considers for a CU.
type interCand struct {
	f        mvField
	merge    bool
	mergeIdx int
	mvd      [2]mv
	mvpFlag  [2]int
	cost     float64 // SATD and bits
}

// mvBits is the bits of an mvd component, roughly (9.3.3.6: a flag, a
// flag, first order Exp-Golomb, a sign), from a table for the usual
// range.
func mvBits(v int) float64 {
	a := iabs(v)
	if a < len(mvBitsTab) {
		return mvBitsTab[a]
	}
	return mvBitsSlow(a)
}

var mvBitsTab = func() (t [1024]float64) {
	for a := range t {
		t[a] = mvBitsSlow(a)
	}
	return t
}()

func mvBitsSlow(a int) float64 {
	if a == 0 {
		return 1
	}
	if a == 1 {
		return 3
	}
	a -= 2
	k := 1
	bits := 3
	for a >= 1<<k {
		a -= 1 << k
		k++
		bits++
	}
	return float64(bits + k)
}

// mvdBits is the bits of a vector's difference from its predictor.
func mvdBits(d mv) float64 { return mvBits(int(d.x)) + mvBits(int(d.y)) }

// refBits is the bits of a reference index among n.
func refBits(r, n int) float64 {
	if n <= 1 {
		return 0
	}
	return float64(min(r+1, n-1))
}

// mergeBits is the bits of a merge index among n candidates.
func mergeBits(idx, n int) float64 {
	if n <= 1 {
		return 0
	}
	return float64(min(idx+1, n-1))
}

// predictLuma makes the luma prediction of the block (x0, y0, w, h)
// moved by f into dst (stride w).
func (c *picCoder) predictLuma(dst []uint16, x0, y0, w, h int, f *mvField) {
	sd, s := &c.sd, c.s
	depth := s.bitDepth
	for l := range 2 {
		if f.pred&(1<<l) == 0 {
			continue
		}
		ref := sd.refList[l][f.refIdx[l]]
		sd.interpolate(ref, 0, x0, y0, w, h, f.mv[l], sd.pred.l[l][:w*h], depth)
	}
	maxV := 1<<depth - 1
	shift1 := 14 - depth
	l0, l1 := sd.pred.l[0][:w*h], sd.pred.l[1][:w*h]
	switch f.pred {
	case 3:
		putBlock(dst, w, l0, l1, w, w, h, 1, 1, 1<<shift1, uint(shift1+1), 0, maxV)
	case 1:
		putBlock(dst, w, l0, l0, w, w, h, 1, 0, 1<<(shift1-1), uint(shift1), 0, maxV)
	default:
		putBlock(dst, w, l1, l1, w, w, h, 1, 0, 1<<(shift1-1), uint(shift1), 0, maxV)
	}
}

// sadRef is the SAD of the source block against the reference's block
// at an integer offset, the reference's edges repeated.
func (c *picCoder) sadRef(ref *picture, x0, y0, w, h, dx, dy int) int {
	s := c.s
	src := c.src.Y
	if x0+dx >= 0 && x0+dx+w <= s.width && y0+dy >= 0 && y0+dy+h <= s.height {
		return sadBlock(src[y0*c.src.StrideY+x0:], c.src.StrideY, ref.y[(y0+dy)*ref.strideY+x0+dx:], ref.strideY, w, h)
	}
	sum := 0
	for y := range h {
		ry := clip3(0, s.height-1, y0+y+dy)
		row := ref.y[ry*ref.strideY:]
		o := src[(y0+y)*c.src.StrideY+x0:]
		if rx := x0 + dx; rx >= 0 && rx+w <= s.width {
			r := row[rx : rx+w]
			for x := range w {
				d := int(o[x]) - int(r[x])
				if d < 0 {
					d = -d
				}
				sum += d
			}
			continue
		}
		for x := range w {
			d := int(o[x]) - int(row[clip3(0, s.width-1, x0+x+dx)])
			if d < 0 {
				d = -d
			}
			sum += d
		}
	}
	return sum
}

// searchRange is how far the integer search goes from its start, in
// samples.
const searchRange = 64

// motionSearch finds the vector for list l's reference refIdx predicting
// the block (x0, y0, w, h) of the CU being coded: an integer search from
// the predictors, then half and quarter samples on the SATD. It returns
// the vector, its cost (SATD plus the bits of its difference from the
// better predictor) and that predictor's flag.
func (c *picCoder) motionSearch(x0, y0, w, h, partIdx, l, refIdx int) (mv, float64, int) {
	ms := c.intSearch(x0, y0, w, h, partIdx, l, refIdx)
	return c.subSearch(&ms)
}

// mvSearch is a motion search under way: the block, the reference, its
// predictors, and the best vector so far with its cost.
type mvSearch struct {
	x0, y0, w, h, l, refIdx int
	ref                     *picture
	preds                   [2]mv
	best                    mv
	cost                    float64 // SAD and bits, at whole samples
}

// mvCost is the cost of a vector's bits from the better predictor, and
// which predictor that is.
func (c *picCoder) mvCost(ms *mvSearch, v mv) (float64, int) {
	b0 := mvdBits(mv{v.x - ms.preds[0].x, v.y - ms.preds[0].y})
	b1 := mvdBits(mv{v.x - ms.preds[1].x, v.y - ms.preds[1].y})
	if b1 < b0 {
		return c.lambdaS * b1, 1
	}
	return c.lambdaS * b0, 0
}

// intSearch searches the whole-sample positions, by SAD, from the
// predictors.
func (c *picCoder) intSearch(x0, y0, w, h, partIdx, l, refIdx int) mvSearch {
	sd := &c.sd
	ref := sd.refList[l][refIdx]
	ms := mvSearch{x0: x0, y0: y0, w: w, h: h, l: l, refIdx: refIdx, ref: ref,
		preds: [2]mv{sd.amvp(x0, y0, w, h, partIdx, l, refIdx, 0), sd.amvp(x0, y0, w, h, partIdx, l, refIdx, 1)}}
	// Within searchRange of the first predictor; tried marks the
	// positions visited.
	type pos struct{ x, y int }
	origin := pos{int(ms.preds[0].x) >> 2, int(ms.preds[0].y) >> 2}
	// The reference's rows the search reads, final (it may still be
	// coding): around the predictor, which can be far from the block.
	ref.waitLines(y0+origin.y-searchRange, y0+origin.y+searchRange+h-1)
	best := origin
	bestCost := math.Inf(1)
	const span = 2*searchRange + 1
	var tried [span*span/64 + 1]uint64
	try := func(p pos) bool {
		dx, dy := p.x-origin.x+searchRange, p.y-origin.y+searchRange
		if dx < 0 || dy < 0 || dx >= span || dy >= span {
			return false
		}
		bit := dy*span + dx
		if tried[bit>>6]&(1<<(bit&63)) != 0 {
			return false
		}
		tried[bit>>6] |= 1 << (bit & 63)
		bits, _ := c.mvCost(&ms, mv{int16(p.x << 2), int16(p.y << 2)}) //nolint:gosec // within the picture
		cost := float64(c.sadRef(ref, x0, y0, w, h, p.x, p.y)) + bits
		if cost < bestCost {
			best, bestCost = p, cost
			return true
		}
		return false
	}
	try(best)
	try(pos{int(ms.preds[1].x) >> 2, int(ms.preds[1].y) >> 2})
	try(pos{0, 0})
	if l == 0 && refIdx == c.ilr {
		// The other view: the disparity is horizontal, and can be far
		// from the predictors.
		for x := -searchRange; x <= searchRange; x += 4 {
			try(pos{origin.x + x, origin.y})
		}
	}
	// A diamond search from the best start, narrowing.
	for step := 4; step >= 1; step >>= 1 {
		for {
			center := best
			improved := false
			for _, d := range [4]pos{{step, 0}, {-step, 0}, {0, step}, {0, -step}} {
				if try(pos{center.x + d.x, center.y + d.y}) {
					improved = true
				}
			}
			if !improved {
				break
			}
		}
	}
	ms.best = mv{int16(best.x << 2), int16(best.y << 2)} //nolint:gosec // within the picture
	ms.cost = bestCost
	return ms
}

// subSearch refines a search to half and quarter samples, by the SATD
// of the exact prediction, returning the vector, its cost and its
// predictor's flag.
func (c *picCoder) subSearch(ms *mvSearch) (mv, float64, int) {
	x0, y0, w, h, l, refIdx := ms.x0, ms.y0, ms.w, ms.h, ms.l, ms.refIdx
	src := c.src.Y[y0*c.src.StrideY+x0:]
	pred := c.pred[:w*h]
	at := func(v mv) float64 {
		bits, _ := c.mvCost(ms, v)
		f := mvField{refIdx: [2]int8{-1, -1}, pred: 1 << l}
		f.mv[l] = v
		f.refIdx[l] = int8(refIdx) //nolint:gosec // a small index
		c.predictLuma(pred, x0, y0, w, h, &f)
		return float64(satd(src, c.src.StrideY, pred, w, w, h)) + bits
	}
	bestMV := ms.best
	bestCost := at(bestMV)
	for _, step := range [2]int16{2, 1} {
		center := bestMV
		for _, d := range [8][2]int16{{step, 0}, {-step, 0}, {0, step}, {0, -step}, {step, step}, {-step, step}, {step, -step}, {-step, -step}} {
			v := mv{center.x + d[0], center.y + d[1]}
			if cost := at(v); cost < bestCost {
				bestMV, bestCost = v, cost
			}
		}
	}
	_, flag := c.mvCost(ms, bestMV)
	return bestMV, bestCost, flag
}

// puRect is the prediction unit partIdx of a CU at (x0, y0) of size with
// partition mode part.
func puRect(x0, y0, size, part, partIdx int) (x, y, w, h int) {
	switch part {
	case part2NxN:
		return x0, y0 + partIdx*size/2, size, size / 2
	case partNx2N:
		return x0 + partIdx*size/2, y0, size / 2, size
	}
	return x0, y0, size, size
}

// interCandidates finds the inter predictions worth a rate-distortion
// trial for the prediction unit (x0, y0, w, h), partIdx of the CU the
// slice state describes: the best merge candidate, the best
// motion-searched one of either list and, in a B slice, both lists'
// together.
// mergeBest is the prediction unit's best merge candidate by its SATD
// cost.
func (c *picCoder) mergeBest(x0, y0, w, h, partIdx int) interCand {
	sd, hd := &c.sd, &c.h
	src := c.src.Y[y0*c.src.StrideY+x0:]
	pred := c.pred[:w*h]
	merge := interCand{cost: math.Inf(1)}
	for idx := range hd.maxMergeCand {
		f := sd.mergeCandidate(x0, y0, w, h, partIdx, idx)
		c.predictLuma(pred, x0, y0, w, h, &f)
		cost := float64(satd(src, c.src.StrideY, pred, w, w, h)) + c.lambdaS*(mergeBits(idx, hd.maxMergeCand)+1)
		if cost < merge.cost {
			merge = interCand{f: f, merge: true, mergeIdx: idx, cost: cost}
		}
	}
	return merge
}

func (c *picCoder) interCandidates(x0, y0, w, h, partIdx int) (merge, amvp, bi interCand) {
	merge = c.mergeBest(x0, y0, w, h, partIdx)
	return merge, c.searchCandidates(x0, y0, w, h, partIdx, &bi), bi
}

// searchCandidates finds the best motion-searched candidate of either
// list, and in a B slice the two lists' together (bi).
func (c *picCoder) searchCandidates(x0, y0, w, h, partIdx int, bi *interCand) (amvp interCand) {
	sd, hd := &c.sd, &c.h
	src := c.src.Y[y0*c.src.StrideY+x0:]
	pred := c.pred[:w*h]
	amvp.cost = math.Inf(1)
	var bestList [2]interCand
	for l := range 2 {
		bestList[l].cost = math.Inf(1)
		if hd.typ != sliceB && l == 1 {
			break
		}
		// Every reference for the large blocks, the nearest for 16x16 and
		// below, at most two in B pictures: measured smaller (the far
		// references' vectors cost more than they save).
		nref := hd.numRefIdx[l]
		if w <= 16 && h <= 16 {
			nref = 1
		}
		if hd.typ == sliceB {
			nref = min(nref, 2)
		}
		var rbuf [16]int
		rs := rbuf[:0]
		for r := range nref {
			rs = append(rs, r)
		}
		// The other view's picture, always: it is the same instant.
		if l == 0 && c.ilr >= nref {
			rs = append(rs, c.ilr)
		}
		// Several references: their whole-sample searches, then the
		// sub-sample one for the best alone.
		subOnly := -1
		if len(rs) > 1 {
			bestInt := math.Inf(1)
			for _, r := range rs {
				ms := c.intSearch(x0, y0, w, h, partIdx, l, r)
				if cost := ms.cost + c.lambdaS*refBits(r, hd.numRefIdx[l]); cost < bestInt {
					bestInt, subOnly = cost, r
				}
			}
		}
		for _, r := range rs {
			if subOnly >= 0 && r != subOnly {
				continue
			}
			v, cost, flag := c.motionSearch(x0, y0, w, h, partIdx, l, r)
			cost += c.lambdaS * (refBits(r, hd.numRefIdx[l]) + 1)
			if cost < bestList[l].cost {
				var f mvField
				f.refIdx = [2]int8{-1, -1}
				f.refIdx[l] = int8(r) //nolint:gosec // a small index
				f.mv[l] = v
				f.pred = 1 << l
				p := sd.amvp(x0, y0, w, h, partIdx, l, r, flag)
				bestList[l] = interCand{f: f, cost: cost}
				bestList[l].mvd[l] = mv{v.x - p.x, v.y - p.y}
				bestList[l].mvpFlag[l] = flag
			}
		}
		if bestList[l].cost < amvp.cost {
			amvp = bestList[l]
		}
	}
	bi.cost = math.Inf(1)
	if hd.typ == sliceB && bestList[0].cost < math.Inf(1) && bestList[1].cost < math.Inf(1) {
		*bi = interCand{f: mvField{
			mv:     [2]mv{bestList[0].f.mv[0], bestList[1].f.mv[1]},
			refIdx: [2]int8{bestList[0].f.refIdx[0], bestList[1].f.refIdx[1]},
			pred:   3,
		}}
		bi.mvd = [2]mv{bestList[0].mvd[0], bestList[1].mvd[1]}
		bi.mvpFlag = [2]int{bestList[0].mvpFlag[0], bestList[1].mvpFlag[1]}
		c.predictLuma(pred, x0, y0, w, h, &bi.f)
		bits := mvdBits(bi.mvd[0]) + mvdBits(bi.mvd[1]) + refBits(int(bi.f.refIdx[0]), hd.numRefIdx[0]) +
			refBits(int(bi.f.refIdx[1]), hd.numRefIdx[1]) + 3
		bi.cost = float64(satd(src, c.src.StrideY, pred, w, w, h)) + c.lambdaS*bits
	}
	return amvp
}

// bestCandidate is the prediction unit's best candidate by its cost.
func bestCandidate(cands ...interCand) interCand {
	best := cands[0]
	for _, c := range cands[1:] {
		if c.cost < best.cost {
			best = c
		}
	}
	return best
}

// rdInterCU decides the inter CU (2Nx2N): skip, merge with a residual,
// or the motion-searched prediction with one, reconstructing the best
// in the picture; k follows the bits. It returns the cost, or +Inf when
// no inter prediction is possible.
func (c *picCoder) rdInterCU(node *cuNode, k *cabacCost) float64 {
	sd, s := &c.sd, c.s
	x0, y0, log2 := node.x, node.y, node.log2
	size := 1 << log2
	sd.cuX, sd.cuY, sd.cuLog2 = x0, y0, log2
	sd.predMode = modeInter
	sd.partMode = part2Nx2N
	merge := c.mergeBest(x0, y0, size, size, 0)
	{
		// Early skip: the best merge candidate as skip, taken when its
		// error is small for the QP (below twice lambda per 8x8 block:
		// 0.2% larger output for 14% less work, measured).
		n := cuNode{x: x0, y: y0, log2: log2, inter: true, cand: merge, skip: true}
		sd.motionCompensate(x0, y0, size, size, &merge.f)
		c.setInterModes(x0, y0, size, size, modeSkip, &merge.f)
		ssd := c.ssdCU(x0, y0, size)
		if ssd < 2*c.lambda*float64(size*size)/64 {
			var kk cabacCost
			kk.ctx = k.ctx
			c.codeCU(&kk, &n)
			*node = n
			k.bits += kk.bits
			k.ctx = kk.ctx
			return ssd + c.lambda*kk.bits
		}
	}
	var bi interCand
	amvp := c.searchCandidates(x0, y0, size, size, 0, &bi)
	type trial struct {
		part  int
		cand  interCand
		cand1 interCand
		skip  bool
	}
	// Skip first, then a residual on the best candidate by its SATD cost.
	trials := []trial{{part2Nx2N, merge, interCand{}, true}}
	cands := []interCand{merge, amvp}
	if !math.IsInf(bi.cost, 1) {
		cands = append(cands, bi)
	}
	slices.SortFunc(cands, func(a, b interCand) int { return cmp.Compare(a.cost, b.cost) })
	// The best by SATD alone gets a residual trial: a second cost 1.1%
	// in size for 15% more time, measured.
	for _, cand := range cands[:1] {
		trials = append(trials, trial{part2Nx2N, cand, interCand{}, false})
	}
	if log2 > s.log2MinCb && c.e.o.Rect {
		trials = append(trials, trial{part: part2NxN}, trial{part: partNx2N})
	}
	bestCost := math.Inf(1)
	var bestCtx [numContexts]uint8
	var best cuNode
	bestBits := 0.0
	for i, t := range trials {
		n := cuNode{x: x0, y: y0, log2: log2, inter: true, part: t.part, cand: t.cand, cand1: t.cand1, skip: t.skip}
		sd.partMode = t.part
		if t.part != part2Nx2N {
			// Each prediction unit's best candidate by its cost, the
			// second's merge list seeing the first's motion.
			for i := range 2 {
				px, py, pw, ph := puRect(x0, y0, size, t.part, i)
				m, a, b := c.interCandidates(px, py, pw, ph, i)
				cand := bestCandidate(m, a, b)
				if i == 0 {
					n.cand = cand
				} else {
					n.cand1 = cand
				}
				sd.motionCompensate(px, py, pw, ph, &cand.f)
				c.setInterModes(px, py, pw, ph, modeInter, &cand.f)
			}
			c.keepPred(x0, y0, size)
		} else {
			// The prediction, whole, into the picture and the scratch the
			// residual coding reads it from.
			sd.motionCompensate(x0, y0, size, size, &t.cand.f)
			c.keepPred(x0, y0, size)
			mode := modeInter
			if t.skip {
				mode = modeSkip
			}
			c.setInterModes(x0, y0, size, size, mode, &t.cand.f)
		}
		if !t.skip {
			ctx := k.ctx
			n.tu = c.rdInterTree(x0, y0, log2, 0, &n, &ctx)
			n.rootCbf = c.treeHasCbf(n.tu)
			if !n.rootCbf {
				if t.part == part2Nx2N && t.cand.merge {
					// Merge without a residual is the skip trial, done.
					c.restore(&c.saved[slotInter])
					continue
				}
				n.tu = nil
			}
		}
		var kk cabacCost
		kk.ctx = k.ctx
		c.codeCU(&kk, &n)
		cost := c.ssdCU(x0, y0, size) + c.lambda*kk.bits
		if cost < bestCost {
			bestCost, bestCtx, best, bestBits = cost, kk.ctx, n, kk.bits
			if i < len(trials)-1 {
				c.save(x0, y0, log2, slotInter)
			}
		} else {
			c.restore(&c.saved[slotInter])
		}
	}
	*node = best
	k.bits += bestBits
	k.ctx = bestCtx
	return bestCost
}

// keepPred copies the CU's prediction from the picture into the
// prediction scratch, for the residual trials to start from.
func (c *picCoder) keepPred(x0, y0, size int) {
	pic := c.pic
	for y := range size {
		copy(c.predCU[y*size:y*size+size], pic.y[(y0+y)*pic.strideY+x0:])
	}
	for y := range size / 2 {
		copy(c.predCUC[0][y*size/2:y*size/2+size/2], pic.cb[(y0/2+y)*pic.strideC+x0/2:])
		copy(c.predCUC[1][y*size/2:y*size/2+size/2], pic.cr[(y0/2+y)*pic.strideC+x0/2:])
	}
}

// setInterModes records a prediction unit's mode and motion in the block
// state and the picture's motion field.
func (c *picCoder) setInterModes(x0, y0, w, h, mode int, f *mvField) {
	s, ps, pic := c.s, c.ps, c.pic
	for y := y0; y < y0+h && y < s.height; y += 4 {
		for x := x0; x < x0+w && x < s.width; x += 4 {
			ps.predMode[c.sd.blk4(x, y)] = uint8(mode) //nolint:gosec // a small constant
			pic.mvf[(y>>2)*pic.mvfW+x>>2] = *f
		}
	}
}

// treeHasCbf reports whether any unit of the tree has levels.
func (c *picCoder) treeHasCbf(n *tuNode) bool {
	if n == nil {
		return false
	}
	if n.split {
		if n.cbfC[0] || n.cbfC[1] {
			return true
		}
		for _, ch := range n.children {
			if c.treeHasCbf(ch) {
				return true
			}
		}
		return false
	}
	return n.cbfLuma || n.cbfC[0] || n.cbfC[1]
}

// rdInterTree decides the inter CU's transform tree at (x, y): one unit,
// or split, whichever costs less, the residual against the kept
// prediction.
func (c *picCoder) rdInterTree(x, y, log2, depth int, node *cuNode, ctx *[numContexts]uint8) *tuNode {
	s := c.s
	canSplit := log2 <= s.log2MaxTb && log2 > s.log2MinTb && depth < s.maxTrDepthInter
	forced := log2 > s.log2MaxTb
	if !canSplit && !forced {
		return c.codeInterLeaf(node, x, y, log2, depth, log2 > 2)
	}
	size := 1 << log2
	slot := slotTU + log2 - s.log2MinTb
	var one *tuNode
	costOne := math.Inf(1)
	var ctxOne [numContexts]uint8
	if !forced {
		one = c.codeInterLeaf(node, x, y, log2, depth, true)
		var k cabacCost
		k.ctx = *ctx
		c.codeTree(&k, one, nil, node, 0)
		costOne = c.ssdCU(x, y, size) + c.lambda*k.bits
		ctxOne = k.ctx
		if !one.cbfLuma && !one.cbfC[0] && !one.cbfC[1] {
			*ctx = ctxOne // nothing to split
			return one
		}
		c.save(x, y, log2, slot)
	}
	split := &tuNode{x: x, y: y, log2: log2, depth: depth, split: true}
	half := size >> 1
	for i := range 4 {
		cx, cy := x+(i&1)*half, y+(i>>1)*half
		if log2-1 <= s.log2MinTb || depth+1 >= s.maxTrDepthInter && log2-1 <= s.log2MaxTb {
			split.children[i] = c.codeInterLeaf(node, cx, cy, log2-1, depth+1, log2-1 > 2)
		} else {
			split.children[i] = c.rdInterTree(cx, cy, log2-1, depth+1, node, ctx)
		}
	}
	if log2 == 3 {
		for ci := 1; ci <= 2; ci++ {
			split.cbfC[ci-1], split.chroma[ci-1] = c.codeBlockPred(x/2, y/2, 2, ci, node)
		}
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

// codeInterLeaf codes the residual of the inter transform unit at
// (x, y) against the kept prediction, luma and, when it has them,
// chroma.
func (c *picCoder) codeInterLeaf(node *cuNode, x, y, log2, depth int, chroma bool) *tuNode {
	n := &tuNode{x: x, y: y, log2: log2, depth: depth}
	n.cbfLuma, n.luma = c.codeBlockPred(x, y, log2, 0, node)
	if chroma && log2 > 2 {
		for ci := 1; ci <= 2; ci++ {
			n.cbfC[ci-1], n.chroma[ci-1] = c.codeBlockPred(x/2, y/2, log2-1, ci, node)
		}
	}
	return n
}

// codeBlockPred codes the residual of the block of component ci at
// (x, y) (its samples) against the CU's kept prediction, reconstructing
// it in the picture.
func (c *picCoder) codeBlockPred(x, y, log2, ci int, node *cuNode) (bool, []int32) {
	s, sd := c.s, &c.sd
	n := 1 << log2
	cuSize := 1 << node.log2
	pred, pstride := c.predCU[:], cuSize
	px, py := x-node.x, y-node.y
	pl, stride := c.pic.y, c.pic.strideY
	src, sstride := c.src.Y, c.src.StrideY
	depth := s.bitDepth
	if ci > 0 {
		pred, pstride = c.predCUC[ci-1][:], cuSize/2
		px, py = x-node.x/2, y-node.y/2
		pl, stride = c.pic.cb, c.pic.strideC
		src, sstride = c.src.Cb, c.src.StrideC
		if ci == 2 {
			pl, src = c.pic.cr, c.src.Cr
		}
		depth = s.bitDepthC
	}
	res, res16 := c.res[:n*n], c.res16[:n*n]
	for j := range n {
		p := pred[(py+j)*pstride+px:]
		o := src[(y+j)*sstride+x:]
		for i := range n {
			d := int32(o[i]) - int32(p[i])
			res[j*n+i], res16[j*n+i] = d, int16(d) //nolint:gosec // a residual of at most 10 bits
		}
		copy(pl[(y+j)*stride+x:(y+j)*stride+x+n], p[:n]) // the prediction back in place
	}
	coef := c.transform(log2, depth, false)
	qp := c.qp + s.qpBdOffset
	if ci > 0 {
		qpi := min(max(c.qp, -s.qpBdOffsetC), 57)
		qp = chromaQP(qpi) + s.qpBdOffsetC
	}
	q := newQuantizer(qp, log2, depth)
	levels := c.quantize(&q, coef, log2, ci, 0)
	if levels == nil {
		return false, nil
	}
	copy(sd.coeffs[:n*n], levels)
	sd.predMode = modeInter
	sd.reconstruct(x, y, log2, ci, false)
	return true, levels
}

// codeInterCU writes an inter CU's prediction syntax (7.3.8.5, 7.3.8.6)
// to bc: the skip flag was coded by the caller.
func (c *picCoder) codeInterCU(bc binCoder, node *cuNode) {
	if node.skip {
		c.codeMergeIdx(bc, node.cand.mergeIdx)
		return
	}
	bc.decision(ctxPredModeFlag, 0) // inter
	// part_mode (9.3.4.2.1, AMP off): 1 for 2Nx2N, 01 for 2NxN, 00 for
	// Nx2N; at the minimum size only 2Nx2N is coded here.
	bc.decision(ctxPartMode, boolInt(node.part == part2Nx2N))
	if node.part != part2Nx2N {
		bc.decision(ctxPartMode+1, boolInt(node.part == part2NxN))
	}
	size := 1 << node.log2
	n := 1
	if node.part != part2Nx2N {
		n = 2
	}
	for i := range n {
		cand := &node.cand
		if i == 1 {
			cand = &node.cand1
		}
		_, _, w, h := puRect(node.x, node.y, size, node.part, i)
		c.codePU(bc, node, cand, w, h)
	}
	if node.part != part2Nx2N || !node.cand.merge {
		bc.decision(ctxNoResidualDataFlag, boolInt(node.rootCbf))
	}
	if node.rootCbf {
		c.codeTree(bc, node.tu, nil, node, 0)
	}
}

// codePU writes a prediction unit's syntax (7.3.8.6).
func (c *picCoder) codePU(bc binCoder, node *cuNode, cand *interCand, w, h int) {
	hd := &c.h
	bc.decision(ctxMergeFlag, boolInt(cand.merge))
	if cand.merge {
		c.codeMergeIdx(bc, cand.mergeIdx)
		return
	}
	f := &cand.f
	if hd.typ == sliceB {
		if w+h == 12 {
			bc.decision(ctxInterPredIdc+4, boolInt(f.pred == 2))
		} else {
			bc.decision(ctxInterPredIdc+c.sd.ctDepthAt(node.x, node.y), boolInt(f.pred == 3))
			if f.pred != 3 {
				bc.decision(ctxInterPredIdc+4, boolInt(f.pred == 2))
			}
		}
	}
	for l := range 2 {
		if f.pred&(1<<l) == 0 {
			continue
		}
		c.codeRefIdx(bc, l, int(f.refIdx[l]))
		if l != 1 || !hd.mvdL1Zero || f.pred != 3 {
			codeMvd(bc, cand.mvd[l])
		}
		bc.decision(ctxMvpLxFlag, cand.mvpFlag[l])
	}
}

func (c *picCoder) codeMergeIdx(bc binCoder, idx int) {
	n := c.h.maxMergeCand
	if n <= 1 {
		return
	}
	bc.decision(ctxMergeIdx, boolInt(idx > 0))
	if idx == 0 {
		return
	}
	for j := 1; j < n-1; j++ {
		bin := idx > j
		bc.bypass(boolInt(bin))
		if !bin {
			break
		}
	}
}

func (c *picCoder) codeRefIdx(bc binCoder, l, r int) {
	n := c.h.numRefIdx[l]
	if n <= 1 {
		return
	}
	// Both lists' indexes share the contexts (9.3.4.2.2: one ctxTable for
	// ref_idx_l0 and ref_idx_l1).
	ctx := ctxRefIdxL0
	maxCtx := min(n-1, 2)
	i := 0
	for i < maxCtx {
		bin := r > i
		bc.decision(ctx+i, boolInt(bin))
		if !bin {
			return
		}
		i++
	}
	for i < n-1 {
		bin := r > i
		bc.bypass(boolInt(bin))
		if !bin {
			return
		}
		i++
	}
}

// codeMvd writes mvd_coding (7.3.8.9).
func codeMvd(bc binCoder, d mv) {
	ax, ay := iabs(int(d.x)), iabs(int(d.y))
	bc.decision(ctxAbsMvdGreater0Flag, boolInt(ax > 0))
	bc.decision(ctxAbsMvdGreater0Flag, boolInt(ay > 0))
	if ax > 0 {
		bc.decision(ctxAbsMvdGreater1Flag+1, boolInt(ax > 1))
	}
	if ay > 0 {
		bc.decision(ctxAbsMvdGreater1Flag+1, boolInt(ay > 1))
	}
	comp := func(v int16, a int) {
		if a == 0 {
			return
		}
		if a > 1 {
			// abs_mvd_minus2: first order Exp-Golomb.
			val := a - 2
			k := 1
			for val >= 1<<k {
				bc.bypass(1)
				val -= 1 << k
				k++
			}
			bc.bypass(0)
			bc.bypassBits(uint32(val), k) //nolint:gosec // positive
		}
		bc.bypass(boolInt(v < 0))
	}
	comp(d.x, ax)
	comp(d.y, ay)
}
