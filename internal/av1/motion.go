package av1

// Motion search: where in a reference a block of the source is best
// predicted from. An integer search (the best of a few start points,
// then a hexagon walk and a square refinement, by SAD on luma) and a
// subpel refinement (half, quarter and, with allow_high_precision_mv,
// eighth sample steps, each candidate predicted exactly and measured by
// SATD), both adding the motion vector's rate as the caller prices it.

import "math"

// meBlock is the block a motion search predicts.
type meBlock struct {
	ref        *refFrameState
	src        []uint16 // the source's luma
	srcStride  int
	x, y, w, h int // the block, in luma samples (w, h multiples of 4)
	bitDepth   int
	filt       [2]int // InterpFilter, as for predictInter
	allowHP    bool   // allow_high_precision_mv: eighth sample vectors
	// blk, when set, is the block to match instead of src's at (x, y)
	// (with srcStride)
	blk []uint16
	// searchRange is how far, in samples, the integer search goes from
	// its best start point (0 is 64).
	searchRange int
	// cost is a motion vector's rate in the distortion's units (SAD and
	// SATD alike, so sqrt(lambda) times its bits).
	cost func(m mv) float64
}

// srcBlock is the samples the search matches, from the block's top left.
func (b *meBlock) srcBlock() []uint16 {
	if b.blk != nil {
		return b.blk
	}
	return b.src[b.y*b.srcStride+b.x:]
}

// mvLimit is the largest motion vector component magnitude
// (is_mv_valid: below 1 << 14).
const mvLimit = 1<<14 - 1

// motionSearch is a motion search's working memory, reused call to call.
type motionSearch struct {
	s    interScratch
	pred [128 * 128]uint16
	hmid [3][]int32 // subPelStep's horizontal passes
	// hmid16 are subPelStep's horizontal passes as words (with AVX2)
	hmid16 [3][]int16
	seen   [1024]seenPoint
	gen    uint32
}

// reset makes ms as new, keeping its buffers' room.
func (ms *motionSearch) reset() {
	hmid, hmid16, comp := ms.hmid, ms.hmid16, ms.s.comp
	*ms = motionSearch{}
	ms.hmid, ms.hmid16, ms.s.comp = hmid, hmid16, comp
}

// seenPoint is an integer position the search has measured: the cache of
// the current generation's.
type seenPoint struct {
	gen    uint32
	px, py int32
	cost   float64
}

// search is the whole search: the best motion vector for b from starts
// (the reference's candidates, or none for zero) and its cost.
func (ms *motionSearch) search(b *meBlock, starts []mv) (mv, float64) {
	m, _ := ms.fullPel(b, starts)
	return ms.subPel(b, m)
}

// fullPel is the integer search: the best of the start points (rounded
// to whole samples), then a hexagon walk from it to where none of its
// points is better, then its eight neighbours. It gives the vector (in
// 1/8 samples, whole) and its SAD and rate.
func (ms *motionSearch) fullPel(b *meBlock, starts []mv) (mv, float64) {
	ms.gen++
	if ms.gen == 0 {
		ms.seen = [len(ms.seen)]seenPoint{}
		ms.gen = 1
	}
	rng := b.searchRange
	if rng <= 0 {
		rng = 64
	}
	// The integer positions searched: the block at most 8 samples
	// wholly outside the reference (beyond, all is its edge's copies),
	// within is_mv_valid's range.
	loX := max(-b.w-8, b.x-mvLimit/8)
	hiX := min(b.ref.width+8, b.x+mvLimit/8)
	loY := max(-b.h-8, b.y-mvLimit/8)
	hiY := min(b.ref.height+8, b.y+mvLimit/8)

	bestX, bestY, best := b.x, b.y, -1.0
	try := func(px, py int) bool {
		px, py = min(max(px, loX), hiX), min(max(py, loY), hiY)
		if c := ms.fullCost(b, px, py); best < 0 || c < best {
			bestX, bestY, best = px, py, c
			return true
		}
		return false
	}
	if len(starts) == 0 {
		try(b.x, b.y)
	}
	for _, s := range starts {
		try(b.x+int((s.col+4)>>3), b.y+int((s.row+4)>>3))
	}
	cx, cy := bestX, bestY
	loX, hiX = max(loX, cx-rng), min(hiX, cx+rng)
	loY, hiY = max(loY, cy-rng), min(hiY, cy+rng)

	hex := [6][2]int{{-2, 0}, {-1, -2}, {1, -2}, {2, 0}, {1, 2}, {-1, 2}}
	for range 4 * rng {
		ox, oy := bestX, bestY
		for _, d := range hex {
			try(ox+d[0], oy+d[1])
		}
		if bestX == ox && bestY == oy {
			break
		}
	}
	for range rng {
		ox, oy := bestX, bestY
		for _, d := range [4][2]int{{0, -1}, {-1, 0}, {1, 0}, {0, 1}} {
			try(ox+d[0], oy+d[1])
		}
		if bestX == ox && bestY == oy {
			break
		}
	}
	ox, oy := bestX, bestY
	for _, d := range [4][2]int{{-1, -1}, {1, -1}, {-1, 1}, {1, 1}} {
		try(ox+d[0], oy+d[1])
	}
	return mv{row: int32((bestY - b.y) * 8), col: int32((bestX - b.x) * 8)}, best //nolint:gosec // within mvLimit
}

// fullCost is the SAD and rate of the block at integer position (px, py)
// of the reference, cached.
func (ms *motionSearch) fullCost(b *meBlock, px, py int) float64 {
	e := &ms.seen[uint(px*31+py*977)&uint(len(ms.seen)-1)]
	if e.gen == ms.gen && int(e.px) == px && int(e.py) == py {
		return e.cost
	}
	m := mv{row: int32((py - b.y) * 8), col: int32((px - b.x) * 8)} //nolint:gosec // within mvLimit
	c := float64(ms.sadAt(b, px, py)) + b.cost(m)
	*e = seenPoint{gen: ms.gen, px: int32(px), py: int32(py), cost: c} //nolint:gosec // positions
	return c
}

// sadAt is the SAD of the block against the reference's block at integer
// position (px, py), whose samples outside the reference are its edges'
// (an integer vector's prediction is exactly those samples): such a block
// is made in ms.pred first.
func (ms *motionSearch) sadAt(b *meBlock, px, py int) int64 {
	ref, rs := b.ref.rec[0], b.ref.stride[0]
	lastX, lastY := b.ref.width-1, b.ref.height-1
	if px < 0 || py < 0 || px+b.w-1 > lastX || py+b.h-1 > lastY {
		// the columns inside the reference copied, those left and right
		// of it its first and last
		in0, in1 := min(max(-px, 0), b.w), min(max(lastX+1-px, 0), b.w)
		for i := range b.h {
			row := ref[min(max(py+i, 0), lastY)*rs:]
			e := ms.pred[i*b.w : i*b.w+b.w]
			for j := range in0 {
				e[j] = row[0]
			}
			if in1 > in0 {
				copy(e[in0:in1], row[px+in0:px+in1])
			}
			for j := max(in1, in0); j < b.w; j++ {
				e[j] = row[lastX]
			}
		}
		ref, rs = ms.pred[:], b.w
		px, py = 0, 0
	}
	if b.w&7 == 0 {
		return int64(sadBlock(b.srcBlock(), b.srcStride, ref[py*rs+px:], rs, b.w, b.h))
	}
	var sum int64
	for i := range b.h {
		s := b.srcBlock()[i*b.srcStride:][:b.w]
		r := ref[(py+i)*rs+px:][:b.w]
		var d int32
		for j, v := range s {
			d += abs32(int32(v) - int32(r[j]))
		}
		sum += int64(d)
	}
	return sum
}

// subPel refines an integer vector: the eight neighbours at half, then
// quarter, then (with allowHP) eighth sample steps, each predicted
// exactly and measured by SATD plus rate. It gives the best vector and
// its cost.
func (ms *motionSearch) subPel(b *meBlock, start mv) (mv, float64) {
	best, bestCost := start, ms.subCost(b, start)
	minStep := int32(2)
	if b.allowHP {
		minStep = 1
	}
	for step := int32(4); step >= minStep; step >>= 1 {
		c := best
		var costs [8]float64
		shared := ms.subPelStep(b, c, step, &costs)
		for i, d := range subPelDirs {
			m := mv{row: c.row + d[0]*step, col: c.col + d[1]*step}
			if m.row < -mvLimit || m.row > mvLimit || m.col < -mvLimit || m.col > mvLimit {
				continue
			}
			k := costs[i]
			if !shared {
				k = ms.subCost(b, m)
			}
			if k < bestCost {
				best, bestCost = m, k
			}
		}
	}
	return best, bestCost
}

// subPelDirs are the eight neighbours a sub-sample step tries, in order.
var subPelDirs = [8][2]int32{{-1, -1}, {-1, 0}, {-1, 1}, {0, -1}, {0, 1}, {1, -1}, {1, 0}, {1, 1}}

// subPelStep is subCost of c's eight neighbours step apart (costs, in
// subPelDirs' order), their predictions sharing the horizontal filter of
// each of their three columns: the same predictions as blockInterPred's.
// It reports false (costs unset) where it does not apply: a neighbour
// near the reference's edges, at a whole sample or out of mvLimit.
func (ms *motionSearch) subPelStep(b *meBlock, c mv, step int32, costs *[8]float64) bool {
	w, h := b.w, b.h
	lastX, lastY := b.ref.width-1, b.ref.height-1
	var sx, sy [8]int
	ymin, ymax := math.MaxInt, math.MinInt
	fx, fy := filterFor(b.filt[1], w), filterFor(b.filt[0], h)
	for i, d := range subPelDirs {
		m := mv{row: c.row + d[0]*step, col: c.col + d[1]*step}
		if m.row < -mvLimit || m.row > mvLimit || m.col < -mvLimit || m.col > mvLimit {
			return false
		}
		sx[i], sy[i], _, _ = scaleMv(0, b.x, b.y, m, b.ref.width, b.ref.height, b.ref.width, b.ref.height)
		fracX, fracY := (sx[i]>>6)&SUBPEL_MASK, (sy[i]>>6)&SUBPEL_MASK
		x0, y0 := (sx[i]>>10)-3, (sy[i]>>10)-3
		if fracX == 0 && fracY == 0 || x0 < 0 || y0 < 0 || x0+w+6 > lastX || y0+h+6 > lastY {
			return false
		}
		ty := subpelTaps[fy][fracY]
		ymin, ymax = min(ymin, y0+int(ty[0])), max(ymax, y0+h-1+int(ty[1]))
	}
	rnd := interRoundingFor(b.bitDepth, false)
	if useAVX2 && w&7 == 0 {
		ms.subPelStep16(b, c, step, &sx, &sy, costs)
		return true
	}
	sh0 := uint(rnd.round0)
	add0 := int32(1) << (sh0 - 1)
	src, ss := b.ref.rec[0], b.ref.stride[0]
	// the horizontal passes, by column (c's and either side's)
	var cols [3]int
	nc := 0
	col := func(x int) int {
		for k := range nc {
			if cols[k] == x {
				return k
			}
		}
		k := nc
		cols[k], nc = x, nc+1
		if n := w * (ymax - ymin); cap(ms.hmid[k]) < n {
			ms.hmid[k] = make([]int32, n)
		}
		mid := ms.hmid[k]
		fracX, x0 := (x>>6)&SUBPEL_MASK, (x>>10)-3
		kx := &subpelFilters[fx][fracX]
		tx := subpelTaps[fx][fracX]
		for y := ymin; y < ymax; y++ {
			row := src[y*ss+x0 : y*ss+x0+w+7]
			out := mid[(y-ymin)*w : (y-ymin)*w+w]
			if fracX == 0 {
				widen(out, row[3:], FILTER_BITS-sh0)
				continue
			}
			filterRow(out, row, kx, int(tx[0]), int(tx[1]), add0, sh0)
		}
		return k
	}
	sh1 := uint(rnd.round1)
	add1 := int32(1) << (sh1 - 1)
	maxV := int32(1)<<uint(b.bitDepth) - 1
	var acc [128]int32
	for i, d := range subPelDirs {
		mid := ms.hmid[col(sx[i])]
		fracY, y0 := (sy[i]>>6)&SUBPEL_MASK, (sy[i]>>10)-3
		ky := &subpelFilters[fy][fracY]
		ty := subpelTaps[fy][fracY]
		lo, hi := int(ty[0]), int(ty[1])
		for r := range h {
			filterColClip(ms.pred[r*w:r*w+w], mid[(y0+r+lo-ymin)*w:], w, ky, lo, hi, add1, sh1, maxV, acc[:w])
		}
		m := mv{row: c.row + d[0]*step, col: c.col + d[1]*step}
		costs[i] = float64(satdBlock(b.srcBlock(), b.srcStride, ms.pred[:], w, w, h)) + b.cost(m)
	}
	return true
}

// subPelStep16 is subPelStep's predictions with the block filters (w a
// multiple of 8): each column's horizontal pass over the rows any of its
// neighbours' 8 taps read.
func (ms *motionSearch) subPelStep16(b *meBlock, c mv, step int32, sx, sy *[8]int, costs *[8]float64) {
	w, h := b.w, b.h
	fx, fy := filterFor(b.filt[1], w), filterFor(b.filt[0], h)
	ymin, ymax := math.MaxInt, math.MinInt
	for i := range subPelDirs {
		y0 := (sy[i] >> 10) - 3
		ymin, ymax = min(ymin, y0), max(ymax, y0+h+7)
	}
	rnd := interRoundingFor(b.bitDepth, false)
	src, ss := b.ref.rec[0], b.ref.stride[0]
	var cols [3]int
	nc := 0
	col := func(x int) []int16 {
		for k := range nc {
			if cols[k] == x {
				return ms.hmid16[k]
			}
		}
		k := nc
		cols[k], nc = x, nc+1
		if n := w * (ymax - ymin); cap(ms.hmid16[k]) < n {
			ms.hmid16[k] = make([]int16, n)
		}
		fracX, x0 := (x>>6)&SUBPEL_MASK, (x>>10)-3
		filterH16(ms.hmid16[k], src, ymin*ss+x0, ss, w, ymax-ymin, &subpelFilters[fx][fracX], uint(rnd.round0))
		return ms.hmid16[k]
	}
	maxV := int32(1)<<uint(b.bitDepth) - 1
	for i, d := range subPelDirs {
		mid := col(sx[i])
		fracY, y0 := (sy[i]>>6)&SUBPEL_MASK, (sy[i]>>10)-3
		filterVClip16(ms.pred[:], w, mid[(y0-ymin)*w:], w, h, &subpelFilters[fy][fracY], uint(rnd.round1), maxV)
		m := mv{row: c.row + d[0]*step, col: c.col + d[1]*step}
		costs[i] = float64(satdBlock(b.srcBlock(), b.srcStride, ms.pred[:], w, w, h)) + b.cost(m)
	}
}

// subCost is the SATD and rate of the block's exact prediction by m.
func (ms *motionSearch) subCost(b *meBlock, m mv) float64 {
	predictInter(b.ref, 0, b.x, b.y, b.w, b.h, m, b.filt, b.bitDepth, ms.pred[:], b.w, &ms.s)
	return float64(satdBlock(b.srcBlock(), b.srcStride, ms.pred[:], b.w, b.w, b.h)) + b.cost(m)
}

// satdBlock is the sum of the 4x4 Hadamard transforms' magnitudes of the
// difference of two w by h blocks (w, h multiples of 4), halved.
func satdBlock(src []uint16, srcStride int, pred []uint16, predStride, w, h int) int64 {
	if w&7 == 0 && h&7 == 0 {
		return satd8s(src, srcStride, pred, predStride, w, h)
	}
	var sum int64
	var d [16]int32
	for by := 0; by < h; by += 4 {
		for bx := 0; bx < w; bx += 4 {
			for i := range 4 {
				s := src[(by+i)*srcStride+bx:][:4]
				p := pred[(by+i)*predStride+bx:][:4]
				d[i*4], d[i*4+1] = int32(s[0])-int32(p[0]), int32(s[1])-int32(p[1])
				d[i*4+2], d[i*4+3] = int32(s[2])-int32(p[2]), int32(s[3])-int32(p[3])
			}
			for i := range 4 {
				a0, a1, a2, a3 := d[i*4]+d[i*4+1], d[i*4]-d[i*4+1], d[i*4+2]+d[i*4+3], d[i*4+2]-d[i*4+3]
				d[i*4], d[i*4+1], d[i*4+2], d[i*4+3] = a0+a2, a1+a3, a0-a2, a1-a3
			}
			var s int32
			for j := range 4 {
				a0, a1, a2, a3 := d[j]+d[4+j], d[j]-d[4+j], d[8+j]+d[12+j], d[8+j]-d[12+j]
				s += abs32(a0+a2) + abs32(a1+a3) + abs32(a0-a2) + abs32(a1-a3)
			}
			sum += int64(s)
		}
	}
	return sum / 2
}
