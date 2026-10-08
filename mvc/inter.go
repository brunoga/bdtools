package mvc

import "encoding/binary"

// Inter prediction (8.4.2).

// mcBuf holds scratch buffers for motion compensation.
type mcBuf struct {
	predY [2][16 * 16]byte // stride 16
	predC [2][2][8 * 8]byte
	tA    [16 * 16]byte
	tB    [16 * 16]byte
	tJ    [21 * 16]int16
}

func tap6(a, b, c, d, e, f int32) int32 {
	return a + f - 5*(b+e) + 20*(c+d)
}

// hpelH computes horizontal half-sample values b for a w x h block.
func hpelHGeneric(out []byte, ds int, src []byte, so, ss, w, h int) {
	for y := 0; y < h; y++ {
		r := src[so+y*ss-2 : so+y*ss+w+3]
		o := out[y*ds : y*ds+w]
		for x := range o {
			o[x] = clip255((tap6(int32(r[x]), int32(r[x+1]), int32(r[x+2]), int32(r[x+3]), int32(r[x+4]), int32(r[x+5])) + 16) >> 5)
		}
	}
}

// hpelV computes vertical half-sample values h for a w x h block.
func hpelVGeneric(out []byte, ds int, src []byte, so, ss, w, h int) {
	for y := 0; y < h; y++ {
		o := out[y*ds : y*ds+w]
		b := so + y*ss
		for x := range o {
			p := b + x
			o[x] = clip255((tap6(int32(src[p-2*ss]), int32(src[p-ss]), int32(src[p]), int32(src[p+ss]), int32(src[p+2*ss]), int32(src[p+3*ss])) + 16) >> 5)
		}
	}
}

// hpelJ computes the centre half-sample values j for a w x h block.
func hpelJGeneric(out []byte, ds int, tmp []int16, src []byte, so, ss, w, h int) {
	// vertical intermediate for columns -2..w+2
	tw := w + 5
	for y := 0; y < h; y++ {
		b := so + y*ss - 2
		t := tmp[y*21 : y*21+tw]
		for x := range t {
			p := b + x
			t[x] = int16(tap6(int32(src[p-2*ss]), int32(src[p-ss]), int32(src[p]), int32(src[p+ss]), int32(src[p+2*ss]), int32(src[p+3*ss])))
		}
	}
	for y := 0; y < h; y++ {
		t := tmp[y*21 : y*21+tw]
		o := out[y*ds : y*ds+w]
		for x := range o {
			o[x] = clip255((tap6(int32(t[x]), int32(t[x+1]), int32(t[x+2]), int32(t[x+3]), int32(t[x+4]), int32(t[x+5])) + 512) >> 10)
		}
	}
}

func avgIntoGeneric(dst []byte, ds int, a []byte, as int, b []byte, bs int, w, h int) {
	for y := 0; y < h; y++ {
		d := dst[y*ds : y*ds+w]
		ra := a[y*as : y*as+w]
		rb := b[y*bs : y*bs+w]
		for x := range d {
			d[x] = byte((uint16(ra[x]) + uint16(rb[x]) + 1) >> 1)
		}
	}
}

// lumaMC writes the w x h luma prediction at quarter-sample position
// (qx,qy) of ref into dst (stride ds).
func lumaMC(m *mcBuf, dst []byte, ds int, ref *picture, qx, qy, w, h int) {
	ix, iy := qx>>2, qy>>2
	fx, fy := qx&3, qy&3
	if ix < -w-3 {
		ix = -w - 3
	} else if ix > ref.width+2 {
		ix = ref.width + 2
	}
	if iy < -h-3 {
		iy = -h - 3
	} else if iy > ref.height+2 {
		iy = ref.height + 2
	}
	src := ref.planes[0]
	ss := ref.stride[0]
	so := ref.origin[0] + iy*ss + ix
	switch fx | fy<<2 {
	case 0:
		copyBlock(dst, ds, src[so:], ss, w, h)
	case 2:
		hpelH(dst, ds, src, so, ss, w, h)
	case 1, 3:
		hpelH(m.tA[:], 16, src, so, ss, w, h)
		avgInto(dst, ds, m.tA[:], 16, src[so+fx>>1:], ss, w, h)
	case 8:
		hpelV(dst, ds, src, so, ss, w, h)
	case 4, 12:
		hpelV(m.tA[:], 16, src, so, ss, w, h)
		avgInto(dst, ds, m.tA[:], 16, src[so+(fy>>1)*ss:], ss, w, h)
	case 10:
		hpelJ(dst, ds, m.tJ[:], src, so, ss, w, h)
	case 6, 14: // fx=2, fy=1/3: avg(j, b at row y or y+1)
		hpelJ(m.tA[:], 16, m.tJ[:], src, so, ss, w, h)
		hpelH(m.tB[:], 16, src, so+(fy>>1)*ss, ss, w, h)
		avgInto(dst, ds, m.tA[:], 16, m.tB[:], 16, w, h)
	case 9, 11: // fy=2, fx=1/3: avg(j, h at col x or x+1)
		hpelJ(m.tA[:], 16, m.tJ[:], src, so, ss, w, h)
		hpelV(m.tB[:], 16, src, so+(fx>>1), ss, w, h)
		avgInto(dst, ds, m.tA[:], 16, m.tB[:], 16, w, h)
	default: // diagonal quarter positions
		hpelH(m.tA[:], 16, src, so+(fy>>1)*ss, ss, w, h)
		hpelV(m.tB[:], 16, src, so+(fx>>1), ss, w, h)
		avgInto(dst, ds, m.tA[:], 16, m.tB[:], 16, w, h)
	}
}

// chromaMC writes the w x h chroma prediction at eighth-sample position
// (cx,cy) of plane c of ref into dst (stride ds).
func chromaMCGeneric(dst []byte, ds int, ref *picture, c int, cx, cy, w, h int) {
	ix, iy := cx>>3, cy>>3
	fx, fy := int32(cx&7), int32(cy&7)
	cw, ch := ref.width/2, ref.height/2
	if ix < -w-1 {
		ix = -w - 1
	} else if ix > cw {
		ix = cw
	}
	if iy < -h-1 {
		iy = -h - 1
	} else if iy > ch {
		iy = ch
	}
	src := ref.planes[c]
	ss := ref.stride[c]
	so := ref.origin[c] + iy*ss + ix
	wa := (8 - fx) * (8 - fy)
	wb := fx * (8 - fy)
	wc := (8 - fx) * fy
	wd := fx * fy
	for y := 0; y < h; y++ {
		r0 := src[so+y*ss : so+y*ss+w+1]
		r1 := src[so+(y+1)*ss : so+(y+1)*ss+w+1]
		d := dst[y*ds : y*ds+w]
		for x := range d {
			d[x] = byte((wa*int32(r0[x]) + wb*int32(r0[x+1]) + wc*int32(r1[x]) + wd*int32(r1[x+1]) + 32) >> 6)
		}
	}
}

// Weighted prediction modes.
const (
	wpDefault = iota
	wpExplicit
	wpImplicit
)

// mcPartition performs inter prediction of the partition at (x,y) with
// size (w,h) in 4x4 units, using the motion stored in the picture grid.
func (s *sliceDec) mcPartition(x, y, w, h int) {
	idx := s.idx4(x, y)
	pic := s.pic
	var r [2]int8
	r[0], r[1] = pic.refs[0][idx], pic.refs[1][idx]
	m := &s.mc
	pw, ph := w*4, h*4
	px := s.mbX*16 + x*4
	py := s.mbY*16 + y*4
	yo := pic.origin[0] + py*pic.stride[0] + px
	co := pic.origin[1] + (py/2)*pic.stride[1] + px/2
	// the common case: list 0 only, unweighted
	if r[1] < 0 && r[0] >= 0 && int(r[0]) < len(s.refList[0]) &&
		(s.wpMode != wpExplicit || s.h.pw.identity[0][r[0]]) {
		if ref := s.refList[0][r[0]]; ref != nil {
			v := pic.mvs[0][idx]
			bot := py + int(v.y>>2) + ph + 3
			if cb := 2*(py/2+int(v.y>>3)+ph/2+1) + 1; cb > bot {
				bot = cb
			}
			if bot < 0 {
				bot = 0
			}
			ref.waitRows(bot/16 + 1)
			lumaMC(m, pic.planes[0][yo:], pic.stride[0], ref, px*4+int(v.x), py*4+int(v.y), pw, ph)
			chromaMC2(pic.planes[1][co:], pic.planes[2][co:], pic.stride[1], ref, (px/2)*8+int(v.x), (py/2)*8+int(v.y), pw/2, ph/2)
			return
		}
	}
	n := 0
	var lists [2]int
	var refs [2]*picture
	for l := 0; l < 2; l++ {
		if r[l] < 0 {
			continue
		}
		if int(r[l]) >= len(s.refList[l]) {
			r[l] = int8(len(s.refList[l]) - 1)
		}
		ref := s.refList[l][r[l]]
		if ref == nil {
			continue
		}
		v := pic.mvs[l][idx]
		// wait until the reference rows used by the interpolation are final
		bot := py + int(v.y>>2) + ph + 3
		if cb := 2*(py/2+int(v.y>>3)+ph/2+1) + 1; cb > bot {
			bot = cb
		}
		if bot < 0 {
			bot = 0
		}
		ref.waitRows(bot/16 + 1)
		refs[n] = ref
		lists[n] = l
		n++
	}
	if n == 0 {
		return
	}
	mode := s.wpMode
	if mode == wpExplicit {
		// default weights give the unweighted prediction
		switch n {
		case 1:
			if s.h.pw.identity[lists[0]][r[lists[0]]] {
				mode = wpDefault
			}
		case 2:
			if s.h.pw.identity[0][r[0]] && s.h.pw.identity[1][r[1]] {
				mode = wpDefault
			}
		}
	}
	if n == 1 && mode != wpExplicit {
		// single list without weighting: predict into the picture
		l := lists[0]
		ref := refs[0]
		v := pic.mvs[l][idx]
		lumaMC(m, pic.planes[0][yo:], pic.stride[0], ref, px*4+int(v.x), py*4+int(v.y), pw, ph)
		cx := (px/2)*8 + int(v.x)
		cy := (py/2)*8 + int(v.y)
		chromaMC2(pic.planes[1][co:], pic.planes[2][co:], pic.stride[1], ref, cx, cy, pw/2, ph/2)
		return
	}
	if n == 1 {
		if l := lists[0]; weightedFullPel(pic, refs[0], pic.mvs[l][idx], px, py, pw, ph, &s.h.pw, l, int(r[l]), yo, co) {
			return
		}
	}
	for i := 0; i < n; i++ {
		l := lists[i]
		ref := refs[i]
		v := pic.mvs[l][idx]
		lumaMC(m, m.predY[l][:], 16, ref, px*4+int(v.x), py*4+int(v.y), pw, ph)
		cx := (px/2)*8 + int(v.x)
		cy := (py/2)*8 + int(v.y)
		chromaMC2(m.predC[l][0][:], m.predC[l][1][:], 8, ref, cx, cy, pw/2, ph/2)
	}
	switch {
	case n == 2 && mode == wpDefault:
		storeAvg(pic.planes[0][yo:], pic.stride[0], m.predY[0][:], m.predY[1][:], 16, pw, ph)
		storeAvg(pic.planes[1][co:], pic.stride[1], m.predC[0][0][:], m.predC[1][0][:], 8, pw/2, ph/2)
		storeAvg(pic.planes[2][co:], pic.stride[2], m.predC[0][1][:], m.predC[1][1][:], 8, pw/2, ph/2)
	case n == 1: // explicit, single list
		l := lists[0]
		pw8 := &s.h.pw
		for c := 0; c < 3; c++ {
			logWD := pw8.lumaLog2
			if c > 0 {
				logWD = pw8.chromaLog2
			}
			wt := int32(pw8.weight[l][r[l]][c])
			of := int32(pw8.offset[l][r[l]][c])
			if c == 0 {
				storeWeighted1(pic.planes[0][yo:], pic.stride[0], m.predY[l][:], 16, pw, ph, wt, of, uint(logWD))
			} else {
				storeWeighted1(pic.planes[c][co:], pic.stride[c], m.predC[l][c-1][:], 8, pw/2, ph/2, wt, of, uint(logWD))
			}
		}
	default: // bi-pred, explicit or implicit
		for c := 0; c < 3; c++ {
			var w0, w1, o0, o1 int32
			var logWD uint
			if mode == wpExplicit {
				pw8 := &s.h.pw
				logWD = uint(pw8.lumaLog2)
				if c > 0 {
					logWD = uint(pw8.chromaLog2)
				}
				w0 = int32(pw8.weight[0][r[0]][c])
				w1 = int32(pw8.weight[1][r[1]][c])
				o0 = int32(pw8.offset[0][r[0]][c])
				o1 = int32(pw8.offset[1][r[1]][c])
			} else {
				logWD = 5
				w0 = int32(s.implicitW[r[0]][r[1]][0])
				w1 = int32(s.implicitW[r[0]][r[1]][1])
			}
			if c == 0 {
				storeWeighted2(pic.planes[0][yo:], pic.stride[0], m.predY[0][:], m.predY[1][:], 16, pw, ph, w0, w1, o0, o1, logWD)
			} else {
				storeWeighted2(pic.planes[c][co:], pic.stride[c], m.predC[0][c-1][:], m.predC[1][c-1][:], 8, pw/2, ph/2, w0, w1, o0, o1, logWD)
			}
		}
	}
}

func storeAvgGeneric(dst []byte, ds int, a, b []byte, as, w, h int) {
	for y := 0; y < h; y++ {
		d := dst[y*ds : y*ds+w]
		ra := a[y*as : y*as+w]
		rb := b[y*as : y*as+w]
		for x := range d {
			d[x] = byte((uint16(ra[x]) + uint16(rb[x]) + 1) >> 1)
		}
	}
}

func storeWeighted1Generic(dst []byte, ds int, a []byte, as, w, h int, wt, of int32, logWD uint) {
	for y := 0; y < h; y++ {
		d := dst[y*ds : y*ds+w]
		ra := a[y*as : y*as+w]
		for x := range d {
			var v int32
			if logWD >= 1 {
				v = ((int32(ra[x])*wt + 1<<(logWD-1)) >> logWD) + of
			} else {
				v = int32(ra[x])*wt + of
			}
			d[x] = clip255(v)
		}
	}
}

func storeWeighted2Generic(dst []byte, ds int, a, b []byte, as, w, h int, w0, w1, o0, o1 int32, logWD uint) {
	o := (o0 + o1 + 1) >> 1
	rnd := int32(1) << logWD
	for y := 0; y < h; y++ {
		d := dst[y*ds : y*ds+w]
		ra := a[y*as : y*as+w]
		rb := b[y*as : y*as+w]
		for x := range d {
			d[x] = clip255(((int32(ra[x])*w0 + int32(rb[x])*w1 + rnd) >> (logWD + 1)) + o)
		}
	}
}

// copyBlock copies a w x h block (w = 4, 8 or 16) with word-sized moves;
// runtime.memmove has too much per-call overhead for 16-byte rows.
func copyBlockGeneric(dst []byte, ds int, src []byte, ss, w, h int) {
	switch w {
	case 16:
		for y := 0; y < h; y++ {
			s := src[y*ss : y*ss+16]
			d := dst[y*ds : y*ds+16]
			binary.LittleEndian.PutUint64(d, binary.LittleEndian.Uint64(s))
			binary.LittleEndian.PutUint64(d[8:], binary.LittleEndian.Uint64(s[8:]))
		}
	case 8:
		for y := 0; y < h; y++ {
			binary.LittleEndian.PutUint64(dst[y*ds:y*ds+8], binary.LittleEndian.Uint64(src[y*ss:y*ss+8]))
		}
	default:
		for y := 0; y < h; y++ {
			copy(dst[y*ds:y*ds+w], src[y*ss:y*ss+w])
		}
	}
}

// weightedFullPel applies single-list explicit weighting directly from
// the reference when luma and chroma positions are both full-sample, so
// the block is read once. It reports whether it handled the block.
func weightedFullPel(pic, ref *picture, v mv, px, py, pw, ph int, pw8 *predWeight, l, ri, yo, co int) bool {
	if v.x&7 != 0 || v.y&7 != 0 || pw < 8 {
		return false
	}
	ix, iy := px+int(v.x>>2), py+int(v.y>>2)
	if ix < -pw-3 || ix > ref.width+2 || iy < -ph-3 || iy > ref.height+2 {
		return false // the clamped paths handle far-out vectors
	}
	so := ref.origin[0] + iy*ref.stride[0] + ix
	storeWeighted1(pic.planes[0][yo:], pic.stride[0], ref.planes[0][so:], ref.stride[0], pw, ph,
		int32(pw8.weight[l][ri][0]), int32(pw8.offset[l][ri][0]), uint(pw8.lumaLog2))
	cso := ref.origin[1] + (iy/2)*ref.stride[1] + ix/2
	for c := 1; c < 3; c++ {
		storeWeighted1(pic.planes[c][co:], pic.stride[c], ref.planes[c][cso:], ref.stride[c], pw/2, ph/2,
			int32(pw8.weight[l][ri][c]), int32(pw8.offset[l][ri][c]), uint(pw8.chromaLog2))
	}
	return true
}

// chromaMC2Generic predicts both chroma components of a block.
func chromaMC2Generic(cb, cr []byte, ds int, ref *picture, cx, cy, w, h int) {
	chromaMCGeneric(cb, ds, ref, 1, cx, cy, w, h)
	chromaMCGeneric(cr, ds, ref, 2, cx, cy, w, h)
}
