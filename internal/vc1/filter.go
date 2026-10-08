package vc1

// Overlap smoothing (8.5) runs on intra blocks' samples before they are
// stored: first across every vertical edge, then across every horizontal
// one (not in interlaced frames). The loop filter (8.6, 10.10) then runs on
// the picture: first across horizontal edges, then vertical ones. Neither
// crosses the top of a slice.

// hOverlap smooths across a vertical edge between left and right (8 lines
// each, lstride and rstride apart): flags 1 alternates the rounding by
// line, 2 starts it the other way.
func hOverlap(left, right []int16, lstride, rstride, flags int) {
	rnd1 := 4
	if flags&2 != 0 {
		rnd1 = 3
	}
	rnd2 := 7 - rnd1
	for i := range 8 {
		l := left[i*lstride:]
		r := right[i*rstride:]
		a, b, c, d := int(l[6]), int(l[7]), int(r[0]), int(r[1])
		d1 := a - d
		d2 := a - d + b - c
		l[6] = int16((a*8 - d1 + rnd1) >> 3)
		l[7] = int16((b*8 - d2 + rnd2) >> 3)
		r[0] = int16((c*8 + d2 + rnd1) >> 3)
		r[1] = int16((d*8 + d1 + rnd2) >> 3)
		if flags&1 != 0 {
			rnd1, rnd2 = 7-rnd1, 7-rnd2
		}
	}
}

// vOverlap smooths across a horizontal edge between top and bottom.
func vOverlap(top, bottom []int16) {
	rnd1, rnd2 := 4, 3
	for i := range 8 {
		a, b, c, d := int(top[48+i]), int(top[56+i]), int(bottom[i]), int(bottom[8+i])
		d1 := a - d
		d2 := a - d + b - c
		top[48+i] = int16((a*8 - d1 + rnd1) >> 3)
		top[56+i] = int16((b*8 - d2 + rnd2) >> 3)
		bottom[i] = int16((c*8 + d2 + rnd1) >> 3)
		bottom[8+i] = int16((d*8 + d1 + rnd2) >> 3)
		rnd1, rnd2 = 7-rnd1, 7-rnd2
	}
}

// slotAt is a macroblock's blocks from slot s on (with off more).
func slotAt(b *[6 * 64]int16, s, off int) []int16 { return b[s*64+off:] }

// hOverlapEdge smooths vertical edge i of a macroblock (0 and 2 its left
// edge's top and bottom, 1 and 3 its middle's, 4 and 5 its chroma's left)
// between left and cur: with field transforms (lf, rf), the lines of each
// field are smoothed with each other.
func hOverlapEdge(left, cur *[6 * 64]int16, lf, rf bool, i int) {
	ls, rs := 8, 8
	if lf != rf {
		ls, rs = 16-8*boolInt(lf), 16-8*boolInt(rf)
	}
	switch i {
	case 0:
		fl := 1
		if lf || rf {
			fl = 0
		}
		hOverlap(slotAt(left, 2, 0), slotAt(cur, 0, 0), ls, rs, fl)
	case 1:
		fl := 1
		if rf {
			fl = 0
		}
		hOverlap(slotAt(cur, 0, 0), slotAt(cur, 2, 0), 8, 8, fl)
	case 2:
		fl := 1
		if lf || rf {
			fl = 2
		}
		l := slotAt(left, 3, 0)
		if !lf && rf {
			l = slotAt(left, 2, 8)
		}
		r := slotAt(cur, 1, 0)
		if lf && !rf {
			r = slotAt(cur, 0, 8)
		}
		hOverlap(l, r, ls, rs, fl)
	case 3:
		fl := 1
		if rf {
			fl = 2
		}
		hOverlap(slotAt(cur, 1, 0), slotAt(cur, 3, 0), 8, 8, fl)
	default:
		hOverlap(slotAt(left, i, 0), slotAt(cur, i, 0), 8, 8, 1)
	}
}

// vOverlapEdge smooths horizontal edge i of a macroblock (0 and 1 its
// top's, 2 and 3 its middle's, 4 and 5 its chroma's top).
func vOverlapEdge(top, cur *[6 * 64]int16, i int) {
	switch i {
	case 0:
		vOverlap(slotAt(top, 1, 0), slotAt(cur, 0, 0))
	case 1:
		vOverlap(slotAt(top, 3, 0), slotAt(cur, 2, 0))
	case 2:
		vOverlap(slotAt(cur, 0, 0), slotAt(cur, 1, 0))
	case 3:
		vOverlap(slotAt(cur, 2, 0), slotAt(cur, 3, 0))
	default:
		vOverlap(slotAt(top, i, 0), slotAt(cur, i, 0))
	}
}

// overlap smooths the intra blocks of the picture: an I one's (all
// edges, or where CONDOVER says) or a P one's (between intra blocks).
func (m *mbContext) overlap() {
	d := m.d
	p := &d.ph
	if !d.ep.overlap {
		return
	}
	intraPic := p.typ == picI || p.typ == picBI
	if intraPic && p.pq < 9 && p.condover == condOverNone || !intraPic && p.pq < 9 {
		return
	}
	mbw := d.mbw
	flag := func(x, y int) bool { return d.overFlags[y*mbw+x] != 0 }
	all := p.pq >= 9 || p.condover == condOverAll
	ilace := p.fcm == ilaceFrame
	ftx := func(x, y int) bool { return ilace && d.fieldTX[y*mbw+x] != 0 }
	for y := range m.rows {
		for x := range mbw {
			cur := &m.blocks[y*mbw+x]
			var left *[6 * 64]int16
			if x > 0 {
				left = &m.blocks[y*mbw+x-1]
			}
			m.at(x, y)
			for i := range 6 {
				if x == 0 && i&5 != 1 {
					continue
				}
				var smooth bool
				if intraPic {
					smooth = all || flag(x, y) && (i&5 == 1 || flag(x-1, y))
				} else {
					var nb bool
					switch i {
					case 0:
						nb = m.intraY[m.bi[0]-1]
					case 1:
						nb = m.intraY[m.bi[0]]
					case 2:
						nb = m.intraY[m.bi[2]-1]
					case 3:
						nb = m.intraY[m.bi[2]]
					default:
						nb = m.intraC[m.mbi-1]
					}
					smooth = m.intra(i) && nb
				}
				if !smooth {
					continue
				}
				if left == nil {
					left = cur
				}
				hOverlapEdge(left, cur, x > 0 && ftx(x-1, y), ftx(x, y), i)
			}
		}
	}
	if ilace {
		return
	}
	for y := range m.rows {
		for x := range mbw {
			cur := &m.blocks[y*mbw+x]
			var top *[6 * 64]int16
			if y > 0 {
				top = &m.blocks[(y-1)*mbw+x]
			}
			m.at(x, y)
			for i := range 6 {
				if m.sliceTop[y] && i&2 == 0 {
					continue
				}
				var smooth bool
				if intraPic {
					smooth = all || flag(x, y) && (i&2 != 0 || flag(x, y-1))
				} else if i < 4 {
					smooth = m.intraY[m.bi[i]] && m.intraY[m.bi[i]-m.bw]
				} else {
					smooth = m.intraC[m.mbi] && m.intraC[m.mbi-m.cw]
				}
				if smooth {
					vOverlapEdge(top, cur, i)
				}
			}
		}
	}
}

// putIntra stores the intra blocks' samples (P and I pictures', which
// waited for overlap smoothing).
func (m *mbContext) putIntra() {
	d := m.d
	ilace := d.ph.fcm == ilaceFrame
	for y := range m.rows {
		for x := range d.mbw {
			m.at(x, y)
			blocks := &m.blocks[y*d.mbw+x]
			ftx := ilace && d.fieldTX[y*d.mbw+x] != 0
			for k := range 6 {
				if !m.intra(k) {
					continue
				}
				dst, stride := m.blockDst(k, ftx)
				putSigned(dst, stride, block(blocks, k))
			}
		}
	}
}

// filterLine filters across an edge at one line (src[i] the first sample
// after the edge, step the distance across it), giving whether the other
// three lines of its group are to be filtered too.
func filterLine(src []byte, i, step, pq int) bool {
	m2, m1 := int(src[i-2*step]), int(src[i-step])
	p0, p1 := int(src[i]), int(src[i+step])
	a0 := (2*(m2-p1) - 5*(m1-p0) + 4) >> 3
	a0neg := a0 < 0
	if a0neg {
		a0 = -a0
	}
	if a0 >= pq {
		return false
	}
	m4, m3 := int(src[i-4*step]), int(src[i-3*step])
	p2, p3 := int(src[i+2*step]), int(src[i+3*step])
	a1 := iabs((2*(m4-m1) - 5*(m3-m2) + 4) >> 3)
	a2 := iabs((2*(p0-p3) - 5*(p1-p2) + 4) >> 3)
	if a1 >= a0 && a2 >= a0 {
		return false
	}
	clip := m1 - p0
	clipNeg := clip < 0
	if clipNeg {
		clip = -clip
	}
	clip >>= 1
	if clip == 0 {
		return false
	}
	dd := (5 * (a0 - min(a1, a2))) >> 3
	if a0neg != clipNeg {
		dd = min(dd, clip)
		if clipNeg {
			dd = -dd
		}
		src[i-step] = clip8(m1 - dd)
		src[i] = clip8(p0 + dd)
	}
	return true
}

// loopFilter filters n lines across an edge, four at a time: the third
// of each four decides for the others. along is the distance between
// lines, across the distance across the edge.
func loopFilter(src []byte, i, along, across, n, pq int) {
	for k := 0; k < n; k += 4 {
		o := i + k*along
		if filterLine(src, o+2*along, across, pq) {
			filterLine(src, o, across, pq)
			filterLine(src, o+along, across, pq)
			filterLine(src, o+3*along, across, pq)
		}
	}
}

// vFilter filters the horizontal edge above line y, n samples from x on,
// lines lines apart (2: within a field of an interlaced frame); hFilter
// the vertical edge left of x, n lines from y on.
func vFilter(pl []byte, stride, x, y, n, lines, pq int) {
	loopFilter(pl, y*stride+x, 1, lines*stride, n, pq)
}

func hFilter(pl []byte, stride, x, y, n, lines, pq int) {
	loopFilter(pl, y*stride+x, lines*stride, 1, n, pq)
}

// edgeBelow reports whether the bottom of macroblock row y ends a slice
// (or the picture).
func (m *mbContext) edgeBelow(y int) bool { return y == m.rows-1 || m.sliceTop[y+1] }

// loopFilterIntra filters every block edge but the picture's and slices'
// tops and the picture's left (I, BI and progressive B pictures).
func (m *mbContext) loopFilterIntra() {
	d := m.d
	pq := d.ph.pq
	sy, sc := m.vsy, m.vsc
	ilace := d.ph.fcm == ilaceFrame
	for y := range m.rows {
		for x := range d.mbw {
			top := !m.sliceTop[y]
			if !ilace {
				if top {
					vFilter(m.vy, sy, x*16, y*16, 16, 1, pq)
					vFilter(m.vcb, sc, x*8, y*8, 8, 1, pq)
					vFilter(m.vcr, sc, x*8, y*8, 8, 1, pq)
				}
				vFilter(m.vy, sy, x*16, y*16+8, 16, 1, pq)
				continue
			}
			ftx := d.fieldTX[y*d.mbw+x] != 0
			if top {
				for f := range 2 {
					vFilter(m.vy, sy, x*16, y*16+f, 16, 2, pq)
				}
				for f := range 2 {
					vFilter(m.vcb, sc, x*8, y*8+f, 8, 2, pq)
					vFilter(m.vcr, sc, x*8, y*8+f, 8, 2, pq)
				}
			}
			if !ftx {
				for f := range 2 {
					vFilter(m.vy, sy, x*16, y*16+8+f, 16, 2, pq)
				}
			}
		}
	}
	for y := range m.rows {
		for x := range d.mbw {
			for _, e := range [2]int{0, 8} {
				if e == 0 && x == 0 {
					continue
				}
				if ilace {
					for f := range 2 {
						hFilter(m.vy, sy, x*16+e, y*16+f, 8, 2, pq)
					}
				} else {
					hFilter(m.vy, sy, x*16+e, y*16, 16, 1, pq)
				}
			}
			if x > 0 {
				for _, pl := range [2][]byte{m.vcb, m.vcr} {
					if ilace {
						for f := range 2 {
							hFilter(pl, sc, x*8, y*8+f, 4, 2, pq)
						}
					} else {
						hFilter(pl, sc, x*8, y*8, 8, 1, pq)
					}
				}
			}
		}
	}
}

// blockPlane gives block b of macroblock (x, y): its plane, stride and
// top left.
func (m *mbContext) blockPlane(x, y, b int) ([]byte, int, int, int) {
	switch b {
	case 4:
		return m.vcb, m.vsc, x * 8, y * 8
	case 5:
		return m.vcr, m.vsc, x * 8, y * 8
	}
	return m.vy, m.vsy, x*16 + (b&1)*8, y*16 + (b&2)*4
}

// loopFilterP filters a progressive or field P picture's edges: those of
// intra blocks and between blocks of different vectors (or reference
// fields) whole, others where either side has coefficients, and the inner
// edges of 8x4, 4x8 and 4x4 transforms.
func (m *mbContext) loopFilterP() {
	d := m.d
	pq := d.ph.pq
	field := d.ph.fieldMode
	mbw := d.mbw
	bo, mo := m.blkOff, m.mbOff
	for y := range m.rows {
		bottom := m.edgeBelow(y)
		for x := range mbw {
			m.at(x, y)
			mbi := m.mbi
			for b := range 6 {
				pl, stride, bx, by := m.blockPlane(x, y, b)
				topCBP := m.cbp[mbi] >> (4 * b)
				if !bottom || b < 2 {
					topIntra := m.isIntra[mbi]&(1<<b) != 0
					var botIntra, differ bool
					var botCBP uint32
					switch {
					case b > 3:
						botIntra = m.isIntra[mbi+m.cw]&(1<<b) != 0
						botCBP = m.cbp[mbi+m.cw] >> (4 * b)
						differ = m.uvMV[mbi] != m.uvMV[mbi+m.cw] ||
							field && m.mvfC[0][mbi+mo] != m.mvfC[0][mbi+m.cw+mo]
					default:
						nb := b + 2
						if b >= 2 {
							nb = b - 2
							botIntra = m.isIntra[mbi+m.cw]&(1<<nb) != 0
							botCBP = m.cbp[mbi+m.cw] >> (4 * nb)
						} else {
							botIntra = m.isIntra[mbi]&(1<<nb) != 0
							botCBP = m.cbp[mbi] >> (4 * nb)
						}
						i := m.bi[b] + bo
						differ = m.mvs[0][i] != m.mvs[0][i+m.bw] ||
							field && m.mvfY[0][i] != m.mvfY[0][i+m.bw]
					}
					if topIntra || botIntra || differ {
						vFilter(pl, stride, bx, by+8, 8, 1, pq)
					} else {
						idx := (topCBP | botCBP>>2) & 3
						if idx&1 != 0 {
							vFilter(pl, stride, bx+4, by+8, 4, 1, pq)
						}
						if idx&2 != 0 {
							vFilter(pl, stride, bx, by+8, 4, 1, pq)
						}
					}
				}
				tt := m.tt[mbi] >> (4 * b) & 0xf
				if tt == tt4x4 || tt == tt8x4 {
					if topCBP&5 != 0 {
						vFilter(pl, stride, bx+4, by+4, 4, 1, pq)
					}
					if topCBP&10 != 0 {
						vFilter(pl, stride, bx, by+4, 4, 1, pq)
					}
				}
			}
		}
	}
	for y := range m.rows {
		for x := range mbw {
			m.at(x, y)
			mbi := m.mbi
			right := x == mbw-1
			for b := range 6 {
				pl, stride, bx, by := m.blockPlane(x, y, b)
				leftCBP := m.cbp[mbi] >> (4 * b)
				if !right || b&5 == 0 {
					leftIntra := m.isIntra[mbi]&(1<<b) != 0
					var rightIntra, differ bool
					var rightCBP uint32
					switch {
					case b > 3:
						rightIntra = m.isIntra[mbi+1]&(1<<b) != 0
						rightCBP = m.cbp[mbi+1] >> (4 * b)
						differ = m.uvMV[mbi] != m.uvMV[mbi+1] ||
							field && m.mvfC[0][mbi+mo] != m.mvfC[0][mbi+1+mo]
					default:
						if b&1 != 0 {
							rightIntra = m.isIntra[mbi+1]&(1<<(b-1)) != 0
							rightCBP = m.cbp[mbi+1] >> (4 * (b - 1))
						} else {
							rightIntra = m.isIntra[mbi]&(1<<(b+1)) != 0
							rightCBP = m.cbp[mbi] >> (4 * (b + 1))
						}
						i := m.bi[b] + bo
						differ = m.mvs[0][i] != m.mvs[0][i+1] ||
							field && m.mvfY[0][i] != m.mvfY[0][i+1]
					}
					if leftIntra || rightIntra || differ {
						hFilter(pl, stride, bx+8, by, 8, 1, pq)
					} else {
						idx := (leftCBP | rightCBP>>1) & 5
						if idx&1 != 0 {
							hFilter(pl, stride, bx+8, by+4, 4, 1, pq)
						}
						if idx&4 != 0 {
							hFilter(pl, stride, bx+8, by, 4, 1, pq)
						}
					}
				}
				tt := m.tt[mbi] >> (4 * b) & 0xf
				if tt == tt4x4 || tt == tt4x8 {
					if leftCBP&3 != 0 {
						hFilter(pl, stride, bx+4, by+4, 4, 1, pq)
					}
					if leftCBP&12 != 0 {
						hFilter(pl, stride, bx+4, by, 4, 1, pq)
					}
				}
			}
		}
	}
}

// loopFilterIntfr filters an interlaced frame P or B picture: every edge,
// within each field, and the inner edges of transforms.
func (m *mbContext) loopFilterIntfr() {
	d := m.d
	pq := d.ph.pq
	mbw := d.mbw
	for y := range m.rows {
		bottom := m.edgeBelow(y)
		top := m.sliceTop[y]
		for x := range mbw {
			mbi := (y+1)*m.cw + x + 1
			ftx := d.fieldTX[y*mbw+x] != 0
			for b := range 6 {
				pl, stride, bx, by := m.blockPlane(x, y, b)
				tt := m.tt[mbi] >> (4 * b) & 0xf
				inner := tt == tt4x4 || tt == tt8x4
				switch {
				case b > 3:
					if !bottom {
						if !top && inner {
							vFilter(pl, stride, bx, by+4, 8, 2, pq)
							vFilter(pl, stride, bx, by+5, 8, 2, pq)
						}
						vFilter(pl, stride, bx, by+8, 8, 2, pq)
						vFilter(pl, stride, bx, by+9, 8, 2, pq)
					}
				case ftx && b < 2:
					if inner {
						vFilter(pl, stride, bx, by+8, 8, 2, pq)
					}
					if !bottom {
						vFilter(pl, stride, bx, by+16, 8, 2, pq)
					}
				case ftx:
					if inner {
						vFilter(pl, stride, bx, by+1, 8, 2, pq)
					}
					if !bottom {
						vFilter(pl, stride, bx, by+9, 8, 2, pq)
					}
				case b < 2:
					if !top && inner {
						vFilter(pl, stride, bx, by+4, 8, 2, pq)
						vFilter(pl, stride, bx, by+5, 8, 2, pq)
					}
					vFilter(pl, stride, bx, by+8, 8, 2, pq)
					vFilter(pl, stride, bx, by+9, 8, 2, pq)
				case !bottom:
					if inner {
						vFilter(pl, stride, bx, by+4, 8, 2, pq)
						vFilter(pl, stride, bx, by+5, 8, 2, pq)
					}
					vFilter(pl, stride, bx, by+8, 8, 2, pq)
					vFilter(pl, stride, bx, by+9, 8, 2, pq)
				}
			}
		}
	}
	for y := range m.rows {
		for x := range mbw {
			mbi := (y+1)*m.cw + x + 1
			ftx := d.fieldTX[y*mbw+x] != 0
			right := x == mbw-1
			for b := range 6 {
				pl, stride, bx, by := m.blockPlane(x, y, b)
				tt := m.tt[mbi] >> (4 * b) & 0xf
				inner := tt == tt4x4 || tt == tt4x8
				switch {
				case b < 4 && ftx:
					fy := by
					if b >= 2 {
						fy = by - 7
					}
					if inner {
						hFilter(pl, stride, bx+4, fy, 8, 2, pq)
					}
					if !right || b == 0 || b == 2 {
						hFilter(pl, stride, bx+8, fy, 8, 2, pq)
					}
				case b < 4:
					if inner {
						hFilter(pl, stride, bx+4, by, 4, 2, pq)
						hFilter(pl, stride, bx+4, by+1, 4, 2, pq)
					}
					if !right || b&5 == 0 {
						hFilter(pl, stride, bx+8, by, 4, 2, pq)
						hFilter(pl, stride, bx+8, by+1, 4, 2, pq)
					}
				default:
					if inner {
						hFilter(pl, stride, bx+4, by, 4, 2, pq)
						hFilter(pl, stride, bx+4, by+1, 4, 2, pq)
					}
					if !right {
						hFilter(pl, stride, bx+8, by, 4, 2, pq)
						hFilter(pl, stride, bx+8, by+1, 4, 2, pq)
					}
				}
			}
		}
	}
}

// loopFilterBIntfi filters a field B picture: every block edge, and the
// inner edges of transforms where coded.
func (m *mbContext) loopFilterBIntfi() {
	d := m.d
	pq := d.ph.pq
	mbw := d.mbw
	for y := range m.rows {
		bottom := m.edgeBelow(y)
		for x := range mbw {
			mbi := (y+1)*m.cw + x + 1
			for b := range 6 {
				pl, stride, bx, by := m.blockPlane(x, y, b)
				if !bottom || b < 2 {
					vFilter(pl, stride, bx, by+8, 8, 1, pq)
				}
				cbp := m.cbp[mbi] >> (4 * b)
				tt := m.tt[mbi] >> (4 * b) & 0xf
				if tt == tt4x4 || tt == tt8x4 {
					idx := (cbp | cbp>>2) & 3
					if idx&1 != 0 {
						vFilter(pl, stride, bx+4, by+4, 4, 1, pq)
					}
					if idx&2 != 0 {
						vFilter(pl, stride, bx, by+4, 4, 1, pq)
					}
				}
			}
		}
	}
	for y := range m.rows {
		for x := range mbw {
			mbi := (y+1)*m.cw + x + 1
			right := x == mbw-1
			for b := range 6 {
				pl, stride, bx, by := m.blockPlane(x, y, b)
				if !right || b&5 == 0 {
					hFilter(pl, stride, bx+8, by, 8, 1, pq)
				}
				cbp := m.cbp[mbi] >> (4 * b)
				tt := m.tt[mbi] >> (4 * b) & 0xf
				if tt == tt4x4 || tt == tt4x8 {
					idx := (cbp | cbp>>1) & 5
					if idx&1 != 0 {
						hFilter(pl, stride, bx+4, by+4, 4, 1, pq)
					}
					if idx&4 != 0 {
						hFilter(pl, stride, bx+4, by, 4, 1, pq)
					}
				}
			}
		}
	}
}
