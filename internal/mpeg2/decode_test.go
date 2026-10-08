package mpeg2

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// accessUnits splits an elementary stream at its picture start codes, each
// picture with the headers before it.
func accessUnits(b []byte) [][]byte {
	var out [][]byte
	start, sawPicture := 0, false
	for i := 0; i+3 < len(b); i++ {
		if b[i] != 0 || b[i+1] != 0 || b[i+2] != 1 {
			continue
		}
		c := b[i+3]
		if (c == 0x00 || c == 0xb3 || c == 0xb8) && sawPicture {
			out = append(out, b[start:i])
			start, sawPicture = i, false
		}
		if c == 0x00 {
			sawPicture = true
		}
		i += 2
	}
	return append(out, b[start:])
}

// decodeFile decodes an elementary stream, giving each picture as packed
// planar 4:2:0.
func decodeFile(t *testing.T, path string) ([][]byte, *Decoder) {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	d := New()
	var frames [][]byte
	out := func(p *Picture) error {
		var f []byte
		for y := range p.Height {
			f = append(f, p.Y[y*p.StrideY:y*p.StrideY+p.Width]...)
		}
		for _, c := range [][]byte{p.Cb, p.Cr} {
			for y := range p.Height / 2 {
				f = append(f, c[y*p.StrideC:y*p.StrideC+p.Width/2]...)
			}
		}
		frames = append(frames, f)
		return nil
	}
	for i, au := range accessUnits(b) {
		if err := d.Decode(au, int64(i), out); err != nil {
			t.Fatalf("access unit %d: %v", i, err)
		}
	}
	if err := d.Flush(out); err != nil {
		t.Fatal(err)
	}
	return frames, d
}

func psnr(a, b []byte) float64 {
	var se float64
	for i := range a {
		d := float64(a[i]) - float64(b[i])
		se += d * d
	}
	if se == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(255*255*float64(len(a))/se)
}

// compare decodes path and checks every picture against ffmpeg's decode.
func compare(t *testing.T, path string, minPSNR float64) { compareCut(t, path, minPSNR, false) }

// compareCut is compare, leaving out the last picture when the sample is
// cut short (each decoder conceals what is missing its own way).
func compareCut(t *testing.T, path string, minPSNR float64, cut bool) {
	t.Helper()
	got, d := decodeFile(t, path)
	ref, err := exec.CommandContext(t.Context(), "ffmpeg", "-v", "error", "-i", path, "-fps_mode", "passthrough", "-pix_fmt", "yuv420p", "-f", "rawvideo", "-").Output() //nolint:gosec // test
	if err != nil {
		t.Fatalf("ffmpeg: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no pictures")
	}
	size := len(got[0])
	if len(ref) != len(got)*size {
		t.Fatalf("%d pictures of %d bytes, ffmpeg %d bytes (%d pictures)", len(got), size, len(ref), len(ref)/size)
	}
	worst := math.Inf(1)
	if cut {
		got = got[:len(got)-1]
	}
	for i, f := range got {
		p := psnr(f, ref[i*size:(i+1)*size])
		if p < minPSNR {
			t.Errorf("picture %d: %.1f dB from ffmpeg's", i, p)
			if t.Failed() && i > 3 {
				break
			}
		}
		worst = min(worst, p)
	}
	t.Logf("%d pictures, worst %.1f dB, %d slice errors", len(got), worst, d.Errors())
}

func TestDecodeMatchesFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	for _, c := range []struct {
		name string
		args []string
	}{
		{"progressive", []string{"-bf", "2", "-q:v", "4"}},
		{"interlaced", []string{"-bf", "2", "-q:v", "4", "-flags", "+ilme+ildct", "-top", "1"}},
		{"alternate scan, intra VLC, non-linear quantiser", []string{"-bf", "2", "-q:v", "6", "-flags", "+ilme+ildct",
			"-alternate_scan", "1", "-intra_vlc", "1", "-non_linear_quant", "1", "-qmax", "28", "-top", "0"}},
		{"high quality", []string{"-bf", "3", "-q:v", "1", "-intra_vlc", "1"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, fmt.Sprintf("%x.m2v", len(c.name)))
			args := append([]string{"-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=s=720x480:r=30000/1001:d=2", "-c:v", "mpeg2video"}, c.args...)
			if out, err := exec.CommandContext(t.Context(), "ffmpeg", append(args, path)...).CombinedOutput(); err != nil { //nolint:gosec // test
				t.Skipf("making the stream: %v %s", err, out)
			}
			compare(t, path, 45)
		})
	}
}

// The conformance streams in $BDTOOLS_MPEG2_SAMPLES (ffmpeg's FATE suite's
// mpeg2/ directory: sony-ct3.bs, tcela-6.bits).
func TestConformance(t *testing.T) {
	dir := os.Getenv("BDTOOLS_MPEG2_SAMPLES")
	if dir == "" {
		t.Skip("BDTOOLS_MPEG2_SAMPLES not set")
	}
	for _, name := range []string{"sony-ct3.bs", "tcela-6.bits", "mpeg2_field_encoding.ts", "matrixbench_mpeg2.lq1.mpg", "t.mpg"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name)
			if ext := filepath.Ext(name); ext == ".ts" || ext == ".mpg" {
				// The elementary stream out of its container.
				es := filepath.Join(t.TempDir(), "v.m2v")
				if out, err := exec.CommandContext(t.Context(), "ffmpeg", "-v", "error", "-i", path, "-map", "0:v:0", "-c", "copy", //nolint:gosec // test
					"-f", "mpeg2video", es).CombinedOutput(); err != nil {
					t.Fatalf("extracting the video: %v %s", err, out)
				}
				path = es
			}
			compareCut(t, path, 40, name == "mpeg2_field_encoding.ts") // a cut of a longer stream
		})
	}
}
