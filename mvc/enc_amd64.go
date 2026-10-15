//go:build amd64 && !purego

package mvc

//go:noescape
func sadAsm(src *byte, ss int, ref *byte, rs int, w, h int) int

//go:noescape
func satdAsm(src *byte, ss int, pred *byte, ps int, w, h int) int

//go:noescape
func satdAvgAsm(src *byte, ss int, a *byte, as int, b *byte, bs int, w, h int) int

// satdAvgBlock is satdBlock against the rounded average of the blocks at
// a[ao] and b[bo].
func satdAvgBlock(src []byte, so, ss int, a []byte, ao, as int, b []byte, bo, bs, w, h int) int {
	if !useAVX2Asm || w != 8 && w != 16 || h&3 != 0 {
		return satdAvgGeneric(src, so, ss, a, ao, as, b, bo, bs, w, h)
	}
	return satdAvgAsm(&src[so], ss, &a[ao], as, &b[bo], bs, w, h) / 2
}

// sadBlock is the sum of absolute differences between the w x h blocks
// at src[so] and ref[ro].
func sadBlock(src []byte, so, ss int, ref []byte, ro, rs, w, h int) int {
	if !useAVX2Asm || w != 8 && w != 16 {
		return sadGeneric(src, so, ss, ref, ro, rs, w, h)
	}
	return sadAsm(&src[so], ss, &ref[ro], rs, w, h)
}

// satdBlock is the sum of 4x4 Hadamard transformed differences between
// the w x h blocks at src[so] and pred[po], halved.
func satdBlock(src []byte, so, ss int, pred []byte, po, ps, w, h int) int {
	if !useAVX2Asm || w != 8 && w != 16 || h&3 != 0 {
		return satdGeneric(src, so, ss, pred, po, ps, w, h)
	}
	return satdAsm(&src[so], ss, &pred[po], ps, w, h) / 2
}

//go:noescape
func fdctQuantLumaAsm(src *byte, ss int, pred *byte, mfT *int32, qbits, f int, out *int32) uint64

// fdctQuantLuma is fdctQuantLumaGeneric, in assembly on AVX2.
func fdctQuantLuma(src []byte, so, ss int, pred *[256]byte, mf *[16]int32, mfT *[4][8]int32, qbits uint, f int32, out *[16][16]int32) (mask uint32, nz int) {
	if !useAVX2Asm {
		return fdctQuantLumaGeneric(src, so, ss, pred, mf, qbits, f, out)
	}
	r := fdctQuantLumaAsm(&src[so], ss, &pred[0], &mfT[0][0], int(qbits), int(f), &out[0][0])
	return uint32(r & 0xffff), int(r >> 16)
}

//go:noescape
func fdctQuantChromaAsm(src *byte, ss int, pred *byte, mfT *int32, qbits, f int, out *int32, dc *int32) uint64

// fdctQuantChroma is fdctQuantChromaGeneric, in assembly on AVX2.
func fdctQuantChroma(src []byte, so, ss int, pred *[64]byte, mf *[16]int32, mfT *[4][8]int32, qbits uint, f int32, out *[4][16]int32, dc *[16]int32) (mask uint32, nz int) {
	if !useAVX2Asm {
		return fdctQuantChromaGeneric(src, so, ss, pred, mf, qbits, f, out, dc)
	}
	r := fdctQuantChromaAsm(&src[so], ss, &pred[0], &mfT[0][0], int(qbits), int(f), &out[0][0], &dc[0])
	return uint32(r & 0xffff), int(r >> 16)
}
