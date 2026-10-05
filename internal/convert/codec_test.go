package convert

import (
	"context"
	"strings"
	"testing"
)

// encodeArgv returns the encode step of a plan built for codec cod.
func encodeArgv(t *testing.T, goos string, enc Encoder, cod Codec) []string {
	t.Helper()
	p, err := BuildPlan(goos, opts(goos, func(o *Options) {
		o.Encoder, o.Codec = enc, cod
	}))
	if err != nil {
		t.Fatalf("%s/%s/%s: %v", goos, enc, cod, err)
	}
	for _, s := range p.Steps {
		if s.Name == "encode" {
			return s.Argv
		}
	}
	t.Fatalf("%s/%s/%s: plan has no encode step", goos, enc, cod)
	return nil
}

// The codec is a separate axis from the encoder: each backend has to name the
// matching encoder, and ffmpeg spells the HEVC ones "hevc_*" rather than
// "h265_*", which is the detail a substituted codec name gets wrong.
func TestEveryEncoderCanProduceEitherCodec(t *testing.T) {
	cases := []struct {
		goos string
		enc  Encoder
		h264 string
		h265 string
	}{
		{"linux", EncoderSoftware, "x264", "x265"},
		{"linux", EncoderVAAPI, "h264_vaapi", "hevc_vaapi"},
		{"linux", EncoderNVENC, "h264_nvenc", "hevc_nvenc"},
		{"darwin", EncoderVideoToolbox, "h264_videotoolbox", "hevc_videotoolbox"},
	}
	for _, c := range cases {
		h264 := strings.Join(encodeArgv(t, c.goos, c.enc, CodecH264), " ")
		if !strings.Contains(h264, c.h264) {
			t.Errorf("%s h264: want %q in %s", c.enc, c.h264, h264)
		}
		if strings.Contains(h264, "hevc") || strings.Contains(h264, "x265") {
			t.Errorf("%s h264: leaked an HEVC encoder: %s", c.enc, h264)
		}
		h265 := strings.Join(encodeArgv(t, c.goos, c.enc, CodecH265), " ")
		if !strings.Contains(h265, c.h265) {
			t.Errorf("%s h265: want %q in %s", c.enc, c.h265, h265)
		}
	}
}

// Both software encoders have to read the Y4M stream from stdin, or the pipe
// from the decoder cannot work. x265 differs from x264 here: it takes the
// input through --input and cannot infer the format from "-", so --y4m has to
// be passed explicitly.
func TestX265ReadsY4MFromStdin(t *testing.T) {
	argv := encodeArgv(t, "linux", EncoderSoftware, CodecH265)
	joined := strings.Join(argv, " ")
	for _, want := range []string{"--y4m", "--input -"} {
		if !strings.Contains(joined, want) {
			t.Errorf("x265 must be given %q, got: %s", want, joined)
		}
	}
	if strings.Contains(joined, "--demuxer") {
		t.Errorf("--demuxer is an x264 flag; x265 rejects it: %s", joined)
	}
}

// The quality target and preset reach whichever software encoder is used; a
// codec switch must not quietly drop them.
func TestSoftwareCodecsCarryQualitySettings(t *testing.T) {
	for _, cod := range Codecs() {
		p, err := BuildPlan("linux", opts("linux", func(o *Options) {
			o.Codec, o.CRF, o.Preset = cod, 21, "veryslow"
		}))
		if err != nil {
			t.Fatalf("%s: %v", cod, err)
		}
		var joined string
		for _, s := range p.Steps {
			if s.Name == "encode" {
				joined = strings.Join(s.Argv, " ")
			}
		}
		for _, want := range []string{"--crf 21", "--preset veryslow"} {
			if !strings.Contains(joined, want) {
				t.Errorf("%s: want %q in %s", cod, want, joined)
			}
		}
	}
}

// mkvmerge identifies a raw elementary stream by its extension, so an HEVC
// stream written to a .264 file is rejected at the mux — hours after the
// encode started.
func TestRawStreamExtensionFollowsTheCodec(t *testing.T) {
	for _, c := range []struct {
		cod Codec
		ext string
	}{{CodecH264, ".264"}, {CodecH265, ".265"}} {
		p, err := BuildPlan("linux", opts("linux", func(o *Options) { o.Codec = c.cod }))
		if err != nil {
			t.Fatalf("%s: %v", c.cod, err)
		}
		var encodeOut, muxIn string
		for _, s := range p.Steps {
			switch s.Name {
			case "encode":
				encodeOut = encodeOutput(s.Argv)
			case "mux":
				muxIn = s.Argv[len(s.Argv)-1]
			}
		}
		if !strings.HasSuffix(encodeOut, c.ext) {
			t.Errorf("%s: encoder writes %q, want a %s file", c.cod, encodeOut, c.ext)
		}
		if muxIn != encodeOut {
			t.Errorf("%s: mux reads %q but the encoder wrote %q", c.cod, muxIn, encodeOut)
		}
	}
}

// The software encoder needs the binary matching the codec. Reporting x264 as
// present when the run will invoke x265 is the whole point of --check failing.
func TestRequiredSoftwareToolFollowsTheCodec(t *testing.T) {
	names := func(ts []Tool) string {
		var s []string
		for _, t := range ts {
			s = append(s, t.Name)
		}
		return strings.Join(s, " ")
	}
	if got := names(Required("linux", EncoderSoftware, CodecH264, false)); !strings.Contains(got, "x264") || strings.Contains(got, "x265") {
		t.Errorf("h264 software needs x264, got: %s", got)
	}
	if got := names(Required("linux", EncoderSoftware, CodecH265, false)); !strings.Contains(got, "x265") || strings.Contains(got, "x264") {
		t.Errorf("h265 software needs x265, got: %s", got)
	}
	// A hardware encoder is ffmpeg either way — the codec changes the encoder
	// name it is given, not the program.
	for _, cod := range Codecs() {
		if got := names(Required("linux", EncoderVAAPI, cod, true)); !strings.Contains(got, "ffmpeg") {
			t.Errorf("%s vaapi needs ffmpeg, got: %s", cod, got)
		}
	}
}

// Auto-selection has to probe for the codec being produced: a GPU generation
// can carry an H.264 encoder and no HEVC one, so accepting "nvenc works here"
// would pick an encoder that fails at the encode step, hours in.
func TestAutoSelectionProbesForTheRequestedCodec(t *testing.T) {
	origLook, origProbe := LookPath, runProbe
	t.Cleanup(func() { LookPath, runProbe = origLook, origProbe })
	LookPath = func(string) (string, error) { return "/usr/bin/stub", nil }

	var probed []string
	runProbe = func(_ context.Context, argv []string) error {
		joined := strings.Join(argv, " ")
		probed = append(probed, joined)
		// A card that encodes H.264 but not HEVC.
		if strings.Contains(joined, "hevc_") {
			return context.DeadlineExceeded
		}
		return nil
	}

	if got := DefaultEncoder(context.Background(), "linux", CodecH264, "/dev/dri/renderD128"); got != EncoderNVENC {
		t.Errorf("h264 auto = %q, want nvenc", got)
	}
	if got := DefaultEncoder(context.Background(), "linux", CodecH265, "/dev/dri/renderD128"); got != EncoderSoftware {
		t.Errorf("h265 auto = %q, want software — no hardware HEVC encoder here", got)
	}
	var sawHEVC bool
	for _, p := range probed {
		if strings.Contains(p, "hevc_") {
			sawHEVC = true
		}
	}
	if !sawHEVC {
		t.Errorf("the h265 selection never probed an HEVC encoder: %v", probed)
	}
}

func TestUnknownCodecIsRejected(t *testing.T) {
	_, err := BuildPlan("linux", opts("linux", func(o *Options) { o.Codec = "av1" }))
	if err == nil {
		t.Fatal("an unknown codec must not build a plan")
	}
	if !strings.Contains(err.Error(), "av1") {
		t.Errorf("error should name the codec, got: %v", err)
	}
}

// "x264" was this setting's name when H.264 was the only output. It has to go
// on selecting software encoding rather than becoming an unknown encoder.
func TestParseEncoderAcceptsTheOldSoftwareNames(t *testing.T) {
	for _, in := range []string{"software", "x264", "x265"} {
		if got := ParseEncoder(in); got != EncoderSoftware {
			t.Errorf("ParseEncoder(%q) = %q, want %q", in, got, EncoderSoftware)
		}
	}
	for _, in := range []string{"auto", "vaapi", "nvenc", "videotoolbox"} {
		if got := ParseEncoder(in); got != Encoder(in) {
			t.Errorf("ParseEncoder(%q) = %q, want it unchanged", in, got)
		}
	}
}

// encodeOutput finds the file an encode step writes. x264 takes its output
// through --output and then names stdin as the last argument, so the last
// argument is not the answer for every encoder.
func encodeOutput(argv []string) string {
	for i, a := range argv {
		if a == "--output" && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return argv[len(argv)-1]
}
