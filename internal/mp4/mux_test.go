package mp4

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/brunoga/bdtools/internal/mkv"
)

// An MV-HEVC stream muxes into an hvc1 track with the second layer's
// configuration and Apple's stereo boxes, and ffmpeg decodes both views
// from the file.
func TestMuxStereoHEVC(t *testing.T) {
	f, err := os.Open("../mkv/testdata/mvhevc.hevc")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	v, err := mkv.NewVideoSource(f, mkv.HEVC, 24000, 1001, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "stereo.mp4")
	of, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	err = Mux(of, []mkv.Source{v}, Options{FPSNum: 24000, FPSDen: 1001,
		Spatial: &Spatial{FOV: 90, Baseline: 63, Disparity: 0.02}})
	_ = of.Close()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ftyp", "mdat", "moov", "hvc1", "hvcC", "lhvC", "vexu", "eyes", "stri", "hero", "blin", "dadj", "prji", "hfov", "ctts", "stss", "co64"} {
		if !bytes.Contains(b, []byte(want)) {
			t.Errorf("no %s box", want)
		}
	}
	// in-band parameter sets are gone: no VPS NAL after the first sample's length
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg to decode with")
	}
	for view := 0; view < 2; view++ {
		cmd := exec.CommandContext(t.Context(), ff, "-v", "error", "-view_ids", string(rune('0'+view)), "-i", out, "-f", "rawvideo", "-pix_fmt", "yuv420p", "-") //nolint:gosec // test
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		dec, err := cmd.Output()
		if err != nil {
			t.Fatalf("ffmpeg view %d: %v %s", view, err, stderr.String())
		}
		if n := len(dec) / (640 * 368 * 3 / 2); n != 24 {
			t.Errorf("view %d: %d frames decoded, want 24", view, n)
		}
	}
	probe, err := exec.CommandContext(t.Context(), "ffprobe", "-v", "error", "-show_entries", "stream=codec_name,codec_tag_string,width,height,nb_frames", "-of", "csv=p=0", out).Output() //nolint:gosec // test
	if err == nil && !bytes.Contains(probe, []byte("hevc,hvc1,640,368")) {
		t.Errorf("ffprobe: %s", probe)
	}
}
