package convert

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brunoga/bdtools/internal/mkv"
)

// A 3D source's depth changed: its pictures made again, out side by side,
// every one there; a copied video refused.
func TestRunnerRedepth(t *testing.T) {
	work := t.TempDir()
	out := filepath.Join(work, "flatter.mkv")
	o := DefaultOptions()
	o.Input, o.Output, o.TempDir = mvcMatroska(t), out, work
	o.Encoder, o.Codec, o.CRF, o.Decoder = EncoderBuiltin, CodecH265, 30, DecoderCPU
	o.Depth3D, o.Screen3D = 0.5, 1
	var lines []string
	report := func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }
	if err := NewRunner(CurrentGOOS, o, report).Run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, strings.Join(lines, "\n"))
	}
	if !strings.Contains(strings.Join(lines, "\n"), "the depth 0.5 times the source's") {
		t.Errorf("no report of the depth change:\n%s", strings.Join(lines, "\n"))
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
	v := rd.Tracks[0]
	if v.StereoMode != 1 {
		t.Fatalf("stereo mode %d, want side by side (1)", v.StereoMode)
	}
	frames := 0
	for {
		p, err := rd.Next()
		if err != nil {
			break
		}
		if p.Track == v.Number {
			frames++
		}
	}
	if frames == 0 {
		t.Error("no pictures")
	}

	o.Output, o.Encoder = filepath.Join(work, "copy.mkv"), EncoderCopy
	o.Layout, o.Codec = LayoutMVC, CodecH264
	if err := NewRunner(CurrentGOOS, o, func(string, ...any) {}).Run(context.Background()); err == nil || !strings.Contains(err.Error(), "cannot be copied") {
		t.Errorf("a copied video: %v", err)
	}
}

// The subtitles' offsets follow the depth: scaled, moved by half the
// shift (an offset is half a disparity), kept within the metadata's
// range.
func TestRedepthOffsets(t *testing.T) {
	m := &depthMap{frame: 40 * time.Millisecond, scale: 0.5, shift: 9.6}
	m.add(0, offsetGOP{offsets: [][]int8{{10, -20, 100}}})
	for i, want := range []int{15, 0, 60} {
		if got := m.offset(0, time.Duration(i)*40*time.Millisecond); got != want {
			t.Errorf("frame %d: offset %d, want %d", i, got, want)
		}
	}
	if got := redepthOffset(120, 2, 0); got != 127 {
		t.Errorf("clamped: %d, want 127", got)
	}
	o := Options{Depth3D: 0.5}
	if !o.redepthAsked() || o.redepthK() != 0.5 {
		t.Error("--3d-depth 0.5 on a 3D source not asked")
	}
	if o := (Options{To3D: true, Depth3D: 0.5}); o.redepthAsked() {
		t.Error("--to-3d's depth taken for a 3D source's")
	}
	if o := (Options{Depth3D: 1}); o.redepthAsked() {
		t.Error("the depth as it is asked as a change")
	}
}
