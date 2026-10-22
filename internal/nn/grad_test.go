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
