package av1

// The spec's butterfly networks as step lists, built from its step
// descriptions (7.13.2.3, 7.13.2.7, 7.13.2.8), the reference that
// transform_bf.go is written out from (go test -run TestButterflyUnrolled
// -write-bf rewrites it) and is checked against.

import (
	"flag"
	"fmt"
	"go/format"
	"os"
	"strings"
	"testing"
)

var writeBF = flag.Bool("write-bf", false, "rewrite transform_bf.go from the step lists")

// cos128 and sin128 are the spec's Q12 cosine and sine of angle*pi/128.
func cos128(angle int) int32 {
	a := angle & 255
	switch {
	case a <= 64:
		return int32(cos128Lookup[a])
	case a <= 128:
		return -int32(cos128Lookup[128-a])
	case a <= 192:
		return -int32(cos128Lookup[a-128])
	}
	return int32(cos128Lookup[256-a])
}

func sin128(angle int) int32 { return cos128(angle - 64) }

func brev(numBits, x int) int {
	t := 0
	for i := 0; i < numBits; i++ {
		t |= (x >> i & 1) << (numBits - 1 - i)
	}
	return t
}

// A butterfly step. A rotation reads T[a] and T[b] and writes
// Round2(T[a]*c - T[b]*s, 12) to T[oa] and Round2(T[a]*s + T[b]*c, 12) to
// T[ob] (oa and ob swapped for the spec's flipped B). A Hadamard step
// (hadamard set) writes the clamped T[a]+T[b] to T[a] and T[a]-T[b] to
// T[b] (a and b swapped for the flipped H).
type bfStep struct {
	a, b, oa, ob uint8
	hadamard     bool
	c, s         int32
}

// bfList is a butterfly network, its steps in order.
type bfList []bfStep

func (l *bfList) B(a, b, angle, flip int) {
	st := bfStep{a: uint8(a), b: uint8(b), oa: uint8(a), ob: uint8(b), c: cos128(angle), s: sin128(angle)}
	if flip != 0 {
		st.oa, st.ob = st.ob, st.oa
	}
	*l = append(*l, st)
}

func (l *bfList) H(a, b, flip int) {
	if flip != 0 {
		a, b = b, a
	}
	*l = append(*l, bfStep{a: uint8(a), b: uint8(b), hadamard: true})
}

// run applies the steps to t with Hadamard results clamped to r bits.
func (l bfList) run(t *[64]int32, r int) {
	lo, hi := -int32(1)<<(r-1), int32(1)<<(r-1)-1
	for i := range l {
		st := &l[i]
		x, y := t[st.a&63], t[st.b&63]
		if st.hadamard {
			t[st.a&63] = min(max(x+y, lo), hi)
			t[st.b&63] = min(max(x-y, lo), hi)
			continue
		}
		u := int64(x)*int64(st.c) - int64(y)*int64(st.s)
		v := int64(x)*int64(st.s) + int64(y)*int64(st.c)
		t[st.oa&63] = int32((u + 2048) >> 12)
		t[st.ob&63] = int32((v + 2048) >> 12)
	}
}

// idctSteps holds the inverse DCT of 2^n samples (n = 2..6) as steps,
// following the ordered steps of spec 7.13.2.3; idctPerm the input
// permutation (7.13.2.2).
var (
	idctSteps    [7]bfList
	idctPerm     [7][64]uint8
	iadst8Steps  bfList
	iadst16Steps bfList
	// iadstIn and iadstOut are the ADST input and output permutations
	// (7.13.2.4, 7.13.2.5) for n = 3 and 4.
	iadstIn  [5][16]uint8
	iadstOut [5][16]uint8
)

func init() {
	for n := 2; n <= 6; n++ {
		for i := 0; i < 1<<n; i++ {
			idctPerm[n][i] = uint8(brev(n, i))
		}
		idctSteps[n] = buildIDCT(n)
	}
	for n := 3; n <= 4; n++ {
		n0 := 1 << n
		for i := 0; i < n0; i++ {
			if i&1 != 0 {
				iadstIn[n][i] = uint8(i - 1)
			} else {
				iadstIn[n][i] = uint8(n0 - i - 1)
			}
			a := i >> 3 & 1
			b := (i>>2)&1 ^ (i>>3)&1
			c := (i>>1)&1 ^ (i>>2)&1
			d := i&1 ^ (i>>1)&1
			iadstOut[n][i] = uint8((d<<3 | c<<2 | b<<1 | a) >> (4 - n))
		}
	}
	l := &iadst8Steps
	for i := 0; i < 4; i++ {
		l.B(2*i, 2*i+1, 60-16*i, 1)
	}
	for i := 0; i < 4; i++ {
		l.H(i, 4+i, 0)
	}
	for i := 0; i < 2; i++ {
		l.B(4+3*i, 5+i, 48-32*i, 1)
	}
	for i := 0; i < 2; i++ {
		for j := 0; j < 2; j++ {
			l.H(4*j+i, 2+4*j+i, 0)
		}
	}
	for i := 0; i < 2; i++ {
		l.B(2+4*i, 3+4*i, 32, 1)
	}
	l = &iadst16Steps
	for i := 0; i < 8; i++ {
		l.B(2*i, 2*i+1, 62-8*i, 1)
	}
	for i := 0; i < 8; i++ {
		l.H(i, 8+i, 0)
	}
	for i := 0; i < 2; i++ {
		l.B(8+2*i, 9+2*i, 56-32*i, 1)
		l.B(13+2*i, 12+2*i, 8+32*i, 1)
	}
	for i := 0; i < 4; i++ {
		for j := 0; j < 2; j++ {
			l.H(8*j+i, 4+8*j+i, 0)
		}
	}
	for i := 0; i < 2; i++ {
		for j := 0; j < 2; j++ {
			l.B(4+8*j+3*i, 5+8*j+i, 48-32*i, 1)
		}
	}
	for i := 0; i < 2; i++ {
		for j := 0; j < 4; j++ {
			l.H(4*j+i, 2+4*j+i, 0)
		}
	}
	for i := 0; i < 4; i++ {
		l.B(2+4*i, 3+4*i, 32, 1)
	}
}

// buildIDCT lists the steps of spec 7.13.2.3 for 2^n samples, skipping the
// ones the size excludes.
func buildIDCT(n int) bfList {
	var l bfList
	if n == 6 {
		for i := 0; i < 16; i++ {
			l.B(32+i, 63-i, 63-4*brev(4, i), 0)
		}
	}
	if n >= 5 {
		for i := 0; i < 8; i++ {
			l.B(16+i, 31-i, 6+(brev(3, 7-i)<<3), 0)
		}
	}
	if n == 6 {
		for i := 0; i < 16; i++ {
			l.H(32+i*2, 33+i*2, i&1)
		}
	}
	if n >= 4 {
		for i := 0; i < 4; i++ {
			l.B(8+i, 15-i, 12+(brev(2, 3-i)<<4), 0)
		}
	}
	if n >= 5 {
		for i := 0; i < 8; i++ {
			l.H(16+2*i, 17+2*i, i&1)
		}
	}
	if n == 6 {
		for i := 0; i < 4; i++ {
			for j := 0; j < 2; j++ {
				l.B(62-i*4-j, 33+i*4+j, 60-16*brev(2, i)+64*j, 1)
			}
		}
	}
	if n >= 3 {
		for i := 0; i < 2; i++ {
			l.B(4+i, 7-i, 56-32*i, 0)
		}
	}
	if n >= 4 {
		for i := 0; i < 4; i++ {
			l.H(8+2*i, 9+2*i, i&1)
		}
	}
	if n >= 5 {
		for i := 0; i < 2; i++ {
			for j := 0; j < 2; j++ {
				l.B(30-4*i-j, 17+4*i+j, 24+(j<<6)+((1-i)<<5), 1)
			}
		}
	}
	if n == 6 {
		for i := 0; i < 8; i++ {
			for j := 0; j < 2; j++ {
				l.H(32+i*4+j, 35+i*4-j, i&1)
			}
		}
	}
	for i := 0; i < 2; i++ {
		l.B(2*i, 2*i+1, 32+16*i, 1-i)
	}
	if n >= 3 {
		for i := 0; i < 2; i++ {
			l.H(4+2*i, 5+2*i, i)
		}
	}
	if n >= 4 {
		for i := 0; i < 2; i++ {
			l.B(14-i, 9+i, 48+64*i, 1)
		}
	}
	if n >= 5 {
		for i := 0; i < 4; i++ {
			for j := 0; j < 2; j++ {
				l.H(16+4*i+j, 19+4*i-j, i&1)
			}
		}
	}
	if n == 6 {
		for i := 0; i < 2; i++ {
			for j := 0; j < 4; j++ {
				l.B(61-i*8-j, 34+i*8+j, 56-i*32+(j>>1)*64, 1)
			}
		}
	}
	for i := 0; i < 2; i++ {
		l.H(i, 3-i, 0)
	}
	if n >= 3 {
		l.B(6, 5, 32, 1)
	}
	if n >= 4 {
		for i := 0; i < 2; i++ {
			for j := 0; j < 2; j++ {
				l.H(8+4*i+j, 11+4*i-j, i)
			}
		}
	}
	if n >= 5 {
		for i := 0; i < 4; i++ {
			l.B(29-i, 18+i, 48+(i>>1)*64, 1)
		}
	}
	if n == 6 {
		for i := 0; i < 4; i++ {
			for j := 0; j < 4; j++ {
				l.H(32+8*i+j, 39+8*i-j, i&1)
			}
		}
	}
	if n >= 3 {
		for i := 0; i < 4; i++ {
			l.H(i, 7-i, 0)
		}
	}
	if n >= 4 {
		for i := 0; i < 2; i++ {
			l.B(13-i, 10+i, 32, 1)
		}
	}
	if n >= 5 {
		for i := 0; i < 2; i++ {
			for j := 0; j < 4; j++ {
				l.H(16+i*8+j, 23+i*8-j, i)
			}
		}
	}
	if n == 6 {
		for i := 0; i < 8; i++ {
			a := 112
			if i < 4 {
				a = 48
			}
			l.B(59-i, 36+i, a, 1)
		}
	}
	if n >= 4 {
		for i := 0; i < 8; i++ {
			l.H(i, 15-i, 0)
		}
	}
	if n >= 5 {
		for i := 0; i < 4; i++ {
			l.B(27-i, 20+i, 32, 1)
		}
	}
	if n == 6 {
		for i := 0; i < 8; i++ {
			l.H(32+i, 47-i, 0)
			l.H(48+i, 63-i, 1)
		}
	}
	if n >= 5 {
		for i := 0; i < 16; i++ {
			l.H(i, 31-i, 0)
		}
	}
	if n == 6 {
		for i := 0; i < 8; i++ {
			l.B(55-i, 40+i, 32, 1)
		}
	}
	if n == 6 {
		for i := 0; i < 32; i++ {
			l.H(i, 63-i, 0)
		}
	}
	return l
}

// idctRef is the inverse DCT process (7.13.2.3) of 2^n samples of t, by
// the step list.
func idctRef(t *[64]int32, n, r int) {
	var c [64]int32
	m := 1 << n
	copy(c[:m], t[:m])
	p := &idctPerm[n]
	for i := 0; i < m; i++ {
		t[i] = c[p[i]]
	}
	idctSteps[n].run(t, r)
}

// iadstRef is the inverse ADST process (7.13.2.9) of 2^n samples, n = 3
// or 4, by the step list.
func iadstRef(t *[64]int32, n, r int) {
	m := 1 << n
	var c [16]int32
	copy(c[:m], t[:m])
	in := &iadstIn[n]
	for i := 0; i < m; i++ {
		t[i] = c[in[i]]
	}
	if n == 3 {
		iadst8Steps.run(t, r)
	} else {
		iadst16Steps.run(t, r)
	}
	copy(c[:m], t[:m])
	out := &iadstOut[n]
	for i := 0; i < m; i += 2 {
		t[i] = c[out[i]]
		t[i+1] = -c[out[i+1]]
	}
}

func emitButterfly(b *strings.Builder, name, doc string, l bfList, n int, load, store func(i int) string) {
	m := 1 << n
	fmt.Fprintf(b, "%s\nfunc %s(t *[64]int32, lo, hi int32) {\n", doc, name)
	for i := 0; i < m; i++ {
		fmt.Fprintf(b, "\tx%d := %s\n", i, load(i))
	}
	for _, st := range l {
		if st.hadamard {
			fmt.Fprintf(b, "\tx%d, x%d = min(max(x%d+x%d, lo), hi), min(max(x%d-x%d, lo), hi)\n", st.a, st.b, st.a, st.b, st.a, st.b)
		} else {
			fmt.Fprintf(b, "\tx%d, x%d = rot12(x%d, x%d, %d, %d)\n", st.oa, st.ob, st.a, st.b, st.c, st.s)
		}
	}
	for i := 0; i < m; i++ {
		fmt.Fprintf(b, "\tt[%d] = %s\n", i, store(i))
	}
	b.WriteString("}\n\n")
}

func writeButterflies(path string) error {
	var b strings.Builder
	b.WriteString(`// Code generated by go test -run TestButterflyUnrolled -write-bf. DO NOT EDIT.

package av1

// The butterfly networks of the step lists in transform_bf_test.go (the
// inverse DCTs, spec 7.13.2.3, and the 8- and 16-point inverse ADSTs,
// 7.13.2.7 and 7.13.2.8, with their permutations), written out as
// straight-line code: the same steps, in the same order, on locals.

// rot12 is a butterfly rotation: Round2(x*c - y*s, 12), Round2(x*s + y*c, 12).
func rot12(x, y int32, c, s int64) (int32, int32) {
	u := int64(x)*c - int64(y)*s
	v := int64(x)*s + int64(y)*c
	return int32((u + 2048) >> 12), int32((v + 2048) >> 12)
}

`)
	for n := 2; n <= 6; n++ {
		p := idctPerm[n]
		emitButterfly(&b, fmt.Sprintf("idct%d", 1<<n),
			fmt.Sprintf("// idct%d is the inverse DCT of %d samples, clamped to [lo, hi].", 1<<n, 1<<n),
			idctSteps[n], n,
			func(i int) string { return fmt.Sprintf("t[%d]", p[i]) },
			func(i int) string { return fmt.Sprintf("x%d", i) })
	}
	for n := 3; n <= 4; n++ {
		in, out := iadstIn[n], iadstOut[n]
		l := iadst8Steps
		if n == 4 {
			l = iadst16Steps
		}
		emitButterfly(&b, fmt.Sprintf("iadst%d", 1<<n),
			fmt.Sprintf("// iadst%d is the inverse ADST of %d samples, clamped to [lo, hi].", 1<<n, 1<<n),
			l, n,
			func(i int) string { return fmt.Sprintf("t[%d]", in[i]) },
			func(i int) string {
				if i&1 != 0 {
					return fmt.Sprintf("-x%d", out[i])
				}
				return fmt.Sprintf("x%d", out[i])
			})
	}
	src, err := format.Source([]byte(b.String()))
	if err != nil {
		return err
	}
	return os.WriteFile(path, src, 0o600)
}

// TestButterflyUnrolled checks the straight-line butterflies against the
// step lists, on random inputs across the clamping ranges of 8, 10 and 12
// bits.
func TestButterflyUnrolled(t *testing.T) {
	if *writeBF {
		if err := writeButterflies("transform_bf.go"); err != nil {
			t.Fatal(err)
		}
	}
	rs := xorshift32(4242)
	for _, r := range []int{16, 18, 20} {
		lo, hi := -int32(1)<<(r-1), int32(1)<<(r-1)-1
		for n := 2; n <= 6; n++ {
			for k := 0; k < 200; k++ {
				var a, b [64]int32
				for i := 0; i < 1<<n; i++ {
					a[i] = int32(rs.next()%uint32(hi-lo+1)) + lo
					if k&1 != 0 {
						a[i] >>= 4
					}
				}
				b = a
				idctRef(&a, n, r)
				idctFuncs[n](&b, lo, hi)
				if a != b {
					t.Fatalf("idct%d (r %d) differs from its step list", 1<<n, r)
				}
				if n == 3 || n == 4 {
					b = a
					iadstRef(&a, n, r)
					iadstFuncs[n](&b, lo, hi)
					if a != b {
						t.Fatalf("iadst%d (r %d) differs from its step list", 1<<n, r)
					}
				}
			}
		}
	}
}

// idctFuncs and iadstFuncs hold the 1D inverse DCTs and ADSTs by log2 of
// the size (transform_bf.go, and iadst4 in transform.go).
var (
	idctFuncs  = [7]func(t *[64]int32, lo, hi int32){2: idct4, 3: idct8, 4: idct16, 5: idct32, 6: idct64}
	iadstFuncs = [5]func(t *[64]int32, lo, hi int32){2: iadst4, 3: iadst8, 4: iadst16}
)
