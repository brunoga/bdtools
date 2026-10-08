package convert

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/internal/mkv"
	"github.com/brunoga/bdtools/m2ts"
)

// The ffmpeg decoder gives each picture of a stream, in display order, as
// ffmpeg decodes the file itself, with the timestamp its access unit was
// given. Skips without ffmpeg.
func TestFFDecoder(t *testing.T) {
	bin, err := LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	for _, c := range []struct {
		name   string
		codec  gpu.VideoCodec
		args   []string
		pixfmt string
		depth  int
	}{
		{"hevc10", gpu.DecodeHEVC, []string{"-c:v", "libx265", "-pix_fmt", "yuv420p10le", "-x265-params", "log-level=error:bframes=3"}, "p010le", 10},
		{"h264", gpu.DecodeH264, []string{"-c:v", "libx264", "-bf", "3"}, "nv12", 8},
		{"mpeg2", gpu.DecodeMPEG2, []string{"-c:v", "mpeg2video", "-bf", "2"}, "nv12", 8},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := filepath.Join(dir, c.name+".mkv")
			args := append([]string{"-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=s=320x240:r=24:d=2"}, c.args...)
			if out, err := exec.CommandContext(t.Context(), bin, append(args, src)...).CombinedOutput(); err != nil { //nolint:gosec // test
				t.Skipf("making the stream: %v %s", err, out)
			}
			want, err := exec.CommandContext(t.Context(), bin, "-v", "error", "-i", src, "-pix_fmt", c.pixfmt, "-f", "rawvideo", "-").Output() //nolint:gosec // test
			if err != nil {
				t.Fatal(err)
			}
			// The access units with their timestamps, from the file.
			f, err := os.Open(src) //nolint:gosec // test
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			r, err := mkv.NewReader(f)
			if err != nil {
				t.Fatal(err)
			}
			tr := &r.Tracks[0]
			params := videoParams(tr)
			if c.codec == gpu.DecodeMPEG2 {
				params = tr.CodecPrivate
			}
			var got []byte
			var pts, fed []int64
			dec, err := openFFDecoder(bin, gpu.DecodeConfig{Codec: c.codec}, func(p *gpu.DecodedPicture) error {
				if p.Depth != c.depth {
					return fmt.Errorf("depth %d", p.Depth)
				}
				row := p.Width
				if p.Depth > 8 {
					row *= 2
				}
				for y := range p.Height {
					got = append(got, p.Y[y*p.Pitch:y*p.Pitch+row]...)
				}
				for y := range p.Height / 2 {
					got = append(got, p.UV[y*p.Pitch:y*p.Pitch+row]...)
				}
				pts = append(pts, p.PTS)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = dec.Close() }()
			for i := 0; ; i++ {
				pk, err := r.Next()
				if err != nil {
					break
				}
				au := pk.Data
				if c.codec != gpu.DecodeMPEG2 {
					au = annexB(au, videoNALSize(tr))
				}
				if i == 0 {
					au = append(append([]byte(nil), params...), au...)
				}
				ts := int64(pk.Time) * 9 / 100000 // 90 kHz
				fed = append(fed, ts)
				if err := dec.Decode(au, ts+1<<ptsTagShift); err != nil { // as a disc's second clip
					t.Fatal(err)
				}
			}
			if err := dec.Flush(); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%d bytes of pictures, ffmpeg's %d, or other pictures", len(got), len(want))
			}
			slices.Sort(fed)
			for i := range fed {
				fed[i] += 1 << ptsTagShift
			}
			if !slices.Equal(pts, fed) {
				t.Errorf("timestamps\n%v\nwant\n%v", pts, fed)
			}
		})
	}
}

func TestNormalRate(t *testing.T) {
	for _, c := range []struct{ num, den, wn, wd int }{
		{96000, 4004, 24000, 1001}, {48, 2, 24, 1}, {120000, 2002, 60000, 1001}, {7, 3, 7, 3}, {14, 6, 7, 3}, {0, 1, 0, 0},
	} {
		if n, d := normalRate(c.num, c.den); n != c.wn || d != c.wd {
			t.Errorf("normalRate(%d/%d) = %d/%d, want %d/%d", c.num, c.den, n, d, c.wn, c.wd)
		}
	}
}

// A real Blu-ray's VC-1 decodes through ffmpeg as ffmpeg decodes the file:
// the sample in $BDTOOLS_VC1_SAMPLE (ffmpeg's samples site has one,
// A-codecs/TrueHD/vc1-with-truehd.m2ts), its video on PID 0x1011.
func TestFFDecoderVC1(t *testing.T) {
	path := os.Getenv("BDTOOLS_VC1_SAMPLE")
	if path == "" {
		t.Skip("BDTOOLS_VC1_SAMPLE not set")
	}
	bin, err := LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	want, err := exec.CommandContext(t.Context(), bin, "-v", "error", "-i", path, "-map", "0:v:0", "-pix_fmt", "nv12", "-f", "rawvideo", "-").Output() //nolint:gosec // test
	if len(want) == 0 {
		t.Fatalf("ffmpeg decoding the sample: %v", err)
	}
	for _, c := range []struct {
		name string
		open decoderOpener
	}{
		{"ffmpeg", func(cfg gpu.DecodeConfig, picture func(*gpu.DecodedPicture) error) (gpu.Decoder, error) {
			return openFFDecoder(bin, cfg, picture)
		}},
		{"the decoder here", openGoVC1},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, err := os.Open(path) //nolint:gosec // test
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			r := m2ts.NewReader(f)
			r.Select(0x1011)
			var got []byte
			dec, err := c.open(gpu.DecodeConfig{Codec: gpu.DecodeVC1}, func(p *gpu.DecodedPicture) error {
				for y := range p.Height {
					got = append(got, p.Y[y*p.Pitch:y*p.Pitch+p.Width]...)
				}
				for y := range p.Height / 2 {
					got = append(got, p.UV[y*p.Pitch:y*p.Pitch+p.Width]...)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = dec.Close() }()
			for {
				p, err := r.Next()
				if err != nil {
					break
				}
				if err := dec.Decode(p.Payload, p.PTS); err != nil {
					t.Fatal(err)
				}
			}
			if err := dec.Flush(); err != nil {
				t.Logf("flush: %v", err) // the sample's last picture is cut short
			}
			// The sample's last access unit is cut short: decoders may
			// conceal it differently. It is a B picture, the one before the
			// last in display order.
			const size = 1920 * 1080 * 3 / 2
			n := min(len(got), len(want)) - 2*size
			if n <= 0 || !bytes.Equal(got[:n], want[:n]) {
				for i := 0; i+size <= n; i += size {
					if !bytes.Equal(got[i:i+size], want[i:i+size]) {
						t.Errorf("picture %d differs", i/size)
						break
					}
				}
				t.Errorf("%d bytes of pictures, ffmpeg's %d, or other pictures", len(got), len(want))
			}
		})
	}
}
