//go:build amd64 && !purego

package hevc

import "unsafe"

//go:noescape
func sadAVX2(a *uint16, as int, b *uint16, bs int, w, h int) int

//go:noescape
func satd8AVX2(a *uint16, as int, b *uint16, bs int) int

//go:noescape
func satd16x8AVX2(a *uint16, as int, b *uint16, bs int) uint64

// satd16x8 is satd8 of the two 8x8 blocks of a 16x8 region, summed; it
// takes samples of at most 10 bits.
func satd16x8(a []uint16, as int, b []uint16, bs int) int {
	if !useAVX2 {
		return satd8Go(a, as, b, bs) + satd8Go(a[8:], as, b[8:], bs)
	}
	_ = a[7*as+15]
	_ = b[7*bs+15]
	v := satd16x8AVX2(&a[0], as, &b[0], bs)
	return int((v&0xffffffff+1)>>1) + int((v>>32+1)>>1)
}

// sadBlock is the sum of absolute differences of two w x h blocks.
func sadBlock(a []uint16, as int, b []uint16, bs, w, h int) int {
	if !useAVX2 || w&7 != 0 || h == 0 {
		return sadBlockGo(a, as, b, bs, w, h)
	}
	_ = a[(h-1)*as+w-1]
	_ = b[(h-1)*bs+w-1]
	return sadAVX2(&a[0], as, &b[0], bs, w, h)
}

// satd8 is the sum of the absolute Hadamard transformed differences of
// two 8x8 blocks, halved twice.
func satd8(a []uint16, as int, b []uint16, bs int) int {
	if !useAVX2 {
		return satd8Go(a, as, b, bs)
	}
	_ = a[7*as+7]
	_ = b[7*bs+7]
	return (satd8AVX2(&a[0], as, &b[0], bs) + 2) >> 2
}

//go:noescape
func fdct8ColsAVX2(dst unsafe.Pointer, src *int16, pairs *int32, rnd, shift, wide int)

//go:noescape
func fdctColsAVX2(dst unsafe.Pointer, src *int16, n int, pairs *int32, rnd, shift, wide int, scratch *int16)

// forwardTransformFast is forwardTransform from a residual of words, in
// AVX2 for the 16x16 and 32x32 transforms (false when it does not take
// the size).
func forwardTransformFast(r []int16, c []int32, log2Size, depth int, scratch *transformScratch) bool {
	if !useAVX2 || log2Size < 3 {
		return false
	}
	n := 1 << log2Size
	shift1 := log2Size + depth - 9
	shift2 := log2Size + 6
	if n == 8 {
		pairs := &fdctPairs8
		t := scratch.t[:64]
		u := scratch.u[:64]
		transpose16(t, r[:64], 8)
		fdct8ColsAVX2(unsafe.Pointer(&u[0]), &t[0], &pairs[0], 1<<(shift1-1), shift1, 0)
		transpose16(t, u, 8)
		fdct8ColsAVX2(unsafe.Pointer(&c[0]), &t[0], &pairs[0], 1<<(shift2-1), shift2, 1)
		return true
	}
	pairs := &fdctPairs[log2Size-4]
	t := scratch.t[:n*n]
	u := scratch.u[:n*n]
	transpose16(t, r[:n*n], n)
	fdctColsAVX2(unsafe.Pointer(&u[0]), &t[0], n, &pairs[0], 1<<(shift1-1), shift1, 0, &scratch.pairs[0])
	transpose16(t, u, n)
	fdctColsAVX2(unsafe.Pointer(&c[0]), &t[0], n, &pairs[0], 1<<(shift2-1), shift2, 1, &scratch.pairs[0])
	return true
}

//go:noescape
func quantAVX2(c *int32, n int, scale, add, qbits int) int

// quantFast is quantize in AVX2, false when it does not take the block.
func quantFast(q *quantizer, c []int32) (int, bool) {
	if !useAVX2 || len(c)&7 != 0 || len(c) == 0 || q.qBits > 31 || q.add >= 1<<31 {
		return 0, false
	}
	return quantAVX2(&c[0], len(c), int(q.scale), int(q.add), int(q.qBits)), true
}

// idctPairs holds the inverse transforms' matrices (the transposes) as the
// kernels take them, for 8, 16 and 32 points.
var idctPairs8 [8 * 4]int32
var idctPairs [2][32 * 16]int32

func init() {
	for i, n := range []int{8, 16, 32} {
		step := 32 / n
		for j := range n {
			for p := range n / 2 {
				lo := uint16(int16(transMatrix[2*p*step][j]))     //nolint:gosec // a matrix coefficient
				hi := uint16(int16(transMatrix[(2*p+1)*step][j])) //nolint:gosec // a matrix coefficient
				v := int32(uint32(lo) | uint32(hi)<<16)           //nolint:gosec // packed words
				if i == 0 {
					idctPairs8[j*4+p] = v
				} else {
					idctPairs[i-1][j*n/2+p] = v
				}
			}
		}
	}
}

// inverseTransformFast is inverseTransform for the 8, 16 and 32 point
// DCTs in AVX2, false when it does not take the block.
func inverseTransformFast(c []int32, log2Size, depth int) bool {
	if !useAVX2 || log2Size < 3 {
		return false
	}
	n := 1 << log2Size
	var a, b [32 * 32]int16
	var w [32 * 32]int32
	var scratch [32 * 32]int16
	for i, v := range c[:n*n] {
		a[i] = int16(v) //nolint:gosec // scaled coefficients are clipped to 16 bits
	}
	bd := 20 - depth
	cols := func(dst unsafe.Pointer, src *int16, rnd, shift, wide int) {
		if n == 8 {
			fdct8ColsAVX2(dst, src, &idctPairs8[0], rnd, shift, wide)
		} else {
			fdctColsAVX2(dst, src, n, &idctPairs[log2Size-4][0], rnd, shift, wide, &scratch[0])
		}
	}
	// Columns, clipped to 16 bits; then rows, by the columns of the
	// transpose.
	cols(unsafe.Pointer(&b[0]), &a[0], 64, 7, 0)
	transpose16(a[:n*n], b[:n*n], n)
	cols(unsafe.Pointer(&w[0]), &a[0], 1<<(bd-1), bd, 1)
	for y := range n {
		for x := range n {
			c[y*n+x] = w[x*n+y]
		}
	}
	return true
}

//go:noescape
func ssdAVX2(a *uint16, as int, b *uint16, bs int, w, h int) int

// ssdBlock is the sum of squared differences of two w x h blocks of
// samples of at most 10 bits.
func ssdBlock(a []uint16, as int, b []uint16, bs, w, h int) int64 {
	if !useAVX2 || w&15 != 0 || h == 0 {
		return ssdBlockGo(a, as, b, bs, w, h)
	}
	_ = a[(h-1)*as+w-1]
	_ = b[(h-1)*bs+w-1]
	return int64(ssdAVX2(&a[0], as, &b[0], bs, w, h))
}

//go:noescape
func avgAVX2(dst *uint16, ds int, a *uint16, as int, b *uint16, bs int, w, h int)

// avgBlock writes the rounded average of two w x h blocks.
func avgBlock(dst []uint16, ds int, a []uint16, as int, b []uint16, bs, w, h int) {
	if !useAVX2 || w&7 != 0 || h == 0 {
		avgBlockGo(dst, ds, a, as, b, bs, w, h)
		return
	}
	_ = dst[(h-1)*ds+w-1]
	_ = a[(h-1)*as+w-1]
	_ = b[(h-1)*bs+w-1]
	avgAVX2(&dst[0], ds, &a[0], as, &b[0], bs, w, h)
}
