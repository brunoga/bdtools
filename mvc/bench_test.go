package mvc

import (
	"bytes"
	"os"
	"testing"
)

func benchFile(b *testing.B, threads int) {
	name := os.Getenv("MVC_BENCH_FILE")
	if name == "" {
		b.Skip("set MVC_BENCH_FILE")
	}
	data, err := os.ReadFile(name)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(data)))
	frames := 0
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// decode per access unit and drain after each, as a realtime
		// consumer does (and as edge264's test harness does)
		d := NewDecoder(Options{Threads: threads})
		r := NewAUReader(bytes.NewReader(data))
		for {
			au, err := r.Next()
			if err != nil {
				break
			}
			d.DecodeAU(au, -1)
			for {
				f, ok := d.NextFrame()
				if !ok {
					break
				}
				frames++
				f.Release()
			}
		}
		d.Flush()
		for {
			f, ok := d.NextFrame()
			if !ok {
				break
			}
			frames++
			f.Release()
		}
	}
	b.ReportMetric(float64(frames)/b.Elapsed().Seconds(), "frames/s")
}

// BenchmarkFileST decodes $MVC_BENCH_FILE with one picture in flight.
func BenchmarkFileST(b *testing.B) { benchFile(b, 1) }

// BenchmarkFileMT decodes $MVC_BENCH_FILE with all CPUs.
func BenchmarkFileMT(b *testing.B) { benchFile(b, 0) }
