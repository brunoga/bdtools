package hevc

import (
	"math/rand/v2"
	"slices"
	"testing"
)

// The kernels give what their Go versions give, at every width (the 16,
// 8 and 4 column paths, and columns left over) and on extreme samples.
func TestMCKernels(t *testing.T) {
	if !useAVX2 {
		t.Skip("no kernels here")
	}
	rng := rand.New(rand.NewPCG(3, 4))
	const sstride = 96
	for _, depth := range []int{8, 10, 12} {
		maxV := 1<<depth - 1
		src := make([]uint16, sstride*(64+8))
		src16 := make([]int16, sstride*(64+8))
		for trial := range 20 {
			for i := range src {
				switch trial % 4 {
				case 0:
					src[i] = uint16(maxV)
				case 1:
					src[i] = uint16(rng.IntN(2) * maxV)
				default:
					src[i] = uint16(rng.IntN(maxV + 1))
				}
			}
			// A first pass's output, as the second pass takes.
			hFilterGo(src16, sstride, sstride-7, 64+8, src, sstride, &lumaPairs[2], 4, uint(depth-8))
			for w := 1; w <= 64; w++ {
				h := []int{1, 2, 4, 7, 16}[rng.IntN(5)]
				dstride := w + rng.IntN(3)
				got := make([]int16, dstride*h)
				want := make([]int16, dstride*h)
				for _, pairs := range []int{2, 4} {
					var c *tapPairs
					if pairs == 4 {
						c = &lumaPairs[1+rng.IntN(3)]
					} else {
						c = &chromaPairs[1+rng.IntN(7)]
					}
					shift := uint(depth - 8)
					hFilter(got, dstride, w, h, src, sstride, c, pairs, shift)
					hFilterGo(want, dstride, w, h, src, sstride, c, pairs, shift)
					if !slices.Equal(got, want) {
						t.Fatalf("hFilter depth %d w %d h %d pairs %d:\n%v\nwant\n%v", depth, w, h, pairs, got, want)
					}
					vFilter(got, dstride, w, h, src, sstride, c, pairs, shift)
					vFilterGo(want, dstride, w, h, src, sstride, c, pairs, shift)
					if !slices.Equal(got, want) {
						t.Fatalf("vFilter depth %d w %d h %d pairs %d:\n%v\nwant\n%v", depth, w, h, pairs, got, want)
					}
					vFilter16(got, dstride, w, h, src16, sstride, c, pairs)
					vFilterGo(want, dstride, w, h, src16, sstride, c, pairs, 6)
					if !slices.Equal(got, want) {
						t.Fatalf("vFilter16 depth %d w %d h %d pairs %d:\n%v\nwant\n%v", depth, w, h, pairs, got, want)
					}
				}
				copyBlock(got, dstride, w, h, src, sstride, uint(14-depth))
				copyBlockGo(want, dstride, w, h, src, sstride, uint(14-depth))
				if !slices.Equal(got, want) {
					t.Fatalf("copyBlock depth %d w %d h %d", depth, w, h)
				}
				// The predictions: the first pass's range, and beyond.
				a, b := make([]int16, w*h), make([]int16, w*h)
				for i := range a {
					a[i] = int16(rng.IntN(44000) - 22000)
					b[i] = int16(rng.IntN(44000) - 22000)
				}
				out, outWant := make([]uint16, dstride*h), make([]uint16, dstride*h)
				shift1 := 14 - depth
				for _, m := range []struct{ w0, w1, add, shift, o int }{
					{1, 0, 1 << (shift1 - 1), shift1, 0},                      // one list
					{1, 1, 1 << shift1, shift1 + 1, 0},                        // two
					{rng.IntN(256) - 128, 0, 1 << 9, 10, rng.IntN(512) - 256}, // weighted
					{255, -128, 1 << 10, 11, 0},                               // weighted, two lists
					{-128, 0, 0, 0, 1023},                                     // log2WD < 1
				} {
					putBlock(out, dstride, a, b, w, w, h, m.w0, m.w1, m.add, uint(m.shift), m.o, maxV)
					putBlockGo(outWant, dstride, a, b, w, w, h, m.w0, m.w1, m.add, uint(m.shift), m.o, maxV)
					if !slices.Equal(out, outWant) {
						t.Fatalf("putBlock depth %d w %d h %d %+v:\n%v\nwant\n%v", depth, w, h, m, out, outWant)
					}
				}
			}
		}
	}
}

// Pack gives what the Go versions give, at every width, 8 and 10 bits.
func TestPack(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	for _, depth := range []int{8, 10} {
		for w := 1; w <= 80; w++ {
			h := 1 + rng.IntN(6)
			cw, ch := (w+1)/2, (h+1)/2
			p := &Picture{Width: w, Height: h, BitDepth: depth, StrideY: w + 3, StrideC: cw + 1}
			p.Y = make([]uint16, p.StrideY*h)
			p.Cb, p.Cr = make([]uint16, p.StrideC*ch), make([]uint16, p.StrideC*ch)
			for _, pl := range [][]uint16{p.Y, p.Cb, p.Cr} {
				for i := range pl {
					pl[i] = uint16(rng.IntN(1 << depth))
				}
			}
			bps := 1
			if depth > 8 {
				bps = 2
			}
			pitch := (w+w&1)*bps + 4
			y, uv := make([]byte, pitch*h), make([]byte, pitch*ch)
			p.Pack(y, uv, pitch, 0, h)
			wy, wuv := make([]byte, pitch*h), make([]byte, pitch*ch)
			for r := range h {
				if depth == 8 {
					packBytesGo(wy[r*pitch:], p.Y[r*p.StrideY:r*p.StrideY+w])
				} else {
					packWordsGo(wy[r*pitch:], p.Y[r*p.StrideY:r*p.StrideY+w], uint(16-depth))
				}
			}
			for r := range ch {
				cb, cr := p.Cb[r*p.StrideC:r*p.StrideC+cw], p.Cr[r*p.StrideC:r*p.StrideC+cw]
				if depth == 8 {
					weaveBytesGo(wuv[r*pitch:], cb, cr)
				} else {
					weaveWordsGo(wuv[r*pitch:], cb, cr, uint(16-depth))
				}
			}
			if !slices.Equal(y, wy) || !slices.Equal(uv, wuv) {
				t.Fatalf("depth %d width %d: packed differently", depth, w)
			}
		}
	}
}
