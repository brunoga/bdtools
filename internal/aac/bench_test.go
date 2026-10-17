package aac

import "testing"

func BenchmarkEncode5_1(b *testing.B) {
	e, err := NewEncoder(6, true, 48000, 384)
	if err != nil {
		b.Fatal(err)
	}
	in := signal(6, 1)
	pcm := make([][]float64, 6)
	for c := range pcm {
		pcm[c] = in[c][:FrameSamples]
	}
	for b.Loop() {
		if _, err := e.EncodeFrame(pcm); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.N)*FrameSamples/48000/b.Elapsed().Seconds(), "x-realtime")
}
