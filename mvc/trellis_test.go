package mvc

import (
	"math/rand"
	"testing"
)

// TestTrellisRounds: without a rate term the trellis rounds each level.
func TestTrellisRounds(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	var ctx [512]uint8
	tc := new(trellisCosts)
	tc.load(&ctx, catLuma4x4, 16)
	for qp := 0; qp < 52; qp += 7 {
		qbits := uint(15 + qp/6)
		mf := &quantMF[qp%6]
		for range 1000 {
			var d [16]int32
			for i := range d {
				d[i] = int32(rng.Intn(2001) - 1000)
			}
			var lv [16]int32
			trellisBlock(tc, d[:], zigzag4x4[:], mf[:], qbits, trellisW4[qp][:], 0, 0, 0, lv[:])
			for k := range 16 {
				p := zigzag4x4[k]
				if 2*(abs32(d[p])*mf[p])%(1<<qbits) == 0 {
					continue // half way
				}
				if want := quantLevel(d[p], mf[p], qbits, int32(1<<qbits)/2); lv[k] != want {
					t.Fatalf("qp %d: level %d = %d, want %d (%v)", qp, k, lv[k], want, d)
				}
			}
		}
	}
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// TestTrellisDrops: when bits cost far more than the error, nothing is
// coded; the 8x8 block's weights hold too.
func TestTrellisDrops(t *testing.T) {
	var ctx [512]uint8
	tc := new(trellisCosts)
	tc.load(&ctx, catLuma8x8, 64)
	var d [64]int32
	for i := range d {
		d[i] = int32(i%7*40 - 120)
	}
	qp := 26
	var lv [64]int32
	if !trellisBlock(tc, d[:], zigzag8x8[:], quant8MF[qp%6][:], uint(16+qp/6), trellisW8[qp][:], 0, 0, 0, lv[:]) {
		t.Fatal("without a rate term the block should code")
	}
	if trellisBlock(tc, d[:], zigzag8x8[:], quant8MF[qp%6][:], uint(16+qp/6), trellisW8[qp][:], 1e9, 0, 0, lv[:]) {
		t.Fatal("at a huge lambda the block should code nothing")
	}
	for _, l := range lv {
		if l != 0 {
			t.Fatal("a level left in a block reported empty")
		}
	}
	for qp := range 52 {
		for p, w := range trellisW8[qp] {
			if w <= 0 {
				t.Fatalf("qp %d position %d: weight %v", qp, p, w)
			}
		}
	}
}
