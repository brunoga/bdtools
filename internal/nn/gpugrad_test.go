package nn

import (
	"errors"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/brunoga/bdtools/internal/cuda"
)

// TestGPUGradients checks the GPU's gradients against the CPU's.
func TestGPUGradients(t *testing.T) {
	g, err := NewGPU()
	if errors.Is(err, cuda.ErrUnavailable) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	r := rand.New(rand.NewPCG(3, 4))
	near := func(name string, got, want *Tensor) {
		t.Helper()
		d, err := g.Download(got)
		if err != nil {
			t.Fatal(err)
		}
		var worst, scale float64
		for i, v := range want.Data {
			worst = max(worst, math.Abs(float64(d.Data[i]-v)))
			scale = max(scale, math.Abs(float64(v)))
		}
		if worst > 1e-4*max(scale, 1) {
			t.Errorf("%s: %g off (largest %g)", name, worst, scale)
		}
	}
	for _, c := range []struct{ c, o, h, w, stride int }{{4, 16, 37, 53, 1}, {16, 32, 40, 64, 2}, {48, 64, 13, 17, 2}, {16, 16, 80, 120, 1}} {
		x, w := randTensor(r, c.c, c.h, c.w), randTensor(r, c.o, c.c, 3, 3)
		ho, wo := (c.h+2-3)/c.stride+1, (c.w+2-3)/c.stride+1
		dy := randTensor(r, c.o, ho, wo)
		dx, dw, db := Conv2dBackward(x, w, dy, c.stride, 1)
		gx, gw, gb := g.Conv2dBackward(g.Upload(x), g.Upload(w), g.Upload(dy), c.stride, 1)
		near("conv dx", gx, dx)
		near("conv dw", gw, dw)
		near("conv db", gb, db)
	}
	x, w := randTensor(r, 32, 11, 19), randTensor(r, 32, 16, 2, 2)
	dy := randTensor(r, 16, 22, 38)
	dx, dw, db := ConvTranspose2dKBackward(x, w, dy)
	gx, gw, gb := g.ConvTranspose2dKBackward(g.Upload(x), g.Upload(w), g.Upload(dy))
	near("convt dx", gx, dx)
	near("convt dw", gw, dw)
	near("convt db", gb, db)
	// bilinear: the gradient is the transpose, so <dy, B x> = <B' dy, x>
	bx, bdy := randTensor(r, 3, 9, 14), randTensor(r, 3, 31, 40)
	gb2 := g.BilinearBackward(g.Upload(bdy), 9, 14)
	bd, err := g.Download(gb2)
	if err != nil {
		t.Fatal(err)
	}
	if a, b := dotT(bdy, Bilinear(bx, 31, 40)), dotT(bd, bx); math.Abs(a-b) > 1e-3*max(1, math.Abs(a)) {
		t.Errorf("bilinear: <dy, Bx> %v, <B'dy, x> %v", a, b)
	}
	y := ReLU(randTensor(r, 1000))
	d := randTensor(r, 1000)
	gd := g.ReLUBackward(g.Upload(y), g.Upload(d))
	near("relu", gd, ReLUBackward(y, d.Clone()))
}

// TestGPUHeadGradients checks the GPU's gradients of the depth head's
// other layers against the CPU's.
func TestGPUHeadGradients(t *testing.T) {
	g, err := NewGPU()
	if errors.Is(err, cuda.ErrUnavailable) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	r := rand.New(rand.NewPCG(5, 6))
	near := func(name string, got, want *Tensor) {
		t.Helper()
		d, err := g.Download(got)
		if err != nil {
			t.Fatal(err)
		}
		var worst, scale float64
		for i, v := range want.Data {
			worst = max(worst, math.Abs(float64(d.Data[i]-v)))
			scale = max(scale, math.Abs(float64(v)))
		}
		if worst > 2e-4*max(scale, 1) {
			t.Errorf("%s: %g off (largest %g)", name, worst, scale)
		}
	}
	// linear: rows of tokens, and a map's worth
	for _, c := range []struct{ n, in, out int }{{37, 64, 48}, {5000, 96, 32}} {
		x, w, dy := randTensor(r, c.n, c.in), randTensor(r, c.out, c.in), randTensor(r, c.n, c.out)
		dx, dw, db := LinearBackward(x, w, dy)
		gx, gw, gb := g.LinearBackward(g.Upload(x), g.Upload(w), g.Upload(dy))
		near("linear dx", gx, dx)
		near("linear dw", gw, dw)
		near("linear db", gb, db)
	}
	x, w, dy := randTensor(r, 300, 96), randTensor(r, 96), randTensor(r, 300, 96)
	dx, dw, db := LayerNormBackward(x, w, dy, 1e-5)
	gx, gw, gb := g.LayerNormBackward(g.Upload(x), g.Upload(w), g.Upload(dy), 1e-5)
	near("layernorm dx", gx, dx)
	near("layernorm dw", gw, dw)
	near("layernorm db", gb, db)
	x, w, dy = randTensor(r, 64, 20, 30), randTensor(r, 64), randTensor(r, 64, 20, 30)
	dx, dw, db = GroupNormBackward(x, 32, w, dy, 1e-6)
	gx, gw, gb = g.GroupNormBackward(g.Upload(x), 32, g.Upload(w), g.Upload(dy), 1e-6)
	near("groupnorm dx", gx, dx)
	near("groupnorm dw", gw, dw)
	near("groupnorm db", gb, db)
	x, dy = randTensor(r, 200, 64), randTensor(r, 200, 32)
	near("geglu", g.GEGLUBackward(g.Upload(x), g.Upload(dy)), GEGLUBackward(x, dy))
	for _, c := range []struct{ d, c, heads, t int }{{37, 64, 8, 5}, {29, 48, 4, 7}, {9, 128, 8, 32}} {
		q, peQ, peK, peV := randTensor(r, c.d, c.c), randTensor(r, c.c), randTensor(r, c.t, c.c), randTensor(r, c.t, c.c)
		ks, vs := make([]*Tensor, c.t), make([]*Tensor, c.t)
		gks, gvs := make([]*Tensor, c.t), make([]*Tensor, c.t)
		want := make([]bool, c.t)
		for f := range c.t {
			ks[f], vs[f] = randTensor(r, c.d, c.c), randTensor(r, c.d, c.c)
			gks[f], gvs[f] = g.Upload(ks[f]), g.Upload(vs[f])
			want[f] = f%2 == 1
		}
		dy := randTensor(r, c.d, c.c)
		dq, dpeQ, dpeK, dpeV, dks, dvs := TemporalAttentionBackward(q, peQ, peK, peV, ks, vs, c.heads, 0.3, dy)
		gq, gpeQ, gpeK, gpeV, gdks, gdvs := g.TemporalAttentionBackward(g.Upload(q), g.Upload(peQ), g.Upload(peK), g.Upload(peV),
			gks, gvs, c.heads, 0.3, g.Upload(dy), want)
		near("temporal dq", gq, dq)
		near("temporal dpeQ", gpeQ, dpeQ)
		near("temporal dpeK", gpeK, dpeK)
		near("temporal dpeV", gpeV, dpeV)
		for f := range c.t {
			if !want[f] {
				if gdks[f] != nil || gdvs[f] != nil {
					t.Errorf("frame %d: a gradient not asked for", f)
				}
				continue
			}
			near("temporal dk", gdks[f], dks[f])
			near("temporal dv", gdvs[f], dvs[f])
		}
		g.Release()
	}
}
