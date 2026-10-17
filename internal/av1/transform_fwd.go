package av1

// The forward transform, for the encoder: libaom's integer forward transform
// (av1_fwd_txfm2d_c and its 1D transforms, av1_fwd_txfm1d.c, Copyright the
// Alliance for Open Media, BSD-2-Clause), ported. It is not specified by the
// standard; what matters is that its coefficients come out at the scale the
// spec's dequantization and inverse transform expect, which libaom's do: the
// forward then the inverse gives back the residual within a unit or two.
//
// The columns go first: each is scaled up by a size-dependent shift, put
// through the vertical 1D transform and rounded down again; then each row
// through the horizontal one, rounded down and, for 2:1 rectangles, scaled
// by sqrt(2). The 1D transforms are butterfly networks at a size-dependent
// cosine precision (cos bits). Flipped types read the residual flipped, so
// that the inverse followed by reconstructAdd's flips gives it back. Of a
// 64-sample dimension only the 32 lowest frequencies are kept, so the
// output has the inverse's layout: th*tw coefficients, vertical frequency
// i and horizontal frequency j at i*tw+j.

// fwdShift is libaom's av1_fwd_txfm_shift_ls: the shift before the column
// transform (up), after it and after the row transform (down, negated).
var fwdShift = [TX_SIZES_ALL][3]int8{
	TX_4X4: {2, 0, 0}, TX_8X8: {2, -1, 0}, TX_16X16: {2, -2, 0},
	TX_32X32: {2, -4, 0}, TX_64X64: {0, -2, -2}, TX_4X8: {2, -1, 0},
	TX_8X4: {2, -1, 0}, TX_8X16: {2, -2, 0}, TX_16X8: {2, -2, 0},
	TX_16X32: {2, -4, 0}, TX_32X16: {2, -4, 0}, TX_32X64: {0, -2, -2},
	TX_64X32: {2, -4, -2}, TX_4X16: {2, -1, 0}, TX_16X4: {2, -1, 0},
	TX_8X32: {2, -2, 0}, TX_32X8: {2, -2, 0}, TX_16X64: {0, -2, 0},
	TX_64X16: {2, -4, 0},
}

// fwdCosBitCol and fwdCosBitRow are libaom's av1_fwd_cos_bit_col and _row,
// by log2(width)-2 and log2(height)-2.
var fwdCosBitCol = [5][5]uint8{
	{13, 13, 13, 0, 0},
	{13, 13, 13, 12, 0},
	{13, 13, 13, 12, 13},
	{0, 13, 13, 12, 13},
	{0, 0, 13, 12, 13},
}

var fwdCosBitRow = [5][5]uint8{
	{13, 13, 12, 0, 0},
	{13, 13, 13, 12, 0},
	{13, 13, 12, 13, 12},
	{0, 12, 13, 12, 11},
	{0, 0, 12, 11, 10},
}

// cospiTable[i][j] is round(cos(pi*j/128) * 2^(10+i)), libaom's
// av1_cospi_arr_data.
var cospiTable = [4][64]int32{
	{1024, 1024, 1023, 1021, 1019, 1016, 1013, 1009, 1004, 999, 993, 987, 980,
		972, 964, 955, 946, 936, 926, 915, 903, 891, 878, 865, 851, 837,
		822, 807, 792, 775, 759, 742, 724, 706, 688, 669, 650, 630, 610,
		590, 569, 548, 526, 505, 483, 460, 438, 415, 392, 369, 345, 321,
		297, 273, 249, 224, 200, 175, 150, 125, 100, 75, 50, 25},
	{2048, 2047, 2046, 2042, 2038, 2033, 2026, 2018, 2009, 1998, 1987,
		1974, 1960, 1945, 1928, 1911, 1892, 1872, 1851, 1829, 1806, 1782,
		1757, 1730, 1703, 1674, 1645, 1615, 1583, 1551, 1517, 1483, 1448,
		1412, 1375, 1338, 1299, 1260, 1220, 1179, 1138, 1096, 1053, 1009,
		965, 921, 876, 830, 784, 737, 690, 642, 595, 546, 498,
		449, 400, 350, 301, 251, 201, 151, 100, 50},
	{4096, 4095, 4091, 4085, 4076, 4065, 4052, 4036, 4017, 3996, 3973,
		3948, 3920, 3889, 3857, 3822, 3784, 3745, 3703, 3659, 3612, 3564,
		3513, 3461, 3406, 3349, 3290, 3229, 3166, 3102, 3035, 2967, 2896,
		2824, 2751, 2675, 2598, 2520, 2440, 2359, 2276, 2191, 2106, 2019,
		1931, 1842, 1751, 1660, 1567, 1474, 1380, 1285, 1189, 1092, 995,
		897, 799, 700, 601, 501, 401, 301, 201, 101},
	{8192, 8190, 8182, 8170, 8153, 8130, 8103, 8071, 8035, 7993, 7946,
		7895, 7839, 7779, 7713, 7643, 7568, 7489, 7405, 7317, 7225, 7128,
		7027, 6921, 6811, 6698, 6580, 6458, 6333, 6203, 6070, 5933, 5793,
		5649, 5501, 5351, 5197, 5040, 4880, 4717, 4551, 4383, 4212, 4038,
		3862, 3683, 3503, 3320, 3135, 2948, 2760, 2570, 2378, 2185, 1990,
		1795, 1598, 1401, 1202, 1003, 803, 603, 402, 201},
}

// sinpiTable is libaom's av1_sinpi_arr_data: round(sqrt(2)*sin(j*pi/9)*2/3
// * 2^(10+i)), adjusted so that elements 1 and 2 sum to element 4.
var sinpiTable = [4][5]int32{
	{0, 330, 621, 836, 951},
	{0, 660, 1241, 1672, 1901},
	{0, 1321, 2482, 3344, 3803},
	{0, 2642, 4964, 6689, 7606},
}

func cospiArr(bit int) *[64]int32 { return &cospiTable[bit-10] }

// halfBtf is libaom's half_btf: (w0*in0 + w1*in1) rounded down by bit.
func halfBtf(w0, in0, w1, in1 int32, bit int) int32 {
	r := int64(w0*in0) + int64(w1*in1)
	return int32((r + 1<<(bit-1)) >> bit)
}

// roundShift rounds x down by bit > 0 bits.
func roundShift(x int64, bit int) int32 {
	return int32((x + 1<<(bit-1)) >> bit)
}

// fwd1D is a forward 1D transform from in to out (distinct arrays).
type fwd1D func(in, out *[64]int32, cosBit int)

// fwdFuncs holds the 1D forward transforms by kind and log2 of the size.
var fwdFuncs = [3][7]fwd1D{
	tx1dDCT:      {2: fdct4, 3: fdct8, 4: fdct16, 5: fdct32, 6: fdct64},
	tx1dADST:     {2: fadst4, 3: fadst8, 4: fadst16},
	tx1dIdentity: {2: fidentity4, 3: fidentity8, 4: fidentity16, 5: fidentity32},
}

// forwardTransform transforms the w*h residual of a txSz block (row-major,
// picture orientation) by txType into th*tw coefficients; lossless selects
// the Walsh-Hadamard transform (txSz must then be TX_4X4). The
// coefficients are at the scale of the spec's Dequant for every size, so
// for the sizes whose dequantization divides by dqDenom (2 for 32x32,
// 16x32, 32x16, 16x64 and 64x16; 4 for 64x64, 32x64 and 64x32) the
// quantizer takes level = coefficient*dqDenom/q. bitDepth does not change the arithmetic (libaom
// uses it only for range checks) but is taken for symmetry with
// inverseTransform.
func forwardTransform(residual []int32, txSz, txType, bitDepth int, lossless bool, coeffs []int32) {
	_ = bitDepth
	if lossless {
		fwht4x4(residual, coeffs)
		return
	}
	log2W, log2H := int(txWidthLog2[txSz]), int(txHeightLog2[txSz])
	w, h := 1<<log2W, 1<<log2H
	if w == 64 || h == 64 {
		// The column results are needed for the 32 kept rows only, but
		// at the full width: more than the coefficients hold.
		var buf [32 * 64]int32
		forward2D(residual, txSz, txType, log2W, log2H, buf[:], coeffs)
		return
	}
	forward2D(residual, txSz, txType, log2W, log2H, coeffs[:w*h], coeffs)
}

// forward2D is fwd_txfm2d_c. buf holds Min(h, 32) rows of w column results;
// it may be coeffs itself when no dimension is 64.
func forward2D(residual []int32, txSz, txType, log2W, log2H int, buf, coeffs []int32) {
	w, h := 1<<log2W, 1<<log2H
	tw, th := min(w, 32), min(h, 32)
	residual = residual[:w*h]
	shift := &fwdShift[txSz]
	cosBitCol := int(fwdCosBitCol[log2W-2][log2H-2])
	cosBitRow := int(fwdCosBitRow[log2W-2][log2H-2])
	colFn := fwdFuncs[txTypeCol[txType]][log2H]
	rowFn := fwdFuncs[txTypeRow[txType]][log2W]
	if colFn == nil || rowFn == nil {
		panic("av1: no forward transform of this type at this size")
	}
	flipUD, flipLR := txFlipUD[txType], txFlipLR[txType]
	up := uint(shift[0])
	down1, down2 := int(-shift[1]), int(-shift[2])
	rect := log2W-log2H == 1 || log2H-log2W == 1

	var tin, tout [64]int32
	for c := 0; c < w; c++ {
		sc := c
		if flipLR {
			sc = w - 1 - c
		}
		if flipUD {
			for r := 0; r < h; r++ {
				tin[r] = residual[(h-1-r)*w+sc] << up
			}
		} else {
			for r := 0; r < h; r++ {
				tin[r] = residual[r*w+sc] << up
			}
		}
		colFn(&tin, &tout, cosBitCol)
		if down1 > 0 {
			for r := 0; r < th; r++ {
				buf[r*w+c] = roundShift(int64(tout[r]), down1)
			}
		} else {
			for r := 0; r < th; r++ {
				buf[r*w+c] = tout[r]
			}
		}
	}
	// Row r of buf is read whole before row r of coeffs (at most as long,
	// and never further on) is written.
	for r := 0; r < th; r++ {
		copy(tin[:w], buf[r*w:r*w+w])
		rowFn(&tin, &tout, cosBitRow)
		out := coeffs[r*tw : r*tw+tw]
		for c := range out {
			v := tout[c]
			if down2 > 0 {
				v = roundShift(int64(v), down2)
			}
			if rect {
				v = roundShift(int64(v)*5793, 12)
			}
			out[c] = v
		}
	}
}

// fwht4x4 is libaom's av1_fwht4x4_c, the lossless forward Walsh-Hadamard
// transform, whose output the inverse's pre-shift of 2 scales back.
func fwht4x4(residual, coeffs []int32) {
	residual, coeffs = residual[:16], coeffs[:16]
	var tmp [16]int32 // tmp[c*4+k]: column c, vertical frequency k
	for c := 0; c < 4; c++ {
		a, b, cc, d := residual[c], residual[4+c], residual[8+c], residual[12+c]
		a += b
		d -= cc
		e := (a - d) >> 1
		b = e - b
		cc = e - cc
		a -= cc
		d += b
		tmp[c*4], tmp[c*4+1], tmp[c*4+2], tmp[c*4+3] = a, cc, d, b
	}
	for k := 0; k < 4; k++ {
		a, b, cc, d := tmp[k], tmp[4+k], tmp[8+k], tmp[12+k]
		a += b
		d -= cc
		e := (a - d) >> 1
		b = e - b
		cc = e - cc
		a -= cc
		d += b
		coeffs[k*4], coeffs[k*4+1], coeffs[k*4+2], coeffs[k*4+3] = a*4, cc*4, d*4, b*4
	}
}

func fidentity4(in, out *[64]int32, _ int) {
	for i := 0; i < 4; i++ {
		out[i] = roundShift(int64(in[i])*5793, 12)
	}
}

func fidentity8(in, out *[64]int32, _ int) {
	for i := 0; i < 8; i++ {
		out[i] = in[i] * 2
	}
}

func fidentity16(in, out *[64]int32, _ int) {
	for i := 0; i < 16; i++ {
		out[i] = roundShift(int64(in[i])*2*5793, 12)
	}
}

func fidentity32(in, out *[64]int32, _ int) {
	for i := 0; i < 32; i++ {
		out[i] = in[i] * 4
	}
}

// fadst4 is libaom's av1_fadst4.
func fadst4(in, out *[64]int32, cosBit int) {
	sinpi := &sinpiTable[cosBit-10]
	x0, x1, x2, x3 := in[0], in[1], in[2], in[3]
	if x0|x1|x2|x3 == 0 {
		out[0], out[1], out[2], out[3] = 0, 0, 0, 0
		return
	}
	s0 := sinpi[1] * x0
	s1 := sinpi[4] * x0
	s2 := sinpi[2] * x1
	s3 := sinpi[1] * x1
	s4 := sinpi[3] * x2
	s5 := sinpi[4] * x3
	s6 := sinpi[2] * x3
	s7 := x0 + x1 - x3

	x0 = s0 + s2
	x1 = sinpi[3] * s7
	x2 = s1 - s3
	x3 = s4

	x0 += s5
	x2 += s6

	s0 = x0 + x3
	s1 = x1
	s2 = x2 - x3
	s3 = x2 - x0 + x3

	out[0] = roundShift(int64(s0), cosBit)
	out[1] = roundShift(int64(s1), cosBit)
	out[2] = roundShift(int64(s2), cosBit)
	out[3] = roundShift(int64(s3), cosBit)
}

// The DCTs and the 8- and 16-point ADSTs below are libaom's av1_fdct4 to
// av1_fdct64, av1_fadst8 and av1_fadst16, stage by stage (each stage
// writes out or step from the other).

func fdct4(in, out *[64]int32, cosBit int) {
	cospi := cospiArr(cosBit)
	var step [4]int32
	out[0] = in[0] + in[3]
	out[1] = in[1] + in[2]
	out[2] = -in[2] + in[1]
	out[3] = -in[3] + in[0]
	step[0] = halfBtf(cospi[32], out[0], cospi[32], out[1], cosBit)
	step[1] = halfBtf(-cospi[32], out[1], cospi[32], out[0], cosBit)
	step[2] = halfBtf(cospi[48], out[2], cospi[16], out[3], cosBit)
	step[3] = halfBtf(cospi[48], out[3], -cospi[16], out[2], cosBit)
	out[0] = step[0]
	out[1] = step[2]
	out[2] = step[1]
	out[3] = step[3]
}

func fdct8(in, out *[64]int32, cosBit int) {
	cospi := cospiArr(cosBit)
	var step [8]int32
	out[0] = in[0] + in[7]
	out[1] = in[1] + in[6]
	out[2] = in[2] + in[5]
	out[3] = in[3] + in[4]
	out[4] = -in[4] + in[3]
	out[5] = -in[5] + in[2]
	out[6] = -in[6] + in[1]
	out[7] = -in[7] + in[0]
	step[0] = out[0] + out[3]
	step[1] = out[1] + out[2]
	step[2] = -out[2] + out[1]
	step[3] = -out[3] + out[0]
	step[4] = out[4]
	step[5] = halfBtf(-cospi[32], out[5], cospi[32], out[6], cosBit)
	step[6] = halfBtf(cospi[32], out[6], cospi[32], out[5], cosBit)
	step[7] = out[7]
	out[0] = halfBtf(cospi[32], step[0], cospi[32], step[1], cosBit)
	out[1] = halfBtf(-cospi[32], step[1], cospi[32], step[0], cosBit)
	out[2] = halfBtf(cospi[48], step[2], cospi[16], step[3], cosBit)
	out[3] = halfBtf(cospi[48], step[3], -cospi[16], step[2], cosBit)
	out[4] = step[4] + step[5]
	out[5] = -step[5] + step[4]
	out[6] = -step[6] + step[7]
	out[7] = step[7] + step[6]
	step[0] = out[0]
	step[1] = out[1]
	step[2] = out[2]
	step[3] = out[3]
	step[4] = halfBtf(cospi[56], out[4], cospi[8], out[7], cosBit)
	step[5] = halfBtf(cospi[24], out[5], cospi[40], out[6], cosBit)
	step[6] = halfBtf(cospi[24], out[6], -cospi[40], out[5], cosBit)
	step[7] = halfBtf(cospi[56], out[7], -cospi[8], out[4], cosBit)
	out[0] = step[0]
	out[1] = step[4]
	out[2] = step[2]
	out[3] = step[6]
	out[4] = step[1]
	out[5] = step[5]
	out[6] = step[3]
	out[7] = step[7]
}

func fdct16(in, out *[64]int32, cosBit int) {
	cospi := cospiArr(cosBit)
	var step [16]int32
	out[0] = in[0] + in[15]
	out[1] = in[1] + in[14]
	out[2] = in[2] + in[13]
	out[3] = in[3] + in[12]
	out[4] = in[4] + in[11]
	out[5] = in[5] + in[10]
	out[6] = in[6] + in[9]
	out[7] = in[7] + in[8]
	out[8] = -in[8] + in[7]
	out[9] = -in[9] + in[6]
	out[10] = -in[10] + in[5]
	out[11] = -in[11] + in[4]
	out[12] = -in[12] + in[3]
	out[13] = -in[13] + in[2]
	out[14] = -in[14] + in[1]
	out[15] = -in[15] + in[0]
	step[0] = out[0] + out[7]
	step[1] = out[1] + out[6]
	step[2] = out[2] + out[5]
	step[3] = out[3] + out[4]
	step[4] = -out[4] + out[3]
	step[5] = -out[5] + out[2]
	step[6] = -out[6] + out[1]
	step[7] = -out[7] + out[0]
	step[8] = out[8]
	step[9] = out[9]
	step[10] = halfBtf(-cospi[32], out[10], cospi[32], out[13], cosBit)
	step[11] = halfBtf(-cospi[32], out[11], cospi[32], out[12], cosBit)
	step[12] = halfBtf(cospi[32], out[12], cospi[32], out[11], cosBit)
	step[13] = halfBtf(cospi[32], out[13], cospi[32], out[10], cosBit)
	step[14] = out[14]
	step[15] = out[15]
	out[0] = step[0] + step[3]
	out[1] = step[1] + step[2]
	out[2] = -step[2] + step[1]
	out[3] = -step[3] + step[0]
	out[4] = step[4]
	out[5] = halfBtf(-cospi[32], step[5], cospi[32], step[6], cosBit)
	out[6] = halfBtf(cospi[32], step[6], cospi[32], step[5], cosBit)
	out[7] = step[7]
	out[8] = step[8] + step[11]
	out[9] = step[9] + step[10]
	out[10] = -step[10] + step[9]
	out[11] = -step[11] + step[8]
	out[12] = -step[12] + step[15]
	out[13] = -step[13] + step[14]
	out[14] = step[14] + step[13]
	out[15] = step[15] + step[12]
	step[0] = halfBtf(cospi[32], out[0], cospi[32], out[1], cosBit)
	step[1] = halfBtf(-cospi[32], out[1], cospi[32], out[0], cosBit)
	step[2] = halfBtf(cospi[48], out[2], cospi[16], out[3], cosBit)
	step[3] = halfBtf(cospi[48], out[3], -cospi[16], out[2], cosBit)
	step[4] = out[4] + out[5]
	step[5] = -out[5] + out[4]
	step[6] = -out[6] + out[7]
	step[7] = out[7] + out[6]
	step[8] = out[8]
	step[9] = halfBtf(-cospi[16], out[9], cospi[48], out[14], cosBit)
	step[10] = halfBtf(-cospi[48], out[10], -cospi[16], out[13], cosBit)
	step[11] = out[11]
	step[12] = out[12]
	step[13] = halfBtf(cospi[48], out[13], -cospi[16], out[10], cosBit)
	step[14] = halfBtf(cospi[16], out[14], cospi[48], out[9], cosBit)
	step[15] = out[15]
	out[0] = step[0]
	out[1] = step[1]
	out[2] = step[2]
	out[3] = step[3]
	out[4] = halfBtf(cospi[56], step[4], cospi[8], step[7], cosBit)
	out[5] = halfBtf(cospi[24], step[5], cospi[40], step[6], cosBit)
	out[6] = halfBtf(cospi[24], step[6], -cospi[40], step[5], cosBit)
	out[7] = halfBtf(cospi[56], step[7], -cospi[8], step[4], cosBit)
	out[8] = step[8] + step[9]
	out[9] = -step[9] + step[8]
	out[10] = -step[10] + step[11]
	out[11] = step[11] + step[10]
	out[12] = step[12] + step[13]
	out[13] = -step[13] + step[12]
	out[14] = -step[14] + step[15]
	out[15] = step[15] + step[14]
	step[0] = out[0]
	step[1] = out[1]
	step[2] = out[2]
	step[3] = out[3]
	step[4] = out[4]
	step[5] = out[5]
	step[6] = out[6]
	step[7] = out[7]
	step[8] = halfBtf(cospi[60], out[8], cospi[4], out[15], cosBit)
	step[9] = halfBtf(cospi[28], out[9], cospi[36], out[14], cosBit)
	step[10] = halfBtf(cospi[44], out[10], cospi[20], out[13], cosBit)
	step[11] = halfBtf(cospi[12], out[11], cospi[52], out[12], cosBit)
	step[12] = halfBtf(cospi[12], out[12], -cospi[52], out[11], cosBit)
	step[13] = halfBtf(cospi[44], out[13], -cospi[20], out[10], cosBit)
	step[14] = halfBtf(cospi[28], out[14], -cospi[36], out[9], cosBit)
	step[15] = halfBtf(cospi[60], out[15], -cospi[4], out[8], cosBit)
	out[0] = step[0]
	out[1] = step[8]
	out[2] = step[4]
	out[3] = step[12]
	out[4] = step[2]
	out[5] = step[10]
	out[6] = step[6]
	out[7] = step[14]
	out[8] = step[1]
	out[9] = step[9]
	out[10] = step[5]
	out[11] = step[13]
	out[12] = step[3]
	out[13] = step[11]
	out[14] = step[7]
	out[15] = step[15]
}

func fdct32(in, out *[64]int32, cosBit int) {
	cospi := cospiArr(cosBit)
	var step [32]int32
	out[0] = in[0] + in[31]
	out[1] = in[1] + in[30]
	out[2] = in[2] + in[29]
	out[3] = in[3] + in[28]
	out[4] = in[4] + in[27]
	out[5] = in[5] + in[26]
	out[6] = in[6] + in[25]
	out[7] = in[7] + in[24]
	out[8] = in[8] + in[23]
	out[9] = in[9] + in[22]
	out[10] = in[10] + in[21]
	out[11] = in[11] + in[20]
	out[12] = in[12] + in[19]
	out[13] = in[13] + in[18]
	out[14] = in[14] + in[17]
	out[15] = in[15] + in[16]
	out[16] = -in[16] + in[15]
	out[17] = -in[17] + in[14]
	out[18] = -in[18] + in[13]
	out[19] = -in[19] + in[12]
	out[20] = -in[20] + in[11]
	out[21] = -in[21] + in[10]
	out[22] = -in[22] + in[9]
	out[23] = -in[23] + in[8]
	out[24] = -in[24] + in[7]
	out[25] = -in[25] + in[6]
	out[26] = -in[26] + in[5]
	out[27] = -in[27] + in[4]
	out[28] = -in[28] + in[3]
	out[29] = -in[29] + in[2]
	out[30] = -in[30] + in[1]
	out[31] = -in[31] + in[0]
	step[0] = out[0] + out[15]
	step[1] = out[1] + out[14]
	step[2] = out[2] + out[13]
	step[3] = out[3] + out[12]
	step[4] = out[4] + out[11]
	step[5] = out[5] + out[10]
	step[6] = out[6] + out[9]
	step[7] = out[7] + out[8]
	step[8] = -out[8] + out[7]
	step[9] = -out[9] + out[6]
	step[10] = -out[10] + out[5]
	step[11] = -out[11] + out[4]
	step[12] = -out[12] + out[3]
	step[13] = -out[13] + out[2]
	step[14] = -out[14] + out[1]
	step[15] = -out[15] + out[0]
	step[16] = out[16]
	step[17] = out[17]
	step[18] = out[18]
	step[19] = out[19]
	step[20] = halfBtf(-cospi[32], out[20], cospi[32], out[27], cosBit)
	step[21] = halfBtf(-cospi[32], out[21], cospi[32], out[26], cosBit)
	step[22] = halfBtf(-cospi[32], out[22], cospi[32], out[25], cosBit)
	step[23] = halfBtf(-cospi[32], out[23], cospi[32], out[24], cosBit)
	step[24] = halfBtf(cospi[32], out[24], cospi[32], out[23], cosBit)
	step[25] = halfBtf(cospi[32], out[25], cospi[32], out[22], cosBit)
	step[26] = halfBtf(cospi[32], out[26], cospi[32], out[21], cosBit)
	step[27] = halfBtf(cospi[32], out[27], cospi[32], out[20], cosBit)
	step[28] = out[28]
	step[29] = out[29]
	step[30] = out[30]
	step[31] = out[31]
	out[0] = step[0] + step[7]
	out[1] = step[1] + step[6]
	out[2] = step[2] + step[5]
	out[3] = step[3] + step[4]
	out[4] = -step[4] + step[3]
	out[5] = -step[5] + step[2]
	out[6] = -step[6] + step[1]
	out[7] = -step[7] + step[0]
	out[8] = step[8]
	out[9] = step[9]
	out[10] = halfBtf(-cospi[32], step[10], cospi[32], step[13], cosBit)
	out[11] = halfBtf(-cospi[32], step[11], cospi[32], step[12], cosBit)
	out[12] = halfBtf(cospi[32], step[12], cospi[32], step[11], cosBit)
	out[13] = halfBtf(cospi[32], step[13], cospi[32], step[10], cosBit)
	out[14] = step[14]
	out[15] = step[15]
	out[16] = step[16] + step[23]
	out[17] = step[17] + step[22]
	out[18] = step[18] + step[21]
	out[19] = step[19] + step[20]
	out[20] = -step[20] + step[19]
	out[21] = -step[21] + step[18]
	out[22] = -step[22] + step[17]
	out[23] = -step[23] + step[16]
	out[24] = -step[24] + step[31]
	out[25] = -step[25] + step[30]
	out[26] = -step[26] + step[29]
	out[27] = -step[27] + step[28]
	out[28] = step[28] + step[27]
	out[29] = step[29] + step[26]
	out[30] = step[30] + step[25]
	out[31] = step[31] + step[24]
	step[0] = out[0] + out[3]
	step[1] = out[1] + out[2]
	step[2] = -out[2] + out[1]
	step[3] = -out[3] + out[0]
	step[4] = out[4]
	step[5] = halfBtf(-cospi[32], out[5], cospi[32], out[6], cosBit)
	step[6] = halfBtf(cospi[32], out[6], cospi[32], out[5], cosBit)
	step[7] = out[7]
	step[8] = out[8] + out[11]
	step[9] = out[9] + out[10]
	step[10] = -out[10] + out[9]
	step[11] = -out[11] + out[8]
	step[12] = -out[12] + out[15]
	step[13] = -out[13] + out[14]
	step[14] = out[14] + out[13]
	step[15] = out[15] + out[12]
	step[16] = out[16]
	step[17] = out[17]
	step[18] = halfBtf(-cospi[16], out[18], cospi[48], out[29], cosBit)
	step[19] = halfBtf(-cospi[16], out[19], cospi[48], out[28], cosBit)
	step[20] = halfBtf(-cospi[48], out[20], -cospi[16], out[27], cosBit)
	step[21] = halfBtf(-cospi[48], out[21], -cospi[16], out[26], cosBit)
	step[22] = out[22]
	step[23] = out[23]
	step[24] = out[24]
	step[25] = out[25]
	step[26] = halfBtf(cospi[48], out[26], -cospi[16], out[21], cosBit)
	step[27] = halfBtf(cospi[48], out[27], -cospi[16], out[20], cosBit)
	step[28] = halfBtf(cospi[16], out[28], cospi[48], out[19], cosBit)
	step[29] = halfBtf(cospi[16], out[29], cospi[48], out[18], cosBit)
	step[30] = out[30]
	step[31] = out[31]
	out[0] = halfBtf(cospi[32], step[0], cospi[32], step[1], cosBit)
	out[1] = halfBtf(-cospi[32], step[1], cospi[32], step[0], cosBit)
	out[2] = halfBtf(cospi[48], step[2], cospi[16], step[3], cosBit)
	out[3] = halfBtf(cospi[48], step[3], -cospi[16], step[2], cosBit)
	out[4] = step[4] + step[5]
	out[5] = -step[5] + step[4]
	out[6] = -step[6] + step[7]
	out[7] = step[7] + step[6]
	out[8] = step[8]
	out[9] = halfBtf(-cospi[16], step[9], cospi[48], step[14], cosBit)
	out[10] = halfBtf(-cospi[48], step[10], -cospi[16], step[13], cosBit)
	out[11] = step[11]
	out[12] = step[12]
	out[13] = halfBtf(cospi[48], step[13], -cospi[16], step[10], cosBit)
	out[14] = halfBtf(cospi[16], step[14], cospi[48], step[9], cosBit)
	out[15] = step[15]
	out[16] = step[16] + step[19]
	out[17] = step[17] + step[18]
	out[18] = -step[18] + step[17]
	out[19] = -step[19] + step[16]
	out[20] = -step[20] + step[23]
	out[21] = -step[21] + step[22]
	out[22] = step[22] + step[21]
	out[23] = step[23] + step[20]
	out[24] = step[24] + step[27]
	out[25] = step[25] + step[26]
	out[26] = -step[26] + step[25]
	out[27] = -step[27] + step[24]
	out[28] = -step[28] + step[31]
	out[29] = -step[29] + step[30]
	out[30] = step[30] + step[29]
	out[31] = step[31] + step[28]
	step[0] = out[0]
	step[1] = out[1]
	step[2] = out[2]
	step[3] = out[3]
	step[4] = halfBtf(cospi[56], out[4], cospi[8], out[7], cosBit)
	step[5] = halfBtf(cospi[24], out[5], cospi[40], out[6], cosBit)
	step[6] = halfBtf(cospi[24], out[6], -cospi[40], out[5], cosBit)
	step[7] = halfBtf(cospi[56], out[7], -cospi[8], out[4], cosBit)
	step[8] = out[8] + out[9]
	step[9] = -out[9] + out[8]
	step[10] = -out[10] + out[11]
	step[11] = out[11] + out[10]
	step[12] = out[12] + out[13]
	step[13] = -out[13] + out[12]
	step[14] = -out[14] + out[15]
	step[15] = out[15] + out[14]
	step[16] = out[16]
	step[17] = halfBtf(-cospi[8], out[17], cospi[56], out[30], cosBit)
	step[18] = halfBtf(-cospi[56], out[18], -cospi[8], out[29], cosBit)
	step[19] = out[19]
	step[20] = out[20]
	step[21] = halfBtf(-cospi[40], out[21], cospi[24], out[26], cosBit)
	step[22] = halfBtf(-cospi[24], out[22], -cospi[40], out[25], cosBit)
	step[23] = out[23]
	step[24] = out[24]
	step[25] = halfBtf(cospi[24], out[25], -cospi[40], out[22], cosBit)
	step[26] = halfBtf(cospi[40], out[26], cospi[24], out[21], cosBit)
	step[27] = out[27]
	step[28] = out[28]
	step[29] = halfBtf(cospi[56], out[29], -cospi[8], out[18], cosBit)
	step[30] = halfBtf(cospi[8], out[30], cospi[56], out[17], cosBit)
	step[31] = out[31]
	out[0] = step[0]
	out[1] = step[1]
	out[2] = step[2]
	out[3] = step[3]
	out[4] = step[4]
	out[5] = step[5]
	out[6] = step[6]
	out[7] = step[7]
	out[8] = halfBtf(cospi[60], step[8], cospi[4], step[15], cosBit)
	out[9] = halfBtf(cospi[28], step[9], cospi[36], step[14], cosBit)
	out[10] = halfBtf(cospi[44], step[10], cospi[20], step[13], cosBit)
	out[11] = halfBtf(cospi[12], step[11], cospi[52], step[12], cosBit)
	out[12] = halfBtf(cospi[12], step[12], -cospi[52], step[11], cosBit)
	out[13] = halfBtf(cospi[44], step[13], -cospi[20], step[10], cosBit)
	out[14] = halfBtf(cospi[28], step[14], -cospi[36], step[9], cosBit)
	out[15] = halfBtf(cospi[60], step[15], -cospi[4], step[8], cosBit)
	out[16] = step[16] + step[17]
	out[17] = -step[17] + step[16]
	out[18] = -step[18] + step[19]
	out[19] = step[19] + step[18]
	out[20] = step[20] + step[21]
	out[21] = -step[21] + step[20]
	out[22] = -step[22] + step[23]
	out[23] = step[23] + step[22]
	out[24] = step[24] + step[25]
	out[25] = -step[25] + step[24]
	out[26] = -step[26] + step[27]
	out[27] = step[27] + step[26]
	out[28] = step[28] + step[29]
	out[29] = -step[29] + step[28]
	out[30] = -step[30] + step[31]
	out[31] = step[31] + step[30]
	step[0] = out[0]
	step[1] = out[1]
	step[2] = out[2]
	step[3] = out[3]
	step[4] = out[4]
	step[5] = out[5]
	step[6] = out[6]
	step[7] = out[7]
	step[8] = out[8]
	step[9] = out[9]
	step[10] = out[10]
	step[11] = out[11]
	step[12] = out[12]
	step[13] = out[13]
	step[14] = out[14]
	step[15] = out[15]
	step[16] = halfBtf(cospi[62], out[16], cospi[2], out[31], cosBit)
	step[17] = halfBtf(cospi[30], out[17], cospi[34], out[30], cosBit)
	step[18] = halfBtf(cospi[46], out[18], cospi[18], out[29], cosBit)
	step[19] = halfBtf(cospi[14], out[19], cospi[50], out[28], cosBit)
	step[20] = halfBtf(cospi[54], out[20], cospi[10], out[27], cosBit)
	step[21] = halfBtf(cospi[22], out[21], cospi[42], out[26], cosBit)
	step[22] = halfBtf(cospi[38], out[22], cospi[26], out[25], cosBit)
	step[23] = halfBtf(cospi[6], out[23], cospi[58], out[24], cosBit)
	step[24] = halfBtf(cospi[6], out[24], -cospi[58], out[23], cosBit)
	step[25] = halfBtf(cospi[38], out[25], -cospi[26], out[22], cosBit)
	step[26] = halfBtf(cospi[22], out[26], -cospi[42], out[21], cosBit)
	step[27] = halfBtf(cospi[54], out[27], -cospi[10], out[20], cosBit)
	step[28] = halfBtf(cospi[14], out[28], -cospi[50], out[19], cosBit)
	step[29] = halfBtf(cospi[46], out[29], -cospi[18], out[18], cosBit)
	step[30] = halfBtf(cospi[30], out[30], -cospi[34], out[17], cosBit)
	step[31] = halfBtf(cospi[62], out[31], -cospi[2], out[16], cosBit)
	out[0] = step[0]
	out[1] = step[16]
	out[2] = step[8]
	out[3] = step[24]
	out[4] = step[4]
	out[5] = step[20]
	out[6] = step[12]
	out[7] = step[28]
	out[8] = step[2]
	out[9] = step[18]
	out[10] = step[10]
	out[11] = step[26]
	out[12] = step[6]
	out[13] = step[22]
	out[14] = step[14]
	out[15] = step[30]
	out[16] = step[1]
	out[17] = step[17]
	out[18] = step[9]
	out[19] = step[25]
	out[20] = step[5]
	out[21] = step[21]
	out[22] = step[13]
	out[23] = step[29]
	out[24] = step[3]
	out[25] = step[19]
	out[26] = step[11]
	out[27] = step[27]
	out[28] = step[7]
	out[29] = step[23]
	out[30] = step[15]
	out[31] = step[31]
}

func fdct64(in, out *[64]int32, cosBit int) {
	cospi := cospiArr(cosBit)
	var step [64]int32
	out[0] = in[0] + in[63]
	out[1] = in[1] + in[62]
	out[2] = in[2] + in[61]
	out[3] = in[3] + in[60]
	out[4] = in[4] + in[59]
	out[5] = in[5] + in[58]
	out[6] = in[6] + in[57]
	out[7] = in[7] + in[56]
	out[8] = in[8] + in[55]
	out[9] = in[9] + in[54]
	out[10] = in[10] + in[53]
	out[11] = in[11] + in[52]
	out[12] = in[12] + in[51]
	out[13] = in[13] + in[50]
	out[14] = in[14] + in[49]
	out[15] = in[15] + in[48]
	out[16] = in[16] + in[47]
	out[17] = in[17] + in[46]
	out[18] = in[18] + in[45]
	out[19] = in[19] + in[44]
	out[20] = in[20] + in[43]
	out[21] = in[21] + in[42]
	out[22] = in[22] + in[41]
	out[23] = in[23] + in[40]
	out[24] = in[24] + in[39]
	out[25] = in[25] + in[38]
	out[26] = in[26] + in[37]
	out[27] = in[27] + in[36]
	out[28] = in[28] + in[35]
	out[29] = in[29] + in[34]
	out[30] = in[30] + in[33]
	out[31] = in[31] + in[32]
	out[32] = -in[32] + in[31]
	out[33] = -in[33] + in[30]
	out[34] = -in[34] + in[29]
	out[35] = -in[35] + in[28]
	out[36] = -in[36] + in[27]
	out[37] = -in[37] + in[26]
	out[38] = -in[38] + in[25]
	out[39] = -in[39] + in[24]
	out[40] = -in[40] + in[23]
	out[41] = -in[41] + in[22]
	out[42] = -in[42] + in[21]
	out[43] = -in[43] + in[20]
	out[44] = -in[44] + in[19]
	out[45] = -in[45] + in[18]
	out[46] = -in[46] + in[17]
	out[47] = -in[47] + in[16]
	out[48] = -in[48] + in[15]
	out[49] = -in[49] + in[14]
	out[50] = -in[50] + in[13]
	out[51] = -in[51] + in[12]
	out[52] = -in[52] + in[11]
	out[53] = -in[53] + in[10]
	out[54] = -in[54] + in[9]
	out[55] = -in[55] + in[8]
	out[56] = -in[56] + in[7]
	out[57] = -in[57] + in[6]
	out[58] = -in[58] + in[5]
	out[59] = -in[59] + in[4]
	out[60] = -in[60] + in[3]
	out[61] = -in[61] + in[2]
	out[62] = -in[62] + in[1]
	out[63] = -in[63] + in[0]
	step[0] = out[0] + out[31]
	step[1] = out[1] + out[30]
	step[2] = out[2] + out[29]
	step[3] = out[3] + out[28]
	step[4] = out[4] + out[27]
	step[5] = out[5] + out[26]
	step[6] = out[6] + out[25]
	step[7] = out[7] + out[24]
	step[8] = out[8] + out[23]
	step[9] = out[9] + out[22]
	step[10] = out[10] + out[21]
	step[11] = out[11] + out[20]
	step[12] = out[12] + out[19]
	step[13] = out[13] + out[18]
	step[14] = out[14] + out[17]
	step[15] = out[15] + out[16]
	step[16] = -out[16] + out[15]
	step[17] = -out[17] + out[14]
	step[18] = -out[18] + out[13]
	step[19] = -out[19] + out[12]
	step[20] = -out[20] + out[11]
	step[21] = -out[21] + out[10]
	step[22] = -out[22] + out[9]
	step[23] = -out[23] + out[8]
	step[24] = -out[24] + out[7]
	step[25] = -out[25] + out[6]
	step[26] = -out[26] + out[5]
	step[27] = -out[27] + out[4]
	step[28] = -out[28] + out[3]
	step[29] = -out[29] + out[2]
	step[30] = -out[30] + out[1]
	step[31] = -out[31] + out[0]
	step[32] = out[32]
	step[33] = out[33]
	step[34] = out[34]
	step[35] = out[35]
	step[36] = out[36]
	step[37] = out[37]
	step[38] = out[38]
	step[39] = out[39]
	step[40] = halfBtf(-cospi[32], out[40], cospi[32], out[55], cosBit)
	step[41] = halfBtf(-cospi[32], out[41], cospi[32], out[54], cosBit)
	step[42] = halfBtf(-cospi[32], out[42], cospi[32], out[53], cosBit)
	step[43] = halfBtf(-cospi[32], out[43], cospi[32], out[52], cosBit)
	step[44] = halfBtf(-cospi[32], out[44], cospi[32], out[51], cosBit)
	step[45] = halfBtf(-cospi[32], out[45], cospi[32], out[50], cosBit)
	step[46] = halfBtf(-cospi[32], out[46], cospi[32], out[49], cosBit)
	step[47] = halfBtf(-cospi[32], out[47], cospi[32], out[48], cosBit)
	step[48] = halfBtf(cospi[32], out[48], cospi[32], out[47], cosBit)
	step[49] = halfBtf(cospi[32], out[49], cospi[32], out[46], cosBit)
	step[50] = halfBtf(cospi[32], out[50], cospi[32], out[45], cosBit)
	step[51] = halfBtf(cospi[32], out[51], cospi[32], out[44], cosBit)
	step[52] = halfBtf(cospi[32], out[52], cospi[32], out[43], cosBit)
	step[53] = halfBtf(cospi[32], out[53], cospi[32], out[42], cosBit)
	step[54] = halfBtf(cospi[32], out[54], cospi[32], out[41], cosBit)
	step[55] = halfBtf(cospi[32], out[55], cospi[32], out[40], cosBit)
	step[56] = out[56]
	step[57] = out[57]
	step[58] = out[58]
	step[59] = out[59]
	step[60] = out[60]
	step[61] = out[61]
	step[62] = out[62]
	step[63] = out[63]
	out[0] = step[0] + step[15]
	out[1] = step[1] + step[14]
	out[2] = step[2] + step[13]
	out[3] = step[3] + step[12]
	out[4] = step[4] + step[11]
	out[5] = step[5] + step[10]
	out[6] = step[6] + step[9]
	out[7] = step[7] + step[8]
	out[8] = -step[8] + step[7]
	out[9] = -step[9] + step[6]
	out[10] = -step[10] + step[5]
	out[11] = -step[11] + step[4]
	out[12] = -step[12] + step[3]
	out[13] = -step[13] + step[2]
	out[14] = -step[14] + step[1]
	out[15] = -step[15] + step[0]
	out[16] = step[16]
	out[17] = step[17]
	out[18] = step[18]
	out[19] = step[19]
	out[20] = halfBtf(-cospi[32], step[20], cospi[32], step[27], cosBit)
	out[21] = halfBtf(-cospi[32], step[21], cospi[32], step[26], cosBit)
	out[22] = halfBtf(-cospi[32], step[22], cospi[32], step[25], cosBit)
	out[23] = halfBtf(-cospi[32], step[23], cospi[32], step[24], cosBit)
	out[24] = halfBtf(cospi[32], step[24], cospi[32], step[23], cosBit)
	out[25] = halfBtf(cospi[32], step[25], cospi[32], step[22], cosBit)
	out[26] = halfBtf(cospi[32], step[26], cospi[32], step[21], cosBit)
	out[27] = halfBtf(cospi[32], step[27], cospi[32], step[20], cosBit)
	out[28] = step[28]
	out[29] = step[29]
	out[30] = step[30]
	out[31] = step[31]
	out[32] = step[32] + step[47]
	out[33] = step[33] + step[46]
	out[34] = step[34] + step[45]
	out[35] = step[35] + step[44]
	out[36] = step[36] + step[43]
	out[37] = step[37] + step[42]
	out[38] = step[38] + step[41]
	out[39] = step[39] + step[40]
	out[40] = -step[40] + step[39]
	out[41] = -step[41] + step[38]
	out[42] = -step[42] + step[37]
	out[43] = -step[43] + step[36]
	out[44] = -step[44] + step[35]
	out[45] = -step[45] + step[34]
	out[46] = -step[46] + step[33]
	out[47] = -step[47] + step[32]
	out[48] = -step[48] + step[63]
	out[49] = -step[49] + step[62]
	out[50] = -step[50] + step[61]
	out[51] = -step[51] + step[60]
	out[52] = -step[52] + step[59]
	out[53] = -step[53] + step[58]
	out[54] = -step[54] + step[57]
	out[55] = -step[55] + step[56]
	out[56] = step[56] + step[55]
	out[57] = step[57] + step[54]
	out[58] = step[58] + step[53]
	out[59] = step[59] + step[52]
	out[60] = step[60] + step[51]
	out[61] = step[61] + step[50]
	out[62] = step[62] + step[49]
	out[63] = step[63] + step[48]
	step[0] = out[0] + out[7]
	step[1] = out[1] + out[6]
	step[2] = out[2] + out[5]
	step[3] = out[3] + out[4]
	step[4] = -out[4] + out[3]
	step[5] = -out[5] + out[2]
	step[6] = -out[6] + out[1]
	step[7] = -out[7] + out[0]
	step[8] = out[8]
	step[9] = out[9]
	step[10] = halfBtf(-cospi[32], out[10], cospi[32], out[13], cosBit)
	step[11] = halfBtf(-cospi[32], out[11], cospi[32], out[12], cosBit)
	step[12] = halfBtf(cospi[32], out[12], cospi[32], out[11], cosBit)
	step[13] = halfBtf(cospi[32], out[13], cospi[32], out[10], cosBit)
	step[14] = out[14]
	step[15] = out[15]
	step[16] = out[16] + out[23]
	step[17] = out[17] + out[22]
	step[18] = out[18] + out[21]
	step[19] = out[19] + out[20]
	step[20] = -out[20] + out[19]
	step[21] = -out[21] + out[18]
	step[22] = -out[22] + out[17]
	step[23] = -out[23] + out[16]
	step[24] = -out[24] + out[31]
	step[25] = -out[25] + out[30]
	step[26] = -out[26] + out[29]
	step[27] = -out[27] + out[28]
	step[28] = out[28] + out[27]
	step[29] = out[29] + out[26]
	step[30] = out[30] + out[25]
	step[31] = out[31] + out[24]
	step[32] = out[32]
	step[33] = out[33]
	step[34] = out[34]
	step[35] = out[35]
	step[36] = halfBtf(-cospi[16], out[36], cospi[48], out[59], cosBit)
	step[37] = halfBtf(-cospi[16], out[37], cospi[48], out[58], cosBit)
	step[38] = halfBtf(-cospi[16], out[38], cospi[48], out[57], cosBit)
	step[39] = halfBtf(-cospi[16], out[39], cospi[48], out[56], cosBit)
	step[40] = halfBtf(-cospi[48], out[40], -cospi[16], out[55], cosBit)
	step[41] = halfBtf(-cospi[48], out[41], -cospi[16], out[54], cosBit)
	step[42] = halfBtf(-cospi[48], out[42], -cospi[16], out[53], cosBit)
	step[43] = halfBtf(-cospi[48], out[43], -cospi[16], out[52], cosBit)
	step[44] = out[44]
	step[45] = out[45]
	step[46] = out[46]
	step[47] = out[47]
	step[48] = out[48]
	step[49] = out[49]
	step[50] = out[50]
	step[51] = out[51]
	step[52] = halfBtf(cospi[48], out[52], -cospi[16], out[43], cosBit)
	step[53] = halfBtf(cospi[48], out[53], -cospi[16], out[42], cosBit)
	step[54] = halfBtf(cospi[48], out[54], -cospi[16], out[41], cosBit)
	step[55] = halfBtf(cospi[48], out[55], -cospi[16], out[40], cosBit)
	step[56] = halfBtf(cospi[16], out[56], cospi[48], out[39], cosBit)
	step[57] = halfBtf(cospi[16], out[57], cospi[48], out[38], cosBit)
	step[58] = halfBtf(cospi[16], out[58], cospi[48], out[37], cosBit)
	step[59] = halfBtf(cospi[16], out[59], cospi[48], out[36], cosBit)
	step[60] = out[60]
	step[61] = out[61]
	step[62] = out[62]
	step[63] = out[63]
	out[0] = step[0] + step[3]
	out[1] = step[1] + step[2]
	out[2] = -step[2] + step[1]
	out[3] = -step[3] + step[0]
	out[4] = step[4]
	out[5] = halfBtf(-cospi[32], step[5], cospi[32], step[6], cosBit)
	out[6] = halfBtf(cospi[32], step[6], cospi[32], step[5], cosBit)
	out[7] = step[7]
	out[8] = step[8] + step[11]
	out[9] = step[9] + step[10]
	out[10] = -step[10] + step[9]
	out[11] = -step[11] + step[8]
	out[12] = -step[12] + step[15]
	out[13] = -step[13] + step[14]
	out[14] = step[14] + step[13]
	out[15] = step[15] + step[12]
	out[16] = step[16]
	out[17] = step[17]
	out[18] = halfBtf(-cospi[16], step[18], cospi[48], step[29], cosBit)
	out[19] = halfBtf(-cospi[16], step[19], cospi[48], step[28], cosBit)
	out[20] = halfBtf(-cospi[48], step[20], -cospi[16], step[27], cosBit)
	out[21] = halfBtf(-cospi[48], step[21], -cospi[16], step[26], cosBit)
	out[22] = step[22]
	out[23] = step[23]
	out[24] = step[24]
	out[25] = step[25]
	out[26] = halfBtf(cospi[48], step[26], -cospi[16], step[21], cosBit)
	out[27] = halfBtf(cospi[48], step[27], -cospi[16], step[20], cosBit)
	out[28] = halfBtf(cospi[16], step[28], cospi[48], step[19], cosBit)
	out[29] = halfBtf(cospi[16], step[29], cospi[48], step[18], cosBit)
	out[30] = step[30]
	out[31] = step[31]
	out[32] = step[32] + step[39]
	out[33] = step[33] + step[38]
	out[34] = step[34] + step[37]
	out[35] = step[35] + step[36]
	out[36] = -step[36] + step[35]
	out[37] = -step[37] + step[34]
	out[38] = -step[38] + step[33]
	out[39] = -step[39] + step[32]
	out[40] = -step[40] + step[47]
	out[41] = -step[41] + step[46]
	out[42] = -step[42] + step[45]
	out[43] = -step[43] + step[44]
	out[44] = step[44] + step[43]
	out[45] = step[45] + step[42]
	out[46] = step[46] + step[41]
	out[47] = step[47] + step[40]
	out[48] = step[48] + step[55]
	out[49] = step[49] + step[54]
	out[50] = step[50] + step[53]
	out[51] = step[51] + step[52]
	out[52] = -step[52] + step[51]
	out[53] = -step[53] + step[50]
	out[54] = -step[54] + step[49]
	out[55] = -step[55] + step[48]
	out[56] = -step[56] + step[63]
	out[57] = -step[57] + step[62]
	out[58] = -step[58] + step[61]
	out[59] = -step[59] + step[60]
	out[60] = step[60] + step[59]
	out[61] = step[61] + step[58]
	out[62] = step[62] + step[57]
	out[63] = step[63] + step[56]
	step[0] = halfBtf(cospi[32], out[0], cospi[32], out[1], cosBit)
	step[1] = halfBtf(-cospi[32], out[1], cospi[32], out[0], cosBit)
	step[2] = halfBtf(cospi[48], out[2], cospi[16], out[3], cosBit)
	step[3] = halfBtf(cospi[48], out[3], -cospi[16], out[2], cosBit)
	step[4] = out[4] + out[5]
	step[5] = -out[5] + out[4]
	step[6] = -out[6] + out[7]
	step[7] = out[7] + out[6]
	step[8] = out[8]
	step[9] = halfBtf(-cospi[16], out[9], cospi[48], out[14], cosBit)
	step[10] = halfBtf(-cospi[48], out[10], -cospi[16], out[13], cosBit)
	step[11] = out[11]
	step[12] = out[12]
	step[13] = halfBtf(cospi[48], out[13], -cospi[16], out[10], cosBit)
	step[14] = halfBtf(cospi[16], out[14], cospi[48], out[9], cosBit)
	step[15] = out[15]
	step[16] = out[16] + out[19]
	step[17] = out[17] + out[18]
	step[18] = -out[18] + out[17]
	step[19] = -out[19] + out[16]
	step[20] = -out[20] + out[23]
	step[21] = -out[21] + out[22]
	step[22] = out[22] + out[21]
	step[23] = out[23] + out[20]
	step[24] = out[24] + out[27]
	step[25] = out[25] + out[26]
	step[26] = -out[26] + out[25]
	step[27] = -out[27] + out[24]
	step[28] = -out[28] + out[31]
	step[29] = -out[29] + out[30]
	step[30] = out[30] + out[29]
	step[31] = out[31] + out[28]
	step[32] = out[32]
	step[33] = out[33]
	step[34] = halfBtf(-cospi[8], out[34], cospi[56], out[61], cosBit)
	step[35] = halfBtf(-cospi[8], out[35], cospi[56], out[60], cosBit)
	step[36] = halfBtf(-cospi[56], out[36], -cospi[8], out[59], cosBit)
	step[37] = halfBtf(-cospi[56], out[37], -cospi[8], out[58], cosBit)
	step[38] = out[38]
	step[39] = out[39]
	step[40] = out[40]
	step[41] = out[41]
	step[42] = halfBtf(-cospi[40], out[42], cospi[24], out[53], cosBit)
	step[43] = halfBtf(-cospi[40], out[43], cospi[24], out[52], cosBit)
	step[44] = halfBtf(-cospi[24], out[44], -cospi[40], out[51], cosBit)
	step[45] = halfBtf(-cospi[24], out[45], -cospi[40], out[50], cosBit)
	step[46] = out[46]
	step[47] = out[47]
	step[48] = out[48]
	step[49] = out[49]
	step[50] = halfBtf(cospi[24], out[50], -cospi[40], out[45], cosBit)
	step[51] = halfBtf(cospi[24], out[51], -cospi[40], out[44], cosBit)
	step[52] = halfBtf(cospi[40], out[52], cospi[24], out[43], cosBit)
	step[53] = halfBtf(cospi[40], out[53], cospi[24], out[42], cosBit)
	step[54] = out[54]
	step[55] = out[55]
	step[56] = out[56]
	step[57] = out[57]
	step[58] = halfBtf(cospi[56], out[58], -cospi[8], out[37], cosBit)
	step[59] = halfBtf(cospi[56], out[59], -cospi[8], out[36], cosBit)
	step[60] = halfBtf(cospi[8], out[60], cospi[56], out[35], cosBit)
	step[61] = halfBtf(cospi[8], out[61], cospi[56], out[34], cosBit)
	step[62] = out[62]
	step[63] = out[63]
	out[0] = step[0]
	out[1] = step[1]
	out[2] = step[2]
	out[3] = step[3]
	out[4] = halfBtf(cospi[56], step[4], cospi[8], step[7], cosBit)
	out[5] = halfBtf(cospi[24], step[5], cospi[40], step[6], cosBit)
	out[6] = halfBtf(cospi[24], step[6], -cospi[40], step[5], cosBit)
	out[7] = halfBtf(cospi[56], step[7], -cospi[8], step[4], cosBit)
	out[8] = step[8] + step[9]
	out[9] = -step[9] + step[8]
	out[10] = -step[10] + step[11]
	out[11] = step[11] + step[10]
	out[12] = step[12] + step[13]
	out[13] = -step[13] + step[12]
	out[14] = -step[14] + step[15]
	out[15] = step[15] + step[14]
	out[16] = step[16]
	out[17] = halfBtf(-cospi[8], step[17], cospi[56], step[30], cosBit)
	out[18] = halfBtf(-cospi[56], step[18], -cospi[8], step[29], cosBit)
	out[19] = step[19]
	out[20] = step[20]
	out[21] = halfBtf(-cospi[40], step[21], cospi[24], step[26], cosBit)
	out[22] = halfBtf(-cospi[24], step[22], -cospi[40], step[25], cosBit)
	out[23] = step[23]
	out[24] = step[24]
	out[25] = halfBtf(cospi[24], step[25], -cospi[40], step[22], cosBit)
	out[26] = halfBtf(cospi[40], step[26], cospi[24], step[21], cosBit)
	out[27] = step[27]
	out[28] = step[28]
	out[29] = halfBtf(cospi[56], step[29], -cospi[8], step[18], cosBit)
	out[30] = halfBtf(cospi[8], step[30], cospi[56], step[17], cosBit)
	out[31] = step[31]
	out[32] = step[32] + step[35]
	out[33] = step[33] + step[34]
	out[34] = -step[34] + step[33]
	out[35] = -step[35] + step[32]
	out[36] = -step[36] + step[39]
	out[37] = -step[37] + step[38]
	out[38] = step[38] + step[37]
	out[39] = step[39] + step[36]
	out[40] = step[40] + step[43]
	out[41] = step[41] + step[42]
	out[42] = -step[42] + step[41]
	out[43] = -step[43] + step[40]
	out[44] = -step[44] + step[47]
	out[45] = -step[45] + step[46]
	out[46] = step[46] + step[45]
	out[47] = step[47] + step[44]
	out[48] = step[48] + step[51]
	out[49] = step[49] + step[50]
	out[50] = -step[50] + step[49]
	out[51] = -step[51] + step[48]
	out[52] = -step[52] + step[55]
	out[53] = -step[53] + step[54]
	out[54] = step[54] + step[53]
	out[55] = step[55] + step[52]
	out[56] = step[56] + step[59]
	out[57] = step[57] + step[58]
	out[58] = -step[58] + step[57]
	out[59] = -step[59] + step[56]
	out[60] = -step[60] + step[63]
	out[61] = -step[61] + step[62]
	out[62] = step[62] + step[61]
	out[63] = step[63] + step[60]
	step[0] = out[0]
	step[1] = out[1]
	step[2] = out[2]
	step[3] = out[3]
	step[4] = out[4]
	step[5] = out[5]
	step[6] = out[6]
	step[7] = out[7]
	step[8] = halfBtf(cospi[60], out[8], cospi[4], out[15], cosBit)
	step[9] = halfBtf(cospi[28], out[9], cospi[36], out[14], cosBit)
	step[10] = halfBtf(cospi[44], out[10], cospi[20], out[13], cosBit)
	step[11] = halfBtf(cospi[12], out[11], cospi[52], out[12], cosBit)
	step[12] = halfBtf(cospi[12], out[12], -cospi[52], out[11], cosBit)
	step[13] = halfBtf(cospi[44], out[13], -cospi[20], out[10], cosBit)
	step[14] = halfBtf(cospi[28], out[14], -cospi[36], out[9], cosBit)
	step[15] = halfBtf(cospi[60], out[15], -cospi[4], out[8], cosBit)
	step[16] = out[16] + out[17]
	step[17] = -out[17] + out[16]
	step[18] = -out[18] + out[19]
	step[19] = out[19] + out[18]
	step[20] = out[20] + out[21]
	step[21] = -out[21] + out[20]
	step[22] = -out[22] + out[23]
	step[23] = out[23] + out[22]
	step[24] = out[24] + out[25]
	step[25] = -out[25] + out[24]
	step[26] = -out[26] + out[27]
	step[27] = out[27] + out[26]
	step[28] = out[28] + out[29]
	step[29] = -out[29] + out[28]
	step[30] = -out[30] + out[31]
	step[31] = out[31] + out[30]
	step[32] = out[32]
	step[33] = halfBtf(-cospi[4], out[33], cospi[60], out[62], cosBit)
	step[34] = halfBtf(-cospi[60], out[34], -cospi[4], out[61], cosBit)
	step[35] = out[35]
	step[36] = out[36]
	step[37] = halfBtf(-cospi[36], out[37], cospi[28], out[58], cosBit)
	step[38] = halfBtf(-cospi[28], out[38], -cospi[36], out[57], cosBit)
	step[39] = out[39]
	step[40] = out[40]
	step[41] = halfBtf(-cospi[20], out[41], cospi[44], out[54], cosBit)
	step[42] = halfBtf(-cospi[44], out[42], -cospi[20], out[53], cosBit)
	step[43] = out[43]
	step[44] = out[44]
	step[45] = halfBtf(-cospi[52], out[45], cospi[12], out[50], cosBit)
	step[46] = halfBtf(-cospi[12], out[46], -cospi[52], out[49], cosBit)
	step[47] = out[47]
	step[48] = out[48]
	step[49] = halfBtf(cospi[12], out[49], -cospi[52], out[46], cosBit)
	step[50] = halfBtf(cospi[52], out[50], cospi[12], out[45], cosBit)
	step[51] = out[51]
	step[52] = out[52]
	step[53] = halfBtf(cospi[44], out[53], -cospi[20], out[42], cosBit)
	step[54] = halfBtf(cospi[20], out[54], cospi[44], out[41], cosBit)
	step[55] = out[55]
	step[56] = out[56]
	step[57] = halfBtf(cospi[28], out[57], -cospi[36], out[38], cosBit)
	step[58] = halfBtf(cospi[36], out[58], cospi[28], out[37], cosBit)
	step[59] = out[59]
	step[60] = out[60]
	step[61] = halfBtf(cospi[60], out[61], -cospi[4], out[34], cosBit)
	step[62] = halfBtf(cospi[4], out[62], cospi[60], out[33], cosBit)
	step[63] = out[63]
	out[0] = step[0]
	out[1] = step[1]
	out[2] = step[2]
	out[3] = step[3]
	out[4] = step[4]
	out[5] = step[5]
	out[6] = step[6]
	out[7] = step[7]
	out[8] = step[8]
	out[9] = step[9]
	out[10] = step[10]
	out[11] = step[11]
	out[12] = step[12]
	out[13] = step[13]
	out[14] = step[14]
	out[15] = step[15]
	out[16] = halfBtf(cospi[62], step[16], cospi[2], step[31], cosBit)
	out[17] = halfBtf(cospi[30], step[17], cospi[34], step[30], cosBit)
	out[18] = halfBtf(cospi[46], step[18], cospi[18], step[29], cosBit)
	out[19] = halfBtf(cospi[14], step[19], cospi[50], step[28], cosBit)
	out[20] = halfBtf(cospi[54], step[20], cospi[10], step[27], cosBit)
	out[21] = halfBtf(cospi[22], step[21], cospi[42], step[26], cosBit)
	out[22] = halfBtf(cospi[38], step[22], cospi[26], step[25], cosBit)
	out[23] = halfBtf(cospi[6], step[23], cospi[58], step[24], cosBit)
	out[24] = halfBtf(cospi[6], step[24], -cospi[58], step[23], cosBit)
	out[25] = halfBtf(cospi[38], step[25], -cospi[26], step[22], cosBit)
	out[26] = halfBtf(cospi[22], step[26], -cospi[42], step[21], cosBit)
	out[27] = halfBtf(cospi[54], step[27], -cospi[10], step[20], cosBit)
	out[28] = halfBtf(cospi[14], step[28], -cospi[50], step[19], cosBit)
	out[29] = halfBtf(cospi[46], step[29], -cospi[18], step[18], cosBit)
	out[30] = halfBtf(cospi[30], step[30], -cospi[34], step[17], cosBit)
	out[31] = halfBtf(cospi[62], step[31], -cospi[2], step[16], cosBit)
	out[32] = step[32] + step[33]
	out[33] = -step[33] + step[32]
	out[34] = -step[34] + step[35]
	out[35] = step[35] + step[34]
	out[36] = step[36] + step[37]
	out[37] = -step[37] + step[36]
	out[38] = -step[38] + step[39]
	out[39] = step[39] + step[38]
	out[40] = step[40] + step[41]
	out[41] = -step[41] + step[40]
	out[42] = -step[42] + step[43]
	out[43] = step[43] + step[42]
	out[44] = step[44] + step[45]
	out[45] = -step[45] + step[44]
	out[46] = -step[46] + step[47]
	out[47] = step[47] + step[46]
	out[48] = step[48] + step[49]
	out[49] = -step[49] + step[48]
	out[50] = -step[50] + step[51]
	out[51] = step[51] + step[50]
	out[52] = step[52] + step[53]
	out[53] = -step[53] + step[52]
	out[54] = -step[54] + step[55]
	out[55] = step[55] + step[54]
	out[56] = step[56] + step[57]
	out[57] = -step[57] + step[56]
	out[58] = -step[58] + step[59]
	out[59] = step[59] + step[58]
	out[60] = step[60] + step[61]
	out[61] = -step[61] + step[60]
	out[62] = -step[62] + step[63]
	out[63] = step[63] + step[62]
	step[0] = out[0]
	step[1] = out[1]
	step[2] = out[2]
	step[3] = out[3]
	step[4] = out[4]
	step[5] = out[5]
	step[6] = out[6]
	step[7] = out[7]
	step[8] = out[8]
	step[9] = out[9]
	step[10] = out[10]
	step[11] = out[11]
	step[12] = out[12]
	step[13] = out[13]
	step[14] = out[14]
	step[15] = out[15]
	step[16] = out[16]
	step[17] = out[17]
	step[18] = out[18]
	step[19] = out[19]
	step[20] = out[20]
	step[21] = out[21]
	step[22] = out[22]
	step[23] = out[23]
	step[24] = out[24]
	step[25] = out[25]
	step[26] = out[26]
	step[27] = out[27]
	step[28] = out[28]
	step[29] = out[29]
	step[30] = out[30]
	step[31] = out[31]
	step[32] = halfBtf(cospi[63], out[32], cospi[1], out[63], cosBit)
	step[33] = halfBtf(cospi[31], out[33], cospi[33], out[62], cosBit)
	step[34] = halfBtf(cospi[47], out[34], cospi[17], out[61], cosBit)
	step[35] = halfBtf(cospi[15], out[35], cospi[49], out[60], cosBit)
	step[36] = halfBtf(cospi[55], out[36], cospi[9], out[59], cosBit)
	step[37] = halfBtf(cospi[23], out[37], cospi[41], out[58], cosBit)
	step[38] = halfBtf(cospi[39], out[38], cospi[25], out[57], cosBit)
	step[39] = halfBtf(cospi[7], out[39], cospi[57], out[56], cosBit)
	step[40] = halfBtf(cospi[59], out[40], cospi[5], out[55], cosBit)
	step[41] = halfBtf(cospi[27], out[41], cospi[37], out[54], cosBit)
	step[42] = halfBtf(cospi[43], out[42], cospi[21], out[53], cosBit)
	step[43] = halfBtf(cospi[11], out[43], cospi[53], out[52], cosBit)
	step[44] = halfBtf(cospi[51], out[44], cospi[13], out[51], cosBit)
	step[45] = halfBtf(cospi[19], out[45], cospi[45], out[50], cosBit)
	step[46] = halfBtf(cospi[35], out[46], cospi[29], out[49], cosBit)
	step[47] = halfBtf(cospi[3], out[47], cospi[61], out[48], cosBit)
	step[48] = halfBtf(cospi[3], out[48], -cospi[61], out[47], cosBit)
	step[49] = halfBtf(cospi[35], out[49], -cospi[29], out[46], cosBit)
	step[50] = halfBtf(cospi[19], out[50], -cospi[45], out[45], cosBit)
	step[51] = halfBtf(cospi[51], out[51], -cospi[13], out[44], cosBit)
	step[52] = halfBtf(cospi[11], out[52], -cospi[53], out[43], cosBit)
	step[53] = halfBtf(cospi[43], out[53], -cospi[21], out[42], cosBit)
	step[54] = halfBtf(cospi[27], out[54], -cospi[37], out[41], cosBit)
	step[55] = halfBtf(cospi[59], out[55], -cospi[5], out[40], cosBit)
	step[56] = halfBtf(cospi[7], out[56], -cospi[57], out[39], cosBit)
	step[57] = halfBtf(cospi[39], out[57], -cospi[25], out[38], cosBit)
	step[58] = halfBtf(cospi[23], out[58], -cospi[41], out[37], cosBit)
	step[59] = halfBtf(cospi[55], out[59], -cospi[9], out[36], cosBit)
	step[60] = halfBtf(cospi[15], out[60], -cospi[49], out[35], cosBit)
	step[61] = halfBtf(cospi[47], out[61], -cospi[17], out[34], cosBit)
	step[62] = halfBtf(cospi[31], out[62], -cospi[33], out[33], cosBit)
	step[63] = halfBtf(cospi[63], out[63], -cospi[1], out[32], cosBit)
	out[0] = step[0]
	out[1] = step[32]
	out[2] = step[16]
	out[3] = step[48]
	out[4] = step[8]
	out[5] = step[40]
	out[6] = step[24]
	out[7] = step[56]
	out[8] = step[4]
	out[9] = step[36]
	out[10] = step[20]
	out[11] = step[52]
	out[12] = step[12]
	out[13] = step[44]
	out[14] = step[28]
	out[15] = step[60]
	out[16] = step[2]
	out[17] = step[34]
	out[18] = step[18]
	out[19] = step[50]
	out[20] = step[10]
	out[21] = step[42]
	out[22] = step[26]
	out[23] = step[58]
	out[24] = step[6]
	out[25] = step[38]
	out[26] = step[22]
	out[27] = step[54]
	out[28] = step[14]
	out[29] = step[46]
	out[30] = step[30]
	out[31] = step[62]
	out[32] = step[1]
	out[33] = step[33]
	out[34] = step[17]
	out[35] = step[49]
	out[36] = step[9]
	out[37] = step[41]
	out[38] = step[25]
	out[39] = step[57]
	out[40] = step[5]
	out[41] = step[37]
	out[42] = step[21]
	out[43] = step[53]
	out[44] = step[13]
	out[45] = step[45]
	out[46] = step[29]
	out[47] = step[61]
	out[48] = step[3]
	out[49] = step[35]
	out[50] = step[19]
	out[51] = step[51]
	out[52] = step[11]
	out[53] = step[43]
	out[54] = step[27]
	out[55] = step[59]
	out[56] = step[7]
	out[57] = step[39]
	out[58] = step[23]
	out[59] = step[55]
	out[60] = step[15]
	out[61] = step[47]
	out[62] = step[31]
	out[63] = step[63]
}

func fadst8(in, out *[64]int32, cosBit int) {
	cospi := cospiArr(cosBit)
	var step [8]int32
	out[0] = in[0]
	out[1] = -in[7]
	out[2] = -in[3]
	out[3] = in[4]
	out[4] = -in[1]
	out[5] = in[6]
	out[6] = in[2]
	out[7] = -in[5]
	step[0] = out[0]
	step[1] = out[1]
	step[2] = halfBtf(cospi[32], out[2], cospi[32], out[3], cosBit)
	step[3] = halfBtf(cospi[32], out[2], -cospi[32], out[3], cosBit)
	step[4] = out[4]
	step[5] = out[5]
	step[6] = halfBtf(cospi[32], out[6], cospi[32], out[7], cosBit)
	step[7] = halfBtf(cospi[32], out[6], -cospi[32], out[7], cosBit)
	out[0] = step[0] + step[2]
	out[1] = step[1] + step[3]
	out[2] = step[0] - step[2]
	out[3] = step[1] - step[3]
	out[4] = step[4] + step[6]
	out[5] = step[5] + step[7]
	out[6] = step[4] - step[6]
	out[7] = step[5] - step[7]
	step[0] = out[0]
	step[1] = out[1]
	step[2] = out[2]
	step[3] = out[3]
	step[4] = halfBtf(cospi[16], out[4], cospi[48], out[5], cosBit)
	step[5] = halfBtf(cospi[48], out[4], -cospi[16], out[5], cosBit)
	step[6] = halfBtf(-cospi[48], out[6], cospi[16], out[7], cosBit)
	step[7] = halfBtf(cospi[16], out[6], cospi[48], out[7], cosBit)
	out[0] = step[0] + step[4]
	out[1] = step[1] + step[5]
	out[2] = step[2] + step[6]
	out[3] = step[3] + step[7]
	out[4] = step[0] - step[4]
	out[5] = step[1] - step[5]
	out[6] = step[2] - step[6]
	out[7] = step[3] - step[7]
	step[0] = halfBtf(cospi[4], out[0], cospi[60], out[1], cosBit)
	step[1] = halfBtf(cospi[60], out[0], -cospi[4], out[1], cosBit)
	step[2] = halfBtf(cospi[20], out[2], cospi[44], out[3], cosBit)
	step[3] = halfBtf(cospi[44], out[2], -cospi[20], out[3], cosBit)
	step[4] = halfBtf(cospi[36], out[4], cospi[28], out[5], cosBit)
	step[5] = halfBtf(cospi[28], out[4], -cospi[36], out[5], cosBit)
	step[6] = halfBtf(cospi[52], out[6], cospi[12], out[7], cosBit)
	step[7] = halfBtf(cospi[12], out[6], -cospi[52], out[7], cosBit)
	out[0] = step[1]
	out[1] = step[6]
	out[2] = step[3]
	out[3] = step[4]
	out[4] = step[5]
	out[5] = step[2]
	out[6] = step[7]
	out[7] = step[0]
}

func fadst16(in, out *[64]int32, cosBit int) {
	cospi := cospiArr(cosBit)
	var step [16]int32
	out[0] = in[0]
	out[1] = -in[15]
	out[2] = -in[7]
	out[3] = in[8]
	out[4] = -in[3]
	out[5] = in[12]
	out[6] = in[4]
	out[7] = -in[11]
	out[8] = -in[1]
	out[9] = in[14]
	out[10] = in[6]
	out[11] = -in[9]
	out[12] = in[2]
	out[13] = -in[13]
	out[14] = -in[5]
	out[15] = in[10]
	step[0] = out[0]
	step[1] = out[1]
	step[2] = halfBtf(cospi[32], out[2], cospi[32], out[3], cosBit)
	step[3] = halfBtf(cospi[32], out[2], -cospi[32], out[3], cosBit)
	step[4] = out[4]
	step[5] = out[5]
	step[6] = halfBtf(cospi[32], out[6], cospi[32], out[7], cosBit)
	step[7] = halfBtf(cospi[32], out[6], -cospi[32], out[7], cosBit)
	step[8] = out[8]
	step[9] = out[9]
	step[10] = halfBtf(cospi[32], out[10], cospi[32], out[11], cosBit)
	step[11] = halfBtf(cospi[32], out[10], -cospi[32], out[11], cosBit)
	step[12] = out[12]
	step[13] = out[13]
	step[14] = halfBtf(cospi[32], out[14], cospi[32], out[15], cosBit)
	step[15] = halfBtf(cospi[32], out[14], -cospi[32], out[15], cosBit)
	out[0] = step[0] + step[2]
	out[1] = step[1] + step[3]
	out[2] = step[0] - step[2]
	out[3] = step[1] - step[3]
	out[4] = step[4] + step[6]
	out[5] = step[5] + step[7]
	out[6] = step[4] - step[6]
	out[7] = step[5] - step[7]
	out[8] = step[8] + step[10]
	out[9] = step[9] + step[11]
	out[10] = step[8] - step[10]
	out[11] = step[9] - step[11]
	out[12] = step[12] + step[14]
	out[13] = step[13] + step[15]
	out[14] = step[12] - step[14]
	out[15] = step[13] - step[15]
	step[0] = out[0]
	step[1] = out[1]
	step[2] = out[2]
	step[3] = out[3]
	step[4] = halfBtf(cospi[16], out[4], cospi[48], out[5], cosBit)
	step[5] = halfBtf(cospi[48], out[4], -cospi[16], out[5], cosBit)
	step[6] = halfBtf(-cospi[48], out[6], cospi[16], out[7], cosBit)
	step[7] = halfBtf(cospi[16], out[6], cospi[48], out[7], cosBit)
	step[8] = out[8]
	step[9] = out[9]
	step[10] = out[10]
	step[11] = out[11]
	step[12] = halfBtf(cospi[16], out[12], cospi[48], out[13], cosBit)
	step[13] = halfBtf(cospi[48], out[12], -cospi[16], out[13], cosBit)
	step[14] = halfBtf(-cospi[48], out[14], cospi[16], out[15], cosBit)
	step[15] = halfBtf(cospi[16], out[14], cospi[48], out[15], cosBit)
	out[0] = step[0] + step[4]
	out[1] = step[1] + step[5]
	out[2] = step[2] + step[6]
	out[3] = step[3] + step[7]
	out[4] = step[0] - step[4]
	out[5] = step[1] - step[5]
	out[6] = step[2] - step[6]
	out[7] = step[3] - step[7]
	out[8] = step[8] + step[12]
	out[9] = step[9] + step[13]
	out[10] = step[10] + step[14]
	out[11] = step[11] + step[15]
	out[12] = step[8] - step[12]
	out[13] = step[9] - step[13]
	out[14] = step[10] - step[14]
	out[15] = step[11] - step[15]
	step[0] = out[0]
	step[1] = out[1]
	step[2] = out[2]
	step[3] = out[3]
	step[4] = out[4]
	step[5] = out[5]
	step[6] = out[6]
	step[7] = out[7]
	step[8] = halfBtf(cospi[8], out[8], cospi[56], out[9], cosBit)
	step[9] = halfBtf(cospi[56], out[8], -cospi[8], out[9], cosBit)
	step[10] = halfBtf(cospi[40], out[10], cospi[24], out[11], cosBit)
	step[11] = halfBtf(cospi[24], out[10], -cospi[40], out[11], cosBit)
	step[12] = halfBtf(-cospi[56], out[12], cospi[8], out[13], cosBit)
	step[13] = halfBtf(cospi[8], out[12], cospi[56], out[13], cosBit)
	step[14] = halfBtf(-cospi[24], out[14], cospi[40], out[15], cosBit)
	step[15] = halfBtf(cospi[40], out[14], cospi[24], out[15], cosBit)
	out[0] = step[0] + step[8]
	out[1] = step[1] + step[9]
	out[2] = step[2] + step[10]
	out[3] = step[3] + step[11]
	out[4] = step[4] + step[12]
	out[5] = step[5] + step[13]
	out[6] = step[6] + step[14]
	out[7] = step[7] + step[15]
	out[8] = step[0] - step[8]
	out[9] = step[1] - step[9]
	out[10] = step[2] - step[10]
	out[11] = step[3] - step[11]
	out[12] = step[4] - step[12]
	out[13] = step[5] - step[13]
	out[14] = step[6] - step[14]
	out[15] = step[7] - step[15]
	step[0] = halfBtf(cospi[2], out[0], cospi[62], out[1], cosBit)
	step[1] = halfBtf(cospi[62], out[0], -cospi[2], out[1], cosBit)
	step[2] = halfBtf(cospi[10], out[2], cospi[54], out[3], cosBit)
	step[3] = halfBtf(cospi[54], out[2], -cospi[10], out[3], cosBit)
	step[4] = halfBtf(cospi[18], out[4], cospi[46], out[5], cosBit)
	step[5] = halfBtf(cospi[46], out[4], -cospi[18], out[5], cosBit)
	step[6] = halfBtf(cospi[26], out[6], cospi[38], out[7], cosBit)
	step[7] = halfBtf(cospi[38], out[6], -cospi[26], out[7], cosBit)
	step[8] = halfBtf(cospi[34], out[8], cospi[30], out[9], cosBit)
	step[9] = halfBtf(cospi[30], out[8], -cospi[34], out[9], cosBit)
	step[10] = halfBtf(cospi[42], out[10], cospi[22], out[11], cosBit)
	step[11] = halfBtf(cospi[22], out[10], -cospi[42], out[11], cosBit)
	step[12] = halfBtf(cospi[50], out[12], cospi[14], out[13], cosBit)
	step[13] = halfBtf(cospi[14], out[12], -cospi[50], out[13], cosBit)
	step[14] = halfBtf(cospi[58], out[14], cospi[6], out[15], cosBit)
	step[15] = halfBtf(cospi[6], out[14], -cospi[58], out[15], cosBit)
	out[0] = step[1]
	out[1] = step[14]
	out[2] = step[3]
	out[3] = step[12]
	out[4] = step[5]
	out[5] = step[10]
	out[6] = step[7]
	out[7] = step[8]
	out[8] = step[9]
	out[9] = step[6]
	out[10] = step[11]
	out[11] = step[4]
	out[12] = step[13]
	out[13] = step[2]
	out[14] = step[15]
	out[15] = step[0]
}
