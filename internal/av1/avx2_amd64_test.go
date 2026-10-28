//go:build amd64 && !purego

package av1

import (
	"bytes"
	"testing"
)

// The encoder makes the same stream without the AVX2 kernels as with
// them (the Go paths a CPU without AVX2 takes), at 8 and 10 bits.
func TestNoAVX2SameStream(t *testing.T) {
	if !useAVX2 {
		t.Skip("no AVX2")
	}
	defer func() { useAVX2 = true }()
	for _, bd := range []int{8, 10} {
		o := EncoderOptions{Width: 200, Height: 136, BitDepth: bd, QIndex: 110, MiniGOP: 4, Speed: 2}
		p := testPicture(200, 136, bd, 5)
		pics := raceCut([]*Source{p, shifted(p, 3, 1), shifted(p, 5, 3), shifted(p, 8, 4), shifted(p, 11, 5)})
		code := func(avx2 bool) []byte {
			useAVX2 = avx2
			e, err := NewEncoder(o)
			if err != nil {
				t.Fatal(err)
			}
			var b []byte
			for _, u := range encodeAll(t, e, pics) {
				b = append(b, u.Data...)
			}
			return b
		}
		if a, b := code(true), code(false); !bytes.Equal(a, b) {
			t.Fatalf("%d bits: %d bytes with AVX2, %d without: different", bd, len(a), len(b))
		}
	}
}
