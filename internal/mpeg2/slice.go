package mpeg2

// Motion types: frame_motion_type in frame pictures, field_motion_type in
// field pictures.
const (
	motionField   = 1 // field prediction (frame pictures); field prediction (field pictures)
	motionFrame   = 2 // frame prediction (frame pictures); 16x8 (field pictures)
	motionDual    = 3 // dual prime
	motion16x8    = 2
	motionDefault = 0
)

// slice decodes one slice: code is its start code's last byte (the row).
func (d *Decoder) slice(code int, b []byte) error {
	d.r = bits{b: b}
	r := &d.r
	row := code - 1
	if d.height > 2800 {
		row += r.u(3) << 7
	}
	rows := d.mbh
	if d.pic.structure != framePic {
		rows /= 2
	}
	if row >= rows {
		return errStream
	}
	d.qscale = d.scale(r.u(5))
	if r.peek(1) == 1 { // intra_slice_flag, intra_slice, reserved_bits
		r.skip(9)
	}
	for r.u(1) == 1 { // extra_bit_slice, extra_information_slice
		r.skip(8)
	}
	d.resetDC()
	d.pmv = [2][2][2]int{}
	addr := row*d.mbw - 1
	first := true
	for {
		if !first && (r.left() < 1 || r.peek(min(23, r.left())) == 0) {
			return nil // the slice's end: what is left is the zeros before the next start code
		}
		if r.left() < 1 {
			return errShort
		}
		inc := 0
		for {
			v, ok := mbIncrement.read(r)
			if !ok {
				return errStream
			}
			if v == -1 {
				inc += 33
				continue
			}
			if v == -2 { // stuffing (MPEG-1)
				continue
			}
			inc += v
			break
		}
		if first {
			addr += inc
		} else {
			// Skipped macroblocks, then the coded one.
			for range inc - 1 {
				addr++
				if addr >= rows*d.mbw {
					return errStream
				}
				d.skipped(addr)
			}
			addr++
		}
		first = false
		if addr >= rows*d.mbw {
			return errStream
		}
		if err := d.macroblock(addr); err != nil {
			return err
		}
	}
}

func (d *Decoder) scale(code int) int {
	if d.pic.qScaleType {
		return nonLinearScale[code]
	}
	return 2 * code
}

func (d *Decoder) resetDC() {
	v := 1 << (7 + d.pic.dcPrecision)
	d.dcPred = [3]int{v, v, v}
}

// skipped reconstructs a skipped macroblock: a P picture's from the
// reference with no motion; a B picture's in the directions of the
// macroblock before it, with the motion vector predictors as its vectors.
// Either way by frame prediction in a frame picture, and from the field of
// the same parity in a field picture.
func (d *Decoder) skipped(addr int) {
	d.resetDC()
	st := mbState{mbType: mbFor, motionType: motionFrame}
	if d.pic.codingType == 3 {
		st.mbType = d.last.mbType & (mbFor | mbBack)
		for s := range 2 {
			st.vec[0][s], st.vec[1][s] = d.pmv[0][s], d.pmv[0][s]
		}
	} else {
		d.pmv = [2][2][2]int{}
	}
	if d.pic.structure != framePic {
		st.motionType = motionField
		p := 0
		if d.pic.structure == bottomField {
			p = 1
		}
		st.fieldSelect = [2][2]int{{p, p}, {p, p}}
	}
	d.predict(addr, &st)
	d.store(addr, &st, 0, false)
}

// macroblock decodes the macroblock at addr.
func (d *Decoder) macroblock(addr int) error {
	r := &d.r
	var t *vlc
	switch d.pic.codingType {
	case 1:
		t = mbTypeI
	case 2:
		t = mbTypeP
	default:
		t = mbTypeB
	}
	mbType, ok := t.read(r)
	if !ok {
		return errStream
	}
	st := mbState{mbType: mbType}
	frameP := d.pic.structure == framePic
	if mbType&(mbFor|mbBack) != 0 {
		if frameP && d.pic.framePredFrame {
			st.motionType = motionFrame
		} else {
			st.motionType = r.u(2)
		}
	} else if frameP {
		st.motionType = motionFrame
	} else {
		st.motionType = motionField
	}
	fieldDCT := false
	if frameP && !d.pic.framePredFrame && mbType&(mbIntra|mbPattern) != 0 {
		fieldDCT = r.flag()
	}
	if mbType&mbQuant != 0 {
		d.qscale = d.scale(r.u(5))
	}
	intra := mbType&mbIntra != 0
	if intra {
		if d.pic.concealment {
			cm := st
			if frameP {
				cm.motionType = motionFrame
			} else {
				cm.motionType = motionField
			}
			if err := d.vectors(&cm, 0); err != nil {
				return err
			}
			r.skip(1) // marker_bit
		} else {
			d.pmv = [2][2][2]int{}
		}
	} else {
		d.resetDC()
		if mbType&mbFor != 0 {
			if err := d.vectors(&st, 0); err != nil {
				return err
			}
		}
		if mbType&mbBack != 0 {
			if err := d.vectors(&st, 1); err != nil {
				return err
			}
		}
		if d.pic.codingType == 2 && mbType&mbFor == 0 {
			// A P macroblock with no motion: predicted from the reference
			// with a zero vector.
			d.pmv = [2][2][2]int{}
			st.mbType |= mbFor
			if frameP {
				st.motionType = motionFrame
			} else {
				st.motionType = motionField
				p := 0
				if d.pic.structure == bottomField {
					p = 1
				}
				st.fieldSelect = [2][2]int{{p, p}, {p, p}}
			}
		}
	}
	cbp := 0
	switch {
	case intra:
		cbp = 0x3f
	case mbType&mbPattern != 0:
		v, ok := codedBlockPattern.read(r)
		if !ok {
			return errStream
		}
		cbp = v
	}
	for b := range 6 {
		if cbp&(1<<(5-b)) == 0 {
			continue
		}
		if err := d.block(b, intra); err != nil {
			return err
		}
	}
	if !intra {
		d.predict(addr, &st)
	}
	d.store(addr, &st, cbp, fieldDCT)
	d.last = st
	return nil
}

// vectors reads the motion vectors of direction s (0 forward, 1 backward)
// for the macroblock's motion type, updating the predictors.
func (d *Decoder) vectors(st *mbState, s int) error {
	r := &d.r
	frameP := d.pic.structure == framePic
	count, fieldFormat, dual := 1, !frameP, false
	switch {
	case frameP && st.motionType == motionField:
		count, fieldFormat = 2, true
	case frameP && st.motionType == motionDual:
		fieldFormat, dual = true, true
	case !frameP && st.motionType == motion16x8:
		count = 2
	case !frameP && st.motionType == motionDual:
		dual = true
	}
	if count == 1 {
		if fieldFormat && !dual {
			st.fieldSelect[0][s] = r.u(1)
		}
		if err := d.vector(st, 0, s, frameP && fieldFormat, dual); err != nil {
			return err
		}
		d.pmv[1][s] = d.pmv[0][s]
		st.vec[1][s] = st.vec[0][s]
		return nil
	}
	for i := range 2 {
		st.fieldSelect[i][s] = r.u(1)
		if err := d.vector(st, i, s, frameP, false); err != nil {
			return err
		}
	}
	return nil
}

// vector reads motion vector r of direction s. halfVertical: a field
// vector in a frame picture, whose vertical predictor is kept in frame
// units.
func (d *Decoder) vector(st *mbState, rr, s int, halfVertical, dual bool) error {
	r := &d.r
	for t := range 2 {
		code, ok := motionCode.read(r)
		if !ok {
			return errStream
		}
		if code != 0 && r.u(1) == 1 {
			code = -code
		}
		rSize := d.pic.fcode[s][t] - 1
		if rSize < 0 || rSize > 8 {
			return errStream
		}
		f := 1 << rSize
		delta := code
		if f != 1 && code != 0 {
			res := r.u(rSize)
			a := code
			if a < 0 {
				a = -a
			}
			delta = (a-1)*f + res + 1
			if code < 0 {
				delta = -delta
			}
		}
		pred := d.pmv[rr][s][t]
		if halfVertical && t == 1 {
			pred >>= 1
		}
		v := pred + delta
		low, high := -16*f, 16*f-1
		if v < low {
			v += 32 * f
		} else if v > high {
			v -= 32 * f
		}
		if halfVertical && t == 1 {
			d.pmv[rr][s][t] = v * 2
		} else {
			d.pmv[rr][s][t] = v
		}
		st.vec[rr][s][t] = v
		if dual {
			dm := 0
			switch {
			case r.u(1) == 0:
			case r.u(1) == 0:
				dm = 1
			default:
				dm = -1
			}
			st.dmv[t] = dm
		}
	}
	return nil
}

// block reads block b's coefficients and turns them into its residual.
func (d *Decoder) block(b int, intra bool) error {
	r := &d.r
	f := &d.blk
	*f = [64]int32{}
	scan := &scanZigzag
	if d.pic.alternateScan {
		scan = &scanAlternate
	}
	q := &d.nonIntraQ
	i := 0
	if intra {
		q = &d.intraQ
		cc := 0
		t := dcSizeLuma
		if b >= 4 {
			cc, t = b-3, dcSizeChroma
		}
		size, ok := t.read(r)
		if !ok {
			return errStream
		}
		diff := 0
		if size > 0 {
			diff = r.u(size)
			if diff < 1<<(size-1) {
				diff += 1 - 1<<size
			}
		}
		d.dcPred[cc] += diff
		f[0] = int32(d.dcPred[cc] << (3 - d.pic.dcPrecision))
		i = 1
	}
	table := coefB14
	if intra && d.pic.intraVLC {
		table = coefB15
	}
	firstNonIntra := !intra
	for {
		var run, level int
		if firstNonIntra && r.peek(1) == 1 {
			// The first coefficient of a non-intra block: '1s' is run 0,
			// level 1.
			r.skip(1)
			run, level = 0, 1
			if r.u(1) == 1 {
				level = -1
			}
		} else {
			v, ok := table.read(r)
			if !ok {
				return errStream
			}
			switch v {
			case coefEOB:
				goto done
			case coefEscape:
				run = r.u(6)
				level = r.u(12)
				if level >= 2048 {
					level -= 4096
				}
				if level == 0 || level == -2048 {
					return errStream
				}
			default:
				run, level = int(coefRun[v]), int(coefLevel[v])
				if r.u(1) == 1 {
					level = -level
				}
			}
		}
		firstNonIntra = false
		i += run
		if i > 63 {
			return errStream
		}
		pos := scan[i]
		// Inverse quantisation (7.4.2.3).
		k := 0
		if !intra {
			k = 1
			if level < 0 {
				k = -1
			}
		}
		v := (2*level + k) * int(q[pos]) * d.qscale / 32
		f[pos] = int32(min(max(v, -2048), 2047))
		i++
	}
done:
	// Mismatch control.
	var sum int32
	for _, v := range f {
		sum += v
	}
	if sum&1 == 0 {
		f[63] ^= 1
	}
	idct(f, &d.resid[b])
	return nil
}
