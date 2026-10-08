package vc1

// Coding sets: which AC table a block's coefficients use.
const (
	csHighMotIntra = iota
	csHighMotInter
	csLowMotIntra
	csLowMotInter
	csMidRateIntra
	csMidRateInter
	csHighRateIntra
	csHighRateInter
)

// acCoeff reads a coefficient: the zeros before it, its value, and whether
// it is the block's last.
func (m *mbContext) acCoeff(set int) (last bool, run, level int, err error) {
	r := m.r
	t := acVLC[set]
	index, ok := t.read(r)
	if !ok {
		return false, 0, 0, errStream
	}
	escape := int(acSizes[set]) - 1
	sign := 0
	if index != escape {
		run, level = int(acRunLevel[set][index][0]), int(acRunLevel[set][index][1])
		last = index >= int(acLastIndex[set]) || r.left() < 0
		sign = r.u(1)
	} else {
		mode := r.v210()
		if mode != 2 {
			index, ok = t.read(r)
			if !ok || index >= escape {
				return false, 0, 0, errStream
			}
			run, level = int(acRunLevel[set][index][0]), int(acRunLevel[set][index][1])
			last = index >= int(acLastIndex[set])
			if mode == 0 {
				if last {
					level += int(acLastDeltaLevel[set][run])
				} else {
					level += int(acDeltaLevel[set][run])
				}
			} else {
				if last {
					run += int(uint8(acLastDeltaRun[set][level])) + 1
				} else {
					run += int(uint8(acDeltaRun[set][level])) + 1
				}
			}
			sign = r.u(1)
		} else {
			last = r.u(1) == 1
			if m.esc3Level == 0 {
				if m.d.ph.pq < 8 || m.d.ph.dquantFrm {
					m.esc3Level = r.u(3)
					if m.esc3Level == 0 {
						m.esc3Level = r.u(2) + 8
					}
				} else {
					m.esc3Level = r.unary(1, 6) + 2
				}
				m.esc3Run = 3 + r.u(2)
			}
			run = r.u(m.esc3Run)
			sign = r.u(1)
			level = r.u(m.esc3Level)
		}
	}
	if sign != 0 {
		level = -level
	}
	return last, run, level, nil
}

// readDC reads a DC differential.
func (m *mbContext) readDC(n, quant int) (int, error) {
	r := m.r
	chroma := 0
	if n >= 4 {
		chroma = 1
	}
	dc, ok := dcVLC[m.d.ph.dcTable][chroma].read(r)
	if !ok {
		return 0, errStream
	}
	if dc != 0 {
		mm := 0
		if quant == 1 || quant == 2 {
			mm = 3 - quant
		}
		if dc == 119 {
			dc = r.u(8 + mm)
		} else if mm != 0 {
			dc = dc<<mm + r.u(mm) - (1<<mm - 1)
		}
		if r.u(1) == 1 {
			dc = -dc
		}
	}
	return dc, nil
}

// scaleQ rescales a predictor quantized with q2 to q1's scale (DQSCALE).
func scaleQ(v, q2, q1 int) int {
	return int(int32(int64(v)*int64(q2)*int64(dqScale[q1-1])+0x20000)) >> 18
}

func iabs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// predDC predicts block n's DC from the left (c), top (a) and top-left (b)
// blocks', each rescaled to the macroblock's quantizer: it gives the
// prediction and whether it is from the left.
func (m *mbContext) predDC(n int, aAvail, cAvail bool) (int, bool) {
	q1 := iabs(int(m.q[m.mbi]))
	dqi := int(dcScaleTable[q1]) - 1
	if dqi < 0 {
		return 0, false
	}
	var a, b, c int
	if n < 4 {
		xy := m.bi[n]
		a, b, c = int(m.dcY[xy-m.bw]), int(m.dcY[xy-m.bw-1]), int(m.dcY[xy-1])
	} else {
		dc := m.dcC[n-4]
		xy := m.mbi
		a, b, c = int(dc[xy-m.cw]), int(dc[xy-m.cw-1]), int(dc[xy-1])
	}
	scale := func(v, mb int) int {
		q2 := iabs(int(m.q[mb]))
		if q2 != 0 && q2 != q1 {
			return int(int32(int64(v)*int64(dcScaleTable[q2])*int64(dqScale[dqi])+0x20000)) >> 18
		}
		return v
	}
	if cAvail && n != 1 && n != 3 {
		c = scale(c, m.mbi-1)
	}
	if aAvail && n != 2 && n != 3 {
		a = scale(a, m.mbi-m.cw)
	}
	if aAvail && cAvail && n != 3 {
		off := m.mbi
		if n != 1 {
			off--
		}
		if n != 2 {
			off -= m.cw
		}
		b = scale(b, off)
	}
	switch {
	case cAvail && (!aAvail || iabs(a-b) <= iabs(b-c)):
		return c, true
	case aAvail:
		return a, false
	}
	return 0, true
}

// dcSlot is where block n's DC predictor is kept.
func (m *mbContext) dcSlot(n int) *int16 {
	if n < 4 {
		return &m.dcY[m.bi[n]]
	}
	return &m.dcC[n-4][m.mbi]
}

// acSlot is block n's AC predictors (the first column's then the first
// row's), and its neighbour's to the left or above.
func (m *mbContext) acSlots(n int, left bool) (cur, nb *[16]int16) {
	if n < 4 {
		xy := m.bi[n]
		cur = &m.acY[xy]
		if left {
			nb = &m.acY[xy-1]
		} else {
			nb = &m.acY[xy-m.bw]
		}
		return
	}
	ac := m.acC[n-4]
	cur = &ac[m.mbi]
	if left {
		nb = &ac[m.mbi-1]
	} else {
		nb = &ac[m.mbi-m.cw]
	}
	return
}

// acPredQ gives the quantizers (as AC prediction scales them) of the
// macroblock and of the neighbour predicted from, 0 when the neighbour's is
// not to be used.
func (m *mbContext) acPredQ(n int, left, aAvail, cAvail bool) (q1, q2 int) {
	q1 = int(m.q[m.mbi])
	switch {
	case n == 3:
		q2 = q1
	case left:
		if n == 1 {
			q2 = q1
		} else if cAvail {
			q2 = int(m.q[m.mbi-1])
		}
	default:
		if n == 2 {
			q2 = q1
		} else if aAvail {
			q2 = int(m.q[m.mbi-m.cw])
		}
	}
	halfpq := 0
	if m.d.ph.halfpq {
		halfpq = 1
	}
	conv := func(q int) int {
		if q < 0 {
			return -q*2 - 1
		}
		return q*2 + halfpq - 1
	}
	q1 = conv(q1)
	if q2 != 0 {
		q2 = conv(q2)
	}
	return q1, q2
}

// intraBlock decodes intra block n into blk (coefficients, dequantized):
// an I picture's (inI) or an intra block of a P or B picture.
func (m *mbContext) intraBlock(blk *[64]int16, n int, coded bool, set, mquant int, aAvail, cAvail, inI bool) error {
	*blk = [64]int16{}
	p := &m.d.ph
	quant := iabs(mquant)
	if !inI {
		quant = min(quant, 31)
	}
	dcScale := int(dcScaleTable[quant])
	dc, err := m.readDC(n, quant)
	if err != nil {
		return err
	}
	pred, left := m.predDC(n, aAvail, cAvail)
	dc += pred
	*m.dcSlot(n) = int16(dc)
	blk[0] = int16(dc * dcScale)
	usePred := m.acPredFlag
	if !inI {
		if !aAvail {
			left = true
		}
		if !cAvail {
			left = false
		}
	}
	if !aAvail && !cAvail {
		usePred = false
	}
	scale := quant * 2
	if mquant >= 0 && p.halfpq {
		scale++
	}
	cur, nb := m.acSlots(n, left)
	q1, q2 := m.acPredQ(n, left, aAvail, cAvail)
	// Which half of the predictors: the first column's (left) or row's.
	off, step := 0, 8
	if !left {
		off, step = 8, 1
	}
	if coded {
		var zz *[64]uint8
		switch {
		case !inI && p.fcm == progressive:
			zz = &wmv1Scan[0]
		case !inI:
			if usePred && p.fcm == ilaceFrame {
				if left {
					zz = &wmv1Scan[3]
				} else {
					zz = &wmv1Scan[2]
				}
			} else {
				zz = &zz8x8Interlaced
			}
		case m.acPredFlag:
			switch {
			case !usePred && p.fcm == ilaceFrame:
				zz = &zz8x8Interlaced
			case left:
				zz = &wmv1Scan[3]
			default:
				zz = &wmv1Scan[2]
			}
		case p.fcm != ilaceFrame:
			zz = &wmv1Scan[1]
		default:
			zz = &zz8x8Interlaced
		}
		for i := 1; ; i++ {
			last, run, level, err := m.acCoeff(set)
			if err != nil {
				return err
			}
			i += run
			if i > 63 {
				break
			}
			blk[zz[i]] = int16(level)
			if last {
				break
			}
		}
		if usePred {
			if q1 < 1 {
				return errStream
			}
			if q2 != 0 && q1 != q2 {
				for k := 1; k < 8; k++ {
					blk[k*step] += int16(scaleQ(int(nb[k+off]), q2, q1))
				}
			} else {
				for k := 1; k < 8; k++ {
					blk[k*step] += nb[k+off]
				}
			}
		}
		for k := 1; k < 8; k++ {
			cur[k] = blk[k*8]
			cur[k+8] = blk[k]
		}
		for k := 1; k < 64; k++ {
			if blk[k] != 0 {
				blk[k] *= int16(scale)
				if !p.pquantizer {
					if blk[k] < 0 {
						blk[k] -= int16(quant)
					} else {
						blk[k] += int16(quant)
					}
				}
			}
		}
		return nil
	}
	*cur = [16]int16{}
	if !usePred {
		return nil
	}
	copy(cur[off:off+8], nb[off:off+8])
	if q1 < 1 {
		return errStream
	}
	if q2 != 0 && q1 != q2 {
		for k := 1; k < 8; k++ {
			cur[k+off] = int16(scaleQ(int(cur[k+off]), q2, q1))
		}
	}
	for k := 1; k < 8; k++ {
		v := cur[k+off] * int16(scale)
		if !p.pquantizer && v != 0 {
			if v < 0 {
				v -= int16(quant)
			} else {
				v += int16(quant)
			}
		}
		blk[k*step] = v
	}
	return nil
}

// interBlock decodes inter block n's residual and adds it to dst (stride),
// giving which of its 4x4 quarters are coded (bit 3 top left, 2 top
// right, 1 bottom left, 0 bottom right) and its transform type.
func (m *mbContext) interBlock(blk *[64]int16, n, mquant, ttmb int, firstBlock bool, dst []byte, stride int) (pat, tt int, err error) {
	*blk = [64]int16{}
	r := m.r
	p := &m.d.ph
	quant := iabs(mquant)
	ttblk := ttmb & 7
	if ttmb == -1 {
		v, ok := ttblkVLC[p.ttIndex].read(r)
		if !ok {
			return 0, 0, errStream
		}
		ttblk = int(ttblkToTT[p.ttIndex][v])
	}
	subblkpat := 0
	if ttblk == tt4x4 {
		v, ok := subblkpatVLC[p.ttIndex].read(r)
		if !ok {
			return 0, 0, errStream
		}
		subblkpat = ^(v + 1)
	}
	if ttblk != tt8x8 && ttblk != tt4x4 && (p.ttmbf || (ttmb != -1 && ttmb&8 != 0 && !firstBlock)) {
		subblkpat = r.v012()
		if subblkpat != 0 {
			subblkpat ^= 3
		}
		if ttblk == tt8x4Top || ttblk == tt8x4Bottom {
			ttblk = tt8x4
		}
		if ttblk == tt4x8Right || ttblk == tt4x8Left {
			ttblk = tt4x8
		}
	}
	scale := quant * 2
	if mquant >= 0 && p.halfpq {
		scale++
	}
	if ttblk == tt8x4Top || ttblk == tt8x4Bottom {
		subblkpat = 2
		if ttblk == tt8x4Top {
			subblkpat = 1
		}
		ttblk = tt8x4
	}
	if ttblk == tt4x8Right || ttblk == tt4x8Left {
		subblkpat = 2
		if ttblk == tt4x8Left {
			subblkpat = 1
		}
		ttblk = tt4x8
	}
	ilace := p.fcm != progressive
	put := func(i int, v int) {
		x := v * scale
		if !p.pquantizer && x != 0 {
			if x < 0 {
				x -= quant
			} else {
				x += quant
			}
		}
		blk[i] = int16(x)
	}
	// coeffs reads a sub-block's coefficients through scan (offset off),
	// giving how far the scan went.
	coeffs := func(scan []uint8, off int) (int, error) {
		i := 0
		for {
			last, run, level, err := m.acCoeff(m.codingSet2)
			if err != nil {
				return 0, err
			}
			i += run
			if i >= len(scan) {
				return i, nil
			}
			put(int(scan[i])+off, level)
			i++
			if last {
				return i, nil
			}
		}
	}
	switch ttblk {
	case tt8x8:
		pat = 0xf
		scan := wmv1Scan[0][:]
		if ilace {
			scan = zz8x8Interlaced[:]
		}
		i, err := coeffs(scan, 0)
		if err != nil {
			return 0, 0, err
		}
		if i == 1 {
			addDC8x8(dst, stride, int(blk[0]))
		} else {
			idct8x8(blk)
			addBlock(dst, stride, blk[:], 8, 8)
		}
	case tt4x4:
		pat = ^subblkpat & 0xf
		scan := zz4x4Progressive[:]
		if ilace {
			scan = zz4x4Interlaced[:]
		}
		for j := range 4 {
			if subblkpat&(1<<(3-j)) != 0 {
				continue
			}
			off := (j&1)*4 + (j&2)*16
			i, err := coeffs(scan, off)
			if err != nil {
				return 0, 0, err
			}
			d := dst[(j&1)*4+(j&2)*2*stride:]
			if i == 1 {
				dc := (17*int(blk[off]) + 4) >> 3
				addDC(d, stride, 4, 4, (17*dc+64)>>7)
			} else {
				idct4x4(blk[off:], d, stride)
			}
		}
	case tt8x4:
		pat = ^((subblkpat&2)*6 + (subblkpat&1)*3) & 0xf
		scan := zz8x4Progressive[:]
		if ilace {
			scan = zz8x4Interlaced[:]
		}
		for j := range 2 {
			if subblkpat&(1<<(1-j)) != 0 {
				continue
			}
			off := j * 32
			i, err := coeffs(scan, off)
			if err != nil {
				return 0, 0, err
			}
			d := dst[j*4*stride:]
			if i == 1 {
				dc := (3*int(blk[off]) + 1) >> 1
				addDC(d, stride, 8, 4, (17*dc+64)>>7)
			} else {
				idct8x4(blk[off:], d, stride)
			}
		}
	case tt4x8:
		pat = ^(subblkpat * 5) & 0xf
		scan := zz4x8Progressive[:]
		if ilace {
			scan = zz4x8Interlaced[:]
		}
		for j := range 2 {
			if subblkpat&(1<<(1-j)) != 0 {
				continue
			}
			off := j * 4
			i, err := coeffs(scan, off)
			if err != nil {
				return 0, 0, err
			}
			d := dst[j*4:]
			if i == 1 {
				dc := (17*int(blk[off]) + 4) >> 3
				addDC(d, stride, 4, 8, (12*dc+64)>>7)
			} else {
				idct4x8(blk[off:], d, stride)
			}
		}
	}
	return pat, ttblk, nil
}

func addDC8x8(dst []byte, stride, dc int) {
	dc = (3*dc + 1) >> 1
	dc = (3*dc + 16) >> 5
	addDC(dst, stride, 8, 8, dc)
}

func addDC(dst []byte, stride, w, h, dc int) {
	for y := range h {
		row := dst[y*stride : y*stride+w]
		for x := range row {
			row[x] = clip8(int(row[x]) + dc)
		}
	}
}

// addBlock adds a w x h residual (rows of 8) to dst, clamped.
func addBlock(dst []byte, stride int, blk []int16, w, h int) {
	for y := range h {
		row := dst[y*stride : y*stride+w]
		b := blk[y*8:]
		for x := range row {
			row[x] = clip8(int(row[x]) + int(b[x]))
		}
	}
}

// putSigned stores an intra block's samples (centred on 0).
func putSigned(dst []byte, stride int, blk *[64]int16) {
	for y := range 8 {
		row := dst[y*stride : y*stride+8]
		b := blk[y*8:]
		for x := range row {
			row[x] = clip8(int(b[x]) + 128)
		}
	}
}

// The inverse transforms (8.4.5): rows first, then columns, in integers.

func idct8(s0, s1, s2, s3, s4, s5, s6, s7, rnd int) (d [8]int) {
	t1 := 12*(s0+s4) + rnd
	t2 := 12*(s0-s4) + rnd
	t3 := 16*s2 + 6*s6
	t4 := 6*s2 - 16*s6
	t5, t6, t7, t8 := t1+t3, t2+t4, t2-t4, t1-t3
	u1 := 16*s1 + 15*s3 + 9*s5 + 4*s7
	u2 := 15*s1 - 4*s3 - 16*s5 - 9*s7
	u3 := 9*s1 - 16*s3 + 4*s5 + 15*s7
	u4 := 4*s1 - 9*s3 + 15*s5 - 16*s7
	return [8]int{t5 + u1, t6 + u2, t7 + u3, t8 + u4, t8 - u4, t7 - u3, t6 - u2, t5 - u1}
}

// idct8x8 transforms blk in place.
func idct8x8(blk *[64]int16) {
	var tmp [64]int32
	for y := range 8 {
		s := blk[y*8 : y*8+8 : y*8+8]
		if s[0]|s[1]|s[2]|s[3]|s[4]|s[5]|s[6]|s[7] == 0 {
			continue // a row of zeros stays zeros
		}
		s0, s1, s2, s3 := int32(s[0]), int32(s[1]), int32(s[2]), int32(s[3])
		s4, s5, s6, s7 := int32(s[4]), int32(s[5]), int32(s[6]), int32(s[7])
		t1 := 12*(s0+s4) + 4
		t2 := 12*(s0-s4) + 4
		t3 := 16*s2 + 6*s6
		t4 := 6*s2 - 16*s6
		t5, t6, t7, t8 := t1+t3, t2+t4, t2-t4, t1-t3
		u1 := 16*s1 + 15*s3 + 9*s5 + 4*s7
		u2 := 15*s1 - 4*s3 - 16*s5 - 9*s7
		u3 := 9*s1 - 16*s3 + 4*s5 + 15*s7
		u4 := 4*s1 - 9*s3 + 15*s5 - 16*s7
		d := tmp[y*8 : y*8+8 : y*8+8]
		d[0] = int32(int16((t5 + u1) >> 3))
		d[1] = int32(int16((t6 + u2) >> 3))
		d[2] = int32(int16((t7 + u3) >> 3))
		d[3] = int32(int16((t8 + u4) >> 3))
		d[4] = int32(int16((t8 - u4) >> 3))
		d[5] = int32(int16((t7 - u3) >> 3))
		d[6] = int32(int16((t6 - u2) >> 3))
		d[7] = int32(int16((t5 - u1) >> 3))
	}
	for x := range 8 {
		s0, s1, s2, s3 := tmp[x], tmp[8+x], tmp[16+x], tmp[24+x]
		s4, s5, s6, s7 := tmp[32+x], tmp[40+x], tmp[48+x], tmp[56+x]
		t1 := 12*(s0+s4) + 64
		t2 := 12*(s0-s4) + 64
		t3 := 16*s2 + 6*s6
		t4 := 6*s2 - 16*s6
		t5, t6, t7, t8 := t1+t3, t2+t4, t2-t4, t1-t3
		u1 := 16*s1 + 15*s3 + 9*s5 + 4*s7
		u2 := 15*s1 - 4*s3 - 16*s5 - 9*s7
		u3 := 9*s1 - 16*s3 + 4*s5 + 15*s7
		u4 := 4*s1 - 9*s3 + 15*s5 - 16*s7
		blk[x] = int16((t5 + u1) >> 7)
		blk[8+x] = int16((t6 + u2) >> 7)
		blk[16+x] = int16((t7 + u3) >> 7)
		blk[24+x] = int16((t8 + u4) >> 7)
		blk[32+x] = int16((t8 - u4 + 1) >> 7)
		blk[40+x] = int16((t7 - u3 + 1) >> 7)
		blk[48+x] = int16((t6 - u2 + 1) >> 7)
		blk[56+x] = int16((t5 - u1 + 1) >> 7)
	}
}

func idct4(s0, s1, s2, s3, rnd int) [4]int {
	t1 := 17*(s0+s2) + rnd
	t2 := 17*(s0-s2) + rnd
	t3 := 22*s1 + 10*s3
	t4 := 22*s3 - 10*s1
	return [4]int{t1 + t3, t2 - t4, t2 + t4, t1 - t3}
}

// idct8x4 transforms the 8 wide, 4 high block at blk (rows of 8) and adds
// it to dst.
func idct8x4(blk []int16, dst []byte, stride int) {
	var tmp [32]int
	for y := range 4 {
		s := blk[y*8 : y*8+8]
		d := idct8(int(s[0]), int(s[1]), int(s[2]), int(s[3]), int(s[4]), int(s[5]), int(s[6]), int(s[7]), 4)
		for x := range 8 {
			tmp[y*8+x] = int(int16(d[x] >> 3))
		}
	}
	for x := range 8 {
		d := idct4(tmp[x], tmp[8+x], tmp[16+x], tmp[24+x], 64)
		for y := range 4 {
			p := &dst[y*stride+x]
			*p = clip8(int(*p) + d[y]>>7)
		}
	}
}

// idct4x8 is the 4 wide, 8 high transform.
func idct4x8(blk []int16, dst []byte, stride int) {
	var tmp [64]int
	for y := range 8 {
		s := blk[y*8 : y*8+4]
		d := idct4(int(s[0]), int(s[1]), int(s[2]), int(s[3]), 4)
		for x := range 4 {
			tmp[y*8+x] = int(int16(d[x] >> 3))
		}
	}
	for x := range 4 {
		d := idct8(tmp[x], tmp[8+x], tmp[16+x], tmp[24+x], tmp[32+x], tmp[40+x], tmp[48+x], tmp[56+x], 64)
		for y := range 8 {
			v := d[y]
			if y >= 4 {
				v++
			}
			p := &dst[y*stride+x]
			*p = clip8(int(*p) + v>>7)
		}
	}
}

// idct4x4 is the 4 x 4 transform.
func idct4x4(blk []int16, dst []byte, stride int) {
	var tmp [32]int
	for y := range 4 {
		s := blk[y*8 : y*8+4]
		d := idct4(int(s[0]), int(s[1]), int(s[2]), int(s[3]), 4)
		for x := range 4 {
			tmp[y*8+x] = int(int16(d[x] >> 3))
		}
	}
	for x := range 4 {
		d := idct4(tmp[x], tmp[8+x], tmp[16+x], tmp[24+x], 64)
		for y := range 4 {
			p := &dst[y*stride+x]
			*p = clip8(int(*p) + d[y]>>7)
		}
	}
}
