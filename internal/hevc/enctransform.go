package hevc

// The encoder's forward transforms and quantisation. Neither is normative:
// these follow the reference software, whose transforms are the matrices
// of 8.6.4.2 applied with its rounding, and whose quantiser rounds with a
// dead zone.

// transformScratch is the scratch of the fast forward transform.
type transformScratch struct {
	t, u  [32 * 32]int16
	pairs [32 * 32]int16
}

// fdctPairs holds the 16- and 32-point transform matrices as the fast
// transform takes them: T[k][2p] and T[k][2p+1] the low and high words of
// dword k*n/2 + p.
var fdctPairs [2][32 * 16]int32

// fdctPairs8 is the 8-point matrix the same way.
var fdctPairs8 [8 * 4]int32

func init() {
	for k := range 8 {
		for p := range 4 {
			lo := uint16(int16(transMatrix[k*4][2*p]))             //nolint:gosec // a matrix coefficient
			hi := uint16(int16(transMatrix[k*4][2*p+1]))           //nolint:gosec // a matrix coefficient
			fdctPairs8[k*4+p] = int32(uint32(lo) | uint32(hi)<<16) //nolint:gosec // packed words
		}
	}
	for i, n := range []int{16, 32} {
		step := 32 / n
		for k := range n {
			for p := range n / 2 {
				lo := uint16(int16(transMatrix[k*step][2*p]))              //nolint:gosec // a matrix coefficient
				hi := uint16(int16(transMatrix[k*step][2*p+1]))            //nolint:gosec // a matrix coefficient
				fdctPairs[i][k*n/2+p] = int32(uint32(lo) | uint32(hi)<<16) //nolint:gosec // packed words
			}
		}
	}
}

// transpose16 writes the transpose of the n x n matrix src to dst.
func transpose16(dst, src []int16, n int) {
	for y := range n {
		row := src[y*n : y*n+n]
		for x, v := range row {
			dst[x*n+y] = v
		}
	}
}

// forwardTransform turns the residual r (row major, n = 1 << log2Size
// square) into coefficients in c: rows, then columns, each stage rounded
// to the dynamic range the standard's inverse transform assumes.
func forwardTransform(r []int32, c []int32, log2Size, depth int, dst bool) {
	n := 1 << log2Size
	shift1 := uint(log2Size + depth - 9)
	shift2 := uint(log2Size + 6)
	var tmp [32 * 32]int32
	var in, out [32]int32
	add1 := int32(1) << (shift1 - 1)
	for y := range n {
		copy(in[:n], r[y*n:y*n+n])
		forward1D(in[:n], out[:n], dst)
		for k := range n {
			// The first stage's output saturated to 16 bits, as the
			// fast transform's (and x265's) keeps it.
			tmp[k*n+y] = min(max((out[k]+add1)>>shift1, -32768), 32767) // transposed
		}
	}
	add2 := int32(1) << (shift2 - 1)
	for x := range n {
		copy(in[:n], tmp[x*n:x*n+n])
		forward1D(in[:n], out[:n], dst)
		for k := range n {
			c[k*n+x] = (out[k] + add2) >> shift2
		}
	}
}

// oddCoef holds, per size n = 16 and 32, the odd rows of the transform
// matrix for that size flat: coefficient j of odd output k at
// [k*n/2 + j].
var oddCoef [2][]int32

func init() {
	for i, n := range []int{16, 32} {
		half, step := n/2, 32/n
		m := make([]int32, half*half)
		for k := range half {
			for j := range half {
				m[k*half+j] = int32(transMatrix[(2*k+1)*step][j])
			}
		}
		oddCoef[i] = m
	}
}

// forward1D is the one-dimensional forward transform of in into out: the
// matrix product with the standard's matrix (its rows for the size), by
// even and odd halves (the reference software's partial butterflies).
func forward1D(in, out []int32, dst bool) {
	n := len(in)
	if dst {
		for k := range 4 {
			var sum int32
			for j := range 4 {
				sum += int32(dstMatrix[k][j]) * in[j]
			}
			out[k] = sum
		}
		return
	}
	switch n {
	case 4:
		e0, e1 := in[0]+in[3], in[1]+in[2]
		o0, o1 := in[0]-in[3], in[1]-in[2]
		out[0] = 64*e0 + 64*e1
		out[2] = 64*e0 - 64*e1
		out[1] = 83*o0 + 36*o1
		out[3] = 36*o0 - 83*o1
		return
	case 8:
		e0, e1, e2, e3 := in[0]+in[7], in[1]+in[6], in[2]+in[5], in[3]+in[4]
		o0, o1, o2, o3 := in[0]-in[7], in[1]-in[6], in[2]-in[5], in[3]-in[4]
		ee0, ee1 := e0+e3, e1+e2
		eo0, eo1 := e0-e3, e1-e2
		out[0] = 64*ee0 + 64*ee1
		out[4] = 64*ee0 - 64*ee1
		out[2] = 83*eo0 + 36*eo1
		out[6] = 36*eo0 - 83*eo1
		out[1] = 89*o0 + 75*o1 + 50*o2 + 18*o3
		out[3] = 75*o0 - 18*o1 - 89*o2 - 50*o3
		out[5] = 50*o0 - 89*o1 + 18*o2 + 75*o3
		out[7] = 18*o0 - 50*o1 + 75*o2 - 89*o3
		return
	}
	half := n / 2
	var even, odd, evenOut [16]int32
	for j := range half {
		even[j] = in[j] + in[n-1-j]
		odd[j] = in[j] - in[n-1-j]
	}
	forward1D(even[:half], evenOut[:half], false)
	m := oddCoef[0]
	if n == 32 {
		m = oddCoef[1]
	}
	o := odd[:half]
	for k := range half {
		out[2*k] = evenOut[k]
		row := m[k*half : k*half+half]
		var sum int32
		for j, c := range row {
			sum += c * o[j]
		}
		out[2*k+1] = sum
	}
}

// quantScales is the quantiser's scale by qp%6: 2^14 * 16 / (16 *
// levelScale), the inverse of the standard's scaling.
var quantScales = [6]int32{26214, 23302, 20560, 18396, 16384, 14564}

// quantizer quantises a block's coefficients at one QP.
type quantizer struct {
	scale int64
	qBits uint
	add   int64
}

// newQuantizer sets up quantisation at qp (with the bit depth offset
// already added) for a transform of log2Size at depth; intra rounds
// closer to the next level than inter.
func newQuantizer(qp, log2Size, depth int, intra bool) quantizer {
	transformShift := 15 - depth - log2Size
	qBits := uint(14 + qp/6 + transformShift)
	offset := int64(85)
	if intra {
		offset = 171
	}
	return quantizer{scale: int64(quantScales[qp%6]), qBits: qBits, add: offset << (qBits - 9)}
}

// level quantises one coefficient.
func (q *quantizer) level(c int32) int32 {
	a := int64(c)
	neg := a < 0
	if neg {
		a = -a
	}
	l := (a*q.scale + q.add) >> q.qBits
	l = min(l, 32767)
	if neg {
		return int32(-l)
	}
	return int32(l)
}

// quantize quantises c (n*n coefficients) in place, returning the number
// of non-zero levels.
func (q *quantizer) quantize(c []int32) int {
	if nz, ok := quantFast(q, c); ok {
		return nz
	}
	return q.quantizeGo(c)
}

func (q *quantizer) quantizeGo(c []int32) int {
	nz := 0
	for i, v := range c {
		if v == 0 {
			continue
		}
		l := q.level(v)
		c[i] = l
		if l != 0 {
			nz++
		}
	}
	return nz
}
