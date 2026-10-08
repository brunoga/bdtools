package mvc

func clip255(v int32) byte {
	if uint32(v) > 255 {
		if v < 0 {
			return 0
		}
		return 255
	}
	return byte(v)
}

// Coefficient position tables for the spatial layout.
var (
	// Coefficients are stored transposed (row i, column j at j*stride+i)
	// so that the first, horizontal transform pass works on loaded rows
	// directly and SIMD code needs a single transpose.
	zzPos16  [16]uint8 // 4x4 zigzag index -> offset with row stride 16
	zzPos8   [16]uint8 // 4x4 zigzag index -> offset with row stride 8
	zz8Pos16 [64]uint8 // 8x8 zigzag index -> offset with row stride 16
	blkOff16 [16]int   // raster 4x4 block -> offset in luma coefficients
)

func init() {
	for k := 0; k < 16; k++ {
		p := int(zigzag4x4[k])
		zzPos16[k] = uint8(p&3*16 + p>>2)
		zzPos8[k] = uint8(p&3*8 + p>>2)
		blkOff16[k] = k>>2*64 + k&3*4
	}
	for k := 0; k < 64; k++ {
		p := int(zigzag8x8[k])
		zz8Pos16[k] = uint8(p&7*16 + p>>3)
	}
}

// idct4x4AddS applies the 4x4 inverse transform (8.5.12) to coefficients
// c (row stride cs) and adds the result to dst. c is cleared.
func idct4x4AddS(dst []byte, off, stride int, c []int16, cs int) {
	var t [16]int32
	for i := 0; i < 4; i++ {
		// row i is stored as column i (transposed layout)
		d0, d1, d2, d3 := int32(c[i]), int32(c[cs+i]), int32(c[2*cs+i]), int32(c[3*cs+i])
		e0 := d0 + d2
		e1 := d0 - d2
		e2 := (d1 >> 1) - d3
		e3 := d1 + (d3 >> 1)
		t[i*4] = e0 + e3
		t[i*4+1] = e1 + e2
		t[i*4+2] = e1 - e2
		t[i*4+3] = e0 - e3
	}
	for i := 0; i < 4; i++ {
		r := c[i*cs : i*cs+4]
		r[0], r[1], r[2], r[3] = 0, 0, 0, 0
	}
	for j := 0; j < 4; j++ {
		d0, d1, d2, d3 := t[j], t[4+j], t[8+j], t[12+j]
		e0 := d0 + d2
		e1 := d0 - d2
		e2 := (d1 >> 1) - d3
		e3 := d1 + (d3 >> 1)
		p := dst[off+j:]
		p[0] = clip255(int32(p[0]) + (e0+e3+32)>>6)
		p[stride] = clip255(int32(p[stride]) + (e1+e2+32)>>6)
		p[2*stride] = clip255(int32(p[2*stride]) + (e1-e2+32)>>6)
		p[3*stride] = clip255(int32(p[3*stride]) + (e0-e3+32)>>6)
	}
}

// idct8x8AddS applies the 8x8 inverse transform (8.5.13) to coefficients
// c (row stride cs) and adds the result to dst. c is cleared.
func idct8x8AddSGeneric(dst []byte, off, stride int, c []int16, cs int) {
	var t [64]int32
	var in, out [8]int32
	for i := 0; i < 8; i++ {
		for j := range in {
			in[j] = int32(c[j*cs+i]) // transposed layout
		}
		idct8(in[:], t[i*8:i*8+8], 1)
	}
	for i := 0; i < 8; i++ {
		r := c[i*cs : i*cs+8]
		for j := range r {
			r[j] = 0
		}
	}
	for j := 0; j < 8; j++ {
		for i := 0; i < 8; i++ {
			in[i] = t[i*8+j]
		}
		idct8(in[:], out[:], 1)
		for i := 0; i < 8; i++ {
			o := off + i*stride + j
			dst[o] = clip255(int32(dst[o]) + (out[i]+32)>>6)
		}
	}
}

func idct8(d []int32, o []int32, _ int) {
	d0, d1, d2, d3, d4, d5, d6, d7 := d[0], d[1], d[2], d[3], d[4], d[5], d[6], d[7]
	a0 := d0 + d4
	a4 := d0 - d4
	a2 := (d2 >> 1) - d6
	a6 := d2 + (d6 >> 1)
	b0 := a0 + a6
	b2 := a4 + a2
	b4 := a4 - a2
	b6 := a0 - a6
	a1 := -d3 + d5 - d7 - (d7 >> 1)
	a3 := d1 + d7 - d3 - (d3 >> 1)
	a5 := -d1 + d7 + d5 + (d5 >> 1)
	a7 := d3 + d5 + d1 + (d1 >> 1)
	b1 := a1 + (a7 >> 2)
	b7 := a7 - (a1 >> 2)
	b3 := a3 + (a5 >> 2)
	b5 := (a3 >> 2) - a5
	o[0] = b0 + b7
	o[1] = b2 + b5
	o[2] = b4 + b3
	o[3] = b6 + b1
	o[4] = b6 - b1
	o[5] = b4 - b3
	o[6] = b2 - b5
	o[7] = b0 - b7
}

// lumaDCDequant performs the inverse Hadamard transform and scaling of
// Intra16x16 DC coefficients (8.5.10). c is in raster order of the 4x4
// block grid; ls is LevelScale4x4(qp%6, 0, 0).
func lumaDCDequant(c *[16]int32, qp int, ls int32) {
	var t [16]int32
	for i := 0; i < 4; i++ {
		a, b, cc, d := c[i*4], c[i*4+1], c[i*4+2], c[i*4+3]
		t[i*4] = a + b + cc + d
		t[i*4+1] = a + b - cc - d
		t[i*4+2] = a - b - cc + d
		t[i*4+3] = a - b + cc - d
	}
	for j := 0; j < 4; j++ {
		a, b, cc, d := t[j], t[4+j], t[8+j], t[12+j]
		f := [4]int32{a + b + cc + d, a + b - cc - d, a - b - cc + d, a - b + cc - d}
		for i := 0; i < 4; i++ {
			v := f[i] * ls
			if qp >= 36 {
				v <<= uint(qp/6 - 6)
			} else {
				sh := uint(6 - qp/6)
				v = (v + 1<<(sh-1)) >> sh
			}
			c[i*4+j] = v
		}
	}
}

// chromaDCDequant performs the 2x2 chroma DC transform and scaling (8.5.11).
func chromaDCDequant(c *[4]int32, qp int, ls int32) {
	a, b, cc, d := c[0], c[1], c[2], c[3]
	f := [4]int32{a + b + cc + d, a - b + cc - d, a + b - cc - d, a - b - cc + d}
	for i := range f {
		c[i] = ((f[i] * ls) << uint(qp/6)) >> 5
	}
}

// idctRow4Generic transforms the 4x4 blocks in a row of 4 (mask bit i =
// block i coded); c starts at the row's first coefficient (stride 16).
func idctRow4Generic(dst []byte, off, stride int, c []int16, mask uint16) {
	for i := 0; i < 4; i++ {
		if mask>>i&1 != 0 {
			idct4x4AddS(dst, off+i*4, stride, c[i*4:], 16)
		}
	}
}

// idct8x8RowGeneric transforms the two 8x8 blocks of a row (mask bits 0,1).
func idct8x8RowGeneric(dst []byte, off, stride int, c []int16, mask uint8) {
	for i := 0; i < 2; i++ {
		if mask>>i&1 != 0 {
			idct8x8AddSGeneric(dst, off+i*8, stride, c[i*8:], 16)
		}
	}
}

// idctChromaGeneric transforms the coded chroma 4x4 blocks of both
// components.
func idctChromaGeneric(cb, cr []byte, off, stride int, c *[2][64]int16, nz [2]uint8) {
	planes := [2][]byte{cb, cr}
	for p := 0; p < 2; p++ {
		for b := 0; b < 4; b++ {
			if nz[p]>>b&1 != 0 {
				idct4x4AddS(planes[p], off+(b>>1)*4*stride+(b&1)*4, stride, c[p][b>>1*32+b&1*4:], 8)
			}
		}
	}
}

// chromaAddDCGeneric adds per-block constants to both 8x8 chroma blocks.
func chromaAddDCGeneric(cb, cr []byte, off, stride int, dc *[2][4]int16) {
	planes := [2][]byte{cb, cr}
	for p := 0; p < 2; p++ {
		for y := 0; y < 8; y++ {
			row := planes[p][off+y*stride : off+y*stride+8]
			for x := range row {
				row[x] = clip255(int32(row[x]) + int32(dc[p][y>>2*2+x>>2]))
			}
		}
	}
}

// addConst16Generic adds per-column constants to rows of 16 pixels.
func addConst16Generic(dst []byte, off, stride int, v *[16]int16, rows int) {
	for y := 0; y < rows; y++ {
		r := dst[off+y*stride : off+y*stride+16]
		for x := range r {
			r[x] = clip255(int32(r[x]) + int32(v[x]))
		}
	}
}
