package vc1

// Interlaced frame (10.7) and field (10.3-10.5) macroblocks.

var mvOffsets = [2][9]int{
	{0, 1, 2, 4, 8, 16, 32, 64, 128},
	{0, 1, 3, 7, 15, 31, 63, 127, 255},
}

// mvDataInterlaced reads an interlaced picture's MVDATA: the differential
// and, with two reference fields, which one it predicts from.
func (m *mbContext) mvDataInterlaced() (dx, dy, predFlag int, err error) {
	p := &m.d.ph
	r := m.r
	var t *vlc
	esc := 71
	numref := 0
	if !p.numref {
		t = mvdata1RefVLC[p.imvTable&3]
	} else {
		t = mvdata2RefVLC[p.imvTable]
		esc = 125
		numref = 1
	}
	extX := p.dmvrange & 1
	extY := p.dmvrange >> 1 & 1
	index, ok := t.read(r)
	if !ok {
		return 0, 0, 0, errStream
	}
	if index == esc {
		dx = r.u(p.kx)
		dy = r.u(p.ky)
		if p.numref {
			predFlag = dy & 1
			dy = (dy + dy&1) >> 1
		}
		return dx, dy, predFlag, nil
	}
	signed := func(n, ext, i int) int {
		val := r.u(n)
		sign := -(val & 1)
		return (sign ^ (val>>1 + mvOffsets[ext][i])) - sign
	}
	if i := (index + 1) % 9; i != 0 {
		dx = signed(i+extX, extX, i)
	}
	i := (index + 1) / 9
	if i > numref {
		dy = signed(i>>numref+extY, extY, i>>numref)
	}
	if p.numref {
		predFlag = i & 1
	}
	return dx, dy, predFlag, nil
}

// intraMBBlocks decodes an intra macroblock's blocks of an interlaced
// picture into blocks (for overlap smoothing) or, with put, straight into
// the picture.
func (m *mbContext) intraMBBlocks(blocks *[6 * 64]int16, cbp, mq int, fieldTX, put bool) error {
	for k := range 6 {
		m.setIntra(k, true)
		*m.dcSlot(k) = 0
		a, c := m.neighbours(k, true)
		set := m.codingSet
		if k >= 4 {
			set = m.codingSet2
		}
		b := block(blocks, k)
		if err := m.intraBlock(b, k, cbp>>(5-k)&1 != 0, set, mq, a, c, false); err != nil {
			return err
		}
		idct8x8(b)
		if put {
			dst, stride := m.blockDst(k, fieldTX)
			putSigned(dst, stride, b)
		}
	}
	return nil
}

// interBlocks decodes an inter macroblock's coded residuals into the
// prediction, giving the coded quarters and transform types.
func (m *mbContext) interBlocks(cbp, mq int, fieldTX bool) (cbpOut, ttOut uint32, err error) {
	p := &m.d.ph
	ttmb := p.ttfrm
	if !p.ttmbf && cbp != 0 {
		v, ok := ttmbVLC[p.ttIndex].read(m.r)
		if !ok {
			return 0, 0, errStream
		}
		ttmb = v
	}
	first := true
	for k := range 6 {
		*m.dcSlot(k) = 0
		if cbp>>(5-k)&1 == 0 {
			continue
		}
		dst, stride := m.blockDst(k, fieldTX)
		pat, tt, err := m.interBlock(&m.blk, k, mq, ttmb, first, dst, stride)
		if err != nil {
			return 0, 0, err
		}
		cbpOut |= uint32(pat) << (4 * k)
		ttOut |= uint32(tt) << (4 * k)
		if !p.ttmbf && ttmb < 8 {
			ttmb = -1
		}
		first = false
	}
	return cbpOut, ttOut, nil
}

func (m *mbContext) setBlkMVType(field bool) {
	for k := range 4 {
		m.blkMVType[m.bi[k]] = field
	}
}

// icbp reads an interlaced picture's CBPCY.
func (m *mbContext) icbp() (int, error) {
	v, ok := icbpcyVLC[m.d.ph.icbpTable].read(m.r)
	if !ok {
		return 0, errStream
	}
	return v + 1, nil
}

// Interlaced frame macroblock types.
const (
	intfr1MV = iota
	intfr2MVField
	intfr2MV
	intfr4MVField
	intfr4MV
	intfrIntra
)

// pMBIntfr decodes a macroblock of an interlaced frame P picture.
func (m *mbContext) pMBIntfr() error {
	d := m.d
	p := &d.ph
	r := m.r
	pos := m.mby*d.mbw + m.mbx
	var skipped bool
	if d.skipRaw {
		skipped = r.u(1) == 1
	} else {
		skipped = d.skipPlane[pos] != 0
	}
	var cbpOut, ttOut uint32
	if skipped {
		m.isIntra[m.mbi] = 0
		for k := range 6 {
			m.setIntra(k, false)
			*m.dcSlot(k) = 0
		}
		m.q[m.mbi] = 0
		m.setBlkMVType(false)
		m.predMVIntfr(0, 0, 0, 1, 0, false)
		m.mc1MV(0)
		d.fieldTX[pos] = 0
		m.cbp[m.mbi], m.tt[m.mbi] = 0, 0
		return nil
	}
	sw := boolInt(p.fourMVSwitch)
	t := intfrNon4MVModeVLC[p.mbModeTable]
	if p.fourMVSwitch {
		t = intfr4MVModeVLC[p.mbModeTable]
	}
	idx, ok := t.read(r)
	if !ok {
		return errStream
	}
	mode := &mbModeIntfrP[sw][idx]
	fourMV, twoMV := false, false
	switch mode[0] {
	case intfr4MV:
		fourMV = true
		m.setBlkMVType(false)
	case intfr4MVField:
		fourMV = true
		m.setBlkMVType(true)
	case intfr2MVField:
		twoMV = true
		m.setBlkMVType(true)
	case intfr1MV:
		m.setBlkMVType(false)
	}
	if mode[0] == intfrIntra {
		for k := range 4 {
			m.mvs[1][m.bi[k]] = mv{}
		}
		m.isIntra[m.mbi] = 0x3f
		fieldTX := r.u(1) == 1
		d.fieldTX[pos] = uint8(boolInt(fieldTX))
		cbp := 0
		if r.u(1) == 1 {
			v, err := m.icbp()
			if err != nil {
				return err
			}
			cbp = v
		}
		m.acPredFlag = r.u(1) == 1
		d.acPred[pos] = uint8(boolInt(m.acPredFlag))
		mq := m.mquant()
		m.q[m.mbi] = int8(mq)
		if err := m.intraMBBlocks(&m.blocks[pos], cbp, mq, fieldTX, false); err != nil {
			return err
		}
		m.cbp[m.mbi], m.tt[m.mbi] = 0xffffff, 0
		return nil
	}
	cbp := 0
	if mode[3] != 0 {
		v, err := m.icbp()
		if err != nil {
			return err
		}
		cbp = v
	}
	var twoMVBP, fourMVBP int
	if mode[0] == intfr2MVField {
		twoMVBP, ok = twoMVBPVLC[p.twoMVBPTable].read(r)
	} else if fourMV {
		fourMVBP, ok = fourMVBPVLC[p.fourMVBPTable].read(r)
	}
	if !ok {
		return errStream
	}
	m.isIntra[m.mbi] = 0
	for k := range 6 {
		m.setIntra(k, false)
	}
	fieldTX := mode[1] != 0
	d.fieldTX[pos] = mode[1]
	mvd := func(present bool) (int, int, error) {
		if !present {
			return 0, 0, nil
		}
		dx, dy, _, err := m.mvDataInterlaced()
		return dx, dy, err
	}
	switch {
	case fourMV:
		for k := range 4 {
			dx, dy, err := mvd(fourMVBP&(8>>k) != 0)
			if err != nil {
				return err
			}
			m.predMVIntfr(k, dx, dy, 0, 0, false)
			m.mc4MVLuma(k, 0, false)
		}
		m.mc4MVChroma4(0, 0, false)
	case twoMV:
		dx, dy, err := mvd(twoMVBP&2 != 0)
		if err != nil {
			return err
		}
		m.predMVIntfr(0, dx, dy, 2, 0, false)
		m.mc4MVLuma(0, 0, false)
		m.mc4MVLuma(1, 0, false)
		dx, dy, err = mvd(twoMVBP&1 != 0)
		if err != nil {
			return err
		}
		m.predMVIntfr(2, dx, dy, 2, 0, false)
		m.mc4MVLuma(2, 0, false)
		m.mc4MVLuma(3, 0, false)
		m.mc4MVChroma4(0, 0, false)
	default:
		dx, dy, err := mvd(mode[2] != 0)
		if err != nil {
			return err
		}
		m.predMVIntfr(0, dx, dy, 1, 0, false)
		m.mc1MV(0)
	}
	mq := p.pq
	if cbp != 0 {
		mq = m.mquant()
	}
	m.q[m.mbi] = int8(mq)
	var err error
	cbpOut, ttOut, err = m.interBlocks(cbp, mq, fieldTX)
	if err != nil {
		return err
	}
	m.cbp[m.mbi], m.tt[m.mbi] = cbpOut, ttOut
	return nil
}

// bMBIntfr decodes a macroblock of an interlaced frame B picture.
func (m *mbContext) bMBIntfr() error {
	d := m.d
	p := &d.ph
	r := m.r
	pos := m.mby*d.mbw + m.mbx
	var skipped bool
	if d.skipRaw {
		skipped = r.u(1) == 1
	} else {
		skipped = d.skipPlane[pos] != 0
	}
	idx := 0
	twoMV := false
	if !skipped {
		v, ok := intfrNon4MVModeVLC[p.mbModeTable].read(r)
		if !ok {
			return errStream
		}
		idx = v
		twoMV = mbModeIntfrP[0][idx][0] == intfr2MVField
		m.setBlkMVType(twoMV)
	}
	mode := &mbModeIntfrP[0][idx]
	if mode[0] == intfrIntra {
		for k := range 4 {
			m.cmv[0][k], m.cmv[1][k] = mv{}, mv{}
			m.mvs[0][m.bi[k]], m.mvs[1][m.bi[k]] = mv{}, mv{}
		}
		m.isIntra[m.mbi] = 0x3f
		fieldTX := r.u(1) == 1
		d.fieldTX[pos] = uint8(boolInt(fieldTX))
		cbp := 0
		if r.u(1) == 1 {
			v, err := m.icbp()
			if err != nil {
				return err
			}
			cbp = v
		}
		m.acPredFlag = r.u(1) == 1
		d.acPred[pos] = uint8(boolInt(m.acPredFlag))
		mq := m.mquant()
		m.q[m.mbi] = int8(mq)
		if err := m.intraMBBlocks(&m.blocks[pos], cbp, mq, fieldTX, true); err != nil {
			return err
		}
		m.cbp[m.mbi], m.tt[m.mbi] = 0, 0
		return nil
	}
	m.isIntra[m.mbi] = 0
	var direct bool
	if d.directRaw {
		direct = r.u(1) == 1
	} else {
		direct = d.directPlane[pos] != 0
	}
	if direct {
		nxt := d.next
		scaled := func(i, dir int) mv {
			col := nxt.directMV[m.bi[i]]
			return mv{int16(scaleMV(int(col.x), p.bfraction, dir == 1, p.quarter)),
				int16(scaleMV(int(col.y), p.bfraction, dir == 1, p.quarter))}
		}
		for dir := range 2 {
			m.cmv[dir][0] = scaled(0, dir)
			m.mvs[dir][m.bi[0]] = m.cmv[dir][0]
			if twoMV {
				m.cmv[dir][2] = scaled(2, dir)
				m.mvs[dir][m.bi[2]] = m.cmv[dir][2]
				for _, k := range [2]int{1, 3} {
					m.cmv[dir][k] = m.cmv[dir][k-1]
					m.mvs[dir][m.bi[k]] = m.cmv[dir][k]
				}
			} else {
				for k := 1; k < 4; k++ {
					m.cmv[dir][k] = m.cmv[dir][0]
					m.mvs[dir][m.bi[k]] = m.cmv[dir][0]
				}
			}
		}
	}
	bmv := bmvBackward
	mvsw := false
	if !direct {
		half := p.bfraction >= 128
		switch r.v012() {
		case 0:
			bmv = bmvForward
			if half {
				bmv = bmvBackward
			}
		case 1:
			bmv = bmvBackward
			if half {
				bmv = bmvForward
			}
		case 2:
			bmv = bmvInterpolated
		}
		if twoMV && bmv != bmvInterpolated {
			mvsw = r.u(1) == 1
		}
	}
	// mirror gives both fields' blocks of direction dir the vectors of the
	// top field's (fromTop) or the bottom field's.
	mirror := func(dir int, fromTop bool) {
		for i := range 2 {
			a, b := i, i+2
			if !fromTop {
				a, b = i+2, i
			}
			v := m.mvs[dir][m.bi[a]]
			m.mvs[dir][m.bi[b]] = v
			m.cmv[dir][i], m.cmv[dir][i+2] = v, v
		}
	}
	if skipped {
		dir := 0
		for k := range 6 {
			m.setIntra(k, false)
			*m.dcSlot(k) = 0
		}
		m.q[m.mbi] = 0
		m.setBlkMVType(false)
		if !direct {
			if bmv == bmvInterpolated {
				m.predMVIntfr(0, 0, 0, 1, 0, false)
				m.predMVIntfr(0, 0, 0, 1, 1, false)
			} else {
				dir = boolInt(bmv == bmvBackward)
				m.predMVIntfr(0, 0, 0, 1, dir, false)
				if mvsw {
					mirror(dir, true)
					mirror(1-dir, false)
				} else {
					m.setBlkMVType(true)
					m.predMVIntfr(0, 0, 0, 2, 1-dir, false)
					mirror(1-dir, true)
				}
			}
		}
		m.mc1MV(dir)
		if direct || bmv == bmvInterpolated {
			m.interpMC()
		}
		d.fieldTX[pos] = 0
		m.cbp[m.mbi], m.tt[m.mbi] = 0, 0
		return nil
	}
	cbp := 0
	if mode[3] != 0 {
		v, err := m.icbp()
		if err != nil {
			return err
		}
		cbp = v
	}
	var twoMVBP, fourMVBP int
	ok := true
	if !direct {
		if bmv == bmvInterpolated && twoMV {
			fourMVBP, ok = fourMVBPVLC[p.fourMVBPTable].read(r)
		} else if bmv == bmvInterpolated || twoMV {
			twoMVBP, ok = twoMVBPVLC[p.twoMVBPTable].read(r)
		}
	}
	if !ok {
		return errStream
	}
	for k := range 6 {
		m.setIntra(k, false)
	}
	fieldTX := mode[1] != 0
	d.fieldTX[pos] = mode[1]
	mvd := func(present bool) (int, int, error) {
		if !present {
			return 0, 0, nil
		}
		dx, dy, _, err := m.mvDataInterlaced()
		return dx, dy, err
	}
	switch {
	case direct:
		if twoMV {
			for k := range 4 {
				m.mc4MVLuma(k, 0, false)
				m.mc4MVLuma(k, 1, true)
			}
			m.mc4MVChroma4(0, 0, false)
			m.mc4MVChroma4(1, 1, true)
		} else {
			m.mc1MV(0)
			m.interpMC()
		}
	case twoMV && bmv == bmvInterpolated:
		for i := range 4 {
			dir := boolInt(i == 1 || i == 3)
			dx, dy, err := mvd(fourMVBP>>(3-i)&1 != 0)
			if err != nil {
				return err
			}
			j := 0
			if i > 1 {
				j = 2
			}
			m.predMVIntfr(j, dx, dy, 2, dir, false)
			m.mc4MVLuma(j, dir, dir == 1)
			m.mc4MVLuma(j+1, dir, dir == 1)
		}
		m.mc4MVChroma4(0, 0, false)
		m.mc4MVChroma4(1, 1, true)
	case bmv == bmvInterpolated:
		dx, dy, err := mvd(twoMVBP&2 != 0)
		if err != nil {
			return err
		}
		m.predMVIntfr(0, dx, dy, 1, 0, false)
		m.mc1MV(0)
		dx, dy, err = mvd(twoMVBP&1 != 0)
		if err != nil {
			return err
		}
		m.predMVIntfr(0, dx, dy, 1, 1, false)
		m.interpMC()
	case twoMV:
		dir := boolInt(bmv == bmvBackward)
		dir2 := dir
		if mvsw {
			dir2 = 1 - dir
		}
		dx, dy, err := mvd(twoMVBP&2 != 0)
		if err != nil {
			return err
		}
		m.predMVIntfr(0, dx, dy, 2, dir, false)
		dx, dy, err = mvd(twoMVBP&1 != 0)
		if err != nil {
			return err
		}
		m.predMVIntfr(2, dx, dy, 2, dir2, false)
		if mvsw {
			mirror(dir, true)
			mirror(dir2, false)
		} else {
			m.predMVIntfr(0, 0, 0, 2, 1-dir, false)
			m.predMVIntfr(2, 0, 0, 2, 1-dir, false)
		}
		m.mc4MVLuma(0, dir, false)
		m.mc4MVLuma(1, dir, false)
		m.mc4MVLuma(2, dir2, false)
		m.mc4MVLuma(3, dir2, false)
		m.mc4MVChroma4(dir, dir2, false)
	default:
		dir := boolInt(bmv == bmvBackward)
		dx, dy, err := mvd(mode[2] != 0)
		if err != nil {
			return err
		}
		m.predMVIntfr(0, dx, dy, 1, dir, false)
		m.setBlkMVType(true)
		m.predMVIntfr(0, 0, 0, 2, 1-dir, false)
		mirror(1-dir, true)
		m.mc1MV(dir)
	}
	mq := p.pq
	if cbp != 0 {
		mq = m.mquant()
	}
	m.q[m.mbi] = int8(mq)
	cbpOut, ttOut, err := m.interBlocks(cbp, mq, fieldTX)
	if err != nil {
		return err
	}
	m.cbp[m.mbi], m.tt[m.mbi] = cbpOut, ttOut
	return nil
}

// fieldMBMode reads a field macroblock's MBMODE.
func (m *mbContext) fieldMBMode() (int, error) {
	p := &m.d.ph
	t := if1MVModeVLC[p.mbModeTable]
	if p.mixedMV() {
		t = ifMixedModeVLC[p.mbModeTable]
	}
	v, ok := t.read(m.r)
	if !ok {
		return 0, errStream
	}
	return v, nil
}

// intraFieldMB decodes an intra macroblock of a field picture.
func (m *mbContext) intraFieldMB(idx int, put bool) error {
	d := m.d
	pos := m.mby*d.mbw + m.mbx
	m.isIntra[m.mbi] = 0x3f
	m.mvs[1][m.bi[0]+m.blkOff] = mv{}
	d.cur.intraMB[m.mbi+m.mbOff] = true
	mq := m.mquant()
	m.q[m.mbi] = int8(mq)
	m.acPredFlag = m.r.u(1) == 1
	d.acPred[pos] = uint8(boolInt(m.acPredFlag))
	cbp := 0
	if idx&1 != 0 {
		v, err := m.icbp()
		if err != nil {
			return err
		}
		cbp = v
	}
	if err := m.intraMBBlocks(&m.blocks[pos], cbp, mq, false, put); err != nil {
		return err
	}
	if put {
		m.cbp[m.mbi], m.tt[m.mbi] = 0, 0
	} else {
		m.cbp[m.mbi], m.tt[m.mbi] = 0xffffff, 0
	}
	return nil
}

// pMBIntfi decodes a macroblock of a field P picture.
func (m *mbContext) pMBIntfi() error {
	d := m.d
	p := &d.ph
	idx, err := m.fieldMBMode()
	if err != nil {
		return err
	}
	if idx <= 1 {
		return m.intraFieldMB(idx, false)
	}
	m.isIntra[m.mbi] = 0
	d.cur.intraMB[m.mbi+m.mbOff] = false
	for k := range 6 {
		m.setIntra(k, false)
	}
	coeffs := false
	if idx <= 5 {
		dx, dy, pf := 0, 0, 0
		if idx&1 != 0 {
			if dx, dy, pf, err = m.mvDataInterlaced(); err != nil {
				return err
			}
		}
		m.predMV(0, dx, dy, true, pf, 0, false)
		m.mc1MV(0)
		coeffs = idx&2 == 0
	} else {
		bp, ok := fourMVBPVLC[p.fourMVBPTable].read(m.r)
		if !ok {
			return errStream
		}
		for k := range 4 {
			dx, dy, pf := 0, 0, 0
			if bp&(8>>k) != 0 {
				if dx, dy, pf, err = m.mvDataInterlaced(); err != nil {
					return err
				}
			}
			m.predMV(k, dx, dy, false, pf, 0, false)
			m.mc4MVLuma(k, 0, false)
		}
		m.mc4MVChroma(0)
		coeffs = idx&1 != 0
	}
	cbp := 0
	if coeffs {
		if cbp, err = m.icbp(); err != nil {
			return err
		}
	}
	mq := p.pq
	if cbp != 0 {
		mq = m.mquant()
	}
	m.q[m.mbi] = int8(mq)
	cbpOut, ttOut, err := m.interBlocks(cbp, mq, false)
	if err != nil {
		return err
	}
	m.cbp[m.mbi], m.tt[m.mbi] = cbpOut, ttOut
	return nil
}

// bMBIntfi decodes a macroblock of a field B picture.
func (m *mbContext) bMBIntfi() error {
	d := m.d
	p := &d.ph
	r := m.r
	pos := m.mby*d.mbw + m.mbx
	idx, err := m.fieldMBMode()
	if err != nil {
		return err
	}
	if idx <= 1 {
		return m.intraFieldMB(idx, true)
	}
	m.isIntra[m.mbi] = 0
	for k := range 6 {
		m.setIntra(k, false)
	}
	var fwd bool
	if d.forwardRaw {
		fwd = r.u(1) == 1
		d.forwardPlane[pos] = uint8(boolInt(fwd))
	} else {
		fwd = d.forwardPlane[pos] != 0
	}
	bmv := bmvBackward
	coeffs := false
	var dx, dy, pf [2]int
	if idx <= 5 {
		interpMVP := false
		if fwd {
			bmv = bmvForward
		} else {
			switch r.v012() {
			case 0:
				bmv = bmvBackward
			case 1:
				bmv = bmvDirect
			case 2:
				bmv = bmvInterpolated
				interpMVP = r.u(1) == 1
			}
		}
		if bmv != bmvDirect && idx&1 != 0 {
			i := boolInt(bmv == bmvBackward)
			if dx[i], dy[i], pf[i], err = m.mvDataInterlaced(); err != nil {
				return err
			}
		}
		if interpMVP {
			if dx[1], dy[1], pf[1], err = m.mvDataInterlaced(); err != nil {
				return err
			}
		}
		if bmv == bmvDirect {
			dx, dy, pf = [2]int{}, [2]int{}, [2]int{}
			if !d.next.fields {
				return errUnsupported("direct prediction from a frame picture in a field picture")
			}
		}
		m.predBMVIntfi(0, dx, dy, true, pf, bmv)
		switch bmv {
		case bmvDirect, bmvInterpolated:
			m.mc1MV(0)
			m.interpMC()
		default:
			m.mc1MV(boolInt(bmv == bmvBackward))
		}
		coeffs = idx&2 == 0
	} else {
		if fwd {
			bmv = bmvForward
		}
		bp, ok := fourMVBPVLC[p.fourMVBPTable].read(r)
		if !ok {
			return errStream
		}
		dir := boolInt(bmv == bmvBackward)
		for k := range 4 {
			dx, dy, pf = [2]int{}, [2]int{}, [2]int{}
			if bp&(8>>k) != 0 {
				if dx[dir], dy[dir], pf[dir], err = m.mvDataInterlaced(); err != nil {
					return err
				}
			}
			m.predBMVIntfi(k, dx, dy, false, pf, bmv)
			m.mc4MVLuma(k, dir, false)
		}
		m.mc4MVChroma(dir)
		coeffs = idx&1 != 0
	}
	cbp := 0
	if coeffs {
		if cbp, err = m.icbp(); err != nil {
			return err
		}
	}
	mq := p.pq
	if cbp != 0 {
		mq = m.mquant()
	}
	m.q[m.mbi] = int8(mq)
	cbpOut, ttOut, err := m.interBlocks(cbp, mq, false)
	if err != nil {
		return err
	}
	m.cbp[m.mbi], m.tt[m.mbi] = cbpOut, ttOut
	return nil
}
