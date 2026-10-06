package convert

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brunoga/mvc/internal/mkv"
)

func runnerOpts(t *testing.T) Options {
	t.Helper()
	dir := t.TempDir()
	// A real file: a source that does not exist is now reported before any
	// tool is looked up, which would mask what these tests are checking.
	in := filepath.Join(dir, "disc.m2ts")
	if err := os.WriteFile(in, []byte("not really an m2ts"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := DefaultOptions()
	o.Input = in
	o.Output = filepath.Join(dir, "out.mkv")
	o.TempDir = t.TempDir()
	o.Encoder = EncoderSoftware
	return o
}

// A source that is not there is said so plainly, and before anything expensive.
func TestRunnerReportsAMissingSource(t *testing.T) {
	o := runnerOpts(t)
	o.Input = filepath.Join(t.TempDir(), "nope.m2ts")
	r := NewRunner("linux", o, nil)
	r.tool = func(string) (string, error) { return "/bin/true", nil }
	err := r.Run(context.Background())
	if err == nil {
		t.Fatal("a missing source must be refused")
	}
	if !strings.Contains(err.Error(), "nope.m2ts") {
		t.Errorf("the error should name the source, got %q", err)
	}
}

// A missing tool must name itself and what it is for. "executable file not
// found in $PATH" tells someone nothing about which program to go and
// install.
func TestRunnerNamesAMissingTool(t *testing.T) {
	o := runnerOpts(t)
	o.Input = bluray("folder")
	r := NewRunner("linux", o, nil)
	r.tool = func(string) (string, error) { return "", errors.New("nope") }
	err := r.Run(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"x264", "is not installed", "encode", "try:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should contain %q, got:\n%s", want, err)
		}
	}
}

// Configuration is checked before anything runs: a conversion is hours long,
// so an impossible request must not get as far as the demux.
func TestRunnerValidatesBeforeTouchingTheSource(t *testing.T) {
	o := runnerOpts(t)
	o.Output = "/nowhere/out.avi" // not a container this writes
	r := NewRunner("linux", o, nil)
	called := false
	r.tool = func(n string) (string, error) { called = true; return "/bin/true", nil }
	if err := r.Run(context.Background()); err == nil {
		t.Fatal("an impossible request must be refused")
	}
	if called {
		t.Error("no tool should be resolved before the options are validated")
	}
}

// The work directory is created under TempDir and removed afterwards. A
// film's audio and encoded video are tens of gigabytes, so leaving them
// behind is not a small mistake.
func TestRunnerCleansUpItsWorkDirectory(t *testing.T) {
	o := runnerOpts(t)
	r := NewRunner("linux", o, nil)
	r.tool = func(string) (string, error) { return "", errors.New("missing") }
	_ = r.Run(context.Background())
	entries, err := os.ReadDir(o.TempDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "mvctools-") {
			t.Errorf("left a work directory behind: %s", e.Name())
		}
	}
}

func TestRunnerKeepsTempWhenAsked(t *testing.T) {
	o := runnerOpts(t)
	var lines []string
	r := NewRunner("linux", o, func(f string, a ...any) { lines = append(lines, f) })
	r.KeepTemp = true
	r.tool = func(string) (string, error) { return "", errors.New("missing") }
	_ = r.Run(context.Background())
	var kept bool
	for _, l := range lines {
		if strings.Contains(l, "keeping") {
			kept = true
		}
	}
	if !kept {
		t.Errorf("--keep-temp should say what it kept, got %v", lines)
	}
}

// The reporter is optional, and a nil one must not panic — --quiet passes nil.
func TestNilReporterIsSafe(t *testing.T) {
	var r Reporter
	r.Report("this must not panic %d", 1)
}

// encoderTool follows the chosen encoder and codec, because resolving x264 for
// an ffmpeg run would fail on a machine that has only one of them — and
// resolving x264 for an HEVC run would invoke it with x265's flags.
func TestEncoderToolFollowsTheEncoderAndCodec(t *testing.T) {
	for _, c := range []struct {
		enc   Encoder
		codec Codec
		want  string
	}{
		{EncoderSoftware, CodecH264, "x264"},
		{EncoderSoftware, CodecH265, "x265"},
		{EncoderNVENC, CodecH264, "ffmpeg"},
		{EncoderNVENC, CodecH265, "ffmpeg"},
		{EncoderVAAPI, CodecH265, "ffmpeg"},
		{EncoderVideoToolbox, CodecH265, "ffmpeg"},
	} {
		if got := encoderTool(c.enc, c.codec, false).Name; got != c.want {
			t.Errorf("%s/%s should run through %s, got %s", c.enc, c.codec, c.want, got)
		}
	}
}

// The runner and the plan must agree on where the encoded stream goes: the
// plan names it from the codec, and the runner builds the same path itself.
func TestRunnerAndPlanAgreeOnTheVideoPath(t *testing.T) {
	for _, cod := range Codecs() {
		o := opts("linux", func(o *Options) { o.Codec = cod })
		p, err := BuildPlan("linux", o)
		if err != nil {
			t.Fatalf("%s: %v", cod, err)
		}
		want := filepath.Join(o.TempDir, "stacked"+cod.streamExt())
		var muxIn string
		for _, s := range p.Steps {
			if s.Name == "mux" {
				muxIn = s.Argv[len(s.Argv)-1]
			}
		}
		if muxIn != want {
			t.Errorf("%s: plan muxes %q, runner writes %q", cod, muxIn, want)
		}
	}
}

// The end-to-end test: convert a Matroska remux of the MVC fixtures, as a
// MakeMKV rip of a 3D disc is, and check the result: the whole pipeline at
// once, demux to mux.
//
// Skips without x264.
func TestRunnerConvertsAMatroskaSource(t *testing.T) {
	if _, err := LookPath("x264"); err != nil {
		t.Skip("x264 not installed")
	}
	source := mvcMatroska(t)
	work := t.TempDir()
	// A name with spaces and brackets, as a real library uses.
	out := filepath.Join(work, "Test Movie (2012) 3D.mkv")
	o := DefaultOptions()
	o.Input, o.Output, o.TempDir = source, out, work
	o.Encoder = EncoderSoftware
	o.CRF, o.Preset = 25, "ultrafast"
	if err := NewRunner(CurrentGOOS, o, nil).Run(context.Background()); err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	checkConverted(t, out, work, "Surround", "Signs")
}

// checkConverted checks a conversion's output: side by side at double the
// view width, the named tracks there, and no work directory left behind.
func checkConverted(t *testing.T, out, work string, names ...string) {
	t.Helper()
	f, err := os.Open(out) //nolint:gosec // test
	if err != nil {
		t.Fatalf("no output: %v", err)
	}
	defer func() { _ = f.Close() }()
	rd, err := mkv.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	v := rd.Tracks[0]
	if v.Type != mkv.TypeVideo || v.Width != 1280 || v.Height != 480 || v.StereoMode != 1 {
		t.Errorf("video track %+v, want 1280x480 side by side", v)
	}
	have := map[string]bool{}
	for _, tr := range rd.Tracks {
		have[tr.Name] = true
	}
	for _, n := range names {
		if !have[n] {
			t.Errorf("no track named %q in %+v", n, rd.Tracks)
		}
	}
	entries, _ := os.ReadDir(work)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "mvctools-") {
			t.Errorf("work directory left behind: %s", e.Name())
		}
	}
}

// A 2D source has to be refused by name, not fail obscurely partway through.
func TestRunnerRefusesA2DSource(t *testing.T) {
	if _, err := LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	work := t.TempDir()
	src := filepath.Join(work, "2d.m2ts")
	if err := runCmd(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=s=320x240:d=1", "-c:v", "libx264", src); err != nil {
		t.Skipf("could not build a 2D source: %v", err)
	}
	o := DefaultOptions()
	o.Input, o.Output, o.TempDir = src, filepath.Join(work, "x.mkv"), work
	o.Encoder = EncoderSoftware
	err := NewRunner(CurrentGOOS, o, nil).Run(context.Background())
	if err == nil {
		t.Fatal("a 2D source must be refused")
	}
	if !strings.Contains(err.Error(), "not 3D") {
		t.Errorf("error should say the source is not 3D, got: %v", err)
	}
}

// --- helpers for the end-to-end tests ---------------------------------------

func runCmd(t *testing.T, name string, args ...string) error {
	t.Helper()
	bin, err := LookPath(name)
	if err != nil {
		return err
	}
	out, err := execCommand(bin, args...)
	if err != nil {
		return fmt.Errorf("%s: %w\n%s", name, err, out)
	}
	return nil
}

func execCommand(bin string, args ...string) (string, error) {
	cmd := exec.CommandContext(context.Background(), bin, args...) //nolint:gosec // test helper
	b, err := cmd.CombinedOutput()
	return string(b), err
}

// The point of reading the image directly: mounting one needs root, which
// rules it out for an unattended conversion. The Blu-ray fixture converts as
// a folder and as a UDF image, read in place, with the feature chosen and
// reported.
func TestRunnerConvertsADiscImage(t *testing.T) {
	if _, err := LookPath("x264"); err != nil {
		t.Skip("x264 not installed")
	}
	for _, form := range bothForms {
		t.Run(form, func(t *testing.T) {
			work := t.TempDir()
			out := filepath.Join(work, "From Image (2012) 3D.mkv")
			o := DefaultOptions()
			o.Input, o.Output, o.TempDir = bluray(form), out, work
			o.Encoder, o.CRF, o.Preset = EncoderSoftware, 25, "ultrafast"

			var lines []string
			r := NewRunner(CurrentGOOS, o, func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) })
			if err := r.Run(context.Background()); err != nil {
				t.Fatalf("converting failed: %v\n%s", err, strings.Join(lines, "\n"))
			}
			checkConverted(t, out, work)
			// It must say which title it picked, and that an image was read
			// in place: on a real disc both are decisions the operator would
			// otherwise have had to make.
			joined := strings.Join(lines, "\n")
			wants := []string{"chose "}
			if form == "disc.iso" {
				wants = append(wants, "disc image")
			}
			for _, want := range wants {
				if !strings.Contains(joined, want) {
					t.Errorf("progress should mention %q, got:\n%s", want, joined)
				}
			}
		})
	}
}

// An image with no Blu-ray structure must say so rather than fail obscurely.
func TestRunnerRefusesAnImageWithNoBDMV(t *testing.T) {
	work := t.TempDir()
	iso := filepath.Join(work, "empty.iso")
	// Not a UDF image at all: the failure should name the image, not panic.
	if err := os.WriteFile(iso, make([]byte, 1<<16), 0o600); err != nil {
		t.Fatal(err)
	}
	o := DefaultOptions()
	o.Input, o.Output, o.TempDir = iso, filepath.Join(work, "x.mkv"), work
	o.Encoder = EncoderSoftware
	r := NewRunner(CurrentGOOS, o, nil)
	r.tool = func(string) (string, error) { return "/bin/true", nil }
	err := r.Run(context.Background())
	if err == nil {
		t.Fatal("a non-UDF image must be refused")
	}
	if !strings.Contains(err.Error(), "empty.iso") && !strings.Contains(err.Error(), "UDF") {
		t.Errorf("error should name the image or the format, got: %v", err)
	}
}
