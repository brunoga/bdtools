package nn

import (
	"errors"
	"maps"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/brunoga/bdtools/internal/cuda"
)

// tapeOps is what tapeNet runs on: the CPU backend or a Tape.
type tapeOps interface {
	Upload(t *Tensor) *Tensor
	Conv2d(x, w, b *Tensor, stride, pad int) *Tensor
	ConvTranspose2dK(x, w, b *Tensor) *Tensor
	Linear(x, w, b *Tensor) *Tensor
	LayerNorm(x, w, b *Tensor, eps float32) *Tensor
	GroupNorm(x *Tensor, groups int, w, b *Tensor, eps float32) *Tensor
	GEGLU(x *Tensor) *Tensor
	ReLU(x *Tensor) *Tensor
	Add(a, b *Tensor) *Tensor
	Bilinear(x *Tensor, oh, ow int) *Tensor
	Transpose(x *Tensor) *Tensor
	TemporalAttention(q, peQ, peK, peV *Tensor, ks, vs []*Tensor, heads int, scale float32) *Tensor
}

// tapeNet is a little of every layer the depth head has, on o: p the
// weights (o's tensors), x the input, pe two frames' position encodings
// (in memory), k0 and v0 a past frame's states.
func tapeNet(o tapeOps, p map[string]*Tensor, x, pe, k0, v0 *Tensor) *Tensor {
	h := o.ReLU(o.Conv2d(x, p["cw"], p["cb"], 1, 1))
	h = o.GroupNorm(h, 4, p["gw"], p["gb"], 1e-6)
	tok := o.Transpose(h.Reshape(8, 48))
	tok = o.Linear(tok, p["lw"], p["lb"])
	n := o.LayerNorm(tok, p["nw"], p["nb"], 1e-5)
	q, k, v := o.Linear(n, p["wq"], nil), o.Linear(n, p["wk"], nil), o.Linear(n, p["wv"], nil)
	peQ := o.Linear(o.Upload(&Tensor{Shape: []int{1, 8}, Data: pe.Data[8:]}), p["wq"], nil).Reshape(8)
	peK, peV := o.Linear(o.Upload(pe), p["wk"], nil), o.Linear(o.Upload(pe), p["wv"], nil)
	att := o.TemporalAttention(q, peQ, peK, peV, []*Tensor{k0, k}, []*Tensor{v0, v}, 2, 0.5)
	tok = o.Add(tok, att)
	ff := o.GEGLU(o.Linear(tok, p["fw"], p["fb"]))
	tok = o.Add(tok, ff)
	m := o.Transpose(tok).Reshape(8, 6, 8)
	return o.ConvTranspose2dK(o.Bilinear(m, 12, 16), p["tw"], p["tb"])
}

// TestTape checks a tape's gradients at every weight of a little
// network against the loss's differences on the CPU.
func TestTape(t *testing.T) {
	g, err := NewGPU()
	if errors.Is(err, cuda.ErrUnavailable) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	r := rand.New(rand.NewPCG(21, 22))
	shapes := map[string][]int{
		"cw": {8, 4, 3, 3}, "cb": {8}, "gw": {8}, "gb": {8}, "lw": {8, 8}, "lb": {8}, "nw": {8}, "nb": {8},
		"wq": {8, 8}, "wk": {8, 8}, "wv": {8, 8}, "fw": {16, 8}, "fb": {16}, "tw": {8, 2, 2, 2}, "tb": {2},
	}
	names := slices.Sorted(maps.Keys(shapes))
	p := map[string]*Tensor{}
	for _, name := range names {
		p[name] = randTensor(r, shapes[name]...)
	}
	for _, n := range []string{"gw", "nw"} { // norms' weights about 1
		for i := range p[n].Data {
			p[n].Data[i] = 1 + 0.1*p[n].Data[i]
		}
	}
	x, pe, k0, v0 := randTensor(r, 4, 6, 8), randTensor(r, 2, 8), randTensor(r, 48, 8), randTensor(r, 48, 8)
	dy := randTensor(r, 2, 24, 32)
	loss := func() float64 { return dotT(tapeNet(CPU, p, x, pe, k0, v0), dy) }

	tp := NewTape(g)
	on := map[string]*Tensor{}
	for name, w := range p {
		on[name] = tp.Param(name, w)
	}
	gx := tp.Watch(tp.Upload(x))
	out := tapeNet(tp, on, gx, pe, tp.Upload(k0), tp.Upload(v0))
	got, err := g.Download(out)
	if err != nil {
		t.Fatal(err)
	}
	want := tapeNet(CPU, p, x, pe, k0, v0)
	for i, v := range want.Data {
		if d := got.Data[i] - v; d > 1e-3 || d < -1e-3 {
			t.Fatalf("output %d: the tape's %v, the CPU's %v", i, got.Data[i], v)
		}
	}
	tp.Backward(out, tp.Upload(dy))
	grads := tp.Grads()
	for _, name := range names {
		w, d := p[name], grads[name]
		if d == nil {
			t.Errorf("%s: no gradient", name)
			continue
		}
		dw, err := g.Download(d)
		if err != nil {
			t.Fatal(err)
		}
		checkGrad(t, name, w, dw, loss, r)
	}
	dx, err := g.Download(tp.Grad(gx))
	if err != nil {
		t.Fatal(err)
	}
	checkGrad(t, "x", x, dx, loss, r)
	g.Release()
}
