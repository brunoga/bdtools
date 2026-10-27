//go:build amd64 && !purego

package av1

import (
	"math/rand/v2"
	"testing"
)

// The AVX2 inverse kernels give the scalar inverse transform's residual
// exactly, on coefficients across the dequantizer's whole range (dense,
// sparse, only the first row or column, at the limits), at 8 and 10 bits.
func TestInverseKernels(t *testing.T) {
	if !useAVX2 {
		t.Skip("no AVX2")
	}
	rng := rand.New(rand.NewPCG(3, 5))
	for txSz := range TX_SIZES_ALL {
		w, h := int(txWidth[txSz]), int(txHeight[txSz])
		if w < 8 {
			continue
		}
		tw, th := min(w, 32), min(h, 32)
		for tt := range TX_TYPES {
			if !txTypeValid(txSz, tt) {
				continue
			}
			for _, bd := range []int{8, 10} {
				m := int32(1)<<(bd+7) - 1
				for trial := range 30 {
					coef := make([]int32, tw*th)
					for i := range coef {
						r, c := i/tw, i%tw
						switch {
						case trial == 0:
							coef[i] = m
						case trial == 1:
							coef[i] = -m * int32(1-2*(i&1))
						case trial == 2 && r == 0, trial == 3 && c == 0:
							coef[i] = rng.Int32N(2*m+1) - m
						case trial < 4:
						case trial < 15:
							if rng.IntN(8) == 0 {
								coef[i] = rng.Int32N(2*m+1) - m
							}
						default:
							coef[i] = rng.Int32N(2*m+1) - m
						}
					}
					got := make([]int32, w*h)
					inverseTransform(coef, txSz, tt, bd, false, got)
					useAVX2 = false
					want := make([]int32, w*h)
					inverseTransform(coef, txSz, tt, bd, false, want)
					useAVX2 = true
					for i := range want {
						if got[i] != want[i] {
							t.Fatalf("size %d type %d bd %d trial %d: sample %d is %d, want %d", txSz, tt, bd, trial, i, got[i], want[i])
						}
					}
				}
			}
		}
	}
}

// The AVX2 SSE gives sseGo's sum on every block width and height, at the
// 12-bit limit too.
func TestSSEKernel(t *testing.T) {
	// a block past the picture's edge has no visible samples
	if s := sse(make([]uint16, 64), 8, make([]uint16, 64), 8, -2, 4); s != 0 {
		t.Fatalf("width -2: %d", s)
	}
	rng := rand.New(rand.NewPCG(11, 13))
	for _, w := range []int{8, 16, 24, 32, 64} {
		for _, h := range []int{1, 4, 8, 16, 32, 64} {
			for trial := range 6 {
				as, bs := w+rng.IntN(9), w+rng.IntN(9)
				a, b := make([]uint16, h*as), make([]uint16, h*bs)
				for i := range a {
					a[i] = uint16(rng.IntN(4096))
				}
				for i := range b {
					b[i] = uint16(rng.IntN(4096))
					if trial == 0 {
						a[min(i, len(a)-1)], b[i] = 4095, 0
					}
				}
				if got, want := sse(a, as, b, bs, w, h), sseGo(a, as, b, bs, w, h); got != want {
					t.Fatalf("%dx%d trial %d: %d, want %d", w, h, trial, got, want)
				}
			}
		}
	}
}
