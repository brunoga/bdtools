//go:build amd64 && !purego

package av1

import (
	"math/bits"
	"math/rand/v2"
	"testing"
)

// The AVX2 deadzone quantizer gives quantize's scalar levels and end of
// block, on every transform size and scan, from all-zero blocks to the
// largest coefficients and steps.
func TestQuantDZKernel(t *testing.T) {
	if !useAVX2 {
		t.Skip("no AVX2")
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for txSz := range TX_SIZES_ALL {
		n := min(32, int(txWidth[txSz])) * min(32, int(txHeight[txSz]))
		den := int64(dqDenom(txSz))
		for _, tt := range []int{DCT_DCT, IDTX, V_DCT, H_DCT} {
			inv := invScanFor(txSz, tt)
			if len(inv) < n {
				continue // not a size quantize sees
			}
			inv = inv[:n]
			for trial := range 200 {
				qs := int64(4 + rng.IntN(30000))
				m := []int32{0, 8, 1000, 1 << 20}[trial%4]
				coef := make([]int32, n)
				for i := range coef {
					if trial%3 != 0 || rng.IntN(8) == 0 {
						coef[i] = rng.Int32N(2*m+1) - m
					}
				}
				want, wantEob := make([]int32, n), 0
				for i, c := range coef {
					a := int64(abs32(c))
					l := min((a*den+qs/2)/qs, 1<<18)
					if c < 0 {
						l = -l
					}
					want[i] = int32(l)
					if l != 0 {
						wantEob = max(wantEob, int(inv[i])+1)
					}
				}
				got := make([]int32, n)
				for i := range got {
					got[i] = 12345 // overwritten
				}
				eob := quantDZAVX2(&coef[0], &got[0], &inv[0], n, int(den), int(qs))
				if eob != wantEob {
					t.Fatalf("size %d type %d trial %d: eob %d, want %d", txSz, tt, trial, eob, wantEob)
				}
				for i := range want {
					if got[i] != want[i] {
						t.Fatalf("size %d type %d trial %d: level %d is %d, want %d (coef %d qs %d)", txSz, tt, trial, i, got[i], want[i], coef[i], qs)
					}
				}
			}
		}
	}
}

// The AVX2 estimate gives rankTxTypes' scalar errors, bits and end of
// block, on every transform size and scan.
func TestRankEstKernel(t *testing.T) {
	if !useAVX2 {
		t.Skip("no AVX2")
	}
	rng := rand.New(rand.NewPCG(3, 4))
	for txSz := range TX_SIZES_ALL {
		n := min(32, int(txWidth[txSz])) * min(32, int(txHeight[txSz]))
		dsh := uint(bits.TrailingZeros(uint(dqDenom(txSz))))
		for _, tt := range []int{DCT_DCT, IDTX, V_DCT, H_DCT} {
			inv := invScanFor(txSz, tt)
			if len(inv) < n {
				continue
			}
			inv = inv[:n]
			for trial := range 200 {
				qs := int64(4 + rng.IntN(30000))
				m := []int32{0, 8, 1000, 1 << 22}[trial%4]
				coef := make([]int32, n)
				for i := range coef {
					if trial%3 != 0 || rng.IntN(8) == 0 {
						coef[i] = rng.Int32N(2*m+1) - m
					}
				}
				var wd int64
				wnb, weob := 0, 0
				for i, c := range coef {
					d, b, nz := levelEst(int64(abs32(c)), dsh, qs)
					wd, wnb = wd+d, wnb+b
					if nz {
						weob = max(weob, int(inv[i])+1)
					}
				}
				d, nb, eob := rankEstAVX2(&coef[0], &inv[0], n, int(dsh), int(qs))
				if d != wd || nb != wnb || eob != weob {
					t.Fatalf("size %d type %d trial %d: %d %d %d, want %d %d %d", txSz, tt, trial, d, nb, eob, wd, wnb, weob)
				}
			}
		}
	}
}
