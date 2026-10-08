package dovi

import (
	"bytes"
	"math/rand/v2"
	"slices"
	"testing"
)

func randP010(r *rand.Rand, n int) []byte {
	b := make([]byte, 2*n)
	for i := range n {
		setSample(b, i, r.IntN(1024))
	}
	return b
}

func randLUT(r *rand.Rand) *[1024]float32 {
	var t [1024]float32
	for i := range t {
		t[i] = r.Float32()*1.2 - 0.1 // past both ends, to test the clamp
	}
	return &t
}

// Each kernel (AVX2 where there is one) against its Go version, over
// lengths that leave tails.
func TestKernels(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 10))
	for _, n := range []int{1, 7, 8, 9, 15, 16, 17, 33, 100, 1920} {
		el := make([]int32, n)
		for i := range el {
			el[i] = int32(r.IntN(1024))
		}
		lut, res := randLUT(r), randLUT(r)

		bl := randP010(r, n)
		for _, e := range [][]int32{nil, el} {
			got, want := make([]byte, 2*n), make([]byte, 2*n)
			lumaRow(got, bl, e, lut, res)
			lumaRowGo(want, bl, e, lut, res)
			if !bytes.Equal(got, want) {
				t.Fatalf("lumaRow n=%d el=%v differs", n, e != nil)
			}
		}

		cw := n
		y0, y1, uv := randP010(r, 2*cw), randP010(r, 2*cw), randP010(r, 2*cw)
		gy, gb, gr := make([]float32, cw), make([]float32, cw), make([]float32, cw)
		wy, wb, wr := make([]float32, cw), make([]float32, cw), make([]float32, cw)
		chromaPrep(gy, gb, gr, y0, y1, uv)
		chromaPrepGo(wy, wb, wr, y0, y1, uv)
		if !slices.Equal(gy, wy) || !slices.Equal(gb, wb) || !slices.Equal(gr, wr) {
			t.Fatalf("chromaPrep n=%d differs", n)
		}

		ob, or := make([]float32, cw), make([]float32, cw)
		for i := range ob {
			ob[i], or[i] = r.Float32()*1.2-0.1, r.Float32()*1.2-0.1
		}
		eb, er := el, slices.Clone(el)
		slices.Reverse(er)
		for _, withEL := range []bool{false, true} {
			var b2, r2 []int32
			if withEL {
				b2, r2 = eb, er
			}
			got, want := make([]byte, 4*cw), make([]byte, 4*cw)
			chromaStore(got, ob, or, b2, r2, lut, res)
			chromaStoreGo(want, ob, or, b2, r2, lut, res)
			if !bytes.Equal(got, want) {
				t.Fatalf("chromaStore n=%d el=%v differs", n, withEL)
			}
		}

		rows := [4][]byte{randP010(r, 2*n), randP010(r, 2*n), randP010(r, 2*n), randP010(r, 2*n)}
		w := vertOdd
		gv, wv := make([]int32, n), make([]int32, n)
		verticalY(gv, rows[0], rows[1], rows[2], rows[3], w)
		verticalYGo(wv, rows[0], rows[1], rows[2], rows[3], w)
		if !slices.Equal(gv, wv) {
			t.Fatalf("verticalY n=%d differs", n)
		}
		gb2, gr2, wb2, wr2 := make([]int32, n), make([]int32, n), make([]int32, n), make([]int32, n)
		verticalUV(gb2, gr2, rows[0], rows[1], rows[2], rows[3], w)
		verticalUVGo(wb2, wr2, rows[0], rows[1], rows[2], rows[3], w)
		if !slices.Equal(gb2, wb2) || !slices.Equal(gr2, wr2) {
			t.Fatalf("verticalUV n=%d differs", n)
		}

		in := make([]int32, n)
		for i := range in {
			in[i] = int32(r.IntN(1024*128+20000)) - 10000 // overshoots both ways
		}
		gh, wh := make([]int32, 2*n), make([]int32, 2*n)
		horizontal(gh, in)
		horizontalGo(wh, in)
		if !slices.Equal(gh, wh) {
			t.Fatalf("horizontal n=%d differs:\n%v\n%v", n, gh, wh)
		}
	}
}
