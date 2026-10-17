package vmaf

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// testFrames makes a reference clip (a moving texture) and a distorted one
// (blurred and noisy), n pictures of w x h at bpc bits.
func testFrames(w, h, bpc, n int, seed uint64) (ref, dis []Frame) {
	rng := rand.New(rand.NewPCG(seed, 1))
	top := float64(int(1)<<bpc - 1)
	base := make([]float64, (w+64)*(h+64))
	for i := range base {
		base[i] = rng.Float64()
	}
	for f := range n {
		r := Frame{W: w, H: h, BPC: bpc, Y: make([]uint16, w*h)}
		d := Frame{W: w, H: h, BPC: bpc, Y: make([]uint16, w*h)}
		for y := range h {
			for x := range w {
				// smooth texture: a few octaves of the base, moving by f
				v := 0.0
				for o, s := range []int{1, 3, 9} {
					v += base[((y+f*o)%h+s)*(w+64)+(x+2*f)%w] / float64(o+1)
				}
				v = 0.15 + 0.7*(v/1.83) + 0.08*math.Sin(float64(x+f)/7)*math.Cos(float64(y)/11)
				r.Y[y*w+x] = uint16(math.Round(min(max(v, 0), 1) * top))
			}
		}
		for y := range h {
			for x := range w {
				var s float64
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						s += float64(r.Y[mirror(y+dy, h)*w+mirror(x+dx, w)])
					}
				}
				v := s/9 + rng.NormFloat64()*top*0.01*float64(f%3+1)
				d.Y[y*w+x] = uint16(math.Round(min(max(v, 0), top)))
			}
		}
		ref, dis = append(ref, r), append(dis, d)
	}
	return ref, dis
}

// writeYUV writes frames as 4:2:0 (grey chroma), 8 bits or 16-bit little
// endian words.
func writeYUV(t *testing.T, path string, frames []Frame) {
	t.Helper()
	var b []byte
	for _, f := range frames {
		put := func(v uint16) {
			if f.BPC > 8 {
				b = binary.LittleEndian.AppendUint16(b, v)
			} else {
				b = append(b, byte(v))
			}
		}
		for _, v := range f.Y {
			put(v)
		}
		mid := uint16(1) << (f.BPC - 1)
		for range (f.W + 1) / 2 * ((f.H + 1) / 2) * 2 {
			put(mid)
		}
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// cliFeatures runs libvmaf's vmaf tool on the clips, giving each frame's
// metrics by name.
func cliFeatures(t *testing.T, ref, dis []Frame, extra ...string) []map[string]float64 {
	t.Helper()
	bin := os.Getenv("BDTOOLS_VMAF_CLI")
	if bin == "" {
		t.Skip("BDTOOLS_VMAF_CLI not set")
	}
	dir := t.TempDir()
	writeYUV(t, filepath.Join(dir, "r.yuv"), ref)
	writeYUV(t, filepath.Join(dir, "d.yuv"), dis)
	f := ref[0]
	args := append([]string{"-r", filepath.Join(dir, "r.yuv"), "-d", filepath.Join(dir, "d.yuv"),
		"-w", fmt.Sprint(f.W), "-h", fmt.Sprint(f.H), "-p", "420", "-b", fmt.Sprint(f.BPC),
		"--json", "-o", filepath.Join(dir, "o.json"), "-q"}, extra...)
	if out, err := exec.CommandContext(t.Context(), bin, args...).CombinedOutput(); err != nil { //nolint:gosec // test
		t.Fatalf("vmaf: %v\n%s", err, out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "o.json"))
	if err != nil {
		t.Fatal(err)
	}
	var j struct {
		Frames []struct {
			Metrics map[string]float64 `json:"metrics"`
		} `json:"frames"`
	}
	if err := json.Unmarshal(b, &j); err != nil {
		t.Fatal(err)
	}
	var out []map[string]float64
	for _, fr := range j.Frames {
		out = append(out, fr.Metrics)
	}
	return out
}

// VIF and motion as libvmaf's tool gives them, to its six decimals.
func TestVIFAndMotionMatchLibvmaf(t *testing.T) {
	for _, c := range []struct{ w, h, bpc int }{{176, 144, 8}, {320, 180, 10}, {162, 98, 8}} {
		t.Run(fmt.Sprintf("%dx%d-%d", c.w, c.h, c.bpc), func(t *testing.T) {
			ref, dis := testFrames(c.w, c.h, c.bpc, 5, uint64(c.w))
			want := cliFeatures(t, ref, dis)
			if len(want) != len(ref) {
				t.Fatalf("%d frames from vmaf", len(want))
			}
			var sads []float64
			for i := range ref {
				vif := vifScores(ref[i], dis[i])
				for s := range 4 {
					name := fmt.Sprintf("integer_vif_scale%d", s)
					if got := fmt.Sprintf("%.6f", vif[s]); got != fmt.Sprintf("%.6f", want[i][name]) {
						t.Errorf("frame %d %s: %s, want %.6f", i, name, got, want[i][name])
					}
				}
				sad := 0.0
				if i > 0 {
					sad = motionSAD(ref[i-1], ref[i])
				}
				sads = append(sads, sad)
			}
			for i := range ref {
				m2 := 0.0
				if i > 0 {
					m2 = sads[i]
					if i+1 < len(sads) {
						m2 = min(sads[i], sads[i+1])
					}
				}
				if got := fmt.Sprintf("%.6f", m2); got != fmt.Sprintf("%.6f", want[i]["integer_motion2"]) {
					t.Errorf("frame %d motion2: %s, want %.6f", i, got, want[i]["integer_motion2"])
				}
			}
		})
	}
}

// SSIM as libvmaf's ssim feature gives it.
func TestSSIMMatchesLibvmaf(t *testing.T) {
	for _, c := range []struct{ w, h, bpc int }{{176, 144, 8}, {320, 180, 10}, {768, 432, 8}, {1100, 660, 10}} {
		ref, dis := testFrames(c.w, c.h, c.bpc, 2, uint64(c.h))
		want := cliFeatures(t, ref, dis, "--feature", "float_ssim")
		for i := range ref {
			if got := fmt.Sprintf("%.6f", SSIM(ref[i], dis[i])); got != fmt.Sprintf("%.6f", want[i]["float_ssim"]) {
				t.Errorf("%dx%d-%d frame %d: %s, want %.6f (%v)", c.w, c.h, c.bpc, i, got, want[i]["float_ssim"], want[i])
			}
		}
	}
}

// PSNR as libvmaf's psnr feature gives it, an identical picture's too.
func TestPSNRMatchesLibvmaf(t *testing.T) {
	for _, c := range []struct{ w, h, bpc int }{{176, 144, 8}, {320, 180, 10}} {
		ref, dis := testFrames(c.w, c.h, c.bpc, 2, 3)
		dis[1] = ref[1]
		want := cliFeatures(t, ref, dis, "--feature", "psnr")
		for i := range ref {
			if got := fmt.Sprintf("%.6f", PSNR(ref[i], dis[i])); got != fmt.Sprintf("%.6f", want[i]["psnr_y"]) {
				t.Errorf("%dx%d-%d frame %d: %s, want %.6f", c.w, c.h, c.bpc, i, got, want[i]["psnr_y"])
			}
		}
	}
}

// VMAF, with both models, as libvmaf's tool gives it.
func TestVMAFMatchesLibvmaf(t *testing.T) {
	for _, c := range []struct {
		w, h, bpc int
		model     string
	}{{320, 180, 8, "vmaf_v0.6.1"}, {320, 180, 10, "vmaf_v0.6.1"}, {640, 360, 8, "vmaf_4k_v0.6.1"}} {
		ref, dis := testFrames(c.w, c.h, c.bpc, 6, uint64(c.w+c.bpc))
		want := cliFeatures(t, ref, dis, "--model", "version="+c.model)
		m, err := ModelFor(c.h)
		if err != nil {
			t.Fatal(err)
		}
		if c.model == "vmaf_4k_v0.6.1" {
			m, _ = ModelFor(2160)
		}
		s, err := NewScorer(c.h)
		if err != nil {
			t.Fatal(err)
		}
		s.model = m
		for i := range ref {
			s.Add(ref[i], dis[i])
		}
		for i, sc := range s.Scores() {
			if got := fmt.Sprintf("%.6f", sc.VMAF); got != fmt.Sprintf("%.6f", want[i]["vmaf"]) {
				t.Errorf("%dx%d-%d %s frame %d: %s, want %.6f", c.w, c.h, c.bpc, c.model, i, got, want[i]["vmaf"])
			}
		}
	}
}

// Every measure of a small clip, against what libvmaf's tool gave for it
// (vmaf, adm2, motion2, vif scales 0-3, float_ssim, psnr_y).
func TestGolden(t *testing.T) {
	want := [][9]float64{
		{40.233576, 0.709485, 0.000000, 0.016841, 0.899030, 0.970512, 0.988074, 0.173798, 18.086935},
		{42.482567, 0.688862, 8.972691, 0.017330, 0.870174, 0.948483, 0.971433, 0.182009, 18.111322},
		{42.494249, 0.694505, 8.972691, 0.014421, 0.822248, 0.927812, 0.963709, 0.180946, 17.998957},
	}
	ref, dis := testFrames(176, 144, 8, 3, 9)
	s, err := NewScorer(144)
	if err != nil {
		t.Fatal(err)
	}
	for i := range ref {
		s.Add(ref[i], dis[i])
	}
	for i, sc := range s.Scores() {
		got := [9]float64{sc.VMAF, sc.ADM2, sc.Motion2, sc.VIF[0], sc.VIF[1], sc.VIF[2], sc.VIF[3], sc.SSIM, sc.PSNR}
		for k := range got {
			if fmt.Sprintf("%.6f", got[k]) != fmt.Sprintf("%.6f", want[i][k]) {
				t.Errorf("frame %d measure %d: %.6f, want %.6f", i, k, got[k], want[i][k])
			}
		}
	}
}
