//go:build amd64 && !purego

package av1

import (
	"math/rand/v2"
	"testing"
)

// The AVX2 directional rows and gathers give the scalar samples, every
// width and shift, at 12 bits.
func TestDirKernels(t *testing.T) {
	if !useAVX2 {
		t.Skip("no AVX2")
	}
	rng := rand.New(rand.NewPCG(7, 8))
	edge := make([]int32, 300)
	for trial := range 2000 {
		for i := range edge {
			edge[i] = rng.Int32N(4096)
		}
		w := 1 + rng.IntN(64)
		sh := rng.Int32N(32)
		got, want := make([]uint16, w), make([]uint16, w)
		a := edge[3 : 3+w+1]
		dirRow(got, a, sh)
		for j := range want {
			want[j] = uint16((a[j]*(32-sh) + a[j+1]*sh + 16) >> 5) //nolint:gosec // a sample
		}
		if string(u16bytes(got)) != string(u16bytes(want)) {
			t.Fatalf("trial %d row: %v, want %v", trial, got, want)
		}
		var base, shs [64]int32
		for j := range w {
			base[j], shs[j] = rng.Int32N(140)-70, rng.Int32N(32)
		}
		il := rng.Int32N(64)
		dirGather(got, edge, 80, base[:], shs[:], il)
		for j := range want {
			b := 80 + int(il+base[j])
			want[j] = uint16((edge[b]*(32-shs[j]) + edge[b+1]*shs[j] + 16) >> 5) //nolint:gosec // a sample
		}
		if string(u16bytes(got)) != string(u16bytes(want)) {
			t.Fatalf("trial %d gather: %v, want %v", trial, got, want)
		}
	}
}

func u16bytes(s []uint16) []byte {
	b := make([]byte, 2*len(s))
	for i, v := range s {
		b[2*i], b[2*i+1] = byte(v), byte(v>>8)
	}
	return b
}
