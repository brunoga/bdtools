//go:build linux && (amd64 || arm64)

package gpu

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/brunoga/bdtools/internal/mpeg2"
)

// VAAPI decodes MPEG-2 (frame and field pictures, every prediction mode)
// as the decoder in Go does, frame by frame before deinterlacing: within
// the inverse transform's tolerance (it is not bit-exact by definition).
// The streams are made here, and the conformance streams in
// $BDTOOLS_MPEG2_SAMPLES (see internal/mpeg2) are added.
func TestVAAPIMPEG2(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	open := func(t *testing.T) *vaDecoder {
		dec, err := openVAAPIDecoder(DecodeConfig{Codec: DecodeMPEG2}, func(*DecodedPicture) error { return nil })
		if err != nil {
			t.Skip(err)
		}
		t.Cleanup(func() { _ = dec.Close() })
		return dec.(*vaDecoder) //nolint:forcetypeassert // ours
	}
	dir := t.TempDir()
	type stream struct{ name, path string }
	var streams []stream
	for _, c := range []struct {
		name string
		args []string
	}{
		{"interlaced frames", []string{"-bf", "2", "-q:v", "4", "-flags", "+ilme+ildct", "-top", "1"}},
		{"alternate scan, intra VLC, non-linear quantiser", []string{"-bf", "2", "-q:v", "6", "-flags", "+ilme+ildct",
			"-alternate_scan", "1", "-intra_vlc", "1", "-non_linear_quant", "1", "-qmax", "28", "-top", "0"}},
		{"high quality", []string{"-bf", "3", "-q:v", "1", "-intra_vlc", "1"}},
	} {
		path := filepath.Join(dir, c.name+".m2v")
		args := append([]string{"-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=s=720x480:r=30000/1001:d=2", "-c:v", "mpeg2video"}, c.args...)
		if out, err := exec.CommandContext(t.Context(), "ffmpeg", append(args, path)...).CombinedOutput(); err != nil { //nolint:gosec // test
			t.Skipf("making the stream: %v %s", err, out)
		}
		streams = append(streams, stream{c.name, path})
	}
	if s := os.Getenv("BDTOOLS_MPEG2_SAMPLES"); s != "" {
		for _, name := range []string{"sony-ct3.bs", "tcela-6.bits", "mpeg2_field_encoding.ts", "matrixbench_mpeg2.lq1.mpg"} {
			path := filepath.Join(s, name)
			if ext := filepath.Ext(name); ext == ".ts" || ext == ".mpg" {
				es := filepath.Join(dir, name+".m2v")
				if out, err := exec.CommandContext(t.Context(), "ffmpeg", "-v", "error", "-i", path, "-map", "0:v:0", "-c", "copy", //nolint:gosec // test
					"-f", "mpeg2video", es).CombinedOutput(); err != nil {
					t.Fatalf("extracting the video: %v %s", err, out)
				}
				path = es
			}
			streams = append(streams, stream{name, path})
		}
	}
	for _, s := range streams {
		t.Run(s.name, func(t *testing.T) {
			b, err := os.ReadFile(s.path)
			if err != nil {
				t.Fatal(err)
			}
			units := mpeg2Units(b)
			decode := func(d *mpeg2.Decoder) [][]byte {
				var frames [][]byte
				out := func(p *mpeg2.Picture) error {
					var f []byte
					for y := range p.Height {
						f = append(f, p.Y[y*p.StrideY:y*p.StrideY+p.Width]...)
					}
					frames = append(frames, f)
					return nil
				}
				for i, u := range units {
					if err := d.Decode(u, int64(i), out); err != nil {
						t.Fatal(err)
					}
				}
				if err := d.Flush(out); err != nil {
					t.Fatal(err)
				}
				return frames
			}
			want := decode(mpeg2.New())
			hw := mpeg2.New()
			hw.SetAccel(vaMPEG2{open(t)})
			got := decode(hw)
			if len(got) != len(want) {
				t.Fatalf("%d frames, the decoder in Go %d", len(got), len(want))
			}
			if s.name == "mpeg2_field_encoding.ts" {
				// A cut of a longer stream: each decoder conceals what is
				// missing of the last picture its own way.
				got = got[:len(got)-1]
			}
			worst := 99.0
			for i := range got {
				p := lumaPSNR(got[i], want[i])
				worst = min(worst, p)
				if p < 40 {
					t.Errorf("frame %d: %.1f dB from the decoder in Go", i, p)
				}
			}
			t.Logf("%d frames, worst %.1f dB", len(got), worst)
		})
	}
}
