package vc1

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// accessUnits splits an elementary stream at its frame start codes, each
// frame with the headers before it.
func accessUnits(b []byte) [][]byte {
	var out [][]byte
	start, sawFrame := 0, false
	for i := 0; i+3 < len(b); i++ {
		if b[i] != 0 || b[i+1] != 0 || b[i+2] != 1 {
			continue
		}
		c := b[i+3]
		if (c == scFrame || c == scSequence || c == scEntryPoint) && sawFrame {
			out = append(out, b[start:i])
			start, sawFrame = i, false
		}
		if c == scFrame {
			sawFrame = true
		}
		i += 2
	}
	return append(out, b[start:])
}

// compare decodes path and checks every picture against ffmpeg's decode,
// as both go.
func compare(t *testing.T, path string) {
	t.Helper()
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	cmd := exec.CommandContext(context.Background(), ff, "-v", "error", "-f", "vc1", "-i", path,
		"-fps_mode", "passthrough", "-f", "rawvideo", "-pix_fmt", "yuv420p", "-")
	ref, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	b, err := os.ReadFile(path) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	d := New()
	n, bad := 0, 0
	var want []byte
	out := func(p *Picture) error {
		w, h := p.Width, p.Height
		size := w*h + 2*(w/2)*(h/2)
		if len(want) != size {
			want = make([]byte, size)
		}
		if _, err := io.ReadFull(ref, want); err != nil {
			t.Errorf("picture %d: ffmpeg gave no more", n)
			return err
		}
		i := 0
		at := func(pl []byte, stride, pw, ph int, name string) bool {
			for y := range ph {
				row := pl[y*stride : y*stride+pw]
				if !bytes.Equal(row, want[i:i+pw]) {
					for x := range row {
						if row[x] != want[i+x] {
							t.Errorf("picture %d differs: first at %s (%d,%d): %d, ffmpeg %d", n, name, x, y, row[x], want[i+x])
							return false
						}
					}
				}
				i += pw
			}
			return true
		}
		if !at(p.Y, p.StrideY, w, h, "Y") || !at(p.Cb, p.StrideC, w/2, h/2, "Cb") || !at(p.Cr, p.StrideC, w/2, h/2, "Cr") {
			bad++
		}
		n++
		if bad > 3 {
			return errors.New("too many differences")
		}
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
	if extra, _ := io.Copy(io.Discard, ref); extra > 0 {
		t.Errorf("ffmpeg gave more pictures (%d bytes)", extra)
	}
	if d.Errors() > 0 {
		t.Errorf("%d decoding errors", d.Errors())
	}
	t.Logf("%d pictures", n)
}

// TestConformance decodes the SMPTE conformance streams in
// $BDTOOLS_VC1_SAMPLES, each bit-exact with ffmpeg.
func TestConformance(t *testing.T) {
	dir := os.Getenv("BDTOOLS_VC1_SAMPLES")
	if dir == "" {
		t.Skip("BDTOOLS_VC1_SAMPLES not set")
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.vc1"))
	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) { compare(t, p) })
	}
}
