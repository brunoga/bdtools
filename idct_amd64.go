//go:build amd64 && !purego

package mvc

//go:noescape
func idctRow4Asm(dst *byte, stride int, c *int16)

//go:noescape
func idct8x8RowAsm(dst *byte, stride int, c *int16)

//go:noescape
func idct8x8AddAsm(dst *byte, stride int, c *int16, cs int)

//go:noescape
func idctChromaAsm(cb, cr *byte, stride int, c *int16)

//go:noescape
func addConst16Asm(dst *byte, stride int, v *int16, rows int)

//go:noescape
func chromaAddDCAsm(cb, cr *byte, stride int, dc *int16)

func idctRow4(dst []byte, off, stride int, c []int16, mask uint16) {
	if !useAVX2Asm {
		idctRow4Generic(dst, off, stride, c, mask)
		return
	}
	idctRow4Asm(&dst[off], stride, &c[0])
}

func idct8x8Row(dst []byte, off, stride int, c []int16, mask uint8) {
	if !useAVX2Asm {
		idct8x8RowGeneric(dst, off, stride, c, mask)
		return
	}
	idct8x8RowAsm(&dst[off], stride, &c[0])
}

func idct8x8AddS(dst []byte, off, stride int, c []int16, cs int) {
	if !useAVX2Asm {
		idct8x8AddSGeneric(dst, off, stride, c, cs)
		return
	}
	idct8x8AddAsm(&dst[off], stride, &c[0], cs)
}

func idctChroma(cb, cr []byte, off, stride int, c *[2][64]int16, nz [2]uint8) {
	if !useAVX2Asm {
		idctChromaGeneric(cb, cr, off, stride, c, nz)
		return
	}
	idctChromaAsm(&cb[off], &cr[off], stride, &c[0][0])
}

func addConst16(dst []byte, off, stride int, v *[16]int16, rows int) {
	if !useAVX2Asm {
		addConst16Generic(dst, off, stride, v, rows)
		return
	}
	addConst16Asm(&dst[off], stride, &v[0], rows)
}

func chromaAddDC(cb, cr []byte, off, stride int, dc *[2][4]int16) {
	if !useAVX2Asm {
		chromaAddDCGeneric(cb, cr, off, stride, dc)
		return
	}
	chromaAddDCAsm(&cb[off], &cr[off], stride, &dc[0][0])
}
