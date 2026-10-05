package mvc

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// hash128 is the two-lane FNV-1a used by the conformance manifest.
type hash128 struct{ a, b uint64 }

func newHash128() hash128 { return hash128{0xcbf29ce484222325, 0x84222325cbf29ce4} }

func (h *hash128) write(p []byte) {
	a, b := h.a, h.b
	for _, c := range p {
		a = (a ^ uint64(c)) * 0x100000001b3
		b = (b ^ uint64(c)) * 0x100000001b3
	}
	h.a, h.b = a, b
}

func (h *hash128) frame(f *Frame) {
	for y := 0; y < f.Height; y++ {
		h.write(f.Y[y*f.StrideY : y*f.StrideY+f.Width])
	}
	for _, pl := range [][]byte{f.Cb, f.Cr} {
		for y := 0; y < f.Height/2; y++ {
			h.write(pl[y*f.StrideC : y*f.StrideC+f.Width/2])
		}
	}
}

func (h hash128) String() string { return fmt.Sprintf("%016x%016x", h.a, h.b) }

type manifestEntry struct {
	name   string
	frames int
	stereo bool
	base   string
	dep    string
}

func readManifest(t *testing.T, path string) []manifestEntry {
	f, err := os.Open(path)
	if err != nil {
		t.Skip("no conformance manifest:", err)
	}
	defer f.Close()
	var out []manifestEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		fs := strings.Fields(line)
		if len(fs) < 5 {
			continue
		}
		n, _ := strconv.Atoi(fs[1])
		out = append(out, manifestEntry{fs[0], n, fs[2] == "1", fs[3], fs[4]})
	}
	return out
}

// decodeFile decodes a whole Annex B file, returning per-view hashes.
func decodeFile(data []byte, opts Options) (frames int, base, dep hash128, nDep int, err error) {
	d := NewDecoder(opts)
	base, dep = newHash128(), newHash128()
	drain := func() {
		for {
			f, ok := d.NextFrame()
			if !ok {
				return
			}
			frames++
			base.frame(f.Base)
			if f.Dependent != nil {
				dep.frame(f.Dependent)
				nDep++
			}
			f.Release()
		}
	}
	const chunk = 1 << 16
	for i := 0; i < len(data); i += chunk {
		e := d.Decode(data[i:min(i+chunk, len(data))])
		if e != nil && err == nil {
			err = e
		}
		drain()
	}
	if e := d.Flush(); e != nil && err == nil {
		err = e
	}
	drain()
	return
}

func TestConformance(t *testing.T) {
	dir := "testdata/conformance"
	for _, e := range readManifest(t, filepath.Join(dir, "manifest.txt")) {
		e := e
		t.Run(e.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, e.name+".264"))
			if err != nil {
				t.Skip(err)
			}
			frames, base, dep, nDep, derr := decodeFile(data, Options{})
			if derr != nil {
				t.Logf("decode error: %v", derr)
			}
			if frames != e.frames {
				t.Errorf("frames = %d, want %d", frames, e.frames)
			}
			if base.String() != e.base {
				t.Errorf("base hash mismatch")
			}
			if e.stereo {
				if nDep == 0 {
					t.Errorf("no dependent view frames")
				}
				if dep.String() != e.dep {
					t.Errorf("dependent hash mismatch")
				}
			}
		})
	}
}
