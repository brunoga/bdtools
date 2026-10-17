package mvc

import (
	"math/rand"
	"testing"
)

// satd4x4 is satdGeneric's 4x4 case.
func TestSATD4x4(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	src := make([]byte, 64)
	pred := make([]byte, 16)
	for range 1000 {
		for i := range src {
			src[i] = byte(rng.Intn(256))
		}
		for i := range pred {
			pred[i] = byte(rng.Intn(256))
		}
		if got, want := satd4x4(src, 3, 16, pred), satdGeneric(src, 3, 16, pred, 0, 4, 4, 4); got != want {
			t.Fatalf("satd4x4 %d, want %d", got, want)
		}
	}
}
