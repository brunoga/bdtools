//go:build amd64 && !purego

package mvc

import (
	"math/rand"
	"testing"
)

func TestSIMDIDCT(t *testing.T) {
	if !useAVX2Asm {
		t.Skip("no AVX2")
	}
	rng := rand.New(rand.NewSource(3))
	for iter := 0; iter < 20000; iter++ {
		var c1, c2 [256]int16
		amp := []int{4, 64, 512, 2048}[rng.Intn(4)]
		for i := range c1 {
			if rng.Intn(3) == 0 {
				c1[i] = int16(rng.Intn(2*amp+1) - amp)
			}
		}
		c2 = c1
		var p1, p2 [16 * 24]byte
		for i := range p1 {
			p1[i] = byte(rng.Intn(256))
		}
		p2 = p1
		mask := uint16(rng.Intn(16))
		idctRow4(p1[:], 24, 24, c1[:], 15)
		idctRow4Generic(p2[:], 24, 24, c2[:], 15)
		if p1 != p2 || c1 != c2 {
			t.Fatalf("idctRow4 mismatch")
		}
		_ = mask
		for i := range c1 {
			if rng.Intn(3) == 0 {
				c1[i] = int16(rng.Intn(2*amp+1) - amp)
			}
		}
		c2 = c1
		idct8x8Row(p1[:], 24, 24, c1[:], 3)
		idct8x8RowGeneric(p2[:], 24, 24, c2[:], 3)
		if p1 != p2 || c1 != c2 {
			t.Fatalf("idct8x8Row mismatch amp=%d", amp)
		}
		for i := range c1 {
			c1[i] = 0
			if rng.Intn(3) == 0 {
				c1[i] = int16(rng.Intn(2*amp+1) - amp)
			}
		}
		c2 = c1
		idct8x8AddS(p1[:], 25, 24, c1[8:], 16)
		idct8x8AddSGeneric(p2[:], 25, 24, c2[8:], 16)
		if p1 != p2 || c1 != c2 {
			t.Fatalf("idct8x8AddS mismatch")
		}
		var cc1, cc2 [2][64]int16
		for p := 0; p < 2; p++ {
			for i := range cc1[p] {
				if rng.Intn(3) == 0 {
					cc1[p][i] = int16(rng.Intn(2*amp+1) - amp)
				}
			}
		}
		cc2 = cc1
		var q1, q2 [2][16 * 16]byte
		for p := 0; p < 2; p++ {
			for i := range q1[p] {
				q1[p][i] = byte(rng.Intn(256))
			}
		}
		q2 = q1
		idctChroma(q1[0][:], q1[1][:], 17, 16, &cc1, [2]uint8{15, 15})
		idctChromaGeneric(q2[0][:], q2[1][:], 17, 16, &cc2, [2]uint8{15, 15})
		if q1 != q2 || cc1 != cc2 {
			t.Fatalf("idctChroma mismatch")
		}
	}
}

func TestSIMDChromaDC(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	for iter := 0; iter < 5000; iter++ {
		var q1, q2 [2][16 * 16]byte
		for p := 0; p < 2; p++ {
			for i := range q1[p] {
				q1[p][i] = byte(rng.Intn(256))
			}
		}
		q2 = q1
		var dc [2][4]int16
		for p := 0; p < 2; p++ {
			for b := range dc[p] {
				dc[p][b] = int16(rng.Intn(1024) - 512)
			}
		}
		chromaAddDC(q1[0][:], q1[1][:], 17, 16, &dc)
		chromaAddDCGeneric(q2[0][:], q2[1][:], 17, 16, &dc)
		if q1 != q2 {
			t.Fatal("chromaAddDC mismatch")
		}
	}
}
