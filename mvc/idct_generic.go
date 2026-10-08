//go:build !amd64 || purego

package mvc

func addConst16(dst []byte, off, stride int, v *[16]int16, rows int) {
	addConst16Generic(dst, off, stride, v, rows)
}

func chromaAddDC(cb, cr []byte, off, stride int, dc *[2][4]int16) {
	chromaAddDCGeneric(cb, cr, off, stride, dc)
}

func idct8x8AddS(dst []byte, off, stride int, c []int16, cs int) {
	idct8x8AddSGeneric(dst, off, stride, c, cs)
}

func idctRow4(dst []byte, off, stride int, c []int16, mask uint16) {
	idctRow4Generic(dst, off, stride, c, mask)
}

func idct8x8Row(dst []byte, off, stride int, c []int16, mask uint8) {
	idct8x8RowGeneric(dst, off, stride, c, mask)
}

func idctChroma(cb, cr []byte, off, stride int, c *[2][64]int16, nz [2]uint8) {
	idctChromaGeneric(cb, cr, off, stride, c, nz)
}
