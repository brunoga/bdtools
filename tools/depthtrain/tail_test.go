package main

import (
	"math"
	"math/rand/v2"
	"testing"
)

func TestLossGradFixed(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8))
	s := testSample(r)
	out := make([]float32, len(s.depth))
	for i, v := range s.depth {
		out[i] = v * 1.3
	}
	const a, b = 1.7, -0.4
	g, _, ok := lossGradFixed(s, out, a, b, s.h, s.w)
	if !ok {
		t.Fatal("no fit")
	}
	loss := func() float64 {
		_, l, _ := lossGradFixed(s, out, a, b, s.h, s.w)
		return l
	}
	for range 20 {
		i := r.IntN(len(out))
		if math.IsNaN(float64(s.disp[i])) {
			continue
		}
		v := out[i]
		const eps = 1e-2
		out[i] = v + eps
		up := loss()
		out[i] = v - eps
		down := loss()
		out[i] = v
		want := (up - down) / (2 * eps)
		if got := float64(g.Data[i]); math.Abs(got-want) > 0.02*math.Abs(want)+1e-6 {
			t.Errorf("[%d]: gradient %v, by differences %v", i, got, want)
		}
	}
}

func TestStretchFitIsFit(t *testing.T) {
	// one sample's stretch fit is its own fit
	r := rand.New(rand.NewPCG(9, 10))
	s := testSample(r)
	ts := &tailSample{sample: s}
	a, b, n := stretchFit([]*tailSample{ts}, [][]float32{s.depth})
	a2, b2, n2 := fit(s, s.depth, s.w)
	if n != n2 || math.Abs(a-a2) > 1e-9 || math.Abs(b-b2) > 1e-9 {
		t.Fatalf("stretch fit %v %v %d, fit %v %v %d", a, b, n, a2, b2, n2)
	}
}

func TestStretches(t *testing.T) {
	g := stretches([]string{"d/film@25m-000010.bht", "d/film@25m-000002.bht", "d/film@60m-000001.bht", "d/other-000001.bht"})
	if len(g) != 3 || len(g[0]) != 2 || g[0][0] != "d/film@25m-000002.bht" || len(g[1]) != 1 || len(g[2]) != 1 {
		t.Fatalf("groups %v", g)
	}
}
