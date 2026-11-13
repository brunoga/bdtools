package convert

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brunoga/bdtools/mvc"
)

// TestWriteShots3D writes stretches and reads them back as --3d-shots does.
func TestWriteShots3D(t *testing.T) {
	file := filepath.Join(t.TempDir(), "film.3d")
	screen := -0.5
	shots := []shot3D{
		{Start: 90 * time.Second, End: 120 * time.Second, Depth: 1.25, Screen: &screen},
		{Start: 120 * time.Second, Flat: true},
		{Start: 3*time.Hour + 2*time.Second + 500*time.Millisecond, End: 3*time.Hour + 10*time.Second, Depth: 2},
	}
	if err := writeShots3D(file, shots); err != nil {
		t.Fatal(err)
	}
	back, err := readShots3D(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != len(shots) {
		t.Fatalf("%d stretches back, want %d", len(back), len(shots))
	}
	shots[1].End = shots[2].Start // an open stretch runs to the next line's start
	for i, s := range shots {
		b := back[i]
		if b.Start != s.Start || b.End != s.End || b.Depth != s.Depth || b.Flat != s.Flat ||
			(s.Screen == nil) != (b.Screen == nil) || s.Screen != nil && *s.Screen != *b.Screen {
			t.Errorf("stretch %d: %+v back, want %+v", i, b, s)
		}
	}
	if _, err := os.Stat(filepath.Join(t.TempDir(), ".film.3d.tmp")); err == nil {
		t.Error("the temporary file was left behind")
	}
}

// TestGraderKnobs turns the knobs through a film: a stretch begins at
// the last cut when one is near, at the picture when not; the stretch
// before ends there; the one at the playhead is edited in place; a
// removed stretch gives its time to the one before.
func TestGraderKnobs(t *testing.T) {
	r := NewRunner("linux", DefaultOptions(), nil)
	r.Opts.To3D, r.Opts.Output = true, filepath.Join(t.TempDir(), "g.3d")
	g := &grader{r: r, wake: make(chan struct{}, 1), saved: true, frame: time.Second / 24}
	sec := func(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
	g.noteCut(sec(10))
	g.noteCut(sec(10)) // again: once
	g.noteCut(sec(30))
	if len(g.cuts) != 2 {
		t.Fatalf("cuts %v", g.cuts)
	}
	// a turn 1.5 s after the cut at 10 s: the stretch starts at the cut
	g.pos = sec(11.5)
	g.setKnobs(gradeKnobs{Depth: 2, Screen: 0.5})
	if len(g.shots) != 1 || g.shots[0].Start != sec(10) || g.shots[0].End != 0 || g.shots[0].Depth != 2 {
		t.Fatalf("after the first turn: %+v", g.shots)
	}
	// a turn at 20 s, no cut near: from the picture; the first ends there
	g.pos = sec(20)
	g.setKnobs(gradeKnobs{Depth: 1, Screen: 0, Flat: true})
	if len(g.shots) != 2 || g.shots[0].End != sec(20) || g.shots[1].Start != sec(20) || !g.shots[1].Flat {
		t.Fatalf("after the second turn: %+v", g.shots)
	}
	// another turn a moment later edits the same stretch
	g.pos = sec(21)
	g.setKnobs(gradeKnobs{Depth: 1.5, Screen: 0})
	if len(g.shots) != 2 || g.shots[1].Depth != 1.5 || g.shots[1].Flat {
		t.Fatalf("after the third turn: %+v", g.shots)
	}
	// a turn after the cut at 30 s: a new stretch from the cut, the second
	// ends there
	g.pos = sec(30.5)
	g.setKnobs(gradeKnobs{Depth: 0.8, Screen: -1})
	if len(g.shots) != 3 || g.shots[1].End != sec(30) || g.shots[2].Start != sec(30) {
		t.Fatalf("after the fourth turn: %+v", g.shots)
	}
	if k := g.knobsAt(sec(25)); k.Depth != 1.5 || k.Screen != 0 {
		t.Errorf("knobs at 25 s: %+v", k)
	}
	if k := g.knobsAt(sec(5)); k != g.defaults() {
		t.Errorf("knobs before any stretch: %+v, want the defaults %+v", k, g.defaults())
	}
	// the file follows
	back, err := readShots3D(r.Opts.Output)
	if err != nil || len(back) != 3 {
		t.Fatalf("the file: %v, %d stretches", err, len(back))
	}
	// removing the middle stretch: the first runs to 30 s
	g.deleteShot(1)
	if len(g.shots) != 2 || g.shots[0].End != sec(30) {
		t.Fatalf("after removing the second: %+v", g.shots)
	}
	// the state the page sees
	g.pos = sec(31)
	st := g.state()
	if st.Stretch != 1 || len(st.Cuts) != 2 || st.Knobs.Depth != 0.8 {
		t.Errorf("state: stretch %d, cuts %v, knobs %+v", st.Stretch, st.Cuts, st.Knobs)
	}
	// commands
	if err := g.command(gradeCommand{Op: "jump", Dir: -1}); err != nil || g.want.seek == nil || *g.want.seek != sec(30) {
		t.Errorf("jump back from 31 s: %v, %v", err, g.want.seek)
	}
	g.want.seek = nil
	if err := g.command(gradeCommand{Op: "step", N: -1}); err != nil || g.want.seek == nil || *g.want.seek != sec(31)-g.frame {
		t.Errorf("a picture back: %v, %v", err, g.want.seek)
	}
	if err := g.command(gradeCommand{Op: "knobs", Knobs: &gradeKnobs{Depth: 9}}); err == nil {
		t.Error("depth 9 taken")
	}
	if err := g.command(gradeCommand{Op: "dance"}); err == nil {
		t.Error("an unknown op taken")
	}
}

func TestDepthExtremes(t *testing.T) {
	d := make([]float32, 1000)
	for i := range d {
		d[i] = float32(i) / 100 // 0 to 10
	}
	s := DefaultOptions().shape3D("small")
	near, far := depthExtremes(d, s, false)
	wantNear, wantFar := 100*(s.Scale*9.9+s.Offset), 100*(s.Scale*0+s.Offset)
	if near < wantNear-0.02 || near > wantNear+0.02 || far < wantFar-0.02 || far > wantFar+0.02 { // the depth is sampled
		t.Errorf("near %.3f far %.3f, want about %.3f and %.3f", near, far, wantNear, wantFar)
	}
	if n, f := depthExtremes(d, s, true); n != 0 || f != 0 {
		t.Errorf("flat: %g %g", n, f)
	}
}

// TestThumbnailDistance: a picture is nearest itself, farther from the
// same picture moved, and a lighter copy is as near.
func TestThumbnailDistance(t *testing.T) {
	pic := func(shift int, lift byte) *mvc.Frame {
		w, h := 320, 180
		f := &mvc.Frame{Width: w, Height: h, StrideY: w, StrideC: w / 2, Y: make([]byte, w*h), Cb: make([]byte, w*h/4), Cr: make([]byte, w*h/4)}
		for y := range h {
			for x := range w {
				v := byte(0)
				if (x+shift)/40%2 == (y/40)%2 {
					v = 200
				}
				f.Y[y*w+x] = v/2 + lift
			}
		}
		return f
	}
	a := thumbnail(pic(0, 0))
	if d := thumbnailDistance(a, thumbnail(pic(0, 0))); d != 0 {
		t.Errorf("itself: %g", d)
	}
	if d := thumbnailDistance(a, thumbnail(pic(0, 30))); d > 1e-3 {
		t.Errorf("lighter: %g", d)
	}
	if d := thumbnailDistance(a, thumbnail(pic(20, 0))); d < 10 {
		t.Errorf("moved: %g", d)
	}
}
