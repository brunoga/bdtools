package main

import (
	"math"
	"slices"
	"testing"
)

// A textured picture shifted by a known amount: the flow finds it.
func TestFlowLKShift(t *testing.T) {
	const w, h = 320, 180
	tex := func(x, y float64) float32 {
		return float32(128 + 50*math.Sin(x*0.31+y*0.17) + 40*math.Sin(x*0.07-y*0.23) + 20*math.Cos(x*0.53*math.Sin(y*0.05)))
	}
	const du, dv = 6.5, -3.25
	a, b := make([]float32, w*h), make([]float32, w*h)
	for y := range h {
		for x := range w {
			a[y*w+x] = tex(float64(x), float64(y))
			b[y*w+x] = tex(float64(x)-du, float64(y)-dv) // b(x + d) = a(x)
		}
	}
	u, v, _ := flowLK(a, b, w, h)
	i := 90*w + 160
	if math.Abs(float64(u[i])-du) > 0.1 || math.Abs(float64(v[i])-dv) > 0.1 {
		t.Errorf("flow %.3f %.3f, want %.3f %.3f", u[i], v[i], du, dv)
	}
}

// Blurred noise (a picture's detail) shifted by whole pixels: the flow
// finds the shift, to a tenth of a pixel at the median.
func TestFlowLKInteger(t *testing.T) {
	const w, h = 480, 270
	r := uint32(3)
	noise := make([]float32, w*h)
	for i := range noise {
		r = r*1664525 + 1013904223
		noise[i] = float32(r>>24) - 128
	}
	for _, rad := range []int{1, 3} {
		a := boxF(noise, w, h, rad)
		for _, d := range [][2]int{{1, 0}, {3, 2}, {8, -5}} {
			b := make([]float32, w*h)
			for y := range h {
				for x := range w {
					b[y*w+x] = a[min(max(y-d[1], 0), h-1)*w+min(max(x-d[0], 0), w-1)]
				}
			}
			u, v, _ := flowLK(a, b, w, h)
			var e []float64
			for y := 30; y < h-30; y++ {
				for x := 30; x < w-30; x++ {
					e = append(e, math.Hypot(float64(u[y*w+x])-float64(d[0]), float64(v[y*w+x])-float64(d[1])))
				}
			}
			slices.Sort(e)
			if p50 := e[len(e)/2]; p50 > 0.1 {
				t.Errorf("blur %d shift %v: error p50 %.3f", rad, d, p50)
			}
		}
	}
}
