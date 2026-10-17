package convert

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brunoga/bdtools/internal/mkv"
)

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"4.5G": 4_500_000_000, "700M": 700_000_000, "3GiB": 3 << 30, "1200": 1200, "2 GB": 2_000_000_000} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "big", "-3G", "0"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) took it", bad)
		}
	}
}

// Access units: an HEVC picture's first slice segment starts one, its
// other slices and a second layer's do not; H.264's first slice too.
func TestAUSizes(t *testing.T) {
	nal := func(h ...byte) []byte { return append([]byte{0, 0, 1}, append(h, 0xaa, 0xbb)...) }
	var hevc []byte
	hevc = append(hevc, nal(0x40, 0x01)...)       // VPS
	hevc = append(hevc, nal(0x26, 0x01, 0x80)...) // IDR, first slice
	hevc = append(hevc, nal(0x26, 0x01, 0x00)...) // IDR, a later slice
	hevc = append(hevc, nal(0x02, 0x01, 0x80)...) // TRAIL, first
	hevc = append(hevc, nal(0x02, 0x09, 0x80)...) // layer 1, first: same picture
	if got := fmt.Sprint(auSizes(hevc, true)); got != "[16 16]" {
		t.Errorf("HEVC: %s", got)
	}
	var avc []byte
	avc = append(avc, nal(0x65, 0x88)...) // IDR, first_mb 0
	avc = append(avc, nal(0x65, 0x40)...) // a later slice
	avc = append(avc, nal(0x41, 0x9a)...) // P, first
	if got := fmt.Sprint(auSizes(avc, false)); got != "[14 7]" {
		t.Errorf("H.264: %s", got)
	}
}

// --video-size: half the size of a plain encode asked for, about half
// comes out.
func TestRunnerVideoSize(t *testing.T) {
	work := t.TempDir()
	src := filepath.Join(work, "src.mkv")
	if err := runCmd(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=s=320x240:r=24:d=40",
		"-c:v", "libx264", "-crf", "10", "-g", "48", src); err != nil {
		t.Skipf("could not build the source: %v", err)
	}
	encode := func(size int64) (int64, []string) {
		out := filepath.Join(t.TempDir(), "out.mkv")
		o := DefaultOptions()
		o.Input, o.Output, o.TempDir = src, out, t.TempDir()
		o.Encoder, o.Codec, o.CRF, o.Decoder, o.VideoSize = EncoderBuiltin, CodecH265, 22, DecoderCPU, size
		var lines []string
		if err := NewRunner(CurrentGOOS, o, func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }).Run(context.Background()); err != nil {
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
		var n int64
		for {
			p, err := rd.Next()
			if err != nil {
				break
			}
			n += int64(len(p.Data))
		}
		return n, lines
	}
	plain, _ := encode(0)
	got, lines := encode(plain / 2)
	if r := float64(got) / float64(plain/2); r < 0.75 || r > 1.25 {
		t.Errorf("asked for %d bytes, got %d (%.2f)\n%s", plain/2, got, r, strings.Join(lines, "\n"))
	}
}
