package convert

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/brunoga/bdtools/internal/gpu"
)

// withTools makes LookPath find exactly the named programs.
func withTools(t *testing.T, names ...string) {
	t.Helper()
	orig := LookPath
	t.Cleanup(func() { LookPath = orig })
	LookPath = func(name string) (string, error) {
		for _, n := range names {
			if n == name {
				return "/usr/bin/" + name, nil
			}
		}
		return "", os.ErrNotExist
	}
}

// Software AV1 runs SvtAv1EncApp when it is installed, reading Y4M from
// stdin, with --crf and --preset on SVT's scales.
func TestAV1SoftwareUsesSvtAv1EncApp(t *testing.T) {
	withTools(t, "SvtAv1EncApp", "ffmpeg")
	argv := strings.Join(encodeArgv(t, "linux", EncoderSoftware, CodecAV1), " ")
	want := fmt.Sprintf("SvtAv1EncApp -i stdin --crf %d --preset 5 -b /tmp/work/stacked.obu", svtCRF(18))
	if argv != want {
		t.Errorf("got  %s\nwant %s", argv, want)
	}
	o := opts("linux", func(o *Options) { o.Codec = CodecAV1 })
	if req := RequiredFor("linux", o); len(req) != 1 || req[0].Name != "SvtAv1EncApp" {
		t.Errorf("required %+v", req)
	}
}

// Without SvtAv1EncApp the same library is reached through ffmpeg, and
// that is what --check asks for.
func TestAV1SoftwareFallsBackToFFmpeg(t *testing.T) {
	withTools(t, "ffmpeg")
	argv := strings.Join(encodeArgv(t, "linux", EncoderSoftware, CodecAV1), " ")
	if !strings.HasPrefix(argv, "ffmpeg ") || !strings.Contains(argv, "-c:v libsvtav1") {
		t.Errorf("got %s", argv)
	}
	o := opts("linux", func(o *Options) { o.Codec = CodecAV1 })
	if req := RequiredFor("linux", o); len(req) != 1 || req[0].Name != "ffmpeg" {
		t.Errorf("required %+v", req)
	}
	// With neither, SvtAv1EncApp is what to install.
	withTools(t)
	if req := RequiredFor("linux", o); len(req) != 1 || req[0].Name != "SvtAv1EncApp" {
		t.Errorf("required with nothing installed %+v", req)
	}
}

// Half-SBS AV1 in software squeezes through ffmpeg, whichever SVT is there.
func TestAV1HalfSBSFilters(t *testing.T) {
	withTools(t, "SvtAv1EncApp", "ffmpeg")
	p, err := BuildPlan("linux", opts("linux", func(o *Options) { o.Codec, o.Layout = CodecAV1, LayoutHalfSBS }))
	if err != nil {
		t.Fatal(err)
	}
	if s := p.String(); !strings.Contains(s, "scale=iw/2:ih") || !strings.Contains(s, "libsvtav1") {
		t.Errorf("plan:\n%s", s)
	}
}

// Through ffmpeg the GPUs get AV1's quantiser index, as in process.
func TestAV1GPUViaFFmpeg(t *testing.T) {
	for enc, name := range map[Encoder]string{EncoderNVENC: "av1_nvenc", EncoderVAAPI: "av1_vaapi"} {
		argv := strings.Join(encodeArgv(t, "linux", enc, CodecAV1), " ")
		if !strings.Contains(argv, "-c:v "+name) || !strings.Contains(argv, fmt.Sprintf("-qp %d", gpu.AV1QIndex(18))) {
			t.Errorf("%s: %s", enc, argv)
		}
		if !strings.HasSuffix(argv, ".obu") {
			t.Errorf("%s writes %s", enc, argv)
		}
	}
}

// VideoToolbox has no AV1 encoder: refused up front, and never probed as
// if it might.
func TestAV1VideoToolboxIsRefused(t *testing.T) {
	_, err := BuildPlan("darwin", opts("darwin", func(o *Options) { o.Codec, o.Encoder = CodecAV1, EncoderVideoToolbox }))
	if err == nil || !strings.Contains(err.Error(), "AV1") {
		t.Errorf("err = %v", err)
	}
	called := false
	orig := runProbe
	t.Cleanup(func() { runProbe = orig })
	runProbe = func(context.Context, []string) error { called = true; return nil }
	if ProbeEncoder(t.Context(), EncoderVideoToolbox, CodecAV1, "", false) || called {
		t.Error("VideoToolbox AV1 must not probe as available")
	}
}

// The quality scales: --crf 0-51 onto AV1's 0-255 index and SVT's 0-63
// CRF, monotonic and inside their ranges.
func TestAV1QualityMapping(t *testing.T) {
	prevQ, prevC := -1, 0
	for crf := 0; crf <= 51; crf++ {
		q, c := gpu.AV1QIndex(crf), svtCRF(crf)
		if q < prevQ || q < 0 || q > 255 || c < prevC || c < 1 || c > 63 {
			t.Fatalf("crf %d: qindex %d, SVT CRF %d", crf, q, c)
		}
		prevQ, prevC = q, c
	}
	if svtPreset("slow") != 5 || svtPreset("ultrafast") != 12 || svtPreset("bogus") != 5 {
		t.Error("preset mapping")
	}
}
