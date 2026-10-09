package hevc

import (
	"sync"
	"sync/atomic"
)

// The in-loop filters (8.7), CTB row by CTB row as rows are decoded (see
// frame.go for the order): deblocking, every vertical edge of a row then
// its horizontal ones, then SAO from a copy of the deblocked rows.

// noLoopFilter turns the loop filters off (tests).
var noLoopFilter bool

// sliceAt is the slice header of the CTB containing luma (x, y).
func (f *frame) sliceAt(x, y int) *sliceHeader {
	s := f.sps
	return f.ps.slices[f.ps.ctbSlice[(y>>s.log2Ctb)*s.ctbW+x>>s.log2Ctb]]
}

// refPOCAt is the POC of the picture block m (at luma (x, y)) refers to in
// list l.
func (f *frame) refPOCAt(x, y, l int, m *mvField) int32 {
	s := f.sps
	slice := f.pic.ctbSlice[(y>>s.log2Ctb)*s.ctbW+x>>s.log2Ctb]
	return f.pic.refPOC[slice][l][m.refIdx[l]]
}

// mvStrength is the boundary strength two inter blocks' motion gives
// (8.7.2.4).
func (f *frame) mvStrength(xq, yq, xp, yp int) int {
	pic := f.pic
	q := &pic.mvf[(yq>>2)*pic.mvfW+xq>>2]
	p := &pic.mvf[(yp>>2)*pic.mvfW+xp>>2]
	far := func(a, b mv) bool {
		return iabs(int(a.x)-int(b.x)) >= 4 || iabs(int(a.y)-int(b.y)) >= 4
	}
	if q.pred == 3 && p.pred == 3 {
		q0, q1 := f.refPOCAt(xq, yq, 0, q), f.refPOCAt(xq, yq, 1, q)
		p0, p1 := f.refPOCAt(xp, yp, 0, p), f.refPOCAt(xp, yp, 1, p)
		switch {
		case q0 == p0 && q0 == q1 && p0 == p1:
			if (far(p.mv[0], q.mv[0]) || far(p.mv[1], q.mv[1])) && (far(p.mv[1], q.mv[0]) || far(p.mv[0], q.mv[1])) {
				return 1
			}
			return 0
		case p0 == q0 && p1 == q1:
			if far(p.mv[0], q.mv[0]) || far(p.mv[1], q.mv[1]) {
				return 1
			}
			return 0
		case p1 == q0 && p0 == q1:
			if far(p.mv[1], q.mv[0]) || far(p.mv[0], q.mv[1]) {
				return 1
			}
			return 0
		}
		return 1
	}
	if q.pred != 3 && p.pred != 3 {
		lq, lp := 0, 0
		if q.pred&1 == 0 {
			lq = 1
		}
		if p.pred&1 == 0 {
			lp = 1
		}
		if f.refPOCAt(xq, yq, lq, q) != f.refPOCAt(xp, yp, lp, p) {
			return 1
		}
		if far(q.mv[lq], p.mv[lp]) {
			return 1
		}
		return 0
	}
	return 1
}

// edgeStrength is the boundary strength of the 4-sample edge segment
// between (xp, yp) and (xq, yq), or 0 when it is not filtered.
func (f *frame) edgeStrength(xq, yq, xp, yp int, flags uint8, vertical bool) int {
	s, ps, p := f.sps, &f.ps, f.pps
	hq := f.sliceAt(xq, yq)
	if hq.deblockingDisabled {
		return 0
	}
	ctbQ := (yq>>s.log2Ctb)*s.ctbW + xq>>s.log2Ctb
	ctbP := (yp>>s.log2Ctb)*s.ctbW + xp>>s.log2Ctb
	if ctbQ != ctbP {
		hp := f.sliceAt(xp, yp)
		if hp.sliceAddr != hq.sliceAddr && !hq.loopFilterAcross {
			return 0
		}
		if p.tileID[p.rsToTS[ctbQ]] != p.tileID[p.rsToTS[ctbP]] && !p.loopFilterAcrossTiles {
			return 0
		}
	}
	iq, ip := (yq>>2)*ps.w4+xq>>2, (yp>>2)*ps.w4+xp>>2
	if ps.predMode[iq] == modeIntra || ps.predMode[ip] == modeIntra {
		return 2
	}
	if flags&1 != 0 && (ps.cbfLuma[iq] || ps.cbfLuma[ip]) {
		return 1
	}
	return f.mvStrength(xq, yq, xp, yp)
}

// rowLines is CTB row r's luma lines, [y0, y1).
func (f *frame) rowLines(r int) (y0, y1 int) {
	s := f.sps
	return r << s.log2Ctb, min(s.height, (r+1)<<s.log2Ctb)
}

// deblockV derives the boundary strengths of CTB row r's edges and
// filters its vertical edges.
func (f *frame) deblockV(r int) {
	y0, y1 := f.rowLines(r)
	w4 := (f.sps.width + 3) >> 2
	f.edgeStrengths(y0, y1, w4)
	f.deblockEdges(y0, y1, w4, true)
}

// deblockH filters CTB row r's horizontal edges, its top edge with them.
// The edges are 8 apart and change at most 3 samples on each side, so a
// row's never touch the samples another's read.
func (f *frame) deblockH(r int) {
	y0, y1 := f.rowLines(r)
	f.deblockEdges(y0, y1, (f.sps.width+3)>>2, false)
}

// parallel runs fn over [0, n) in bands of size, on f.threads goroutines.
func (f *frame) parallel(n, size int, fn func(lo, hi int)) {
	bands := (n + size - 1) / size
	workers := min(f.threads, bands)
	if workers < 2 {
		fn(0, n)
		return
	}
	var next atomic.Int32
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				b := int(next.Add(1)) - 1
				if b >= bands {
					return
				}
				fn(b*size, min(n, (b+1)*size))
			}
		}()
	}
	wg.Wait()
}

// edgeStrengths derives the boundary strengths of the edges in luma rows
// [y0, y1).
func (f *frame) edgeStrengths(y0, y1, w4 int) {
	s, ps := f.sps, &f.ps
	bsV, bsH := f.bsV, f.bsH
	for y := y0; y < y1; y += 4 {
		for x := 0; x < s.width; x += 4 {
			i := (y>>2)*w4 + x>>2
			bsV[i], bsH[i] = 0, 0
			if x > 0 && x&7 == 0 {
				if e := ps.tuEdgeV[(y>>2)*ps.w4+x>>2]; e != 0 {
					bsV[i] = uint8(f.edgeStrength(x, y, x-1, y, e, true))
				}
			}
			if y > 0 && y&7 == 0 {
				if e := ps.tuEdgeH[(y>>2)*ps.w4+x>>2]; e != 0 {
					bsH[i] = uint8(f.edgeStrength(x, y, x, y-1, e, false))
				}
			}
		}
	}
}

// deblockEdges filters the vertical or horizontal edges in luma rows
// [y0, y1), luma and chroma.
func (f *frame) deblockEdges(y0, y1, w4 int, vertical bool) {
	s, ps := f.sps, &f.ps
	pic := f.pic
	bsV, bsH := f.bsV, f.bsH
	qpAt := func(x, y int) int { return int(ps.qpY[(y>>2)*ps.w4+x>>2]) }
	noF := func(x, y int) bool { return ps.noFilter[(y>>2)*ps.w4+x>>2] }
	for y := y0; y < y1; y += 4 {
		for x := 0; x < s.width; x += 4 {
			var bs uint8
			var xp, yp int
			if vertical {
				if x == 0 || x&7 != 0 {
					continue
				}
				bs = bsV[(y>>2)*w4+x>>2]
				xp, yp = x-1, y
			} else {
				if y == 0 || y&7 != 0 {
					continue
				}
				bs = bsH[(y>>2)*w4+x>>2]
				xp, yp = x, y-1
			}
			if bs == 0 {
				continue
			}
			h := f.sliceAt(x, y)
			qp := (qpAt(x, y) + qpAt(xp, yp) + 1) >> 1
			beta := betaTable[clip3(0, 51, qp+h.betaOffset)] << (s.bitDepth - 8)
			tc := tcTable[clip3(0, 53, qp+2*(int(bs)-1)+h.tcOffset)] << (s.bitDepth - 8)
			lumaEdge(pic.y, pic.strideY, x, y, vertical, beta, tc, noF(xp, yp), noF(x, y), s.bitDepth)
		}
	}
	// Chroma edges on the 8x8 chroma grid (16 luma), with strength 2.
	for ci := 1; ci <= 2; ci++ {
		pl := pic.cb
		off := f.pps.cbQPOffset
		if ci == 2 {
			pl = pic.cr
			off = f.pps.crQPOffset
		}
		for y := (y0 + 7) &^ 7; y < y1; y += 8 {
			for x := 0; x < s.width; x += 8 {
				var bs uint8
				var xp, yp int
				if vertical {
					if x == 0 || x&15 != 0 {
						continue
					}
					bs = bsV[(y>>2)*w4+x>>2]
					xp, yp = x-1, y
				} else {
					if y == 0 || y&15 != 0 {
						continue
					}
					bs = bsH[(y>>2)*w4+x>>2]
					xp, yp = x, y-1
				}
				if bs != 2 {
					continue
				}
				h := f.sliceAt(x, y)
				qpi := ((qpAt(x, y) + qpAt(xp, yp) + 1) >> 1) + off
				qpc := chromaQP(qpi)
				if qpi < 0 {
					qpc = qpi
				}
				tc := tcTable[clip3(0, 53, qpc+2+h.tcOffset)] << (s.bitDepthC - 8)
				chromaEdge(pl, pic.strideC, x/2, y/2, vertical, tc, noF(xp, yp), noF(x, y), s.bitDepthC)
			}
		}
	}
}

func lumaEdge(pl []uint16, stride, x, y int, vertical bool, beta, tc int, noP, noQ bool, depth int) {
	// at(k, i) is sample i across the edge (p0 = -1, q0 = 0) of line k.
	var across, along int
	if vertical {
		across, along = 1, stride
	} else {
		across, along = stride, 1
	}
	base := y*stride + x
	at := func(k, i int) int { return int(pl[base+k*along+i*across]) }
	dp0 := iabs(at(0, -3) - 2*at(0, -2) + at(0, -1))
	dp3 := iabs(at(3, -3) - 2*at(3, -2) + at(3, -1))
	dq0 := iabs(at(0, 2) - 2*at(0, 1) + at(0, 0))
	dq3 := iabs(at(3, 2) - 2*at(3, 1) + at(3, 0))
	dpq0, dpq3 := dp0+dq0, dp3+dq3
	dp, dq := dp0+dp3, dq0+dq3
	if dpq0+dpq3 >= beta {
		return
	}
	strong := func(k, dpq int) bool {
		return 2*dpq < beta>>2 &&
			iabs(at(k, -4)-at(k, -1))+iabs(at(k, 0)-at(k, 3)) < beta>>3 &&
			iabs(at(k, -1)-at(k, 0)) < (5*tc+1)>>1
	}
	dE := 1
	if strong(0, dpq0) && strong(3, dpq3) {
		dE = 2
	}
	dEp := dp < (beta+beta>>1)>>3
	dEq := dq < (beta+beta>>1)>>3
	maxV := 1<<depth - 1
	for k := range 4 {
		o := base + k*along
		p0, p1, p2, p3 := at(k, -1), at(k, -2), at(k, -3), at(k, -4)
		q0, q1, q2, q3 := at(k, 0), at(k, 1), at(k, 2), at(k, 3)
		set := func(i, v int) { pl[o+i*across] = uint16(v) }
		if dE == 2 {
			if !noP {
				set(-1, clip3(p0-2*tc, p0+2*tc, (p2+2*p1+2*p0+2*q0+q1+4)>>3))
				set(-2, clip3(p1-2*tc, p1+2*tc, (p2+p1+p0+q0+2)>>2))
				set(-3, clip3(p2-2*tc, p2+2*tc, (2*p3+3*p2+p1+p0+q0+4)>>3))
			}
			if !noQ {
				set(0, clip3(q0-2*tc, q0+2*tc, (p1+2*p0+2*q0+2*q1+q2+4)>>3))
				set(1, clip3(q1-2*tc, q1+2*tc, (p0+q0+q1+q2+2)>>2))
				set(2, clip3(q2-2*tc, q2+2*tc, (p0+q0+q1+3*q2+2*q3+4)>>3))
			}
			continue
		}
		delta := (9*(q0-p0) - 3*(q1-p1) + 8) >> 4
		if iabs(delta) >= tc*10 {
			continue
		}
		delta = clip3(-tc, tc, delta)
		if !noP {
			set(-1, clip3(0, maxV, p0+delta))
		}
		if !noQ {
			set(0, clip3(0, maxV, q0-delta))
		}
		if dEp && !noP {
			dpv := clip3(-(tc >> 1), tc>>1, (((p2+p0+1)>>1)-p1+delta)>>1)
			set(-2, clip3(0, maxV, p1+dpv))
		}
		if dEq && !noQ {
			dqv := clip3(-(tc >> 1), tc>>1, (((q2+q0+1)>>1)-q1-delta)>>1)
			set(1, clip3(0, maxV, q1+dqv))
		}
	}
}

// chromaEdge filters the four lines of a chroma edge segment at (x, y)
// (chroma samples) (8.7.2.5.5).
func chromaEdge(pl []uint16, stride, x, y int, vertical bool, tc int, noP, noQ bool, depth int) {
	var across, along int
	if vertical {
		across, along = 1, stride
	} else {
		across, along = stride, 1
	}
	maxV := 1<<depth - 1
	for k := range 4 {
		o := y*stride + x + k*along
		p0, p1 := int(pl[o-across]), int(pl[o-2*across])
		q0, q1 := int(pl[o]), int(pl[o+across])
		delta := clip3(-tc, tc, ((((q0 - p0) << 2) + p1 - q1 + 4) >> 3))
		if !noP {
			pl[o-across] = uint16(clip3(0, maxV, p0+delta))
		}
		if !noQ {
			pl[o] = uint16(clip3(0, maxV, q0-delta))
		}
	}
}

// saoCopy keeps CTB row r's first and last lines, deblocked, for the SAO
// of the rows around it to read (their own SAO changes them).
func (f *frame) saoCopy(r int) {
	s, pic := f.sps, f.pic
	for ci, pl := range [3][]uint16{pic.y, pic.cb, pic.cr} {
		stride, size := pic.strideY, s.ctbSize
		if ci > 0 {
			stride, size = pic.strideC, s.ctbSize/2
		}
		lines := f.saoLines[ci][2*r*stride : (2*r+2)*stride]
		copy(lines[:stride], pl[r*size*stride:])
		copy(lines[stride:], pl[((r+1)*size-1)*stride:])
	}
}

// saoScratch holds a CTB row, deblocked, with a line above and below.
var saoScratch sync.Pool

// sao applies sample adaptive offset (8.7.3) to CTB row r, from a copy of
// it with the lines around it.
func (f *frame) sao(r int) {
	s, pic := f.sps, f.pic
	dst := [3][]uint16{pic.y, pic.cb, pic.cr}
	var src [3][]uint16
	var base [3]int
	bufs, _ := saoScratch.Get().(*[3][]uint16)
	if bufs == nil {
		bufs = new([3][]uint16)
	}
	for ci, pl := range dst {
		stride, size := pic.strideY, s.ctbSize
		if ci > 0 {
			stride, size = pic.strideC, s.ctbSize/2
		}
		n := (size + 2) * stride
		if cap(bufs[ci]) < n {
			bufs[ci] = make([]uint16, n)
		}
		buf := bufs[ci][:n]
		if r > 0 {
			copy(buf[:stride], f.saoLines[ci][(2*r-1)*stride:])
		}
		copy(buf[stride:(size+1)*stride], pl[r*size*stride:])
		if r+1 < s.ctbH {
			copy(buf[(size+1)*stride:], f.saoLines[ci][(2*r+2)*stride:(2*r+3)*stride])
		}
		src[ci], base[ci] = buf, (r*size-1)*stride
	}
	f.saoRow(r, src, base, dst)
	saoScratch.Put(bufs)
}

// saoRow applies SAO to CTB row ry, from src (sample i of a plane at
// i - base) to dst.
func (f *frame) saoRow(ry int, src [3][]uint16, base [3]int, dst [3][]uint16) {
	s, ps := f.sps, &f.ps
	pic := f.pic
	for rx := range s.ctbW {
		addr := ry*s.ctbW + rx
		h := ps.slices[ps.ctbSlice[addr]]
		noFilter := f.ctbNoFilter(rx, ry)
		for ci := range 3 {
			if ci == 0 && !h.saoLuma || ci > 0 && !h.saoChroma {
				continue
			}
			sp := &ps.sao[addr][ci]
			if sp.typ == 0 {
				continue
			}
			shift := 0
			stride := pic.strideY
			depth := s.bitDepth
			if ci > 0 {
				shift = 1
				stride = pic.strideC
				depth = s.bitDepthC
			}
			size := s.ctbSize >> shift
			x0, y0 := rx*size, ry*size
			w := min(size, (s.width>>shift)-x0)
			hgt := min(size, (s.height>>shift)-y0)
			maxV := 1<<depth - 1
			in, out := src[ci][max(0, -base[ci]):], dst[ci]
			ib := max(0, base[ci]) // in[o-ib] is sample o
			if sp.typ == 1 {
				var bandTable [32]int
				for k := range 4 {
					bandTable[(k+int(sp.band))&31] = k + 1
				}
				bandShift := depth - 5
				for y := range hgt {
					o := (y0+y)*stride + x0
					row, orow := in[o-ib:o-ib+w], out[o:o+w]
					for x, v := range row {
						k := bandTable[int(v)>>bandShift]
						if k == 0 || noFilter && f.sampleNoFilter(x0+x, y0+y, shift) {
							continue
						}
						orow[x] = uint16(clip3(0, maxV, int(v)+int(sp.offset[k-1])))
					}
				}
				continue
			}
			// Edge offset: the offset of each edge category.
			hPos := [4][2]int{{-1, 1}, {0, 0}, {-1, 1}, {1, -1}}[sp.class]
			vPos := [4][2]int{{0, 0}, {-1, 1}, {-1, 1}, {-1, 1}}[sp.class]
			offs := [5]int{int(sp.offset[0]), int(sp.offset[1]), 0, int(sp.offset[2]), int(sp.offset[3])}
			d0 := vPos[0]*stride + hPos[0]
			d1 := vPos[1]*stride + hPos[1]
			edge := func(x, y int) {
				xs, ys := x0+x, y0+y
				xl, yl := xs<<shift, ys<<shift
				if noFilter && f.sampleNoFilter(xs, ys, shift) {
					return
				}
				for i := range 2 {
					xn, yn := xs+hPos[i], ys+vPos[i]
					if xn < 0 || yn < 0 || xn >= s.width>>shift || yn >= s.height>>shift {
						return
					}
					if !f.saoNeighbourOK(xl, yl, xn<<shift, yn<<shift) {
						return
					}
				}
				o := ys*stride + xs
				v := int(in[o-ib])
				e := 2 + sign(v-int(in[o-ib+d0])) + sign(v-int(in[o-ib+d1]))
				if e != 2 {
					out[o] = uint16(clip3(0, maxV, v+offs[e]))
				}
			}
			// The samples inside the CTB have their neighbours in it.
			for y := range hgt {
				if y == 0 || y == hgt-1 || noFilter {
					for x := range w {
						edge(x, y)
					}
					continue
				}
				edge(0, y)
				o := (y0+y)*stride + x0
				for x := 1; x < w-1; x++ {
					v := int(in[o-ib+x])
					e := 2 + sign(v-int(in[o-ib+x+d0])) + sign(v-int(in[o-ib+x+d1]))
					if e != 2 {
						out[o+x] = uint16(clip3(0, maxV, v+offs[e]))
					}
				}
				if w > 1 {
					edge(w-1, y)
				}
			}
		}
	}
}

func sign(v int) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}

// ctbNoFilter reports whether a block of the CTB is left unfiltered (PCM
// with its loop filter off, transquant bypass).
func (f *frame) ctbNoFilter(rx, ry int) bool {
	s, ps := f.sps, &f.ps
	n := s.ctbSize >> 2
	bx, by := rx*n, ry*n
	for y := by; y < min(by+n, ps.h4); y++ {
		for _, f := range ps.noFilter[y*ps.w4+bx : y*ps.w4+min(bx+n, ps.w4)] {
			if f {
				return true
			}
		}
	}
	return false
}

// sampleNoFilter reports whether the sample at (x, y) of a component
// (shift 1 for chroma) is in an unfiltered block.
func (f *frame) sampleNoFilter(x, y, shift int) bool {
	xl, yl := x<<shift, y<<shift
	return f.ps.noFilter[(yl>>2)*f.ps.w4+xl>>2]
}

func (f *frame) saoNeighbourOK(x, y, xn, yn int) bool {
	s, p, ps := f.sps, f.pps, &f.ps
	ctb := (y>>s.log2Ctb)*s.ctbW + x>>s.log2Ctb
	ctbN := (yn>>s.log2Ctb)*s.ctbW + xn>>s.log2Ctb
	if ctb == ctbN {
		return true
	}
	h, hn := ps.slices[ps.ctbSlice[ctb]], ps.slices[ps.ctbSlice[ctbN]]
	if h.sliceAddr != hn.sliceAddr {
		zc := p.minTbZs[(y>>s.log2MinTb)*s.minTbW+x>>s.log2MinTb]
		zn := p.minTbZs[(yn>>s.log2MinTb)*s.minTbW+xn>>s.log2MinTb]
		if zn < zc && !h.loopFilterAcross || zc < zn && !hn.loopFilterAcross {
			return false
		}
	}
	return p.loopFilterAcrossTiles || p.tileID[p.rsToTS[ctb]] == p.tileID[p.rsToTS[ctbN]]
}
