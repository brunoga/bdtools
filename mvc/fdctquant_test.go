package mvc

import (
	"math/rand"
	"testing"
)

// The transform-and-quantise kernel gives the levels the plain code
// gives, at every quantiser, on residuals of every size.
func TestFDCTQuantLumaMatchesGeneric(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	const ss = 64
	src := make([]byte, ss*16+32)
	var pred [256]byte
	for qp := 0; qp < 52; qp += 3 {
		qbits := uint(15 + qp/6)
		f := int32(1<<qbits) / 6
		mf := &quantMF[qp%6]
		mfT := mfTranspose(mf)
		for it := 0; it < 200; it++ {
			spread := []int{2, 16, 64, 256}[it%4]
			for i := range src {
				src[i] = byte(rng.Intn(256))
			}
			for i := range pred {
				v := int(src[16+(i>>4)*ss+i&15]) + rng.Intn(spread) - spread/2
				pred[i] = byte(max(0, min(255, v)))
			}
			var want, got [16][16]int32
			wm, wn := fdctQuantLumaGeneric(src, 16, ss, &pred, mf, qbits, f, &want)
			gm, gn := fdctQuantLuma(src, 16, ss, &pred, mf, &mfT, qbits, f, &got)
			if want != got || wm != gm || wn != gn {
				t.Fatalf("qp %d spread %d: levels differ (mask %x vs %x, count %d vs %d)", qp, spread, wm, gm, wn, gn)
			}
		}
	}
}

func BenchmarkFDCTQuantLuma(b *testing.B) {
	rng := rand.New(rand.NewSource(7))
	const ss = 64
	src := make([]byte, ss*16+32)
	var pred [256]byte
	for i := range src {
		src[i] = byte(rng.Intn(256))
	}
	for i := range pred {
		pred[i] = byte(rng.Intn(256))
	}
	mf := &quantMF[2]
	mfT := mfTranspose(mf)
	var out [16][16]int32
	b.Run("asm", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			fdctQuantLuma(src, 16, ss, &pred, mf, &mfT, 17, 1<<17/6, &out)
		}
	})
	b.Run("generic", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			fdctQuantLumaGeneric(src, 16, ss, &pred, mf, 17, 1<<17/6, &out)
		}
	})
}

func TestFDCTQuantChromaMatchesGeneric(t *testing.T) {
	rng := rand.New(rand.NewSource(9))
	const ss = 40
	src := make([]byte, ss*8+16)
	var pred [64]byte
	for qp := 0; qp < 40; qp += 3 {
		qbits := uint(15 + qp/6)
		f := int32(1<<qbits) / 6
		mf := &quantMF[qp%6]
		mfT := mfTranspose(mf)
		for it := 0; it < 200; it++ {
			spread := []int{2, 16, 64, 256}[it%4]
			for i := range src {
				src[i] = byte(rng.Intn(256))
			}
			for i := range pred {
				v := int(src[8+(i>>3)*ss+i&7]) + rng.Intn(spread) - spread/2
				pred[i] = byte(max(0, min(255, v)))
			}
			var want, got [4][16]int32
			var wdc, gdc [16]int32
			wm, wn := fdctQuantChromaGeneric(src, 8, ss, &pred, mf, qbits, f, &want, &wdc)
			gm, gn := fdctQuantChroma(src, 8, ss, &pred, mf, &mfT, qbits, f, &got, &gdc)
			if want != got || wm != gm || wn != gn {
				t.Fatalf("qp %d spread %d: levels differ (mask %x vs %x, count %d vs %d)\n%v\n%v", qp, spread, wm, gm, wn, gn, want, got)
			}
			for b := 0; b < 4; b++ {
				if wdc[b*4] != gdc[b*4] {
					t.Fatalf("qp %d: DC of block %d %d vs %d", qp, b, wdc[b*4], gdc[b*4])
				}
			}
		}
	}
}
