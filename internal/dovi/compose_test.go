package dovi

import (
	"math"
	"math/rand/v2"
	"os"
	"testing"
)

func newPicture(w, h int) *Picture {
	return &Picture{Width: w, Height: h, Y: make([]byte, w*h*2), UV: make([]byte, w*h), Pitch: w * 2}
}

func setSample(b []byte, i int, v int) {
	v <<= 6
	b[2*i], b[2*i+1] = byte(v), byte(v>>8)
}

func getSample(b []byte, i int) int { return int(uint16(b[2*i])|uint16(b[2*i+1])<<8) >> 6 }

func randomPicture(w, h int, r *rand.Rand, lo, hi int) *Picture {
	p := newPicture(w, h)
	for i := range len(p.Y) / 2 {
		setSample(p.Y, i, lo+r.IntN(hi-lo))
	}
	for i := range len(p.UV) / 2 {
		setSample(p.UV, i, lo+r.IntN(hi-lo))
	}
	return p
}

// fixtureRPU is the first RPU of a fixture, parsed.
func fixtureRPU(t testing.TB, name string) *RPU {
	t.Helper()
	nals := splitRPUs(mustRead(t, "testdata/"+name))
	u, err := ParseNAL(nals[0])
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// Profile 8.1's identity mapping with no residual leaves a picture as it is.
func TestComposeIdentity(t *testing.T) {
	u := fixtureRPU(t, "fel-cmv29.bin")
	if err := u.ToProfile81(); err != nil {
		t.Fatal(err)
	}
	c, err := NewComposer(u)
	if err != nil {
		t.Fatal(err)
	}
	if c.HasResidual() {
		t.Error("profile 8.1 has a residual")
	}
	r := rand.New(rand.NewPCG(1, 2))
	bl := randomPicture(64, 32, r, 0, 1024)
	dst := newPicture(64, 32)
	if err := c.Compose(dst, bl, nil); err != nil {
		t.Fatal(err)
	}
	for i := range len(bl.Y) / 2 {
		if getSample(dst.Y, i) != getSample(bl.Y, i) {
			t.Fatalf("luma %d: %d, was %d", i, getSample(dst.Y, i), getSample(bl.Y, i))
		}
	}
	for i := range len(bl.UV) / 2 {
		if getSample(dst.UV, i) != getSample(bl.UV, i) {
			t.Fatalf("chroma %d: %d, was %d", i, getSample(dst.UV, i), getSample(bl.UV, i))
		}
	}
}

// The dequantised residual matches vs-nlq's fixed-point arithmetic (its
// result, in 2^-16 of the signal, over the whole code range).
func TestComposeResidualMatchesVsNLQ(t *testing.T) {
	u := fixtureRPU(t, "fel-cmv29.bin")
	c, err := NewComposer(u)
	if err != nil {
		t.Fatal(err)
	}
	if !c.HasResidual() {
		t.Fatal("a FEL RPU without a residual")
	}
	q, denom := u.Mapping.NLQ, int64(u.CoefLog2Denom) //nolint:gosec // small
	const eld = 10
	for ci := range 3 {
		slope := int64(q.SlopeInt[ci])<<denom + int64(q.Slope[ci])   //nolint:gosec // small
		thresh := int64(q.ThreshInt[ci])<<denom + int64(q.Thresh[ci]) //nolint:gosec // small
		inMax := int64(q.InMaxInt[ci])<<denom + int64(q.InMax[ci])    //nolint:gosec // small
		for code := range 1024 {
			tmp := int64(code) - int64(q.Offset[ci]) //nolint:gosec // small
			var want int64
			if tmp != 0 {
				sign := int64(1)
				if tmp < 0 {
					sign = -1
				}
				tmp = (2*tmp - sign) << (10 - eld)
				dq := tmp*slope + (thresh<<(10-eld+1))*sign
				rr := inMax << (10 - eld + 1)
				dq = min(max(dq, -rr), rr)
				want = dq >> (denom - 5 - eld)
			}
			got := float64(c.residual[ci][code]) * 65536
			if math.Abs(got-float64(want)) > 1 {
				t.Fatalf("component %d, code %d: residual %.2f/65536, vs-nlq's %d", ci, code, got, want)
			}
		}
	}
}

// A FEL picture composes: the enhancement layer at its offset adds
// nothing, so the result is the mapping alone; away from it, it moves the
// picture by its residual.
func TestComposeAddsTheResidual(t *testing.T) {
	u := fixtureRPU(t, "fel-cmv29.bin")
	c, err := NewComposer(u)
	if err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewPCG(3, 4))
	bl := randomPicture(64, 32, r, 64, 940)
	el := newPicture(32, 16)
	off := int(u.Mapping.NLQ.Offset[0])
	for i := range len(el.Y) / 2 {
		setSample(el.Y, i, off)
	}
	for i := range len(el.UV) / 2 {
		setSample(el.UV, i, int(u.Mapping.NLQ.Offset[1+i%2]))
	}
	mapped, composed := newPicture(64, 32), newPicture(64, 32)
	if err := c.Compose(mapped, bl, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Compose(composed, bl, el); err != nil {
		t.Fatal(err)
	}
	for i := range len(bl.Y) / 2 {
		if getSample(mapped.Y, i) != getSample(composed.Y, i) {
			t.Fatalf("luma %d: an enhancement layer at its offset changed the picture", i)
		}
	}
	// A flat enhancement layer above the offset raises luma by its residual.
	for i := range len(el.Y) / 2 {
		setSample(el.Y, i, off+100)
	}
	if err := c.Compose(composed, bl, el); err != nil {
		t.Fatal(err)
	}
	for i := range len(bl.Y) / 2 {
		want := min(1023, int(math.Round(float64(c.luma[getSample(bl.Y, i)]+c.residual[0][off+100])*1023)))
		if got := getSample(composed.Y, i); got != want {
			t.Fatalf("luma %d: %d, want %d", i, got, want)
		}
	}
}

func BenchmarkCompose4K(b *testing.B) {
	u := fixtureRPU(b, "fel-cmv29.bin")
	c, err := NewComposer(u)
	if err != nil {
		b.Fatal(err)
	}
	r := rand.New(rand.NewPCG(5, 6))
	bl := randomPicture(3840, 2160, r, 64, 940)
	el := randomPicture(1920, 1080, r, 400, 624)
	dst := newPicture(3840, 2160)
	b.ResetTimer()
	for range b.N {
		_ = c.Compose(dst, bl, el)
	}
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "fps")
}

func mustRead(t testing.TB, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The MMR row kernel (AVX2 where there is one) agrees with the Go one
// within float32 rounding (FMA rounds once where Go rounds twice).
func TestMMRRowKernel(t *testing.T) {
	c, err := NewComposer(fixtureRPU(t, "fel-cmv29.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if c.mmr == nil {
		t.Fatal("the fixture's chroma is not single-piece MMR")
	}
	r := rand.New(rand.NewPCG(7, 8))
	const n = 1000 // a tail past the vectors too
	sy, sb, sr := make([]float32, n), make([]float32, n), make([]float32, n)
	for i := range n {
		sy[i], sb[i], sr[i] = r.Float32(), r.Float32(), r.Float32()
	}
	ob, or := make([]float32, n), make([]float32, n)
	wb, wr := make([]float32, n), make([]float32, n)
	c.mmr.row(ob, or, sy, sb, sr)
	c.mmr.rowGo(wb, wr, sy, sb, sr)
	for i := range n {
		if math.Abs(float64(ob[i]-wb[i])) > 1e-5 || math.Abs(float64(or[i]-wr[i])) > 1e-5 {
			t.Fatalf("sample %d: %v %v, Go %v %v", i, ob[i], or[i], wb[i], wr[i])
		}
	}
}

// The enhancement layer's upsampling keeps a linear ramp linear, at the
// siting chosen: co-sited across (even outputs are the inputs, odd ones
// halfway), centred down (output rows a quarter of a row either side of
// an input row).
func TestUpsampleSiting(t *testing.T) {
	el := newPicture(16, 16)
	for y := range 16 {
		for x := range 16 {
			setSample(el.Y, y*16+x, 300+8*x+4*y)
		}
	}
	out, tmp := make([]int32, 32), make([]int32, 16)
	for _, y := range []int{4, 5, 10, 11} { // away from the edges
		upsampleLuma(out, tmp, el, y)
		row := float64(y)/2 - 0.25 // the output row's position in input rows
		for x := 4; x < 28; x++ {
			want := 300 + 8*float64(x)/2 + 4*row
			if math.Abs(float64(out[x])-want) > 0.5 {
				t.Fatalf("row %d, x %d: %d, want %.2f", y, x, out[x], want)
			}
		}
	}
}
