package vmaf

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// admRNG is a small deterministic generator (xorshift64*), so the test
// pictures never change under the test's feet.
type admRNG uint64

func (r *admRNG) next() uint64 {
	x := uint64(*r)
	x ^= x >> 12
	x ^= x << 25
	x ^= x >> 27
	*r = admRNG(x)
	return x * 0x2545f4914f6cdd1d
}

// float is uniform in [0, 1).
func (r *admRNG) float() float64 { return float64(r.next()>>11) / (1 << 53) }

// gauss is roughly normal, mean 0 and deviation 1 (a sum of uniforms).
func (r *admRNG) gauss() float64 {
	var s float64
	for range 12 {
		s += r.float()
	}
	return s - 6
}

func admClampIndex(i, n int) int {
	return min(max(i, 0), n-1)
}

// admTexture is a reference picture: a gradient, ripples, blobs of
// smoothed noise and fine grain, from seed.
func admTexture(w, h, bpc int, seed uint64) Frame {
	rng := admRNG(seed*0x9e3779b97f4a7c15 + 1)
	top := float64(int(1)<<bpc - 1)
	noise := make([]float64, w*h)
	for i := range noise {
		noise[i] = rng.float()
	}
	smooth := make([]float64, w*h)
	const k = 3
	for y := range h {
		for x := range w {
			var s float64
			for dy := -k; dy <= k; dy++ {
				for dx := -k; dx <= k; dx++ {
					s += noise[admClampIndex(y+dy, h)*w+admClampIndex(x+dx, w)]
				}
			}
			smooth[y*w+x] = s / ((2*k + 1) * (2*k + 1))
		}
	}
	fx, fy := 3+rng.float()*9, 4+rng.float()*12
	f := Frame{W: w, H: h, BPC: bpc, Y: make([]uint16, w*h)}
	for y := range h {
		for x := range w {
			v := 0.2 + 0.4*float64(x+y)/float64(w+h) +
				0.12*math.Sin(float64(x)/fx)*math.Cos(float64(y)/fy) +
				0.9*(smooth[y*w+x]-0.5) + 0.06*(noise[y*w+x]-0.5)
			f.Y[y*w+x] = uint16(math.Round(min(max(v, 0), 1) * top))
		}
	}
	return f
}

// admDistort returns a distorted copy of ref: kind 0 is a 3x3 blur plus
// noise, 1 a 5x5 blur, 2 noise alone, 3 coarse quantisation, 4
// sharpening (an enhancement), 5 the reference itself; strength scales
// the noise.
func admDistort(ref Frame, kind int, strength float64, seed uint64) Frame {
	w, h := ref.W, ref.H
	top := float64(int(1)<<ref.BPC - 1)
	rng := admRNG(seed*0xbf58476d1ce4e5b9 + 7)
	at := func(x, y int) float64 { return float64(ref.Y[admClampIndex(y, h)*w+admClampIndex(x, w)]) }
	box := func(x, y, k int) float64 {
		var s float64
		for dy := -k; dy <= k; dy++ {
			for dx := -k; dx <= k; dx++ {
				s += at(x+dx, y+dy)
			}
		}
		return s / float64((2*k+1)*(2*k+1))
	}
	d := Frame{W: w, H: h, BPC: ref.BPC, Y: make([]uint16, w*h)}
	for y := range h {
		for x := range w {
			var v float64
			switch kind {
			case 0:
				v = box(x, y, 1) + rng.gauss()*top*0.01*strength
			case 1:
				v = box(x, y, 2)
			case 2:
				v = at(x, y) + rng.gauss()*top*0.02*strength
			case 3:
				q := top / 12
				v = math.Floor(at(x, y)/q)*q + q/2
			case 4:
				v = at(x, y) + 0.8*strength*(at(x, y)-box(x, y, 1))
			default:
				v = at(x, y)
			}
			d.Y[y*w+x] = uint16(math.Round(min(max(v, 0), top)))
		}
	}
	return d
}

type admCase struct {
	w, h, bpc int
	seed      uint64
	kind      int
	strength  float64
}

func (c admCase) String() string {
	return fmt.Sprintf("%dx%d/%dbit/seed%d/kind%d/s%g", c.w, c.h, c.bpc, c.seed, c.kind, c.strength)
}

func (c admCase) pictures() (ref, dis Frame) {
	ref = admTexture(c.w, c.h, c.bpc, c.seed)
	return ref, admDistort(ref, c.kind, c.strength, c.seed)
}

// admGolden are adm2 scores from libvmaf's vmaf tool (built without
// assembly, git 229f247), as its JSON prints them. An undistorted
// picture scores a hair over 1: the noise floor is added to the
// numerator's and the denominator's bands alike, and their cube roots
// are not the same.
var admGolden = []struct {
	c    admCase
	want string
}{
	{admCase{176, 144, 8, 1, 0, 1}, "0.914811"},
	{admCase{176, 144, 8, 2, 1, 1}, "0.868659"},
	{admCase{176, 144, 8, 3, 2, 2}, "0.912946"},
	{admCase{176, 144, 8, 4, 3, 1}, "0.947392"},
	{admCase{176, 144, 8, 5, 4, 1}, "0.998751"},
	{admCase{176, 144, 8, 6, 5, 1}, "1.000002"},
	{admCase{320, 180, 8, 1, 0, 1}, "0.919201"},
	{admCase{320, 180, 8, 2, 1, 1}, "0.859975"},
	{admCase{320, 180, 8, 3, 2, 2}, "0.918563"},
	{admCase{320, 180, 8, 4, 3, 1}, "0.930792"},
	{admCase{320, 180, 8, 5, 4, 1}, "1.001636"},
	{admCase{320, 180, 8, 6, 5, 1}, "1.000002"},
	{admCase{176, 144, 10, 1, 0, 1}, "0.914625"},
	{admCase{176, 144, 10, 2, 1, 1}, "0.868677"},
	{admCase{176, 144, 10, 3, 2, 2}, "0.913431"},
	{admCase{176, 144, 10, 4, 3, 1}, "0.946976"},
	{admCase{176, 144, 10, 5, 4, 1}, "1.001609"},
	{admCase{176, 144, 10, 6, 5, 1}, "1.000002"},
	{admCase{320, 180, 10, 1, 0, 1}, "0.919316"},
	{admCase{320, 180, 10, 2, 1, 1}, "0.859955"},
	{admCase{320, 180, 10, 3, 2, 2}, "0.917491"},
	{admCase{320, 180, 10, 4, 3, 1}, "0.933295"},
	{admCase{320, 180, 10, 5, 4, 1}, "1.001422"},
	{admCase{320, 180, 10, 6, 5, 1}, "1.000002"},
}

func TestADM2Golden(t *testing.T) {
	for _, g := range admGolden {
		ref, dis := g.c.pictures()
		if got := fmt.Sprintf("%.6f", adm2(ref, dis)); got != g.want {
			t.Errorf("%v: adm2 %s, libvmaf %s", g.c, got, g.want)
		}
	}
}

// admWriteYUV writes pictures as 4:2:0 with grey chroma, 8-bit samples
// or 16-bit little endian words.
func admWriteYUV(t *testing.T, path string, frames []Frame) {
	t.Helper()
	var b []byte
	put := func(v uint16, bpc int) {
		if bpc == 8 {
			b = append(b, byte(v))
		} else {
			b = binary.LittleEndian.AppendUint16(b, v)
		}
	}
	for _, f := range frames {
		for _, v := range f.Y {
			put(v, f.BPC)
		}
		grey := uint16(1) << (f.BPC - 1)
		for range 2 * ((f.W + 1) / 2) * ((f.H + 1) / 2) {
			put(grey, f.BPC)
		}
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// admCLI runs libvmaf's vmaf tool on the pictures and returns its
// per-frame adm2 scores as printed.
func admCLI(t *testing.T, cli string, ref, dis []Frame) []string {
	t.Helper()
	dir := t.TempDir()
	rp, dp, out := filepath.Join(dir, "ref.yuv"), filepath.Join(dir, "dis.yuv"), filepath.Join(dir, "out.json")
	admWriteYUV(t, rp, ref)
	admWriteYUV(t, dp, dis)
	f := ref[0]
	cmd := exec.CommandContext(t.Context(), cli, "-r", rp, "-d", dp, "-w", fmt.Sprint(f.W), "-h", fmt.Sprint(f.H),
		"-p", "420", "-b", fmt.Sprint(f.BPC), "--no_prediction", "--feature", "adm",
		"--json", "-o", out, "-q")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v\n%s", cmd, err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Frames []struct {
			Metrics map[string]json.Number `json:"metrics"`
		} `json:"frames"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	var s []string
	for _, fr := range res.Frames {
		v, ok := fr.Metrics["integer_adm2"]
		if !ok {
			t.Fatalf("no integer_adm2 in %s", b)
		}
		s = append(s, v.String())
	}
	return s
}

// TestADM2MatchesCLI compares adm2 with the vmaf tool named by
// BDTOOLS_VMAF_CLI on random pictures and distortions.
func TestADM2MatchesCLI(t *testing.T) {
	cli := os.Getenv("BDTOOLS_VMAF_CLI")
	if cli == "" {
		t.Skip("BDTOOLS_VMAF_CLI not set")
	}
	// Even sizes only: the tool's 4:2:0 reader fails on odd ones. (Odd
	// sizes, 33x33 and 3840x2160, 12 and 16 bits, were checked bit for
	// bit through the libvmaf API with 4:4:4 pictures.)
	sizes := [][2]int{{176, 144}, {320, 180}, {354, 202}, {640, 360}, {1920, 1080}}
	frames := 12
	if testing.Short() {
		sizes, frames = sizes[:3], 6
	}
	compared, maxDiff := 0, 0.0
	for _, bpc := range []int{8, 10} {
		for si, sz := range sizes {
			n := frames
			if sz[0] >= 1920 {
				n = 3
			}
			var refs, diss []Frame
			var cases []admCase
			for k := range n {
				c := admCase{sz[0], sz[1], bpc, uint64(100*bpc + 10*si + k), k % 6, 0.5 + float64(k%4)}
				ref, dis := c.pictures()
				refs, diss, cases = append(refs, ref), append(diss, dis), append(cases, c)
			}
			want := admCLI(t, cli, refs, diss)
			if len(want) != n {
				t.Fatalf("%dx%d: %d scores from the CLI, want %d", sz[0], sz[1], len(want), n)
			}
			for k := range n {
				g := adm2(refs[k], diss[k])
				got := fmt.Sprintf("%.6f", g)
				var w float64
				fmt.Sscan(want[k], &w)
				maxDiff = max(maxDiff, math.Abs(g-w))
				compared++
				if got != want[k] {
					t.Errorf("%v: adm2 %s, libvmaf %s", cases[k], got, want[k])
				}
			}
		}
	}
	t.Logf("%d frames compared, max |adm2 - printed libvmaf score| %.3g (the print rounds to 6 places)", compared, maxDiff)
}

func BenchmarkADM2_1080(b *testing.B) {
	ref, dis := admCase{1920, 1080, 8, 1, 0, 1}.pictures()
	for b.Loop() {
		adm2(ref, dis)
	}
}

func BenchmarkADM2_1080_10bit(b *testing.B) {
	ref, dis := admCase{1920, 1080, 10, 1, 0, 1}.pictures()
	for b.Loop() {
		adm2(ref, dis)
	}
}
