//go:build amd64 && !purego

package av1

import (
	"math/rand/v2"
	"testing"
	"unsafe"
)

// cdefParams' layout is the one cdefsearch_amd64.s addresses.
func TestCdefParamsLayout(t *testing.T) {
	var p cdefParams
	for _, c := range []struct {
		name      string
		off, want uintptr
	}{
		{"priT", unsafe.Offsetof(p.priT), 128}, {"priSh", unsafe.Offsetof(p.priSh), 192},
		{"tap0", unsafe.Offsetof(p.tap0), 256}, {"tap1", unsafe.Offsetof(p.tap1), 320},
		{"secT", unsafe.Offsetof(p.secT), 384}, {"secSh", unsafe.Offsetof(p.secSh), 400},
		{"d", unsafe.Offsetof(p.d), 416}, {"p", unsafe.Offsetof(p.p), 1056},
		{"s", unsafe.Offsetof(p.s), 1568}, {"s0", unsafe.Offsetof(p.s0), 1696},
		{"acc", unsafe.Offsetof(p.acc), 1824},
	} {
		if c.off != c.want {
			t.Errorf("%s at %d, want %d", c.name, c.off, c.want)
		}
	}
}

// The AVX2 search of a whole 8x8 luma block adds cdefSearchBlock's
// errors, at every bit depth and direction, with windows reaching past
// the picture (cdefVeryLarge).
func TestCdefSearch8Kernel(t *testing.T) {
	if !useAVX2 {
		t.Skip("no AVX2")
	}
	rng := rand.New(rand.NewPCG(5, 6))
	var cp cdefParams
	for trial := range 3000 {
		bd := []int{8, 10, 12}[trial%3]
		cs := bd - 8
		ucs := uint(cs) //nolint:gosec // small
		damping := 3 + rng.IntN(4)
		maxV := int32(1)<<uint(bd) - 1 //nolint:gosec // small
		win := make([]int32, 12*12)
		base := rng.Int32N(maxV + 1)
		spread := []int32{2, 16, maxV}[trial%3/1%3]
		for i := range win {
			win[i] = min(max(base+rng.Int32N(2*spread+1)-spread, 0), maxV)
		}
		if trial%4 == 0 { // an edge
			for i := range 12 {
				for j := range 2 {
					win[i*12+j] = cdefVeryLarge
				}
			}
		}
		if trial%5 == 0 {
			for j := range 12 {
				win[j], win[12+j] = cdefVeryLarge, cdefVeryLarge
			}
		}
		src := make([]uint16, 8*10)
		for i := range src {
			src[i] = uint16(min(max(base+rng.Int32N(2*spread+1)-spread, 0), maxV)) //nolint:gosec // a sample
		}
		dir := rng.IntN(8)
		v := rng.IntN(1 << 12)
		var secT [4]int32
		var secSh [4]uint
		for si := 1; si < 4; si++ {
			s := cdefSecValues[si] << ucs
			secT[si], secSh[si] = int32(s), constrainShift(s, damping+cs) //nolint:gosec // small
		}
		var priT [16]int32
		var priSh [16]uint
		var priTaps [16][2]int32
		for pi := 1; pi < 16; pi++ {
			s := cdefAdjust(pi<<ucs, v)
			priT[pi] = int32(s) //nolint:gosec // small
			if s > 0 {
				priSh[pi] = constrainShift(s, damping+cs)
			}
			tp := cdefPriTaps[(s>>ucs)&1]
			priTaps[pi] = [2]int32{int32(tp[0]), int32(tp[1])}
		}
		var want, got [64]int64
		cdefSearchBlock(win, 8, 8, 8, src, 10, dir, &priT, &priSh, &priTaps, &secT, &secSh, &want)
		if !cdefSearch8(win, 8, 8, 8, src, 10, dir, &priT, &priSh, &priTaps, &secT, &secSh, &got, &cp) {
			t.Fatal("not taken")
		}
		if got != want {
			t.Fatalf("trial %d (bd %d dir %d): %v, want %v", trial, bd, dir, got, want)
		}
	}
}
