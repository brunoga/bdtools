package hevc

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// compare decodes path, on threads goroutines (0: the default), checking
// every picture against ffmpeg's decode as both go.
func compare(t *testing.T, path string, threads int) {
	t.Helper()
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	b, err := os.ReadFile(path) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	d := New()
	if threads > 0 {
		d.SetThreads(threads)
	}
	var cmd *exec.Cmd
	var ref io.Reader
	defer func() {
		if cmd != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	n, bad := 0, 0
	var want []byte
	out := func(p *Picture) error {
		if cmd == nil {
			pix := "yuv420p"
			if p.BitDepth > 8 {
				pix = "yuv420p10le"
			}
			args := []string{"-v", "error", "-flags", "+output_corrupt"}
			if noLoopFilter {
				args = append(args, "-skip_loop_filter", "all")
			}
			cmd = exec.CommandContext(context.Background(), ff, append(args, "-f", "hevc", "-i", path,
				"-fps_mode", "passthrough", "-f", "rawvideo", "-pix_fmt", pix, "-")...)
			r, err := cmd.StdoutPipe()
			if err != nil {
				return err
			}
			if err := cmd.Start(); err != nil {
				return err
			}
			ref = r
		}
		bps := 1
		if p.BitDepth > 8 {
			bps = 2
		}
		w, h := p.Width, p.Height
		size := (w*h + 2*(w/2)*(h/2)) * bps
		if len(want) != size {
			want = make([]byte, size)
		}
		if _, err := io.ReadFull(ref, want); err != nil {
			t.Errorf("picture %d: ffmpeg gave no more", n)
			return err
		}
		i := 0
		at := func(pl []uint16, stride, pw, ph int, name string) bool {
			for y := range ph {
				for x := range pw {
					var v uint16
					if bps == 1 {
						v = uint16(want[i])
					} else {
						v = uint16(want[i]) | uint16(want[i+1])<<8
					}
					i += bps
					if pl[y*stride+x] != v {
						t.Errorf("picture %d differs: first at %s (%d,%d): %d, ffmpeg %d", n, name, x, y, pl[y*stride+x], v)
						return false
					}
				}
			}
			return true
		}
		if !at(p.Y, p.StrideY, w, h, "Y") || !at(p.Cb, p.StrideC, w/2, h/2, "Cb") || !at(p.Cr, p.StrideC, w/2, h/2, "Cr") {
			bad++
		}
		n++
		if bad > 2 {
			return errors.New("too many differences")
		}
		return nil
	}
	if err := d.Decode(b, 0, out); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if err := d.Flush(out); err != nil {
		t.Fatal(err)
	}
	if ref != nil {
		if extra, _ := io.Copy(io.Discard, ref); extra > 0 {
			t.Errorf("ffmpeg gave more pictures (%d bytes)", extra)
		}
	}
	if d.Errors() > 0 {
		t.Errorf("%d decoding errors", d.Errors())
	}
	if n == 0 {
		t.Error("no pictures")
	}
	t.Logf("%d pictures", n)
}

// TestConformance decodes the JCT-VC conformance streams in
// $BDTOOLS_HEVC_SAMPLES ($BDTOOLS_HEVC_ONLY: a glob of names), each
// against ffmpeg's decode.
func TestConformance(t *testing.T) {
	dir := os.Getenv("BDTOOLS_HEVC_SAMPLES")
	if dir == "" {
		t.Skip("BDTOOLS_HEVC_SAMPLES not set")
	}
	noLoopFilter = os.Getenv("BDTOOLS_HEVC_NOLF") != ""
	glob := os.Getenv("BDTOOLS_HEVC_ONLY")
	if glob == "" {
		glob = "*"
	}
	paths, _ := filepath.Glob(filepath.Join(dir, glob+".bit"))
	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			if strings.Contains(p, "RExt") {
				t.Skip("the format range extensions")
			}
			compare(t, p, 0)
		})
	}
}
