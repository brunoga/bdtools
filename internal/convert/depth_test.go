package convert

import (
	"strings"
	"testing"
)

// planArgv is the encode step of a plan for the options mut makes.
func planArgv(t *testing.T, goos string, mut func(*Options)) string {
	t.Helper()
	p, err := BuildPlan(goos, opts(goos, mut))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range p.Steps {
		if s.Name == "encode" {
			return strings.Join(s.Argv, " ")
		}
	}
	t.Fatal("no encode step")
	return ""
}

// 10-bit output is HEVC or AV1, from any encoder but Media Foundation, and
// is not a remux's to change.
func TestBitDepthValidation(t *testing.T) {
	for _, c := range []struct {
		name string
		mut  func(*Options)
		ok   bool
	}{
		{"h265", func(o *Options) { o.Codec = CodecH265 }, true},
		{"av1", func(o *Options) { o.Codec = CodecAV1 }, true},
		{"h264", func(o *Options) { o.Codec = CodecH264 }, false},
		{"mediafoundation", func(o *Options) { o.Codec, o.Encoder = CodecH265, EncoderMediaFoundation }, false},
		{"remux", func(o *Options) { o.Remux, o.Output = true, "/out/a.m2ts" }, false},
		{"12-bit", func(o *Options) { o.Codec, o.BitDepth = CodecH265, 12 }, false},
	} {
		goos := "linux"
		if strings.Contains(c.name, "foundation") {
			goos = "windows"
		}
		o := opts(goos, func(o *Options) {
			o.BitDepth = 10
			c.mut(o)
		})
		if err := o.Validate(goos); (err == nil) != c.ok {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if err := opts("linux", func(o *Options) { o.BitDepth = 8 }).Validate("linux"); err != nil {
		t.Errorf("8-bit: %v", err)
	}
}

// Each encoder is asked for 10 bits in its own terms.
func TestBitDepthEncoderArguments(t *testing.T) {
	withTools(t, "x265", "ffmpeg", "SvtAv1EncApp")
	ten := func(enc Encoder, c Codec, mut ...func(*Options)) func(*Options) {
		return func(o *Options) {
			o.Encoder, o.Codec, o.BitDepth = enc, c, 10
			for _, m := range mut {
				m(o)
			}
		}
	}
	for _, c := range []struct {
		name, goos string
		mut        func(*Options)
		want       []string
		not        []string
	}{
		{"x265", "linux", ten(EncoderSoftware, CodecH265), []string{"x265 ", "--output-depth 10"}, nil},
		{"libx265, half", "linux", ten(EncoderSoftware, CodecH265, func(o *Options) { o.Layout = LayoutHalfSBS }),
			[]string{"libx265", "-pix_fmt yuv420p10le"}, nil},
		{"SvtAv1EncApp", "linux", ten(EncoderSoftware, CodecAV1), []string{"SvtAv1EncApp"}, []string{"pix_fmt"}},
		{"nvenc hevc", "linux", ten(EncoderNVENC, CodecH265), []string{"hevc_nvenc", "-pix_fmt p010le", "-profile:v main10"}, nil},
		{"nvenc av1", "linux", ten(EncoderNVENC, CodecAV1), []string{"av1_nvenc", "-pix_fmt p010le"}, []string{"main10"}},
		{"vaapi hevc", "linux", ten(EncoderVAAPI, CodecH265), []string{"format=p010,hwupload", "-profile:v main10"}, []string{"nv12"}},
		{"videotoolbox", "darwin", ten(EncoderVideoToolbox, CodecH265), []string{"hevc_videotoolbox", "-pix_fmt p010le", "-profile:v main10"}, nil},
		{"8-bit x265", "linux", func(o *Options) { o.Codec = CodecH265 }, []string{"x265 "}, []string{"depth", "10"}},
		{"8-bit nvenc", "linux", func(o *Options) { o.Encoder, o.Codec = EncoderNVENC, CodecH265 }, []string{"hevc_nvenc"}, []string{"p010", "main10"}},
	} {
		argv := planArgv(t, c.goos, c.mut)
		for _, w := range c.want {
			if !strings.Contains(argv, w) {
				t.Errorf("%s: no %q in %s", c.name, w, argv)
			}
		}
		for _, n := range c.not {
			if strings.Contains(argv, n) {
				t.Errorf("%s: %q in %s", c.name, n, argv)
			}
		}
	}
}

// A 10-bit file says so in its name.
func TestBitDepthInDetailTags(t *testing.T) {
	o := DefaultOptions()
	o.Codec, o.CRF, o.Encoder, o.BitDepth = CodecH265, 20, EncoderNVENC, 10
	if got := DetailTags(o, nil, 1080); got != "3D FSBS 1080p HEVC 10bit QP20 NVENC" {
		t.Errorf("tags %q", got)
	}
}

// Picking an encoder for 10-bit output asks the GPUs for 10 bits and passes
// over Media Foundation, which does not do them here.
func TestBitDepthAutoSelection(t *testing.T) {
	origNative, origLook := ProbeNative, LookPath
	t.Cleanup(func() { ProbeNative, LookPath = origNative, origLook })
	withTools(t) // nothing to fall back on through ffmpeg
	var asked []int
	ProbeNative = func(e Encoder, _ Codec, depth int, _ string) bool {
		asked = append(asked, depth)
		return e == EncoderMediaFoundation
	}
	if got := DefaultEncoder(t.Context(), "windows", CodecH265, 10, ""); got != EncoderSoftware {
		t.Errorf("10-bit on Windows chose %s", got)
	}
	if got := DefaultEncoder(t.Context(), "windows", CodecH265, 8, ""); got != EncoderMediaFoundation {
		t.Errorf("8-bit on Windows chose %s", got)
	}
	if len(asked) == 0 || asked[0] != 10 {
		t.Errorf("probed at depths %v", asked)
	}
}
