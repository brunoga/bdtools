package main

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/brunoga/bdtools/internal/nn"
	"github.com/brunoga/bdtools/internal/stereo"
)

// testSample is a small sample: a picture of a few shapes, a depth not
// quite the disc's.
func testSample(r *rand.Rand) *sample {
	const w, h = 40, 24
	s := &sample{w: w, h: h, y: make([]byte, w*h), cb: make([]byte, w*h), cr: make([]byte, w*h),
		depth: make([]float32, w*h), disp: make([]float32, w*h)}
	for i := range s.y {
		x, y := i%w, i/w
		s.y[i] = byte(40 + 3*x + 2*y + r.IntN(20))
		s.cb[i], s.cr[i] = byte(120+r.IntN(16)), byte(120+r.IntN(16))
		near := float32(0)
		if x > 10 && x < 25 && y > 5 && y < 18 {
			near = 1
		}
		s.depth[i] = float32(y)/10 + near*0.5 + float32(r.NormFloat64()*0.05)
		s.disp[i] = 2*float32(y)/10 + 3*near
		if r.IntN(5) == 0 {
			s.disp[i] = float32(math.NaN())
		}
	}
	return s
}

func TestStepGradient(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	m := newModel(r)
	// the last layer not zero, so every layer has a gradient
	for i := range m.W["out"].Data {
		m.W["out"].Data[i] = float32(r.NormFloat64() * 0.1)
	}
	s := testSample(r)
	m.zeroGrad()
	m.step(s)
	loss := func() float64 {
		mm := &model{RefineWeights: m.RefineWeights}
		mm.zeroGrad()
		l, _ := mm.step(s)
		return l
	}
	for _, name := range []string{"e1a", "e3b", "e4a", "u2", "d1", "out"} {
		w, g := m.W[name], m.gw[name]
		checked := 0
		for range 40 {
			i := r.IntN(len(w.Data))
			if math.Abs(float64(g.Data[i])) < 1e-5 {
				continue
			}
			v := w.Data[i]
			const eps = 1e-3 // float32 convolutions: the differences are noisy below 2e-4
			w.Data[i] = v + eps
			up := loss()
			w.Data[i] = v - eps
			down := loss()
			w.Data[i] = v
			want := (up - down) / (2 * eps)
			if got := float64(g.Data[i]); math.Abs(got-want) > 0.05*math.Abs(want)+2e-4 {
				t.Errorf("%s[%d]: gradient %v, by differences %v", name, i, got, want)
			}
			if checked++; checked == 4 {
				break
			}
		}
	}
}

func TestStepIsRefine(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8))
	m := newModel(r)
	for i := range m.W["out"].Data {
		m.W["out"].Data[i] = float32(r.NormFloat64() * 0.1)
	}
	s := testSample(r)
	x, _, _ := stereo.RefineInput(s.y, s.cb, s.cr, s.w, s.depth, s.w, s.h)
	y := m.Refine(nn.CPU, x)
	a, b, _ := fit(s, y.Data, x.Dim(2))
	// the loss by hand, from Refine's output
	var want float64
	var n int
	for rr := range s.h {
		for c := range s.w {
			if d := s.disp[rr*s.w+c]; !math.IsNaN(float64(d)) {
				res := a*float64(y.Data[rr*x.Dim(2)+c]) + b - float64(d)
				want += math.Sqrt(res*res+1) - 1
				n++
			}
		}
	}
	m.zeroGrad()
	got, _ := m.step(s)
	if math.Abs(got-want/float64(n)) > 1e-6 {
		t.Errorf("the training pass's loss %v, Refine's %v", got, want/float64(n))
	}
}
