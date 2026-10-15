//go:build amd64 && !purego

package mvc

import (
	"math/rand"
	"testing"
)

func TestSIMDSADAndSATD(t *testing.T) {
	if !useAVX2Asm {
		t.Skip("no AVX2")
	}
	rng := rand.New(rand.NewSource(3))
	src := make([]byte, 64*64)
	ref := make([]byte, 64*64)
	for iter := 0; iter < 2000; iter++ {
		extreme := iter%3 == 0
		for i := range src {
			if extreme {
				src[i], ref[i] = byte(rng.Intn(2)*255), byte(rng.Intn(2)*255)
			} else {
				src[i], ref[i] = byte(rng.Intn(256)), byte(rng.Intn(256))
			}
		}
		w, h := 8+8*rng.Intn(2), 4+4*rng.Intn(4)
		ss, rs := 64, 32+rng.Intn(33)
		so, ro := rng.Intn(64-w)+rng.Intn(64-h)*ss, rng.Intn(32-w)+rng.Intn(10)*rs
		if g, a := sadGeneric(src, so, ss, ref, ro, rs, w, h), sadAsm(&src[so], ss, &ref[ro], rs, w, h); g != a {
			t.Fatalf("iter %d: SAD %dx%d: generic %d, asm %d", iter, w, h, g, a)
		}
		g, a := satdGeneric(src, so, ss, ref, ro, rs, w, h), satdAsm(&src[so], ss, &ref[ro], rs, w, h)/2
		if g != a {
			t.Fatalf("iter %d: SATD %dx%d: generic %d, asm %d", iter, w, h, g, a)
		}
	}
}
