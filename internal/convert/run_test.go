package convert

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/brunoga/bdtools/internal/dovi"
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
	t.Run("MPEG-2, the decoder here", func(t *testing.T) {
		mpeg2 := make2D("mpeg2.m2ts", "-c:v", "mpeg2video", "-bf", "2", "-q:v", "3")
		if got := convert(t, mpeg2, CodecH264, DecoderCPU); !bytes.HasPrefix(got, []byte("8-bit ")) {
			t.Errorf("output %s", got[:7])
		}
		t.Run("in Matroska", func(t *testing.T) {
			remux := filepath.Join(work, "mpeg2.mkv")
			if err := runCmd(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", mpeg2, "-c", "copy", remux); err != nil {
				t.Skipf("could not remux: %v", err)
			}
			if got := convert(t, remux, CodecH264, DecoderCPU); !bytes.HasPrefix(got, []byte("8-bit ")) {
				t.Errorf("output %s", got[:7])
			}
		})
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
	for _, dec := range []Decoder{DecoderAuto, DecoderCPU} { // the GPU's, else ffmpeg's; ffmpeg's
		t.Run("decoder="+cmp.Or(string(dec), "auto"), func(t *testing.T) { carriesHDR10(t, src, dec) })
	}
}

func carriesHDR10(t *testing.T, src string, dec Decoder) {
	out := filepath.Join(t.TempDir(), "out.mkv")
	o := DefaultOptions()
	o.Input, o.Output, o.Encoder, o.Codec, o.CRF, o.Preset, o.Decoder = src, out, EncoderSoftware, CodecH265, 25, "ultrafast", dec
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

// A Dolby Vision profile 7 source converts to profile 8.1: every frame of
// the encode ends with its own picture's RPU, converted, and the track has
// the profile 8.1 configuration. The source is an HEVC Matroska file whose
// frames carry a FEL disc's RPUs in turn. Skips without ffmpeg, x265 and a
// GPU decoder for HEVC.
func TestRunnerCarriesDolbyVision(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "x265"} {
		if _, err := LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	dir := t.TempDir()
	es := filepath.Join(dir, "bl.hevc")
	if err := runCmd(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=s=320x240:r=24:d=1",
		"-c:v", "libx265", "-pix_fmt", "yuv420p10le", "-x265-params",
		"log-level=error:bframes=3:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc", "-f", "hevc", es); err != nil {
		t.Skipf("making the base layer: %v", err)
	}
	fixture, err := os.ReadFile(filepath.Join("..", "dovi", "testdata", "fel-cmv29.bin"))
	if err != nil {
		t.Fatal(err)
	}
	var rpus, want [][]byte
	for _, p := range bytes.Split(fixture, []byte{0, 0, 0, 1})[1:] {
		nal := append([]byte{dovi.NALRPU << 1, 1}, p...)
		rpus = append(rpus, nal)
		u, err := dovi.ParseNAL(nal)
		if err != nil {
			t.Fatal(err)
		}
		if err := u.ToProfile81(); err != nil {
			t.Fatal(err)
		}
		want = append(want, u.NAL())
	}
	// The source: each frame, by display index, ends with RPU i mod n.
	src := filepath.Join(dir, "p7.mkv")
	f, err := os.Open(es) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	v, err := mkv.NewVideoSource(f, mkv.HEVC, 24, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	v.SetTrailer(func(display int64) ([][]byte, error) { return [][]byte{rpus[int(display)%len(rpus)]}, nil })
	w, err := os.Create(src) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	if err := mkv.Mux(w, []mkv.Source{v}, mkv.Options{}); err != nil {
		t.Fatal(err)
	}
	_, _ = f.Close(), w.Close()

	out := filepath.Join(dir, "out.mkv")
	o := DefaultOptions()
	o.Input, o.Output, o.Encoder, o.Codec, o.CRF, o.Preset = src, out, EncoderSoftware, CodecH265, 25, "ultrafast"
	var lines []string
	if err := NewRunner(CurrentGOOS, o, func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }).Run(t.Context()); err != nil {
		if strings.Contains(err.Error(), "GPU") {
			t.Skipf("no GPU decoder: %v", err)
		}
		t.Fatalf("%v\n%s", err, strings.Join(lines, "\n"))
	}
	in, err := os.Open(out) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	r, err := mkv.NewReader(in)
	if err != nil {
		t.Fatal(err)
	}
	var cfg *dovi.Config
	for _, a := range r.Tracks[0].BlockAdditions {
		if a.Type == mkv.FourCC("dvvC") {
			c, err := dovi.ParseConfig(a.ExtraData)
			if err != nil {
				t.Fatal(err)
			}
			cfg = &c
		}
	}
	if cfg == nil || cfg.Profile != 8 || cfg.Compatibility != 1 || cfg.EL || !cfg.RPU {
		t.Errorf("configuration %+v, want profile 8.1 (dvvC)", cfg)
	}
	type frame struct {
		at  time.Duration
		rpu []byte
	}
	var frames []frame
	for {
		p, err := r.Next()
		if err != nil {
			break
		}
		var last []byte
		for b := p.Data; len(b) >= 4; {
			n := int(binary.BigEndian.Uint32(b))
			if n > len(b)-4 {
				break
			}
			last, b = b[4:4+n], b[4+n:]
		}
		frames = append(frames, frame{p.Time, last})
	}
	sort.Slice(frames, func(i, j int) bool { return frames[i].at < frames[j].at })
	if len(frames) != 24 {
		t.Fatalf("%d frames, want 24\n%s", len(frames), strings.Join(lines, "\n"))
	}
	for i, fr := range frames {
		if !bytes.Equal(fr.rpu, want[i%len(want)]) {
			t.Fatalf("frame %d does not end with its picture's RPU, converted", i)
		}
	}
}

// felSource is a Dolby Vision profile 7 FEL source for tests: a Matroska
// file of two HEVC layers coded alike (the enhancement layer half the
// size), interleaved as Matroska carries them, with a FEL disc's RPUs in
// turn. It skips without ffmpeg and x265.
type felSource struct {
	path       string // the Matroska file
	blES, elES string // the layers' elementary streams
	rpus       [][]byte
	w, h       int
}

func makeFELSource(t *testing.T, blCRF int) felSource {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "x265"} {
		if _, err := LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	dir := t.TempDir()
	const w, h = 640, 360 // NVDEC's smallest HEVC is 144x144: the enhancement layer is 320x180
	x265 := "log-level=error:bframes=3:b-adapt=0:scenecut=0:keyint=24:min-keyint=24:open-gop=0"
	encode := func(name, src string, w, h, crf int, vf string) string {
		p := filepath.Join(dir, name)
		if err := runCmd(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i",
			fmt.Sprintf("%s=s=%dx%d:r=24:d=1", src, w, h), "-vf", vf,
			"-c:v", "libx265", "-pix_fmt", "yuv420p10le", "-crf", fmt.Sprint(crf),
			"-x265-params", x265+":colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc", "-f", "hevc", p); err != nil {
			t.Skipf("making %s: %v", name, err)
		}
		return p
	}
	// An enhancement layer is a residual: a few codes either side of the
	// RPU's offset (512), as a disc's is.
	lowContrast := "format=yuv420p,lutyuv=y=128+(val-128)/10:u=128+(val-128)/10:v=128+(val-128)/10"
	fs := felSource{blES: encode("bl.hevc", "gradients", w, h, blCRF, "null"),
		elES: encode("el.hevc", "testsrc2", w/2, h/2, 10, lowContrast), w: w, h: h}
	fixture, err := os.ReadFile(filepath.Join("..", "dovi", "testdata", "fel-cmv29.bin"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range bytes.Split(fixture, []byte{0, 0, 0, 1})[1:] {
		fs.rpus = append(fs.rpus, append([]byte{dovi.NALRPU << 1, 1}, p...))
	}
	// The enhancement layer's NAL units by display index, behind type 63
	// headers.
	elFrames := map[int64][][]byte{}
	ef, err := os.Open(fs.elES) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	ev, err := mkv.NewVideoSource(ef, mkv.HEVC, 24, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	for {
		f, err := ev.Next()
		if err != nil {
			break
		}
		d := int64(f.PTS / (time.Second / 24))
		for b := f.Data; len(b) >= 4; {
			n := int(binary.BigEndian.Uint32(b))
			elFrames[d] = append(elFrames[d], append([]byte{dovi.NALEL << 1, 1}, b[4:4+n]...))
			b = b[4+n:]
		}
	}
	_ = ef.Close()
	fs.path = filepath.Join(dir, "fel.mkv")
	bf, err := os.Open(fs.blES) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	v, err := mkv.NewVideoSource(bf, mkv.HEVC, 24, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	v.SetTrailer(func(d int64) ([][]byte, error) {
		return append(append([][]byte(nil), elFrames[d]...), fs.rpus[int(d)%len(fs.rpus)]), nil
	})
	out, err := os.Create(fs.path) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	if err := mkv.Mux(out, []mkv.Source{v}, mkv.Options{}); err != nil {
		t.Fatal(err)
	}
	_, _ = bf.Close(), out.Close()
	return fs
}

// rawP010 decodes a stream's pictures, in display order, as P010.
func rawP010(t *testing.T, path string) []byte {
	t.Helper()
	b, err := exec.CommandContext(t.Context(), "ffmpeg", "-v", "error", "-i", path, "-pix_fmt", "p010le", "-f", "rawvideo", "-").Output() //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func p010Picture(b []byte, w, h int) *dovi.Picture {
	return &dovi.Picture{Width: w, Height: h, Y: b[:w*h*2], UV: b[w*h*2 : w*h*3], Pitch: 2 * w}
}

func lumaPSNR(a, b []byte) float64 {
	var se float64
	for i := 0; i+1 < len(a); i += 2 {
		d := float64(int(binary.LittleEndian.Uint16(a[i:])>>6) - int(binary.LittleEndian.Uint16(b[i:])>>6))
		se += d * d
	}
	return 10 * math.Log10(1023*1023*float64(len(a)/2)/max(se, 1e-9))
}

// composition is the picture a source's layers compose to, frame by frame.
func (fs felSource) composition(t *testing.T, bl, el []byte) []*dovi.Picture {
	t.Helper()
	bs, es := fs.w*fs.h*3, fs.w*fs.h*3/4
	var out []*dovi.Picture
	for i := 0; (i+1)*bs <= len(bl) && (i+1)*es <= len(el); i++ {
		u, err := dovi.ParseNAL(fs.rpus[i%len(fs.rpus)])
		if err != nil {
			t.Fatal(err)
		}
		c, err := dovi.NewComposer(u)
		if err != nil {
			t.Fatal(err)
		}
		p := p010Picture(make([]byte, bs), fs.w, fs.h)
		if err := c.Compose(p, p010Picture(bl[i*bs:], fs.w, fs.h), p010Picture(el[i*es:], fs.w/2, fs.h/2)); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// A Dolby Vision profile 7 FEL source is composed: the encode is the base
// layer mapped and corrected by the enhancement layer, picture by picture,
// as dovi.Composer makes it. Skips without ffmpeg, x265 and a GPU decoder.
func TestRunnerComposesFEL(t *testing.T) {
	fs := makeFELSource(t, 10)
	for _, dec := range []Decoder{DecoderAuto, DecoderCPU} { // the GPU's, else ffmpeg's; ffmpeg's
		t.Run("decoder="+cmp.Or(string(dec), "auto"), func(t *testing.T) { composesFEL(t, fs, dec) })
	}
}

func composesFEL(t *testing.T, fs felSource, dec Decoder) {
	out := filepath.Join(t.TempDir(), "out.mkv")
	o := DefaultOptions()
	o.Input, o.Output, o.Encoder, o.Codec, o.CRF, o.Preset, o.Decoder = fs.path, out, EncoderSoftware, CodecH265, 4, "ultrafast", dec
	var lines []string
	if err := NewRunner(CurrentGOOS, o, func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }).Run(t.Context()); err != nil {
		if strings.Contains(err.Error(), "GPU") {
			t.Skipf("no GPU decoder: %v", err)
		}
		t.Fatalf("%v\n%s", err, strings.Join(lines, "\n"))
	}
	if !slices.Contains(lines, "composed the full enhancement layer into 24 pictures") {
		t.Fatalf("the enhancement layer was not composed:\n%s", strings.Join(lines, "\n"))
	}
	bl := rawP010(t, fs.blES)
	ref := fs.composition(t, bl, rawP010(t, fs.elES))
	enc := rawP010(t, out)
	bs, ys := fs.w*fs.h*3, fs.w*fs.h*2
	if len(ref) != 24 || len(enc) != 24*bs {
		t.Fatalf("%d pictures composed, %d bytes encoded", len(ref), len(enc))
	}
	for i, r := range ref {
		got := enc[i*bs : i*bs+ys]
		p, pb := lumaPSNR(got, r.Y), lumaPSNR(got, bl[i*bs:i*bs+ys])
		if i%8 == 0 {
			t.Logf("picture %d: %.1f dB from the composition, %.1f dB from the base layer", i, p, pb)
		}
		if p < 45 || p < pb+3 {
			t.Errorf("picture %d: %.1f dB from the composition, %.1f dB from the base layer", i, p, pb)
		}
	}
}

// --dv-fel keep and reencode keep Dolby Vision's layers apart: profile 7
// out, the source's RPUs unchanged, each frame with its enhancement layer
// behind type 63 headers. The output's layers compose to near the source's
// composition, and nearer with the enhancement layer rebuilt for the
// encoded base layer (keep) than with the source's re-encoded (reencode).
// Skips without ffmpeg, x265, NVDEC and NVENC.
func TestRunnerKeepsFELLayers(t *testing.T) {
	fs := makeFELSource(t, 10)
	for _, enc := range []Encoder{EncoderNVENC, EncoderSoftware} {
		t.Run(string(enc), func(t *testing.T) {
			if enc == EncoderNVENC && !ProbeNative(EncoderNVENC, CodecH265, 10, "") {
				t.Skip("no NVENC")
			}
			keepsFELLayers(t, fs, enc)
		})
	}
}

func keepsFELLayers(t *testing.T, fs felSource, enc Encoder) {
	ref := fs.composition(t, rawP010(t, fs.blES), rawP010(t, fs.elES))
	mean := map[FEL]float64{}
	for _, mode := range []FEL{FELKeep, FELReencode} {
		out := filepath.Join(t.TempDir(), "out.mkv")
		o := DefaultOptions()
		// A coarse base layer on smooth pictures (an error a half-size
		// enhancement layer can make up for) and a fine enhancement layer.
		o.Input, o.Output, o.Encoder, o.NativeGPU, o.Codec, o.DVFEL = fs.path, out, enc, enc == EncoderNVENC, CodecH265, mode
		o.Preset = "ultrafast"
		o.CRF, o.DVELCRF = 40, 8
		var lines []string
		if err := NewRunner(CurrentGOOS, o, func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }).Run(t.Context()); err != nil {
			if strings.Contains(err.Error(), "GPU") {
				t.Skipf("no GPU decoder: %v", err)
			}
			t.Fatalf("%s: %v\n%s", mode, err, strings.Join(lines, "\n"))
		}
		// Profile 7 with the enhancement layer, and the layers apart again.
		in, err := os.Open(out) //nolint:gosec // test
		if err != nil {
			t.Fatal(err)
		}
		r, err := mkv.NewReader(in)
		if err != nil {
			t.Fatal(err)
		}
		var cfg *dovi.Config
		for _, a := range r.Tracks[0].BlockAdditions {
			if c, err := dovi.ParseConfig(a.ExtraData); err == nil && a.Type == mkv.FourCC("dvcC") {
				cfg = &c
			}
		}
		if cfg == nil || cfg.Profile != 7 || !cfg.EL || cfg.Compatibility != 6 {
			t.Errorf("%s: configuration %+v, want profile 7 with an enhancement layer", mode, cfg)
		}
		type frame struct {
			at     time.Duration
			bl, el []byte
			rpu    []byte
		}
		var frames []frame
		for {
			p, err := r.Next()
			if err != nil {
				break
			}
			var f frame
			f.at = p.Time
			for b := p.Data; len(b) >= 4; {
				n := int(binary.BigEndian.Uint32(b))
				nal := b[4 : 4+n]
				switch nal[0] >> 1 & 0x3f {
				case dovi.NALEL:
					f.el = append(append(f.el, 0, 0, 0, 1), nal[2:]...)
				case dovi.NALRPU:
					f.rpu = nal
				default:
					f.bl = append(append(f.bl, 0, 0, 0, 1), nal...)
				}
				b = b[4+n:]
			}
			frames = append(frames, f)
		}
		_ = in.Close()
		if len(frames) != 24 {
			t.Fatalf("%s: %d frames", mode, len(frames))
		}
		dir := t.TempDir()
		blOut, elOut := filepath.Join(dir, "bl.hevc"), filepath.Join(dir, "el.hevc")
		var blb, elb []byte
		for _, f := range frames { // decoding order
			blb, elb = append(blb, f.bl...), append(elb, f.el...)
		}
		if err := os.WriteFile(blOut, blb, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(elOut, elb, 0o600); err != nil {
			t.Fatal(err)
		}
		sort.Slice(frames, func(i, j int) bool { return frames[i].at < frames[j].at })
		for i, f := range frames {
			if !bytes.Equal(f.rpu, fs.rpus[i%len(fs.rpus)]) {
				t.Fatalf("%s: frame %d does not carry its source RPU unchanged", mode, i)
			}
		}
		got := fs.composition(t, rawP010(t, blOut), rawP010(t, elOut))
		if len(got) != 24 {
			t.Fatalf("%s: %d pictures composed from the output", mode, len(got))
		}
		for i := range got {
			mean[mode] += lumaPSNR(got[i].Y, ref[i].Y) / 24
		}
		t.Logf("%s: %.2f dB from the source's composition, on average", mode, mean[mode])
		if mean[mode] < 35 {
			t.Errorf("%s: %.2f dB from the source's composition", mode, mean[mode])
		}
	}
	if mean[FELKeep] < mean[FELReencode]+1 {
		t.Errorf("the rebuilt enhancement layer (%.2f dB) is not clearly better than the re-encoded one (%.2f dB)", mean[FELKeep], mean[FELReencode])
	}
}
