package convert

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseShots3D(t *testing.T) {
	shots, err := parseShots3D(`# a film's fixes
0:12:30 0:13:05 depth=0.8   # the close-up
1:02:10.5 flat
1:30:00 screen=-1 depth=2

1:40:00 1:41:00 flat
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(shots) != 4 {
		t.Fatalf("%d stretches: %+v", len(shots), shots)
	}
	at := func(h, m int, s float64) time.Duration {
		return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(s*float64(time.Second))
	}
	if s := shots[0]; s.Start != at(0, 12, 30) || s.End != at(0, 13, 5) || s.Depth != 0.8 || s.Screen != nil || s.Flat {
		t.Errorf("first: %+v", s)
	}
	// open: to the next start
	if s := shots[1]; s.Start != at(1, 2, 10.5) || s.End != at(1, 30, 0) || !s.Flat {
		t.Errorf("second: %+v", s)
	}
	if s := shots[2]; s.End != at(1, 40, 0) || s.Depth != 2 || s.Screen == nil || *s.Screen != -1 {
		t.Errorf("third: %+v", s)
	}
	for _, c := range []struct {
		t    time.Duration
		want int
	}{
		{at(0, 12, 29), -1}, {at(0, 12, 30), 0}, {at(0, 13, 5), -1}, {at(1, 20, 0), 1},
		{at(1, 40, 30), 3}, {at(1, 41, 0), -1}, {at(3, 0, 0), -1},
	} {
		if got := shotAt(shots, c.t); got != c.want {
			t.Errorf("at %s: stretch %d, want %d", clock(c.t), got, c.want)
		}
	}

	for _, bad := range []string{
		"0:10 depth=7", "0:10 screen=9", "0:10 0:09 flat", "0:10", "0:10 0:20", "x flat", "0:10 bright=2", "0:10 flat=1",
	} {
		if _, err := parseShots3D(bad); err == nil || !strings.Contains(err.Error(), "line 1") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// A stretch's shape is the command's with its settings: depth and screen
// as --3d-depth and --3d-screen would make it; elsewhere the command's.
func TestShotShapes(t *testing.T) {
	shots, err := parseShots3D("0:10 0:20 depth=3\n0:30 0:40 screen=1 flat\n")
	if err != nil {
		t.Fatal(err)
	}
	o := Options{To3D: true, Depth3D: 1.5}
	at := func(pts int64) time.Duration { return time.Duration(pts) * time.Second }
	shot := o.shotShapes(shots, "small", at)
	base := o.shape3D("small")
	if s, flat := shot(5); s != base || flat {
		t.Errorf("outside: %+v %v", s, flat)
	}
	d3 := o
	d3.Depth3D = 3
	if s, flat := shot(15); s != d3.shape3D("small") || flat {
		t.Errorf("depth=3: %+v %v", s, flat)
	}
	s1 := o
	s1.Screen3D = 1
	if s, flat := shot(35); s != s1.shape3D("small") || !flat {
		t.Errorf("screen=1 flat: %+v %v", s, flat)
	}
}

// What the stretches say is part of the resume key: an edited file makes
// other pictures. --3d-shots is --to-3d's.
func TestShots3DOptions(t *testing.T) {
	dir := t.TempDir()
	in, file := filepath.Join(dir, "film.mkv"), filepath.Join(dir, "shots.txt")
	if err := os.WriteFile(in, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := func(shots string) string {
		if err := os.WriteFile(file, []byte(shots), 0o600); err != nil {
			t.Fatal(err)
		}
		o := DefaultOptions()
		o.Input, o.To3D, o.Shots3D = in, true, file
		k, err := NewRunner(CurrentGOOS, o, nil).resumeKey()
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	if key("0:10 0:20 depth=2\n") == key("0:10 0:20 depth=2.5\n") {
		t.Error("the same resume key for another depth")
	}
	if key("0:10 0:20 depth=2\n") != key("# a comment\n0:10   0:20 depth=2\n") {
		t.Error("another resume key for the same stretches")
	}

	o := opts("linux", func(o *Options) { o.Shots3D = file })
	if err := o.Validate("linux"); err == nil {
		t.Error("--3d-shots without --to-3d taken")
	}
	o.To3D = true
	if err := o.Validate("linux"); err != nil {
		t.Errorf("--to-3d --3d-shots: %v", err)
	}
	o.Shots3D = filepath.Join(dir, "missing.txt")
	if err := o.Validate("linux"); err == nil {
		t.Error("a missing --3d-shots file taken")
	}
}
