package convert

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brunoga/bdtools/internal/mkv"
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
		if strings.HasSuffix(e.Name(), ".bdtools") {
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
	// One eye's shape, as ffmpeg-based players read the display size of a
	// side-by-side track: 640x480 square pixels, doubled to 1280x960.
	if v.DisplayWidth != 1280 || v.DisplayHeight != 960 {
		t.Errorf("display size %dx%d, want 1280x960", v.DisplayWidth, v.DisplayHeight)
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
		if strings.HasSuffix(e.Name(), ".bdtools") {
			t.Errorf("work directory left behind: %s", e.Name())
		}
	}
}

// A 2D source converts to a 2D file: one picture a frame, no stereo mode,
// every frame there, the depth the source's (a 10-bit source stays 10-bit).
// H.264 decodes with the decoder here or on the GPU, to the same pictures;
// HEVC needs the GPU. Skips without ffmpeg and the encoders.
func TestRunnerConverts2DSources(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "x264", "x265"} {
		if _, err := LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	work := t.TempDir()
	make2D := func(name string, args ...string) string {
		src := filepath.Join(work, name)
		if err := runCmd(t, "ffmpeg", append([]string{"-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", "testsrc2=s=320x240:r=24:d=1"}, append(args, "-f", "mpegts", src)...)...); err != nil {
			t.Skipf("could not build a 2D source: %v", err)
		}
		return src
	}
	h264 := make2D("avc.m2ts", "-c:v", "libx264", "-bf", "2")
	hevc10 := make2D("hevc.m2ts", "-c:v", "libx265", "-pix_fmt", "yuv420p10le", "-x265-params", "log-level=error")
	convert := func(t *testing.T, src string, codec Codec, dec Decoder) []byte {
		t.Helper()
		out := filepath.Join(t.TempDir(), "out.mkv")
		o := DefaultOptions()
		o.Input, o.Output = src, out
		o.Encoder, o.Codec, o.CRF, o.Preset, o.Decoder = EncoderSoftware, codec, 25, "ultrafast", dec
		o.BitDepth = 0 // the source's
		var lines []string
		r := NewRunner(CurrentGOOS, o, func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) })
		if err := r.Run(context.Background()); err != nil {
			if strings.Contains(err.Error(), "GPU") {
				t.Skipf("no GPU decoder: %v", err)
			}
			t.Fatalf("%v\n%s", err, strings.Join(lines, "\n"))
		}
		f, err := os.Open(out) //nolint:gosec // test
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		rd, err := mkv.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		v := rd.Tracks[0]
		if v.Width != 320 || v.Height != 240 || v.StereoMode != 0 || v.DisplayWidth != 0 {
			t.Errorf("video track %+v, want 320x240 and no stereo mode", v)
		}
		var data []byte
		frames := 0
		for {
			p, err := rd.Next()
			if err != nil {
				break
			}
			if p.Track == v.Number {
				frames++
				data = append(data, p.Data...)
			}
		}
		if frames != 24 {
			t.Errorf("%d frames, want 24", frames)
		}
		depth := 8
		if codec == CodecH265 { // hvcC's bitDepthLumaMinus8
			depth += int(v.CodecPrivate[17] & 7)
		}
		return append([]byte(fmt.Sprintf("%d-bit ", depth)), data...)
	}
	t.Run("H.264, the decoder here", func(t *testing.T) {
		cpu := convert(t, h264, CodecH264, DecoderCPU)
		t.Run("and on the GPU, the same", func(t *testing.T) {
			if gpu := convert(t, h264, CodecH264, DecoderGPU); !bytes.Equal(gpu, cpu) {
				t.Error("the GPU's pictures encode differently from the decoder's here")
			}
		})
	})
	t.Run("HEVC 10-bit stays 10-bit", func(t *testing.T) {
		if got := convert(t, hevc10, CodecH265, DecoderAuto); !bytes.HasPrefix(got, []byte("10-bit ")) {
			t.Errorf("output %s", got[:7])
		}
	})
}

// A 2D remux into Matroska carries the source's video untouched: it
// decodes to the same pictures, every one of them. From a transport stream
// and from a Matroska file, H.264 and HEVC. Skips without ffmpeg.
func TestRunnerRemuxes2DToMatroska(t *testing.T) {
	if _, err := LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	work := t.TempDir()
	frames := func(path string) []byte {
		out, err := exec.CommandContext(t.Context(), "ffmpeg", "-v", "error", "-i", path, "-map", "0:v:0", //nolint:gosec // test
			"-f", "framemd5", "-").Output()
		if err != nil {
			t.Fatalf("decoding %s: %v", path, err)
		}
		var sums []string
		for _, l := range strings.Split(string(out), "\n") {
			if f := strings.Split(l, ","); len(f) == 6 && !strings.HasPrefix(l, "#") {
				sums = append(sums, strings.TrimSpace(f[5]))
			}
		}
		return []byte(strings.Join(sums, " "))
	}
	for _, c := range []struct{ name, ext string }{
		{"h264", ".m2ts"}, {"h264", ".mkv"}, {"hevc", ".m2ts"},
	} {
		t.Run(c.name+c.ext, func(t *testing.T) {
			src := filepath.Join(work, c.name+c.ext)
			enc := map[string][]string{"h264": {"-c:v", "libx264", "-bf", "2"}, "hevc": {"-c:v", "libx265", "-x265-params", "log-level=error"}}[c.name]
			format := map[string]string{".m2ts": "mpegts", ".mkv": "matroska"}[c.ext]
			if err := runCmd(t, "ffmpeg", append(append([]string{"-hide_banner", "-loglevel", "error", "-y",
				"-f", "lavfi", "-i", "testsrc2=s=320x240:r=24:d=2", "-f", "lavfi", "-i", "sine=d=2",
				"-c:a", "ac3"}, enc...), "-f", format, src)...); err != nil {
				t.Skipf("making the source: %v", err)
			}
			out := filepath.Join(t.TempDir(), "remux.mkv")
			o := DefaultOptions()
			o.Input, o.Output, o.Remux = src, out, true
			var lines []string
			if err := NewRunner(CurrentGOOS, o, func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }).Run(t.Context()); err != nil {
				t.Fatalf("%v\n%s", err, strings.Join(lines, "\n"))
			}
			want, got := frames(src), frames(out)
			if len(want) == 0 || !bytes.Equal(got, want) {
				t.Errorf("the remux decodes to other pictures (%d vs %d bytes of sums)\n%s", len(got), len(want), strings.Join(lines, "\n"))
			}
		})
	}
}

// A UHD Blu-ray's Dolby Vision enhancement layer (PID 0x1015) goes into
// the remuxed HEVC track: each picture gains the enhancement layer's NAL
// units behind type 63 headers, the track gains the dvcC record, and the
// pictures still decode as the base layer's. The "enhancement layer" here is
// a second HEVC stream with the same structure, which is all the merge
// looks at. Skips without ffmpeg (with libx265) and ffprobe.
func TestRunnerRemuxKeepsDolbyVisionLayers(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	work := t.TempDir()
	src := filepath.Join(work, "uhd.m2ts")
	x265 := []string{"-c:v", "libx265", "-pix_fmt", "yuv420p10le", "-x265-params", "log-level=error:keyint=24:bframes=2"}
	args := []string{"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=s=320x240:r=24:d=2", "-f", "lavfi", "-i", "mandelbrot=s=320x240:r=24",
		"-map", "0:v", "-map", "1:v", "-t", "2"}
	args = append(append(args, x265...), "-streamid", "0:0x1011", "-streamid", "1:0x1015", "-f", "mpegts", src)
	if err := runCmd(t, "ffmpeg", args...); err != nil {
		t.Skipf("making the source: %v", err)
	}
	out := filepath.Join(work, "remux.mkv")
	o := DefaultOptions()
	o.Input, o.Output, o.Remux = src, out, true
	var lines []string
	if err := NewRunner(CurrentGOOS, o, func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }).Run(t.Context()); err != nil {
		t.Fatalf("%v\n%s", err, strings.Join(lines, "\n"))
	}
	probe, err := exec.CommandContext(t.Context(), "ffprobe", "-v", "error", "-show_streams", out).Output() //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"dv_profile=7", "el_present_flag=1", "bl_present_flag=1", "dv_bl_signal_compatibility_id=6"} {
		if !strings.Contains(string(probe), want) {
			t.Errorf("ffprobe does not show %s:\n%s", want, probe)
		}
	}
	if n := strings.Count(string(probe), "codec_type=video"); n != 1 {
		t.Errorf("%d video tracks, want 1", n)
	}
	es, err := exec.CommandContext(t.Context(), "ffmpeg", "-v", "error", "-i", out, "-c", "copy", //nolint:gosec // test
		"-bsf:v", "hevc_mp4toannexb", "-f", "hevc", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(es, []byte{0, 0, 1, 0x7e, 0x01}); n < 48 {
		t.Errorf("%d enhancement layer NAL units in the track, want at least one per picture", n)
	}
	base := filepath.Join(work, "base.hevc")
	if err := runCmd(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", src, "-map", "0:v:0", "-c", "copy", base); err != nil {
		t.Fatal(err)
	}
	sums := func(path string) string {
		b, err := exec.CommandContext(t.Context(), "ffmpeg", "-v", "error", "-i", path, "-map", "0:v:0", "-f", "framemd5", "-").Output() //nolint:gosec // test
		if err != nil {
			t.Fatal(err)
		}
		var s []string
		for _, l := range strings.Split(string(b), "\n") {
			if f := strings.Split(l, ","); len(f) == 6 && !strings.HasPrefix(l, "#") {
				s = append(s, strings.TrimSpace(f[5]))
			}
		}
		return strings.Join(s, " ")
	}
	if got, want := sums(out), sums(base); got == "" || got != want {
		t.Errorf("the remux does not decode to the base layer's pictures\n%s", strings.Join(lines, "\n"))
	}
}

// An HDR10 source keeps its HDR: the colour signalling and the mastering
// display and light level metadata, in the stream and in the Matroska
// Colour element. Skips without ffmpeg, x265 and a GPU decoder for HEVC.
func TestRunnerCarriesHDR10(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe", "x265"} {
		if _, err := LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	src := filepath.Join(t.TempDir(), "hdr10.mkv")
	if err := runCmd(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=s=320x240:r=24:d=1",
		"-c:v", "libx265", "-pix_fmt", "yuv420p10le", "-x265-params",
		"log-level=error:hdr10=1:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc:"+
			"master-display=G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1):max-cll=1000,400", src); err != nil {
		t.Skipf("making an HDR10 source: %v", err)
	}
	out := filepath.Join(t.TempDir(), "out.mkv")
	o := DefaultOptions()
	o.Input, o.Output, o.Encoder, o.Codec, o.CRF, o.Preset = src, out, EncoderSoftware, CodecH265, 25, "ultrafast"
	if err := NewRunner(CurrentGOOS, o, nil).Run(t.Context()); err != nil {
		if strings.Contains(err.Error(), "GPU") {
			t.Skipf("no GPU decoder: %v", err)
		}
		t.Fatal(err)
	}
	probe, err := exec.CommandContext(t.Context(), "ffprobe", "-v", "error", "-select_streams", "v:0", "-read_intervals", "%+#1", //nolint:gosec // test
		"-show_frames", "-show_streams", "-of", "flat", out).Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`color_transfer="smpte2084"`, `color_primaries="bt2020"`, `pix_fmt="yuv420p10le"`,
		`red_x="34000/50000"`, `max_luminance="10000000/10000"`, `max_content=1000`, `max_average=400`} {
		if !strings.Contains(string(probe), want) {
			t.Errorf("no %s in the output", want)
		}
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
