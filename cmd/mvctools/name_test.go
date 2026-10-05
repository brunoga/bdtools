package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/brunoga/mvc/internal/convert"
)

func TestAppendCodecToName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Toy Story (1995) 3D FSBS.mkv")
	write := func() {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	write()
	got, err := appendCodecToName(path, []convert.Track{{Type: "TrueHD Atmos"}})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	want := filepath.Join(dir, "Toy Story (1995) 3D FSBS.TrueHD-Atmos.mkv")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if _, err := os.Stat(got); err != nil {
		t.Errorf("renamed file is not there: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the original name still exists, so the file was copied rather than moved")
	}
}

// With several tracks kept the first is used: the disc's own order, so its
// primary mix.
func TestAppendCodecUsesTheFirstKeptTrack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.mkv")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := appendCodecToName(path, []convert.Track{{Type: "DTS-HD Master Audio"}, {Type: "AC3"}})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if filepath.Base(got) != "f.DTS-HD-Master-Audio.mkv" {
		t.Errorf("got %q, want the first track's codec", filepath.Base(got))
	}
}

// With no audio kept the name is left alone: a file labelled with a codec it
// does not contain is worse than an unlabelled one.
func TestAppendCodecLeavesNameAloneWithNoAudio(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.mkv")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := appendCodecToName(path, nil)
	if err != nil || got != path {
		t.Errorf("got %q (err %v), want the path unchanged", got, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the file was moved anyway: %v", err)
	}
}

// A failed rename must report the original path, so the caller still knows
// where the conversion's output actually is.
func TestAppendCodecReportsTheOriginalOnFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-there.mkv")
	got, err := appendCodecToName(missing, []convert.Track{{Type: "AC3"}})
	if err == nil {
		t.Fatal("renaming a file that does not exist should fail")
	}
	if got != missing {
		t.Errorf("got %q, want the original path back so the file can still be found", got)
	}
}
