//go:build amd64 && !purego

package mvc

import (
	"math/rand"
	"testing"
)

func TestSIMDDeblock(t *testing.T) {
	if !useAVX2Asm {
		t.Skip("no AVX2")
	}
	rng := rand.New(rand.NewSource(4))
	const st = 64
	for iter := 0; iter < 20000; iter++ {
		var a, b [st * 40]byte
		base := byte(rng.Intn(200))
		spread := []int{2, 8, 30, 255}[rng.Intn(4)]
		for i := range a {
			a[i] = byte(min(255, int(base)+rng.Intn(spread+1)))
		}
		b = a
		var bs [4]uint8
		for k := range bs {
			bs[k] = uint8(rng.Intn(5))
		}
		ia := rng.Intn(52)
		ib := rng.Intn(52)
		alpha, beta := int32(alphaTab[ia]), int32(betaTab[ib])
		off := 12*st + 16
		if iter&1 == 0 {
			filterLuma(a[:], off, 1, st, &bs, alpha, beta, ia)
			filterLumaGeneric(b[:], off, 1, st, &bs, alpha, beta, ia)
		} else {
			filterLuma(a[:], off, st, 1, &bs, alpha, beta, ia)
			filterLumaGeneric(b[:], off, st, 1, &bs, alpha, beta, ia)
		}
		if a != b {
			for i := range a {
				if a[i] != b[i] {
					t.Fatalf("luma deblock mismatch dir=%d at row %d col %d: %d vs %d bs=%v ia=%d ib=%d", iter&1, i/st, i%st, a[i], b[i], bs, ia, ib)
				}
			}
		}
		var c1, c2, d1, d2 [st * 20]byte
		for i := range c1 {
			c1[i] = byte(min(255, int(base)+rng.Intn(spread+1)))
			d1[i] = byte(min(255, int(base)+rng.Intn(spread+1)))
		}
		c2, d2 = c1, d1
		ia2 := [2]int{ia, rng.Intn(52)}
		al := [2]int32{alpha, int32(alphaTab[ia2[1]])}
		be := [2]int32{beta, int32(betaTab[rng.Intn(52)])}
		coff := 6*st + 8
		if iter&1 == 0 {
			filterChroma2(c1[:], d1[:], coff, 1, st, &bs, al, be, ia2)
			filterChroma2Generic(c2[:], d2[:], coff, 1, st, &bs, al, be, ia2)
		} else {
			filterChroma2(c1[:], d1[:], coff, st, 1, &bs, al, be, ia2)
			filterChroma2Generic(c2[:], d2[:], coff, st, 1, &bs, al, be, ia2)
		}
		if c1 != c2 || d1 != d2 {
			t.Fatalf("chroma deblock mismatch dir=%d bs=%v", iter&1, bs)
		}
	}
}
