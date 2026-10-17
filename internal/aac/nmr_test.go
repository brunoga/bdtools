package aac

import (
	"math"
	"math/cmplx"
)

// A yardstick for perceptual coders, independent of the encoder's model
// (its own analysis, bands and constants, after PEAQ's noise-to-mask
// ratio): how far the coding noise rises over what the original signal
// masks.

func fft2048(x []complex128) {
	n := len(x)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		w := cmplx.Exp(complex(0, -2*math.Pi/float64(size)))
		for s := 0; s < n; s += size {
			wk := complex(1, 0)
			for k := range size / 2 {
				t := wk * x[s+k+size/2]
				x[s+k+size/2] = x[s+k] - t
				x[s+k] += t
				wk *= w
			}
		}
	}
}

// nmr gives the mean noise-to-mask ratio (dB) of decoded against orig and
// the share of frames with a band over its mask by more than 1.5 dB.
func nmr(orig, decoded []float64) (meanDB, disturbed float64) {
	const n, rate = 2048, 48000.0
	hann := make([]float64, n)
	for i := range hann {
		hann[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/n)
	}
	// Half-Bark bands up to 16 kHz (the bandwidth both coders keep at the
	// rates compared).
	bark := func(f float64) float64 { return 13*math.Atan(0.00076*f) + 3.5*math.Atan((f/7500)*(f/7500)) }
	band := make([]int, n/2)
	nb := 0
	for k := range n / 2 {
		f := float64(k) * rate / n
		band[k] = int(2 * bark(f))
		if f < 16000 {
			nb = max(nb, band[k]+1)
		}
	}
	centre := make([]float64, nb)
	for b := range centre {
		centre[b] = (float64(b) + 0.5) / 2
	}
	ath := make([]float64, nb)
	for k := 1; k < n/2; k++ {
		if b := band[k]; b < nb {
			f := float64(k) * rate / n / 1000
			db := 3.64*math.Pow(f, -0.8) - 6.5*math.Exp(-0.6*(f-3.3)*(f-3.3)) + 1e-3*math.Pow(f, 4)
			ath[b] += math.Pow(10, (db-96)/10) * n * n / 16
		}
	}
	var sum float64
	frames, bad := 0, 0
	x := make([]complex128, n)
	y := make([]complex128, n)
	for off := 0; off+n <= len(orig) && off+n <= len(decoded); off += n / 2 {
		for i := range n {
			x[i] = complex(orig[off+i]*hann[i], 0)
			y[i] = complex((decoded[off+i]-orig[off+i])*hann[i], 0)
		}
		fft2048(x)
		fft2048(y)
		es := make([]float64, nb)
		en := make([]float64, nb)
		for k := 1; k < n/2; k++ {
			if b := band[k]; b < nb {
				es[b] += real(x[k])*real(x[k]) + imag(x[k])*imag(x[k])
				en[b] += real(y[k])*real(y[k]) + imag(y[k])*imag(y[k])
			}
		}
		var total float64
		worst := -100.0
		for b := range nb {
			var mask float64
			for j := range nb {
				dz := centre[b] - centre[j]
				att := 22 * dz
				if dz < 0 {
					att = -27 * dz
				}
				mask += es[j] * math.Pow(10, -att/10)
			}
			offset := 3.0
			if centre[b] > 12 {
				offset = 0.25 * centre[b] * 4
			}
			mask = mask*math.Pow(10, -offset/10) + ath[b]
			r := en[b] / mask
			total += r
			worst = max(worst, 10*math.Log10(r+1e-12))
		}
		sum += total / float64(nb)
		frames++
		if worst > 1.5 {
			bad++
		}
	}
	return 10 * math.Log10(sum/float64(frames)), float64(bad) / float64(frames)
}
