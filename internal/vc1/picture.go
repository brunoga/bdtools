package vc1

import "sort"

// slice is a slice of a picture: its first macroblock row, whether it is
// of the second field, and its data after the address.
type slice struct {
	row    int
	second bool
	r      *bits
}

// decodePicture decodes the frame whose header r has just been read: its
// macroblocks from the frame unit and the slices after it, and for field
// pictures the second field from its unit and slices.
func (d *Decoder) decodePicture(r *bits, field *unit, slices []unit) error {
	p := &d.ph
	f := d.cur
	m := &d.mb
	clear(f.directMV)
	clear(m.mvs[0])
	clear(m.bmv)
	if p.skipped {
		// A skipped P picture repeats its reference.
		if d.last != nil {
			copy(f.y, d.last.y)
			copy(f.cb, d.last.cb)
			copy(f.cr, d.last.cr)
		}
		return nil
	}
	var ss []slice
	for i := range slices {
		sr := &bits{b: slices[i].data}
		ss = append(ss, slice{sr.u(9), slices[i].code&0x80 != 0, sr})
	}
	sort.SliceStable(ss, func(i, j int) bool {
		if ss[i].second != ss[j].second {
			return !ss[i].second
		}
		return ss[i].row < ss[j].row
	})
	var first, second []slice
	for _, s := range ss {
		if s.second {
			second = append(second, s)
		} else {
			first = append(first, s)
		}
	}
	err := d.decodeField(r, first)
	if !p.fieldMode {
		return err
	}
	if field == nil {
		return errStream
	}
	d.secondField = true
	r2 := &bits{b: field.data}
	if e := d.pictureHeader(r2, false); e != nil {
		return e
	}
	if e := d.decodeField(r2, second); e != nil && err == nil {
		err = e
	}
	d.secondField = false
	if p.typ != picB && p.typ != picBI {
		d.mvfNext, m.mvfY = m.mvfY, d.mvfNext
		d.mvfNextC, m.mvfC = m.mvfC, d.mvfNextC
	}
	return err
}

// decodeField decodes a picture (a frame or a field): its rows from r, then
// from each slice, then smooths and filters it.
func (d *Decoder) decodeField(r *bits, slices []slice) error {
	p := &d.ph
	f := d.cur
	m := &d.mb
	m.mvs[1] = f.directMV
	if p.typ == picB {
		m.mvs[1] = m.bmv
	}
	if p.fieldMode {
		parity := boolInt(d.curFieldBottom)
		m.vy, m.vcb, m.vcr = f.y[parity*d.strideY:], f.cb[parity*d.strideC:], f.cr[parity*d.strideC:]
		m.vsy, m.vsc = 2*d.strideY, 2*d.strideC
		m.rows = d.fieldMBH()
		m.blkOff, m.mbOff = 0, 0
		if d.secondField {
			m.blkOff = d.mbhA * m.bw
			m.mbOff = d.mbhA / 2 * m.cw
		}
	} else {
		m.vy, m.vcb, m.vcr = f.y, f.cb, f.cr
		m.vsy, m.vsc = d.strideY, d.strideC
		m.rows = d.mbh
		m.blkOff, m.mbOff = 0, 0
	}
	clear(m.sliceTop)
	start := 0
	var err error
	for i := 0; i <= len(slices); i++ {
		end := m.rows
		if i < len(slices) {
			end = min(slices[i].row%m.rows, m.rows)
		}
		if end > start {
			if e := m.decodeRows(r, start, end); e != nil && err == nil {
				err = e
			}
		}
		if i == len(slices) {
			break
		}
		r = slices[i].r
		start = slices[i].row % m.rows
		if r.u(1) == 1 {
			if e := d.pictureHeader(r, false); e != nil {
				return e
			}
		}
	}
	lf := d.ep.loopFilter
	switch p.typ {
	case picI, picBI:
		m.overlap()
		m.putIntra()
		if lf {
			m.loopFilterIntra()
		}
	case picP:
		m.overlap()
		m.putIntra()
		if lf {
			if p.fcm == ilaceFrame {
				m.loopFilterIntfr()
			} else {
				m.loopFilterP()
			}
		}
	case picB:
		if lf {
			switch p.fcm {
			case ilaceFrame:
				m.loopFilterIntfr()
			case ilaceField:
				m.loopFilterBIntfi()
			default:
				m.loopFilterIntra()
			}
		}
	}
	return err
}

// decodeRows decodes macroblock rows [start, end) of a slice.
func (m *mbContext) decodeRows(r *bits, start, end int) error {
	d := m.d
	p := &d.ph
	m.r = r
	m.esc3Level = 0
	m.setCodingSets()
	m.sliceTop[start] = true
	typ := p.typ
	if typ == picI || typ == picBI {
		// Coded block prediction does not reach above the slice.
		row := (2 * start) * m.bw
		clear(m.coded[row : row+m.bw])
	}
	for y := start; y < end; y++ {
		for x := range d.mbw {
			m.at(x, y)
			m.firstLine = y == start
			var err error
			switch {
			case typ == picI || typ == picBI:
				err = m.iMB()
			case typ == picP && p.fcm == ilaceFrame:
				err = m.pMBIntfr()
			case typ == picP && p.fcm == ilaceField:
				err = m.pMBIntfi()
			case typ == picP:
				err = m.pMB()
			case p.fcm == ilaceFrame:
				err = m.bMBIntfr()
			case p.fcm == ilaceField:
				err = m.bMBIntfi()
			default:
				err = m.bMB()
			}
			if err == nil && r.left() < 0 {
				err = errShort
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}
