package dovi

import (
	"math/rand/v2"
	"testing"
)

// A whole composition with the AVX2 kernels matches the Go one: luma
// exactly, chroma within a code (the MMR kernel's FMA rounds once).
func TestComposeAVX2MatchesGo(t *testing.T) {
	if !useAVX2 {
		t.Skip("no AVX2")
	}
	c, err := NewComposer(fixtureRPU(t, "fel-cmv29.bin"))
	if err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewPCG(11, 12))
	const w, h = 214, 70 // tails everywhere
	bl := randomPicture(w, h, r, 64, 940)
	el := randomPicture(w/2, h/2, r, 400, 624)
	got, want := newPicture(w, h), newPicture(w, h)
	if err := c.Compose(got, bl, el); err != nil {
		t.Fatal(err)
	}
	useAVX2 = false
	err = c.Compose(want, bl, el)
	useAVX2 = true
	if err != nil {
		t.Fatal(err)
	}
	for i := range len(got.Y) / 2 {
		if getSample(got.Y, i) != getSample(want.Y, i) {
			t.Fatalf("luma %d: %d, Go %d", i, getSample(got.Y, i), getSample(want.Y, i))
		}
	}
	for i := range len(got.UV) / 2 {
		if d := getSample(got.UV, i) - getSample(want.UV, i); d < -1 || d > 1 {
			t.Fatalf("chroma %d: %d, Go %d", i, getSample(got.UV, i), getSample(want.UV, i))
		}
	}
}
