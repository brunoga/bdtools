//go:build goexperiment.simd && amd64

package mvc

import "simd/archsimd"

type i16x16 = archsimd.Int16x16

// transpose4 transposes each 4x4 block of 16-bit values held in four
// row vectors (four blocks side by side).
func transpose4(r0, r1, r2, r3 i16x16) (i16x16, i16x16, i16x16, i16x16) {
	a0 := r0.InterleaveLoGrouped(r1).AsInt32x8()
	a1 := r0.InterleaveHiGrouped(r1).AsInt32x8()
	a2 := r2.InterleaveLoGrouped(r3).AsInt32x8()
	a3 := r2.InterleaveHiGrouped(r3).AsInt32x8()
	b0 := a0.InterleaveLoGrouped(a2).AsInt64x4()
	b1 := a0.InterleaveHiGrouped(a2).AsInt64x4()
	b2 := a1.InterleaveLoGrouped(a3).AsInt64x4()
	b3 := a1.InterleaveHiGrouped(a3).AsInt64x4()
	return b0.InterleaveLoGrouped(b2).AsInt16x16(), b0.InterleaveHiGrouped(b2).AsInt16x16(),
		b1.InterleaveLoGrouped(b3).AsInt16x16(), b1.InterleaveHiGrouped(b3).AsInt16x16()
}

// idct4v is the 1-D 4-point inverse transform applied lane-wise.
func idct4v(d0, d1, d2, d3 i16x16) (i16x16, i16x16, i16x16, i16x16) {
	e0 := d0.Add(d2)
	e1 := d0.Sub(d2)
	e2 := d1.ShiftAllRight(1).Sub(d3)
	e3 := d1.Add(d3.ShiftAllRight(1))
	return e0.Add(e3), e1.Add(e2), e1.Sub(e2), e0.Sub(e3)
}

// addRes16 adds (r+32)>>6 to 16 pixels and stores them.
func addRes16(dst []byte, r i16x16) {
	r = r.AddSaturated(archsimd.BroadcastInt16x16(32)).ShiftAllRight(6)
	packU8x16(ld16(dst).Add(r)).Store(dst[:16])
}

func idctRow4Simd(dst []byte, off, stride int, c []int16, mask uint16) {
	if !useAVX2 {
		idctRow4Generic(dst, off, stride, c, mask)
		return
	}
	c = c[:64]
	r0 := archsimd.LoadInt16x16(c[0:16])
	r1 := archsimd.LoadInt16x16(c[16:32])
	r2 := archsimd.LoadInt16x16(c[32:48])
	r3 := archsimd.LoadInt16x16(c[48:64])
	var z i16x16
	z.Store(c[0:16])
	z.Store(c[16:32])
	z.Store(c[32:48])
	z.Store(c[48:64])
	// coefficients are stored transposed: rows hold block columns
	r0, r1, r2, r3 = idct4v(r0, r1, r2, r3)
	r0, r1, r2, r3 = transpose4(r0, r1, r2, r3)
	r0, r1, r2, r3 = idct4v(r0, r1, r2, r3)
	addRes16(dst[off:], r0)
	addRes16(dst[off+stride:], r1)
	addRes16(dst[off+2*stride:], r2)
	addRes16(dst[off+3*stride:], r3)
	archsimd.ClearAVXUpperBits()
}

// transpose8 transposes each 8x8 block held in eight row vectors (two
// blocks side by side).
func transpose8(r *[8]i16x16) {
	a0 := r[0].InterleaveLoGrouped(r[1]).AsInt32x8()
	a1 := r[0].InterleaveHiGrouped(r[1]).AsInt32x8()
	a2 := r[2].InterleaveLoGrouped(r[3]).AsInt32x8()
	a3 := r[2].InterleaveHiGrouped(r[3]).AsInt32x8()
	a4 := r[4].InterleaveLoGrouped(r[5]).AsInt32x8()
	a5 := r[4].InterleaveHiGrouped(r[5]).AsInt32x8()
	a6 := r[6].InterleaveLoGrouped(r[7]).AsInt32x8()
	a7 := r[6].InterleaveHiGrouped(r[7]).AsInt32x8()
	b0 := a0.InterleaveLoGrouped(a2).AsInt64x4()
	b1 := a0.InterleaveHiGrouped(a2).AsInt64x4()
	b2 := a1.InterleaveLoGrouped(a3).AsInt64x4()
	b3 := a1.InterleaveHiGrouped(a3).AsInt64x4()
	b4 := a4.InterleaveLoGrouped(a6).AsInt64x4()
	b5 := a4.InterleaveHiGrouped(a6).AsInt64x4()
	b6 := a5.InterleaveLoGrouped(a7).AsInt64x4()
	b7 := a5.InterleaveHiGrouped(a7).AsInt64x4()
	r[0] = b0.InterleaveLoGrouped(b4).AsInt16x16()
	r[1] = b0.InterleaveHiGrouped(b4).AsInt16x16()
	r[2] = b1.InterleaveLoGrouped(b5).AsInt16x16()
	r[3] = b1.InterleaveHiGrouped(b5).AsInt16x16()
	r[4] = b2.InterleaveLoGrouped(b6).AsInt16x16()
	r[5] = b2.InterleaveHiGrouped(b6).AsInt16x16()
	r[6] = b3.InterleaveLoGrouped(b7).AsInt16x16()
	r[7] = b3.InterleaveHiGrouped(b7).AsInt16x16()
}

// idct8v is the 1-D 8-point inverse transform applied lane-wise.
func idct8v(d *[8]i16x16) {
	a0 := d[0].Add(d[4])
	a4 := d[0].Sub(d[4])
	a2 := d[2].ShiftAllRight(1).Sub(d[6])
	a6 := d[2].Add(d[6].ShiftAllRight(1))
	b0 := a0.Add(a6)
	b2 := a4.Add(a2)
	b4 := a4.Sub(a2)
	b6 := a0.Sub(a6)
	a1 := d[5].Sub(d[3]).Sub(d[7]).Sub(d[7].ShiftAllRight(1))
	a3 := d[1].Add(d[7]).Sub(d[3]).Sub(d[3].ShiftAllRight(1))
	a5 := d[7].Sub(d[1]).Add(d[5]).Add(d[5].ShiftAllRight(1))
	a7 := d[3].Add(d[5]).Add(d[1]).Add(d[1].ShiftAllRight(1))
	b1 := a1.Add(a7.ShiftAllRight(2))
	b7 := a7.Sub(a1.ShiftAllRight(2))
	b3 := a3.Add(a5.ShiftAllRight(2))
	b5 := a3.ShiftAllRight(2).Sub(a5)
	d[0] = b0.Add(b7)
	d[1] = b2.Add(b5)
	d[2] = b4.Add(b3)
	d[3] = b6.Add(b1)
	d[4] = b6.Sub(b1)
	d[5] = b4.Sub(b3)
	d[6] = b2.Sub(b5)
	d[7] = b0.Sub(b7)
}

func idct8x8RowSimd(dst []byte, off, stride int, c []int16, mask uint8) {
	if !useAVX2 {
		idct8x8RowGeneric(dst, off, stride, c, mask)
		return
	}
	c = c[:128]
	var r [8]i16x16
	var z i16x16
	for i := 0; i < 8; i++ {
		r[i] = archsimd.LoadInt16x16(c[i*16 : i*16+16])
		z.Store(c[i*16 : i*16+16])
	}
	idct8v(&r) // transposed layout: first pass needs no transpose
	transpose8(&r)
	idct8v(&r)
	for i := 0; i < 8; i++ {
		addRes16(dst[off+i*stride:], r[i])
	}
	archsimd.ClearAVXUpperBits()
}

func idctChromaSimd(cb, cr []byte, off, stride int, c *[2][64]int16, nz [2]uint8) {
	if !useAVX2 {
		idctChromaGeneric(cb, cr, off, stride, c, nz)
		return
	}
	for by := 0; by < 2; by++ {
		if (nz[0]|nz[1])>>(by*2)&3 == 0 {
			continue
		}
		var r [4]i16x16
		var z archsimd.Int16x8
		for i := 0; i < 4; i++ {
			o := (by*4 + i) * 8
			r[i] = r[i].SetLo(archsimd.LoadInt16x8(c[0][o : o+8])).SetHi(archsimd.LoadInt16x8(c[1][o : o+8]))
			z.Store(c[0][o : o+8])
			z.Store(c[1][o : o+8])
		}
		r0, r1, r2, r3 := idct4v(r[0], r[1], r[2], r[3])
		r0, r1, r2, r3 = transpose4(r0, r1, r2, r3)
		r0, r1, r2, r3 = idct4v(r0, r1, r2, r3)
		c32 := archsimd.BroadcastInt16x16(32)
		for i, v := range [4]i16x16{r0, r1, r2, r3} {
			o := off + (by*4+i)*stride
			v = v.AddSaturated(c32).ShiftAllRight(6)
			p := ld8x2s(cb[o:], cr[o:]).Add(v)
			storeRows8(packU8x16(p), cb[o:], cr[o:], 8)
		}
	}
	archsimd.ClearAVXUpperBits()
}

// idct8x8AddS transforms a single 8x8 block (coefficient row stride cs).
func idct8x8AddSSimd(dst []byte, off, stride int, c []int16, cs int) {
	if !useAVX2 {
		idct8x8AddSGeneric(dst, off, stride, c, cs)
		return
	}
	var r [8]i16x16
	var z archsimd.Int16x8
	for i := 0; i < 8; i++ {
		row := c[i*cs : i*cs+8]
		r[i] = r[i].SetLo(archsimd.LoadInt16x8(row))
		z.Store(row)
	}
	idct8v(&r)
	transpose8(&r)
	idct8v(&r)
	c32 := archsimd.BroadcastInt16x16(32)
	for i := 0; i < 8; i += 2 {
		v := r[i].GetLo()
		var pair i16x16
		pair = pair.SetLo(v).SetHi(r[i+1].GetLo())
		pair = pair.AddSaturated(c32).ShiftAllRight(6)
		o0 := off + i*stride
		o1 := o0 + stride
		p := ld8x2s(dst[o0:], dst[o1:]).Add(pair)
		storeRows8(packU8x16(p), dst[o0:], dst[o1:], 8)
	}
	archsimd.ClearAVXUpperBits()
}

func chromaAddDCSimd(cb, cr []byte, off, stride int, dc *[2][4]int16) {
	if !useAVX2 {
		chromaAddDCGeneric(cb, cr, off, stride, dc)
		return
	}
	for by := 0; by < 2; by++ {
		var a [16]int16
		for i := 0; i < 8; i++ {
			a[i] = dc[0][by*2+i>>2]
			a[8+i] = dc[1][by*2+i>>2]
		}
		v := archsimd.LoadInt16x16Array(&a)
		for y := by * 4; y < by*4+4; y++ {
			o := off + y*stride
			storeRows8(packU8x16(ld8x2s(cb[o:], cr[o:]).Add(v)), cb[o:], cr[o:], 8)
		}
	}
	archsimd.ClearAVXUpperBits()
}

func addConst16Simd(dst []byte, off, stride int, v *[16]int16, rows int) {
	if !useAVX2 {
		addConst16Generic(dst, off, stride, v, rows)
		return
	}
	c := archsimd.LoadInt16x16Array(v)
	for y := 0; y < rows; y++ {
		o := off + y*stride
		packU8x16(ld16(dst[o:]).Add(c)).Store(dst[o : o+16])
	}
	archsimd.ClearAVXUpperBits()
}
