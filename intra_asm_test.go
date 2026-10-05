//go:build amd64 && !purego

package mvc

import (
	"math/rand"
	"testing"
)

func TestIntraAsm(t *testing.T) {
	if !useAVX2Asm {
		t.Skip("no AVX2")
	}
	rng := rand.New(rand.NewSource(31))
	const st = 48
	for iter := 0; iter < 40000; iter++ {
		var a, b [st * 24]byte
		for i := range a {
			a[i] = byte(rng.Intn(256))
		}
		b = a
		av := intraAvail{left: rng.Intn(2) == 0, top: rng.Intn(2) == 0, topRight: rng.Intn(2) == 0, topLeft: rng.Intn(2) == 0}
		mode := rng.Intn(9)
		// modes needing unavailable samples are never selected by a valid
		// stream; skip them
		switch mode {
		case 0, 3, 7:
			if !av.top {
				continue
			}
		case 1, 8:
			if !av.left {
				continue
			}
		case 4, 5, 6:
			if !av.top || !av.left || !av.topLeft {
				continue
			}
		}
		off := 4*st + 8
		if iter&1 == 0 {
			pred4x4(a[:], off, st, mode, av)
			pred4x4Generic(b[:], off, st, mode, av)
		} else {
			pred8x8L(a[:], off, st, mode, av)
			pred8x8LGeneric(b[:], off, st, mode, av)
		}
		if a != b {
			n := 4 + 4*(iter&1)
			for y := 0; y < n; y++ {
				t.Logf("row %d: %v vs %v", y, a[off+y*st:off+y*st+n], b[off+y*st:off+y*st+n])
			}
			t.Fatalf("iter %d: %dx%d mode %d avail %+v mismatch", iter, n, n, mode, av)
		}
	}
	for iter := 0; iter < 5000; iter++ {
		var a, b [st * 24]byte
		for i := range a {
			a[i] = byte(rng.Intn(256))
		}
		b = a
		w := 16
		if iter&1 == 1 {
			w = 8
		}
		predPlane(a[:], 2*st+4, st, w, w)
		predPlaneGeneric(b[:], 2*st+4, st, w, w)
		if a != b {
			t.Fatalf("iter %d: plane %dx%d mismatch", iter, w, w)
		}
	}
}

func TestIntraChromaAsm(t *testing.T) {
	if !useAVX2Asm {
		t.Skip("no AVX2")
	}
	rng := rand.New(rand.NewSource(51))
	const st = 48
	for iter := 0; iter < 20000; iter++ {
		var a, b [st * 24]byte
		for i := range a {
			a[i] = byte(rng.Intn(256))
		}
		b = a
		top, left := rng.Intn(2) == 0, rng.Intn(2) == 0
		mode := rng.Intn(3)
		if mode == 1 && !left || mode == 2 && !top {
			continue
		}
		predChroma(a[:], 4*st+8, st, mode, top, left)
		predChromaGeneric(b[:], 4*st+8, st, mode, top, left)
		if a != b {
			t.Fatalf("iter %d: chroma mode %d top %v left %v mismatch", iter, mode, top, left)
		}
	}
}
