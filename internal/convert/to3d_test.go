package convert

import (
	"context"
	"fmt"
	"github.com/brunoga/bdtools/internal/stereo"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brunoga/bdtools/internal/mkv"
)

// A 2D source made 3D (--to-3d, the depth by the depth tests' small
// model): full side by side out, twice the source's width, marked side by
// side, every picture there. The source is the MVC fixture's base view,
// converted 2D first (no tools beside this program).
func TestRunnerTo3D(t *testing.T) {
	work := t.TempDir()
	flat := filepath.Join(work, "flat.mkv")
	o := DefaultOptions()
	o.Input, o.Output, o.TempDir = mvcMatroska(t), flat, work
	o.Encoder, o.Codec, o.TwoD, o.CRF, o.Decoder = EncoderBuiltin, CodecH265, true, 30, DecoderCPU
	o.Scale = Scale{W: 160, H: 120}
	var lines []string
	report := func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }
	if err := NewRunner(CurrentGOOS, o, report).Run(context.Background()); err != nil {
		t.Fatalf("the 2D source: %v\n%s", err, strings.Join(lines, "\n"))
	}

	out := filepath.Join(work, "3d.mkv")
	o = DefaultOptions()
	o.Input, o.Output, o.TempDir = flat, out, t.TempDir()
	o.Encoder, o.Codec, o.CRF, o.Decoder = EncoderBuiltin, CodecH265, 30, DecoderCPU
	o.To3D, o.DepthModel = true, filepath.Join("..", "depth", "testdata", "tiny.pth")
	lines = nil
	if err := NewRunner(CurrentGOOS, o, report).Run(context.Background()); err != nil {
		t.Fatalf("--to-3d: %v\n%s", err, strings.Join(lines, "\n"))
	}
	if !strings.Contains(strings.Join(lines, "\n"), "made 3D (its depth estimated)") {
		t.Errorf("no report of the conversion:\n%s", strings.Join(lines, "\n"))
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
	if v.Width != 320 || v.Height != 120 || v.StereoMode != 1 {
		t.Fatalf("video %dx%d, stereo mode %d; want 320x120 side by side (1)", v.Width, v.Height, v.StereoMode)
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

	// a 3D source is refused
	o.Input = mvcMatroska(t)
	if err := NewRunner(CurrentGOOS, o, func(string, ...any) {}).Run(context.Background()); err == nil || !strings.Contains(err.Error(), "3D already") {
		t.Errorf("a 3D source: %v", err)
	}
}

// A subtitle sits just in front of the nearest of the picture's lower
// half, at the screen when it is all behind.
func TestSubtitleOffset(t *testing.T) {
	const w, h = 64, 40
	d := make([]float32, w*h)
	for i := range d {
		d[i] = -6
	}
	if o := subtitleOffset(stereo.SamplesOf(d, w, h, 0, h).Low); o != 0 {
		t.Errorf("all behind: offset %g, want 0 (the screen)", o)
	}
	for y := h / 2; y < h; y++ {
		for x := range w / 2 {
			d[y*w+x] = 10 // nearer, in the lower half
		}
	}
	for x := range w {
		d[x] = 40 // nearer still, but at the top
	}
	if o := subtitleOffset(stereo.SamplesOf(d, w, h, 0, h).Low); o != 6 {
		t.Errorf("offset %g, want 6 (half of 10, plus 1)", o)
	}
}

// Another depth network makes other pictures: an interrupted conversion
// with one does not resume with another's.
func TestResumeKeyHasTheDepthModel(t *testing.T) {
	in := filepath.Join(t.TempDir(), "film.mkv")
	if err := os.WriteFile(in, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := func(model string) string {
		o := DefaultOptions()
		o.Input, o.To3D, o.DepthModel = in, true, model
		k, err := NewRunner(CurrentGOOS, o, nil).resumeKey()
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	if key("") == key("base") {
		t.Error("the same resume key for the small and the base network")
	}
}
