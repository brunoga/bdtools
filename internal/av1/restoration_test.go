package av1

import (
	"fmt"
	"math"
	"testing"
)

// TestLRFixed codes pictures with fixed loop restoration settings: each
// plane type, every unit size and lr_uv_shift, the searched filters and
// random ones (the coefficients' whole ranges, every self-guided set),
// both bit depths, sizes not whole units or superblocks, with deblocking
// and CDEF, and inter frames.
func TestLRFixed(t *testing.T) {
	W, S, X := RESTORE_WIENER, RESTORE_SGRPROJ, RESTORE_SWITCHABLE
	sets := []lrSettings{
		{types: [3]int{W, W, W}},
		{types: [3]int{S, S, S}, unitShift: 1, uvShift: 1},
		{types: [3]int{X, X, X}, unitShift: 2},
		{types: [3]int{W, S, X}, uvShift: 1},
		{types: [3]int{S, RESTORE_NONE, W}, unitShift: 1},
		{types: [3]int{RESTORE_NONE, X, X}, unitShift: 2, uvShift: 1},
		{types: [3]int{W, W, W}, unitShift: 1, random: true},
		{types: [3]int{S, S, S}, random: true, uvShift: 1},
		{types: [3]int{X, X, X}, random: true},
		{types: [3]int{X, X, X}, unitShift: 2, uvShift: 1, random: true},
	}
	for _, c := range []struct{ w, h, depth, q, n int }{
		{200, 136, 8, 140, 1}, {90, 70, 10, 180, 1}, {264, 200, 8, 100, 1}, {136, 72, 10, 120, 3},
	} {
		o := EncoderOptions{Width: c.w, Height: c.h, BitDepth: c.depth, QIndex: c.q, FPSNum: 25, FPSDen: 1, MiniGOP: 2}
		p := testPicture(c.w, c.h, c.depth, 5)
		pics := []*Source{p}
		for i := 1; i < c.n; i++ {
			pics = append(pics, shifted(p, 2*i, i))
		}
		for i, s := range sets {
			t.Run(fmt.Sprintf("%dx%d-%d-set%d", c.w, c.h, c.depth, i), func(t *testing.T) {
				checkDecodesWith(t, o, pics, func(e *Encoder) { e.seq.restoration, e.lrFixed = true, &s })
			})
		}
	}
}

// TestLRTiles codes the units of pictures of several tiles (the
// references reset at each).
func TestLRTiles(t *testing.T) {
	o := EncoderOptions{Width: 264, Height: 200, BitDepth: 8, QIndex: 120, FPSNum: 25, FPSDen: 1, TileColsLog2: 1, TileRowsLog2: 1}
	for _, s := range []lrSettings{
		{types: [3]int{RESTORE_WIENER, RESTORE_SGRPROJ, RESTORE_SWITCHABLE}, random: true},
		{types: [3]int{RESTORE_SWITCHABLE, RESTORE_SWITCHABLE, RESTORE_SWITCHABLE}, unitShift: 1, uvShift: 1, random: true},
	} {
		checkDecodesWith(t, o, []*Source{testPicture(264, 200, 8, 6)}, func(e *Encoder) { e.seq.restoration, e.lrFixed = true, &s })
	}
}

// TestLRSearch codes pictures with the restoration searched and compares
// them with it off.
func TestLRSearch(t *testing.T) {
	for _, c := range []struct{ w, h, depth, q int }{
		{200, 136, 8, 60}, {200, 136, 8, 140}, {200, 136, 8, 220}, {264, 200, 10, 120}, {90, 70, 10, 200}, {320, 240, 8, 160},
	} {
		o := EncoderOptions{Width: c.w, Height: c.h, BitDepth: c.depth, QIndex: c.q, FPSNum: 25, FPSDen: 1}
		for k, pic := range []*Source{testPicture(c.w, c.h, c.depth, 7), smoothPicture(c.w, c.h, c.depth)} {
			var e, e0 *Encoder
			n, _ := checkDecodesWith(t, o, []*Source{pic}, func(enc *Encoder) { e, enc.seq.restoration = enc, true })
			fp := e.frame.fp
			n0, _ := checkDecodesWith(t, o, []*Source{pic}, func(enc *Encoder) { e0, enc.seq.restoration = enc, false })
			var d [3]float64
			for p := range 3 {
				d[p] = 10 * math.Log10(float64(e0.frame.planeSSE(p, e0.frame.rec[p]))/float64(e.frame.planeSSE(p, e.frame.rec[p])))
			}
			t.Logf("  picture %d: lr %v shift %d uv %d: Y %+.3f U %+.3f V %+.3f dB, %+d bytes (%+.2f%%)", k, fp.lrType, fp.lrUnitShift, fp.lrUVShift,
				d[0], d[1], d[2], n-n0, 100*float64(n-n0)/float64(n0))
		}
	}
}

// TestLRSubexp checks the subexponential code against a reading of it.
func TestLRSubexp(t *testing.T) {
	for _, c := range []struct{ low, high, k int }{{-5, 11, 1}, {-23, 9, 2}, {-17, 47, 3}, {-96, 32, 4}, {-32, 96, 4}} {
		for r := c.low; r < c.high; r++ {
			for v := c.low; v < c.high; v++ {
				var b bitRecorder
				writeSubexpRefBool(&b, c.low, c.high, c.k, r, v)
				if got := b.readSubexpRefBool(c.low, c.high, c.k, r); got != v || b.pos != len(b.bits) {
					t.Fatalf("%v r %d: wrote %d, read %d (%d of %d bits)", c, r, v, got, b.pos, len(b.bits))
				}
			}
		}
	}
}

// bitRecorder records equiprobable bits (a coder of literals only) and
// reads them back.
type bitRecorder struct {
	bits []bool
	pos  int
}

func (b *bitRecorder) symbol(int, []uint16)      { panic("symbol") }
func (b *bitRecorder) encode(int, []uint16, int) { panic("encode") }
func (b *bitRecorder) boolean(v bool)            { b.bits = append(b.bits, v) }
func (b *bitRecorder) literal(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		b.boolean(v>>uint(i)&1 != 0)
	}
}

func (b *bitRecorder) l(n int) int {
	x := 0
	for range n {
		x = 2*x + b2i(b.bits[b.pos])
		b.pos++
	}
	return x
}

func (b *bitRecorder) readSubexpRefBool(low, high, k, r int) int {
	mx := high - low
	r -= low
	// decode_subexp_bool
	v := func() int {
		i, mk := 0, 0
		for {
			b2 := k
			if i > 0 {
				b2 = k + i - 1
			}
			a := 1 << uint(b2)
			if mx <= mk+3*a {
				n := mx - mk
				w := 0
				for 1<<uint(w) <= n {
					w++
				}
				m := 1<<uint(w) - n
				x := b.l(w - 1)
				if x >= m {
					x = x<<1 - m + b.l(1)
				}
				return x + mk
			}
			if b.l(1) == 0 {
				return b.l(b2) + mk
			}
			i++
			mk += a
		}
	}()
	inv := func(r, v int) int {
		switch {
		case v > 2*r:
			return v
		case v&1 != 0:
			return r - (v+1)>>1
		}
		return r + v>>1
	}
	if r<<1 <= mx {
		return inv(r, v) + low
	}
	return mx - 1 - inv(mx-1-r, v) + low
}
