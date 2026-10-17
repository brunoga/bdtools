package av1

// Motion search: where in a reference a block of the source is best
// predicted from. An integer search (the best of a few start points,
// then a hexagon walk and a square refinement, by SAD on luma) and a
// subpel refinement (half, quarter and, with allow_high_precision_mv,
// eighth sample steps, each candidate predicted exactly and measured by
// SATD), both adding the motion vector's rate as the caller prices it.

// meBlock is the block a motion search predicts.
type meBlock struct {
	ref        *refFrameState
	src        []uint16 // the source's luma
	srcStride  int
	x, y, w, h int // the block, in luma samples (w, h multiples of 4)
	bitDepth   int
	filt       [2]int // InterpFilter, as for predictInter
	allowHP    bool   // allow_high_precision_mv: eighth sample vectors
	// searchRange is how far, in samples, the integer search goes from
	// its best start point (0 is 64).
	searchRange int
	// cost is a motion vector's rate in the distortion's units (SAD and
	// SATD alike, so sqrt(lambda) times its bits).
	cost func(m mv) float64
}

// mvLimit is the largest motion vector component magnitude
// (is_mv_valid: below 1 << 14).
const mvLimit = 1<<14 - 1

// motionSearch is a motion search's working memory, reused call to call.
type motionSearch struct {
	s    interScratch
	pred [128 * 128]uint16
	seen [1024]seenPoint
	gen  uint32
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
	c := float64(sadAt(b, px, py)) + b.cost(m)
	*e = seenPoint{gen: ms.gen, px: int32(px), py: int32(py), cost: c} //nolint:gosec // positions
	return c
}

// sadAt is the SAD of the block against the reference's block at integer
// position (px, py), whose samples outside the reference are its edges'
// (an integer vector's prediction is exactly those samples).
func sadAt(b *meBlock, px, py int) int64 {
	ref, rs := b.ref.rec[0], b.ref.stride[0]
	lastX, lastY := b.ref.width-1, b.ref.height-1
	var sum int64
	if px >= 0 && py >= 0 && px+b.w-1 <= lastX && py+b.h-1 <= lastY {
		if b.w&7 == 0 {
			return int64(sadBlock(b.src[b.y*b.srcStride+b.x:], b.srcStride, ref[py*rs+px:], rs, b.w, b.h))
		}
		for i := range b.h {
			s := b.src[(b.y+i)*b.srcStride+b.x:][:b.w]
			r := ref[(py+i)*rs+px:][:b.w]
			var d int32
			for j, v := range s {
				d += abs32(int32(v) - int32(r[j]))
			}
			sum += int64(d)
		}
		return sum
	}
	for i := range b.h {
		s := b.src[(b.y+i)*b.srcStride+b.x:][:b.w]
		r := ref[min(max(py+i, 0), lastY)*rs:]
		var d int32
		for j, v := range s {
			d += abs32(int32(v) - int32(r[min(max(px+j, 0), lastX)]))
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
		for _, d := range [8][2]int32{{-1, -1}, {-1, 0}, {-1, 1}, {0, -1}, {0, 1}, {1, -1}, {1, 0}, {1, 1}} {
			m := mv{row: c.row + d[0]*step, col: c.col + d[1]*step}
			if m.row < -mvLimit || m.row > mvLimit || m.col < -mvLimit || m.col > mvLimit {
				continue
			}
			if k := ms.subCost(b, m); k < bestCost {
				best, bestCost = m, k
			}
		}
	}
	return best, bestCost
}

// subCost is the SATD and rate of the block's exact prediction by m.
func (ms *motionSearch) subCost(b *meBlock, m mv) float64 {
	predictInter(b.ref, 0, b.x, b.y, b.w, b.h, m, b.filt, b.bitDepth, ms.pred[:], b.w, &ms.s)
	return float64(satdBlock(b.src[b.y*b.srcStride+b.x:], b.srcStride, ms.pred[:], b.w, b.w, b.h)) + b.cost(m)
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
