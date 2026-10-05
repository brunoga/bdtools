package convert

import (
	"strings"
	"testing"
)

func opts(goos string, mut func(*Options)) Options {
	o := DefaultOptions()
	o.Input = "/media/in/Life of Pi (2012).iso"
	o.Output = "/media/out/Life of Pi (2012).mkv"
	o.TempDir = "/tmp/work"
	o.Encoder = EncoderSoftware
	if mut != nil {
		mut(&o)
	}
	return o
}

func TestBuildPlanHasTheFourStagesInOrder(t *testing.T) {
	p, err := BuildPlan("linux", opts("linux", nil))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"demux", "decode", "encode", "mux"}
	if len(p.Steps) != len(want) {
		t.Fatalf("got %d steps, want %d", len(p.Steps), len(want))
	}
	for i, n := range want {
		if p.Steps[i].Name != n {
			t.Errorf("step %d = %q, want %q", i, p.Steps[i].Name, n)
		}
	}
}

// The decode must stream into the encoder. Writing raw frames to disk for a
// feature film is hundreds of gigabytes.
func TestDecodeStreamsIntoTheEncoder(t *testing.T) {
	p, _ := BuildPlan("linux", opts("linux", nil))
	var decode Step
	for _, s := range p.Steps {
		if s.Name == "decode" {
			decode = s
		}
	}
	if decode.PipeTo != "encode" {
		t.Errorf("decode should pipe into encode, got %q", decode.PipeTo)
	}
	if !decode.Builtin {
		t.Error("the decode runs in process")
	}
	if !contains(decode.Argv, "sbs") {
		t.Errorf("decode must ask for the stacked side-by-side output, got %v", decode.Argv)
	}
	if !contains(decode.Argv, p.BaseView) || !contains(decode.Argv, p.DependentView) {
		t.Errorf("decode must read the two demuxed views, got %v", decode.Argv)
	}
	if contains(decode.Argv, "-swap") {
		t.Errorf("no swap was asked for, got %v", decode.Argv)
	}
}

// Nothing in the plan may route the two views through a file: the decoder
// reads the demuxed pair, so a multi-gigabyte combined stream is never made.
func TestNoThirdCopyOfTheStreams(t *testing.T) {
	p, _ := BuildPlan("linux", opts("linux", nil))
	if p.BaseView == "" || p.DependentView == "" {
		t.Fatal("the plan must say where the demux puts the two views")
	}
	for _, s := range p.Steps {
		for _, a := range s.Argv {
			if a == "combined.264" || strings.HasSuffix(a, "interleaved.264") {
				t.Errorf("step %q writes a combined stream to disk: %v", s.Name, s.Argv)
			}
		}
	}
}

// Every encoder variant has to read the same Y4M stream on stdin, or the pipe
// from the decoder cannot work.
func TestEveryEncoderReadsY4MFromStdin(t *testing.T) {
	for _, enc := range []Encoder{EncoderSoftware, EncoderVAAPI, EncoderVideoToolbox, EncoderNVENC} {
		goos := "linux"
		if enc == EncoderVideoToolbox {
			goos = "darwin"
		}
		p, err := BuildPlan(goos, opts(goos, func(o *Options) { o.Encoder = enc }))
		if err != nil {
			t.Fatalf("%s: %v", enc, err)
		}
		var step Step
		for _, s := range p.Steps {
			if s.Name == "encode" {
				step = s
			}
		}
		joined := strings.Join(step.Argv, " ")
		if !strings.Contains(joined, "y4m") && !strings.Contains(joined, "yuv4mpegpipe") {
			t.Errorf("%s: encoder must read y4m, got %v", enc, step.Argv)
		}
		if !contains(step.Argv, "-") {
			t.Errorf("%s: encoder must read stdin, got %v", enc, step.Argv)
		}
	}
}

func TestEncoderSelectionPicksTheRightBinary(t *testing.T) {
	cases := []struct {
		goos string
		enc  Encoder
		prog string
		tag  string
	}{
		{"linux", EncoderSoftware, "x264", "--crf"},
		{"linux", EncoderVAAPI, "ffmpeg", "h264_vaapi"},
		{"linux", EncoderNVENC, "ffmpeg", "h264_nvenc"},
		{"darwin", EncoderVideoToolbox, "ffmpeg", "h264_videotoolbox"},
	}
	for _, c := range cases {
		p, err := BuildPlan(c.goos, opts(c.goos, func(o *Options) { o.Encoder = c.enc }))
		if err != nil {
			t.Fatalf("%s/%s: %v", c.goos, c.enc, err)
		}
		for _, s := range p.Steps {
			if s.Name != "encode" {
				continue
			}
			if s.Argv[0] != c.prog {
				t.Errorf("%s: program = %q, want %q", c.enc, s.Argv[0], c.prog)
			}
			if !strings.Contains(strings.Join(s.Argv, " "), c.tag) {
				t.Errorf("%s: argv should mention %q, got %v", c.enc, c.tag, s.Argv)
			}
		}
	}
}

func TestValidateRejectsIncoherentOptions(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Options)
		want string
	}{
		{"no input", func(o *Options) { o.Input = "" }, "no input"},
		{"no output", func(o *Options) { o.Output = "" }, "no output"},
		{"same file", func(o *Options) { o.Output = o.Input }, "same file"},
		{"not an mkv", func(o *Options) { o.Output = "/out/x.mp4" }, "must be a .mkv"},
		{"unknown layout", func(o *Options) { o.Layout = "sbs3d" }, "unknown layout"},
		{"crf out of range", func(o *Options) { o.CRF = 99 }, "out of range"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := BuildPlan("linux", opts("linux", c.mut))
			if err == nil {
				t.Fatalf("expected an error mentioning %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q should mention %q", err, c.want)
			}
		})
	}
}

// A GPU encoder the platform cannot have must be refused before the run, not
// hours in at the first ffmpeg call.
func TestValidateRejectsAnImpossibleEncoder(t *testing.T) {
	_, err := BuildPlan("darwin", opts("darwin", func(o *Options) { o.Encoder = EncoderVAAPI }))
	if err == nil {
		t.Fatal("VAAPI on macOS should be refused")
	}
	if !strings.Contains(err.Error(), "not available on darwin") {
		t.Errorf("error should name the platform, got %q", err)
	}
	if !strings.Contains(err.Error(), "videotoolbox") {
		t.Errorf("error should list what is available, got %q", err)
	}
}

// The output path a user gives with spaces must reach the muxer as one
// argument — the plan is argv, never a shell string.
func TestPathsWithSpacesStayOneArgument(t *testing.T) {
	p, _ := BuildPlan("linux", opts("linux", nil))
	for _, s := range p.Steps {
		if s.Name != "mux" {
			continue
		}
		if !contains(s.Argv, "/media/out/Life of Pi (2012).mkv") {
			t.Errorf("output path should be a single argv element, got %v", s.Argv)
		}
	}
}

func TestIntermediatesAreListedForCleanup(t *testing.T) {
	p, _ := BuildPlan("linux", opts("linux", nil))
	if len(p.Intermediates) == 0 {
		t.Fatal("a plan must say what it will leave behind")
	}
	for _, f := range p.Intermediates {
		if !strings.HasPrefix(f, "/tmp/work") {
			t.Errorf("intermediate %q should live in the temp dir", f)
		}
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// The decoder stacks the views either way round, so exchanging the eyes is
// its job and never a filter: the standalone encoder stays in use.
func TestSwapIsDoneByTheDecoder(t *testing.T) {
	p, err := BuildPlan("linux", opts("linux", func(o *Options) {
		o.Encoder = EncoderSoftware
		o.SwapLR = true
	}))
	if err != nil {
		t.Fatal(err)
	}
	var decode Step
	for _, s := range p.Steps {
		if s.Name == "decode" {
			decode = s
		}
	}
	if !contains(decode.Argv, "-swap") {
		t.Errorf("the decode should exchange the views, got %v", decode.Argv)
	}
	joined := encodeArgs(p)
	if strings.Contains(joined, "hstack") || strings.Contains(joined, "crop=") {
		t.Errorf("the swap must not be an encoder filter, got: %s", joined)
	}
	if !strings.HasPrefix(joined, "x264 ") {
		t.Errorf("software encoding with a swap should still use x264 itself, got: %s", joined)
	}
}

// x264 and x265 have no filters, so software encoding that needs one is
// driven through ffmpeg's build of the same library rather than being refused.
// Both filters have to work, and both have to reach the right encoder.
func TestSoftwareEncodingWithFiltersGoesThroughFFmpeg(t *testing.T) {
	for _, c := range []struct {
		name  string
		mut   func(*Options)
		codec Codec
		want  string
	}{
		{"half-SBS, h264", func(o *Options) { o.Layout = LayoutHalfSBS }, CodecH264, "libx264"},
		{"half-SBS, h265", func(o *Options) { o.Layout = LayoutHalfSBS }, CodecH265, "libx265"},
	} {
		p, err := BuildPlan("linux", opts("linux", func(o *Options) {
			o.Encoder, o.Codec = EncoderSoftware, c.codec
			c.mut(o)
		}))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		var joined string
		for _, st := range p.Steps {
			if st.Name == "encode" {
				joined = strings.Join(st.Argv, " ")
			}
		}
		if !strings.HasPrefix(joined, "ffmpeg ") {
			t.Errorf("%s: encode runs %q, want ffmpeg", c.name, joined)
		}
		if !strings.Contains(joined, c.want) {
			t.Errorf("%s: want %s in %q", c.name, c.want, joined)
		}
		if !strings.Contains(joined, "-vf ") {
			t.Errorf("%s: no filter chain in %q", c.name, joined)
		}
	}
}

// The quality settings must still reach the encoder: ffmpeg passes -crf and
// -preset straight to the library, so they mean what they meant before.
func TestFilteredSoftwareEncodeKeepsQualitySettings(t *testing.T) {
	p, err := BuildPlan("linux", opts("linux", func(o *Options) {
		o.Encoder, o.Layout, o.CRF, o.Preset = EncoderSoftware, LayoutHalfSBS, 21, "veryslow"
	}))
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	var joined string
	for _, st := range p.Steps {
		if st.Name == "encode" {
			joined = strings.Join(st.Argv, " ")
		}
	}
	for _, want := range []string{"-crf 21", "-preset veryslow"} {
		if !strings.Contains(joined, want) {
			t.Errorf("want %q in %q", want, joined)
		}
	}
}

// Unfiltered software encoding still uses the standalone binary: fewer moving
// parts, and it works on a machine that has x264 but no ffmpeg.
func TestUnfilteredSoftwareEncodingStillUsesTheStandaloneBinary(t *testing.T) {
	for _, c := range []struct{ codec, want string }{{"h264", "x264"}, {"h265", "x265"}} {
		p, err := BuildPlan("linux", opts("linux", func(o *Options) {
			o.Encoder, o.Codec = EncoderSoftware, Codec(c.codec)
		}))
		if err != nil {
			t.Fatalf("%s: %v", c.codec, err)
		}
		for _, st := range p.Steps {
			if st.Name != "encode" {
				continue
			}
			if st.Argv[0] != c.want {
				t.Errorf("%s runs %q, want the standalone %s", c.codec, st.Argv[0], c.want)
			}
		}
	}
}

// Which tool has to be present follows the same rule, so --check looks for
// ffmpeg rather than reporting x265 missing on a machine that never needs it.
func TestRequiredToolFollowsTheFilterRequirement(t *testing.T) {
	names := func(ts []Tool) string {
		var s []string
		for _, t := range ts {
			s = append(s, t.Name)
		}
		return strings.Join(s, " ")
	}
	plain := names(Required("linux", EncoderSoftware, CodecH265, false))
	if !strings.Contains(plain, "x265") {
		t.Errorf("unfiltered h265 software needs x265, got: %s", plain)
	}
	filtered := names(Required("linux", EncoderSoftware, CodecH265, true))
	if !strings.Contains(filtered, "ffmpeg") || strings.Contains(filtered, "x265") {
		t.Errorf("filtered h265 software needs ffmpeg, got: %s", filtered)
	}
}

// And the options know it themselves, which is what the command and the
// toolchain check both read.
func TestNeedsFiltersAndEncodesViaFFmpeg(t *testing.T) {
	plain := opts("linux", func(o *Options) { o.Encoder = EncoderSoftware })
	if plain.NeedsFilters() || plain.EncodesViaFFmpeg() {
		t.Error("plain full-SBS software encoding needs no filters and no ffmpeg")
	}
	swapped := opts("linux", func(o *Options) { o.Encoder, o.SwapLR = EncoderSoftware, true })
	if swapped.NeedsFilters() || swapped.EncodesViaFFmpeg() {
		t.Error("an eye swap is the decoder's job, not a filter")
	}
	half := opts("linux", func(o *Options) { o.Encoder, o.Layout = EncoderSoftware, LayoutHalfSBS })
	if !half.NeedsFilters() || !half.EncodesViaFFmpeg() {
		t.Errorf("%+v should need filters and ffmpeg", half)
	}
	// A hardware encoder is ffmpeg regardless.
	hw := opts("linux", func(o *Options) { o.Encoder = EncoderNVENC })
	if !hw.EncodesViaFFmpeg() {
		t.Error("a hardware encoder always runs through ffmpeg")
	}
}

// Order matters: VAAPI's upload comes last because the filters before it work
// on software frames.
func TestFilterOrderIsSqueezeThenUpload(t *testing.T) {
	p, err := BuildPlan("linux", opts("linux", func(o *Options) {
		o.Encoder = EncoderVAAPI
		o.SwapLR = true
		o.Layout = LayoutHalfSBS
	}))
	if err != nil {
		t.Fatal(err)
	}
	joined := encodeArgs(p)
	iHalf := strings.Index(joined, "scale=iw/2:ih")
	iUp := strings.Index(joined, "hwupload")
	if iHalf < 0 || iUp < 0 {
		t.Fatalf("both filters should be present, got: %s", joined)
	}
	if iHalf > iUp {
		t.Errorf("filters out of order (squeeze %d, upload %d): %s", iHalf, iUp, joined)
	}
}

// Full-SBS with a hardware encoder needs no resample at all: the decoder's own
// output is already the layout we want.
func TestFullSBSAddsNoScaleFilter(t *testing.T) {
	p, err := BuildPlan("linux", opts("linux", func(o *Options) { o.Encoder = EncoderNVENC }))
	if err != nil {
		t.Fatal(err)
	}
	if joined := encodeArgs(p); strings.Contains(joined, "scale=") {
		t.Errorf("full-SBS must not rescale, got: %s", joined)
	}
}

func encodeArgs(p *Plan) string {
	for _, s := range p.Steps {
		if s.Name == "encode" {
			return strings.Join(s.Argv, " ")
		}
	}
	return ""
}
