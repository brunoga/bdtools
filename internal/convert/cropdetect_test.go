package convert

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brunoga/bdtools/internal/mkv"
)

// A picture's bars: black rows and columns at its edges, a stray bright
// sample in a bar (ringing) allowed, a dark picture area not taken for a
// bar; a picture all black shows nothing.
func TestBlackBars(t *testing.T) {
	const w, h = 256, 144
	for _, depth := range []int{8, 10} {
		for _, full := range []bool{false, true} {
			black := 16 << (depth - 8)
			if full {
				black = 0
			}
			p := testPicture(w, h, depth, func(x, y int) int {
				switch {
				case y < 18 || y >= h-20 || x < 8:
					if y == 3 && x == 100 {
						return 235 << (depth - 8) // a stray sample
					}
					return black
				case y < 30:
					return black + 10<<(depth-8) // dark, not black
				}
				return (60 + x%100) << (depth - 8)
			}, func(x, y, c int) int { return 128 << (depth - 8) })
			p.Color.FullRange = full
			c, ok := blackBars(p)
			if !ok || c != (Crop{Top: 18, Bottom: 20, Left: 8}) {
				t.Errorf("%d-bit full %v: %v %v, want 18:20:8:0", depth, full, c, ok)
			}
			fade := testPicture(w, h, depth, func(x, y int) int { return black }, func(x, y, c int) int { return 128 << (depth - 8) })
			fade.Color.FullRange = full
			if _, ok := blackBars(fade); ok {
				t.Errorf("%d-bit full %v: a black picture measured", depth, full)
			}
		}
	}
}

// Resync goes on at a cluster in the middle of a file (ffmpeg's, whose
// clusters start with a CRC-32), its frames in order from there.
func TestMatroskaResync(t *testing.T) {
	path := filepath.Join(t.TempDir(), "src.mkv")
	if err := runCmd(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=s=320x240:r=24:d=4", "-c:v", "libx264", "-g", "6",
		"-cluster_time_limit", "250", path); err != nil {
		t.Skipf("could not build the source: %v", err)
	}
	f, err := os.Open(path) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	rd, err := mkv.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := f.Stat()
	if err := rd.Resync(f, st.Size()/2); err != nil {
		t.Fatal(err)
	}
	var last time.Duration
	for i := range 12 {
		p, err := rd.Next()
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && (p.Time < time.Second || p.Time > 3*time.Second) {
			t.Errorf("first frame after the middle at %v", p.Time)
		}
		if i == 0 && !p.Keyframe {
			t.Error("a cluster that does not start with a keyframe")
		}
		if p.Time < last-time.Second/4 {
			t.Errorf("frame %d at %v after %v", i, p.Time, last)
		}
		last = p.Time
	}
}

// --crop auto finds a letterboxed film's bars, in Matroska, MP4 and
// transport stream sources, and the picture comes out without them.
func TestRunnerCropsAuto(t *testing.T) {
	work := t.TempDir()
	for _, c := range []struct{ name, format string }{{"src.mkv", "matroska"}, {"src.mp4", "mp4"}, {"src.m2ts", "mpegts"}} {
		t.Run(c.format, func(t *testing.T) {
			src := filepath.Join(work, c.name)
			if err := runCmd(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
				"-f", "lavfi", "-i", "testsrc2=s=320x180:r=24:d=4", "-vf", "pad=320:240:0:30",
				"-c:v", "libx264", "-g", "12", "-pix_fmt", "yuv420p", "-f", c.format, src); err != nil {
				t.Skipf("could not build the source: %v", err)
			}
			out := filepath.Join(t.TempDir(), "out.mkv")
			o := DefaultOptions()
			o.Input, o.Output, o.TempDir = src, out, t.TempDir()
			o.Encoder, o.Codec, o.CRF, o.Decoder = EncoderBuiltin, CodecH265, 30, DecoderCPU
			o.Crop = Crop{Auto: true}
			var lines []string
			if err := NewRunner(CurrentGOOS, o, func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }).Run(context.Background()); err != nil {
				t.Fatalf("%v\n%s", err, strings.Join(lines, "\n"))
			}
			if !slices.ContainsFunc(lines, func(l string) bool {
				return strings.HasPrefix(l, "--crop auto: black bars top 30, bottom 30, left 0, right 0")
			}) {
				t.Fatalf("bars not found:\n%s", strings.Join(lines, "\n"))
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
			if v := rd.Tracks[0]; v.Width != 320 || v.Height != 180 {
				t.Errorf("%dx%d out, want 320x180", v.Width, v.Height)
			}
		})
	}
}
