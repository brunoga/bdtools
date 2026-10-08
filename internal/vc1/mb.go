package vc1

// mbContext is the macroblock layer's state: the macroblock being decoded
// and what its neighbours left (predictors, on grids with a border).
type mbContext struct {
	d *Decoder
	r *bits

	bw, cw        int // strides of the block grid (2 a macroblock) and macroblock grid
	blkLen, mbLen int

	// The macroblock being decoded.
	mbx, mby   int
	mbi        int    // its index on the macroblock grid
	bi         [4]int // its luma blocks' on the block grid
	firstLine  bool   // the first row of its slice
	acPredFlag bool
	cmv        [2][4]mv // its vectors, [direction][block]

	esc3Level, esc3Run    int
	codingSet, codingSet2 int

	// Grids.
	intraY []bool  // block grid
	intraC []bool  // macroblock grid
	mvs    [2][]mv // vectors on the block grid: forward, and backward (B) or direct (P)
	bmv    []mv    // backward vectors of a B picture
	dcY    []int16
	dcC    [2][]int16
	acY    [][16]int16
	acC    [2][][16]int16
	coded  []uint8
	q      []int8 // macroblock quantizers (negative: without HALFQP)

	// For the loop filter, a macroblock's: coded 4x4 quarters (4 bits a
	// block), transform types (4 bits a block), intra blocks (a bit each),
	// chroma vector.
	cbp     []uint32
	tt      []uint32
	isIntra []uint8
	uvMV    []mv

	// Field pictures' and interlaced frames': which vectors (block grid,
	// and macroblock grid for chroma) are from the opposite field, and
	// which blocks have field vectors.
	mvfY      [2][]bool
	mvfC      [2][]bool
	blkMVType []bool

	// The picture (a frame, or a field: every other line) being decoded,
	// its macroblock rows, and where its vectors are on the grids.
	vy, vcb, vcr []byte
	vsy, vsc     int
	rows         int
	blkOff       int
	mbOff        int

	// Intra blocks' samples, waiting for overlap smoothing: of each
	// macroblock, its blocks in slot order (see slot).
	blocks [][6 * 64]int16
	blk    [64]int16

	sliceTop []bool // macroblock rows that start a slice

	mcBuf [19 * 19]byte
}

func (m *mbContext) init(d *Decoder) {
	m.d = d
	m.bw = 2*d.mbw + 2
	m.cw = d.mbw + 2
	m.blkLen = (2*d.mbhA + 3) * m.bw
	m.mbLen = (d.mbhA + 3) * m.cw
	m.intraY = make([]bool, m.blkLen)
	m.intraC = make([]bool, m.mbLen)
	m.mvs[0] = make([]mv, m.blkLen)
	m.bmv = make([]mv, m.blkLen)
	m.dcY = make([]int16, m.blkLen)
	m.dcC = [2][]int16{make([]int16, m.mbLen), make([]int16, m.mbLen)}
	m.acY = make([][16]int16, m.blkLen)
	m.acC = [2][][16]int16{make([][16]int16, m.mbLen), make([][16]int16, m.mbLen)}
	m.coded = make([]uint8, m.blkLen)
	m.q = make([]int8, m.mbLen)
	m.cbp = make([]uint32, m.mbLen)
	m.tt = make([]uint32, m.mbLen)
	m.isIntra = make([]uint8, m.mbLen)
	m.uvMV = make([]mv, m.mbLen)
	m.blocks = make([][6 * 64]int16, d.mbw*d.mbhA)
	for dir := range 2 {
		m.mvfY[dir] = make([]bool, m.blkLen)
		m.mvfC[dir] = make([]bool, m.mbLen)
	}
	m.blkMVType = make([]bool, m.blkLen)
	m.sliceTop = make([]bool, d.mbhA)
}

// slot is where block k of a macroblock is kept: the left blocks before
// the right ones, so that a block and the one below it are contiguous (as
// overlap smoothing of interlaced frames reads them).
var slot = [6]int{0, 2, 1, 3, 4, 5}

// block gives block k of blocks.
func block(blocks *[6 * 64]int16, k int) *[64]int16 {
	return (*[64]int16)(blocks[slot[k]*64:])
}

// at makes (x, y) the macroblock being decoded.
func (m *mbContext) at(x, y int) {
	m.mbx, m.mby = x, y
	m.mbi = (y+1)*m.cw + x + 1
	b := (2*y+1)*m.bw + 2*x + 1
	m.bi = [4]int{b, b + 1, b + m.bw, b + m.bw + 1}
}

// intra reports whether block k (4, 5 chroma) is intra.
func (m *mbContext) intra(k int) bool {
	if k < 4 {
		return m.intraY[m.bi[k]]
	}
	return m.intraC[m.mbi]
}

func (m *mbContext) setIntra(k int, v bool) {
	if k < 4 {
		m.intraY[m.bi[k]] = v
	} else {
		m.intraC[m.mbi] = v
	}
}

// availability of block k's neighbours above (a) and to the left (c) for
// intra prediction: inside the slice and (intraOnly) intra.
func (m *mbContext) neighbours(k int, intraOnly bool) (a, c bool) {
	a = k == 2 || k == 3 || !m.firstLine
	c = k == 1 || k == 3 || m.mbx > 0
	if !intraOnly {
		return a, c
	}
	if a {
		if k < 4 {
			a = m.intraY[m.bi[k]-m.bw]
		} else {
			a = m.intraC[m.mbi-m.cw]
		}
	}
	if c {
		if k < 4 {
			c = m.intraY[m.bi[k]-1]
		} else {
			c = m.intraC[m.mbi-1]
		}
	}
	return a, c
}

// mquant reads the macroblock's quantizer (GET_MQUANT).
func (m *mbContext) mquant() int {
	p := &m.d.ph
	mq := p.pq
	if !p.dquantFrm {
		return mq
	}
	r := m.r
	edges := 0
	switch p.dqProfile {
	case dqAllMBs:
		if p.dqBilevel {
			if r.u(1) == 1 {
				mq = -p.altpq
			}
		} else if diff := r.u(3); diff != 7 {
			mq = -p.pq - diff
		} else {
			mq = -r.u(5)
		}
	case dqSingleEdge:
		edges = 1 << p.dqSBEdge
	case dqDoubleEdges:
		edges = (3 << p.dqSBEdge) % 15
	case dqFourEdges:
		edges = 15
	}
	rows := m.d.mbh
	if p.fieldMode {
		rows = m.d.fieldMBH()
	}
	if edges&1 != 0 && m.mbx == 0 ||
		edges&2 != 0 && m.mby == 0 ||
		edges&4 != 0 && m.mbx == m.d.mbw-1 ||
		edges&8 != 0 && m.mby == rows-1 {
		mq = -p.altpq
	}
	if mq == 0 || mq > 31 || mq < -31 {
		mq = 1
	}
	return mq
}

// setCodingSets picks the AC tables for the picture.
func (m *mbContext) setCodingSets() {
	p := &m.d.ph
	y := p.yACTable
	if p.typ != picI && p.typ != picBI {
		y = p.cACTable
	}
	switch y {
	case 0:
		m.codingSet = csLowMotIntra
		if p.pqindex <= 8 {
			m.codingSet = csHighRateIntra
		}
	case 1:
		m.codingSet = csHighMotIntra
	case 2:
		m.codingSet = csMidRateIntra
	}
	switch p.cACTable {
	case 0:
		m.codingSet2 = csLowMotInter
		if p.pqindex <= 8 {
			m.codingSet2 = csHighRateInter
		}
	case 1:
		m.codingSet2 = csHighMotInter
	case 2:
		m.codingSet2 = csMidRateInter
	}
}

// codedBlockPred predicts whether luma block k is coded from its
// neighbours' and gives the result with diff applied.
func (m *mbContext) codedBlockPred(k, diff int) int {
	xy := m.bi[k]
	a, b, c := m.coded[xy-1], m.coded[xy-1-m.bw], m.coded[xy-m.bw]
	pred := c
	if b == c {
		pred = a
	}
	v := pred ^ uint8(diff)
	m.coded[xy] = v
	return int(v)
}

// iMB decodes a macroblock of an I or BI picture.
func (m *mbContext) iMB() error {
	d := m.d
	r := m.r
	pos := m.mby*d.mbw + m.mbx
	if d.fieldTXRaw {
		d.fieldTX[pos] = uint8(r.u(1))
	}
	cbp, ok := mbIVLC.read(r)
	if !ok {
		return errStream
	}
	if d.acPredRaw {
		m.acPredFlag = r.u(1) == 1
	} else {
		m.acPredFlag = d.acPred[pos] != 0
	}
	if d.ph.condover == condOverSelect && d.overFlagsRaw {
		d.overFlags[pos] = uint8(r.u(1))
	}
	mq := m.mquant()
	m.q[m.mbi] = int8(mq)
	for k := range 4 {
		m.mvs[0][m.bi[k]+m.blkOff] = mv{}
		m.mvs[1][m.bi[k]+m.blkOff] = mv{}
	}
	d.cur.intraMB[m.mbi+m.mbOff] = true
	blocks := &m.blocks[pos]
	for k := range 6 {
		m.setIntra(k, true)
		val := cbp >> (5 - k) & 1
		if k < 4 {
			val = m.codedBlockPred(k, val)
		}
		a := !m.firstLine || k == 2 || k == 3
		c := m.mbx > 0 || k == 1 || k == 3
		set := m.codingSet
		if k >= 4 {
			set = m.codingSet2
		}
		b := block(blocks, k)
		if err := m.intraBlock(b, k, val != 0, set, mq, a, c, true); err != nil {
			return err
		}
		idct8x8(b)
	}
	m.cbp[m.mbi], m.tt[m.mbi], m.isIntra[m.mbi] = 0, 0, 0x3f
	return nil
}

var (
	mvOffset = [9]int{0, 1, 3, 7, 15, 31, 63, 127, 255}
	mvSize   = [6]int{0, 2, 3, 4, 5, 8}
)

// mvData reads MVDATA (progressive): the differential, whether the block
// is intra and whether it has coefficients.
func (m *mbContext) mvData() (dx, dy int, intra, coeffs bool, err error) {
	p := &m.d.ph
	r := m.r
	v, ok := mvDiffVLC[p.mvTable].read(r)
	if !ok {
		return 0, 0, false, false, errStream
	}
	index := v + 1
	if index > 36 {
		coeffs = true
		index -= 37
	}
	switch index {
	case 0:
	case 35:
		q := 0
		if p.quarter {
			q = 1
		}
		dx = r.u(p.kx - 1 + q)
		dy = r.u(p.ky - 1 + q)
	case 36:
		intra = true
	default:
		comp := func(i int) int {
			val := mvSize[i]
			if !p.quarter && i == 5 {
				val--
			}
			c := mvOffset[i]
			if val > 0 {
				bits := r.u(val)
				sign := -(bits & 1)
				c = (sign ^ (bits>>1 + c)) - sign
			}
			return c
		}
		dx = comp(index % 6)
		dy = comp(index / 6)
	}
	return dx, dy, intra, coeffs, nil
}

// blockDst is where block k of the macroblock lies (fieldTX: luma
// blocks of a field each, the top ones of the first field).
func (m *mbContext) blockDst(k int, fieldTX bool) ([]byte, int) {
	if k >= 4 {
		off := m.mby*8*m.vsc + m.mbx*8
		if k == 4 {
			return m.vcb[off:], m.vsc
		}
		return m.vcr[off:], m.vsc
	}
	if fieldTX {
		return m.vy[(m.mby*16+k>>1)*m.vsy+m.mbx*16+(k&1)*8:], 2 * m.vsy
	}
	return m.vy[(m.mby*16+(k&2)*4)*m.vsy+m.mbx*16+(k&1)*8:], m.vsy
}

// pMB decodes a macroblock of a progressive P picture.
func (m *mbContext) pMB() error {
	d := m.d
	p := &d.ph
	r := m.r
	pos := m.mby*d.mbw + m.mbx
	var fourMV, skipped bool
	if d.mvTypeRaw {
		fourMV = r.u(1) == 1
	} else {
		fourMV = d.mvTypePlane[pos] != 0
	}
	if d.skipRaw {
		skipped = r.u(1) == 1
	} else {
		skipped = d.skipPlane[pos] != 0
	}
	blocks := &m.blocks[pos]
	var cbpOut, ttOut uint32
	var intraOut uint8
	ttmb := p.ttfrm
	first := true
	// inter decodes block k's residual into the prediction.
	inter := func(k, mq int) error {
		dst, stride := m.blockDst(k, false)
		pat, tt, err := m.interBlock(&m.blk, k, mq, ttmb, first, dst, stride)
		if err != nil {
			return err
		}
		cbpOut |= uint32(pat) << (4 * k)
		ttOut |= uint32(tt) << (4 * k)
		if !p.ttmbf && ttmb < 8 {
			ttmb = -1
		}
		first = false
		return nil
	}
	intraBlk := func(k int, coded bool, mq int) error {
		a, c := m.neighbours(k, true)
		set := m.codingSet
		if k >= 4 {
			set = m.codingSet2
		}
		b := block(blocks, k)
		if err := m.intraBlock(b, k, coded, set, mq, a, c, false); err != nil {
			return err
		}
		idct8x8(b)
		cbpOut |= 0xf << (4 * k)
		intraOut |= 1 << k
		return nil
	}
	switch {
	case !fourMV && !skipped:
		dx, dy, intra, coeffs, err := m.mvData()
		if err != nil {
			return err
		}
		for k := range 6 {
			m.setIntra(k, false)
		}
		m.predMV(0, dx, dy, true, 0, 0, intra)
		mq := p.pq
		cbp := 0
		switch {
		case intra && !coeffs:
			mq = m.mquant()
			m.acPredFlag = r.u(1) == 1
		case coeffs:
			if intra {
				m.acPredFlag = r.u(1) == 1
			}
			v, ok := cbpcyPVLC[p.cbpTable].read(r)
			if !ok {
				return errStream
			}
			cbp = v
			mq = m.mquant()
		}
		m.q[m.mbi] = int8(mq)
		if !p.ttmbf && !intra && coeffs {
			v, ok := ttmbVLC[p.ttIndex].read(r)
			if !ok {
				return errStream
			}
			ttmb = v
		}
		if !intra {
			m.mc1MV(0)
		}
		for k := range 6 {
			*m.dcSlot(k) = 0
			coded := cbp>>(5-k)&1 != 0
			m.setIntra(k, intra)
			if intra {
				if err := intraBlk(k, coded, mq); err != nil {
					return err
				}
			} else if coded {
				if err := inter(k, mq); err != nil {
					return err
				}
			}
		}
	case !fourMV:
		for k := range 6 {
			m.setIntra(k, false)
			*m.dcSlot(k) = 0
		}
		m.q[m.mbi] = 0
		m.predMV(0, 0, 0, true, 0, 0, false)
		m.mc1MV(0)
	case !skipped:
		v, ok := cbpcyPVLC[p.cbpTable].read(r)
		if !ok {
			return errStream
		}
		cbp := v
		var isIntra, isCoded [6]bool
		intraCount := 0
		codedInter := false
		for k := range 6 {
			coded := cbp>>(5-k)&1 != 0
			*m.dcSlot(k) = 0
			if k < 4 {
				dx, dy, intra, coeffs := 0, 0, false, false
				if coded {
					var err error
					dx, dy, intra, coeffs, err = m.mvData()
					if err != nil {
						return err
					}
				}
				m.setIntra(k, intra) // the vector prediction's neighbours are the blocks before
				m.predMV(k, dx, dy, false, 0, 0, intra)
				if !intra {
					m.mc4MVLuma(k, 0, false)
				}
				if intra {
					intraCount++
				}
				isIntra[k], isCoded[k] = intra, coeffs
			} else {
				isIntra[k], isCoded[k] = intraCount >= 3, coded
			}
			if k == 4 {
				m.mc4MVChroma(0)
			}
			m.setIntra(k, isIntra[k])
			if !isIntra[k] && isCoded[k] {
				codedInter = true
			}
		}
		if intraCount == 0 && !codedInter {
			break
		}
		mq := m.mquant()
		m.q[m.mbi] = int8(mq)
		intraPred := false
		for k := range 6 {
			if !isIntra[k] {
				continue
			}
			a, c := m.neighbours(k, true)
			if a || c {
				intraPred = true
				break
			}
		}
		m.acPredFlag = false
		if intraPred {
			m.acPredFlag = r.u(1) == 1
		}
		if !p.ttmbf && codedInter {
			v, ok := ttmbVLC[p.ttIndex].read(r)
			if !ok {
				return errStream
			}
			ttmb = v
		}
		for k := range 6 {
			if isIntra[k] {
				if err := intraBlk(k, isCoded[k], mq); err != nil {
					return err
				}
			} else if isCoded[k] {
				if err := inter(k, mq); err != nil {
					return err
				}
			}
		}
	default:
		m.q[m.mbi] = 0
		for k := range 6 {
			m.setIntra(k, false)
			*m.dcSlot(k) = 0
		}
		for k := range 4 {
			m.predMV(k, 0, 0, false, 0, 0, false)
			m.mc4MVLuma(k, 0, false)
		}
		m.mc4MVChroma(0)
	}
	m.cbp[m.mbi], m.tt[m.mbi], m.isIntra[m.mbi] = cbpOut, ttOut, intraOut
	return nil
}

// bMB decodes a macroblock of a progressive B picture.
func (m *mbContext) bMB() error {
	d := m.d
	p := &d.ph
	r := m.r
	pos := m.mby*d.mbw + m.mbx
	var direct, skipped bool
	if d.directRaw {
		direct = r.u(1) == 1
	} else {
		direct = d.directPlane[pos] != 0
	}
	if d.skipRaw {
		skipped = r.u(1) == 1
	} else {
		skipped = d.skipPlane[pos] != 0
	}
	ttmb := p.ttfrm
	var dx, dy [2]int
	intra, coeffs := false, false
	mode := bmvBackward
	for k := range 6 {
		m.setIntra(k, false)
		*m.dcSlot(k) = 0
	}
	m.q[m.mbi] = 0
	if !direct {
		if !skipped {
			var err error
			dx[0], dy[0], intra, coeffs, err = m.mvData()
			if err != nil {
				return err
			}
			dx[1], dy[1] = dx[0], dy[0]
		}
		if skipped || !intra {
			half := p.bfraction >= 128
			switch r.v012() {
			case 0:
				mode = bmvForward
				if half {
					mode = bmvBackward
				}
			case 1:
				mode = bmvBackward
				if half {
					mode = bmvForward
				}
			case 2:
				mode = bmvInterpolated
				dx[0], dy[0] = 0, 0
			}
		}
	}
	for k := range 6 {
		m.setIntra(k, intra)
	}
	mc := func() {
		if direct || mode == bmvInterpolated {
			m.mc1MV(0)
			m.interpMC()
			return
		}
		m.mc1MV(boolInt(mode == bmvBackward))
	}
	if skipped {
		if direct {
			mode = bmvInterpolated
		}
		m.predBMV(dx, dy, direct, mode, false)
		mc()
		return nil
	}
	cbp := 0
	var mq int
	if direct {
		v, ok := cbpcyPVLC[p.cbpTable].read(r)
		if !ok {
			return errStream
		}
		cbp = v
		mq = m.mquant()
		m.q[m.mbi] = int8(mq)
		if !p.ttmbf {
			v, ok := ttmbVLC[p.ttIndex].read(r)
			if !ok {
				return errStream
			}
			ttmb = v
		}
		m.predBMV([2]int{}, [2]int{}, true, mode, false)
		mc()
	} else {
		if !coeffs && !intra {
			m.predBMV(dx, dy, false, mode, false)
			mc()
			return nil
		}
		if intra && !coeffs {
			mq = m.mquant()
			m.q[m.mbi] = int8(mq)
			m.acPredFlag = r.u(1) == 1
			m.predBMV(dx, dy, false, mode, true)
		} else {
			if mode == bmvInterpolated {
				var err error
				dx[0], dy[0], intra, coeffs, err = m.mvData()
				if err != nil {
					return err
				}
				if !coeffs {
					m.predBMV(dx, dy, false, mode, intra)
					mc()
					return nil
				}
			}
			m.predBMV(dx, dy, false, mode, intra)
			if !intra {
				mc()
			} else {
				m.acPredFlag = r.u(1) == 1
			}
			v, ok := cbpcyPVLC[p.cbpTable].read(r)
			if !ok {
				return errStream
			}
			cbp = v
			mq = m.mquant()
			m.q[m.mbi] = int8(mq)
			if !p.ttmbf && !intra && coeffs {
				v, ok := ttmbVLC[p.ttIndex].read(r)
				if !ok {
					return errStream
				}
				ttmb = v
			}
		}
	}
	first := true
	for k := range 6 {
		*m.dcSlot(k) = 0
		coded := cbp>>(5-k)&1 != 0
		m.setIntra(k, intra)
		dst, stride := m.blockDst(k, false)
		if intra {
			a, c := m.neighbours(k, true)
			set := m.codingSet
			if k >= 4 {
				set = m.codingSet2
			}
			if err := m.intraBlock(&m.blk, k, coded, set, mq, a, c, false); err != nil {
				return err
			}
			idct8x8(&m.blk)
			putSigned(dst, stride, &m.blk)
		} else if coded {
			if _, _, err := m.interBlock(&m.blk, k, mq, ttmb, first, dst, stride); err != nil {
				return err
			}
			if !p.ttmbf && ttmb < 8 {
				ttmb = -1
			}
			first = false
		}
	}
	return nil
}
