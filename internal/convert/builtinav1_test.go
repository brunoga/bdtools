package convert

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The AV1 encoder here, end to end: a clip converted to Matroska and to
// MP4, read back by ffmpeg as AV1 with every frame.
func TestRunnerBuiltinAV1(t *testing.T) {
	ff, err := LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	work := t.TempDir()
	src := filepath.Join(work, "src.mkv")
	if err := runCmd(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=s=160x96:r=24:d=1", "-c:v", "libx264", "-g", "24", src); err != nil {
		t.Skipf("could not build the source: %v", err)
	}
	for _, ext := range []string{".mkv", ".mp4"} {
		t.Run(ext, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "out"+ext)
			o := DefaultOptions()
			o.Input, o.Output, o.TempDir = src, out, t.TempDir()
			o.Encoder, o.Codec, o.CRF, o.Decoder = EncoderBuiltin, CodecAV1, 30, DecoderCPU
			var lines []string
			if err := NewRunner(CurrentGOOS, o, func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }).Run(context.Background()); err != nil {
				t.Fatalf("%v\n%s", err, strings.Join(lines, "\n"))
			}
			info, err := exec.CommandContext(t.Context(), ff, "-hide_banner", "-i", out).CombinedOutput() //nolint:gosec // test
			if !strings.Contains(string(info), "Video: av1") {
				t.Fatalf("not AV1: %v\n%s", err, info)
			}
			frames, err := exec.CommandContext(t.Context(), ff, "-v", "error", "-i", out, "-vf", "scale=8:8,format=gray", "-f", "rawvideo", "-").Output() //nolint:gosec // test
			if err != nil {
				t.Fatal(err)
			}
			if n := len(frames) / 64; n != 24 {
				t.Errorf("%d frames, want 24", n)
			}
		})
	}
}
