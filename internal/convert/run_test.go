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
	// These describe the tsMuxeR pipeline's tool handling.
	o.Demuxer = DemuxerTSMuxeR
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
// found in $PATH" tells someone nothing about which of five programs to go and
// install.
func TestRunnerNamesAMissingTool(t *testing.T) {
	r := NewRunner("linux", runnerOpts(t), nil)
	r.tool = func(string) (string, error) { return "", errors.New("nope") }
	err := r.Run(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"tsmuxer", "is not installed", "demux", "try:"} {
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

// The work directory is created under TempDir and removed afterwards. Demuxed
// views are tens of gigabytes, so leaving them behind is not a small mistake.
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

// The end-to-end test: build a real 3D m2ts by muxing MVC elementary streams,
// convert it, and check the result. This is the only way to know the meta file,
// the demuxed filenames and the stream plumbing are all right at once.
//
// Skips unless the toolchain is present.
func TestRunnerConvertsARealSource(t *testing.T) {
	fixtures := fixtureDir(t)
	for _, n := range []string{"tsMuxeR", "x264", "mkvmerge"} {
		if _, err := LookPath(n); err != nil {
			t.Skipf("%s not installed", n)
		}
	}
	for _, f := range []string{"mvc_base.264", "mvc_dependent.mvc"} {
		if _, err := os.Stat(filepath.Join(fixtures, f)); err != nil {
			t.Skipf("fixture %s missing", f)
		}
	}

	source := buildTestSource(t, t.TempDir(), fixtures)
	for _, c := range []struct {
		dm Demuxer
		mx Muxer
	}{{DemuxerBuiltin, MuxerBuiltin}, {DemuxerBuiltin, MuxerMkvmerge}, {DemuxerTSMuxeR, MuxerBuiltin}, {DemuxerTSMuxeR, MuxerMkvmerge}} {
		dm := c.dm
		t.Run(string(c.dm)+"+"+string(c.mx), func(t *testing.T) {
			work := t.TempDir()
			// A name with spaces and brackets, as a real library uses.
			out := filepath.Join(work, "Test Movie (2012) 3D.mkv")
			o := DefaultOptions()
			o.Input, o.Output, o.TempDir = source, out, work
			o.Encoder = EncoderSoftware
			o.CRF, o.Preset = 25, "ultrafast"
			o.Demuxer = dm
			o.Muxer = c.mx

			r := NewRunner(CurrentGOOS, o, nil)
			if err := r.Run(context.Background()); err != nil {
				t.Fatalf("conversion failed: %v", err)
			}

			st, err := os.Stat(out)
			if err != nil {
				t.Fatalf("no output: %v", err)
			}
			if st.Size() == 0 {
				t.Fatal("output is empty")
			}
			// The stacked frame must be double the single-view width.
			if w, h := probeSize(t, out); w != 1280 || h != 480 {
				t.Errorf("output is %dx%d, want 1280x480 side-by-side", w, h)
			}
			// And nothing may be left behind.
			entries, _ := os.ReadDir(work)
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "mvctools-") {
					t.Errorf("work directory left behind: %s", e.Name())
				}
			}
		})
	}
}

// A 2D source has to be refused by name, not fail obscurely partway through.
func TestRunnerRefusesA2DSource(t *testing.T) {
	for _, n := range []string{"tsMuxeR", "ffmpeg"} {
		if _, err := LookPath(n); err != nil {
			t.Skipf("%s not installed", n)
		}
	}
	work := t.TempDir()
	src := filepath.Join(work, "2d.m2ts")
	if err := runCmd(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=s=320x240:d=1", "-c:v", "libx264", src); err != nil {
		t.Skipf("could not build a 2D source: %v", err)
	}
	for _, dm := range []Demuxer{DemuxerBuiltin, DemuxerTSMuxeR} {
		o := DefaultOptions()
		o.Input, o.Output, o.TempDir = src, filepath.Join(work, "x.mkv"), work
		o.Encoder = EncoderSoftware
		o.Demuxer = dm
		err := NewRunner(CurrentGOOS, o, nil).Run(context.Background())
		if err == nil {
			t.Fatalf("%s: a 2D source must be refused", dm)
		}
		if !strings.Contains(err.Error(), "not 3D") {
			t.Errorf("%s: error should say the source is not 3D, got: %v", dm, err)
		}
	}
}

// --- helpers for the end-to-end tests ---------------------------------------

// buildTestSource muxes the MVC fixtures into a real 3D m2ts with an audio
// track, which is what makes an end-to-end test possible without a disc: the
// demuxer's own muxer builds the source it will later take apart.
func buildTestSource(t *testing.T, work, fixtures string) string {
	t.Helper()
	audio := filepath.Join(work, "audio.ac3")
	if err := runCmd(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-c:a", "ac3", "-b:a", "192k", audio); err != nil {
		t.Skipf("could not build an audio track: %v", err)
	}
	meta := filepath.Join(work, "mux.meta")
	content := "MUXOPT --no-pcr-on-video-pid --new-audio-pes --vbr --vbv-len=500\n" +
		`V_MPEG4/ISO/AVC, "` + filepath.Join(fixtures, "mvc_base.264") + `", fps=23.976, insertSEI, contSPS` + "\n" +
		`V_MPEG4/ISO/MVC, "` + filepath.Join(fixtures, "mvc_dependent.mvc") + `", fps=23.976, insertSEI, contSPS` + "\n" +
		`A_AC3, "` + audio + `", lang=eng` + "\n"
	if err := os.WriteFile(meta, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(work, "source.m2ts")
	if err := runCmd(t, "tsMuxeR", meta, src); err != nil {
		t.Skipf("could not mux a 3D source: %v", err)
	}
	return src
}

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

// probeSize returns a file's video dimensions via ffprobe.
func probeSize(t *testing.T, path string) (int, int) {
	t.Helper()
	bin, err := LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not installed")
	}
	out, err := execCommand(bin, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height", "-of", "csv=p=0:s=x", path)
	if err != nil {
		t.Fatalf("ffprobe: %v\n%s", err, out)
	}
	var w, h int
	if _, err := fmt.Sscanf(strings.TrimSpace(out), "%dx%d", &w, &h); err != nil {
		t.Fatalf("could not read dimensions from %q", out)
	}
	return w, h
}

func execCommand(bin string, args ...string) (string, error) {
	cmd := exec.CommandContext(context.Background(), bin, args...) //nolint:gosec // test helper
	b, err := cmd.CombinedOutput()
	return string(b), err
}

// The point of reading the image directly: mounting one needs root, which
// rules it out for an unattended conversion. This builds a real UDF Blu-ray
// image from the MVC fixtures and converts it without touching a mount.
func TestRunnerConvertsADiscImage(t *testing.T) {
	fixtures := fixtureDir(t)
	for _, n := range []string{"tsMuxeR", "x264", "mkvmerge", "ffmpeg"} {
		if _, err := LookPath(n); err != nil {
			t.Skipf("%s not installed", n)
		}
	}
	iso := buildTestISO(t, t.TempDir(), fixtures)
	for _, c := range []struct {
		dm Demuxer
		mx Muxer
	}{{DemuxerBuiltin, MuxerBuiltin}, {DemuxerBuiltin, MuxerMkvmerge}, {DemuxerTSMuxeR, MuxerBuiltin}, {DemuxerTSMuxeR, MuxerMkvmerge}} {
		dm := c.dm
		t.Run(string(c.dm)+"+"+string(c.mx), func(t *testing.T) {
			work := t.TempDir()
			out := filepath.Join(work, "From Image (2012) 3D.mkv")
			o := DefaultOptions()
			o.Input, o.Output, o.TempDir = iso, out, work
			o.Encoder, o.CRF, o.Preset = EncoderSoftware, 25, "ultrafast"
			o.Demuxer = dm
			o.Muxer = c.mx

			var lines []string
			r := NewRunner(CurrentGOOS, o, func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) })
			if err := r.Run(context.Background()); err != nil {
				t.Fatalf("converting the image failed: %v\n%s", err, strings.Join(lines, "\n"))
			}
			if w, h := probeSize(t, out); w != 1280 || h != 480 {
				t.Errorf("output is %dx%d, want 1280x480", w, h)
			}
			// It must say it read the image and which title it picked, since
			// on a real disc both are decisions the operator would otherwise
			// have had to make.
			joined := strings.Join(lines, "\n")
			for _, want := range []string{"disc image", "chose "} {
				if !strings.Contains(joined, want) {
					t.Errorf("progress should mention %q, got:\n%s", want, joined)
				}
			}
			// Nothing extracted from the image may be left behind — on a real
			// disc that is tens of gigabytes.
			entries, _ := os.ReadDir(work)
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "mvctools-") {
					t.Errorf("work directory left behind: %s", e.Name())
				}
			}
		})
	}
}

// buildTestISO muxes the MVC fixtures into a real UDF Blu-ray image, which is
// what makes an image test possible without a disc.
func buildTestISO(t *testing.T, work, fixtures string) string {
	t.Helper()
	audio := filepath.Join(work, "iso-audio.ac3")
	if err := runCmd(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-c:a", "ac3", "-b:a", "192k", audio); err != nil {
		t.Skipf("could not build an audio track: %v", err)
	}
	meta := filepath.Join(work, "bd.meta")
	content := "MUXOPT --blu-ray --no-pcr-on-video-pid --new-audio-pes --vbr --vbv-len=500\n" +
		`V_MPEG4/ISO/AVC, "` + filepath.Join(fixtures, "mvc_base.264") + `", fps=23.976, insertSEI, contSPS` + "\n" +
		`V_MPEG4/ISO/MVC, "` + filepath.Join(fixtures, "mvc_dependent.mvc") + `", fps=23.976, insertSEI, contSPS` + "\n" +
		`A_AC3, "` + audio + `", lang=eng` + "\n"
	if err := os.WriteFile(meta, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	iso := filepath.Join(work, "disc.iso")
	if err := runCmd(t, "tsMuxeR", meta, iso); err != nil {
		t.Skipf("could not build a Blu-ray image: %v", err)
	}
	return iso
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
