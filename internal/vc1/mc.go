package vc1

// Motion compensation (8.3.6): luma by bicubic (or bilinear half-sample)
// interpolation of quarter-sample vectors, chroma bilinear at quarter
// samples; samples beyond the reference's coded area repeat its edge, and
// intensity compensation maps a reference's samples through tables first.

// refPlane is a reference plane as prediction reads it.
type refPlane struct {
	pix    []byte
	stride int
	w, h   int // the coded area
	// interlaced references repeat each field's edge for its own lines.
	interlaced bool
	lut        *[2][256]uint8 // intensity compensation, by line parity
}

// fetch copies a w x h area into dst (stride w): columns from x, lines
// from frame line y every step lines (parity < 0), or from line y of the
// field of parity. What lies outside repeats the edge.
func (p *refPlane) fetch(dst []byte, x, y, w, h, step, parity int) {
	inside := x >= 0 && x+w <= p.w
	for j := range h {
		row := dst[j*w : j*w+w]
		r := y + j*step
		if parity >= 0 {
			r = 2*(y+j) + parity
		}
		var sy int
		if p.interlaced {
			fr := min(max(r>>1, 0), p.h>>1-1)
			sy = 2*fr + r&1
		} else {
			sy = min(max(r, 0), p.h-1)
		}
		src := p.pix[sy*p.stride:]
		if inside {
			copy(row, src[x:x+w])
		} else {
			for i := range row {
				row[i] = src[min(max(x+i, 0), p.w-1)]
			}
		}
		if p.lut != nil {
			lut := &p.lut[r&1]
			for i, v := range row {
				row[i] = lut[v]
			}
		}
	}
}

// view gives the area fetch would copy, in place when it lies inside the
// plane and no table applies: the slice from its top left, and its stride.
func (p *refPlane) view(buf []byte, x, y, w, h, step, parity int) ([]byte, int) {
	if p.lut == nil && x >= 0 && x+w <= p.w {
		top, stride := y, p.stride*step
		last := y + (h-1)*step
		if parity >= 0 {
			top, stride = 2*y+parity, 2*p.stride
			last = 2*(y+h-1) + parity
		}
		if top >= 0 && last < p.h {
			return p.pix[top*p.stride+x:], stride
		}
	}
	p.fetch(buf, x, y, w, h, step, parity)
	return buf, w
}

// bicubicTaps are the taps for a quarter (1), half (2) and three quarter
// (3) position, and the shift that normalizes them.
var bicubicTaps = [4][4]int{{0, 64, 0, 0}, {-4, 53, 18, -3}, {-1, 9, 9, -1}, {-3, 18, 53, -4}}

var mspelShift = [4]int{0, 6, 4, 6}

// mspel predicts a w x h luma block at (hmode, vmode) quarter positions
// from src (the block's top left at src[sstride+1]: one column before,
// two after, one line before, two after), storing (avg false) or averaging
// into dst.
func mspel(dst []byte, dstride int, src []byte, sstride, w, h, hmode, vmode int, rnd int, avg bool) {
	o := sstride + 1 // the block's first sample in src
	put := func(d []byte, i, v int) {
		c := clip8(v)
		if avg {
			c = uint8((int(d[i]) + int(c) + 1) >> 1)
		}
		d[i] = c
	}
	switch {
	case vmode != 0 && hmode != 0:
		shiftV := [4]int{0, 5, 1, 5}
		shift := uint((shiftV[hmode] + shiftV[vmode]) >> 1)
		r := 1<<(shift-1) + rnd - 1
		tw := w + 3
		var tmpBuf [19 * 16]int32
		tmp := tmpBuf[:tw*h]
		v := &bicubicTaps[vmode]
		v0, v1, v2, v3 := v[0], v[1], v[2], v[3]
		for j := range h {
			s0 := src[o+j*sstride-sstride-1:]
			s1 := src[o+j*sstride-1:]
			s2 := src[o+j*sstride+sstride-1:]
			s3 := src[o+j*sstride+2*sstride-1:]
			t := tmp[j*tw : j*tw+tw]
			for i := range t {
				t[i] = int32((v0*int(s0[i]) + v1*int(s1[i]) + v2*int(s2[i]) + v3*int(s3[i]) + r) >> shift)
			}
		}
		hc := &bicubicTaps[hmode]
		h0, h1, h2, h3 := int32(hc[0]), int32(hc[1]), int32(hc[2]), int32(hc[3])
		r2 := int32(64 - rnd)
		for j := range h {
			t := tmp[j*tw : j*tw+tw]
			d := dst[j*dstride : j*dstride+w]
			for i := range d {
				put(d, i, int((h0*t[i]+h1*t[i+1]+h2*t[i+2]+h3*t[i+3]+r2)>>7)) //nolint:gosec // t has w+3 entries
			}
		}
	case vmode != 0:
		v := &bicubicTaps[vmode]
		v0, v1, v2, v3 := v[0], v[1], v[2], v[3]
		sh := uint(mspelShift[vmode])
		add := 1<<(sh-1) - (1 - rnd)
		for j := range h {
			s0 := src[o+j*sstride-sstride:]
			s1 := src[o+j*sstride:]
			s2 := src[o+j*sstride+sstride:]
			s3 := src[o+j*sstride+2*sstride:]
			d := dst[j*dstride : j*dstride+w]
			for i := range d {
				put(d, i, (v0*int(s0[i])+v1*int(s1[i])+v2*int(s2[i])+v3*int(s3[i])+add)>>sh)
			}
		}
	case hmode != 0:
		c := &bicubicTaps[hmode]
		c0, c1, c2, c3 := c[0], c[1], c[2], c[3]
		sh := uint(mspelShift[hmode])
		add := 1<<(sh-1) - rnd
		for j := range h {
			s := src[o+j*sstride-1:]
			d := dst[j*dstride : j*dstride+w]
			for i := range d {
				put(d, i, (c0*int(s[i])+c1*int(s[i+1])+c2*int(s[i+2])+c3*int(s[i+3])+add)>>sh)
			}
		}
	default:
		for j := range h {
			s := src[o+j*sstride : o+j*sstride+w]
			d := dst[j*dstride : j*dstride+w]
			if !avg {
				copy(d, s)
				continue
			}
			for i := range d {
				d[i] = uint8((int(d[i]) + int(s[i]) + 1) >> 1)
			}
		}
	}
}

// hpel predicts a w x h block at half sample positions (dx, dy each 0 or
// 1) bilinearly; noRnd rounds the averages down.
func hpel(dst []byte, dstride int, src []byte, sstride, w, h, dx, dy int, noRnd, avg bool) {
	r2, r4 := 1, 2
	if noRnd {
		r2, r4 = 0, 1
	}
	for j := range h {
		s0 := src[j*sstride:]
		d := dst[j*dstride : j*dstride+w]
		switch {
		case dx == 0 && dy == 0:
			if !avg {
				copy(d, s0[:w])
				continue
			}
			for i := range d {
				d[i] = uint8((int(d[i]) + int(s0[i]) + 1) >> 1)
			}
			continue
		case dy == 0:
			for i := range d {
				v := (int(s0[i]) + int(s0[i+1]) + r2) >> 1
				if avg {
					v = (int(d[i]) + v + 1) >> 1
				}
				d[i] = uint8(v)
			}
		case dx == 0:
			s1 := src[(j+1)*sstride:]
			for i := range d {
				v := (int(s0[i]) + int(s1[i]) + r2) >> 1
				if avg {
					v = (int(d[i]) + v + 1) >> 1
				}
				d[i] = uint8(v)
			}
		default:
			s1 := src[(j+1)*sstride:]
			for i := range d {
				v := (int(s0[i]) + int(s0[i+1]) + int(s1[i]) + int(s1[i+1]) + r4) >> 2
				if avg {
					v = (int(d[i]) + v + 1) >> 1
				}
				d[i] = uint8(v)
			}
		}
	}
}

// chromaMC predicts a w x h chroma block at eighth sample position (x, y)
// bilinearly.
func chromaMC(dst []byte, dstride int, src []byte, sstride, w, h, x, y int, noRnd, avg bool) {
	a := (8 - x) * (8 - y)
	b := x * (8 - y)
	c := (8 - x) * y
	dd := x * y
	rnd := 32
	if noRnd {
		rnd = 28
	}
	for j := range h {
		s0 := src[j*sstride : j*sstride+w+1]
		s1 := src[(j+1)*sstride : (j+1)*sstride+w+1]
		d := dst[j*dstride : j*dstride+w]
		for i := range d {
			v := (a*int(s0[i]) + b*int(s0[i+1]) + c*int(s1[i]) + dd*int(s1[i+1]) + rnd) >> 6
			if avg {
				v = (int(d[i]) + v + 1) >> 1
			}
			d[i] = uint8(v)
		}
	}
}


// refFrame picks direction dir's reference for a prediction from the field
// of parity refBottom (field pictures): the frame, its intensity tables
// and whether they apply.
func (m *mbContext) refFrame(dir int, refBottom bool) (f *frame, luty, lutuv *[2][256]uint8, ic bool) {
	d := m.d
	if dir == 1 {
		return d.next, &d.nextLUTY, &d.nextLUTUV, d.nextUseIC
	}
	if d.ph.fieldMode && d.curFieldBottom != refBottom && d.secondField {
		// The first field of this frame.
		return d.cur, d.currLUTY(), d.currLUTUV(), *d.currUseIC()
	}
	return d.last, &d.lastLUTY, &d.lastLUTUV, d.lastUseIC
}

func (m *mbContext) planes(f *frame, luty, lutuv *[2][256]uint8, ic bool) (y, cb, cr refPlane) {
	d := m.d
	il := f.interlaced || f == d.cur
	y = refPlane{pix: f.y, stride: d.strideY, w: d.width, h: d.height, interlaced: il}
	cb = refPlane{pix: f.cb, stride: d.strideC, w: d.width >> 1, h: d.height >> 1, interlaced: il}
	cr = refPlane{pix: f.cr, stride: d.strideC, w: d.width >> 1, h: d.height >> 1, interlaced: il}
	if ic {
		y.lut, cb.lut, cr.lut = luty, lutuv, lutuv
	}
	return y, cb, cr
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// noRef reports whether prediction from direction dir has no reference
// (as ffmpeg: no forward reference, unless a field predicts from this
// frame's first).
func (m *mbContext) noRef(dir int) bool {
	d := m.d
	return (!d.ph.fieldMode || (d.refFieldBottom[dir] && d.curFieldBottom)) && d.last == nil
}

// lumaPred predicts a w x h luma block of the picture at (bx, by) (picture
// coordinates) from ref, its source at (sx, sy) with quarter fractions
// (fx, fy): lines step apart (2: one field of an interlaced frame), or in
// the field of parity.
func (m *mbContext) lumaPred(ref *refPlane, dst []byte, dstride, sx, sy, w, h, fx, fy, step, parity int, avg bool) {
	p := &m.d.ph
	buf := m.mcBuf[:]
	rnd := boolInt(p.rnd)
	if p.mspel {
		top := sy - step
		if parity >= 0 {
			top = sy - 1
		}
		src, stride := ref.view(buf, sx-1, top, w+3, h+3, step, parity)
		mspel(dst, dstride, src, stride, w, h, fx, fy, rnd, avg)
		return
	}
	src, stride := ref.view(buf, sx, sy, w+1, h+1, step, parity)
	hpel(dst, dstride, src, stride, w, h, fx>>1, fy>>1, p.rnd, avg)
}

// chromaPred predicts a w x h chroma block (both planes) from (sx, sy) at
// eighth fractions (fx, fy).
func (m *mbContext) chromaPred(cb, cr *refPlane, dcb, dcr []byte, dstride, sx, sy, w, h, fx, fy, step, parity int, avg bool) {
	buf := m.mcBuf[:]
	for i, pl := range [2]*refPlane{cb, cr} {
		dst := dcb
		if i == 1 {
			dst = dcr
		}
		src, stride := pl.view(buf, sx, sy, w+1, h+1, step, parity)
		chromaMC(dst, dstride, src, stride, w, h, fx, fy, m.d.ph.rnd, avg)
	}
}

// clipY clips a source line as the picture type does: interlaced frames
// keep its parity (and luma there stops at the coded height, rather than
// a line after).
func (m *mbContext) clipY(y, lo, hi int, luma bool) int {
	if m.d.ph.fcm == ilaceFrame {
		if luma {
			hi--
		}
		return min(max(y, lo+y&1), hi+y&1)
	}
	return min(max(y, lo), hi)
}

// mc1MV predicts the macroblock from direction dir with its vector.
func (m *mbContext) mc1MV(dir int) {
	d := m.d
	p := &d.ph
	if m.noRef(dir) {
		return
	}
	v := m.cmv[dir][0]
	mx, my := int(v.x), int(v.y)
	if p.typ == picP {
		for k := range 4 {
			m.mvs[1][m.bi[k]+m.blkOff] = v
		}
	}
	uvmx, uvmy := chromaMV(mx), chromaMV(my)
	m.uvMV[m.mbi] = mv{int16(uvmx), int16(uvmy)}
	refBottom := d.refFieldBottom[dir]
	if p.fieldMode && d.curFieldBottom != refBottom {
		adj := -2 + 4*boolInt(d.curFieldBottom)
		my += adj
		uvmy += adj
	}
	if d.ep.fastUVMC && p.fcm != ilaceFrame {
		uvmx, uvmy = fastUV(uvmx, false), fastUV(uvmy, false)
	}
	f, luty, lutuv, ic := m.refFrame(dir, refBottom)
	if f == nil {
		return
	}
	y, cb, cr := m.planes(f, luty, lutuv, ic)
	m.predictMB(&y, &cb, &cr, mx, my, uvmx, uvmy, refBottom, false)
	if p.fieldMode {
		m.mvfC[dir][m.mbi+m.mbOff] = d.curFieldBottom != refBottom
	}
}

// predictMB predicts the whole macroblock (16x16 luma, 8x8 chroma) at
// luma vector (mx, my) and chroma vector (uvmx, uvmy).
func (m *mbContext) predictMB(y, cb, cr *refPlane, mx, my, uvmx, uvmy int, refBottom, avg bool) {
	d := m.d
	sx := min(max(m.mbx*16+mx>>2, -17), d.width)
	sy := m.clipY(m.mby*16+my>>2, -18, d.height+1, true)
	usx := min(max(m.mbx*8+uvmx>>2, -8), d.width>>1)
	usy := m.clipY(m.mby*8+uvmy>>2, -8, d.height>>1, false)
	parity := -1
	if d.ph.fieldMode {
		parity = boolInt(refBottom)
	}
	dy := m.vy[m.mby*16*m.vsy+m.mbx*16:]
	m.lumaPred(y, dy, m.vsy, sx, sy, 16, 16, mx&3, my&3, 1, parity, avg)
	off := m.mby*8*m.vsc + m.mbx*8
	m.chromaPred(cb, cr, m.vcb[off:], m.vcr[off:], m.vsc, usx, usy, 8, 8, (uvmx&3)<<1, (uvmy&3)<<1, 1, parity, avg)
}

// interpMC averages the backward prediction into the forward one (direct
// and interpolated B macroblocks).
func (m *mbContext) interpMC() {
	d := m.d
	p := &d.ph
	if !p.fieldMode && d.next == nil {
		return
	}
	v := m.cmv[1][0]
	mx, my := int(v.x), int(v.y)
	uvmx, uvmy := chromaMV(mx), chromaMV(my)
	refBottom := d.refFieldBottom[1]
	if p.fieldMode && d.curFieldBottom != refBottom {
		adj := -2 + 4*boolInt(d.curFieldBottom)
		my += adj
		uvmy += adj
	}
	if d.ep.fastUVMC {
		uvmx, uvmy = fastUV(uvmx, true), fastUV(uvmy, true)
	}
	if d.next == nil {
		return
	}
	y, cb, cr := m.planes(d.next, &d.nextLUTY, &d.nextLUTUV, d.nextUseIC)
	m.predictMB(&y, &cb, &cr, mx, my, uvmx, uvmy, refBottom, true)
}

// mc4MVLuma predicts luma block n with its own vector.
func (m *mbContext) mc4MVLuma(n, dir int, avg bool) {
	d := m.d
	p := &d.ph
	if m.noRef(dir) {
		return
	}
	v := m.cmv[dir][n]
	mx, my := int(v.x), int(v.y)
	refBottom := d.refFieldBottom[dir]
	f, luty, lutuv, ic := m.refFrame(dir, refBottom)
	if f == nil {
		return
	}
	if p.fieldMode && d.curFieldBottom != refBottom {
		my += -2 + 4*boolInt(d.curFieldBottom)
	}
	if p.typ == picP && n == 3 && p.fieldMode {
		tx, ty, opp := m.lumaMV(0)
		m.mvs[1][m.bi[0]+m.blkOff] = mv{int16(tx), int16(ty)}
		for k := range 4 {
			m.mvfY[1][m.bi[k]+m.blkOff] = opp > 2
		}
	}
	fieldMV := false
	if p.fcm == ilaceFrame {
		fieldMV = m.blkMVType[m.bi[n]]
		if p.typ == picP {
			m.mvs[1][m.bi[n]] = mv{int16(mx), int16(my)}
		}
		qx := m.mbx*16 + mx>>2
		qy := m.mby*8 + my>>3
		w, h := d.width, d.height>>1
		if qx < -17 {
			mx -= 4 * (qx + 17)
		} else if qx > w {
			mx -= 4 * (qx - w)
		}
		if qy < -18 {
			my -= 8 * (qy + 18)
		} else if qy > h+1 {
			my -= 8 * (qy - h - 1)
		}
	}
	var dst []byte
	dstride := m.vsy
	sx := m.mbx*16 + (n&1)*8 + mx>>2
	var sy int
	step := 1
	if fieldMV {
		dst = m.vy[(m.mby*16+boolInt(n > 1))*m.vsy+m.mbx*16+(n&1)*8:]
		dstride *= 2
		sy = m.mby*16 + boolInt(n > 1) + my>>2
		step = 2
	} else {
		dst = m.vy[(m.mby*16+(n&2)*4)*m.vsy+m.mbx*16+(n&1)*8:]
		sy = m.mby*16 + (n&2)*4 + my>>2
	}
	sx = min(max(sx, -17), d.width)
	sy = m.clipY(sy, -18, d.height+1, true)
	parity := -1
	if p.fieldMode {
		parity = boolInt(refBottom)
	}
	y, _, _ := m.planes(f, luty, lutuv, ic)
	m.lumaPred(&y, dst, dstride, sx, sy, 8, 8, mx&3, my&3, step, parity, avg)
}

// lumaMV gives the vector of a field 4MV macroblock's chroma: of its
// luma blocks' vectors from the field most of them use, and how many use
// the opposite field.
func (m *mbContext) lumaMV(dir int) (tx, ty, opp int) {
	idx := 0
	for k := range 4 {
		if m.mvfY[dir][m.bi[k]+m.blkOff] {
			idx |= 1 << k
		}
	}
	mv4 := &m.cmv[dir]
	opp = popcount4[idx]
	pick := func(set bool) (s [4]int, n int) {
		for k := range 4 {
			if (idx&(1<<k) != 0) == set {
				s[n] = k
				n++
			}
		}
		return
	}
	switch opp {
	case 0, 4:
		tx = median4(int(mv4[0].x), int(mv4[1].x), int(mv4[2].x), int(mv4[3].x))
		ty = median4(int(mv4[0].y), int(mv4[1].y), int(mv4[2].y), int(mv4[3].y))
	case 1, 3:
		s, _ := pick(opp == 3)
		tx = median3(int(mv4[s[0]].x), int(mv4[s[1]].x), int(mv4[s[2]].x))
		ty = median3(int(mv4[s[0]].y), int(mv4[s[1]].y), int(mv4[s[2]].y))
	case 2:
		s, _ := pick(false)
		tx = (int(mv4[s[0]].x) + int(mv4[s[1]].x)) / 2
		ty = (int(mv4[s[0]].y) + int(mv4[s[1]].y)) / 2
	}
	return tx, ty, opp
}

// mc4MVChroma predicts the chroma of a 4MV macroblock (progressive or
// field) with a vector made from its luma blocks' (8.3.5.4.4).
func (m *mbContext) mc4MVChroma(dir int) {
	d := m.d
	p := &d.ph
	if !p.fieldMode && d.last == nil {
		return
	}
	var tx, ty int
	var chromaRef bool
	if !p.fieldMode || !p.numref {
		var ok bool
		tx, ty, ok = m.chromaVector(dir)
		if !ok {
			m.mvs[1][m.bi[0]+m.blkOff] = mv{}
			m.uvMV[m.mbi] = mv{}
			return
		}
		chromaRef = d.refFieldBottom[dir]
	} else {
		var opp int
		tx, ty, opp = m.lumaMV(dir)
		chromaRef = d.curFieldBottom != (opp > 2)
	}
	if p.fieldMode && chromaRef && d.curFieldBottom && d.last == nil {
		return
	}
	m.mvs[1][m.bi[0]+m.blkOff] = mv{int16(tx), int16(ty)}
	uvmx, uvmy := chromaMV(tx), chromaMV(ty)
	m.uvMV[m.mbi] = mv{int16(uvmx), int16(uvmy)}
	if d.ep.fastUVMC {
		uvmx, uvmy = fastUV(uvmx, false), fastUV(uvmy, false)
	}
	if d.curFieldBottom != chromaRef {
		uvmy += 2 - 4*boolInt(chromaRef)
	}
	usx := min(max(m.mbx*8+uvmx>>2, -8), d.width>>1)
	usy := min(max(m.mby*8+uvmy>>2, -8), d.height>>1)
	f, luty, lutuv, ic := m.refFrame(dir, chromaRef)
	if f == nil {
		return
	}
	_, cb, cr := m.planes(f, luty, lutuv, ic)
	parity := -1
	if p.fieldMode {
		parity = boolInt(chromaRef)
	}
	off := m.mby*8*m.vsc + m.mbx*8
	m.chromaPred(&cb, &cr, m.vcb[off:], m.vcr[off:], m.vsc, usx, usy, 8, 8, (uvmx&3)<<1, (uvmy&3)<<1, 1, parity, false)
	if p.fieldMode {
		m.mvfC[dir][m.mbi+m.mbOff] = d.curFieldBottom != chromaRef
	}
}

// chromaVector derives a 4MV macroblock's chroma vector from its inter
// luma blocks' (false with fewer than two: the chroma is intra).
func (m *mbContext) chromaVector(dir int) (tx, ty int, ok bool) {
	idx := 0
	for k := range 4 {
		if !m.intraY[m.bi[k]] {
			idx |= 1 << k
		}
	}
	mv4 := &m.cmv[dir]
	var s [4]int
	j := 0
	for k := range 4 {
		if idx&(1<<k) != 0 {
			s[j] = k
			j++
		}
	}
	switch j {
	case 4:
		tx = median4(int(mv4[0].x), int(mv4[1].x), int(mv4[2].x), int(mv4[3].x))
		ty = median4(int(mv4[0].y), int(mv4[1].y), int(mv4[2].y), int(mv4[3].y))
	case 3:
		tx = median3(int(mv4[s[0]].x), int(mv4[s[1]].x), int(mv4[s[2]].x))
		ty = median3(int(mv4[s[0]].y), int(mv4[s[1]].y), int(mv4[s[2]].y))
	case 2:
		tx = (int(mv4[s[0]].x) + int(mv4[s[1]].x)) / 2
		ty = (int(mv4[s[0]].y) + int(mv4[s[1]].y)) / 2
	default:
		return 0, 0, false
	}
	return tx, ty, true
}

var fieldChromaRound = [16]int{0, 0, 1, 2, 4, 4, 5, 6, 2, 2, 3, 8, 6, 6, 7, 12}

// mc4MVChroma4 predicts an interlaced frame macroblock's chroma as four
// 4x4 blocks, each with its luma block's vector: the top two from
// direction dir, the bottom two from dir2.
func (m *mbContext) mc4MVChroma4(dir, dir2 int, avg bool) {
	d := m.d
	fieldMV := m.blkMVType[m.bi[0]]
	var uvx, uvy [4]int
	for i := range 4 {
		dd := dir
		if i >= 2 {
			dd = dir2
		}
		v := m.cmv[dd][i]
		uvx[i] = chromaMV(int(v.x))
		ty := int(v.y)
		if fieldMV {
			uvy[i] = (ty>>4)*8 + fieldChromaRound[ty&0xf]
		} else {
			uvy[i] = chromaMV(ty)
		}
	}
	vdist, step := 4, 1
	if fieldMV {
		vdist, step = 1, 2
	}
	for i := range 4 {
		dd := dir
		if i >= 2 {
			dd = dir2
		}
		lower := 0
		if i&2 != 0 {
			lower = vdist
		}
		off := (m.mby*8+lower)*m.vsc + m.mbx*8 + (i&1)*4
		usx := min(max(m.mbx*8+(i&1)*4+uvx[i]>>2, -8), d.width>>1)
		usy := m.clipY(m.mby*8+lower+uvy[i]>>2, -8, d.height>>1, false)
		f, luty, lutuv, ic := d.last, &d.lastLUTY, &d.lastLUTUV, d.lastUseIC
		if dd == 1 {
			f, luty, lutuv, ic = d.next, &d.nextLUTY, &d.nextLUTUV, d.nextUseIC
		}
		if f == nil {
			return
		}
		_, cb, cr := m.planes(f, luty, lutuv, ic)
		m.chromaPred(&cb, &cr, m.vcb[off:], m.vcr[off:], m.vsc*step, usx, usy, 4, 4, (uvx[i]&3)<<1, (uvy[i]&3)<<1, step, -1, avg)
	}
}

// chromaMV derives a chroma vector component from a luma one.
func chromaMV(v int) int {
	if v&3 == 3 {
		return (v + 1) >> 1
	}
	return v >> 1
}

// fastUV rounds a chroma vector component to half samples (FASTUVMC):
// towards zero, or (as ffmpeg does for a B macroblock's averaged
// backward prediction) away from it.
func fastUV(v int, away bool) int {
	if away {
		if v < 0 {
			return v - v&1
		}
		return v + v&1
	}
	if v < 0 {
		return v + v&1
	}
	return v - v&1
}

var popcount4 = [16]int{0, 1, 1, 2, 1, 2, 2, 3, 1, 2, 2, 3, 2, 3, 3, 4}
