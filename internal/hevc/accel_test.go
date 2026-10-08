package hevc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// fakeAccel checks what the decoder hands an accelerator.
type fakeAccel struct {
	t       *testing.T
	used    map[int]bool
	next    int
	live    map[int]int // surface -> POC of the picture decoded into it
	outputs []int64
}

func (f *fakeAccel) NewSurface(w, h, depth int) (int, error) {
	for s := range f.next {
		if !f.used[s] {
			f.used[s] = true
			return s, nil
		}
	}
	if f.next >= 20 {
		return 0, errors.New("out of surfaces")
	}
	f.used[f.next] = true
	f.next++
	return f.next - 1, nil
}

func (f *fakeAccel) Release(s int) {
	if !f.used[s] {
		f.t.Errorf("surface %d released twice", s)
	}
	f.used[s] = false
}

func (f *fakeAccel) DecodePicture(p *AccelPicture, slices []AccelSlice) error {
	if !f.used[p.Surface] {
		return fmt.Errorf("picture %d in a free surface", p.POC)
	}
	for _, r := range p.Refs {
		if poc, ok := f.live[r.Surface]; !ok || poc != r.POC || r.Surface == p.Surface {
			return fmt.Errorf("picture %d refers to POC %d in surface %d, which holds %d", p.POC, r.POC, r.Surface, poc)
		}
	}
	for i, s := range slices {
		if s.DataOffset <= 2 || s.DataOffset > len(s.Data) || s.Data[0]>>1&0x3f > 21 {
			return fmt.Errorf("picture %d slice %d: data offset %d of %d", p.POC, i, s.DataOffset, len(s.Data))
		}
		if (s.SegmentAddr == 0) != (i == 0) {
			return fmt.Errorf("picture %d slice %d at %d", p.POC, i, s.SegmentAddr)
		}
		for l := range 2 {
			if len(s.RefList[l]) != s.NumRefIdx[l] && s.Type != 2 && l < numLists(s.Type) {
				return fmt.Errorf("picture %d slice %d: list %d has %d of %d", p.POC, i, l, len(s.RefList[l]), s.NumRefIdx[l])
			}
			for _, r := range s.RefList[l] {
				if r < 0 || r >= len(p.Refs) || p.Refs[r].RPS == 0 {
					return fmt.Errorf("picture %d slice %d: list %d refers to %d", p.POC, i, l, r)
				}
			}
		}
	}
	f.live[p.Surface] = p.POC
	return nil
}

func (f *fakeAccel) Output(s int, p *Picture) error {
	if _, ok := f.live[s]; !ok {
		return fmt.Errorf("output of surface %d, never decoded", s)
	}
	f.outputs = append(f.outputs, p.PTS)
	return nil
}

// With an accelerator, the decoder hands it each picture's slices with
// consistent references and surfaces, and gives out the pictures the
// software decoding gives, in the same order.
func TestAccel(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	for _, c := range []struct{ name, params string }{
		{"b-pyramid", "bframes=4:b-pyramid=1:ref=4:slices=2"},
		{"weighted", "weightb=1:weightp=1:bframes=3:keyint=12:open-gop=1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, c.name+".hevc")
			if out, err := exec.CommandContext(t.Context(), ff, "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=200x136:r=25:d=2", //nolint:gosec // test
				"-c:v", "libx265", "-pix_fmt", "yuv420p10le", "-x265-params", "log-level=error:"+c.params, "-f", "hevc", path).CombinedOutput(); err != nil {
				t.Skipf("making the stream: %v %s", err, out)
			}
			b, err := os.ReadFile(path) //nolint:gosec // test
			if err != nil {
				t.Fatal(err)
			}
			// The software decoder's order, each access unit timed by its
			// place in the stream.
			units := splitAUs(b)
			var want []int64
			sw := New()
			for i, u := range units {
				if err := sw.Decode(u, int64(i), func(p *Picture) error { want = append(want, p.PTS); return nil }); err != nil {
					t.Fatal(err)
				}
			}
			if err := sw.Flush(func(p *Picture) error { want = append(want, p.PTS); return nil }); err != nil {
				t.Fatal(err)
			}
			f := &fakeAccel{t: t, used: map[int]bool{}, live: map[int]int{}}
			d := New()
			d.SetAccel(f)
			for i, u := range units {
				if err := d.Decode(u, int64(i), nil); err != nil {
					t.Fatal(err)
				}
			}
			if err := d.Flush(nil); err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(f.outputs) != fmt.Sprint(want) {
				t.Errorf("output\n%v\nwant\n%v", f.outputs, want)
			}
		})
	}
}

// splitAUs splits an Annex B stream before each picture's first slice
// (and the parameter sets before it).
func splitAUs(b []byte) [][]byte {
	var units [][]byte
	start, sawSlice := 0, false
	for i := 0; i+4 < len(b); i++ {
		if b[i] != 0 || b[i+1] != 0 || b[i+2] != 1 {
			continue
		}
		t := b[i+3] >> 1 & 0x3f
		first := t <= 21 && b[i+5]&0x80 != 0
		if (first || t >= 32 && t <= 34) && sawSlice {
			at := i
			if at > 0 && b[at-1] == 0 {
				at--
			}
			units = append(units, b[start:at])
			start, sawSlice = at, false
		}
		if t <= 21 {
			sawSlice = true
		}
	}
	return append(units, b[start:])
}
