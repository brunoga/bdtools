package av1

import (
	"math/rand/v2"
	"slices"
	"testing"
)

// wideFilter matches the spec's sums (wideFilterGo) for each of its uses.
func TestWideFilterMatches(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	for _, c := range []struct {
		n, n2 int
		log2  uint
	}{{6, 1, 4}, {3, 0, 3}, {2, 1, 3}} {
		for range 200 {
			buf := make([]uint16, 64)
			for i := range buf {
				buf[i] = uint16(rng.IntN(1 << 10))
			}
			got, want := slices.Clone(buf), slices.Clone(buf)
			wideFilter(got, 30, 2, c.n, c.n2, c.log2)
			wideFilterGo(want, 30, 2, c.n, c.n2, c.log2)
			if !slices.Equal(got, want) {
				t.Fatalf("n %d n2 %d: %v, want %v", c.n, c.n2, got, want)
			}
		}
	}
}

// wideFilterGo is the wide filter process as the spec writes it.
func wideFilterGo(buf []uint16, pos, step, n, n2 int, log2Size uint) {
	var s [14]int32
	for k := -(n + 1); k <= n; k++ {
		s[k+n+1] = int32(buf[pos+k*step])
	}
	var out [12]int32
	for i := -n; i < n; i++ {
		var t int32
		for j := -n; j <= n; j++ {
			k := min(max(i+j, -(n+1)), n)
			v := s[k+n+1]
			if j >= -n2 && j <= n2 {
				v *= 2
			}
			t += v
		}
		out[i+n] = (t + 1<<(log2Size-1)) >> log2Size
	}
	for i := -n; i < n; i++ {
		buf[pos+i*step] = uint16(out[i+n]) //nolint:gosec // a sample
	}
}
