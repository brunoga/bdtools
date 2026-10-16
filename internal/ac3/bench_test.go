package ac3

import (
	"math"
	"testing"
)

// BenchmarkEncode5_1 codes a 5.1 frame at 640 kb/s.
func BenchmarkEncode5_1(b *testing.B) {
	e, err := NewEncoder(6, true, 640)
	if err != nil {
		b.Fatal(err)
	}
	pcm := tone(6, FrameSamples, 440)
	for b.Loop() {
		if _, err := e.EncodeFrame(pcm); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.N)*FrameSamples/48000/b.Elapsed().Seconds(), "x-realtime")
}

// The fast transform is the direct one.
func TestMDCT(t *testing.T) {
	var x [512]float64
	seed := uint32(3)
	for i := range x {
		seed = seed*1664525 + 1013904223
		x[i] = float64(seed>>8)/(1<<24) - 0.5
	}
	var fast [256]float64
	mdct(&x, &fast, 1)
	for k := range 256 {
		s := 0.0
		for n, v := range x {
			s += v * math.Cos(math.Pi/256*(float64(n)+0.5+128)*(float64(k)+0.5))
		}
		if d := math.Abs(s - fast[k]); d > 1e-9 {
			t.Fatalf("coefficient %d: %g, direct %g", k, fast[k], s)
		}
	}
}
