package nn

import (
	"errors"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/brunoga/bdtools/internal/cuda"
)

// Buffers of sizes no longer asked for are freed: a stream whose
// pictures change size does not keep every size's.
func TestGPUFreesUnusedSizes(t *testing.T) {
	g, err := NewGPU()
	if errors.Is(err, cuda.ErrUnavailable) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	spares := func() int {
		n := 0
		for _, l := range g.free {
			n += len(l)
		}
		return n
	}
	for size := 1000; size < 1100; size++ {
		g.tensor(size)
		g.Release()
	}
	if n := spares(); n > 2 {
		t.Errorf("%d spare buffers after 100 sizes, each used once; want at most 2", n)
	}
	// the same size every frame is reused, not reallocated
	g.tensor(5000)
	g.Release()
	b := g.free[4*5000][0].buf
	x := g.tensor(5000)
	if x.dev.a.buf != b {
		t.Error("a frame's buffer not reused by the next")
	}
	g.Release()
}

// The products in half precision (the tensor cores): each within half
// precision's rounding of the CPU's, at sizes off the tiles' and with
// every mode (a linear layer, a 1x1 and a strided 3x3 convolution,
// attention's batched products).
func TestGPUHalfProducts(t *testing.T) {
	g, err := NewGPU()
	if errors.Is(err, cuda.ErrUnavailable) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	g.Half = true
	r := rand.New(rand.NewPCG(7, 8))
	rnd := func(shape ...int) *Tensor {
		x := New(shape...)
		for i := range x.Data {
			x.Data[i] = float32(r.NormFloat64())
		}
		return x
	}
	check := func(name string, want, got *Tensor) {
		t.Helper()
		gd, err := g.Download(got)
		if err != nil {
			t.Fatal(err)
		}
		var worst, peak float64
		for i, v := range want.Data {
			worst = max(worst, math.Abs(float64(gd.Data[i]-v)))
			peak = max(peak, math.Abs(float64(v)))
		}
		if worst > 4e-3*peak {
			t.Errorf("%s: off by %.3g of a peak of %.3g", name, worst, peak)
		}
	}
	x, w, b := rnd(301, 200), rnd(97, 200), rnd(97)
	check("linear", Linear(x, w, b), g.Linear(g.Upload(x), g.Upload(w), g.Upload(b)))
	// big enough for the tiles of 128 (the small ones are of 64)
	x, w, b = rnd(2600, 96), rnd(2400, 96), rnd(2400)
	check("big linear", Linear(x, w, b), g.Linear(g.Upload(x), g.Upload(w), g.Upload(b)))
	img, w1, b1 := rnd(70, 23, 41), rnd(33, 70, 1, 1), rnd(33)
	check("1x1 convolution", Conv2d(img, w1, b1, 1, 0), g.Conv2d(g.Upload(img), g.Upload(w1), g.Upload(b1), 1, 0))
	w3, b3 := rnd(45, 70, 3, 3), rnd(45)
	check("3x3 convolution", Conv2d(img, w3, b3, 2, 1), g.Conv2d(g.Upload(img), g.Upload(w3), g.Upload(b3), 2, 1))
	q, k, v := rnd(150, 128), rnd(170, 128), rnd(170, 128)
	check("attention", Attention(q, k, v, 2, 0.125), g.Attention(g.Upload(q), g.Upload(k), g.Upload(v), 2, 0.125))
	// a vision transformer's: six heads of 64, many tiles of keys
	q, k, v = rnd(333, 384), rnd(333, 384), rnd(333, 384)
	check("attention of six heads", Attention(q, k, v, 6, 0.125), g.Attention(g.Upload(q), g.Upload(k), g.Upload(v), 6, 0.125))
}
