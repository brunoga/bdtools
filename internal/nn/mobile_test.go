package nn

import (
	"errors"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/brunoga/bdtools/internal/cuda"
)

// The GPU's mobile layers give the CPU's, at sizes off the kernels'
// blocks and with every activation and stride.
func TestGPUMobileLayers(t *testing.T) {
	g, err := NewGPU()
	if errors.Is(err, cuda.ErrUnavailable) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	r := rand.New(rand.NewPCG(1, 2))
	x := randTensor(r, 7, 23, 37)
	same := func(name string, want, got *Tensor) {
		t.Helper()
		d, err := g.Download(got)
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Data) != len(want.Data) {
			t.Fatalf("%s: %v, want %v", name, d.Shape, want.Shape)
		}
		for i, v := range want.Data {
			if math.Abs(float64(d.Data[i]-v)) > 1e-4*(1+math.Abs(float64(v))) {
				t.Fatalf("%s: element %d is %g, want %g", name, i, d.Data[i], v)
			}
		}
	}
	gx := g.Upload(x)
	for _, k := range []int{3, 5} {
		for _, stride := range []int{1, 2} {
			w, b := randTensor(r, 7, 1, k, k), randTensor(r, 7)
			same("depthwise", DepthwiseConv2d(x, w, b, stride, k/2), g.DepthwiseConv2d(gx, g.Upload(w), g.Upload(b), stride, k/2))
		}
	}
	for fn := ActNone; fn <= ActSigmoid; fn++ {
		a := Act{Fn: fn, PreS: 1.5, PreT: 0.25, PostS: 0.75, PostT: -0.5, A: 0.2, B: 0.5}
		same("act", Activate(x, a), g.Activate(gx, a))
	}
	same("pool", GlobalAvgPool(x), g.GlobalAvgPool(gx))
	s := randTensor(r, 7, 1, 1)
	same("mulch", MulChannels(x, s), g.MulChannels(gx, g.Upload(s)))
	same("nearest", Nearest(x, 4), g.Nearest(gx, 4))
	y := randTensor(r, 3, 23, 37)
	same("concat", Concat(x, y, x), g.Concat(gx, g.Upload(y), gx))
}

// A hard swish is x clip(x + 3, 0, 6) / 6, and the act's affine parts go
// either side of it.
func TestActApply(t *testing.T) {
	a := Act{Fn: ActHardSwish, PreS: 2, PreT: 1, PostS: 3, PostT: -1}
	for _, x := range []float32{-5, -1, 0, 0.5, 2} {
		v := 2*x + 1
		want := 3*(v*min(max(v+3, 0), 6)/6) - 1
		if got := a.Apply(x); got != want {
			t.Errorf("Apply(%g) = %g, want %g", x, got, want)
		}
	}
}
