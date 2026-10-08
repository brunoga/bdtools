package mvc

// Deblocking filter (8.7).

var alphaTab = [52]uint8{
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 4, 4, 5, 6, 7, 8, 9, 10, 12, 13,
	15, 17, 20, 22, 25, 28, 32, 36, 40, 45, 50, 56, 63, 71, 80, 90, 101, 113, 127, 144, 162, 182, 203, 226, 255, 255,
}
var betaTab = [52]uint8{
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2, 2, 2, 3, 3, 3, 3, 4, 4, 4,
	6, 6, 7, 7, 8, 8, 9, 9, 10, 10, 11, 11, 12, 12, 13, 13, 14, 14, 15, 15, 16, 16, 17, 17, 18, 18,
}
var tc0Tab = [52][3]uint8{
	{}, {}, {}, {}, {}, {}, {}, {}, {}, {}, {}, {}, {}, {}, {}, {}, {},
	{0, 0, 1}, {0, 0, 1}, {0, 0, 1}, {0, 0, 1}, {0, 1, 1}, {0, 1, 1}, {1, 1, 1}, {1, 1, 1}, {1, 1, 1}, {1, 1, 1},
	{1, 1, 2}, {1, 1, 2}, {1, 1, 2}, {1, 1, 2}, {1, 2, 3}, {1, 2, 3}, {2, 2, 3}, {2, 2, 4}, {2, 3, 4}, {2, 3, 4},
	{3, 3, 5}, {3, 4, 6}, {3, 4, 6}, {4, 5, 7}, {4, 5, 8}, {4, 6, 9}, {5, 7, 10}, {6, 8, 11}, {6, 8, 13}, {7, 10, 14},
	{8, 11, 16}, {9, 12, 18}, {10, 13, 20}, {11, 15, 23}, {13, 17, 25},
}

func iabs(x int32) int32 {
	if x < 0 {
		return -x
	}
	return x
}

func clip3(lo, hi, v int32) int32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// filterLuma filters a 16-sample luma edge. step is the offset from p0 to
// q0, along the offset between successive lines.
func filterLumaGeneric(pl []byte, off, step, along int, bs *[4]uint8, alpha, beta int32, indexA int) {
	for i := 0; i < 16; i++ {
		b := bs[i>>2]
		if b == 0 {
			continue
		}
		o := off + i*along
		p0 := int32(pl[o-step])
		q0 := int32(pl[o])
		if iabs(p0-q0) >= alpha {
			continue
		}
		p1 := int32(pl[o-2*step])
		q1 := int32(pl[o+step])
		if iabs(p1-p0) >= beta || iabs(q1-q0) >= beta {
			continue
		}
		p2 := int32(pl[o-3*step])
		q2 := int32(pl[o+2*step])
		ap := iabs(p2 - p0)
		aq := iabs(q2 - q0)
		if b < 4 {
			tc0 := int32(tc0Tab[indexA][b-1])
			tc := tc0
			if ap < beta {
				tc++
			}
			if aq < beta {
				tc++
			}
			d := clip3(-tc, tc, ((q0-p0)<<2+(p1-q1)+4)>>3)
			pl[o-step] = clip255(p0 + d)
			pl[o] = clip255(q0 - d)
			if ap < beta {
				pl[o-2*step] = byte(p1 + clip3(-tc0, tc0, (p2+((p0+q0+1)>>1)-(p1<<1))>>1))
			}
			if aq < beta {
				pl[o+step] = byte(q1 + clip3(-tc0, tc0, (q2+((p0+q0+1)>>1)-(q1<<1))>>1))
			}
		} else {
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
	}
}

// filterChroma filters an 8-sample chroma edge.
func filterChromaGeneric(pl []byte, off, step, along int, bs *[4]uint8, alpha, beta int32, indexA int) {
	for i := 0; i < 8; i++ {
		b := bs[i>>1]
		if b == 0 {
			continue
		}
		o := off + i*along
		p0 := int32(pl[o-step])
		q0 := int32(pl[o])
		if iabs(p0-q0) >= alpha {
			continue
		}
		p1 := int32(pl[o-2*step])
		q1 := int32(pl[o+step])
		if iabs(p1-p0) >= beta || iabs(q1-q0) >= beta {
			continue
		}
		if b < 4 {
			tc := int32(tc0Tab[indexA][b-1]) + 1
			d := clip3(-tc, tc, ((q0-p0)<<2+(p1-q1)+4)>>3)
			pl[o-step] = clip255(p0 + d)
			pl[o] = clip255(q0 - d)
		} else {
			pl[o-step] = byte((2*p1 + p0 + q1 + 2) >> 2)
			pl[o] = byte((2*q1 + q0 + p1 + 2) >> 2)
		}
	}
}

func mvDiff(a, b mv) bool {
	dx := int32(a.x) - int32(b.x)
	dy := int32(a.y) - int32(b.y)
	return dx >= 4 || dx <= -4 || dy >= 4 || dy <= -4
}

// motionBS returns 1 if the two inter 4x4 blocks have different motion.
func (fc *frameCtx) motionBS(p, q int) uint8 {
	pic := fc.pic
	p0, p1 := fc.refIDs[0][p], fc.refIDs[1][p]
	q0, q1 := fc.refIDs[0][q], fc.refIDs[1][q]
	if p1 < 0 && q1 < 0 {
		// the common single-reference case
		if p0 != q0 || p0 < 0 || mvDiff(pic.mvs[0][p], pic.mvs[0][q]) {
			return 1
		}
		return 0
	}
	np := 0
	if p0 >= 0 {
		np++
	}
	if p1 >= 0 {
		np++
	}
	nq := 0
	if q0 >= 0 {
		nq++
	}
	if q1 >= 0 {
		nq++
	}
	if np != nq {
		return 1
	}
	if np == 1 {
		var rp, rq int32
		var mp, mq mv
		if p0 >= 0 {
			rp, mp = p0, pic.mvs[0][p]
		} else {
			rp, mp = p1, pic.mvs[1][p]
		}
		if q0 >= 0 {
			rq, mq = q0, pic.mvs[0][q]
		} else {
			rq, mq = q1, pic.mvs[1][q]
		}
		if rp != rq || mvDiff(mp, mq) {
			return 1
		}
		return 0
	}
	if np == 0 {
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

// deblockRow filters all macroblocks of MB row y.
func (fc *frameCtx) deblockRow(y int) {
	for x := 0; x < fc.mbW; x++ {
		fc.deblockMB(x, y)
	}
}

func (fc *frameCtx) deblockMB(mbX, mbY int) {
	addr := mbY*fc.mbW + mbX
	cur := &fc.mbs[addr]
	if cur.flags&mbfAvail == 0 {
		return
	}
	sp := &fc.slices[cur.slice]
	if sp.disableDeblock == 1 {
		return
	}
	pic := fc.pic
	st4 := fc.mbW * 4
	base4 := mbY*4*st4 + mbX*4
	curIntra := cur.flags&mbfIntra != 0
	t8 := cur.flags&mbfT8x8 != 0

	filterLeft := mbX > 0
	filterTop := mbY > 0
	if filterLeft {
		l := &fc.mbs[addr-1]
		if l.flags&mbfAvail == 0 || (sp.disableDeblock == 2 && l.slice != cur.slice) {
			filterLeft = false
		}
	}
	if filterTop {
		t := &fc.mbs[addr-fc.mbW]
		if t.flags&mbfAvail == 0 || (sp.disableDeblock == 2 && t.slice != cur.slice) {
			filterTop = false
		}
	}

	// Nothing is filtered when alpha or beta is 0 on every edge, which the
	// largest QP among the macroblock and its filtered neighbours decides
	// (the thresholds are monotone in QP and 0 below index 16).
	if mx := int(cur.qp); mx+sp.alphaOffset < 16 || mx+sp.betaOffset < 16 {
		if filterLeft {
			mx = max(mx, int(fc.mbs[addr-1].qp))
		}
		if filterTop {
			mx = max(mx, int(fc.mbs[addr-fc.mbW].qp))
		}
		skip := mx+sp.alphaOffset < 16 || mx+sp.betaOffset < 16
		if fc.sps.chromaFormatIdc != 0 {
			for c := 0; c < 2; c++ {
				qc := chromaQP(mx, sp.chromaQPOffset[c])
				if qc+sp.alphaOffset >= 16 && qc+sp.betaOffset >= 16 {
					skip = false
				}
			}
		}
		if skip {
			return
		}
	}

	if fc.deblockMBFast(mbX, mbY, cur, sp, filterLeft, filterTop) {
		return
	}

	// boundary strengths: [dir][edge][segment]. The residual condition
	// (bS 2) of all four segments of an edge comes from the nonzero-block
	// masks of the two macroblocks with a couple of shifts; motion is only
	// compared across edges that can separate different motion.
	var bs [2][4][4]uint8
	nzq := uint32(cur.nzMask)
	for dir := 0; dir < 2; dir++ {
		for e := 0; e < 4; e++ {
			if e == 0 {
				if dir == 0 && !filterLeft || dir == 1 && !filterTop {
					continue
				}
			} else if t8 && e&1 != 0 {
				continue
			}
			nb := cur
			if e == 0 {
				if dir == 0 {
					nb = &fc.mbs[addr-1]
				} else {
					nb = &fc.mbs[addr-fc.mbW]
				}
			}
			if curIntra || nb.flags&mbfIntra != 0 {
				v := uint8(3)
				if e == 0 {
					v = 4
				}
				bs[dir][e] = [4]uint8{v, v, v, v}
				continue
			}
			// pm: bit k set where segment k has residual on either side
			var pm uint32
			if dir == 0 {
				if e == 0 {
					pm = nzq | uint32(nb.nzMask)>>3
				} else {
					pm = nzq>>e | nzq>>(e-1)
				}
				pm &= 0x1111
				pm = (pm | pm>>3 | pm>>6 | pm>>9) & 15
			} else {
				if e == 0 {
					pm = nzq | uint32(nb.nzMask)>>12
				} else {
					pm = nzq>>(4*e) | nzq>>(4*(e-1))
				}
				pm &= 15
			}
			mvCheck := e == 0 || cur.mvEdges>>(dir*4+e-1)&1 != 0
			if !mvCheck {
				for k := 0; k < 4; k++ {
					bs[dir][e][k] = uint8(pm>>k&1) << 1
				}
				continue
			}
			// across the boundary of two uniform macroblocks all four
			// segments compare the same motion
			sameAll := e == 0 && cur.mvEdges == 0 && nb.mvEdges == 0
			var mbs uint8
			mbsValid := false
			for k := 0; k < 4; k++ {
				switch {
				case pm>>k&1 != 0:
					bs[dir][e][k] = 2
				case sameAll && mbsValid:
					bs[dir][e][k] = mbs
				default:
					var p, q int
					if dir == 0 {
						q = base4 + k*st4 + e
						p = q - 1
					} else {
						q = base4 + e*st4 + k
						p = q - st4
					}
					mbs, mbsValid = fc.motionBS(p, q), true
					bs[dir][e][k] = mbs
				}
			}
		}
	}

	qp := int32(cur.qp)
	stY := pic.stride[0]
	yo := pic.origin[0] + mbY*16*stY + mbX*16
	stC := pic.stride[1]
	co := pic.origin[1] + mbY*8*stC + mbX*8
	chroma := fc.sps.chromaFormatIdc != 0
	// filter parameters: the same for all internal edges, and per
	// neighbour for the two macroblock boundary edges
	type edgeParams struct {
		alpha, beta int32
		ia          int
		ca, cb      [2]int32
		cia         [2]int
	}
	params := func(qpp int32) (ep edgeParams) {
		qav := (qp + qpp + 1) >> 1
		ep.ia = int(clip3(0, 51, qav+int32(sp.alphaOffset)))
		ib := int(clip3(0, 51, qav+int32(sp.betaOffset)))
		ep.alpha, ep.beta = int32(alphaTab[ep.ia]), int32(betaTab[ib])
		if chroma {
			for c := 0; c < 2; c++ {
				qc := int32(chromaQP(int(qp), sp.chromaQPOffset[c]))
				qcp := int32(chromaQP(int(qpp), sp.chromaQPOffset[c]))
				qav := (qc + qcp + 1) >> 1
				ep.cia[c] = int(clip3(0, 51, qav+int32(sp.alphaOffset)))
				ib := int(clip3(0, 51, qav+int32(sp.betaOffset)))
				ep.ca[c], ep.cb[c] = int32(alphaTab[ep.cia[c]]), int32(betaTab[ib])
			}
		}
		return
	}
	inner := params(qp)
	for dir := 0; dir < 2; dir++ {
		for e := 0; e < 4; e++ {
			b := &bs[dir][e]
			if b[0]|b[1]|b[2]|b[3] == 0 {
				continue
			}
			ep := &inner
			var edge edgeParams
			if e == 0 {
				if dir == 0 {
					edge = params(int32(fc.mbs[addr-1].qp))
				} else {
					edge = params(int32(fc.mbs[addr-fc.mbW].qp))
				}
				ep = &edge
			}
			if dir == 0 {
				filterLuma(pic.planes[0], yo+e*4, 1, stY, b, ep.alpha, ep.beta, ep.ia)
			} else {
				filterLuma(pic.planes[0], yo+e*4*stY, stY, 1, b, ep.alpha, ep.beta, ep.ia)
			}
			// chroma edges at luma edges 0 and 2
			if !chroma || e&1 != 0 {
				continue
			}
			if dir == 0 {
				filterChroma2(pic.planes[1], pic.planes[2], co+e*2, 1, stC, b, ep.ca, ep.cb, ep.cia)
			} else {
				filterChroma2(pic.planes[1], pic.planes[2], co+e*2*stC, stC, 1, b, ep.ca, ep.cb, ep.cia)
			}
		}
	}
}

// filterChroma2Generic filters the same edge of both chroma planes.
func filterChroma2Generic(cb, cr []byte, off, step, along int, bs *[4]uint8, alpha, beta [2]int32, indexA [2]int) {
	filterChromaGeneric(cb, off, step, along, bs, alpha[0], beta[0], indexA[0])
	filterChromaGeneric(cr, off, step, along, bs, alpha[1], beta[1], indexA[1])
}
