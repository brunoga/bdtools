package nn

import (
	"math"
	"math/rand/v2"
	"testing"
)

// sgemm against the plain triple loop: shapes with and without edge
// tiles, more than one block of k, both operand layouts, the Go kernel too.
func TestSgemmMatches(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 7))
	for _, avx := range []bool{true, false} {
		old := useAVX2
		useAVX2 = useAVX2 && avx
		for _, s := range [][3]int{{1, 1, 1}, {6, 16, 7}, {7, 17, 5}, {13, 33, 300}, {96, 64, 513}, {100, 5, 260}} {
			m, n, k := s[0], s[1], s[2]
			a, b := make([]float32, m*k), make([]float32, k*n)
			for i := range a {
				a[i] = rng.Float32()*2 - 1
			}
			for i := range b {
				b[i] = rng.Float32()*2 - 1
			}
			want := make([]float64, m*n)
			for i := range m {
				for j := range n {
					for p := range k {
						want[i*n+j] += float64(a[i*k+p]) * float64(b[p*n+j])
					}
				}
			}
			bt := make([]float32, n*k) // b transposed, for gemmNT
			for p := range k {
				for j := range n {
					bt[j*k+p] = b[p*n+j]
				}
			}
			for name, f := range map[string]func(c []float32){
				"NN": func(c []float32) { gemmNN(c, a, b, m, k, n) },
				"NT": func(c []float32) { gemmNT(c, a, bt, m, k, n) },
			} {
				c := make([]float32, m*n)
				for i := range c {
					c[i] = 1 // accumulated into
				}
				f(c)
				for i, w := range want {
					if d := math.Abs(float64(c[i]) - (w + 1)); d > 1e-4*math.Sqrt(float64(k))+1e-5 {
						t.Fatalf("avx %v %s %dx%dx%d: c[%d] = %v, want %v", avx, name, m, n, k, i, c[i], w+1)
					}
				}
			}
		}
		useAVX2 = old
	}
}
