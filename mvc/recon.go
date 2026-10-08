package mvc

import "unsafe"

// Residual parsing and macroblock reconstruction.

func (s *sliceDec) nCLuma(bx, by int) int {
	st := s.fc.mbW * 4
	idx := s.idx4(bx, by)
	aA := bx > 0 || s.availA
	aB := by > 0 || s.availB
	switch {
	case aA && aB:
		return (int(s.fc.nnz[idx-1]) + int(s.fc.nnz[idx-st]) + 1) >> 1
	case aA:
		return int(s.fc.nnz[idx-1])
	case aB:
		return int(s.fc.nnz[idx-st])
	}
	return 0
}

func (s *sliceDec) nCChroma(c, bx, by int) int {
	st := s.fc.mbW * 2
	idx := (s.mbY*2+by)*st + s.mbX*2 + bx
	g := s.fc.nnzC[c]
	aA := bx > 0 || s.availA
	aB := by > 0 || s.availB
	switch {
	case aA && aB:
		return (int(g[idx-1]) + int(g[idx-st]) + 1) >> 1
	case aA:
		return int(g[idx-1])
	case aB:
		return int(g[idx-st])
	}
	return 0
}

// cbfCond returns the coded_block_flag condition term for a neighbour
// macroblock bit.
func (s *sliceDec) cbfNeighbor(avail bool, m *mbInfo, bit uint32) uint32 {
	if !avail {
		if s.mb.flags&mbfIntra != 0 {
			return 1
		}
		return 0
	}
	if m.cbf&bit != 0 {
		return 1
	}
	return 0
}

// cbfMasks returns the neighbours' cbf masks for the multi-block decoder:
// unavailable neighbours count as coded for intra macroblocks.
func (s *sliceDec) cbfMasks() (a, b uint32) {
	var un uint32
	if s.mb.flags&mbfIntra != 0 {
		un = ^uint32(0)
	}
	a, b = un, un
	if s.availA {
		a = s.leftMB().cbf
	}
	if s.availB {
		b = s.topMB().cbf
	}
	return
}

// lumaCBF decodes coded_block_flag for luma 4x4 block (bx,by), cat 1/2.
func (s *sliceDec) lumaCBF(cur *mbInfo, cat, bx, by int) int {
	var a, b uint32
	r := by*4 + bx
	if bx > 0 {
		a = cur.cbf >> (r - 1) & 1
	} else {
		a = s.cbfNeighbor(s.availA, s.leftMB(), 1<<(r+3))
	}
	if by > 0 {
		b = cur.cbf >> (r - 4) & 1
	} else {
		b = s.cbfNeighbor(s.availB, s.topMB(), 1<<(r+12))
	}
	return cbfCtxIdx(cat, a, b)
}

func (s *sliceDec) chromaACCBF(cur *mbInfo, c, bx, by int) int {
	var a, b uint32
	bit := 16 + c*4 + by*2 + bx
	if bx > 0 {
		a = cur.cbf >> (bit - 1) & 1
	} else {
		a = s.cbfNeighbor(s.availA, s.leftMB(), 1<<(bit+1))
	}
	if by > 0 {
		b = cur.cbf >> (bit - 2) & 1
	} else {
		b = s.cbfNeighbor(s.availB, s.topMB(), 1<<(bit+2))
	}
	return cbfCtxIdx(catChromaAC, a, b)
}

// dequant4 scales the coefficients of a 4x4 block (8.5.12.1) into dst,
// which starts at the block's first coefficient; pt maps zigzag indices to
// offsets in dst.
func dequant4(dst []int16, pt *[16]uint8, cb *coeffBuf, start int, ls *[16]int32, qp int) {
	qd := uint(qp / 6)
	for k := 0; k < cb.n; k++ {
		zi := (int(cb.idx[k]) + start) & 15
		v := cb.level[k] * ls[zigzag4x4[zi]]
		if qd >= 4 {
			v <<= qd - 4
		} else {
			v = (v + 1<<(3-qd)) >> (4 - qd)
		}
		dst[pt[zi]] = int16(v)
	}
}

// dequant8 scales the coefficients of an 8x8 block into dst (row stride
// 16). interleave >= 0 selects the CAVLC 4x4 interleaving.
func dequant8(dst []int16, cb *coeffBuf, interleave int, ls *[64]int32, qp int) {
	qd := uint(qp / 6)
	for k := 0; k < cb.n; k++ {
		si := int(cb.idx[k])
		if interleave >= 0 {
			si = si*4 + interleave
		}
		si &= 63
		v := cb.level[k] * ls[zigzag8x8[si]]
		if qd >= 6 {
			v <<= qd - 6
		} else {
			v = (v + 1<<(5-qd)) >> (6 - qd)
		}
		dst[zz8Pos16[si]] = int16(v)
	}
}

// residualAndRecon parses mb_qp_delta and the residual, then reconstructs
// the macroblock.
func (s *sliceDec) residualAndRecon(cur *mbInfo) error {
	mb := &s.mb
	if mb.cbp != 0 || mb.flags&mbfI16x16 != 0 {
		var dq int
		if s.cabacOn {
			dq = s.cabacQPDelta()
		} else {
			dq = int(s.br.se())
		}
		if dq < -26 || dq > 25 {
			return errInvalid
		}
		s.lastDQ = dq
		if dq != 0 {
			s.setQP((s.qp + dq + 52) % 52)
		}
		if err := s.parseResidual(cur); err != nil {
			return err
		}
	} else {
		s.lastDQ = 0
	}
	cur.qp = int8(s.qp)
	if mb.flags&mbfIntra != 0 {
		s.reconIntra(cur)
	} else {
		s.reconInter()
	}
	return nil
}

func (s *sliceDec) parseResidual(cur *mbInfo) error {
	mb := &s.mb
	cb := &s.cb
	intra := mb.flags&mbfIntra != 0
	listY, listCb := 3, 4
	if intra {
		listY, listCb = 0, 1
	}
	qp := s.qp
	var nz uint32
	ls := &s.dq4[listY][qp%6]
	lsz := &s.dq4z[listY][qp%6]
	sh4 := dqShifts(qp, false)
	st := s.fc.mbW * 4
	if mb.flags&mbfI16x16 != 0 {
		// DC
		var coded bool
		if s.cabacOn {
			a := s.cbfNeighbor(s.availA, s.leftMB(), cbfLumaDC)
			b := s.cbfNeighbor(s.availB, s.topMB(), cbfLumaDC)
			if s.cabacBlock(cbfCtxIdx(catLumaDC, a, b), catLumaDC, 16, cb) {
				cur.cbf |= cbfLumaDC
				coded = true
			}
		} else {
			if s.cavlcResidual(s.nCLuma(0, 0), 16, cb) < 0 {
				return errInvalid
			}
			coded = cb.n > 0
		}
		if coded {
			mb.dcY = [16]int32{}
			for k := 0; k < cb.n; k++ {
				mb.dcY[zigzag4x4[cb.idx[k]]] = cb.level[k]
			}
			lumaDCDequant(&mb.dcY, qp, s.dq4[listY][qp%6][0])
			for r := 0; r < 16; r++ {
				if mb.dcY[r] != 0 {
					mb.coefY[blkOff16[r]] = int16(mb.dcY[r])
					mb.nzY |= 1 << r
				}
			}
		}
		if mb.cbp&15 != 0 {
			if useCabacAsm && s.cabacOn {
				cbfA, cbfB := s.cbfMasks()
				cur.cbf, nz, _ = s.cabacBlocks(&blockArgs{cat: catLumaAC, desc: lumaBlkDesc[:], cbp: 15,
					cbfA: cbfA, cbfB: cbfB, dst: mb.coefY[:], scale: [2][]int32{lsz[1:]}, pos: zzPos16[1:],
					shifts: [2]int{sh4}}, cur.cbf)
				mb.nzY |= uint16(nz)
				mb.acY |= uint16(nz)
				cur.nzMask |= uint16(nz)
			} else {
				for blk := 0; blk < 16; blk++ {
					bx, by := int(blkX[blk]), int(blkY[blk])
					r := by*4 + bx
					if s.cabacOn {
						if !s.cabacBlockDQ(s.lumaCBF(cur, catLumaAC, bx, by), catLumaAC, 15, cb,
							mb.coefY[blkOff16[r]:], lsz[1:], zzPos16[1:], sh4) {
							continue
						}
						cur.cbf |= 1 << r
						mb.nzY |= 1 << r
						mb.acY |= 1 << r
						cur.nzMask |= 1 << r
						continue
					}
					n := s.cavlcResidual(s.nCLuma(bx, by), 15, cb)
					if n < 0 {
						return errInvalid
					}
					s.fc.nnz[s.idx4(bx, by)] = uint8(n)
					if n == 0 {
						continue
					}
					dequant4(mb.coefY[blkOff16[r]:], &zzPos16, cb, 1, ls, qp)
					mb.nzY |= 1 << r
					mb.acY |= 1 << r
					cur.nzMask |= 1 << r
				}
			}
		}
	} else if mb.flags&mbfT8x8 != 0 {
		ls8 := &s.dq8[listY/3][qp%6]
		ls8z := &s.dq8z[listY/3][qp%6]
		sh8 := dqShifts(qp, true)
		if useCabacAsm && s.cabacOn {
			var nz8, ac8 uint32
			cur.cbf, nz8, ac8 = s.cabacBlocks(&blockArgs{cat: catLuma8x8, desc: luma8x8Desc[:], cbp: int(mb.cbp & 15),
				dst: mb.coefY[:], scale: [2][]int32{ls8z[:]}, pos: zz8Pos16[:], shifts: [2]int{sh8}}, cur.cbf)
			mb.nz8 |= uint8(nz8)
			mb.ac8 |= uint8(ac8)
			for b8 := 0; b8 < 4; b8++ {
				if nz8>>b8&1 != 0 {
					mask := uint16(0x33) << ((b8>>1)*8 + (b8&1)*2)
					cur.cbf |= uint32(mask)
					cur.nzMask |= mask
				}
			}
		} else {
			for b8 := 0; b8 < 4; b8++ {
				if mb.cbp>>b8&1 == 0 {
					continue
				}
				bx0, by0 := (b8&1)*2, (b8>>1)*2
				mask := uint16(0x33) << (by0*4 + bx0)
				if s.cabacOn {
					s.cabacBlockDQ(-1, catLuma8x8, 64, cb, mb.coefY[b8>>1*128+b8&1*8:], ls8z[:], zz8Pos16[:], sh8)
					cur.cbf |= uint32(mask)
					mb.nz8 |= 1 << b8
					if cb.n != 1 || cb.idx[0] != 0 {
						mb.ac8 |= 1 << b8
					}
					cur.nzMask |= mask
				} else {
					total := 0
					for i4 := 0; i4 < 4; i4++ {
						bx, by := bx0+i4&1, by0+i4>>1
						n := s.cavlcResidual(s.nCLuma(bx, by), 16, cb)
						if n < 0 {
							return errInvalid
						}
						s.fc.nnz[s.idx4(bx, by)] = uint8(n)
						total += n
						dequant8(mb.coefY[b8>>1*128+b8&1*8:], cb, i4, ls8, qp)
					}
					if total > 0 {
						mb.nz8 |= 1 << b8
						mb.ac8 |= 1 << b8
						cur.nzMask |= mask
					}
				}
			}
		}
	} else if useCabacAsm && s.cabacOn {
		cbfA, cbfB := s.cbfMasks()
		var ac uint32
		cur.cbf, nz, ac = s.cabacBlocks(&blockArgs{cat: catLuma4x4, desc: lumaBlkDesc[:], cbp: int(mb.cbp & 15),
			cbfA: cbfA, cbfB: cbfB, dst: mb.coefY[:], scale: [2][]int32{lsz[:]}, pos: zzPos16[:],
			shifts: [2]int{sh4}}, cur.cbf)
		mb.nzY |= uint16(nz)
		mb.acY |= uint16(ac)
		cur.nzMask |= uint16(nz)
	} else {
		for blk := 0; blk < 16; blk++ {
			if mb.cbp>>(blk>>2)&1 == 0 {
				continue
			}
			bx, by := int(blkX[blk]), int(blkY[blk])
			r := by*4 + bx
			if s.cabacOn {
				if !s.cabacBlockDQ(s.lumaCBF(cur, catLuma4x4, bx, by), catLuma4x4, 16, cb,
					mb.coefY[blkOff16[r]:], lsz[:], zzPos16[:], sh4) {
					continue
				}
				cur.cbf |= 1 << r
				mb.nzY |= 1 << r
				if cb.n != 1 || cb.idx[0] != 0 {
					mb.acY |= 1 << r
				}
				cur.nzMask |= 1 << r
				continue
			}
			n := s.cavlcResidual(s.nCLuma(bx, by), 16, cb)
			if n < 0 {
				return errInvalid
			}
			s.fc.nnz[s.idx4(bx, by)] = uint8(n)
			if n == 0 {
				continue
			}
			dequant4(mb.coefY[blkOff16[r]:], &zzPos16, cb, 0, ls, qp)
			mb.nzY |= 1 << r
			if cb.n != 1 || cb.idx[0] != 0 {
				mb.acY |= 1 << r
			}
			cur.nzMask |= 1 << r
		}
	}
	_ = st
	if s.h.sps.chromaFormatIdc == 0 || mb.cbp>>4 == 0 {
		return nil
	}
	// chroma DC
	if useCabacAsm && s.cabacOn {
		cur.cbf, nz = s.chromaDCBlocks(cur.cbf, listCb)
		mb.nzC[0] |= uint8(nz & 15)
		mb.nzC[1] |= uint8(nz >> 4)
	}
	for c := 0; c < 2 && (!useCabacAsm || !s.cabacOn); c++ {
		var coded bool
		if s.cabacOn {
			bit := uint32(cbfCbDC) << c
			a := s.cbfNeighbor(s.availA, s.leftMB(), bit)
			b := s.cbfNeighbor(s.availB, s.topMB(), bit)
			if s.cabacBlock(cbfCtxIdx(catChromaDC, a, b), catChromaDC, 4, cb) {
				cur.cbf |= bit
				coded = true
			}
		} else {
			if s.cavlcResidual(-1, 4, cb) < 0 {
				return errInvalid
			}
			coded = cb.n > 0
		}
		if coded {
			var dc [4]int32
			for k := 0; k < cb.n; k++ {
				dc[cb.idx[k]] = cb.level[k]
			}
			qpc := s.qpc[c]
			chromaDCDequant(&dc, qpc, s.dq4[listCb+c][qpc%6][0])
			for b := 0; b < 4; b++ {
				if dc[b] != 0 {
					mb.coefC[c][b>>1*32+b&1*4] = int16(dc[b])
					mb.nzC[c] |= 1 << b
				}
			}
		}
	}
	if mb.cbp>>4 != 2 {
		return nil
	}
	if useCabacAsm && s.cabacOn {
		cbfA, cbfB := s.cbfMasks()
		q0, q1 := s.qpc[0], s.qpc[1]
		cur.cbf, nz, _ = s.cabacBlocks(&blockArgs{cat: catChromaAC, desc: chromaACDesc[:], cbp: 0xff,
			cbfA: cbfA, cbfB: cbfB, dst: unsafe.Slice(&mb.coefC[0][0], 128), pos: zzPos8[1:],
			scale:  [2][]int32{s.dq4z[listCb][q0%6][1:], s.dq4z[listCb+1][q1%6][1:]},
			shifts: [2]int{dqShifts(q0, false), dqShifts(q1, false)}}, cur.cbf)
		mb.nzC[0] |= uint8(nz & 15)
		mb.nzC[1] |= uint8(nz >> 4)
		return nil
	}
	for c := 0; c < 2; c++ {
		qpc := s.qpc[c]
		lsc := &s.dq4[listCb+c][qpc%6]
		lscz := &s.dq4z[listCb+c][qpc%6]
		shc := dqShifts(qpc, false)
		for b := 0; b < 4; b++ {
			bx, by := b&1, b>>1
			if s.cabacOn {
				if !s.cabacBlockDQ(s.chromaACCBF(cur, c, bx, by), catChromaAC, 15, cb,
					mb.coefC[c][b>>1*32+b&1*4:], lscz[1:], zzPos8[1:], shc) {
					continue
				}
				cur.cbf |= 1 << (16 + c*4 + b)
				mb.nzC[c] |= 1 << b
				continue
			} else {
				n := s.cavlcResidual(s.nCChroma(c, bx, by), 15, cb)
				if n < 0 {
					return errInvalid
				}
				cst := s.fc.mbW * 2
				s.fc.nnzC[c][(s.mbY*2+by)*cst+s.mbX*2+bx] = uint8(n)
				if n == 0 {
					continue
				}
			}
			dequant4(mb.coefC[c][b>>1*32+b&1*4:], &zzPos8, cb, 1, lsc, qpc)
			mb.nzC[c] |= 1 << b
		}
	}
	return nil
}

func (s *sliceDec) addLuma4x4(r int, off, stride int) {
	mb := &s.mb
	if mb.nzY>>r&1 == 0 {
		return
	}
	idct4x4AddS(s.pic.planes[0], off, stride, mb.coefY[blkOff16[r]:], 16)
}

// addLumaResidual adds the residual of all luma 4x4 blocks of the
// macroblock (inter and Intra16x16 macroblocks).
func (s *sliceDec) addLumaResidual(yo, st int) {
	mb := &s.mb
	for by := 0; by < 4; by++ {
		m := mb.nzY >> (by * 4) & 15
		if m == 0 {
			continue
		}
		c := mb.coefY[by*64:]
		if mb.acY>>(by*4)&15 == 0 {
			// DC-only blocks: the residual is a constant per block
			var v [16]int16
			for bx := 0; bx < 4; bx++ {
				dc := (int32(c[bx*4]) + 32) >> 6
				c[bx*4] = 0
				for i := 0; i < 4; i++ {
					v[bx*4+i] = int16(dc)
				}
			}
			addConst16(s.pic.planes[0], yo+by*4*st, st, &v, 4)
			continue
		}
		idctRow4(s.pic.planes[0], yo+by*4*st, st, c, m)
	}
}

func (s *sliceDec) reconChroma() {
	mb := &s.mb
	pic := s.pic
	st := pic.stride[1]
	co := pic.origin[1] + s.mbY*8*st + s.mbX*8
	if mb.nzC[0]|mb.nzC[1] == 0 {
		return
	}
	if mb.cbp>>4 == 1 {
		// only DC coefficients: each 4x4 block adds a constant
		var dc [2][4]int16
		for c := 0; c < 2; c++ {
			for b := 0; b < 4; b++ {
				o := b>>1*32 + b&1*4
				dc[c][b] = int16((int32(mb.coefC[c][o]) + 32) >> 6)
				mb.coefC[c][o] = 0
			}
		}
		chromaAddDC(pic.planes[1], pic.planes[2], co, st, &dc)
		return
	}
	idctChroma(pic.planes[1], pic.planes[2], co, st, &mb.coefC, mb.nzC)
}

func (s *sliceDec) reconInter() {
	mb := &s.mb
	pic := s.pic
	st := pic.stride[0]
	yo := pic.origin[0] + s.mbY*16*st + s.mbX*16
	if mb.flags&mbfT8x8 != 0 {
		for row := 0; row < 2; row++ {
			m := mb.nz8 >> (row * 2) & 3
			if m == 0 {
				continue
			}
			c := mb.coefY[row*128:]
			off := yo + row*8*st
			if mb.ac8>>(row*2)&3 == 0 {
				var v [16]int16
				for bx := 0; bx < 2; bx++ {
					dc := (int32(c[bx*8]) + 32) >> 6
					c[bx*8] = 0
					for i := 0; i < 8; i++ {
						v[bx*8+i] = int16(dc)
					}
				}
				addConst16(pic.planes[0], off, st, &v, 8)
				continue
			}
			idct8x8Row(pic.planes[0], off, st, c, m)
		}
	} else if mb.nzY != 0 {
		s.addLumaResidual(yo, st)
	}
	s.reconChroma()
}

// intraNeighbours returns availability of neighbour macroblocks for intra
// prediction (taking constrained_intra_pred into account).
func (s *sliceDec) intraNeighbours() (a, b, c, d bool) {
	a, b, c, d = s.availA, s.availB, s.availC, s.availD
	if s.h.pps.constrainedIntraPred {
		w := s.fc.mbW
		mbs := s.fc.mbs
		a = a && mbs[s.mbAddr-1].flags&mbfIntra != 0
		b = b && mbs[s.mbAddr-w].flags&mbfIntra != 0
		c = c && mbs[s.mbAddr-w+1].flags&mbfIntra != 0
		d = d && mbs[s.mbAddr-w-1].flags&mbfIntra != 0
	}
	return
}

func (s *sliceDec) reconIntra(cur *mbInfo) {
	mb := &s.mb
	pic := s.pic
	st := pic.stride[0]
	yo := pic.origin[0] + s.mbY*16*st + s.mbX*16
	nA, nB, nC, nD := s.intraNeighbours()
	g := s.fc.i4
	gst := s.fc.mbW * 4
	switch {
	case mb.flags&mbfI16x16 != 0:
		pred16x16(pic.planes[0], yo, st, int(mb.i16Mode), nB, nA, nD)
		s.addLumaResidual(yo, st)
	case mb.flags&mbfT8x8 != 0:
		for b8 := 0; b8 < 4; b8++ {
			bx, by := (b8&1)*2, (b8>>1)*2
			gi := s.idx4(bx, by)
			aA := bx > 0 || nA
			aB := by > 0 || nB
			pm := int8(2)
			if aA && aB {
				pm = min(g[gi-1], g[gi-gst])
			}
			mode := pm
			if !mb.prevFlag[b8] {
				mode = mb.remMode[b8]
				if mode >= pm {
					mode++
				}
			}
			g[gi], g[gi+1], g[gi+gst], g[gi+gst+1] = mode, mode, mode, mode
			var av intraAvail
			av.left = aA
			av.top = aB
			switch b8 {
			case 0:
				av.topLeft = nD
				av.topRight = nB
			case 1:
				av.topLeft = nB
				av.topRight = nC
			case 2:
				av.topLeft = nA
				av.topRight = true
			case 3:
				av.topLeft = true
				av.topRight = false
			}
			off := yo + by*4*st + bx*4
			pred8x8L(pic.planes[0], off, st, int(mode), av)
			if mb.nz8>>b8&1 != 0 {
				idct8x8AddS(pic.planes[0], off, st, mb.coefY[b8>>1*128+b8&1*8:], 16)
			}
		}
	default:
		for blk := 0; blk < 16; blk++ {
			bx, by := int(blkX[blk]), int(blkY[blk])
			gi := s.idx4(bx, by)
			aA := bx > 0 || nA
			aB := by > 0 || nB
			pm := int8(2)
			if aA && aB {
				pm = min(g[gi-1], g[gi-gst])
			}
			mode := pm
			if !mb.prevFlag[blk] {
				mode = mb.remMode[blk]
				if mode >= pm {
					mode++
				}
			}
			g[gi] = mode
			var av intraAvail
			av.left = aA
			av.top = aB
			switch {
			case bx > 0 && by > 0:
				av.topLeft = true
			case by > 0:
				av.topLeft = nA
			case bx > 0:
				av.topLeft = nB
			default:
				av.topLeft = nD
			}
			if by == 0 {
				if bx < 3 {
					av.topRight = nB
				} else {
					av.topRight = nC
				}
			} else if bx < 3 {
				av.topRight = blkOrder[(by-1)*4+bx+1] < blkOrder[by*4+bx]
			}
			off := yo + by*4*st + bx*4
			pred4x4(pic.planes[0], off, st, int(mode), av)
			r := by*4 + bx
			s.addLuma4x4(r, off, st)
		}
	}
	if s.h.sps.chromaFormatIdc != 0 {
		cst := pic.stride[1]
		co := pic.origin[1] + s.mbY*8*cst + s.mbX*8
		predChroma(pic.planes[1], co, cst, int(mb.chromaPred), nB, nA)
		predChroma(pic.planes[2], co, cst, int(mb.chromaPred), nB, nA)
		s.reconChroma()
	}
	_ = cur
}
