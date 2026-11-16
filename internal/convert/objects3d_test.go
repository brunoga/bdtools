package convert

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brunoga/bdtools/internal/stereo"
)

// The shots file keeps its objects beside its stretches, and the
// stretches' reader passes over them.
func TestObjectLines(t *testing.T) {
	file := filepath.Join(t.TempDir(), "film.shots")
	shots := []shot3D{{Start: time.Second, End: 2 * time.Second, Depth: 1.5}}
	objs := []object3D{{ID: 1, Start: 25500 * time.Millisecond, End: 31 * time.Second, Push: 1.5}, {ID: 2, Start: time.Minute, End: 61 * time.Second, Push: -0.5}}
	if err := writeShots3D(file, shots, objs); err != nil {
		t.Fatal(err)
	}
	gotShots, err := readShots3D(file)
	if err != nil || len(gotShots) != 1 || gotShots[0].Depth != 1.5 {
		t.Fatalf("stretches %+v, %v", gotShots, err)
	}
	got, err := readObjects3D(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != objs[0] || got[1] != objs[1] {
		t.Fatalf("objects %+v, want %+v", got, objs)
	}
	for _, bad := range []string{"object 0 0:01 0:02 push=1", "object 1 0:02 0:01 push=1", "object 1 0:01 0:02 push=9", "object 1 0:01 0:02 depth=1", "object 1 0:01"} {
		if err := os.WriteFile(file, []byte(bad+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readObjects3D(file); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

// Masks come back from their file as they went in, and are found by the
// time of their picture, within half a picture.
func TestMasksFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "film.shots.masks")
	const n = stereo.ObjectSide * stereo.ObjectSide
	mk := func(from, to int) []byte {
		m := make([]byte, n)
		for i := from; i < to; i++ {
			m[i] = 255
		}
		return m
	}
	tracks := map[int]*maskTrack{1: {}, 7: {}}
	tracks[1].set(2*time.Second, mk(100, 5000))
	tracks[1].set(time.Second, mk(0, n)) // out of order, and all of it
	tracks[7].set(time.Second, mk(n-3, n))
	if err := writeMasks3D(file, tracks); err != nil {
		t.Fatal(err)
	}
	got, err := readMasks3D(file)
	if err != nil {
		t.Fatal(err)
	}
	for id, tr := range tracks {
		g := got[id]
		if g == nil || len(g.at) != len(tr.at) {
			t.Fatalf("object %d: %d masks, want %d", id, len(g.at), len(tr.at))
		}
		for i := range tr.at {
			if g.at[i] != tr.at[i] || !bytes.Equal(g.masks[i], tr.masks[i]) {
				t.Fatalf("object %d mask %d differs", id, i)
			}
		}
	}
	tol := 20 * time.Millisecond
	if m := got[1].near(2*time.Second+10*time.Millisecond, tol); m == nil || m[100] != 255 || m[99] != 0 {
		t.Error("the mask at 2 s not found near it")
	}
	if m := got[1].near(1500*time.Millisecond, tol); m != nil {
		t.Error("a mask found between two pictures")
	}
	if m, err := readMasks3D(filepath.Join(t.TempDir(), "none")); err != nil || len(m) != 0 {
		t.Errorf("a missing file: %v, %v", m, err)
	}
	at := func(pts int64) time.Duration { return time.Duration(pts) * time.Millisecond }
	objs := objectsAt([]object3D{{ID: 1, Start: 0, End: 1500 * time.Millisecond, Push: 2}, {ID: 7, Start: 0, End: time.Hour, Push: -1}}, got, at, tol)
	if o := objs(1000); len(o) != 2 || o[0].Push != 0.02 || o[1].Push != -0.01 {
		t.Errorf("at 1 s: %+v", o)
	}
	if o := objs(2000); len(o) != 0 {
		t.Errorf("at 2 s, past object 1's stretch and without a mask of 7's: %d objects", len(o))
	}
}
