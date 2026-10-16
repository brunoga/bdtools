package ac3

import (
	"math"
	"math/cmplx"
)

// The transform: a 512-sample MDCT (256 coefficients) folded into a
// 256-point DCT-IV, computed with a 128-point complex FFT.

const (
	mdctN = 256 // coefficients
	fftN  = mdctN / 2
)

var (
	twiddlePre  [fftN]complex128
	twiddlePost [fftN]complex128
	fftRoots    [fftN / 2]complex128
	bitrev      [fftN]int
)

func init() {
	for n := range fftN {
		twiddlePre[n] = cmplx.Exp(complex(0, -math.Pi*float64(n)/mdctN))
		twiddlePost[n] = cmplx.Exp(complex(0, -math.Pi*(float64(n)+0.25)/mdctN))
	}
	for k := range fftN / 2 {
		fftRoots[k] = cmplx.Exp(complex(0, -2*math.Pi*float64(k)/fftN))
	}
	bits := 0
	for 1<<bits < fftN {
		bits++
	}
	for i := range fftN {
		r := 0
		for b := range bits {
			if i&(1<<b) != 0 {
				r |= 1 << (bits - 1 - b)
			}
		}
		bitrev[i] = r
	}
}

// fft transforms z in place (radix 2, decimation in time).
func fft(z *[fftN]complex128) {
	for i, r := range bitrev {
		if i < r {
			z[i], z[r] = z[r], z[i]
		}
	}
	for size := 2; size <= fftN; size <<= 1 {
		half, step := size/2, fftN/size
		for start := 0; start < fftN; start += size {
			for j := range half {
				t := fftRoots[j*step] * z[start+j+half]
				u := z[start+j]
				z[start+j] = u + t
				z[start+j+half] = u - t
			}
		}
	}
}

// dct4 computes X[k] = sum v[n] cos(pi/N (n+1/2)(k+1/2)), N = 256.
func dct4(v *[mdctN]float64, out *[mdctN]float64) {
	var z [fftN]complex128
	for n := range fftN {
		z[n] = complex(v[2*n], v[mdctN-1-2*n]) * twiddlePre[n]
	}
	fft(&z)
	for k := range fftN {
		y := z[k] * twiddlePost[k]
		out[2*k] = real(y)
		out[mdctN-1-2*k] = -imag(y)
	}
}

// mdct computes the 256 coefficients of 512 windowed samples, scaled as
// X[k] = scale * sum x[n] cos(pi/256 (n + 1/2 + 128)(k + 1/2)).
func mdct(x *[512]float64, out *[mdctN]float64, scale float64) {
	// Fold: with x = (a, b, c, d) in quarters, the MDCT is the DCT-IV of
	// (-c_r - d, a - b_r).
	const q = 128
	var v [mdctN]float64
	for n := range q {
		v[n] = -x[3*q-1-n] - x[3*q+n]
		v[q+n] = x[n] - x[2*q-1-n]
	}
	dct4(&v, out)
	for k := range out {
		out[k] *= scale
	}
}
