//go:build !amd64 || purego

package mvc

func sadBlock(src []byte, so, ss int, ref []byte, ro, rs, w, h int) int {
	return sadGeneric(src, so, ss, ref, ro, rs, w, h)
}

func satdBlock(src []byte, so, ss int, pred []byte, po, ps, w, h int) int {
	return satdGeneric(src, so, ss, pred, po, ps, w, h)
}

func fdctQuantLuma(src []byte, so, ss int, pred *[256]byte, mf *[16]int32, mfT *[4][8]int32, qbits uint, f int32, out *[16][16]int32) (mask uint32, nz int) {
	return fdctQuantLumaGeneric(src, so, ss, pred, mf, qbits, f, out)
}

func fdctQuantChroma(src []byte, so, ss int, pred *[64]byte, mf *[16]int32, mfT *[4][8]int32, qbits uint, f int32, out *[4][16]int32, dc *[16]int32) (mask uint32, nz int) {
	return fdctQuantChromaGeneric(src, so, ss, pred, mf, qbits, f, out, dc)
}

func satdAvgBlock(src []byte, so, ss int, a []byte, ao, as int, b []byte, bo, bs, w, h int) int {
	return satdAvgGeneric(src, so, ss, a, ao, as, b, bo, bs, w, h)
}
