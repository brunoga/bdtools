package mpeg2

// Motion compensation (7.6) and reconstruction.

// plane is a picture plane, or one of its fields: rows step apart.
type plane struct {
	b    []byte
	off  int // the first row
	step int // between rows
	w, h int // in samples, rows
}

func (d *Decoder) planes(f *frame, parity int, field bool) (y, cb, cr plane) {
	sy, sc := d.strideY, d.strideC
	hy, hc := d.lumaH, d.lumaH/2
	if !field {
		return plane{b: f.y, step: sy, w: sy, h: hy}, plane{b: f.cb, step: sc, w: sc, h: hc}, plane{b: f.cr, step: sc, w: sc, h: hc}
	}
	return plane{b: f.y, off: parity * sy, step: 2 * sy, w: sy, h: hy / 2},
		plane{b: f.cb, off: parity * sc, step: 2 * sc, w: sc, h: hc / 2},
		plane{b: f.cr, off: parity * sc, step: 2 * sc, w: sc, h: hc / 2}
}

// at is the sample at (x, y), the nearest one inside the plane when
// outside it.
func (p *plane) at(x, y int) int {
	x = min(max(x, 0), p.w-1)
	y = min(max(y, 0), p.h-1)
	return int(p.b[p.off+y*p.step+x])
}

// pred predicts a w×h block at (x, y) of plane p moved by (dx, dy) in half
// samples, into dst (dst rows dstep apart): averaged with what is there
// when avg.
func pred(dst []uint8, dstep int, p *plane, x, y, dx, dy, w, h int, avg bool) {
	x += dx >> 1
	y += dy >> 1
	hx, hy := dx&1, dy&1
	inside := x >= 0 && y >= 0 && x+w+hx <= p.w && y+h+hy <= p.h
	for j := range h {
		row := dst[j*dstep:]
		for i := range w {
			var a, b, c, e int
			if inside {
				o := p.off + (y+j)*p.step + x + i
				a = int(p.b[o])
				if hx != 0 {
					b = int(p.b[o+1])
				}
				if hy != 0 {
					c = int(p.b[o+p.step])
					if hx != 0 {
						e = int(p.b[o+p.step+1])
					}
				}
			} else {
				a, b = p.at(x+i, y+j), p.at(x+i+1, y+j)
				c, e = p.at(x+i, y+j+1), p.at(x+i+1, y+j+1)
			}
			var v int
			switch {
			case hx == 0 && hy == 0:
				v = a
			case hy == 0:
				v = (a + b + 1) >> 1
			case hx == 0:
				v = (a + c + 1) >> 1
			default:
				v = (a + b + c + e + 2) >> 2
			}
			if avg {
				v = (int(row[i]) + v + 1) >> 1
			}
			row[i] = uint8(v)
		}
	}
}

// chromaVec is a luma vector component for chroma (4:2:0): halved, toward
// zero.
func chromaVec(v int) int { return v / 2 }

// predict forms the macroblock's prediction in predY and predC.
func (d *Decoder) predict(addr int, st *mbState) {
	mbx, mby := addr%d.mbw, addr/d.mbw
	x, y := mbx*16, mby*16
	first := true
	for s := range 2 {
		if s == 0 && st.mbType&mbFor == 0 || s == 1 && st.mbType&mbBack == 0 {
			continue
		}
		d.predictDir(st, s, x, y, !first)
		first = false
	}
}

// ref is the reference frame for direction s, and for a field's
// prediction from field sel whether that is the current frame's first
// field (the second field of a P frame predicting from the opposite
// parity).
func (d *Decoder) ref(s, sel int) *frame {
	if s == 1 {
		return d.bwd
	}
	if d.pic.codingType == 3 {
		return d.fwd
	}
	// P: the anchor before the current one.
	if d.pic.structure != framePic && d.second {
		cur := 0
		if d.pic.structure == bottomField {
			cur = 1
		}
		if sel != cur {
			return d.cur // the first field of this frame
		}
	}
	if d.fwd == nil {
		return d.cur // a stream's first frame, an I field and a P field
	}
	return d.fwd
}

// predictDir forms (or averages in) direction s's prediction.
func (d *Decoder) predictDir(st *mbState, s, x, y int, avg bool) {
	v := st.vec
	if d.pic.structure == framePic {
		switch st.motionType {
		case motionFrame:
			py, pcb, pcr := d.planes(d.ref(s, 0), 0, false)
			mx, my := v[0][s][0], v[0][s][1]
			pred(d.predY[:], 16, &py, x, y, mx, my, 16, 16, avg)
			cx, cy := chromaVec(mx), chromaVec(my)
			pred(d.predC[0][:], 8, &pcb, x/2, y/2, cx, cy, 8, 8, avg)
			pred(d.predC[1][:], 8, &pcr, x/2, y/2, cx, cy, 8, 8, avg)
		case motionField:
			// Each field of the macroblock from a field of the reference.
			for f := range 2 {
				sel := st.fieldSelect[f][s]
				py, pcb, pcr := d.planes(d.ref(s, sel), sel, true)
				mx, my := v[f][s][0], v[f][s][1]
				pred(d.predY[f*16:], 32, &py, x, y/2, mx, my, 16, 8, avg)
				cx, cy := chromaVec(mx), chromaVec(my)
				pred(d.predC[0][f*8:], 16, &pcb, x/2, y/4, cx, cy, 8, 4, avg)
				pred(d.predC[1][f*8:], 16, &pcr, x/2, y/4, cx, cy, 8, 4, avg)
			}
		case motionDual:
			dmv := d.dualPrime(v[0][s][0], v[0][s][1], st.dmv)
			ref := d.ref(s, 0)
			for f := range 2 { // the macroblock's top, then bottom field
				// From the field of the same parity, then the other's
				// derived vector, averaged.
				py, pcb, pcr := d.planes(ref, f, true)
				mx, my := v[0][s][0], v[0][s][1]
				pred(d.predY[f*16:], 32, &py, x, y/2, mx, my, 16, 8, false)
				pred(d.predC[0][f*8:], 16, &pcb, x/2, y/4, chromaVec(mx), chromaVec(my), 8, 4, false)
				pred(d.predC[1][f*8:], 16, &pcr, x/2, y/4, chromaVec(mx), chromaVec(my), 8, 4, false)
				py, pcb, pcr = d.planes(ref, 1-f, true)
				mx, my = dmv[f][0], dmv[f][1]
				pred(d.predY[f*16:], 32, &py, x, y/2, mx, my, 16, 8, true)
				pred(d.predC[0][f*8:], 16, &pcb, x/2, y/4, chromaVec(mx), chromaVec(my), 8, 4, true)
				pred(d.predC[1][f*8:], 16, &pcr, x/2, y/4, chromaVec(mx), chromaVec(my), 8, 4, true)
			}
		}
		return
	}
	// Field pictures.
	cur := 0
	if d.pic.structure == bottomField {
		cur = 1
	}
	switch st.motionType {
	case motionField:
		sel := st.fieldSelect[0][s]
		py, pcb, pcr := d.planes(d.ref(s, sel), sel, true)
		mx, my := v[0][s][0], v[0][s][1]
		pred(d.predY[:], 16, &py, x, y, mx, my, 16, 16, avg)
		pred(d.predC[0][:], 8, &pcb, x/2, y/2, chromaVec(mx), chromaVec(my), 8, 8, avg)
		pred(d.predC[1][:], 8, &pcr, x/2, y/2, chromaVec(mx), chromaVec(my), 8, 8, avg)
	case motion16x8:
		for h := range 2 {
			sel := st.fieldSelect[h][s]
			py, pcb, pcr := d.planes(d.ref(s, sel), sel, true)
			mx, my := v[h][s][0], v[h][s][1]
			pred(d.predY[h*128:], 16, &py, x, y+h*8, mx, my, 16, 8, avg)
			pred(d.predC[0][h*32:], 8, &pcb, x/2, y/2+h*4, chromaVec(mx), chromaVec(my), 8, 4, avg)
			pred(d.predC[1][h*32:], 8, &pcr, x/2, y/2+h*4, chromaVec(mx), chromaVec(my), 8, 4, avg)
		}
	case motionDual:
		dmv := d.dualPrime(v[0][s][0], v[0][s][1], st.dmv)
		py, pcb, pcr := d.planes(d.ref(s, cur), cur, true)
		mx, my := v[0][s][0], v[0][s][1]
		pred(d.predY[:], 16, &py, x, y, mx, my, 16, 16, false)
		pred(d.predC[0][:], 8, &pcb, x/2, y/2, chromaVec(mx), chromaVec(my), 8, 8, false)
		pred(d.predC[1][:], 8, &pcr, x/2, y/2, chromaVec(mx), chromaVec(my), 8, 8, false)
		py, pcb, pcr = d.planes(d.ref(s, 1-cur), 1-cur, true)
		mx, my = dmv[0][0], dmv[0][1]
		pred(d.predY[:], 16, &py, x, y, mx, my, 16, 16, true)
		pred(d.predC[0][:], 8, &pcb, x/2, y/2, chromaVec(mx), chromaVec(my), 8, 8, true)
		pred(d.predC[1][:], 8, &pcr, x/2, y/2, chromaVec(mx), chromaVec(my), 8, 8, true)
	}
}

// dualPrime derives dual prime's vectors for the opposite parity (7.6.3.6):
// in a frame picture [0] predicts the top field from the bottom one and
// [1] the bottom from the top; in a field picture [0] is the one.
func (d *Decoder) dualPrime(mx, my int, dm [2]int) (out [2][2]int) {
	half := func(v, m int) int {
		t := v * m
		if v > 0 {
			t++
		}
		return t >> 1
	}
	if d.pic.structure == framePic {
		m0, m1 := 1, 3 // the top field from the bottom is the nearer when the top comes first
		if !d.pic.tff {
			m0, m1 = 3, 1
		}
		out[0] = [2]int{half(mx, m0) + dm[0], half(my, m0) + dm[1] - 1}
		out[1] = [2]int{half(mx, m1) + dm[0], half(my, m1) + dm[1] + 1}
		return
	}
	out[0] = [2]int{half(mx, 1) + dm[0], half(my, 1) + dm[1]}
	if d.pic.structure == topField {
		out[0][1]--
	} else {
		out[0][1]++
	}
	return
}

// store adds the coded blocks' residual to the prediction (or, intra, takes
// the residual) and writes the macroblock into the picture.
func (d *Decoder) store(addr int, st *mbState, cbp int, fieldDCT bool) {
	intra := st.mbType&mbIntra != 0
	if intra {
		d.predY = [256]uint8{}
		d.predC = [2][64]uint8{}
	}
	// Luma blocks.
	for b := range 4 {
		if cbp&(1<<(5-b)) == 0 {
			continue
		}
		off, step := (b&1)*8+(b>>1)*128, 16
		if fieldDCT {
			off, step = (b&1)*8+(b>>1)*16, 32
		}
		res := &d.resid[b]
		for j := range 8 {
			row := d.predY[off+j*step:]
			for i := range 8 {
				row[i] = clip(int32(row[i]) + res[j*8+i])
			}
		}
	}
	for c := range 2 {
		if cbp&(1<<(1-c)) == 0 {
			continue
		}
		res := &d.resid[4+c]
		for k := range 64 {
			d.predC[c][k] = clip(int32(d.predC[c][k]) + res[k])
		}
	}
	// Into the picture.
	mbx, mby := addr%d.mbw, addr/d.mbw
	f := d.cur
	sy, sc := d.strideY, d.strideC
	yOff, yStep := mby*16*sy, sy
	cOff, cStep := mby*8*sc, sc
	if d.pic.structure != framePic {
		p := 0
		if d.pic.structure == bottomField {
			p = 1
		}
		yOff, yStep = (mby*32+p)*sy, 2*sy
		cOff, cStep = (mby*16+p)*sc, 2*sc
	}
	yOff += mbx * 16
	cOff += mbx * 8
	for j := range 16 {
		copy(f.y[yOff+j*yStep:yOff+j*yStep+16], d.predY[j*16:j*16+16])
	}
	for j := range 8 {
		copy(f.cb[cOff+j*cStep:cOff+j*cStep+8], d.predC[0][j*8:j*8+8])
		copy(f.cr[cOff+j*cStep:cOff+j*cStep+8], d.predC[1][j*8:j*8+8])
	}
}

func clip(v int32) uint8 {
	return uint8(min(max(v, 0), 255))
}
