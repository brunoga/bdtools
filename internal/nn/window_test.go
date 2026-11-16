package nn

import (
	"errors"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/brunoga/bdtools/internal/cuda"
)

// The windows hold the grid's rows where window_partition puts them,
// zeros in the padding, and unpartitioning gives the grid back.
func TestWindowPartition(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	h, w, ws, c := 9, 11, 4, 3
	x := randTensor(r, h*w, c)
	y := WindowPartition(x, h, w, ws)
	_, pw, n := WindowGrid(h, w, ws)
	if n != 9 || y.Dim(0) != n*ws*ws {
		t.Fatalf("%d windows, %v", n, y.Shape)
	}
	for win := range n {
		for t2 := range ws * ws {
			Y, X := win/(pw/ws)*ws+t2/ws, win%(pw/ws)*ws+t2%ws
			for j := range c {
				want := float32(0)
				if Y < h && X < w {
					want = x.Data[(Y*w+X)*c+j]
				}
				if got := y.Data[(win*ws*ws+t2)*c+j]; got != want {
					t.Fatalf("window %d row %d: %g, want %g", win, t2, got, want)
				}
			}
		}
	}
	back := WindowUnpartition(y, h, w, ws)
	for i, v := range x.Data {
		if back.Data[i] != v {
			t.Fatalf("unpartitioned %d: %g, want %g", i, back.Data[i], v)
		}
	}
}

// WindowPool is the 2 x 2 maximum within each window.
func TestWindowPool(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	ws, c, n := 4, 2, 3
	x := randTensor(r, n*ws*ws, c)
	y := WindowPool(x, ws)
	for win := range n {
		for t2 := range 4 {
			for j := range c {
				Y, X := 2*(t2/2), 2*(t2%2)
				at := func(dy, dx int) float32 { return x.Data[(win*ws*ws+(Y+dy)*ws+X+dx)*c+j] }
				want := max(at(0, 0), at(0, 1), at(1, 0), at(1, 1))
				if got := y.Data[(win*4+t2)*c+j]; got != want {
					t.Fatalf("window %d row %d: %g, want %g", win, t2, got, want)
				}
			}
		}
	}
}

// AttentionBatch is Attention on each batch alone.
func TestAttentionBatch(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8))
	b, nq, nk, d, heads := 5, 6, 9, 12, 3
	q, k, v := randTensor(r, b*nq, d), randTensor(r, b*nk, d), randTensor(r, b*nk, d)
	got := AttentionBatch(q, k, v, b, heads, 0.3)
	for i := range b {
		sl := func(x *Tensor, n int) *Tensor {
			return &Tensor{Shape: []int{n, d}, Data: x.Data[i*n*d : (i+1)*n*d]}
		}
		want := Attention(sl(q, nq), sl(k, nk), sl(v, nk), heads, 0.3)
		for j, w := range want.Data {
			if math.Abs(float64(got.Data[i*nq*d+j]-w)) > 1e-5 {
				t.Fatalf("batch %d element %d: %g, want %g", i, j, got.Data[i*nq*d+j], w)
			}
		}
	}
}

// The GPU's windowed layers give the CPU's.
func TestGPUWindowLayers(t *testing.T) {
	g, err := NewGPU()
	if errors.Is(err, cuda.ErrUnavailable) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	r := rand.New(rand.NewPCG(9, 10))
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
	h, w, ws, c := 30, 33, 14, 5
	x := randTensor(r, h*w, c)
	gx := g.Upload(x)
	p := WindowPartition(x, h, w, ws)
	gp := g.WindowPartition(gx, h, w, ws)
	same("partition", p, gp)
	same("unpartition", WindowUnpartition(p, h, w, ws), g.WindowUnpartition(gp, h, w, ws))
	same("pool", WindowPool(p, ws), g.WindowPool(gp, ws))
	b, n, d, heads := 9, 49, 96*2, 2
	q, k, v := randTensor(r, b*n, d), randTensor(r, b*n, d), randTensor(r, b*n, d)
	rx, cs := randTensor(r, 50, 8), randTensor(r, 7, 8)
	grx := g.Upload(rx)
	same("rope", Rope(rx.Clone(), cs, 45), g.Rope(grx, g.Upload(cs), 45))
	same("attention", AttentionBatch(q, k, v, b, heads, 0.1), g.AttentionBatch(g.Upload(q), g.Upload(k), g.Upload(v), b, heads, 0.1))
}

// Rope multiplies each pair by its row's cos + i sin, repeating the table.
func TestRope(t *testing.T) {
	x := &Tensor{Shape: []int{3, 2}, Data: []float32{1, 2, 3, 4, 5, 6}}
	cs := &Tensor{Shape: []int{2, 2}, Data: []float32{0, 1, 1, 0}} // i, then 1
	Rope(x, cs, 3)
	want := []float32{-2, 1, 3, 4, -6, 5}
	for i, v := range want {
		if x.Data[i] != v {
			t.Fatalf("%v, want %v", x.Data, want)
		}
	}
}
