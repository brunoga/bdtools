package mvc

// Macroblock-adaptive frame/field coding (MBAFF): a frame's macroblocks in
// vertical pairs, each pair two frame macroblocks (16 rows each) or two
// field macroblocks (the pair's 32 rows split by parity). The grids keep a
// pair's macroblocks at their frame positions (top at row 2r, bottom at
// 2r+1); a field macroblock's motion vectors count field rows. Its
// neighbours are found by 6.4.12.2.

// mbfField marks a field macroblock in mbInfo.flags.
const mbfField = 1 << 8

// The neighbouring pairs of the current one.
const (
	pairA = iota // left
	pairB        // above
	pairC        // above right
	pairD        // above left
	pairCur
)

// mbaffState is a slice decoder's MBAFF state.
type mbaffState struct {
	on        bool
	firstPair int
	pairX     int
	pairRow   int
	bottom    bool
	pairAvail [4]bool
	// the macroblocks holding luma (-1,0) and (0,-1) (6.4.10.1), when
	// available
	mbA, mbB int
	// the coded_block_flag bits of the neighbours, as a frame neighbour's
	cbfA, cbfB uint32
	// the slice's reference lists: the frame's, the field
	// macroblocks' by parity; field macroblocks' implicit weights
	frmList [2][]*picture
	frmInfo *sliceRefInfo
	fldList *[2][2][]*picture
	fldInfo *[2]sliceRefInfo
	iw      [2][maxRefsPerList][maxRefsPerList][2]int16
	// the current frame's fields, as views for field macroblocks'
	// samples
	views [2]*picture
	ws    *intraWS
}

// pairPos is the position of the current pair's neighbour n.
func (m *mbaffState) pairPos(n int) (x, row int) {
	switch n {
	case pairA:
		return m.pairX - 1, m.pairRow
	case pairB:
		return m.pairX, m.pairRow - 1
	case pairC:
		return m.pairX + 1, m.pairRow - 1
	case pairD:
		return m.pairX - 1, m.pairRow - 1
	}
	return m.pairX, m.pairRow
}

// pairField reports whether the (available) pair n is a field pair.
func (s *sliceDec) pairField(n int) bool {
	x, row := s.mbf.pairPos(n)
	return s.fc.mbs[2*row*s.fc.mbW+x].flags&mbfField != 0
}

// mbaffNb locates the neighbouring location (xN, yN) of the current
// macroblock (6.4.12.2, Table 6-4; maxW x maxH is 16x16 for luma, 8x8 for
// chroma): the grid index of the macroblock holding it and the location
// in that macroblock.
func (s *sliceDec) mbaffNb(xN, yN, maxW, maxH int) (gi, xW, yW int, ok bool) {
	m := &s.mbf
	top, curFrame := !m.bottom, !s.field
	var x, yM int
	var bot bool
	// frameX reports whether pair x is available and a frame pair
	avail := func(n int) bool { return n == pairCur || m.pairAvail[n] }
	switch {
	case xN < 0 && yN < 0:
		switch {
		case curFrame && top:
			x, bot, yM = pairD, true, yN
		case curFrame:
			x = pairA
			if !avail(x) {
				return 0, 0, 0, false
			}
			if !s.pairField(x) {
				bot, yM = false, yN
			} else {
				bot, yM = true, (yN+maxH)>>1
			}
		case top:
			x = pairD
			if !avail(x) {
				return 0, 0, 0, false
			}
			if !s.pairField(x) {
				bot, yM = true, 2*yN
			} else {
				bot, yM = false, yN
			}
		default:
			x, bot, yM = pairD, true, yN
		}
	case xN < 0 && yN < maxH:
		x = pairA
		if !avail(x) {
			return 0, 0, 0, false
		}
		frameX := !s.pairField(x)
		switch {
		case curFrame && top:
			if frameX {
				bot, yM = false, yN
			} else {
				bot, yM = yN%2 != 0, yN>>1
			}
		case curFrame:
			if frameX {
				bot, yM = true, yN
			} else {
				bot, yM = yN%2 != 0, (yN+maxH)>>1
			}
		case top:
			switch {
			case !frameX:
				bot, yM = false, yN
			case yN < maxH/2:
				bot, yM = false, yN<<1
			default:
				bot, yM = true, yN<<1-maxH
			}
		default:
			switch {
			case !frameX:
				bot, yM = true, yN
			case yN < maxH/2:
				bot, yM = false, yN<<1+1
			default:
				bot, yM = true, yN<<1+1-maxH
			}
		}
	case xN < 0:
		return 0, 0, 0, false
	case xN < maxW && yN < 0:
		switch {
		case curFrame && top:
			x, bot, yM = pairB, true, yN
		case curFrame:
			x, bot, yM = pairCur, false, yN
		case top:
			x = pairB
			if !avail(x) {
				return 0, 0, 0, false
			}
			if !s.pairField(x) {
				bot, yM = true, 2*yN
			} else {
				bot, yM = false, yN
			}
		default:
			x, bot, yM = pairB, true, yN
		}
	case xN < maxW && yN < maxH:
		x, bot, yM = pairCur, m.bottom, yN
	case yN < 0 && xN >= maxW:
		switch {
		case curFrame && top:
			x, bot, yM = pairC, true, yN
		case curFrame:
			return 0, 0, 0, false
		case top:
			x = pairC
			if !avail(x) {
				return 0, 0, 0, false
			}
			if !s.pairField(x) {
				bot, yM = true, 2*yN
			} else {
				bot, yM = false, yN
			}
		default:
			x, bot, yM = pairC, true, yN
		}
	default:
		return 0, 0, 0, false
	}
	if !avail(x) {
		return 0, 0, 0, false
	}
	px, prow := m.pairPos(x)
	W := s.fc.mbW
	gi = 2*prow*W + px
	if bot {
		gi += W
	}
	return gi, (xN + maxW) % maxW, (yM + maxH) % maxH, true
}

// mbaffBlock4 is the grid index of the luma 4x4 block holding the
// neighbouring location (xN, yN), and the macroblock's grid index.
func (s *sliceDec) mbaffBlock4(xN, yN int) (idx, gi int, ok bool) {
	gi, xW, yW, ok := s.mbaffNb(xN, yN, 16, 16)
	if !ok {
		return 0, 0, false
	}
	W := s.fc.mbW
	return ((gi/W)*4+yW/4)*W*4 + (gi%W)*4 + xW/4, gi, true
}

// mbaffChroma4 is mbaffBlock4 for chroma 4x4 blocks (grid stride 2 a
// macroblock).
func (s *sliceDec) mbaffChroma4(xN, yN int) (idx, gi int, ok bool) {
	gi, xW, yW, ok := s.mbaffNb(xN, yN, 8, 8)
	if !ok {
		return 0, 0, false
	}
	W := s.fc.mbW
	return ((gi/W)*2+yW/4)*W*2 + (gi%W)*2 + xW/4, gi, true
}

// setMbPosMBAFF moves to macroblock address addr of an MBAFF frame (two
// a pair) with its pair's field decoding flag.
func (s *sliceDec) setMbPosMBAFF(addr int, field bool) {
	m := &s.mbf
	W := s.fc.mbW
	pair := addr >> 1
	m.bottom = addr&1 != 0
	m.pairX, m.pairRow = pair%W, pair/W
	s.mbX, s.mbY = m.pairX, 2*m.pairRow+addr&1
	s.mbAddr = s.mbY*W + s.mbX
	f := m.firstPair
	m.pairAvail[pairA] = m.pairX > 0 && pair-1 >= f
	m.pairAvail[pairB] = m.pairRow > 0 && pair-W >= f
	m.pairAvail[pairC] = m.pairRow > 0 && m.pairX < W-1 && pair-W+1 >= f
	m.pairAvail[pairD] = m.pairRow > 0 && m.pairX > 0 && pair-W-1 >= f
	s.setMBField(field)
}

// setMBField sets the current macroblock's field decoding flag and what
// follows from it: scans and contexts, samples, neighbours.
func (s *sliceDec) setMBField(field bool) {
	m := &s.mbf
	s.setField(field)
	s.wpShift = 0
	s.iw = &s.implicitW
	if field {
		par := 0
		if m.bottom {
			par = 1
		}
		s.setSamples(m.views[par], m.pairRow)
		if m.fldList != nil {
			s.refList, s.refInfo = m.fldList[par], &m.fldInfo[par]
			s.iw, s.wpShift = &m.iw[par], 1
		}
	} else {
		s.setSamples(s.pic, s.mbY)
		if m.fldList != nil {
			s.refList, s.refInfo = m.frmList, m.frmInfo
		}
	}
	var ok bool
	m.mbA, _, _, s.availA = s.mbaffNb(-1, 0, 16, 16)
	m.mbB, _, _, s.availB = s.mbaffNb(0, -1, 16, 16)
	_, _, _, s.availC = s.mbaffNb(16, -1, 16, 16)
	_, _, _, s.availD = s.mbaffNb(-1, -1, 16, 16)
	_ = ok
	if s.cabacOn {
		s.mbaffCBFMasks()
	}
}

// inferField is the field decoding flag of a pair that codes none (both
// macroblocks skipped; 7.4.4): the left pair's, else the one above's,
// else frame.
func (s *sliceDec) inferField() bool {
	m := &s.mbf
	switch {
	case m.pairAvail[pairA]:
		return s.pairField(pairA)
	case m.pairAvail[pairB]:
		return s.pairField(pairB)
	}
	return false
}

// cabacFieldFlag decodes mb_field_decoding_flag (ctxIdx 70-72: the left
// and above pairs being field pairs).
func (s *sliceDec) cabacFieldFlag() bool {
	m := &s.mbf
	inc := 0
	if m.pairAvail[pairA] && s.pairField(pairA) {
		inc++
	}
	if m.pairAvail[pairB] && s.pairField(pairB) {
		inc++
	}
	return s.cab.decision(&s.ctx[70+inc]) != 0
}

// initMBAFF readies the slice decoder for an MBAFF frame.
func (s *sliceDec) initMBAFF() {
	m := &s.mbf
	m.on = s.h.mbaff
	if !m.on {
		return
	}
	m.firstPair = s.h.firstMb
	if m.ws == nil {
		m.ws = new(intraWS)
	}
	for par := range m.views {
		if m.views[par] == nil {
			m.views[par] = new(picture)
		}
		v := m.views[par]
		v.parent, v.parity = s.pic, par
		v.mbW, v.mbH, v.width, v.height = s.pic.mbW, s.pic.mbH/2, s.pic.width, s.pic.height/2
		v.viewFrame()
	}
}

// intraWS is an intra macroblock's workspace for MBAFF: its neighbouring
// samples gathered by 6.4.12.2 around it (row 0 above, column 0 left), so
// that the prediction reads them as in a frame.
type intraWS struct {
	y      [wsYStride * 17]byte
	cb, cr [wsCStride * 9]byte
}

const (
	wsYStride = 32
	wsCStride = 16
)

// mbSample is the plane offset of sample (x, y) of macroblock gi of
// component c (luma 16x16, chroma 8x8), a field macroblock's in its rows
// of the pair.
func (s *sliceDec) mbSample(gi, c, x, y int) int {
	p := s.pic
	W := s.fc.mbW
	row, col := gi/W, gi%W
	n := 16
	if c > 0 {
		n = 8
	}
	var fy int
	if s.fc.mbs[gi].flags&mbfField != 0 {
		fy = (row>>1)*2*n + row&1 + 2*y
	} else {
		fy = row*n + y
	}
	return p.origin[c] + fy*p.stride[c] + col*n + x
}

// fillWS gathers the current macroblock's neighbouring samples into the
// workspace.
func (s *sliceDec) fillWS() {
	ws := s.mbf.ws
	pl := &s.pic.planes
	for x := -1; x < 24; x++ {
		if gi, xW, yW, ok := s.mbaffNb(x, -1, 16, 16); ok {
			ws.y[x+1] = pl[0][s.mbSample(gi, 0, xW, yW)]
		}
	}
	for y := 0; y < 16; y++ {
		if gi, xW, yW, ok := s.mbaffNb(-1, y, 16, 16); ok {
			ws.y[(y+1)*wsYStride] = pl[0][s.mbSample(gi, 0, xW, yW)]
		}
	}
	for c, d := range [2]*[wsCStride * 9]byte{&ws.cb, &ws.cr} {
		for x := -1; x < 8; x++ {
			if gi, xW, yW, ok := s.mbaffNb(x, -1, 8, 8); ok {
				d[x+1] = pl[1+c][s.mbSample(gi, 1, xW, yW)]
			}
		}
		for y := 0; y < 8; y++ {
			if gi, xW, yW, ok := s.mbaffNb(-1, y, 8, 8); ok {
				d[(y+1)*wsCStride] = pl[1+c][s.mbSample(gi, 1, xW, yW)]
			}
		}
	}
}

// reconIntraMBAFF reconstructs an intra macroblock of an MBAFF frame in
// the workspace, then puts it in place.
func (s *sliceDec) reconIntraMBAFF(cur *mbInfo) {
	ws := s.mbf.ws
	s.fillWS()
	spl, yo, co, ys, cs := s.spl, s.yo, s.co, s.ys, s.cs
	s.spl = [3][]byte{ws.y[:], ws.cb[:], ws.cr[:]}
	s.yo, s.co, s.ys, s.cs = wsYStride+1, wsCStride+1, wsYStride, wsCStride
	s.reconIntra(cur)
	s.spl, s.yo, s.co, s.ys, s.cs = spl, yo, co, ys, cs
	for y := 0; y < 16; y++ {
		copy(spl[0][yo+y*ys:yo+y*ys+16], ws.y[(y+1)*wsYStride+1:])
	}
	if s.h.sps.chromaFormatIdc != 0 {
		for y := 0; y < 8; y++ {
			copy(spl[1][co+y*cs:co+y*cs+8], ws.cb[(y+1)*wsCStride+1:])
			copy(spl[2][co+y*cs:co+y*cs+8], ws.cr[(y+1)*wsCStride+1:])
		}
	}
}

// decodeSliceDataMBAFF is decodeSliceData for an MBAFF frame (7.3.4): the
// macroblocks in pairs, a pair's field decoding flag before its first
// coded macroblock (inferred when both are skipped).
func (s *sliceDec) decodeSliceDataMBAFF() error {
	h := s.h
	total := s.fc.mbW * s.fc.mbH
	addr := 2 * h.firstMb
	var field bool
	pairDone := func() {
		if s.mbX == s.fc.mbW-1 {
			s.fc.pairRowDecoded(s.mbf.pairRow + 1)
		}
	}
	if s.cabacOn {
		s.br.alignByte()
		s.cab.init(s.padded, s.br.bitPos()/8)
		set := 0
		if h.sliceType != sliceI {
			set = h.cabacInitIdc + 1
		}
		initContexts(&s.ctx, set, s.qp)
		var topSkipped, bottomSkip bool
		for {
			if addr >= total {
				return errInvalid
			}
			if addr&1 == 0 {
				s.setMbPosMBAFF(addr, false)
				field = s.inferField()
				s.setMBField(field)
				topSkipped = h.sliceType != sliceI && s.cabacSkipFlag()
				if topSkipped {
					// the bottom's skip flag, and the pair's field flag
					// when the bottom is coded, come next; the bottom
					// sees the top as the skipped macroblock it is
					cur := &s.fc.mbs[s.mbAddr]
					*cur = mbInfo{slice: uint16(s.sliceIdx), flags: mbfAvail | mbfSkip}
					if field {
						cur.flags |= mbfField
					}
					s.setMbPosMBAFF(addr+1, field)
					bottomSkip = s.cabacSkipFlag()
					if !bottomSkip {
						field = s.cabacFieldFlag()
					}
					s.setMbPosMBAFF(addr, field)
					s.decodeSkipMB()
				} else {
					field = s.cabacFieldFlag()
					s.setMBField(field)
					if err := s.decodeMB(); err != nil {
						return err
					}
				}
			} else {
				s.setMbPosMBAFF(addr, field)
				skip := false
				if h.sliceType != sliceI {
					if topSkipped {
						skip = bottomSkip
					} else {
						skip = s.cabacSkipFlag()
					}
				}
				if skip {
					s.decodeSkipMB()
				} else if err := s.decodeMB(); err != nil {
					return err
				}
				pairDone()
				if s.cab.terminate() != 0 {
					break
				}
			}
			addr++
			if s.cab.pos > len(s.cab.buf) {
				return errInvalid
			}
		}
		return nil
	}
	for {
		if h.sliceType != sliceI {
			run := int(s.br.ue())
			for ; run > 0; run-- {
				if addr >= total {
					return errInvalid
				}
				if addr&1 == 0 {
					s.setMbPosMBAFF(addr, false)
					field = s.inferField()
					if run == 1 {
						// the bottom is coded: the pair's flag follows
						// the run
						field = s.br.u1() != 0
					}
					s.setMBField(field)
				} else {
					s.setMbPosMBAFF(addr, field)
				}
				s.decodeSkipMB()
				if addr&1 != 0 {
					pairDone()
				}
				addr++
			}
			if !s.br.moreRBSPData() {
				break
			}
		}
		if addr >= total {
			return errInvalid
		}
		if addr&1 == 0 {
			s.setMbPosMBAFF(addr, false)
			field = s.br.u1() != 0
			s.setMBField(field)
		} else {
			// after a skipped top macroblock its flag came with the run
			s.setMbPosMBAFF(addr, field)
		}
		if err := s.decodeMB(); err != nil {
			return err
		}
		if addr&1 != 0 {
			pairDone()
		}
		addr++
		if !s.br.moreRBSPData() {
			break
		}
		if s.br.overrun() {
			return errInvalid
		}
	}
	return nil
}

// predIntraMode is the predicted Intra4x4/8x8 mode of the block at
// (bx, by) (grid index gi), its neighbours A and B available or not
// (8.3.1.1, 8.3.2.1).
func (s *sliceDec) predIntraMode(gi, bx, by int, aA, aB, eight bool) int8 {
	if !aA || !aB {
		return 2
	}
	if !s.mbf.on {
		g := s.fc.i4
		return min(g[gi-1], g[gi-s.fc.mbW*4])
	}
	return min(s.mbaffIntraMode(bx*4-1, by*4, eight, 1, bx == 0 && by == 2), s.mbaffIntraMode(bx*4, by*4-1, eight, 2, false))
}

// mbaffIntraMode is the intra mode the neighbouring location (xN, yN)
// gives: of its 4x4 block, or, for an 8x8 block's I_4x4 neighbour, of the
// 4x4 block n of the 8x8 block holding it (8-72; n is 3 for the left
// neighbour, a field macroblock, of a frame macroblock's 8x8 block 2:
// block2).
func (s *sliceDec) mbaffIntraMode(xN, yN int, eight bool, n int, block2 bool) int8 {
	gi, xW, yW, ok := s.mbaffNb(xN, yN, 16, 16)
	if !ok {
		return 2
	}
	m := &s.fc.mbs[gi]
	if eight && m.flags&mbfINxN != 0 && m.flags&mbfT8x8 == 0 {
		if n == 1 && block2 && !s.field && m.flags&mbfField != 0 {
			n = 3
		}
		xW, yW = xW&^7+4*(n&1), yW&^7+4*(n>>1)
	}
	W := s.fc.mbW
	return s.fc.i4[((gi/W)*4+yW/4)*W*4+(gi%W)*4+xW/4]
}

// mbaffNC is nC for a 4x4 block (8.2.1 / 9.2.1) in an MBAFF frame: of
// the luma (or a chroma) total_coeff grid g.
func (s *sliceDec) mbaffNC(g []uint8, bx, by int, chroma bool) int {
	nb := s.mbaffBlock4
	if chroma {
		nb = s.mbaffChroma4
	}
	ia, _, aA := nb(bx*4-1, by*4)
	ib, _, aB := nb(bx*4, by*4-1)
	switch {
	case aA && aB:
		return (int(g[ia]) + int(g[ib]) + 1) >> 1
	case aA:
		return int(g[ia])
	case aB:
		return int(g[ib])
	}
	return 0
}

// mbaffCBFMasks computes the coded_block_flag bits the current
// macroblock reads of its neighbours, laid out as a frame neighbour's
// (the left one's right column, the top one's bottom row), from wherever
// 6.4.12.2 finds them.
func (s *sliceDec) mbaffCBFMasks() {
	m := &s.mbf
	m.cbfA, m.cbfB = 0, 0
	mbs := s.fc.mbs
	if s.availA {
		for by := 0; by < 4; by++ {
			gi, xW, yW, _ := s.mbaffNb(-1, by*4, 16, 16)
			m.cbfA |= (mbs[gi].cbf >> ((yW/4)*4 + xW/4) & 1) << (by*4 + 3)
		}
		for by := 0; by < 2; by++ {
			gi, xW, yW, _ := s.mbaffNb(-1, by*4, 8, 8)
			for c := 0; c < 2; c++ {
				m.cbfA |= (mbs[gi].cbf >> (16 + c*4 + (yW/4)*2 + xW/4) & 1) << (16 + c*4 + by*2 + 1)
			}
		}
		m.cbfA |= mbs[m.mbA].cbf & (cbfLumaDC | cbfCbDC | cbfCrDC)
	}
	if s.availB {
		for bx := 0; bx < 4; bx++ {
			gi, xW, yW, _ := s.mbaffNb(bx*4, -1, 16, 16)
			m.cbfB |= (mbs[gi].cbf >> ((yW/4)*4 + xW/4) & 1) << (12 + bx)
		}
		for bx := 0; bx < 2; bx++ {
			gi, xW, yW, _ := s.mbaffNb(bx*4, -1, 8, 8)
			for c := 0; c < 2; c++ {
				m.cbfB |= (mbs[gi].cbf >> (16 + c*4 + (yW/4)*2 + xW/4) & 1) << (16 + c*4 + 2 + bx)
			}
		}
		m.cbfB |= mbs[m.mbB].cbf & (cbfLumaDC | cbfCbDC | cbfCrDC)
	}
}

// mbaffCBF is cbfMasks for MBAFF.
func (s *sliceDec) mbaffCBF(un uint32) (a, b uint32) {
	a, b = un, un
	if s.availA {
		a = s.mbf.cbfA
	}
	if s.availB {
		b = s.mbf.cbfB
	}
	return a, b
}

// cbfLeft and cbfTop are the coded_block_flag condition terms of the
// left and top neighbours' bit.
func (s *sliceDec) cbfLeft(bit uint32) uint32 {
	if s.mbf.on {
		return s.cbfNeighborMask(s.availA, s.mbf.cbfA, bit)
	}
	return s.cbfNeighbor(s.availA, s.leftMB(), bit)
}

func (s *sliceDec) cbfTop(bit uint32) uint32 {
	if s.mbf.on {
		return s.cbfNeighborMask(s.availB, s.mbf.cbfB, bit)
	}
	return s.cbfNeighbor(s.availB, s.topMB(), bit)
}

func (s *sliceDec) cbfNeighborMask(avail bool, mask, bit uint32) uint32 {
	if !avail {
		if s.mb.flags&mbfIntra != 0 {
			return 1
		}
		return 0
	}
	if mask&bit != 0 {
		return 1
	}
	return 0
}

// mbaffCBPNeighbours is the luma coded block pattern bits of the 8x8
// blocks left of and above the current macroblock's, laid out as a frame
// neighbour's (9.3.3.1.1.4).
func (s *sliceDec) mbaffCBPNeighbours() (lumaA, lumaB int) {
	lumaA, lumaB = 0xf, 0xf
	mbs := s.fc.mbs
	if s.availA {
		lumaA = 0
		for k, y := range [2]int{0, 8} {
			gi, xW, yW, _ := s.mbaffNb(-1, y, 16, 16)
			lumaA |= int(mbs[gi].cbp>>((yW/8)*2+xW/8)&1) << (2*k + 1)
		}
	}
	if s.availB {
		lumaB = 0
		for k, x := range [2]int{0, 8} {
			gi, xW, yW, _ := s.mbaffNb(x, -1, 16, 16)
			lumaB |= int(mbs[gi].cbp>>((yW/8)*2+xW/8)&1) << (k + 2)
		}
	}
	return lumaA, lumaB
}

// pairRowDecoded is rowDecoded for MBAFF frames, by rows of pairs: once
// pair row r is decoded, row r-1 is deblocked.
func (fc *frameCtx) pairRowDecoded(r int) {
	if r < 2 || fc.rowsDeblocked != 2*(r-2) {
		return
	}
	fc.deblockPairRow(r - 2)
	fc.rowsDeblocked = 2 * (r - 1)
	fc.publish(2*(r-1) - 2)
}

// deblockPairRow deblocks the macroblocks of pair row r in address
// order.
func (fc *frameCtx) deblockPairRow(r int) {
	for x := 0; x < fc.mbW; x++ {
		fc.deblockMBAFF(x, 2*r)
		fc.deblockMBAFF(x, 2*r+1)
	}
}

// mbaffMotion is nb for MBAFF: the neighbouring block's reference index
// and vector in the current macroblock's terms (8.4.1.3.2: a frame
// neighbour of a field macroblock halves its vertical vector and doubles
// its reference index, a field neighbour of a frame one the reverse).
//
// The block is a partition's neighbour A (left: its top row, y*4) or, with
// d, D (above left: its bottom right sample, the partition's (x-1, y-1)).
func (s *sliceDec) mbaffMotion(l, x, y int, d bool) (int8, mv, bool) {
	xN, yN := x*4, y*4
	if d {
		yN += 3
	}
	if x < 0 {
		xN = -1
	}
	if y < 0 {
		yN = -1
	}
	idx, gi, ok := s.mbaffBlock4(xN, yN)
	if !ok {
		return -1, mv{}, false
	}
	r, m := s.pic.refs[l][idx], s.pic.mvs[l][idx]
	if r >= 0 {
		switch nbField := s.fc.mbs[gi].flags&mbfField != 0; {
		case s.field && !nbField:
			m.y /= 2
			r *= 2
		case !s.field && nbField:
			m.y *= 2
			r >>= 1
		}
	}
	return r, m, true
}

// numRef is list l's num_ref_idx_active for the current macroblock: an
// MBAFF field macroblock's lists count fields.
func (s *sliceDec) numRef(l int) int {
	if s.mbf.on && s.field {
		return 2 * s.h.numRefIdxActive[l]
	}
	return s.h.numRefIdxActive[l]
}

// mbaffRefIdxCond is refIdxCond for MBAFF (9.3.3.1.1.6).
func (s *sliceDec) mbaffRefIdxCond(l, x, y int) int {
	xN, yN := x*4, y*4
	if x < 0 {
		xN = -1
	}
	if y < 0 {
		yN = -1
	}
	idx, gi, ok := s.mbaffBlock4(xN, yN)
	if !ok {
		return 0
	}
	m := &s.fc.mbs[gi]
	if m.flags&(mbfSkip|mbfIntra) != 0 {
		return 0
	}
	W := s.fc.mbW
	bx, by := idx%(W*4)%4, idx/(W*4)%4
	if m.directSub>>((by>>1)*2+(bx>>1))&1 != 0 {
		return 0
	}
	r := s.pic.refs[l][idx]
	if !s.field && m.flags&mbfField != 0 {
		r >>= 1
	}
	if r > 0 {
		return 1
	}
	return 0
}

// mbaffAbsMvd is absMvdN for MBAFF (9.3.3.1.1.7).
func (s *sliceDec) mbaffAbsMvd(l, comp, x, y int) int {
	xN, yN := x*4, y*4
	if x < 0 {
		xN = -1
	}
	if y < 0 {
		yN = -1
	}
	idx, gi, ok := s.mbaffBlock4(xN, yN)
	if !ok {
		return 0
	}
	v := int(s.fc.mvd[l][idx][comp])
	if comp == 1 {
		switch nbField := s.fc.mbs[gi].flags&mbfField != 0; {
		case !s.field && nbField:
			v *= 2
		case s.field && !nbField:
			v /= 2
		}
	}
	return v
}

// colRef is a co-located block's reference picture: a frame or a field
// of it.
type colRef struct {
	frame    int32 // the frame's id
	parity   int   // the field's parity, or -1 for the frame
	colField bool  // the co-located macroblock is a field one
}

// mbaffColocated finds the co-located 4x4 block (8.4.1.2.1) of the
// current macroblock's block (x, y) in an MBAFF frame (direct_8x8_inference
// holds: the corners): its reference index, vector (in its own units: the
// spatial mode's zero test reads it so) and list, and the picture it
// references.
func (s *sliceDec) mbaffColocated(x, y int) (rc int8, mc mv, lc int, ref colRef) {
	m := &s.mbf
	col := m.frmList[1][0]
	W := s.fc.mbW
	st4 := W * 4
	x8, y8 := x>>1, y>>1
	r := m.pairRow
	par := 0
	if m.bottom {
		par = 1
	}
	colParity := 0
	if iabs(col.topPOC-s.pic.poc) >= iabs(col.botPOC-s.pic.poc) {
		colParity = 1
	}
	var cp *picture // the picture holding the co-located block
	var i int
	colField := false // the co-located macroblock is a field one
	colPar := 0       // and its parity
	switch {
	case col.asFields && !s.field:
		cp = col.field(colParity)
		i = (r*4+2*(s.mbY&1)+y8)*st4 + s.mbX*4 + x8*3
		colField, colPar = true, colParity
	case col.asFields:
		cp = col.field(par)
		i = (r*4+y8*3)*st4 + s.mbX*4 + x8*3
		colField, colPar = true, par
	default:
		cp = col
		pairField := col.fieldMB[2*r*W+s.mbX]
		switch {
		case !s.field && !pairField:
			i = (s.mbY*4+y8*3)*st4 + s.mbX*4 + x8*3
		case !s.field:
			i = ((2*r+colParity)*4+2*(s.mbY&1)+y8)*st4 + s.mbX*4 + x8*3
			colField, colPar = true, colParity
		case !pairField:
			i = (8*r+6*y8)*st4 + s.mbX*4 + x8*3
		default:
			i = (s.mbY*4+y8*3)*st4 + s.mbX*4 + x8*3
			colField, colPar = true, par
		}
	}
	rc, lc = cp.refs[0][i], 0
	if rc < 0 {
		rc, lc = cp.refs[1][i], 1
	}
	if rc < 0 {
		return -1, mv{}, 0, colRef{}
	}
	mc = cp.mvs[lc][i]
	cmb := (i/st4)/4*W + (i%st4)/4
	id := cp.sliceRef(int(cp.mbSlice[cmb])).ids[lc]
	switch {
	case cp.parent != nil:
		// a field picture's references are fields
		f := id[rc]
		ref = colRef{frame: (f & (1<<30 - 1)) >> 1, parity: int(f & 1)}
	case colField:
		// an MBAFF field macroblock's index counts fields
		ref = colRef{frame: id[rc>>1], parity: colPar ^ int(rc&1)}
	default:
		ref = colRef{frame: id[rc], parity: -1}
	}
	ref.colField = colField
	return rc, mc, lc, ref
}

// directTemporalMBAFF is directTemporal for MBAFF frames (8.4.1.2.3).
func (s *sliceDec) directTemporalMBAFF(mask uint8) {
	m := &s.mbf
	par := 0
	if m.bottom {
		par = 1
	}
	for b8 := 0; b8 < 4; b8++ {
		if mask>>b8&1 == 0 {
			continue
		}
		bx, by := (b8&1)*2, (b8>>1)*2
		rc, mc, _, ref := s.mbaffColocated(bx, by)
		// the vertical vector in the current macroblock's units
		switch {
		case ref.colField && !s.field:
			mc.y *= 2
		case !ref.colField && s.field:
			mc.y /= 2
		}
		var ref0 int8
		if rc >= 0 {
			// MapColToList0: the frame holding the referenced picture,
			// as a field macroblock's index: the field of its parity
			// first
			f := 0
			for k, id := range m.frmInfo.ids[0][:len(m.frmList[0])] {
				if id == ref.frame {
					f = k
					break
				}
			}
			ref0 = int8(f)
			if s.field {
				ref0 = int8(f << 1)
				if ref.parity >= 0 && ref.parity != par {
					ref0++
				}
			}
		}
		// the distance scaling: of the fields of the macroblock's
		// parity for a field macroblock
		info := m.frmInfo
		cur := s.pic.poc
		if s.field {
			info = &m.fldInfo[par]
			cur = s.pic.topPOC
			if par == 1 {
				cur = s.pic.botPOC
			}
		}
		var m0, m1 mv
		p0, p1 := info.poc[0][ref0], info.poc[1][0]
		td := clip3(-128, 127, p1-p0)
		if info.lt[0][ref0] || td == 0 {
			m0 = mc
		} else {
			tb := clip3(-128, 127, cur-p0)
			tx := (16384 + iabs(td/2)) / td
			dsf := clip3(-1024, 1023, (tb*tx+32)>>6)
			m0 = mv{int16((dsf*int32(mc.x) + 128) >> 8), int16((dsf*int32(mc.y) + 128) >> 8)}
			m1 = mv{m0.x - mc.x, m0.y - mc.y}
		}
		for j := 0; j < 2; j++ {
			for i := 0; i < 2; i++ {
				idx := s.idx4(bx+i, by+j)
				s.pic.refs[0][idx], s.pic.mvs[0][idx] = ref0, m0
				s.pic.refs[1][idx], s.pic.mvs[1][idx] = 0, m1
			}
		}
	}
}
