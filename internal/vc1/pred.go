package vc1

func median3(a, b, c int) int {
	if a > b {
		a, b = b, a
	}
	if b > c {
		b = c
	}
	if a > b {
		return a
	}
	return b
}

func median4(a, b, c, d int) int {
	if a < b {
		if c < d {
			return (min(b, d) + max(a, c)) / 2
		}
		return (min(b, c) + max(a, d)) / 2
	}
	if c < d {
		return (min(a, d) + max(b, c)) / 2
	}
	return (min(a, c) + max(b, d)) / 2
}

// wrapMV reduces a vector component to the range [-r, r) by the signed
// modulus of 4.11.
func wrapMV(v, r int) int16 { return int16((v+r)&(2*r-1) - r) }

// refdist is the distance to the reference of direction dir that field
// vector scaling uses.
func (m *mbContext) refdist(dir int) int {
	p := &m.d.ph
	rd := p.refdist
	if p.typ == picB {
		rd = p.frfd
		if dir == 1 {
			rd = p.brfd
		}
	}
	return min(rd, 3)
}

// scaleZone scales a predictor component by the zoned scales of 10.3.5.4.3.4.
func scaleZone(n, limit, zone, s1, s2, offset int) int {
	switch {
	case iabs(n) > limit:
		return n
	case iabs(n) < zone:
		return (n * s1) >> 8
	case n < 0:
		return (n*s2)>>8 - offset
	}
	return (n*s2)>>8 + offset
}

func (m *mbContext) clipFieldY(v, dir int) int {
	p := &m.d.ph
	if m.d.curFieldBottom && !m.d.refFieldBottom[dir] {
		return min(max(v, -p.rangeY/2+1), p.rangeY/2)
	}
	return min(max(v, -p.rangeY/2), p.rangeY/2-1)
}

// scaleForSame scales a predictor from the opposite field to the same
// field; dim 1 is the vertical component.
func (m *mbContext) scaleForSame(n, dim, dir int) int {
	p := &m.d.ph
	hpel := 1 - boolInt(p.quarter)
	n >>= hpel
	if p.typ != picB || m.d.secondField || dir == 0 {
		t := &fieldMVPredScales[dir^boolInt(m.d.secondField)]
		rd := m.refdist(dir)
		if dim == 0 {
			n = scaleZone(n, 255, int(t[3][rd]), int(t[1][rd]), int(t[2][rd]), int(t[5][rd]))
			n = min(max(n, -p.rangeX), p.rangeX-1)
		} else {
			n = scaleZone(n, 63, int(t[4][rd]), int(t[1][rd]), int(t[2][rd]), int(t[6][rd]))
			n = m.clipFieldY(n, dir)
		}
		return n * (1 << hpel)
	}
	brfd := min(p.brfd, 3)
	return (n * int(bFieldMVPredScales[0][brfd]) >> 8) * (1 << hpel)
}

// scaleForOpp scales a predictor from the same field to the opposite one.
func (m *mbContext) scaleForOpp(n, dim, dir int) int {
	p := &m.d.ph
	hpel := 1 - boolInt(p.quarter)
	n >>= hpel
	if p.typ == picB && !m.d.secondField && dir == 1 {
		t := &bFieldMVPredScales
		brfd := min(p.brfd, 3)
		if dim == 0 {
			n = scaleZone(n, 255, int(t[3][brfd]), int(t[1][brfd]), int(t[2][brfd]), int(t[5][brfd]))
			n = min(max(n, -p.rangeX), p.rangeX-1)
		} else {
			n = scaleZone(n, 63, int(t[4][brfd]), int(t[1][brfd]), int(t[2][brfd]), int(t[6][brfd]))
			n = m.clipFieldY(n, dir)
		}
		return n * (1 << hpel)
	}
	scale := int(fieldMVPredScales[dir^boolInt(m.d.secondField)][0][m.refdist(dir)])
	return (n * scale >> 8) * (1 << hpel)
}

// predMV predicts the vector of block n (the macroblock's when mv1) of a
// progressive or field P (or field B) picture in direction dir, adds the
// differential and stores it: the median of the blocks to the left, above
// and above right (or left), in field pictures scaled to the reference
// field chosen (8.3.5.3, 10.3.5.4); pulled back into the picture, and
// replaced by the one above or to the left (HYBRIDPRED) when far from
// both.
func (m *mbContext) predMV(n, dx, dy int, mv1 bool, predFlag, dir int, intra bool) {
	d := m.d
	p := &d.ph
	if !p.quarter {
		dx *= 2
		dy *= 2
	}
	xy := m.bi[n]
	bw := m.bw
	bo := m.blkOff
	mvs := m.mvs[dir]
	mvf := m.mvfY[dir]
	if intra {
		m.cmv[0][n] = mv{}
		for _, k := range [4]int{0, 1, bw, bw + 1} {
			if k != 0 && !mv1 {
				break
			}
			m.mvs[0][xy+k+bo] = mv{}
			m.mvs[1][xy+k+bo] = mv{}
		}
		if mv1 {
			m.uvMV[m.mbi] = mv{}
		}
		return
	}
	aValid := !m.firstLine || n == 2 || n == 3
	bValid := aValid
	cValid := m.mbx > 0 || n == 1 || n == 3
	off := 0
	last := m.mbx == d.mbw-1
	if mv1 {
		switch {
		case p.fieldMode && p.mixedMV() && last:
			off = -2
		case last:
			off = -1
		default:
			off = 2
		}
		bValid = bValid && d.mbw > 1
	} else {
		switch n {
		case 0:
			off = 1
			if m.mbx > 0 {
				off = -1
			}
		case 1:
			off = 1
			if last {
				off = -1
			}
		case 2:
			off = 1
		case 3:
			off = -1
		}
		if p.fieldMode && d.mbw == 1 {
			bValid = bValid && cValid
		}
	}
	if p.fieldMode {
		aValid = aValid && !m.intraY[xy-bw]
		bValid = bValid && !m.intraY[xy-bw+off]
		cValid = cValid && !m.intraY[xy-1]
	}
	var a, b, c [2]int
	var af, bf, cf bool
	same, opp := 0, 0
	if aValid {
		v := mvs[xy-bw+bo]
		a = [2]int{int(v.x), int(v.y)}
		af = mvf[xy-bw+bo]
		if af {
			opp++
		} else {
			same++
		}
	}
	if bValid {
		v := mvs[xy-bw+off+bo]
		b = [2]int{int(v.x), int(v.y)}
		bf = mvf[xy-bw+off+bo]
		if bf {
			opp++
		} else {
			same++
		}
	}
	if cValid {
		v := mvs[xy-1+bo]
		c = [2]int{int(v.x), int(v.y)}
		cf = mvf[xy-1+bo]
		if cf {
			opp++
		} else {
			same++
		}
	}
	opposite := false
	if p.fieldMode {
		if !p.numref {
			opposite = p.reffield == 0
		} else if same <= opp {
			opposite = predFlag == 0
		} else {
			opposite = predFlag != 0
		}
	}
	scale := func(v *[2]int, valid, f bool) {
		if !valid {
			return
		}
		if opposite && !f {
			v[0], v[1] = m.scaleForOpp(v[0], 0, dir), m.scaleForOpp(v[1], 1, dir)
		} else if !opposite && f {
			v[0], v[1] = m.scaleForSame(v[0], 0, dir), m.scaleForSame(v[1], 1, dir)
		}
	}
	mvf[xy+bo] = opposite
	d.refFieldBottom[dir] = d.curFieldBottom != opposite
	scale(&a, aValid, af)
	scale(&b, bValid, bf)
	scale(&c, cValid, cf)
	var px, py int
	switch {
	case aValid:
		px, py = a[0], a[1]
	case cValid:
		px, py = c[0], c[1]
	case bValid:
		px, py = b[0], b[1]
	}
	if same+opp > 1 {
		px = median3(a[0], b[0], c[0])
		py = median3(a[1], b[1], c[1])
	}
	if !p.fieldMode {
		// Pull back (8.3.5.3.4).
		lim := -28
		if mv1 {
			lim = -60
		}
		qx := m.mbx << 6
		qy := m.mby << 6
		if n == 1 || n == 3 {
			qx += 32
		}
		if n == 2 || n == 3 {
			qy += 32
		}
		X := d.mbw<<6 - 4
		Y := d.mbh<<6 - 4
		if qx+px < lim {
			px = lim - qx
		}
		if qy+py < lim {
			py = lim - qy
		}
		if qx+px > X {
			px = X - qx
		}
		if qy+py > Y {
			py = Y - qy
		}
	}
	if (!p.fieldMode || p.typ != picB) && aValid && cValid {
		// Hybrid prediction (8.3.5.3.5).
		var sum int
		if m.intraY[xy-bw] {
			sum = iabs(px) + iabs(py)
		} else {
			sum = iabs(px-a[0]) + iabs(py-a[1])
		}
		if sum <= 32 {
			if m.intraY[xy-1] {
				sum = iabs(px) + iabs(py)
			} else {
				sum = iabs(px-c[0]) + iabs(py-c[1])
			}
		}
		if sum > 32 {
			if m.r.u(1) == 1 {
				px, py = a[0], a[1]
			} else {
				px, py = c[0], c[1]
			}
		}
	}
	ry := p.rangeY
	if p.fieldMode && p.numref {
		ry >>= 1
	}
	bias := 0
	if p.fieldMode && d.curFieldBottom && !d.refFieldBottom[dir] {
		bias = 1
	}
	v := mv{wrapMV(px+dx, p.rangeX), int16((py+dy+ry-bias)&(2*ry-1) - ry + bias)}
	m.cmv[dir][n] = v
	mvs[xy+bo] = v
	if mv1 {
		mvs[xy+1+bo], mvs[xy+bw+bo], mvs[xy+bw+1+bo] = v, v, v
		mvf[xy+1+bo], mvf[xy+bw+bo], mvf[xy+bw+1+bo] = opposite, opposite, opposite
	}
}

// scaleMV scales a co-located vector for direct prediction: by bfraction
// forwards, by bfraction - 1 backwards.
func scaleMV(v, bfrac int, backward, quarter bool) int {
	n := bfrac
	if backward {
		n -= 256
	}
	if !quarter {
		return 2 * ((v*n + 255) >> 9)
	}
	return (v*n + 128) >> 8
}

// BMV types.
const (
	bmvBackward = iota
	bmvForward
	bmvInterpolated
	bmvDirect
)

// predBMV predicts a progressive B macroblock's vectors (8.4.5): the
// direct ones scaled from the next reference's, and those coded from their
// neighbours'.
func (m *mbContext) predBMV(dx, dy [2]int, direct bool, mode int, intra bool) {
	p := &m.d.ph
	if !p.quarter {
		for i := range 2 {
			dx[i] *= 2
			dy[i] *= 2
		}
	}
	xy := m.bi[0]
	bw := m.bw
	if intra {
		m.mvs[0][xy] = mv{}
		m.mvs[1][xy] = mv{}
		return
	}
	col := m.d.next.directMV[xy]
	clampMV := func(v, pos, size int) int16 {
		return int16(min(max(v, -60-pos<<6), size<<6-4-pos<<6))
	}
	var v [2]mv
	for dir := range 2 {
		v[dir].x = clampMV(scaleMV(int(col.x), p.bfraction, dir == 1, p.quarter), m.mbx, m.d.mbw)
		v[dir].y = clampMV(scaleMV(int(col.y), p.bfraction, dir == 1, p.quarter), m.mby, m.d.mbh)
	}
	if !direct {
		for dir := range 2 {
			if dir == 0 && mode != bmvForward && mode != bmvInterpolated {
				continue
			}
			if dir == 1 && mode != bmvBackward && mode != bmvInterpolated {
				continue
			}
			mvs := m.mvs[dir]
			if m.mbx == 0 {
				mvs[xy-2] = mv{}
			}
			c := mvs[xy-2]
			var a, b mv
			if !m.firstLine {
				off := 2
				if m.mbx == m.d.mbw-1 {
					off = -2
				}
				a, b = mvs[xy-2*bw], mvs[xy-2*bw+off]
			}
			var px, py int
			switch {
			case !m.firstLine:
				if m.d.mbw == 1 {
					px, py = int(a.x), int(a.y)
				} else {
					px = median3(int(a.x), int(b.x), int(c.x))
					py = median3(int(a.y), int(b.y), int(c.y))
				}
			case m.mbx > 0:
				px, py = int(c.x), int(c.y)
			}
			qx, qy := m.mbx<<6, m.mby<<6
			X, Y := m.d.mbw<<6-4, m.d.mbh<<6-4
			if qx+px < -60 {
				px = -60 - qx
			}
			if qy+py < -60 {
				py = -60 - qy
			}
			if qx+px > X {
				px = X - qx
			}
			if qy+py > Y {
				py = Y - qy
			}
			v[dir] = mv{wrapMV(px+dx[dir], p.rangeX), wrapMV(py+dy[dir], p.rangeY)}
		}
	}
	m.cmv[0][0], m.cmv[1][0] = v[0], v[1]
	m.mvs[0][xy] = v[0]
	m.mvs[1][xy] = v[1]
}

// predMVIntfr predicts the vector of block n of an interlaced frame
// macroblock in direction dir (10.7.5.3), adds the differential and
// stores it; mvn 1 is the macroblock's one vector, 2 a field's (n 0 the
// top's, 2 the bottom's).
func (m *mbContext) predMVIntfr(n, dx, dy, mvn, dir int, intra bool) {
	d := m.d
	p := &d.ph
	bw := m.bw
	xy := m.bi[n]
	mvs := m.mvs[dir]
	if intra {
		m.cmv[0][n] = mv{}
		for _, k := range [4]int{0, 1, bw, bw + 1} {
			if k != 0 && mvn != 1 {
				break
			}
			m.mvs[0][xy+k] = mv{}
			m.mvs[1][xy+k] = mv{}
		}
		if mvn == 1 {
			m.uvMV[m.mbi] = mv{}
		}
		return
	}
	get := func(i int) [2]int { v := mvs[i]; return [2]int{int(v.x), int(v.y)} }
	avg := func(a, b [2]int) [2]int { return [2]int{(a[0] + b[0] + 1) >> 1, (a[1] + b[1] + 1) >> 1} }
	field := m.blkMVType[xy]
	var a, b, c [2]int
	aValid, bValid, cValid := false, false, false
	off := -1
	if n == 0 || n == 1 {
		off = 1
	}
	if m.mbx > 0 || n == 1 || n == 3 {
		if field || !m.blkMVType[xy-1] {
			a = get(xy - 1)
		} else {
			a = avg(get(xy-1), get(xy-1+off*bw))
		}
		aValid = true
		if n&1 == 0 && m.isIntra[m.mbi-1] != 0 {
			aValid = false
			a = [2]int{}
		}
	}
	bi := func(k int) int { return m.bi[k] }
	if n == 0 || n == 1 || field {
		if !m.firstLine {
			if m.isIntra[m.mbi-m.cw] == 0 {
				bValid = true
				nAdj := n | 2
				posB := bi(nAdj) - 2*bw
				if m.blkMVType[posB] && field {
					nAdj = n & 3
				}
				b = get(bi(nAdj) - 2*bw)
				if m.blkMVType[posB] && !field {
					b = avg(b, get(bi(nAdj^2)-2*bw))
				}
			}
			if d.mbw > 1 {
				// Beyond the right edge counts as not intra, as ffmpeg's border.
				right := m.mbx == d.mbw-1 || m.isIntra[m.mbi-m.cw+1] == 0
				if right {
					cValid = true
					nAdj := 2
					posC := bi(2) - 2*bw + 2
					if m.mbx < d.mbw-1 {
						if m.blkMVType[posC] && field {
							nAdj = n & 2
						}
						c = get(bi(nAdj) - 2*bw + 2)
						if m.blkMVType[posC] && !field {
							c = avg(c, get(bi(nAdj^2)-2*bw+2))
						}
					}
					if m.mbx == d.mbw-1 {
						if m.isIntra[m.mbi-m.cw-1] == 0 {
							nAdj = 3
							posC = bi(3) - 2*bw - 2
							if m.blkMVType[posC] && field {
								nAdj = n | 1
							}
							c = get(bi(nAdj) - 2*bw - 2)
							if m.blkMVType[posC] && !field {
								c = avg(c, get(bi(1)-2*bw-2))
							}
						} else {
							cValid = false
						}
					}
				}
			}
		}
	} else {
		bValid, cValid = true, true
		b = get(bi(1))
		c = get(bi(0))
	}
	total := boolInt(aValid) + boolInt(bValid) + boolInt(cValid)
	if m.mbx == 0 && n != 1 && n != 3 {
		a = [2]int{}
	}
	if m.firstLine && (field || n&2 == 0) {
		b, c = [2]int{}, [2]int{}
	}
	var px, py int
	if !field {
		switch {
		case d.mbw == 1:
			px, py = b[0], b[1]
		case total >= 2:
			px, py = median3(a[0], b[0], c[0]), median3(a[1], b[1], c[1])
		case aValid:
			px, py = a[0], a[1]
		case bValid:
			px, py = b[0], b[1]
		case cValid:
			px, py = c[0], c[1]
		}
	} else {
		fa := aValid && a[1]&4 != 0
		fb := bValid && b[1]&4 != 0
		fc := cValid && c[1]&4 != 0
		opp := boolInt(fa) + boolInt(fb) + boolInt(fc)
		same := total - opp
		switch total {
		case 3:
			switch {
			case same == 3 || opp == 3:
				px, py = median3(a[0], b[0], c[0]), median3(a[1], b[1], c[1])
			case same >= opp:
				if !fa {
					px, py = a[0], a[1]
				} else {
					px, py = b[0], b[1]
				}
			default:
				if fa {
					px, py = a[0], a[1]
				} else {
					px, py = b[0], b[1]
				}
			}
		case 2:
			if same >= opp {
				switch {
				case !fa && aValid:
					px, py = a[0], a[1]
				case !fb && bValid:
					px, py = b[0], b[1]
				default:
					px, py = c[0], c[1]
				}
			} else {
				if fa && aValid {
					px, py = a[0], a[1]
				} else {
					px, py = b[0], b[1]
				}
			}
		case 1:
			switch {
			case aValid:
				px, py = a[0], a[1]
			case bValid:
				px, py = b[0], b[1]
			default:
				px, py = c[0], c[1]
			}
		}
	}
	v := mv{wrapMV(px+dx, p.rangeX), wrapMV(py+dy, p.rangeY)}
	m.cmv[dir][n] = v
	mvs[xy] = v
	switch mvn {
	case 1:
		mvs[xy+1], mvs[xy+bw], mvs[xy+bw+1] = v, v, v
	case 2:
		mvs[xy+1] = v
		m.cmv[dir][n+1] = v
	}
}

// predBMVIntfi predicts a field B macroblock's vectors (block n's, or the
// macroblock's when mv1) for its type: direct ones from the next
// reference's co-located vector.
func (m *mbContext) predBMVIntfi(n int, dx, dy [2]int, mv1 bool, predFlag [2]int, mode int) {
	d := m.d
	p := &d.ph
	if mode == bmvDirect {
		var v [2]mv
		f := false
		nxt := d.next
		if !nxt.intraMB[m.mbi+m.mbOff] {
			col := nxt.directMV[m.bi[0]+m.blkOff]
			for dir := range 2 {
				v[dir] = mv{int16(scaleMV(int(col.x), p.bfraction, dir == 1, p.quarter)),
					int16(scaleMV(int(col.y), p.bfraction, dir == 1, p.quarter))}
			}
			opp := 0
			for k := range 4 {
				opp += boolInt(d.mvfNext[0][m.bi[k]+m.blkOff])
			}
			f = opp > 2
		}
		m.cmv[0][0], m.cmv[1][0] = v[0], v[1]
		d.refFieldBottom[0] = d.curFieldBottom != f
		d.refFieldBottom[1] = d.refFieldBottom[0]
		for k := range 4 {
			i := m.bi[k] + m.blkOff
			m.mvs[0][i], m.mvs[1][i] = v[0], v[1]
			m.mvfY[0][i], m.mvfY[1][i] = f, f
		}
		return
	}
	if mode == bmvInterpolated {
		m.predMV(0, dx[0], dy[0], true, predFlag[0], 0, false)
		m.predMV(0, dx[1], dy[1], true, predFlag[1], 1, false)
		return
	}
	dir := boolInt(mode == bmvBackward)
	m.predMV(n, dx[dir], dy[dir], mv1, predFlag[dir], dir, false)
	if n == 3 || mv1 {
		m.predMV(0, dx[1-dir], dy[1-dir], true, 0, 1-dir, false)
	}
}
