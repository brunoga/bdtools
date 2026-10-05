package mvc

// Motion vector prediction (8.4.1).

// blkOrder is the decoding order of 4x4 blocks by raster position.
var blkOrder = [16]uint8{0, 1, 4, 5, 2, 3, 6, 7, 8, 9, 12, 13, 10, 11, 14, 15}

// nb returns refIdx, mv and availability of the 4x4 block at (x,y)
// relative to the current macroblock for list l.
func (s *sliceDec) nb(l, x, y int) (int8, mv, bool) {
	if y < 0 {
		if x < 0 {
			if !s.availD {
				return -1, mv{}, false
			}
		} else if x >= 4 {
			if !s.availC {
				return -1, mv{}, false
			}
		} else if !s.availB {
			return -1, mv{}, false
		}
	} else if x < 0 {
		if !s.availA {
			return -1, mv{}, false
		}
	} else if x >= 4 {
		return -1, mv{}, false
	}
	i := s.idx4(x, y)
	return s.pic.refs[l][i], s.pic.mvs[l][i], true
}

// nbC returns neighbour C (or D when C is unavailable) for a partition.
func (s *sliceDec) nbC(l, x, y, w int) (int8, mv, bool) {
	cx, cy := x+w, y-1
	if cy >= 0 && (cx >= 4 || blkOrder[cy*4+cx] > blkOrder[y*4+x]) {
		return s.nb(l, x-1, y-1)
	}
	r, m, a := s.nb(l, cx, cy)
	if !a {
		return s.nb(l, x-1, y-1)
	}
	return r, m, a
}

func median3(a, b, c int16) int16 {
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

// mvPred derives the motion vector predictor for a partition with top-left
// 4x4 block (x,y) and size (w,h) in 4x4 units.
func (s *sliceDec) mvPred(l int, ref int8, x, y, w, h int) mv {
	rA, mA, aA := s.nb(l, x-1, y)
	rB, mB, aB := s.nb(l, x, y-1)
	rC, mC, aC := s.nbC(l, x, y, w)
	if w == 4 && h == 2 {
		if y == 0 {
			if rB == ref {
				return mB
			}
		} else if rA == ref {
			return mA
		}
	} else if w == 2 && h == 4 {
		if x == 0 {
			if rA == ref {
				return mA
			}
		} else if rC == ref {
			return mC
		}
	}
	if !aB && !aC && aA {
		return mA
	}
	eq := 0
	var m mv
	if rA == ref {
		eq++
		m = mA
	}
	if rB == ref {
		eq++
		m = mB
	}
	if rC == ref {
		eq++
		m = mC
	}
	if eq == 1 {
		return m
	}
	return mv{median3(mA.x, mB.x, mC.x), median3(mA.y, mB.y, mC.y)}
}

// fillMotion writes ref and mv for list l into the w x h block region.
func (s *sliceDec) fillMotion(l, x, y, w, h int, ref int8, m mv) {
	if w == 4 && h == 4 {
		s.fillMotionMB(l, ref, m)
		return
	}
	s.fillMotionGo(l, x, y, w, h, ref, m)
}

func (s *sliceDec) fillMotionGo(l, x, y, w, h int, ref int8, m mv) {
	st := s.fc.mbW * 4
	base := s.idx4(x, y)
	refs := s.pic.refs[l]
	mvs := s.pic.mvs[l]
	for j := 0; j < h; j++ {
		o := base + j*st
		for i := 0; i < w; i++ {
			refs[o+i] = ref
			mvs[o+i] = m
		}
	}
}

func (s *sliceDec) pskipMV() mv {
	if !s.availA || !s.availB {
		return mv{}
	}
	rA, mA, _ := s.nb(0, -1, 0)
	rB, mB, _ := s.nb(0, 0, -1)
	if rA == 0 && mA == (mv{}) || rB == 0 && mB == (mv{}) {
		return mv{}
	}
	return s.mvPred(0, 0, 0, 0, 4, 4)
}

func minPositive(a, b int8) int8 {
	if a >= 0 && b >= 0 {
		if a < b {
			return a
		}
		return b
	}
	if a > b {
		return a
	}
	return b
}

// colocated returns refIdxCol, mvCol, the list used and the colocated
// block index for 4x4 block (x,y) of the current macroblock.
func (s *sliceDec) colocated(x, y int) (int8, mv, int, int) {
	if s.h.sps.direct8x8Inference {
		x = (x >> 1) * 3
		y = (y >> 1) * 3
	}
	i := s.idx4(x, y)
	col := s.colPic
	if r := col.refs[0][i]; r >= 0 {
		return r, col.mvs[0][i], 0, i
	}
	if r := col.refs[1][i]; r >= 0 {
		return r, col.mvs[1][i], 1, i
	}
	return -1, mv{}, 0, i
}

// directMotion derives motion for direct-predicted 8x8 blocks of the
// current macroblock (mask bit i = 8x8 block i).
func (s *sliceDec) directMotion(mask uint8) {
	s.colPic.waitRows(s.mbY + 1)
	if s.h.directSpatial {
		s.directSpatial(mask)
	} else {
		s.directTemporal(mask)
	}
}

func (s *sliceDec) directSpatial(mask uint8) {
	var ref [2]int8
	var pm [2]int
	ref[0], pm[0] = s.spatialPred(0)
	ref[1], pm[1] = s.spatialPred(1)
	directZero := false
	if ref[0] < 0 && ref[1] < 0 {
		ref[0], ref[1] = 0, 0
		directZero = true
	}
	colShortTerm := s.colIsShortTerm()
	if useCabacAsm {
		s.directUniform = s.directFill(mask, ref, pm, colShortTerm && !directZero)
		s.directUniformValid = true
		return
	}
	pmv := [2]mv{unpackMV(pm[0]), unpackMV(pm[1])}
	for b8 := 0; b8 < 4; b8++ {
		if mask>>b8&1 == 0 {
			continue
		}
		bx, by := (b8&1)*2, (b8>>1)*2
		for j := 0; j < 2; j++ {
			for i := 0; i < 2; i++ {
				x, y := bx+i, by+j
				var m [2]mv
				if !directZero {
					colZero := false
					if colShortTerm {
						rc, mc, _, _ := s.colocated(x, y)
						colZero = rc == 0 && mc.x >= -1 && mc.x <= 1 && mc.y >= -1 && mc.y <= 1
					}
					for l := 0; l < 2; l++ {
						if ref[l] < 0 || (ref[l] == 0 && colZero) {
							m[l] = mv{}
						} else {
							m[l] = pmv[l]
						}
					}
				}
				idx := s.idx4(x, y)
				for l := 0; l < 2; l++ {
					s.pic.refs[l][idx] = ref[l]
					s.pic.mvs[l][idx] = m[l]
				}
			}
		}
	}
}

// colIsShortTerm reports whether RefPicList1[0] is a short-term reference.
func (s *sliceDec) colIsShortTerm() bool {
	return !s.refIsLongTerm(1, 0)
}

func (s *sliceDec) refIsLongTerm(l, i int) bool {
	return s.refInfo.lt[l][i]
}

func (s *sliceDec) directTemporal(mask uint8) {
	col := s.colPic
	for b8 := 0; b8 < 4; b8++ {
		if mask>>b8&1 == 0 {
			continue
		}
		bx, by := (b8&1)*2, (b8>>1)*2
		for j := 0; j < 2; j++ {
			for i := 0; i < 2; i++ {
				x, y := bx+i, by+j
				rc, mc, lc, ci := s.colocated(x, y)
				var ref0 int8
				if rc >= 0 {
					cmb := (ci/(s.fc.mbW*4))/4*s.fc.mbW + (ci%(s.fc.mbW*4))/4
					id := col.sliceRef(int(col.mbSlice[cmb])).ids[lc][rc]
					r, ok := s.mapCol[id]
					if !ok {
						r = 0
					}
					ref0 = int8(r)
				}
				var m0, m1 mv
				if s.dsLongTerm[ref0] {
					m0 = mc
				} else {
					dsf := s.distScale[ref0]
					m0 = mv{int16((dsf*int32(mc.x) + 128) >> 8), int16((dsf*int32(mc.y) + 128) >> 8)}
					m1 = mv{m0.x - mc.x, m0.y - mc.y}
				}
				idx := s.idx4(x, y)
				s.pic.refs[0][idx] = ref0
				s.pic.mvs[0][idx] = m0
				s.pic.refs[1][idx] = 0
				s.pic.mvs[1][idx] = m1
			}
		}
	}
}

// predFillGo is the Go version of predFill.
func (s *sliceDec) predFillGo(l int, ref int8, x, y, w, h int, mvdx, mvdy int16) {
	pm := s.mvPred(l, ref, x, y, w, h)
	s.fillMotion(l, x, y, w, h, ref, mv{pm.x + mvdx, pm.y + mvdy})
}

func packMV(m mv) int { return int(uint16(m.x)) | int(uint16(m.y))<<16 }

func unpackMV(v int) mv { return mv{int16(v), int16(v >> 16)} }
