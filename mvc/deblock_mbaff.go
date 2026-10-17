package mvc

// The deblocking filter of MBAFF frames (8.7), edge line by edge line as
// 8.7.1 lays it out: the samples across an edge are the frame's (a field
// macroblock's lines every other row), so what changes from a frame is
// which macroblock and block each p0 and q0 is in, and the strength rules
// for edges between frame and field macroblocks.

// mbaffAt locates the luma sample (x, y) of an MBAFF frame: the grid index
// of its macroblock and its 4x4 block (raster) in it.
func (fc *frameCtx) mbaffAt(x, y int) (gi, blk int) {
	W := fc.mbW
	px, prow := x>>4, y>>5
	top := 2 * prow * W
	var row int
	if fc.mbs[top+px].flags&mbfField != 0 {
		gi = top + px + (y&1)*W
		row = (y & 31) >> 1
	} else {
		gi = top + px + ((y&31)>>4)*W
		row = y & 15
	}
	return gi, (row>>2)*4 + (x&15)>>2
}

// mbaffBS is the boundary strength (8.7.2.1) of the luma line whose p0
// is at (px, py) and q0 at (qx, qy), across a macroblock edge (mbEdge) or
// not, vertical or horizontal.
func (fc *frameCtx) mbaffBS(px, py, qx, qy int, mbEdge, vertical bool) uint8 {
	pg, pb := fc.mbaffAt(px, py)
	qg, qb := fc.mbaffAt(qx, qy)
	p, q := &fc.mbs[pg], &fc.mbs[qg]
	pField, qField := p.flags&mbfField != 0, q.flags&mbfField != 0
	W := fc.mbW
	// different pairs, one a field pair, the other a frame pair
	mixed := mbEdge && pField != qField && (pg%W != qg%W || pg/(2*W) != qg/(2*W))
	if p.flags&mbfIntra != 0 || q.flags&mbfIntra != 0 {
		if mbEdge && (!pField && !qField || vertical) {
			return 4
		}
		return 3
	}
	if p.nzMask>>pb&1 != 0 || q.nzMask>>qb&1 != 0 {
		return 2
	}
	if mixed {
		return 1
	}
	pi := (pg/W*4+pb>>2)*W*4 + pg%W*4 + pb&3
	qi := (qg/W*4+qb>>2)*W*4 + qg%W*4 + qb&3
	diff := mvDiff
	if qField {
		diff = mvDiffField // four quarter frame samples are two field ones
	}
	return fc.motionBSWith(pi, qi, diff)
}

// motionBSWith is motionBS with the vector comparison given.
func (fc *frameCtx) motionBSWith(p, q int, mvDiff func(a, b mv) bool) uint8 {
	pic := fc.pic
	p0, p1 := fc.refIDs[0][p], fc.refIDs[1][p]
	q0, q1 := fc.refIDs[0][q], fc.refIDs[1][q]
	np, nq := 0, 0
	if p0 >= 0 {
		np++
	}
	if p1 >= 0 {
		np++
	}
	if q0 >= 0 {
		nq++
	}
	if q1 >= 0 {
		nq++
	}
	if np != nq {
		return 1
	}
	switch np {
	case 0:
		return 0
	case 1:
		rp, mp := p0, pic.mvs[0][p]
		if p0 < 0 {
			rp, mp = p1, pic.mvs[1][p]
		}
		rq, mq := q0, pic.mvs[0][q]
		if q0 < 0 {
			rq, mq = q1, pic.mvs[1][q]
		}
		if rp != rq || mvDiff(mp, mq) {
			return 1
		}
		return 0
	}
	if (p0 != q0 || p1 != q1) && (p0 != q1 || p1 != q0) {
		return 1
	}
	mp0, mp1 := pic.mvs[0][p], pic.mvs[1][p]
	mq0, mq1 := pic.mvs[0][q], pic.mvs[1][q]
	if p0 != p1 {
		if p0 == q0 {
			if mvDiff(mp0, mq0) || mvDiff(mp1, mq1) {
				return 1
			}
		} else if mvDiff(mp0, mq1) || mvDiff(mp1, mq0) {
			return 1
		}
		return 0
	}
	if (mvDiff(mp0, mq0) || mvDiff(mp1, mq1)) && (mvDiff(mp0, mq1) || mvDiff(mp1, mq0)) {
		return 1
	}
	return 0
}

// mbaffLine filters one line across an edge: q0 at pl[o], p0 at
// pl[o-step], with bS and the quantisers of the two sides (8.7.2).
func mbaffLine(pl []byte, o, step int, bs uint8, qp, qq int, offA, offB int, chroma bool) {
	if bs == 0 {
		return
	}
	qpav := int32((qp + qq + 1) >> 1)
	indexA := clip3(0, 51, qpav+int32(offA))
	indexB := clip3(0, 51, qpav+int32(offB))
	alpha, beta := int32(alphaTab[indexA]), int32(betaTab[indexB])
	p0, q0 := int32(pl[o-step]), int32(pl[o])
	p1, q1 := int32(pl[o-2*step]), int32(pl[o+step])
	if iabs(p0-q0) >= alpha || iabs(p1-p0) >= beta || iabs(q1-q0) >= beta {
		return
	}
	if chroma {
		if bs < 4 {
			tc := int32(tc0Tab[indexA][bs-1]) + 1
			d := clip3(-tc, tc, (((q0-p0)<<2)+(p1-q1)+4)>>3)
			pl[o-step] = clip255(p0 + d)
			pl[o] = clip255(q0 - d)
			return
		}
		pl[o-step] = byte((2*p1 + p0 + q1 + 2) >> 2)
		pl[o] = byte((2*q1 + q0 + p1 + 2) >> 2)
		return
	}
	p2, q2 := int32(pl[o-3*step]), int32(pl[o+2*step])
	ap, aq := iabs(p2-p0), iabs(q2-q0)
	if bs < 4 {
		tc0 := int32(tc0Tab[indexA][bs-1])
		tc := tc0
		if ap < beta {
			tc++
		}
		if aq < beta {
			tc++
		}
		d := clip3(-tc, tc, (((q0-p0)<<2)+(p1-q1)+4)>>3)
		pl[o-step] = clip255(p0 + d)
		pl[o] = clip255(q0 - d)
		if ap < beta {
			pl[o-2*step] = byte(p1 + clip3(-tc0, tc0, (p2+((p0+q0+1)>>1)-(p1<<1))>>1))
		}
		if aq < beta {
			pl[o+step] = byte(q1 + clip3(-tc0, tc0, (q2+((p0+q0+1)>>1)-(q1<<1))>>1))
		}
		return
	}
	strong := iabs(p0-q0) < (alpha>>2)+2
	if ap < beta && strong {
		p3 := int32(pl[o-4*step])
		pl[o-step] = byte((p2 + 2*p1 + 2*p0 + 2*q0 + q1 + 4) >> 3)
		pl[o-2*step] = byte((p2 + p1 + p0 + q0 + 2) >> 2)
		pl[o-3*step] = byte((2*p3 + 3*p2 + p1 + p0 + q0 + 4) >> 3)
	} else {
		pl[o-step] = byte((2*p1 + p0 + q1 + 2) >> 2)
	}
	if aq < beta && strong {
		q3 := int32(pl[o+3*step])
		pl[o] = byte((p1 + 2*p0 + 2*q0 + 2*q1 + q2 + 4) >> 3)
		pl[o+step] = byte((p0 + q0 + q1 + q2 + 2) >> 2)
		pl[o+2*step] = byte((2*q3 + 3*q2 + q1 + q0 + p0 + 4) >> 3)
	} else {
		pl[o] = byte((2*q1 + q0 + p1 + 2) >> 2)
	}
}

// deblockMBAFF deblocks a macroblock of an MBAFF frame (8.7).
func (fc *frameCtx) deblockMBAFF(mbX, mbY int) {
	W := fc.mbW
	gi := mbY*W + mbX
	cur := &fc.mbs[gi]
	if cur.flags&mbfAvail == 0 {
		return
	}
	sp := &fc.slices[cur.slice]
	if sp.disableDeblock == 1 {
		return
	}
	field := cur.flags&mbfField != 0
	pairRow, bottom := mbY>>1, mbY&1
	xI, yI, dy := mbX*16, mbY*16, 1
	if field {
		yI, dy = pairRow*32+bottom, 2
	}
	sameSlice := func(g int) bool {
		m := &fc.mbs[g]
		return m.flags&mbfAvail != 0 && (sp.disableDeblock != 2 || m.slice == cur.slice)
	}
	filterLeft := mbX > 0 && sameSlice(gi-1)
	// the top edge: none at the picture's top (a field macroblock's, or
	// a top frame one's); a bottom frame macroblock's is its pair's
	var filterTop bool
	topSpecial := false // a top frame macroblock under a field pair: filtered per field
	switch {
	case !field && bottom == 1:
		filterTop = sameSlice(gi - W)
	case pairRow == 0:
		filterTop = false
	default:
		above := (2*pairRow-2)*W + mbX
		filterTop = sameSlice(above)
		topSpecial = !field && bottom == 0 && fc.mbs[above].flags&mbfField != 0
	}
	t8 := cur.flags&mbfT8x8 != 0
	pic := fc.pic
	ys, cs := pic.stride[0], pic.stride[1]
	yp := pic.planes[0]
	yo := pic.origin[0]
	qpOf := func(x, y int) int {
		g, _ := fc.mbaffAt(x, y)
		return int(fc.mbs[g].qp)
	}
	// luma; the strengths kept for chroma: vertical edges 0 and 8,
	// horizontal edges 0 (and the second field pass) and 8, per line
	var bsV [2][16]uint8
	var bsH [3][16]uint8
	for e := 0; e < 4; e++ {
		if e == 0 && !filterLeft || e > 0 && t8 && e&1 != 0 {
			continue
		}
		xE := e * 4
		for k := 0; k < 16; k++ {
			y := yI + dy*k
			bs := fc.mbaffBS(xI+xE-1, y, xI+xE, y, e == 0, true)
			if e&1 == 0 {
				bsV[e>>1][k] = bs
			}
			mbaffLine(yp, yo+y*ys+xI+xE, 1, bs, qpOf(xI+xE-1, y), int(cur.qp), sp.alphaOffset, sp.betaOffset, false)
		}
	}
	horiz := func(yE, dyE, slot int, mbEdge bool) {
		row := yI + dyE*yE - yE%2 // q0's row
		for k := 0; k < 16; k++ {
			x := xI + k
			bs := fc.mbaffBS(x, row-dyE, x, row, mbEdge, false)
			if slot >= 0 {
				bsH[slot][k] = bs
			}
			mbaffLine(yp, yo+row*ys+x, dyE*ys, bs, qpOf(x, row-dyE), int(cur.qp), sp.alphaOffset, sp.betaOffset, false)
		}
	}
	if filterTop {
		if topSpecial {
			horiz(0, 2, 0, true)
			horiz(1, 2, 1, true)
		} else {
			horiz(0, dy, 0, true)
		}
	}
	for e := 1; e < 4; e++ {
		if t8 && e&1 != 0 {
			continue
		}
		slot := -1
		if e == 2 {
			slot = 2
		}
		horiz(e*4, dy, slot, false)
	}
	if fc.sps.chromaFormatIdc == 0 {
		return
	}
	// chroma: the strength of the luma line of the same field
	xc, yc := xI/2, (yI+1)/2
	lumaRow := func(k int) int { // chroma line k of the macroblock -> its luma line
		if field {
			return 2 * k
		}
		return 4*(k>>1) + k&1
	}
	cqp := func(c, qp int) int { return chromaQP(qp, sp.chromaQPOffset[c]) }
	// the quantiser of the macroblock holding chroma sample (x, r): the
	// luma rows of the chroma row's field
	qpOfC := func(x, r int) int { return qpOf(2*x, 4*(r>>1)+r&1) }
	for c := 0; c < 2; c++ {
		pl := pic.planes[1+c]
		co := pic.origin[1]
		for e := 0; e < 2; e++ {
			if e == 0 && !filterLeft {
				continue
			}
			xE := e * 4
			for k := 0; k < 8; k++ {
				y := yc + dy*k
				mbaffLine(pl, co+y*cs+xc+xE, 1, bsV[e][lumaRow(k)], cqp(c, qpOfC(xc+xE-1, y)), cqp(c, int(cur.qp)), sp.alphaOffset, sp.betaOffset, true)
			}
		}
		hc := func(yE, dyE, slot int) {
			row := yc + dyE*yE - yE%2
			for k := 0; k < 8; k++ {
				mbaffLine(pl, co+row*cs+xc+k, dyE*cs, bsH[slot][2*k], cqp(c, qpOfC(xc+k, row-dyE)), cqp(c, int(cur.qp)), sp.alphaOffset, sp.betaOffset, true)
			}
		}
		if filterTop {
			if topSpecial {
				hc(0, 2, 0)
				hc(1, 2, 1)
			} else {
				hc(0, dy, 0)
			}
		}
		hc(4, dy, 2)
	}
}
