//go:build amd64 && !purego

package mvc

//go:noescape
func filterLumaAsm(p *byte, step, along int, bs, tc0 *int16, alpha, beta int)

//go:noescape
func filterChroma2Asm(cb, cr *byte, step, along int, bs, tc0, alpha, beta *int16)

func filterLuma(pl []byte, off, step, along int, bs *[4]uint8, alpha, beta int32, indexA int) {
	if !useAVX2Asm {
		filterLumaGeneric(pl, off, step, along, bs, alpha, beta, indexA)
		return
	}
	if alpha == 0 || beta == 0 {
		return
	}
	var vbs, vtc [16]int16
	for k := 0; k < 4; k++ {
		b := int16(bs[k])
		var tc int16
		if b > 0 && b < 4 {
			tc = int16(tc0Tab[indexA][b-1])
		}
		for i := 0; i < 4; i++ {
			vbs[k*4+i] = b
			vtc[k*4+i] = tc
		}
	}
	filterLumaAsm(&pl[off], step, along, &vbs[0], &vtc[0], int(alpha), int(beta))
}

func filterChroma2(cb, cr []byte, off, step, along int, bs *[4]uint8, alpha, beta [2]int32, indexA [2]int) {
	if !useAVX2Asm {
		filterChroma2Generic(cb, cr, off, step, along, bs, alpha, beta, indexA)
		return
	}
	var vbs, vtc, va, vb [16]int16
	for i := 0; i < 16; i++ {
		b := bs[(i&7)>>1]
		vbs[i] = int16(b)
		if b > 0 && b < 4 {
			vtc[i] = int16(tc0Tab[indexA[i>>3]][b-1])
		}
		va[i] = int16(alpha[i>>3])
		vb[i] = int16(beta[i>>3])
	}
	filterChroma2Asm(&cb[off], &cr[off], step, along, &vbs[0], &vtc[0], &va[0], &vb[0])
}
