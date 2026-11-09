package depth

import (
	"fmt"
	"maps"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/brunoga/bdtools/internal/nn"
)

// headStream runs the first n frames of a reference through the head on
// o with p, the window built as Infer builds it, and gives the last
// frame's depth and the frames' states. enc encodes a frame.
func headStream(o Ops, p Params, enc func(*nn.Tensor) [4]*nn.Tensor, frames []*nn.Tensor) *nn.Tensor {
	var states [][8]KV
	var out *nn.Tensor
	for _, x := range frames {
		ph, pw := x.Dim(1)/patch, x.Dim(2)/patch
		var s [8]KV
		out, s = Head(o, p, enc(o.Upload(x)), ph, pw, StartWindow(states), nil)
		states = append(states, s)
	}
	return out
}

// TestHeadOnTape checks the head run on a tape: the same depth as the
// model's, and the gradients at its weights (every one reached) against
// the loss's differences on the CPU, through three frames of the tiny
// model (its temporal window in play).
func TestHeadOnTape(t *testing.T) {
	g := gpu(t)
	m, err := Load("testdata/tiny.pth")
	if err != nil {
		t.Fatal(err)
	}
	io, err := nn.LoadPTH("testdata/tiny-io.pth")
	if err != nil {
		t.Fatal(err)
	}
	var frames []*nn.Tensor
	for i := range 3 {
		frames = append(frames, io[fmt.Sprintf("in.%d", i)])
	}
	r := rand.New(rand.NewPCG(31, 32))
	// the loss: the last frame's depth against a fixed random one
	want := m.Frame(frames[0])
	dy := nn.New(1, want.Dim(0), want.Dim(1))
	for i := range dy.Data {
		dy.Data[i] = float32(r.NormFloat64())
	}
	loss := func() float64 {
		m.Use(nn.CPU) // the caches (the position encodings' projections) afresh
		out := headStream(nn.CPU, m, m.Encode, frames)
		var s float64
		for i, v := range out.Data {
			s += float64(v) * float64(dy.Data[i])
		}
		return s
	}
	isHead := func(name string) bool { return strings.HasPrefix(name, "head.") }
	m.Use(g)
	tp := nn.NewTape(g)
	p := TapeParams(tp, m.Weights(), isHead)
	out := headStream(tp, p, m.Encode, frames)
	got, err := g.Download(out)
	if err != nil {
		t.Fatal(err)
	}
	m.Use(nn.CPU)
	if d := compare(got, headStream(nn.CPU, m, m.Encode, frames)); d > 1e-4 {
		t.Errorf("the tape's depth differs by %.3g of the range", d)
	}
	tp.Backward(out, g.Upload(dy))
	grads := map[string]*nn.Tensor{}
	for name, d := range tp.Grads() {
		if grads[name], err = g.Download(d); err != nil {
			t.Fatal(err)
		}
	}
	unused := func(name string) bool { // buffers, and the first fusion block's skip unit (it has no skip)
		return strings.Contains(name, "pos_encoder") || strings.Contains(name, "refinenet4.resConfUnit1")
	}
	for name := range m.Weights() {
		if isHead(name) && !unused(name) && grads[name] == nil {
			t.Errorf("%s: no gradient", name)
		}
	}
	// The loss's derivative along the gradient's own direction through a
	// set of weights (every weight's part adding up, none cancelling
	// another's), by central differences, against the gradients' dot with
	// it: the sets all the head, its motion modules, and their attention
	// projections alone (small gradients the others would drown).
	for _, set := range []struct {
		name string
		of   func(string) bool
	}{
		{"the head", func(string) bool { return true }},
		{"the motion modules", func(n string) bool { return strings.Contains(n, "motion_modules") }},
		{"the attention projections", func(n string) bool { return strings.Contains(n, "attention_blocks") && strings.Contains(n, ".to_") }},
	} {
		var peak float64
		for _, name := range slices.Sorted(maps.Keys(grads)) {
			if set.of(name) {
				for _, v := range grads[name].Data {
					peak = max(peak, math.Abs(float64(v)))
				}
			}
		}
		var pred float64
		dir := map[string][]float32{}
		for _, name := range slices.Sorted(maps.Keys(grads)) {
			if !set.of(name) {
				continue
			}
			d := grads[name]
			v := make([]float32, len(d.Data))
			for i := range v {
				v[i] = float32(float64(d.Data[i]) / peak)
				pred += float64(d.Data[i]) * float64(v[i])
			}
			dir[name] = v
		}
		// a small step, as the loss curves (ReLUs, softmaxes); the pass
		// is single precision, so the differences are a little off
		const eps = 3e-5
		{
			move := func(sign float64) {
				for name, v := range dir {
					w := m.Weights()[name].Data
					for i := range w {
						w[i] += float32(sign * eps * float64(v[i]))
					}
				}
			}
			move(1)
			up := loss()
			move(-2)
			down := loss()
			move(1)
			if by := (up - down) / (2 * eps); math.Abs(pred-by) > 3e-2*math.Abs(by) {
				t.Errorf("%s: the gradients say %v along a direction, the differences %v", set.name, pred, by)
			}
		}
	}
	g.Release()
}
