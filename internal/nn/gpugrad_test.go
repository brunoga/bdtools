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
