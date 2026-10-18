package av1

// The inverse transform (spec 7.13.2, 7.13.3) and the reconstruction around
// it (7.12.3): dequantization of the coded levels, the 2D inverse transform
// from the dequantized coefficients to the residual, and the addition of the
// residual to the prediction.
//
// A transform block of w x h samples (19 sizes, from 4x4 to 64x64, square and
// rectangular at 2:1 and 4:1) is transformed in two passes: every row by the
// horizontal 1D transform, then every column by the vertical one; a transform
// type names the pair (DCT, ADST, flipped ADST or identity each way, 16
// types), and lossless blocks use the 4x4 Walsh-Hadamard transform instead.
// The 1D DCT and the 8- and 16-point ADSTs are butterfly networks: rotations
// (B, products by Q12 cosines rounded back by 12 bits) and Hadamard steps (H,
// sum and difference clamped to the pass's range). transform_bf.go has them
// as straight-line code, written out (by transform_bf_test.go) from lists of
// those steps built from the spec's step descriptions. Rows are
// clamped to BitDepth+8 bits, the row results are rounded down by a
// size-dependent shift and clamped to Max(BitDepth+6, 16) bits, and the column
// results by 4 more bits. 2:1 rectangles scale their input by 1/sqrt(2); a
// 64-sample dimension carries only its 32 lowest frequencies.
//
// Coefficients are laid out as the spec's Quant and Dequant arrays: row i
// (vertical frequency) and column j (horizontal frequency) at i*tw+j, with tw
// = Min(32, w) and th = Min(32, h) rows. The residual is w*h, row-major, in
// the transform's orientation: the flips of the FLIPADST types are applied
// when it is added to the picture (reconstructAdd), as the spec does.

// Transform kinds in one dimension.
const (
	tx1dDCT = iota
	tx1dADST
	tx1dIdentity
)

// txTypeCol and txTypeRow give the vertical (column) and horizontal (row)
// 1D transform of each transform type; FLIPADST is ADST here, its flip is
// in txFlipUD and txFlipLR.
var txTypeCol = [TX_TYPES]uint8{
	DCT_DCT: tx1dDCT, ADST_DCT: tx1dADST, DCT_ADST: tx1dDCT, ADST_ADST: tx1dADST,
	FLIPADST_DCT: tx1dADST, DCT_FLIPADST: tx1dDCT, FLIPADST_FLIPADST: tx1dADST,
	ADST_FLIPADST: tx1dADST, FLIPADST_ADST: tx1dADST, IDTX: tx1dIdentity,
	V_DCT: tx1dDCT, H_DCT: tx1dIdentity, V_ADST: tx1dADST, H_ADST: tx1dIdentity,
	V_FLIPADST: tx1dADST, H_FLIPADST: tx1dIdentity,
}

var txTypeRow = [TX_TYPES]uint8{
	DCT_DCT: tx1dDCT, ADST_DCT: tx1dDCT, DCT_ADST: tx1dADST, ADST_ADST: tx1dADST,
	FLIPADST_DCT: tx1dDCT, DCT_FLIPADST: tx1dADST, FLIPADST_FLIPADST: tx1dADST,
	ADST_FLIPADST: tx1dADST, FLIPADST_ADST: tx1dADST, IDTX: tx1dIdentity,
	V_DCT: tx1dIdentity, H_DCT: tx1dDCT, V_ADST: tx1dIdentity, H_ADST: tx1dADST,
	V_FLIPADST: tx1dIdentity, H_FLIPADST: tx1dADST,
}

// txFlipUD and txFlipLR say which types flip the residual upside down and
// left to right (spec 7.12.3).
var txFlipUD = [TX_TYPES]bool{FLIPADST_DCT: true, FLIPADST_ADST: true, V_FLIPADST: true, FLIPADST_FLIPADST: true}

var txFlipLR = [TX_TYPES]bool{DCT_FLIPADST: true, ADST_FLIPADST: true, H_FLIPADST: true, FLIPADST_FLIPADST: true}

// txTypeValid reports whether the 1D transforms of txType exist at the sizes
// of txSz: the ADST up to 16 samples, the identity up to 32, the DCT at all.
// (Which of these a bitstream may use is narrower, by the transform sets.)
func txTypeValid(txSz, txType int) bool {
	ok := func(kind uint8, log2 uint8) bool {
		switch kind {
		case tx1dADST:
			return log2 <= 4
		case tx1dIdentity:
			return log2 <= 5
		}
		return true
	}
	return ok(txTypeRow[txType], txWidthLog2[txSz]) && ok(txTypeCol[txType], txHeightLog2[txSz])
}

// iadst4 is the inverse ADST4 process (7.13.2.6), in 64-bit as its
// intermediate values may need r+12 bits. It has no clamping steps; lo and
// hi are there for the common signature.
func iadst4(t *[64]int32, _, _ int32) {
	const (
		sinpi19 = 1321
		sinpi29 = 2482
		sinpi39 = 3344
		sinpi49 = 3803
	)
	t0, t1, t2, t3 := int64(t[0]), int64(t[1]), int64(t[2]), int64(t[3])
	s0 := sinpi19 * t0
	s1 := sinpi29 * t0
	s2 := sinpi39 * t1
	s3 := sinpi49 * t2
	s4 := sinpi19 * t2
	s5 := sinpi29 * t3
	s6 := sinpi49 * t3
	b7 := t0 - t2 + t3

	s0 += s3
	s1 -= s4
	s3 = s2
	s2 = sinpi39 * b7

	s0 += s5
	s1 -= s6

	x0 := s0 + s3
	x1 := s1 + s3
	x2 := s2
	x3 := s0 + s1 - s3

	t[0] = int32((x0 + 2048) >> 12)
	t[1] = int32((x1 + 2048) >> 12)
	t[2] = int32((x2 + 2048) >> 12)
	t[3] = int32((x3 + 2048) >> 12)
}

// iidentity is the inverse identity transform process (7.13.2.15).
func iidentity(t *[64]int32, n int) {
	m := 1 << n
	switch n {
	case 2:
		for i := 0; i < m; i++ {
			t[i] = int32((int64(t[i])*5793 + 2048) >> 12)
		}
	case 3:
		for i := 0; i < m; i++ {
			t[i] *= 2
		}
	case 4:
		for i := 0; i < m; i++ {
			t[i] = int32((int64(t[i])*11586 + 2048) >> 12)
		}
	case 5:
		for i := 0; i < m; i++ {
			t[i] *= 4
		}
	default:
		panic("av1: no identity transform of this size")
	}
}

// iwht is the inverse Walsh-Hadamard transform process (7.13.2.10) of the 4
// samples of t.
func iwht(t *[64]int32, shift uint) {
	a := t[0] >> shift
	c := t[1] >> shift
	d := t[2] >> shift
	b := t[3] >> shift
	a += c
	d -= b
	e := (a - d) >> 1
	b = e - b
	c = e - c
	a -= b
	d += c
	t[0], t[1], t[2], t[3] = a, b, c, d
}

// inverse1D applies the 1D inverse transform kind to 2^n samples of t,
// with intermediate results clamped to r bits.
func inverse1D(t *[64]int32, kind uint8, n, r int) {
	lo, hi := -int32(1)<<(r-1), int32(1)<<(r-1)-1
	// direct calls, not through a table of funcs: through those t escapes
	// to the heap
	switch kind {
	case tx1dDCT:
		switch n {
		case 2:
			idct4(t, lo, hi)
		case 3:
			idct8(t, lo, hi)
		case 4:
			idct16(t, lo, hi)
		case 5:
			idct32(t, lo, hi)
		default:
			idct64(t, lo, hi)
		}
	case tx1dADST:
		switch n {
		case 2:
			iadst4(t, lo, hi)
		case 3:
			iadst8(t, lo, hi)
		case 4:
			iadst16(t, lo, hi)
		default:
			panic("av1: no ADST of this size")
		}
	default:
		iidentity(t, n)
	}
}

// round2 is the spec's Round2 for a signed value and n >= 0.
func round2(x int32, n uint) int32 {
	if n == 0 {
		return x
	}
	return (x + 1<<(n-1)) >> n
}

// inverseTransform is the 2D inverse transform process (7.13.3): from the
// th*tw dequantized coefficients of a txSz block of type txType to its w*h
// residual (in transform orientation, before the flips). lossless selects
// the Walsh-Hadamard transform (txSz must then be TX_4X4).
func inverseTransform(coeffs []int32, txSz, txType, bitDepth int, lossless bool, residual []int32) {
	log2W, log2H := int(txWidthLog2[txSz]), int(txHeightLog2[txSz])
	w, h := 1<<log2W, 1<<log2H
	tw, th := min(w, 32), min(h, 32)
	residual = residual[:w*h]
	coeffs = coeffs[:tw*th]
	rowShift := uint(transformRowShift[txSz])
	colShift := uint(4)
	if lossless {
		rowShift, colShift = 0, 0
	}
	rowRange := bitDepth + 8
	colRange := max(bitDepth+6, 16)
	colLo, colHi := -int32(1)<<(colRange-1), int32(1)<<(colRange-1)-1
	rect := log2W-log2H == 1 || log2H-log2W == 1
	rowKind, colKind := txTypeRow[txType], txTypeCol[txType]

	var t [64]int32
	for i := 0; i < h; i++ {
		out := residual[i*w : i*w+w]
		if i >= th {
			clear(out)
			continue
		}
		in := coeffs[i*tw : i*tw+tw]
		nz := int32(0)
		for _, v := range in {
			nz |= v
		}
		if nz == 0 {
			// Every 1D transform maps zeros to zeros.
			clear(out)
			continue
		}
		copy(t[:tw], in)
		clear(t[tw:w])
		if rect {
			for j := 0; j < tw; j++ {
				t[j] = int32((int64(t[j])*2896 + 2048) >> 12)
			}
		}
		if lossless {
			iwht(&t, 2)
		} else {
			inverse1D(&t, rowKind, log2W, rowRange)
		}
		for j := range out {
			out[j] = min(max(round2(t[j], rowShift), colLo), colHi)
		}
	}
	for j := 0; j < w; j++ {
		for i := 0; i < h; i++ {
			t[i] = residual[i*w+j]
		}
		if lossless {
			iwht(&t, 0)
		} else {
			inverse1D(&t, colKind, log2H, colRange)
		}
		for i := 0; i < h; i++ {
			residual[i*w+j] = round2(t[i], colShift)
		}
	}
}

// dequantize is step 1 of the reconstruction and dequantization process
// (7.12.3) without quantizer matrices: the th*tw levels times the DC or AC
// quantizer (dcQ for the first, acQ for the rest), kept to 24 bits of
// magnitude, divided down for the large sizes and clamped to BitDepth+8
// bits.
func dequantize(quant []int32, txSz int, dcQ, acQ, bitDepth int, out []int32) {
	n := min(int(txWidth[txSz]), 32) * min(int(txHeight[txSz]), 32)
	var denom int64 = 1
	switch txSz {
	case TX_32X32, TX_16X32, TX_32X16, TX_16X64, TX_64X16:
		denom = 2
	case TX_64X64, TX_32X64, TX_64X32:
		denom = 4
	}
	lo, hi := -int64(1)<<(7+bitDepth), int64(1)<<(7+bitDepth)-1
	quant, out = quant[:n], out[:n]
	q := int64(dcQ)
	for i, v := range quant {
		if i == 1 {
			q = int64(acQ)
		}
		if v == 0 {
			out[i] = 0
			continue
		}
		dq := int64(v) * q
		if dq < 0 {
			dq = -((-dq & 0xFFFFFF) / denom)
		} else {
			dq = (dq & 0xFFFFFF) / denom
		}
		out[i] = int32(min(max(dq, lo), hi))
	}
}

// reconstructAdd is step 3 of the reconstruction (7.12.3): it adds the w*h
// residual of a txSz block of type txType to the samples at rec (rows
// stride apart), flipped as the type says, clipped to the sample range.
func reconstructAdd(rec []uint16, stride int, residual []int32, txSz, txType, bitDepth int) {
	w, h := int(txWidth[txSz]), int(txHeight[txSz])
	maxV := int32(1)<<bitDepth - 1
	flipUD, flipLR := txFlipUD[txType], txFlipLR[txType]
	for i := 0; i < h; i++ {
		yy := i
		if flipUD {
			yy = h - 1 - i
		}
		row := rec[yy*stride : yy*stride+w]
		res := residual[i*w : i*w+w]
		if flipLR {
			for j, r := range res {
				p := &row[w-1-j]
				*p = uint16(min(max(int32(*p)+r, 0), maxV))
			}
		} else {
			for j, r := range res {
				p := &row[j]
				*p = uint16(min(max(int32(*p)+r, 0), maxV))
			}
		}
	}
}
