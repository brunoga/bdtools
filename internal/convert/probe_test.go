package convert

import (
	"context"
	"strings"
	"testing"
)

// Each hardware encoder needs its own filter chain. A single shared command
// with the codec name substituted would mis-probe VAAPI, which needs an
// explicit device and a hwupload — the shape that produced a false negative
// when this was first tried by hand.
func TestProbeArgvIsPerEncoder(t *testing.T) {
	vaapi := strings.Join(probeArgv(EncoderVAAPI, CodecH264, "/dev/dri/renderD128", false), " ")
	for _, want := range []string{"-vaapi_device /dev/dri/renderD128", "format=nv12,hwupload", "h264_vaapi"} {
		if !strings.Contains(vaapi, want) {
			t.Errorf("vaapi probe should contain %q, got: %s", want, vaapi)
		}
	}
	nvenc := strings.Join(probeArgv(EncoderNVENC, CodecH264, "", false), " ")
	if !strings.Contains(nvenc, "h264_nvenc") {
		t.Errorf("nvenc probe should name the encoder: %s", nvenc)
	}
	if strings.Contains(nvenc, "hwupload") {
		t.Errorf("nvenc takes software frames; hwupload would break it: %s", nvenc)
	}
	vt := strings.Join(probeArgv(EncoderVideoToolbox, CodecH264, "", false), " ")
	if !strings.Contains(vt, "h264_videotoolbox") {
		t.Errorf("videotoolbox probe should name the encoder: %s", vt)
	}
}

// Every probe must be exactly one frame — it runs on every auto-selection, so
// it has to be cheap.
func TestProbesEncodeOneFrame(t *testing.T) {
	for _, enc := range []Encoder{EncoderVAAPI, EncoderNVENC, EncoderVideoToolbox} {
		argv := probeArgv(enc, CodecH264, "/dev/dri/renderD128", false)
		joined := strings.Join(argv, " ")
		if !strings.Contains(joined, "-frames:v 1") {
			t.Errorf("%s: probe should encode one frame, got: %s", enc, joined)
		}
		if !strings.Contains(joined, "-f null -") {
			t.Errorf("%s: probe should discard its output, got: %s", enc, joined)
		}
	}
}

// Software encoding needs no trial: Detect already found the binary, and
// x264/x265 have no hardware to be missing.
func TestSoftwareEncoderNeedsNoProbe(t *testing.T) {
	for _, cod := range Codecs() {
		if probeArgv(EncoderSoftware, cod, "", false) != nil {
			t.Errorf("%s software encoding should have no probe command", cod)
		}
		if !ProbeEncoder(context.Background(), EncoderSoftware, cod, "", false) {
			t.Errorf("%s software encoding must always probe as available", cod)
		}
	}
}
