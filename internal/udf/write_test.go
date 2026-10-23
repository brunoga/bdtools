package udf

import (
	"bytes"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golift.io/udf"
)

func content(b []byte) func() (io.Reader, error) {
	return func() (io.Reader, error) { return bytes.NewReader(b), nil }
}

func random(r *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

// a BDMV of two stream files, their 3D interleaving an SSIF of
// alternating pieces of them, enough of them for its entry's allocation
// descriptors to go on in allocation extent descriptors.
func bdmvTree(r *rand.Rand, pieces int) (root *File, order []Piece, want map[string][]byte) {
	const unit = 6144
	base, dep := random(r, pieces*3*unit), random(r, pieces*unit)
	small := random(r, 300)
	want = map[string][]byte{"BDMV/index.bdmv": small, "BDMV/STREAM/00000.m2ts": base, "BDMV/STREAM/00001.m2ts": dep}
	fb := &File{Name: "00000.m2ts", Size: int64(len(base)), Content: content(base), RealTime: true}
	fd := &File{Name: "00001.m2ts", Size: int64(len(dep)), Content: content(dep), RealTime: true}
	var ssif []byte
	for k := range pieces {
		d := Piece{fd, int64(k * unit), unit}
		b := Piece{fb, int64(k * 3 * unit), 3 * unit}
		order = append(order, d, b)
		ssif = append(append(ssif, dep[k*unit:(k+1)*unit]...), base[k*3*unit:(k+1)*3*unit]...)
	}
	want["BDMV/STREAM/SSIF/00000.ssif"] = ssif
	fs := &File{Name: "00000.ssif", RealTime: true, Parts: order}
	root = &File{Dir: true, Children: []*File{
		{Name: "BDMV", Dir: true, Children: []*File{
			{Name: "index.bdmv", Size: int64(len(small)), Content: content(small)},
			{Name: "STREAM", Dir: true, Children: []*File{fb, fd, {Name: "SSIF", Dir: true, Children: []*File{fs}}}},
			{Name: "BACKUP", Dir: true},
		}},
		{Name: "CERTIFICATE", Dir: true},
	}}
	return root, order, want
}

func TestWriteReadsBack(t *testing.T) {
	for _, pieces := range []int{3, 300} {
		root, order, want := bdmvTree(rand.New(rand.NewPCG(1, uint64(pieces))), pieces)
		path := filepath.Join(t.TempDir(), "disc.iso")
		if keep := os.Getenv("UDF_KEEP"); keep != "" {
			path = filepath.Join(keep, fmt.Sprintf("disc%d.iso", pieces)) // for other readers to look at
		}
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := Write(f, root, order, Options{Label: "FILM", Time: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)}); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		img, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		u, err := udf.NewUdfFromReader(img)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string][]byte{}
		var walk func(dir []udf.File, at string)
		walk = func(dir []udf.File, at string) {
			for _, e := range dir {
				p := filepath.Join(at, e.Name())
				if e.IsDir() {
					sub, err := e.ReadDir()
					if err != nil {
						t.Fatalf("%s: %v", p, err)
					}
					walk(sub, p)
					continue
				}
				sr, err := e.NewReader()
				if err != nil {
					t.Fatalf("%s: %v", p, err)
				}
				b, err := io.ReadAll(sr)
				if err != nil {
					t.Fatalf("%s: %v", p, err)
				}
				got[p] = b
			}
		}
		top, err := u.ReadDir(nil)
		if err != nil {
			t.Fatal(err)
		}
		walk(top, "")
		for name, b := range want {
			if !bytes.Equal(got[name], b) {
				t.Errorf("%d pieces: %s read back as %d bytes, want %d", pieces, name, len(got[name]), len(b))
			}
		}
		if len(got) != len(want) {
			t.Errorf("%d pieces: %d files read back, want %d", pieces, len(got), len(want))
		}
		_ = img.Close()
	}
}
