package av1

// The loop filter (spec 7.14): every plane's transform block edges
// deblocked, the vertical edges of the whole plane first and then the
// horizontal ones. Each 4-sample edge segment gets a filter size from the
// transform sizes on its two sides (at most 16 samples for luma, 8 for
// chroma), a strength from its block's level (limit, blimit and thresh,
// with the sharpness), and then per sample line the masks pick no
// filter, the narrow 4-tap filter, or the 6/8-tap or 14-tap low pass
// filters, exactly as a decoder applies them to the reconstruction.
//
// The filter sizes guarantee that no two edges of a pass touch the same
// samples, so a pass is run on bands of superblock rows in parallel.
//
// The encoder picks the levels as libaom's picklpf does: a coarse to fine
// search of each level, each try filtering a copy of its plane, for the
// least squared error against the source over the visible picture.

import (
	"runtime"
	"sync"
	"sync/atomic"
)

// loopFilter is the frame's loop filter settings, its strengths by level.
type loopFilter struct {
	f     *frameEnc
	level [4]int // loop_filter_level
	sharp int    // loop_filter_sharpness
	// By level: limit, blimit and thresh scaled to the bit depth.
	limit, blimit, thresh [MAX_LOOP_FILTER + 1]int32
}

func newLoopFilter(f *frameEnc, level [4]int, sharp int) *loopFilter {
	lf := &loopFilter{f: f, level: level, sharp: sharp}
	shift := 0
	switch {
	case sharp > 4:
		shift = 2
	case sharp > 0:
		shift = 1
	}
	bd := uint(f.bitDepth - 8) //nolint:gosec // 0 or 2
	for lvl := range MAX_LOOP_FILTER + 1 {
		limit := lvl >> uint(shift)
		if sharp > 0 {
			limit = min(max(limit, 1), 9-sharp)
		} else {
			limit = max(1, limit)
		}
		lf.limit[lvl] = int32(limit) << bd            //nolint:gosec // small
		lf.blimit[lvl] = int32(2*(lvl+2)+limit) << bd //nolint:gosec // small
		lf.thresh[lvl] = int32(lvl>>4) << bd          //nolint:gosec // small
	}
	return lf
}

// lvl is the adaptive filter strength selection (spec 7.14.5) for the
// block at mi index mi, i being the level's index (pass for luma, plane+1
// for chroma). With no segmentation, delta_lf or loop_filter_delta_enabled
// it is the frame's level; those (and the block's reference frame and
// mode type) would apply here.
func (lf *loopFilter) lvl(mi, i int) int {
	_ = mi
	return lf.level[i]
}

// filterPlane applies the loop filter to plane p of buf (laid out as
// f.rec[p]).
func (lf *loopFilter) filterPlane(p int, buf []uint16) {
	f := lf.f
	if p > 0 && lf.level[1+p] == 0 || lf.level[0] == 0 && lf.level[1] == 0 {
		return
	}
	sub := min(p, 1)
	bandH := 16 >> uint(sub) // a superblock row, in 4x4s of the plane
	h4 := f.miRows >> uint(sub)
	bands := (h4 + bandH - 1) / bandH
	workers := min(runtime.GOMAXPROCS(0), bands)
	for pass := range 2 {
		if workers <= 1 {
			lf.edges(p, pass, 0, h4, buf)
			continue
		}
		var next atomic.Int32
		var wg sync.WaitGroup
		for range workers {
			wg.Go(func() {
				for {
					b := int(next.Add(1)) - 1
					if b >= bands {
						return
					}
					lf.edges(p, pass, b*bandH, min((b+1)*bandH, h4), buf)
				}
			})
		}
		wg.Wait()
	}
}

// edges runs the edge loop filter process (spec 7.14.2) over the rows of
// 4x4 blocks y4a..y4b-1 of plane p, for one pass (0 vertical edges, 1
// horizontal).
func (lf *loopFilter) edges(p, pass, y4a, y4b int, buf []uint16) {
	f := lf.f
	sub := uint(min(p, 1)) //nolint:gosec // 0 or 1
	stride := f.stride[p]
	lw := f.miCols >> sub
	// the 4x4s whose luma position is on screen
	w4 := min(lw, (f.width+(4<<sub)-1)/(4<<sub))
	h4 := min(y4b, (f.height+(4<<sub)-1)/(4<<sub))
	lfTx := f.lfTx[p]
	dx, dy, step := 1, 0, 1
	if pass == 1 {
		dx, dy, step = 0, 1, stride
	}
	li := pass
	if p > 0 {
		li = p + 1
	}
	maxSize := 16
	if p > 0 {
		maxSize = 8
	}
	for y4 := y4a; y4 < h4; y4++ {
		if pass == 1 && y4 == 0 {
			continue
		}
		row := y4<<sub | int(sub)
		for x4 := 0; x4 < w4; x4++ {
			if pass == 0 && x4 == 0 {
				continue
			}
			col := x4<<sub | int(sub)
			xP, yP := x4*4, y4*4
			mi := f.mi(row, col)
			txSz := lfTx[y4*lw+x4]
			prevTxSz := lfTx[(y4-dy)*lw+x4-dx]
			planeSize := subsampledSize[f.miSize[mi]][sub][sub]
			var isBlockEdge, isTxEdge bool
			var baseSize int
			if pass == 0 {
				isBlockEdge = xP%int(blockWidth[planeSize]) == 0
				isTxEdge = xP%int(txWidth[txSz]) == 0
				baseSize = int(min(txWidth[prevTxSz], txWidth[txSz]))
			} else {
				isBlockEdge = yP%int(blockHeight[planeSize]) == 0
				isTxEdge = yP%int(txHeight[txSz]) == 0
				baseSize = int(min(txHeight[prevTxSz], txHeight[txSz]))
			}
			if !isTxEdge {
				continue
			}
			isIntra := f.refFrames[0][mi] <= INTRA_FRAME
			if !isBlockEdge && f.skip[mi] != 0 && !isIntra {
				continue
			}
			lvl := lf.lvl(mi, li)
			if lvl == 0 {
				lvl = lf.lvl(f.mi(row-(dy<<sub), col-(dx<<sub)), li)
				if lvl == 0 {
					continue
				}
			}
			size := min(maxSize, baseSize)
			pos := yP*stride + xP
			across := stride // along the edge
			if pass == 1 {
				across = 1
			}
			for i := range 4 {
				lf.sample(buf, pos+i*across, step, p, size, lf.limit[lvl], lf.blimit[lvl], lf.thresh[lvl])
			}
		}
	}
}

// sample is the sample filtering process (spec 7.14.6) across the edge
// before buf[pos], step apart, the limits already scaled to the bit
// depth.
func (lf *loopFilter) sample(buf []uint16, pos, step, plane, size int, limit, blimit, thresh int32) {
	bd := uint(lf.f.bitDepth - 8) //nolint:gosec // 0 or 2
	q0, q1 := int32(buf[pos]), int32(buf[pos+step])
	p0, p1 := int32(buf[pos-step]), int32(buf[pos-2*step])
	// the filter mask
	filterLen := 16
	switch {
	case size == 4:
		filterLen = 4
	case plane != 0:
		filterLen = 6
	case size == 8:
		filterLen = 8
	}
	ap, aq := abs32(p1-p0), abs32(q1-q0)
	if ap > limit || aq > limit || abs32(p0-q0)*2+abs32(p1-q1)/2 > blimit {
		return
	}
	var p2, q2, p3, q3 int32
	if filterLen >= 6 {
		p2, q2 = int32(buf[pos-3*step]), int32(buf[pos+2*step])
		if abs32(p2-p1) > limit || abs32(q2-q1) > limit {
			return
		}
		if filterLen >= 8 {
			p3, q3 = int32(buf[pos-4*step]), int32(buf[pos+3*step])
			if abs32(p3-p2) > limit || abs32(q3-q2) > limit {
				return
			}
		}
	}
	flat := int32(1) << bd
	if size >= 8 && ap <= flat && aq <= flat && abs32(p2-p0) <= flat && abs32(q2-q0) <= flat &&
		(filterLen < 8 || abs32(p3-p0) <= flat && abs32(q3-q0) <= flat) {
		if size == 16 {
			flat2 := true
			for k := 4; k <= 6; k++ {
				if abs32(int32(buf[pos-(k+1)*step])-p0) > flat || abs32(int32(buf[pos+k*step])-q0) > flat {
					flat2 = false
					break
				}
			}
			if flat2 {
				wideFilter(buf, pos, step, 6, 1, 4)
				return
			}
		}
		if plane == 0 {
			wideFilter(buf, pos, step, 3, 0, 3)
		} else {
			wideFilter(buf, pos, step, 2, 1, 3)
		}
		return
	}
	// the narrow filter
	hev := ap > thresh || aq > thresh
	lo, hi := -(int32(1) << (bd + 7)), int32(1)<<(bd+7)-1
	c := func(v int32) int32 { return min(max(v, lo), hi) }
	off := int32(0x80) << bd
	ps1, ps0, qs0, qs1 := p1-off, p0-off, q0-off, q1-off
	var filter int32
	if hev {
		filter = c(ps1 - qs1)
	}
	filter = c(filter + 3*(qs0-ps0))
	filter1 := c(filter+4) >> 3
	filter2 := c(filter+3) >> 3
	buf[pos] = uint16(c(qs0-filter1) + off)      //nolint:gosec // a sample
	buf[pos-step] = uint16(c(ps0+filter2) + off) //nolint:gosec // a sample
	if !hev {
		filter = (filter1 + 1) >> 1
		buf[pos+step] = uint16(c(qs1-filter) + off)   //nolint:gosec // a sample
		buf[pos-2*step] = uint16(c(ps1+filter) + off) //nolint:gosec // a sample
	}
}

// wideFilter is the wide filter process (spec 7.14.6.4): n taps each
// side of the centre, the n2 nearest weighing 2, the sum over 1<<log2Size.
// The sums of both windows (the samples clamped to the n+1 each side)
// slide along from one output to the next.
func wideFilter(buf []uint16, pos, step, n, n2 int, log2Size uint) {
	var s [14]int32 // s[k+n+1] is the sample at k, k from -(n+1) to n
	for k := -(n + 1); k <= n; k++ {
		s[k+n+1] = int32(buf[pos+k*step])
	}
	at := func(k int) int32 { return s[min(max(k, -(n+1)), n)+n+1] }
	var wn, w2 int32 // the windows' sums for output -n
	for j := -n; j <= n; j++ {
		wn += at(j - n)
	}
	for j := -n2; j <= n2; j++ {
		w2 += at(j - n)
	}
	round := int32(1) << (log2Size - 1)
	for i := -n; i < n; i++ {
		buf[pos+i*step] = uint16((wn + w2 + round) >> log2Size) //nolint:gosec // a sample
		wn += at(i+n+1) - at(i-n)
		w2 += at(i+n2+1) - at(i-n2)
	}
}

// planeSSE is the squared error of plane p of buf against the source over
// the visible picture.
func (f *frameEnc) planeSSE(p int, buf []uint16) int64 {
	w, h := f.width, f.height
	if p > 0 {
		w, h = (w+1)/2, (h+1)/2
	}
	stride := f.stride[p]
	var parts [64]int64
	n := min(runtime.GOMAXPROCS(0), len(parts), h)
	var wg sync.WaitGroup
	for k := range n {
		wg.Go(func() {
			var se int64
			for y := k * h / n; y < (k+1)*h/n; y++ {
				a, b := f.src[p][y*stride:y*stride+w], buf[y*stride:y*stride+w]
				for x := range a {
					d := int64(a[x]) - int64(b[x])
					se += d * d
				}
			}
			parts[k] = se
		})
	}
	wg.Wait()
	var se int64
	for _, v := range parts[:n] {
		se += v
	}
	return se
}

// deblock decides the frame's loop filter levels (or takes fixed ones,
// with the sharpness at fixed[4]) into f.fp and applies the filter to
// f.rec.
func (f *frameEnc) deblock(fixed *[5]int) {
	if f.lossless {
		f.fp.lfLevel, f.fp.lfSharp = [4]int{}, 0
		return
	}
	var level [4]int
	sharp := 0
	if fixed != nil {
		level, sharp = [4]int(fixed[:4]), fixed[4]
	} else {
		level = f.pickLevels(sharp)
	}
	if level[0] == 0 && level[1] == 0 {
		level = [4]int{}
	}
	f.fp.lfLevel, f.fp.lfSharp = level, sharp
	lf := newLoopFilter(f, level, sharp)
	for p := range 3 {
		lf.filterPlane(p, f.rec[p])
	}
}

// pickLevels searches the levels: luma's two together, then each on its
// own from there, then each chroma plane's (when luma filters at all).
func (f *frameEnc) pickLevels(sharp int) [4]int {
	var level [4]int
	tmp := make([]uint16, len(f.rec[0]))
	try := func(p int, l [4]int) int64 {
		buf := tmp[:len(f.rec[p])]
		copy(buf, f.rec[p])
		newLoopFilter(f, l, sharp).filterPlane(p, buf)
		return f.planeSSE(p, buf)
	}
	both := scanLevel(func(v int) int64 { return try(0, [4]int{v, v}) })
	level[0], level[1] = both, both
	if !f.tools.lfBoth {
		level[0] = searchLevel(both, 4, func(v int) int64 { return try(0, [4]int{v, level[1]}) })
		level[1] = searchLevel(both, 4, func(v int) int64 { return try(0, [4]int{level[0], v}) })
	}
	if level[0] == 0 && level[1] == 0 {
		return level
	}
	for p := 1; p <= 2; p++ {
		level[1+p] = scanLevel(func(v int) int64 {
			l := level
			l[1+p] = v
			return try(p, l)
		})
	}
	return level
}

// scanLevel is the level of least error: the best of every eighth, then
// searched from there. (The error is far from convex in the level, flat
// over ranges where the masks stop changing, so a search from a guess
// alone, as libaom's from q, can stall.)
func scanLevel(errAt func(int) int64) int {
	var errs [MAX_LOOP_FILTER + 1]int64
	var done [MAX_LOOP_FILTER + 1]bool
	get := func(v int) int64 {
		if !done[v] {
			errs[v], done[v] = errAt(v), true
		}
		return errs[v]
	}
	best, bestErr := 0, get(0)
	for v := 8; v <= MAX_LOOP_FILTER+7; v += 8 {
		v = min(v, MAX_LOOP_FILTER)
		if e := get(v); e < bestErr {
			best, bestErr = v, e
		}
	}
	return searchLevel(best, 4, get)
}

// searchLevel is libaom's search_filter_level: from start, steps of step
// halving each time neither neighbour does better, going on in the
// direction that did. A tie goes to the lower level.
func searchLevel(start, step int, errAt func(int) int64) int {
	var errs [MAX_LOOP_FILTER + 1]int64
	var done [MAX_LOOP_FILTER + 1]bool
	get := func(v int) int64 {
		if !done[v] {
			errs[v], done[v] = errAt(v), true
		}
		return errs[v]
	}
	mid := start
	best := get(mid)
	dir := 0
	for step > 0 {
		hi, lo := min(mid+step, MAX_LOOP_FILTER), max(mid-step, 0)
		bestL := mid
		if dir <= 0 && lo != mid {
			if e := get(lo); e <= best {
				best, bestL = e, lo
			}
		}
		if dir >= 0 && hi != mid {
			if e := get(hi); e < best {
				best, bestL = e, hi
			}
		}
		if bestL != mid {
			dir = 1
			if bestL < mid {
				dir = -1
			}
			mid = bestL
		} else {
			dir = 0
			step /= 2
		}
	}
	return mid
}
