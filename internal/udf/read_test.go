package udf

import (
	"os"
	"testing"
)

// TestOpensImage opens a real image (BDTOOLS_ISO2) with this reader alone
// and reads both ends of its biggest file: a disc whose chain of
// allocation extent descriptors runs deeper than other readers follow.
func TestOpensImage(t *testing.T) {
	path := os.Getenv("BDTOOLS_ISO2")
	if path == "" {
		t.Skip("set BDTOOLS_ISO2")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	img, err := Open(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	var big *Entry
	var bigSize int64
	var files int
	var walk func(e *Entry, prefix string)
	walk = func(e *Entry, prefix string) {
		kids, err := e.ReadDir()
		if err != nil {
			t.Fatalf("%s: %v", prefix, err)
		}
		for _, k := range kids {
			p := prefix + "/" + k.Name()
			if k.IsDir() {
				walk(k, p)
				continue
			}
			files++
			n, err := k.Size()
			if err != nil {
				t.Fatalf("%s: %v", p, err)
			}
			if n > bigSize {
				big, bigSize = k, n
			}
		}
	}
	walk(img.Root(), "")
	if big == nil {
		t.Fatal("no files")
	}
	r, err := big.Reader()
	if err != nil {
		t.Fatal(err)
	}
	head, tail := make([]byte, 192), make([]byte, 192)
	if _, err := r.ReadAt(head, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadAt(tail, bigSize-192); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d files; the biggest %s: %d bytes in %d extents, sync bytes %x %x", files, big.Name(), bigSize, len(big.exts), head[4], tail[4])
}
