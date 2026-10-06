package convert

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeLookPath makes the resolver answer from a set, so the tests run
// identically on Linux, macOS and Windows without creating executables.
func fakeLookPath(t *testing.T, present ...string) {
	t.Helper()
	set := make(map[string]bool, len(present))
	for _, p := range present {
		set[p] = true
	}
	orig := LookPath
	LookPath = func(name string) (string, error) {
		if set[name] {
			return "/fake/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
	t.Cleanup(func() { LookPath = orig })
}

// fakeProbe makes the trial encode answer from a set, so encoder selection can
// be tested on a machine with no GPU — which is every CI runner.
func fakeProbe(t *testing.T, working ...Encoder) {
	t.Helper()
	ok := make(map[Encoder]bool, len(working))
	for _, e := range working {
		ok[e] = true
	}
	origNative := ProbeNative
	ProbeNative = func(Encoder, Codec, string) bool { return false }
	t.Cleanup(func() { ProbeNative = origNative })
	orig := runProbe
	runProbe = func(_ context.Context, argv []string) error {
		for e := range ok {
			if sameArgv(probeArgv(e, CodecH264, "/dev/dri/renderD128", false), argv) {
				return nil
			}
		}
		return errors.New("probe failed")
	}
	t.Cleanup(func() { runProbe = orig })
}

func sameArgv(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRequiredToolsDependOnTheEncoder(t *testing.T) {
	sw := Required("linux", EncoderSoftware, CodecH264, false)
	if !hasTool(sw, "x264") || hasTool(sw, "ffmpeg") {
		t.Errorf("software encoding needs x264 and not ffmpeg, got %v", names(sw))
	}
	hw := Required("linux", EncoderVAAPI, CodecH264, false)
	if !hasTool(hw, "ffmpeg") || hasTool(hw, "x264") {
		t.Errorf("hardware encoding needs ffmpeg and not x264, got %v", names(hw))
	}
	// The demux, decode and mux are built in and need nothing.
	if len(sw) != 1 || len(hw) != 1 {
		t.Errorf("only the encoder is required, got %v and %v", names(sw), names(hw))
	}
}

func TestDetectReportsWhatIsMissing(t *testing.T) {
	fakeLookPath(t, "ffmpeg")
	rep := Detect(context.Background(), "linux", EncoderSoftware, CodecH264, false)
	if rep.OK() {
		t.Fatal("report should not be OK when tools are absent")
	}
	if missing := names(toolsOf(rep.Missing())); !containsFold(missing, "x264") {
		t.Errorf("missing list %v should contain x264", missing)
	}
}

func TestDetectIsOKWhenEverythingIsPresent(t *testing.T) {
	fakeLookPath(t, "x264")
	rep := Detect(context.Background(), "linux", EncoderSoftware, CodecH264, false)
	if !rep.OK() {
		t.Errorf("expected OK, missing: %v", names(toolsOf(rep.Missing())))
	}
}

// A missing-tool report has to explain itself: which tool, what it is for,
// and where to start looking. Someone reads this once, having never seen the
// pipeline.
func TestMissingToolReportExplainsItself(t *testing.T) {
	fakeLookPath(t)
	out := Detect(context.Background(), "linux", EncoderSoftware, CodecH264, false).String()
	if !strings.Contains(out, "MISSING") {
		t.Error("report should mark missing tools")
	}
	if !strings.Contains(out, "encode the stacked frames in software") {
		t.Error("report should say what each missing tool is for")
	}
	if !strings.Contains(out, "try:") {
		t.Error("report should offer an install hint")
	}
	if !strings.Contains(out, "1 of 1 tools missing") {
		t.Errorf("report should total the misses, got:\n%s", out)
	}
}

func TestInstallHintFallsBackForAnUnlistedPlatform(t *testing.T) {
	if h := toolX264.InstallHint("plan9"); h == "" {
		t.Error("an unlisted platform should still get a starting point")
	}
	if h := toolX264.InstallHint("darwin"); !strings.Contains(h, "brew") {
		t.Errorf("darwin hint should be the macOS one, got %q", h)
	}
}

// Asking for a GPU encoder the platform cannot have is a configuration error,
// and a conversion runs for hours — so it must be caught up front.
func TestSupportsEncoderIsPlatformAware(t *testing.T) {
	cases := []struct {
		goos string
		enc  Encoder
		want bool
	}{
		{"linux", EncoderVAAPI, true},
		{"darwin", EncoderVAAPI, false},
		{"darwin", EncoderVideoToolbox, true},
		{"linux", EncoderVideoToolbox, false},
		{"windows", EncoderNVENC, true},
		{"darwin", EncoderNVENC, false},
		{"windows", EncoderMediaFoundation, true},
		{"linux", EncoderMediaFoundation, false},
		{"darwin", EncoderMediaFoundation, false},
		{"linux", EncoderSoftware, true},
		{"windows", EncoderSoftware, true},
		{"darwin", EncoderSoftware, true},
		{"plan9", EncoderSoftware, true},
		{"linux", EncoderAuto, true},
	}
	for _, c := range cases {
		if got := SupportsEncoder(c.goos, c.enc); got != c.want {
			t.Errorf("SupportsEncoder(%q, %q) = %v, want %v", c.goos, c.enc, got, c.want)
		}
	}
}

func TestDefaultEncoderFallsBackToSoftware(t *testing.T) {
	// Only the software chain is installed, so auto must not pick a GPU path.
	fakeLookPath(t, "x264")
	fakeProbe(t)
	if got := DefaultEncoder(context.Background(), "linux", CodecH264, "/dev/dri/renderD128"); got != EncoderSoftware {
		t.Errorf("got %q, want x264 when no ffmpeg is installed", got)
	}
}

func TestDefaultEncoderPrefersHardwareWhenItActuallyWorks(t *testing.T) {
	fakeLookPath(t, "ffmpeg")
	fakeProbe(t, EncoderVideoToolbox)
	if got := DefaultEncoder(context.Background(), "darwin", CodecH264, ""); got != EncoderVideoToolbox {
		t.Errorf("got %q, want videotoolbox when its trial encode succeeds", got)
	}
}

// The case that made this necessary: ffmpeg advertises h264_nvenc on a machine
// with no NVIDIA card. Locating the tools is not evidence the encoder works, so
// a failing trial must fall through rather than be chosen.
func TestDefaultEncoderSkipsAnEncoderThatDoesNotWork(t *testing.T) {
	fakeLookPath(t, "ffmpeg")
	fakeProbe(t) // nothing works
	if got := DefaultEncoder(context.Background(), "linux", CodecH264, "/dev/dri/renderD128"); got != EncoderSoftware {
		t.Errorf("got %q, want x264 when no hardware trial succeeds", got)
	}
}

// On Linux NVENC is preferred over VAAPI, but only if it actually encodes.
func TestDefaultEncoderFallsFromNVENCToVAAPI(t *testing.T) {
	fakeLookPath(t, "ffmpeg")
	fakeProbe(t, EncoderVAAPI)
	if got := DefaultEncoder(context.Background(), "linux", CodecH264, "/dev/dri/renderD128"); got != EncoderVAAPI {
		t.Errorf("got %q, want vaapi when nvenc's trial fails and vaapi's passes", got)
	}
}

func TestEveryPlatformHasASoftwareFallback(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows", "plan9"} {
		encs := Encoders(goos)
		if len(encs) == 0 || encs[len(encs)-1] != EncoderSoftware {
			t.Errorf("%s: x264 must be the last resort, got %v", goos, encs)
		}
	}
}

func hasTool(ts []Tool, name string) bool {
	for _, t := range ts {
		if t.Name == name {
			return true
		}
	}
	return false
}
func names(ts []Tool) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return out
}
func toolsOf(fs []Found) []Tool {
	out := make([]Tool, len(fs))
	for i, f := range fs {
		out[i] = f.Tool
	}
	return out
}

// An ffmpeg that is there but cannot drive the GPU asked for is reported as
// unusable, so --check and the start of a run say so before the demux.
func TestDetectForTriesTheGPUThroughFFmpeg(t *testing.T) {
	origLook, origProbe := LookPath, runProbe
	t.Cleanup(func() { LookPath, runProbe = origLook, origProbe })
	LookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	runProbe = func(context.Context, []string) error { return errors.New("Unknown encoder 'hevc_nvenc'") }
	o := DefaultOptions()
	o.Input, o.Output, o.Encoder, o.Codec = "/in/a.iso", "/out/a.mkv", EncoderNVENC, CodecH265
	rep := DetectFor(t.Context(), "linux", o)
	if rep.OK() || !strings.Contains(rep.String(), "UNUSABLE") || !strings.Contains(rep.String(), "hevc_nvenc") {
		t.Errorf("report:\n%s", rep)
	}
	runProbe = func(context.Context, []string) error { return nil }
	if rep := DetectFor(t.Context(), "linux", o); !rep.OK() {
		t.Errorf("a working ffmpeg is reported unusable:\n%s", rep)
	}
	o.NativeGPU = true
	runProbe = func(context.Context, []string) error { t.Error("probed ffmpeg for an in-process GPU"); return nil }
	DetectFor(t.Context(), "linux", o)
}

// Media Foundation is spelled out or as "mf"; through ffmpeg it is the _mf
// encoder at constant quality, hardware only.
func TestMediaFoundationEncoder(t *testing.T) {
	if got := ParseEncoder("mf"); got != EncoderMediaFoundation {
		t.Errorf(`ParseEncoder("mf") = %q`, got)
	}
	if enc := Encoders("windows"); len(enc) != 3 || enc[0] != EncoderNVENC || enc[1] != EncoderMediaFoundation {
		t.Errorf("windows encoders %v: NVENC, then Media Foundation, then software", enc)
	}
	o := DefaultOptions()
	o.Input, o.Output, o.Encoder, o.GPUAPI, o.Codec = "/in/a.iso", "/out/a.mkv", EncoderMediaFoundation, GPUFFmpeg, CodecH265
	p, err := BuildPlan("windows", o)
	if err != nil {
		t.Fatal(err)
	}
	s := p.String()
	for _, want := range []string{"-c:v hevc_mf", "-hw_encoding 1", "-rate_control quality", "-quality 65"} {
		if !strings.Contains(s, want) {
			t.Errorf("plan lacks %q:\n%s", want, s)
		}
	}
	if argv := strings.Join(probeArgv(EncoderMediaFoundation, CodecH264, "", false), " "); !strings.Contains(argv, "-c:v h264_mf -hw_encoding 1") {
		t.Errorf("the probe could pass on the software MFT: %s", argv)
	}
	if k, ok := hwKind(EncoderMediaFoundation); !ok || k != "mediafoundation" {
		t.Errorf("hwKind = %q, %v", k, ok)
	}
}

// AV1 is not offered on Media Foundation, in process or through ffmpeg.
func TestMediaFoundationRefusesAV1(t *testing.T) {
	o := DefaultOptions()
	o.Input, o.Output, o.Encoder, o.Codec = "/in/a.iso", "/out/a.mkv", EncoderMediaFoundation, CodecAV1
	if err := o.Validate("windows"); err == nil {
		t.Error("AV1 on Media Foundation must be refused")
	}
	if CodecAV1.ffmpegEncoder(EncoderMediaFoundation) != "" {
		t.Error("the ffmpeg route must not offer av1_mf")
	}
}
