//go:build goexperiment.simd && amd64

package mvc

import (
	"encoding/binary"
	"simd/archsimd"
)

var useAVX2 = archsimd.X86.AVX2()

// byte gather tables for packing 16-bit lanes (values 0..255) to bytes
var packLo = [16]int8{0, 2, 4, 6, 8, 10, 12, 14, -1, -1, -1, -1, -1, -1, -1, -1}
var packHi = [16]int8{-1, -1, -1, -1, -1, -1, -1, -1, 0, 2, 4, 6, 8, 10, 12, 14}

// packU8x16 clamps 16 int16 lanes to 0..255 and packs them to bytes.
func packU8x16(v archsimd.Int16x16) archsimd.Uint8x16 {
	v = v.Max(archsimd.BroadcastInt16x16(0)).Min(archsimd.BroadcastInt16x16(255))
	lo := v.GetLo().AsUint8x16().PermuteOrZero(archsimd.LoadInt8x16Array(&packLo))
	hi := v.GetHi().AsUint8x16().PermuteOrZero(archsimd.LoadInt8x16Array(&packHi))
	return lo.Or(hi)
}

// store16 writes 16 packed bytes as two 8-byte rows (dst, dst2).
func storeRows8(v archsimd.Uint8x16, dst0, dst1 []byte, w int) {
	q := v.AsUint64x2()
	a, b := q.GetElem(0), q.GetElem(1)
	if w == 8 {
		binary.LittleEndian.PutUint64(dst0[:8], a)
		binary.LittleEndian.PutUint64(dst1[:8], b)
	} else {
		binary.LittleEndian.PutUint32(dst0[:4], uint32(a))
		binary.LittleEndian.PutUint32(dst1[:4], uint32(b))
	}
}

// sra16 shifts right arithmetically by s (0..7). Constant shift counts are
// used because a variable count makes the compiler emit a legacy-SSE MOVQ
// next to AVX code (an AVX/SSE transition penalty on Intel CPUs).
func sra16(v archsimd.Int16x16, s uint) archsimd.Int16x16 {
	switch s {
	case 0:
		return v
	case 1:
		return v.ShiftAllRight(1)
	case 2:
		return v.ShiftAllRight(2)
	case 3:
		return v.ShiftAllRight(3)
	case 4:
		return v.ShiftAllRight(4)
	case 5:
		return v.ShiftAllRight(5)
	case 6:
		return v.ShiftAllRight(6)
	default:
		return v.ShiftAllRight(7)
	}
}

// sra32 shifts right arithmetically by s (1..8), see sra16.
func sra32(v archsimd.Int32x8, s uint) archsimd.Int32x8 {
	switch s {
	case 1:
		return v.ShiftAllRight(1)
	case 2:
		return v.ShiftAllRight(2)
	case 3:
		return v.ShiftAllRight(3)
	case 4:
		return v.ShiftAllRight(4)
	case 5:
		return v.ShiftAllRight(5)
	case 6:
		return v.ShiftAllRight(6)
	case 7:
		return v.ShiftAllRight(7)
	default:
		return v.ShiftAllRight(8)
	}
}

func ld16(s []byte) archsimd.Int16x16 {
	return archsimd.LoadUint8x16(s[:16]).ExtendToUint16().AsInt16x16()
}

func ld8(s []byte) archsimd.Int16x8 {
	return archsimd.LoadUint8x16(s[:16]).ExtendLo8ToUint16().AsInt16x8()
}

// ld8x2 loads 8 bytes from each of two rows into the halves of an
// Int16x16. It may read up to 16 bytes from each row.
func ld8x2(a, b []byte) archsimd.Int16x16 {
	var z archsimd.Int16x16
	return z.SetLo(ld8(a)).SetHi(ld8(b))
}

// ld8s loads exactly 8 bytes.
func ld8s(s []byte) archsimd.Int16x8 {
	return archsimd.BroadcastUint64x2(binary.LittleEndian.Uint64(s[:8])).AsUint8x16().ExtendLo8ToUint16().AsInt16x8()
}

// ld8x2s is ld8x2 reading exactly 8 bytes per row.
func ld8x2s(a, b []byte) archsimd.Int16x16 {
	var z archsimd.Int16x16
	return z.SetLo(ld8s(a)).SetHi(ld8s(b))
}

// tap6 computes a + f - 5(b+e) + 20(c+d).
func tap6v(a, b, c, d, e, f archsimd.Int16x16) archsimd.Int16x16 {
	c5 := archsimd.BroadcastInt16x16(5)
	c20 := archsimd.BroadcastInt16x16(20)
	return a.Add(f).Sub(b.Add(e).Mul(c5)).Add(c.Add(d).Mul(c20))
}

func hpelHSimd(out []byte, ds int, src []byte, so, ss, w, h int) {
	if !useAVX2 || w < 4 {
		hpelHGeneric(out, ds, src, so, ss, w, h)
		return
	}
	c16 := archsimd.BroadcastInt16x16(16)
	if w == 16 {
		for y := 0; y < h; y++ {
			r := src[so+y*ss-2:]
			v := tap6v(ld16(r), ld16(r[1:]), ld16(r[2:]), ld16(r[3:]), ld16(r[4:]), ld16(r[5:]))
			packU8x16(v.Add(c16).ShiftAllRight(5)).Store(out[y*ds : y*ds+16])
		}
		archsimd.ClearAVXUpperBits()
		return
	}
	for y := 0; y < h; y += 2 {
		r0 := src[so+y*ss-2:]
		r1 := src[so+(y+1)*ss-2:]
		v := tap6v(ld8x2(r0, r1), ld8x2(r0[1:], r1[1:]), ld8x2(r0[2:], r1[2:]),
			ld8x2(r0[3:], r1[3:]), ld8x2(r0[4:], r1[4:]), ld8x2(r0[5:], r1[5:]))
		storeRows8(packU8x16(v.Add(c16).ShiftAllRight(5)), out[y*ds:], out[(y+1)*ds:], w)
	}
	archsimd.ClearAVXUpperBits()
}

func hpelVSimd(out []byte, ds int, src []byte, so, ss, w, h int) {
	if !useAVX2 || w < 4 {
		hpelVGeneric(out, ds, src, so, ss, w, h)
		return
	}
	c16 := archsimd.BroadcastInt16x16(16)
	if w == 16 {
		b := so - 2*ss
		r0, r1, r2, r3, r4 := ld16(src[b:]), ld16(src[b+ss:]), ld16(src[b+2*ss:]), ld16(src[b+3*ss:]), ld16(src[b+4*ss:])
		for y := 0; y < h; y++ {
			r5 := ld16(src[b+(y+5)*ss:])
			v := tap6v(r0, r1, r2, r3, r4, r5)
			packU8x16(v.Add(c16).ShiftAllRight(5)).Store(out[y*ds : y*ds+16])
			r0, r1, r2, r3, r4 = r1, r2, r3, r4, r5
		}
		archsimd.ClearAVXUpperBits()
		return
	}
	b := so - 2*ss
	// rows paired: low half row y, high half row y+1
	l := func(i int) archsimd.Int16x16 { return ld8x2(src[b+i*ss:], src[b+(i+1)*ss:]) }
	for y := 0; y < h; y += 2 {
		v := tap6v(l(y), l(y+1), l(y+2), l(y+3), l(y+4), l(y+5))
		storeRows8(packU8x16(v.Add(c16).ShiftAllRight(5)), out[y*ds:], out[(y+1)*ds:], w)
	}
	archsimd.ClearAVXUpperBits()
}

// tapJ computes the centre sample j from six vectors of vertical
// intermediates t0..t5 using 32-bit arithmetic.
func tapJ(t0, t1, t2, t3, t4, t5 archsimd.Int16x16) archsimd.Int16x16 {
	u := t0.Add(t5)
	v := t1.Add(t4)
	w := t2.Add(t3)
	r := archsimd.BroadcastInt16x16(512)
	cuv := archsimd.BroadcastInt32x8(1 | -5<<16).AsInt16x16()
	cw := archsimd.BroadcastInt32x8(20 | 1<<16).AsInt16x16()
	lo := u.InterleaveLoGrouped(v).DotProductPairs(cuv).Add(w.InterleaveLoGrouped(r).DotProductPairs(cw))
	hi := u.InterleaveHiGrouped(v).DotProductPairs(cuv).Add(w.InterleaveHiGrouped(r).DotProductPairs(cw))
	return lo.ShiftAllRight(10).SaturateToInt16ConcatGrouped(hi.ShiftAllRight(10))
}

func hpelJSimd(out []byte, ds int, tmp []int16, src []byte, so, ss, w, h int) {
	if !useAVX2 || w < 4 {
		hpelJGeneric(out, ds, tmp, src, so, ss, w, h)
		return
	}
	// vertical intermediates for columns x-2 .. x-2+cols, rows 0..h-1,
	// stored with stride 24
	const ts = 24
	var t [16 * ts]int16
	cols := 16
	if w == 16 {
		cols = 24
	}
	for c := 0; c < cols; c += 16 {
		b := so - 2*ss - 2 + c
		r0, r1, r2, r3, r4 := ld16(src[b:]), ld16(src[b+ss:]), ld16(src[b+2*ss:]), ld16(src[b+3*ss:]), ld16(src[b+4*ss:])
		for y := 0; y < h; y++ {
			r5 := ld16(src[b+(y+5)*ss:])
			v := tap6v(r0, r1, r2, r3, r4, r5)
			if c == 0 {
				v.Store(t[y*ts : y*ts+16])
			} else {
				v.GetLo().Store(t[y*ts+16 : y*ts+24])
			}
			r0, r1, r2, r3, r4 = r1, r2, r3, r4, r5
		}
	}
	if w == 16 {
		for y := 0; y < h; y++ {
			r := t[y*ts:]
			l := func(k int) archsimd.Int16x16 { return archsimd.LoadInt16x16(r[k : k+16]) }
			packU8x16(tapJ(l(0), l(1), l(2), l(3), l(4), l(5))).Store(out[y*ds : y*ds+16])
		}
		archsimd.ClearAVXUpperBits()
		return
	}
	for y := 0; y < h; y += 2 {
		r0 := t[y*ts:]
		r1 := t[(y+1)*ts:]
		l := func(k int) archsimd.Int16x16 {
			var z archsimd.Int16x16
			return z.SetLo(archsimd.LoadInt16x8(r0[k : k+8])).SetHi(archsimd.LoadInt16x8(r1[k : k+8]))
		}
		storeRows8(packU8x16(tapJ(l(0), l(1), l(2), l(3), l(4), l(5))), out[y*ds:], out[(y+1)*ds:], w)
	}
	archsimd.ClearAVXUpperBits()
}

func avgIntoSimd(dst []byte, ds int, a []byte, as int, b []byte, bs int, w, h int) {
	if !useAVX2 || w < 8 {
		avgIntoGeneric(dst, ds, a, as, b, bs, w, h)
		return
	}
	if w == 16 {
		for y := 0; y < h; y++ {
			va := archsimd.LoadUint8x16(a[y*as : y*as+16])
			vb := archsimd.LoadUint8x16(b[y*bs : y*bs+16])
			va.Average(vb).Store(dst[y*ds : y*ds+16])
		}
		archsimd.ClearAVXUpperBits()
		return
	}
	for y := 0; y < h; y++ {
		x := binary.LittleEndian.Uint64(a[y*as:]) // 8 bytes
		z := binary.LittleEndian.Uint64(b[y*bs:])
		// average with rounding: (x|z) - ((x^z)>>1 & 0x7f..)
		r := (x | z) - ((x ^ z) >> 1 & 0x7f7f7f7f7f7f7f7f)
		binary.LittleEndian.PutUint64(dst[y*ds:], r)
	}
	archsimd.ClearAVXUpperBits()
}

func storeAvgSimd(dst []byte, ds int, a, b []byte, as, w, h int) {
	if !useAVX2 || w < 8 {
		storeAvgGeneric(dst, ds, a, b, as, w, h)
		return
	}
	if w == 16 {
		for y := 0; y < h; y++ {
			va := archsimd.LoadUint8x16(a[y*as : y*as+16])
			vb := archsimd.LoadUint8x16(b[y*as : y*as+16])
			va.Average(vb).Store(dst[y*ds : y*ds+16])
		}
		archsimd.ClearAVXUpperBits()
		return
	}
	for y := 0; y < h; y++ {
		x := binary.LittleEndian.Uint64(a[y*as:])
		z := binary.LittleEndian.Uint64(b[y*as:])
		r := (x | z) - ((x ^ z) >> 1 & 0x7f7f7f7f7f7f7f7f)
		binary.LittleEndian.PutUint64(dst[y*ds:], r)
	}
	archsimd.ClearAVXUpperBits()
}

func storeWeighted1Simd(dst []byte, ds int, a []byte, as, w, h int, wt, of int32, logWD uint) {
	if !useAVX2 || w < 8 {
		storeWeighted1Generic(dst, ds, a, as, w, h, wt, of, logWD)
		return
	}
	vw := archsimd.BroadcastInt16x16(int16(wt))
	var rnd int16
	if logWD >= 1 {
		rnd = 1 << (logWD - 1)
	}
	vr := archsimd.BroadcastInt16x16(rnd)
	vo := archsimd.BroadcastInt16x16(int16(of))
	if w == 16 {
		for y := 0; y < h; y++ {
			v := sra16(ld16(a[y*as:]).Mul(vw).Add(vr), logWD).Add(vo)
			packU8x16(v).Store(dst[y*ds : y*ds+16])
		}
		archsimd.ClearAVXUpperBits()
		return
	}
	for y := 0; y < h; y += 2 {
		v := sra16(ld8x2s(a[y*as:], a[(y+1)*as:]).Mul(vw).Add(vr), logWD).Add(vo)
		storeRows8(packU8x16(v), dst[y*ds:], dst[(y+1)*ds:], 8)
	}
	archsimd.ClearAVXUpperBits()
}

func storeWeighted2Simd(dst []byte, ds int, a, b []byte, as, w, h int, w0, w1, o0, o1 int32, logWD uint) {
	if !useAVX2 || w < 8 {
		storeWeighted2Generic(dst, ds, a, b, as, w, h, w0, w1, o0, o1, logWD)
		return
	}
	cw := archsimd.BroadcastInt32x8(int32(uint16(w0)) | w1<<16).AsInt16x16()
	vr := archsimd.BroadcastInt32x8(1 << logWD)
	vo := archsimd.BroadcastInt16x16(int16((o0 + o1 + 1) >> 1))
	sh := logWD + 1
	if w == 16 {
		for y := 0; y < h; y++ {
			pa, pb := ld16(a[y*as:]), ld16(b[y*as:])
			lo := sra32(pa.InterleaveLoGrouped(pb).DotProductPairs(cw).Add(vr), sh)
			hi := sra32(pa.InterleaveHiGrouped(pb).DotProductPairs(cw).Add(vr), sh)
			packU8x16(lo.SaturateToInt16ConcatGrouped(hi).Add(vo)).Store(dst[y*ds : y*ds+16])
		}
		archsimd.ClearAVXUpperBits()
		return
	}
	for y := 0; y < h; y += 2 {
		pa, pb := ld8x2s(a[y*as:], a[(y+1)*as:]), ld8x2s(b[y*as:], b[(y+1)*as:])
		lo := sra32(pa.InterleaveLoGrouped(pb).DotProductPairs(cw).Add(vr), sh)
		hi := sra32(pa.InterleaveHiGrouped(pb).DotProductPairs(cw).Add(vr), sh)
		storeRows8(packU8x16(lo.SaturateToInt16ConcatGrouped(hi).Add(vo)), dst[y*ds:], dst[(y+1)*ds:], 8)
	}
	archsimd.ClearAVXUpperBits()
}

func chromaMCSimd(dst []byte, ds int, ref *picture, c int, cx, cy, w, h int) {
	if !useAVX2 || w < 4 {
		chromaMCGeneric(dst, ds, ref, c, cx, cy, w, h)
		return
	}
	ix, iy := cx>>3, cy>>3
	fx, fy := int16(cx&7), int16(cy&7)
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
	wa := archsimd.BroadcastInt16x16((8 - fx) * (8 - fy))
	wb := archsimd.BroadcastInt16x16(fx * (8 - fy))
	wc := archsimd.BroadcastInt16x16((8 - fx) * fy)
	wd := archsimd.BroadcastInt16x16(fx * fy)
	c32 := archsimd.BroadcastInt16x16(32)
	for y := 0; y < h; y += 2 {
		r0 := src[so+y*ss:]
		r1 := src[so+(y+1)*ss:]
		r2 := src[so+(y+2)*ss:]
		v := ld8x2(r0, r1).Mul(wa).Add(ld8x2(r0[1:], r1[1:]).Mul(wb)).
			Add(ld8x2(r1, r2).Mul(wc)).Add(ld8x2(r1[1:], r2[1:]).Mul(wd))
		storeRows8(packU8x16(v.Add(c32).ShiftAllRight(6)), dst[y*ds:], dst[(y+1)*ds:], w)
	}
	archsimd.ClearAVXUpperBits()
}
