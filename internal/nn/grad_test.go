package nn

import (
	"math"
	"math/rand/v2"
	"testing"
)

func randTensor(r *rand.Rand, shape ...int) *Tensor {
	t := New(shape...)
	for i := range t.Data {
		t.Data[i] = float32(r.NormFloat64())
	}
	return t
}

// checkGrad compares a gradient with the loss's change by central
// differences, at a few of its entries.
func checkGrad(t *testing.T, name string, p, g *Tensor, loss func() float64, r *rand.Rand) {
	t.Helper()
	const eps = 1e-2
	for range 12 {
		i := r.IntN(len(p.Data))
		v := p.Data[i]
		p.Data[i] = v + eps
		up := loss()
		p.Data[i] = v - eps
		down := loss()
		p.Data[i] = v
		want := (up - down) / (2 * eps)
		if got := float64(g.Data[i]); math.Abs(got-want) > 2e-2*max(1, math.Abs(want)) {
			t.Errorf("%s[%d]: gradient %v, by differences %v", name, i, got, want)
		}
	}
}

// The loss is the output's dot product with a fixed tensor, so its
// gradient at the output is that tensor.
func dotT(a, b *Tensor) float64 {
	var s float64
	for i, v := range a.Data {
		s += float64(v) * float64(b.Data[i])
	}
	return s
}

func TestConv2dBackward(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for _, tc := range []struct{ c, o, h, w, k, stride, pad int }{
		{3, 5, 9, 70, 3, 1, 1},
		{4, 6, 10, 9, 3, 2, 1},
		{2, 3, 300, 7, 3, 1, 1}, // many bands
		{5, 4, 6, 8, 1, 1, 0},
	} {
		x := randTensor(r, tc.c, tc.h, tc.w)
		w := randTensor(r, tc.o, tc.c, tc.k, tc.k)
		b := randTensor(r, tc.o)
		y := Conv2d(x, w, b, tc.stride, tc.pad)
		dy := randTensor(r, y.Shape...)
		loss := func() float64 { return dotT(Conv2d(x, w, b, tc.stride, tc.pad), dy) }
		dx, dw, db := Conv2dBackward(x, w, dy, tc.stride, tc.pad)
		checkGrad(t, "x", x, dx, loss, r)
		checkGrad(t, "w", w, dw, loss, r)
		checkGrad(t, "b", b, db, loss, r)
	}
}

func TestConvTranspose2dKBackward(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	x := randTensor(r, 4, 5, 7)
	w := randTensor(r, 4, 3, 2, 2)
	b := randTensor(r, 3)
	dy := randTensor(r, 3, 10, 14)
	loss := func() float64 { return dotT(ConvTranspose2dK(x, w, b), dy) }
	dx, dw, db := ConvTranspose2dKBackward(x, w, dy)
	checkGrad(t, "x", x, dx, loss, r)
	checkGrad(t, "w", w, dw, loss, r)
	checkGrad(t, "b", b, db, loss, r)
}

func TestLinearBackward(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	x, w, b := randTensor(r, 7, 9), randTensor(r, 5, 9), randTensor(r, 5)
	dy := randTensor(r, 7, 5)
	loss := func() float64 { return dotT(Linear(x, w, b), dy) }
	dx, dw, db := LinearBackward(x, w, dy)
	checkGrad(t, "x", x, dx, loss, r)
	checkGrad(t, "w", w, dw, loss, r)
	checkGrad(t, "b", b, db, loss, r)
}

func TestLayerNormBackward(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8))
	x, w, b := randTensor(r, 6, 24), randTensor(r, 24), randTensor(r, 24)
	dy := randTensor(r, 6, 24)
	loss := func() float64 { return dotT(LayerNorm(x, w, b, 1e-5), dy) }
	dx, dw, db := LayerNormBackward(x, w, dy, 1e-5)
	checkGrad(t, "x", x, dx, loss, r)
	checkGrad(t, "w", w, dw, loss, r)
	checkGrad(t, "b", b, db, loss, r)
}

func TestGroupNormBackward(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 10))
	x, w, b := randTensor(r, 8, 5, 6), randTensor(r, 8), randTensor(r, 8)
	dy := randTensor(r, 8, 5, 6)
	loss := func() float64 { return dotT(GroupNorm(x, 4, w, b, 1e-6), dy) }
	dx, dw, db := GroupNormBackward(x, 4, w, dy, 1e-6)
	checkGrad(t, "x", x, dx, loss, r)
	checkGrad(t, "w", w, dw, loss, r)
	checkGrad(t, "b", b, db, loss, r)
}

func TestGEGLUBackward(t *testing.T) {
	r := rand.New(rand.NewPCG(11, 12))
	x, dy := randTensor(r, 9, 10), randTensor(r, 9, 5)
	loss := func() float64 { return dotT(CPU.GEGLU(x), dy) }
	checkGrad(t, "x", x, GEGLUBackward(x, dy), loss, r)
}

func TestTemporalAttentionBackward(t *testing.T) {
	r := rand.New(rand.NewPCG(13, 14))
	const d, c, heads, frames = 5, 12, 3, 4
	q, peQ, peK, peV := randTensor(r, d, c), randTensor(r, c), randTensor(r, frames, c), randTensor(r, frames, c)
	ks, vs := make([]*Tensor, frames), make([]*Tensor, frames)
	for f := range frames {
		ks[f], vs[f] = randTensor(r, d, c), randTensor(r, d, c)
	}
	dy := randTensor(r, d, c)
	loss := func() float64 { return dotT(CPU.TemporalAttention(q, peQ, peK, peV, ks, vs, heads, 0.4), dy) }
	dq, dpeQ, dpeK, dpeV, dks, dvs := TemporalAttentionBackward(q, peQ, peK, peV, ks, vs, heads, 0.4, dy)
	checkGrad(t, "q", q, dq, loss, r)
	checkGrad(t, "peQ", peQ, dpeQ, loss, r)
	checkGrad(t, "peK", peK, dpeK, loss, r)
	checkGrad(t, "peV", peV, dpeV, loss, r)
	checkGrad(t, "k2", ks[2], dks[2], loss, r)
	checkGrad(t, "v1", vs[1], dvs[1], loss, r)
}
