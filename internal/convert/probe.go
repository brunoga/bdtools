package convert

import (
	"context"
	"os/exec"
)

// probeArgv is a one-frame trial encode: the cheapest honest answer to "can
// this machine actually use this encoder for this codec". Listing ffmpeg's
// encoders is not enough — a build routinely advertises h264_nvenc on a machine
// with no NVIDIA card, and h264_vaapi on one with no supported GPU — and the
// answer differs per codec: a GPU generation can carry an H.264 encoder and no
// HEVC one. VAAPI needs its own filter chain, which is why this is a switch
// rather than one command with a substituted codec name.
func probeArgv(enc Encoder, codec Codec, device string, viaFFmpeg bool) []string {
	const src = "testsrc2=s=320x240:d=1"
	name := codec.ffmpegEncoder(enc)
	if name == "" {
		if !viaFFmpeg {
			// Standalone software encoding always works if the binary is
			// there, which Detect already established.
			return nil
		}
		// Software encoding through ffmpeg does need a trial: having ffmpeg
		// is not having libx265, which a build may simply omit, and finding
		// that out at the encode step would waste the hours already spent
		// decoding.
		return []string{"ffmpeg", "-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", src, "-frames:v", "1",
			"-c:v", codec.ffmpegSoftwareEncoder(), "-f", "null", "-"}
	}
	switch enc {
	case EncoderVAAPI:
		return []string{"ffmpeg", "-hide_banner", "-loglevel", "error",
			"-vaapi_device", device,
			"-f", "lavfi", "-i", src, "-frames:v", "1",
			"-vf", "format=nv12,hwupload", "-c:v", name,
			"-f", "null", "-"}
	default:
		return []string{"ffmpeg", "-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", src, "-frames:v", "1",
			"-c:v", name, "-f", "null", "-"}
	}
}

// runProbe executes a trial. Indirected so the selection logic can be tested
// without a GPU, which no CI runner is guaranteed to have.
var runProbe = func(ctx context.Context, argv []string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // fixed argv from probeArgv
	return cmd.Run()
}

// ProbeEncoder reports whether enc actually encodes codec on this machine.
func ProbeEncoder(ctx context.Context, enc Encoder, codec Codec, device string, viaFFmpeg bool) bool {
	argv := probeArgv(enc, codec, device, viaFFmpeg)
	if argv == nil {
		return true // software
	}
	return runProbe(ctx, argv) == nil
}
