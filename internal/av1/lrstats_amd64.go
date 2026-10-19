//go:build amd64 && !purego

package av1

//go:noescape
func gramAVX2(y *int16, n, k int, gm *int64)

// gram is gramGo with AVX2 where n is a multiple of 16.
func gram(y []int16, n, k int, g *[17 * 17]int64) {
	if !useAVX2 || n == 0 || n&15 != 0 {
		gramGo(y, n, k, g)
		return
	}
	_ = y[k*n-1]
	gramAVX2(&y[0], n, k, &g[0])
}

//go:noescape
func dot32AVX2(a, b *int32, n int) int64

// dot32 is the sum of a[i]*b[i], as 64 bits.
func dot32(a, b []int32) int64 {
	b = b[:len(a)]
	n := 0
	var s int64
	if useAVX2 && len(a) >= 8 {
		n = len(a) &^ 7
		s = dot32AVX2(&a[0], &b[0], n)
	}
	return s + dot32Go(a[n:], b[n:])
}
