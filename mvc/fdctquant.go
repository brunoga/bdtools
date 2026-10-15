package mvc

// fdctQuantLumaGeneric transforms and quantises the 16 4x4 blocks of the
// macroblock's luma residual (the source at src[so], stride ss, less the
// prediction, stride 16) into levels in zigzag order per raster block,
// and gives which blocks have a level (bit r) and how many levels there
// are.
func fdctQuantLumaGeneric(src []byte, so, ss int, pred *[256]byte, mf *[16]int32, qbits uint, f int32, out *[16][16]int32) (mask uint32, nz int) {
	for r := 0; r < 16; r++ {
		bx, by := r&3, r>>2
		var d [16]int32
		for j := 0; j < 4; j++ {
			o := so + (by*4+j)*ss + bx*4
			po := (by*4+j)*16 + bx*4
			for i := 0; i < 4; i++ {
				d[j*4+i] = int32(src[o+i]) - int32(pred[po+i])
			}
		}
		fdct4(&d)
		for k := 0; k < 16; k++ {
			p := zigzag4x4[k]
			l := quantLevel(d[p], mf[p], qbits, f)
			out[r][k] = l
			if l != 0 {
				mask |= 1 << r
				nz++
			}
		}
	}
	return mask, nz
}

// mfTranspose lays a quantiser's 16 multipliers out for
// fdctQuantLumaAsm: per column j, rows 0-3, twice.
func mfTranspose(mf *[16]int32) (t [4][8]int32) {
	for j := 0; j < 4; j++ {
		for l := 0; l < 8; l++ {
			t[j][l] = mf[4*(l&3)+j]
		}
	}
	return t
}

// fdctQuantChromaGeneric transforms and quantises one chroma component's
// 8x8 (the source at src[so], stride ss, less the prediction, stride 8):
// the four blocks' AC levels in zigzag order (the first, the DC's, left
// zero), the transformed DC coefficients unquantised at dc[0], dc[4],
// dc[8] and dc[12], which blocks have an AC level and how many there
// are.
func fdctQuantChromaGeneric(src []byte, so, ss int, pred *[64]byte, mf *[16]int32, qbits uint, f int32, out *[4][16]int32, dc *[16]int32) (mask uint32, nz int) {
	for b := 0; b < 4; b++ {
		bx, by := b&1, b>>1
		var d [16]int32
		for j := 0; j < 4; j++ {
			o := so + (by*4+j)*ss + bx*4
			po := (by*4+j)*8 + bx*4
			for i := 0; i < 4; i++ {
				d[j*4+i] = int32(src[o+i]) - int32(pred[po+i])
			}
		}
		fdct4(&d)
		dc[b*4] = d[0]
		out[b][0] = 0
		for k := 1; k < 16; k++ {
			p := zigzag4x4[k]
			l := quantLevel(d[p], mf[p], qbits, f)
			out[b][k] = l
			if l != 0 {
				mask |= 1 << b
				nz++
			}
		}
	}
	return mask, nz
}
