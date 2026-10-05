//go:build goexperiment.simd && amd64

package mvc

import "simd/archsimd"

// lumaFilter applies the luma edge filter (8.7.2.3/8.7.2.4) lane-wise.
// bs and tc0 hold per-lane boundary strength and tC0.
func lumaFilter(r *[8]i16x16, bs, tc0 i16x16, alpha, beta int16) {
	p3, p2, p1, p0, q0, q1, q2, q3 := r[0], r[1], r[2], r[3], r[4], r[5], r[6], r[7]
	zero := archsimd.BroadcastInt16x16(0)
	one := archsimd.BroadcastInt16x16(1)
	two := archsimd.BroadcastInt16x16(2)
	four := archsimd.BroadcastInt16x16(4)
	va := archsimd.BroadcastInt16x16(alpha)
	vb := archsimd.BroadcastInt16x16(beta)
	ad := p0.Sub(q0).Abs()
	m := va.Greater(ad).And(vb.Greater(p1.Sub(p0).Abs())).And(vb.Greater(q1.Sub(q0).Abs())).And(bs.Greater(zero))
	if m.ToInt16x16().IsZero() {
		return
	}
	mp := vb.Greater(p2.Sub(p0).Abs())
	mq := vb.Greater(q2.Sub(q0).Abs())
	mpi := mp.ToInt16x16()
	mqi := mq.ToInt16x16()

	// bS < 4
	tc := tc0.Sub(mpi).Sub(mqi)
	delta := q0.Sub(p0).ShiftAllLeft(2).Add(p1.Sub(q1)).Add(four).ShiftAllRight(3)
	delta = delta.Max(tc.Neg()).Min(tc)
	np0 := p0.Add(delta)
	nq0 := q0.Sub(delta)
	avg := p0.Add(q0).Add(one).ShiftAllRight(1)
	ntc0 := tc0.Neg()
	np1 := p1.Add(p2.Add(avg).Sub(p1.ShiftAllLeft(1)).ShiftAllRight(1).Max(ntc0).Min(tc0)).IfElse(mp, p1)
	nq1 := q1.Add(q2.Add(avg).Sub(q1.ShiftAllLeft(1)).ShiftAllRight(1).Max(ntc0).Min(tc0)).IfElse(mq, q1)

	// bS == 4
	is4 := bs.Equal(four)
	if !is4.ToInt16x16().IsZero() {
		strong := archsimd.BroadcastInt16x16((alpha >> 2) + 2).Greater(ad)
		sp := mp.And(strong)
		sq := mq.And(strong)
		sum := p1.Add(p0).Add(q0) // p1+p0+q0
		sp0 := p2.Add(sum.ShiftAllLeft(1)).Add(q1).Add(four).ShiftAllRight(3)
		wp0 := p1.ShiftAllLeft(1).Add(p0).Add(q1).Add(two).ShiftAllRight(2)
		sp1 := p2.Add(sum).Add(two).ShiftAllRight(2)
		sp2 := p3.ShiftAllLeft(1).Add(p2.Add(p2.ShiftAllLeft(1))).Add(sum).Add(four).ShiftAllRight(3)
		sumq := q1.Add(q0).Add(p0)
		sq0 := q2.Add(sumq.ShiftAllLeft(1)).Add(p1).Add(four).ShiftAllRight(3)
		wq0 := q1.ShiftAllLeft(1).Add(q0).Add(p1).Add(two).ShiftAllRight(2)
		sq1 := q2.Add(sumq).Add(two).ShiftAllRight(2)
		sq2 := q3.ShiftAllLeft(1).Add(q2.Add(q2.ShiftAllLeft(1))).Add(sumq).Add(four).ShiftAllRight(3)
		np0 = sp0.IfElse(sp, wp0).IfElse(is4, np0)
		np1 = sp1.IfElse(sp, p1).IfElse(is4, np1)
		r[1] = sp2.IfElse(sp, p2).IfElse(is4, p2).IfElse(m, p2)
		nq0 = sq0.IfElse(sq, wq0).IfElse(is4, nq0)
		nq1 = sq1.IfElse(sq, q1).IfElse(is4, nq1)
		r[6] = sq2.IfElse(sq, q2).IfElse(is4, q2).IfElse(m, q2)
	}
	r[2] = np1.IfElse(m, p1)
	r[3] = np0.IfElse(m, p0)
	r[4] = nq0.IfElse(m, q0)
	r[5] = nq1.IfElse(m, q1)
}

// segLanes expands four per-segment values to 16 lanes (4 lanes each).
func segLanes(v [4]int16) i16x16 {
	var a [16]int16
	for i := 0; i < 16; i++ {
		a[i] = v[i>>2]
	}
	return archsimd.LoadInt16x16Array(&a)
}

func filterLumaSimd(pl []byte, off, step, along int, bs *[4]uint8, alpha, beta int32, indexA int) {
	if !useAVX2 {
		filterLumaGeneric(pl, off, step, along, bs, alpha, beta, indexA)
		return
	}
	if alpha == 0 || beta == 0 {
		return
	}
	var bsv, tcv [4]int16
	for k := 0; k < 4; k++ {
		bsv[k] = int16(bs[k])
		if b := bs[k]; b > 0 && b < 4 {
			tcv[k] = int16(tc0Tab[indexA][b-1])
		}
	}
	vbs := segLanes(bsv)
	vtc := segLanes(tcv)
	var r [8]i16x16
	if step == 1 {
		// vertical edge: transpose 16 rows of p3..q3
		b := off - 4
		for i := 0; i < 8; i++ {
			r[i] = ld8x2(pl[b+i*along:], pl[b+(i+8)*along:])
		}
		transpose8(&r)
		lumaFilter(&r, vbs, vtc, int16(alpha), int16(beta))
		transpose8(&r)
		for i := 0; i < 8; i++ {
			storeRows8(packU8x16(r[i]), pl[b+i*along:], pl[b+(i+8)*along:], 8)
		}
		archsimd.ClearAVXUpperBits()
		return
	}
	for i := 0; i < 8; i++ {
		r[i] = ld16(pl[off+(i-4)*step:])
	}
	lumaFilter(&r, vbs, vtc, int16(alpha), int16(beta))
	for i := 1; i < 7; i++ {
		o := off + (i-4)*step
		packU8x16(r[i]).Store(pl[o : o+16])
	}
	archsimd.ClearAVXUpperBits()
}

// chromaFilter applies the chroma edge filter to p0/q0 lane-wise.
func chromaFilter(p1, p0, q0, q1, bs, tc0, va, vb i16x16) (i16x16, i16x16) {
	zero := archsimd.BroadcastInt16x16(0)
	one := archsimd.BroadcastInt16x16(1)
	two := archsimd.BroadcastInt16x16(2)
	four := archsimd.BroadcastInt16x16(4)
	m := va.Greater(p0.Sub(q0).Abs()).And(vb.Greater(p1.Sub(p0).Abs())).And(vb.Greater(q1.Sub(q0).Abs())).And(bs.Greater(zero))
	tc := tc0.Add(one)
	delta := q0.Sub(p0).ShiftAllLeft(2).Add(p1.Sub(q1)).Add(four).ShiftAllRight(3)
	delta = delta.Max(tc.Neg()).Min(tc)
	np0 := p0.Add(delta)
	nq0 := q0.Sub(delta)
	is4 := bs.Equal(four)
	sp0 := p1.ShiftAllLeft(1).Add(p0).Add(q1).Add(two).ShiftAllRight(2)
	sq0 := q1.ShiftAllLeft(1).Add(q0).Add(p1).Add(two).ShiftAllRight(2)
	np0 = sp0.IfElse(is4, np0).IfElse(m, p0)
	nq0 = sq0.IfElse(is4, nq0).IfElse(m, q0)
	return np0, nq0
}

func filterChroma2Simd(cb, cr []byte, off, step, along int, bs *[4]uint8, alpha, beta [2]int32, indexA [2]int) {
	if !useAVX2 {
		filterChroma2Generic(cb, cr, off, step, along, bs, alpha, beta, indexA)
		return
	}
	// lanes: 0..7 Cb samples 0..7 along the edge, 8..15 Cr; sample i uses
	// boundary strength bs[i>>1]
	var abs, atc [16]int16
	for i := 0; i < 16; i++ {
		b := bs[(i&7)>>1]
		abs[i] = int16(b)
		if b > 0 && b < 4 {
			atc[i] = int16(tc0Tab[indexA[i>>3]][b-1])
		}
	}
	vbs := archsimd.LoadInt16x16Array(&abs)
	vtc := archsimd.LoadInt16x16Array(&atc)
	var z i16x16
	va := z.SetLo(archsimd.BroadcastInt16x8(int16(alpha[0]))).SetHi(archsimd.BroadcastInt16x8(int16(alpha[1])))
	vb := z.SetLo(archsimd.BroadcastInt16x8(int16(beta[0]))).SetHi(archsimd.BroadcastInt16x8(int16(beta[1])))
	if step == 1 {
		b := off - 4
		var r [8]i16x16
		for i := 0; i < 8; i++ {
			r[i] = ld8x2(cb[b+i*along:], cr[b+i*along:])
		}
		transpose8(&r)
		r[3], r[4] = chromaFilter(r[2], r[3], r[4], r[5], vbs, vtc, va, vb)
		transpose8(&r)
		for i := 0; i < 8; i++ {
			storeRows8(packU8x16(r[i]), cb[b+i*along:], cr[b+i*along:], 8)
		}
		archsimd.ClearAVXUpperBits()
		return
	}
	p1 := ld8x2s(cb[off-2*step:], cr[off-2*step:])
	p0 := ld8x2s(cb[off-step:], cr[off-step:])
	q0 := ld8x2s(cb[off:], cr[off:])
	q1 := ld8x2s(cb[off+step:], cr[off+step:])
	np0, nq0 := chromaFilter(p1, p0, q0, q1, vbs, vtc, va, vb)
	storeRows8(packU8x16(np0), cb[off-step:], cr[off-step:], 8)
	storeRows8(packU8x16(nq0), cb[off:], cr[off:], 8)
	archsimd.ClearAVXUpperBits()
}
