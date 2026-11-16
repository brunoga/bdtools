package convert

import (
	"path/filepath"
	"testing"
	"time"
)

// The page's painting commands: hints are checked, a paint pauses the
// film, track needs a mask, and an object's push and removal reach the
// shots file.
func TestPaintCommands(t *testing.T) {
	r := NewRunner("linux", DefaultOptions(), nil)
	r.Opts.To3D, r.Opts.Output = true, filepath.Join(t.TempDir(), "g.3d")
	g := &grader{r: r, wake: make(chan struct{}, 1), saved: true, frame: time.Second / 24,
		masks: map[int]*maskTrack{}, events: map[chan struct{}]struct{}{}}
	g.want.playing = true
	g.pos = 27 * time.Second
	push := 1.5
	for _, bad := range []gradeCommand{
		{Op: "paint", Box: []float64{0.5, 0.5, 0.4, 0.6}},
		{Op: "paint", Box: []float64{0, 0, 1}},
		{Op: "paint", Points: [][3]float64{{1.2, 0.5, 1}}},
		{Op: "paint", I: 3, Box: []float64{0, 0, 1, 1}},
		{Op: "track"},
		{Op: "object", I: 1, Push: &push},
	} {
		if err := g.command(bad); err == nil {
			t.Errorf("%+v: no error", bad)
		}
	}
	if err := g.command(gradeCommand{Op: "paint", Box: []float64{0.25, 0.7, 0.75, 0.95}}); err != nil {
		t.Fatal(err)
	}
	if g.want.playing || !g.want.paint || g.paint == nil || g.paint.at != 27*time.Second {
		t.Fatalf("after paint: %+v, %+v", g.want, g.paint)
	}
	if err := g.command(gradeCommand{Op: "track"}); err == nil {
		t.Error("track with no mask yet: no error")
	}
	g.paint.mask, g.paint.Found = make([]byte, 4), true
	if err := g.command(gradeCommand{Op: "track"}); err != nil || !g.want.track {
		t.Fatalf("track: %v, %+v", err, g.want)
	}
	// an object as tracking leaves it
	g.objects = []object3D{{ID: 1, Start: 25 * time.Second, End: 31 * time.Second}}
	g.masks[1] = &maskTrack{}
	if err := g.command(gradeCommand{Op: "object", I: 1, Push: &push}); err != nil {
		t.Fatal(err)
	}
	if got, err := readObjects3D(r.Opts.Output); err != nil || len(got) != 1 || got[0].Push != 1.5 {
		t.Fatalf("the shots file's objects: %+v, %v", got, err)
	}
	if err := g.command(gradeCommand{Op: "select", I: 1}); err != nil || g.selected != 1 {
		t.Fatalf("select: %v, %d", err, g.selected)
	}
	if err := g.command(gradeCommand{Op: "object", I: 1, Delete: true}); err != nil {
		t.Fatal(err)
	}
	if len(g.objects) != 0 || g.masks[1] != nil || g.selected != 0 {
		t.Fatalf("after delete: %+v, %v, %d", g.objects, g.masks, g.selected)
	}
	if got, _ := readObjects3D(r.Opts.Output); len(got) != 0 {
		t.Fatalf("the shots file still has %+v", got)
	}
}
