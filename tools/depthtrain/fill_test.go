package main

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/brunoga/bdtools/internal/nn"
	"github.com/brunoga/bdtools/internal/stereo"
)

func testFillSample(r *rand.Rand, n int) *fillSample {
	s := &fillSample{in: nn.New(stereo.FillChannels, n, n), holes: make([]bool, n*n), n: n}
	for i := range s.in.Data {
		s.in.Data[i] = float32(r.Float64() - 0.5)
	}
	for c := range 3 {
		s.want[c] = make([]float32, n*n)
		for i := range s.want[c] {
			s.want[c][i] = float32(r.Float64() - 0.5)
		}
	}
	for y := range n {
		for x := n / 3; x < n/2; x++ {
			s.holes[y*n+x] = true
			s.in.Data[3*n*n+y*n+x] = 1
		}
	}
	return s
}

// The loss's gradient is its derivative.
func TestFillLossGradient(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 3))
	s := testFillSample(r, 16)
	out := make([]float32, 3*16*16)
	for i := range out {
		out[i] = float32(r.Float64()-0.5) * 0.1
	}
	g, loss, ok := fillLoss(s, out)
	if !ok {
		t.Fatal("no holes")
	}
	const h = 1e-3
	checked := 0
	for i := 0; i < len(out); i += 7 {
		if !s.holes[i%(16*16)] {
			if g.Data[i] != 0 {
				t.Errorf("a gradient outside the holes at %d: %g", i, g.Data[i])
			}
			continue
		}
		out[i] += h
		_, up, _ := fillLoss(s, out)
		out[i] -= 2 * h
		_, down, _ := fillLoss(s, out)
		out[i] += h
		num := (up - down) / (2 * h)
		if math.Abs(num-float64(g.Data[i])) > 1e-3 {
			t.Errorf("at %d: gradient %g, numerically %g (loss %g)", i, g.Data[i], num, loss)
		}
		checked++
	}
	if checked < 10 {
		t.Fatalf("only %d checked", checked)
	}
}

// A filler at its start corrects nothing: the eye as Render made it.
func TestFillStartsAsRender(t *testing.T) {
	m := newFillModel(rand.New(rand.NewPCG(1, 1)))
	s := testFillSample(rand.New(rand.NewPCG(2, 2)), 32)
	out, err := nn.CPU.Download(m.Fill(nn.CPU, s.in))
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range out.Data {
		if v != 0 {
			t.Fatalf("a correction at %d: %g", i, v)
		}
	}
}

// A step on the CPU moves the loss down along its gradient.
func TestFillStepLearns(t *testing.T) {
	r := rand.New(rand.NewPCG(4, 4))
	m := newFillModel(r)
	s := testFillSample(r, 32)
	be := cpuGrads{nn.CPU}
	m.zeroGrad()
	first, ok := m.step(be, s)
	if !ok {
		t.Fatal("no holes")
	}
	for range 30 {
		m.zeroGrad()
		m.step(be, s)
		m.update(1e-3, 1)
	}
	m.zeroGrad()
	last, _ := m.step(be, s)
	if last >= first {
		t.Errorf("the loss %g after 30 steps, %g at the start", last, first)
	}
}
