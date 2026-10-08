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
		slope := int64(q.SlopeInt[ci])<<denom + int64(q.Slope[ci])    //nolint:gosec // small
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

// smoothPicture is a picture of gentle gradients, which a half-size
// enhancement layer can correct.
func smoothPicture(w, h int, phase float64, lo, hi int) *Picture {
	p := newPicture(w, h)
	span := float64(hi - lo)
	for y := range h {
		for x := range w {
			v := 0.5 + 0.25*math.Sin(float64(x)/23+phase) + 0.2*math.Cos(float64(y)/17-phase)
			setSample(p.Y, y*w+x, lo+int(v*span))
		}
	}
	for y := range h / 2 {
		for x := range w / 2 {
			v := 0.5 + 0.2*math.Sin(float64(x+y)/13+phase)
			setSample(p.UV, y*w+2*x, lo+int(v*span))
			setSample(p.UV, y*w+2*x+1, hi-int(v*span))
		}
	}
	return p
}

func lumaPSNR(a, b *Picture) float64 {
	var se float64
	n := len(a.Y) / 2
	for i := range n {
		d := float64(getSample(a.Y, i) - getSample(b.Y, i))
		se += d * d
	}
	return 10 * math.Log10(1023*1023*float64(n)/max(se, 1e-9))
}

// An enhancement layer rebuilt for an altered base layer makes the
// composition again: far closer than the source's enhancement layer put
// with the new base layer.
func TestRebuild(t *testing.T) {
	c, err := NewComposer(fixtureRPU(t, "fel-cmv29.bin"))
	if err != nil {
		t.Fatal(err)
	}
	const w, h = 256, 128
	bl := smoothPicture(w, h, 0, 100, 900)
	el := smoothPicture(w/2, h/2, 1, 480, 560)
	want := newPicture(w, h)
	if err := c.Compose(want, bl, el); err != nil {
		t.Fatal(err)
	}
	// The base layer as an encoder might give it back: off by a few codes,
	// smoothly.
	blNew := newPicture(w, h)
	for y := range h {
		for x := range w {
			d := int(6 * math.Sin(float64(x)/40+float64(y)/30))
			setSample(blNew.Y, y*w+x, min(max(getSample(bl.Y, y*w+x)+d, 0), 1023))
		}
	}
	copy(blNew.UV, bl.UV)
	elNew := newPicture(w/2, h/2)
	if err := c.Rebuild(elNew, bl, el, blNew); err != nil {
		t.Fatal(err)
	}
	rebuilt, naive := newPicture(w, h), newPicture(w, h)
	if err := c.Compose(rebuilt, blNew, elNew); err != nil {
		t.Fatal(err)
	}
	if err := c.Compose(naive, blNew, el); err != nil {
		t.Fatal(err)
	}
	pr, pn := lumaPSNR(rebuilt, want), lumaPSNR(naive, want)
	t.Logf("rebuilt %.1f dB, source enhancement layer %.1f dB", pr, pn)
	if pr < 50 || pr < pn+6 {
		t.Errorf("rebuilt %.1f dB from the composition, the source's enhancement layer %.1f dB", pr, pn)
	}
	// With the base layer unchanged, the rebuilt layer gives the same
	// picture back.
	if err := c.Rebuild(elNew, bl, el, bl); err != nil {
		t.Fatal(err)
	}
	if err := c.Compose(rebuilt, bl, elNew); err != nil {
		t.Fatal(err)
	}
	if p := lumaPSNR(rebuilt, want); p < 55 {
		t.Errorf("unchanged base layer: %.1f dB", p)
	}
}

func BenchmarkRebuild4K(b *testing.B) {
	c, err := NewComposer(fixtureRPU(b, "fel-cmv29.bin"))
	if err != nil {
		b.Fatal(err)
	}
	r := rand.New(rand.NewPCG(5, 6))
	bl, blNew := randomPicture(3840, 2160, r, 64, 940), randomPicture(3840, 2160, r, 64, 940)
	el := randomPicture(1920, 1080, r, 400, 624)
	dst := newPicture(1920, 1080)
	b.ResetTimer()
	for range b.N {
		_ = c.Rebuild(dst, bl, el, blNew)
	}
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "fps")
}

// The quantiser picks, for any residual, a code whose dequantised residual
// is as near as any code's.
func TestQuantiserNearest(t *testing.T) {
	c, err := NewComposer(fixtureRPU(t, "fel-cmv29.bin"))
	if err != nil {
		t.Fatal(err)
	}
	q := c.quantiser()
	for ci := range 3 {
		for i := -6000; i <= 6000; i++ {
			r := float32(i) / 10000
			got := c.residual[ci][q.nearest(ci, r)]
			best := float32(math.Inf(1))
			for code := range 1024 {
				best = min(best, float32(math.Abs(float64(c.residual[ci][code]-r))))
			}
			if d := float32(math.Abs(float64(got - r))); d > best+1e-6 {
				t.Fatalf("component %d, residual %v: code's residual %v is %v off, the best %v", ci, r, got, d, best)
			}
		}
	}
}
