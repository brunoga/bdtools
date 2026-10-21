package nn

import "testing"

// A DINOv2 projection's product: 2443 tokens, 384 in, 1152 out.
func BenchmarkSgemmQKV(b *testing.B) {
	m, k, n := 2443, 384, 1152
	a, w, c := make([]float32, m*k), make([]float32, n*k), make([]float32, m*n)
	for i := range a {
		a[i] = float32(i % 7)
	}
	for i := range w {
		w[i] = float32(i % 5)
	}
	b.SetBytes(int64(2 * m * k * n)) // flops, as "bytes"
	for range b.N {
		gemmNT(c, a, w, m, k, n)
	}
}
