package mvc

// Macroblock layer decoding (7.3.5) shared by CAVLC and CABAC.

var bMBTypeInfo = [23]struct{ shape, p0, p1 uint8 }{
	{}, // direct
	{part16x16, predL0, 0}, {part16x16, predL1, 0}, {part16x16, predBi, 0},
	{part16x8, predL0, predL0}, {part8x16, predL0, predL0},
	{part16x8, predL1, predL1}, {part8x16, predL1, predL1},
	{part16x8, predL0, predL1}, {part8x16, predL0, predL1},
	{part16x8, predL1, predL0}, {part8x16, predL1, predL0},
	{part16x8, predL0, predBi}, {part8x16, predL0, predBi},
	{part16x8, predL1, predBi}, {part8x16, predL1, predBi},
	{part16x8, predBi, predL0}, {part8x16, predBi, predL0},
	{part16x8, predBi, predL1}, {part8x16, predBi, predL1},
	{part16x8, predBi, predBi}, {part8x16, predBi, predBi},
	{part8x8, 0, 0},
}

// Sub-macroblock shapes.
const (
	sub8x8 = iota
	sub8x4
	sub4x8
	sub4x4
)

var bSubInfo = [13]struct{ shape, pred uint8 }{
	{sub8x8, 0}, // direct
	{sub8x8, predL0}, {sub8x8, predL1}, {sub8x8, predBi},
	{sub8x4, predL0}, {sub4x8, predL0}, {sub8x4, predL1}, {sub4x8, predL1},
	{sub8x4, predBi}, {sub4x8, predBi},
	{sub4x4, predL0}, {sub4x4, predL1}, {sub4x4, predBi},
}

var cbpIntraMap = [48]uint8{
	47, 31, 15, 0, 23, 27, 29, 30, 7, 11, 13, 14, 39, 43, 45, 46,
	16, 3, 5, 10, 12, 19, 21, 26, 28, 35, 37, 42, 44, 1, 2, 4,
	8, 17, 18, 20, 24, 6, 9, 22, 25, 32, 33, 34, 36, 40, 38, 41,
}
var cbpInterMap = [48]uint8{
	0, 16, 1, 2, 4, 8, 32, 3, 5, 10, 12, 15, 47, 7, 11, 13,
	14, 6, 9, 31, 35, 37, 42, 44, 33, 34, 36, 40, 39, 43, 45, 46,
	17, 18, 20, 24, 19, 21, 26, 28, 23, 27, 29, 30, 22, 25, 38, 41,
}
var cbpIntraMapMono = [16]uint8{15, 0, 7, 11, 13, 14, 3, 5, 10, 12, 1, 2, 4, 8, 6, 9}
var cbpInterMapMono = [16]uint8{0, 1, 2, 4, 8, 3, 5, 10, 12, 15, 7, 11, 13, 14, 6, 9}

// partRect returns the rectangle (x,y,w,h in 4x4 units) of partition i.
func partRect(shape uint8, i int) (int, int, int, int) {
	switch shape {
	case part16x16:
		return 0, 0, 4, 4
	case part16x8:
		return 0, i * 2, 4, 2
	case part8x16:
		return i * 2, 0, 2, 4
	}
	return (i & 1) * 2, (i >> 1) * 2, 2, 2
}

// subRect returns the rectangle of sub-partition j of a sub-macroblock
// relative to its top-left.
func subRect(shape uint8, j int) (int, int, int, int) {
	switch shape {
	case sub8x8:
		return 0, 0, 2, 2
	case sub8x4:
		return 0, j, 2, 1
	case sub4x8:
		return j, 0, 1, 2
	}
	return j & 1, j >> 1, 1, 1
}

var subCount = [4]int{1, 2, 2, 4}

// beginMB resets per-macroblock state in the picture grids.
func (s *sliceDec) beginMB() *mbInfo {
	cur := &s.fc.mbs[s.mbAddr]
	*cur = mbInfo{slice: uint16(s.sliceIdx), flags: mbfAvail}
	mb := &s.mb
	mb.flags = 0
	mb.cbp = 0
	mb.directSub = 0
	mb.nzY, mb.nz8 = 0, 0
	mb.acY, mb.ac8 = 0, 0
	mb.nzC = [2]uint8{}
	mb.nzDC = false
	mb.nzCDC = [2]bool{}
	st := s.fc.mbW * 4
	base := s.idx4(0, 0)
	pic := s.pic
	s.clearMBGrids(base, st)
	if !s.cabacOn { // total_coeff is only used by CAVLC
		for j := 0; j < 4; j++ {
			o := base + j*st
			n := s.fc.nnz[o : o+4]
			n[0], n[1], n[2], n[3] = 0, 0, 0, 0
		}
	}
	if !s.cabacOn {
		cst := s.fc.mbW * 2
		cb := s.mbY*2*cst + s.mbX*2
		for c := 0; c < 2; c++ {
			s.fc.nnzC[c][cb], s.fc.nnzC[c][cb+1] = 0, 0
			s.fc.nnzC[c][cb+cst], s.fc.nnzC[c][cb+cst+1] = 0, 0
		}
	}
	pic.mbSlice[s.mbAddr] = uint16(s.sliceIdx)
	return cur
}

// decodeSkipMB decodes a P_Skip or B_Skip macroblock.
func (s *sliceDec) decodeSkipMB() {
	cur := s.beginMB()
	cur.flags |= mbfSkip
	cur.qp = int8(s.qp)
	s.lastDQ = 0
	if s.h.sliceType == sliceP {
		s.pskipFill()
		s.mcPartition(0, 0, 4, 4)
		s.finishInter(cur, 0)
	} else {
		cur.flags |= mbfDirect16
		cur.directSub = 15
		s.directMotion(15)
		s.finishInter(cur, s.mcDirect(15))
	}
}

// finishInter records, for deblocking, the reference picture ids of the
// macroblock's 4x4 blocks and the internal edges that may separate
// different motion.
func (s *sliceDec) finishInter(cur *mbInfo, edges uint8) {
	cur.mvEdges = edges
	if !s.deblock {
		return
	}
	st := s.fc.mbW * 4
	base := s.idx4(0, 0)
	if edges == 0 {
		// uniform motion: one reference per list for the whole macroblock
		var id [2]int32
		for l := 0; l < 2; l++ {
			id[l] = -1
			if r := s.pic.refs[l][base]; r >= 0 {
				id[l] = s.refInfo.ids[l][r&31]
			}
		}
		s.fillIDs(base, id[0], id[1])
		return
	}
	for l := 0; l < 2; l++ {
		ids := &s.refInfo.ids[l]
		refs := s.pic.refs[l]
		out := s.fc.refIDs[l]
		for j := 0; j < 4; j++ {
			o := base + j*st
			for i := 0; i < 4; i++ {
				id := int32(-1)
				if r := refs[o+i]; r >= 0 {
					id = ids[r&31]
				}
				out[o+i] = id
			}
		}
	}
}

// mcDirect performs motion compensation of direct 8x8 blocks and returns
// the mvEdges bits of those blocks (see mbInfo).
func (s *sliceDec) mcDirect(mask uint8) uint8 {
	u := s.directUniform
	uv := s.directUniformValid
	s.directUniformValid = false
	if mask == 15 && (uv && u&1 != 0 || !uv && s.uniformMotion(0, 0, 4)) {
		s.mcPartition(0, 0, 4, 4)
		return 0
	}
	var edges uint8
	for b8 := 0; b8 < 4; b8++ {
		if mask>>b8&1 == 0 {
			continue
		}
		x, y := (b8&1)*2, (b8>>1)*2
		edges |= 0x22 // edge 2 in both directions
		if uv && u>>(1+b8)&1 != 0 || !uv && s.uniformMotion(x, y, 2) {
			s.mcPartition(x, y, 2, 2)
		} else {
			edges |= subEdges[b8]
			for j := 0; j < 4; j++ {
				s.mcPartition(x+j&1, y+j>>1, 1, 1)
			}
		}
	}
	return edges
}

// subEdges[b8] are the mvEdges bits of the internal edges of 8x8 block b8.
var subEdges = [4]uint8{0x11, 0x14, 0x41, 0x44}

// uniformMotion reports whether the n x n blocks at (x,y) share motion.
func (s *sliceDec) uniformMotion(x, y, n int) bool {
	st := s.fc.mbW * 4
	b := s.idx4(x, y)
	pic := s.pic
	r0, r1 := pic.refs[0][b], pic.refs[1][b]
	m0, m1 := pic.mvs[0][b], pic.mvs[1][b]
	for j := 0; j < n; j++ {
		for i := 0; i < n; i++ {
			o := b + j*st + i
			if pic.refs[0][o] != r0 || pic.refs[1][o] != r1 ||
				(r0 >= 0 && pic.mvs[0][o] != m0) || (r1 >= 0 && pic.mvs[1][o] != m1) {
				return false
			}
		}
	}
	return true
}

func (s *sliceDec) readUE() int { return int(s.br.ue()) }

// decodeMB decodes a non-skipped macroblock.
func (s *sliceDec) decodeMB() error {
	cur := s.beginMB()
	mb := &s.mb
	var t int
	switch s.h.sliceType {
	case sliceI:
		if s.cabacOn {
			t = s.cabacMBTypeI(0, true)
		} else {
			t = s.readUE()
		}
		return s.decodeIntraMB(cur, t)
	case sliceP:
		if s.cabacOn {
			t = s.cabacMBTypeP()
		} else {
			t = s.readUE()
		}
		if t >= 5 {
			return s.decodeIntraMB(cur, t-5)
		}
	default:
		if s.cabacOn {
			t = s.cabacMBTypeB()
		} else {
			t = s.readUE()
		}
		if t >= 23 {
			return s.decodeIntraMB(cur, t-23)
		}
	}
	pps := s.h.pps
	isB := s.h.sliceType == sliceB
	noSmall := true
	if isB && t == 0 {
		// B_Direct_16x16
		mb.flags |= mbfDirect16
		cur.flags |= mbfDirect16
		cur.directSub = 15
		mb.directSub = 15
		s.directMotion(15)
	} else {
		var shape uint8
		if isB {
			if t > 22 {
				return errInvalid
			}
			shape = bMBTypeInfo[t].shape
			mb.partPred[0], mb.partPred[1] = bMBTypeInfo[t].p0, bMBTypeInfo[t].p1
		} else {
			if t > 4 {
				return errInvalid
			}
			shape = [5]uint8{part16x16, part16x8, part8x16, part8x8, part8x8}[t]
			mb.partPred = [4]uint8{predL0, predL0, predL0, predL0}
		}
		mb.shape = shape
		if shape == part8x8 {
			if err := s.parseSubMBPred(cur, isB, !isB && t == 4); err != nil {
				return err
			}
			for i := 0; i < 4; i++ {
				if mb.directSub>>i&1 != 0 {
					if !s.h.sps.direct8x8Inference {
						noSmall = false
					}
				} else if mb.subShape[i] != sub8x8 {
					noSmall = false
				}
			}
		} else {
			if err := s.parseMBPred(); err != nil {
				return err
			}
		}
	}
	// coded_block_pattern
	if err := s.parseCBP(false); err != nil {
		return err
	}
	if mb.cbp&15 != 0 && pps.transform8x8 && noSmall &&
		(mb.flags&mbfDirect16 == 0 || s.h.sps.direct8x8Inference) {
		if s.readT8x8() {
			mb.flags |= mbfT8x8
		}
	}
	// motion compensation can now be done
	s.finishInter(cur, s.interPredict())
	cur.flags |= mb.flags & mbfT8x8
	cur.cbp = mb.cbp
	return s.residualAndRecon(cur)
}

func (s *sliceDec) readT8x8() bool {
	if s.cabacOn {
		return s.cabacTransform8x8()
	}
	return s.br.u1() != 0
}

func (s *sliceDec) parseCBP(intra bool) error {
	mb := &s.mb
	if s.cabacOn {
		mb.cbp = s.cabacCBP()
		return nil
	}
	v := s.br.ue()
	if s.h.sps.chromaFormatIdc == 0 {
		if v > 15 {
			return errInvalid
		}
		if intra {
			mb.cbp = cbpIntraMapMono[v]
		} else {
			mb.cbp = cbpInterMapMono[v]
		}
		return nil
	}
	if v > 47 {
		return errInvalid
	}
	if intra {
		mb.cbp = cbpIntraMap[v]
	} else {
		mb.cbp = cbpInterMap[v]
	}
	return nil
}

func (s *sliceDec) readRefIdx(l, x, y int) (int8, error) {
	n := s.h.numRefIdxActive[l]
	var v int
	if s.cabacOn {
		v = s.cabacRefIdx(l, x, y)
	} else {
		v = int(s.br.te(n - 1))
	}
	if v >= n {
		return 0, errInvalid
	}
	return int8(v), nil
}

func (s *sliceDec) readMvd(l, x, y, w, h int) (int16, int16) {
	if s.cabacOn {
		return s.cabacMvd(l, x, y, w, h)
	}
	return int16(s.br.se()), int16(s.br.se())
}

func (s *sliceDec) setRefRegion(l, x, y, w, h int, r int8) {
	st := s.fc.mbW * 4
	base := s.idx4(x, y)
	refs := s.pic.refs[l]
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			refs[base+j*st+i] = r
		}
	}
}

// parseMBPred parses mb_pred for 16x16, 16x8 and 8x16 inter macroblocks
// and derives their motion vectors.
func (s *sliceDec) parseMBPred() error {
	mb := &s.mb
	np := 1
	if mb.shape != part16x16 {
		np = 2
	}
	for l := 0; l < 2; l++ {
		for p := 0; p < np; p++ {
			mb.refIdx[l][p] = -1
			if mb.partPred[p]&(1<<l) == 0 {
				continue
			}
			x, y, w, h := partRect(mb.shape, p)
			if s.h.numRefIdxActive[l] > 1 {
				r, err := s.readRefIdx(l, x, y)
				if err != nil {
					return err
				}
				mb.refIdx[l][p] = r
			} else {
				mb.refIdx[l][p] = 0
			}
			s.setRefRegion(l, x, y, w, h, mb.refIdx[l][p])
		}
	}
	var mvd [2][2][2]int16
	for l := 0; l < 2; l++ {
		for p := 0; p < np; p++ {
			if mb.partPred[p]&(1<<l) == 0 {
				continue
			}
			x, y, w, h := partRect(mb.shape, p)
			mvd[l][p][0], mvd[l][p][1] = s.readMvd(l, x, y, w, h)
		}
	}
	// derive motion vectors in partition order
	for p := 0; p < np; p++ {
		x, y, w, h := partRect(mb.shape, p)
		for l := 0; l < 2; l++ {
			if mb.partPred[p]&(1<<l) == 0 {
				continue
			}
			s.predFill(l, mb.refIdx[l][p], x, y, w, h, mvd[l][p][0], mvd[l][p][1])
		}
	}
	return nil
}

// parseSubMBPred parses sub_mb_pred and derives motion vectors.
func (s *sliceDec) parseSubMBPred(cur *mbInfo, isB, ref0 bool) error {
	mb := &s.mb
	for i := 0; i < 4; i++ {
		var t int
		if isB {
			if s.cabacOn {
				t = s.cabacSubMBTypeB()
			} else {
				t = s.readUE()
			}
			if t > 12 {
				return errInvalid
			}
			if t == 0 {
				mb.directSub |= 1 << i
				mb.subShape[i] = sub8x8
				mb.partPred[i] = 0
			} else {
				mb.subShape[i] = bSubInfo[t].shape
				mb.partPred[i] = bSubInfo[t].pred
			}
		} else {
			if s.cabacOn {
				t = s.cabacSubMBTypeP()
			} else {
				t = s.readUE()
			}
			if t > 3 {
				return errInvalid
			}
			mb.subShape[i] = uint8(t)
			mb.partPred[i] = predL0
		}
	}
	cur.directSub = mb.directSub
	for l := 0; l < 2; l++ {
		for i := 0; i < 4; i++ {
			mb.refIdx[l][i] = -1
			if mb.partPred[i]&(1<<l) == 0 {
				continue
			}
			x, y := (i&1)*2, (i>>1)*2
			if s.h.numRefIdxActive[l] > 1 && !ref0 {
				r, err := s.readRefIdx(l, x, y)
				if err != nil {
					return err
				}
				mb.refIdx[l][i] = r
			} else {
				mb.refIdx[l][i] = 0
			}
			s.setRefRegion(l, x, y, 2, 2, mb.refIdx[l][i])
		}
	}
	var mvd [2][4][4][2]int16
	for l := 0; l < 2; l++ {
		for i := 0; i < 4; i++ {
			if mb.partPred[i]&(1<<l) == 0 {
				continue
			}
			bx, by := (i&1)*2, (i>>1)*2
			sh := mb.subShape[i]
			for j := 0; j < subCount[sh]; j++ {
				x, y, w, h := subRect(sh, j)
				mvd[l][i][j][0], mvd[l][i][j][1] = s.readMvd(l, bx+x, by+y, w, h)
			}
		}
	}
	for i := 0; i < 4; i++ {
		if mb.directSub>>i&1 != 0 {
			s.directMotion(1 << i)
			continue
		}
		bx, by := (i&1)*2, (i>>1)*2
		sh := mb.subShape[i]
		for j := 0; j < subCount[sh]; j++ {
			x, y, w, h := subRect(sh, j)
			for l := 0; l < 2; l++ {
				if mb.partPred[i]&(1<<l) == 0 {
					continue
				}
				s.predFill(l, mb.refIdx[l][i], bx+x, by+y, w, h, mvd[l][i][j][0], mvd[l][i][j][1])
			}
		}
	}
	return nil
}

// interPredict performs motion compensation for the current macroblock and
// returns its mvEdges bits.
func (s *sliceDec) interPredict() uint8 {
	mb := &s.mb
	if mb.flags&mbfDirect16 != 0 {
		return s.mcDirect(15)
	}
	switch mb.shape {
	case part16x16:
		s.mcPartition(0, 0, 4, 4)
		return 0
	case part16x8:
		s.mcPartition(0, 0, 4, 2)
		s.mcPartition(0, 2, 4, 2)
		return 0x20
	case part8x16:
		s.mcPartition(0, 0, 2, 4)
		s.mcPartition(2, 0, 2, 4)
		return 0x02
	default:
		edges := uint8(0x22)
		for i := 0; i < 4; i++ {
			if mb.directSub>>i&1 != 0 {
				edges |= s.mcDirect(1 << i)
				continue
			}
			bx, by := (i&1)*2, (i>>1)*2
			sh := mb.subShape[i]
			switch sh {
			case sub8x4:
				edges |= subEdges[i] & 0x70
			case sub4x8:
				edges |= subEdges[i] & 0x07
			case sub4x4:
				edges |= subEdges[i]
			}
			for j := 0; j < subCount[sh]; j++ {
				x, y, w, h := subRect(sh, j)
				s.mcPartition(bx+x, by+y, w, h)
			}
		}
		return edges
	}
}

// decodeIntraMB decodes an intra macroblock with I mb_type t.
func (s *sliceDec) decodeIntraMB(cur *mbInfo, t int) error {
	mb := &s.mb
	mb.flags = mbfIntra
	cur.flags |= mbfIntra
	if t == 25 {
		return s.decodePCM(cur)
	}
	if t > 25 {
		return errInvalid
	}
	pps := s.h.pps
	if t == 0 {
		mb.flags |= mbfINxN
		cur.flags |= mbfINxN
		if pps.transform8x8 && s.readT8x8() {
			mb.flags |= mbfT8x8
			cur.flags |= mbfT8x8
		}
		n := 16
		if mb.flags&mbfT8x8 != 0 {
			n = 4
		}
		if s.cabacOn {
			s.cabacIntraModes(n)
		} else {
			for i := 0; i < n; i++ {
				mb.prevFlag[i] = s.br.u1() != 0
				if !mb.prevFlag[i] {
					mb.remMode[i] = int8(s.br.u(3))
				}
			}
		}
	} else {
		mb.flags |= mbfI16x16
		cur.flags |= mbfI16x16
		t--
		mb.i16Mode = uint8(t & 3)
		mb.cbp = uint8((t>>2)%3) << 4
		if t >= 12 {
			mb.cbp |= 15
		}
	}
	if s.h.sps.chromaFormatIdc != 0 {
		if s.cabacOn {
			mb.chromaPred = s.cabacChromaPredMode()
		} else {
			mb.chromaPred = uint8(s.br.ue())
		}
		if mb.chromaPred > 3 {
			return errInvalid
		}
	} else {
		mb.chromaPred = 0
	}
	cur.chromaPred = mb.chromaPred
	if mb.flags&mbfINxN != 0 {
		if err := s.parseCBP(true); err != nil {
			return err
		}
	}
	cur.cbp = mb.cbp
	return s.residualAndRecon(cur)
}

// decodePCM decodes an I_PCM macroblock.
func (s *sliceDec) decodePCM(cur *mbInfo) error {
	cur.flags |= mbfPCM
	cur.cbp = 0x2f
	cur.cbf = 0x07ffffff
	cur.qp = 0
	cur.nzMask = 0xffff
	s.lastDQ = 0
	mb := &s.mb
	n := 256
	if s.h.sps.chromaFormatIdc != 0 {
		n = 384
	}
	if s.cabacOn {
		pos := s.cab.start + (s.cab.bitPos()+7)/8
		if pos+n > len(s.cab.buf) {
			return errInvalid
		}
		copy(mb.pcm[:n], s.cab.buf[pos:pos+n])
		s.cab.init(s.cab.buf, pos+n)
	} else {
		s.br.alignByte()
		for i := 0; i < n; i++ {
			mb.pcm[i] = byte(s.br.u(8))
		}
	}
	pic := s.pic
	yo := pic.origin[0] + s.mbY*16*pic.stride[0] + s.mbX*16
	for y := 0; y < 16; y++ {
		copy(pic.planes[0][yo+y*pic.stride[0]:], mb.pcm[y*16:y*16+16])
	}
	co := pic.origin[1] + s.mbY*8*pic.stride[1] + s.mbX*8
	for c := 0; c < 2; c++ {
		for y := 0; y < 8; y++ {
			d := pic.planes[1+c][co+y*pic.stride[1] : co+y*pic.stride[1]+8]
			if n == 384 {
				copy(d, mb.pcm[256+c*64+y*8:256+c*64+y*8+8])
			} else {
				for i := range d {
					d[i] = 128
				}
			}
		}
	}
	// total_coeff of PCM blocks is 16 for CAVLC nC derivation
	st := s.fc.mbW * 4
	base := s.idx4(0, 0)
	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			s.fc.nnz[base+j*st+i] = 16
		}
	}
	cst := s.fc.mbW * 2
	cb := s.mbY*2*cst + s.mbX*2
	for c := 0; c < 2; c++ {
		s.fc.nnzC[c][cb], s.fc.nnzC[c][cb+1] = 16, 16
		s.fc.nnzC[c][cb+cst], s.fc.nnzC[c][cb+cst+1] = 16, 16
	}
	return nil
}
