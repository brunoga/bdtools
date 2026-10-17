package aac

import (
	"math"
	"math/cmplx"
)

// mdctN is a forward MDCT of 2n windowed samples to n coefficients,
// X[k] = sum x[i] cos(pi/n (i + 1/2 + n/2)(k + 1/2)), through a DCT-IV of
// size n computed with an n/2-point FFT.
type mdctN struct {
	n         int
	pre, post []complex128
	roots     []complex128
	rev       []int
	z         []complex128
	v, out    []float64
}

func newMDCT(n int) *mdctN {
	m := &mdctN{n: n, pre: make([]complex128, n/2), post: make([]complex128, n/2), roots: make([]complex128, n/4),
		rev: make([]int, n/2), z: make([]complex128, n/2), v: make([]float64, n), out: make([]float64, n)}
	for k := range n / 2 {
		m.pre[k] = cmplx.Exp(complex(0, -math.Pi*float64(k)/float64(n)))
		m.post[k] = cmplx.Exp(complex(0, -math.Pi*(float64(k)+0.25)/float64(n)))
	}
	for k := range n / 4 {
		m.roots[k] = cmplx.Exp(complex(0, -2*math.Pi*float64(k)/float64(n/2)))
	}
	bits := 0
	for 1<<bits < n/2 {
		bits++
	}
	for i := range n / 2 {
		r := 0
		for b := range bits {
			if i&(1<<b) != 0 {
				r |= 1 << (bits - 1 - b)
			}
		}
		m.rev[i] = r
	}
	return m
}

func (m *mdctN) fft() {
	z, n := m.z, m.n/2
	for i, r := range m.rev {
		if i < r {
			z[i], z[r] = z[r], z[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		half, step := size/2, n/size
		for start := 0; start < n; start += size {
			for j := range half {
				t := m.roots[j*step] * z[start+j+half]
				u := z[start+j]
				z[start+j] = u + t
				z[start+j+half] = u - t
			}
		}
	}
}

// transform computes the coefficients of x (2n samples, windowed).
func (m *mdctN) transform(x []float64) []float64 {
	n, q := m.n, m.n/2
	// Fold: the MDCT of (a, b, c, d) is the DCT-IV of (-c_r - d, a - b_r).
	h := n / 2
	for i := range h {
		m.v[i] = -x[3*h-1-i] - x[3*h+i]
		m.v[h+i] = x[i] - x[2*h-1-i]
	}
	for k := range q {
		m.z[k] = complex(m.v[2*k], m.v[n-1-2*k]) * m.pre[k]
	}
	m.fft()
	for k := range q {
		y := m.z[k] * m.post[k]
		m.out[2*k] = real(y)
		m.out[n-1-2*k] = -imag(y)
	}
	return m.out
}
