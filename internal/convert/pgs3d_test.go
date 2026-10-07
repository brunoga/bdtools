package convert

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brunoga/mvc/internal/mkv"
)

// Run-length data decodes to what was encoded, whatever the runs.
func TestRLERoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	for _, w := range []int{1, 2, 63, 64, 65, 300, 20000} {
		h := 3
		pix := make([]byte, w*h)
		for i := range pix {
			switch r.IntN(4) {
			case 0:
				pix[i] = byte(r.IntN(256))
			default: // runs
				if i > 0 {
					pix[i] = pix[i-1]
				}
			}
		}
		got, err := decodeRLE(encodeRLE(pix, w, h), w, h)
		if err != nil || !bytes.Equal(got, pix) {
			t.Errorf("width %d: %v, round trip differs", w, err)
		}
	}
}

// displaySet builds a display set: a composition of one object in one
// window at x, y, a palette, the object, and the end.
func displaySet(x, y, w, h int, pix []byte) []byte {
	be := func(v int) []byte { return []byte{byte(v >> 8), byte(v)} }
	var pcs []byte
	pcs = append(pcs, be(1920)...)
	pcs = append(pcs, be(1080)...)
	pcs = append(pcs, 0x10, 0, 1, 0x80, 0, 0, 1)
	pcs = append(pcs, 0, 0, 0, 0)
	pcs = append(pcs, be(x)...)
	pcs = append(pcs, be(y)...)
	wds := append([]byte{1, 0}, be(x)...)
	wds = append(wds, be(y)...)
	wds = append(wds, be(w)...)
	wds = append(wds, be(h)...)
	pds := []byte{0, 0, 1, 235, 128, 128, 255, 2, 16, 128, 128, 0} // entry 1 opaque, entry 2 clear
	rle := encodeRLE(pix, w, h)
	ods := append([]byte{0, 0, 0, 0x80}, byte((len(rle)+4)>>16), byte((len(rle)+4)>>8), byte(len(rle)+4))
	ods = append(ods, be(w)...)
	ods = append(ods, be(h)...)
	// A large object goes over several segments: 30000 bytes of data in the
	// first, 40000 in each after it.
	n := min(len(rle), 30000)
	odss := [][]byte{append(ods, rle[:n]...)}
	for rle = rle[n:]; len(rle) > 0; rle = rle[n:] {
		n = min(len(rle), 40000)
		odss = append(odss, append([]byte{0, 0, 0, 0}, rle[:n]...))
	}
	odss[len(odss)-1][3] |= 0x40
	out := appendSegment(nil, segPCS, pcs)
	out = appendSegment(out, segWDS, wds)
	out = appendSegment(out, segPDS, pds)
	for _, o := range odss {
		out = appendSegment(out, segODS, o)
	}
	return appendSegment(out, segEND, nil)
}

type layoutOf struct {
	width   int
	objects [][3]int // window, x, y
	windows [][3]int // id, x, width
	objW    int
	pix     []byte
}

func readLayout(t *testing.T, ds []byte) layoutOf {
	t.Helper()
	types, payloads, err := segments(ds)
	if err != nil {
		t.Fatal(err)
	}
	var l layoutOf
	u16 := func(b []byte) int { return int(binary.BigEndian.Uint16(b)) }
	for i, typ := range types {
		s := payloads[i]
		switch typ {
		case segPCS:
			l.width = u16(s)
			for o := s[11:]; len(o) >= 8; o = o[8:] {
				l.objects = append(l.objects, [3]int{int(o[2]), u16(o[4:]), u16(o[6:])})
			}
		case segWDS:
			for k := range int(s[0]) {
				w := s[1+9*k:]
				l.windows = append(l.windows, [3]int{int(w[0]), u16(w[1:]), u16(w[5:])})
			}
		case segODS:
			l.objW = u16(s[7:])
			pix, err := decodeRLE(s[11:], l.objW, u16(s[9:]))
			if err != nil {
				t.Fatal(err)
			}
			l.pix = pix
		}
	}
	return l
}

// Side by side, the object is in both halves, shifted apart by the offset:
// right in the left eye and left in the right one, toward the viewer.
func TestPGS3DFull(t *testing.T) {
	pix := bytes.Repeat([]byte{1}, 100*10)
	src := &framesSource{frames: [][]byte{displaySet(800, 900, 100, 10, pix)}}
	p := newPGS3D(src, false, func(time.Duration) int { return 6 })
	f, err := p.Next()
	if err != nil {
		t.Fatal(err)
	}
	l := readLayout(t, f.Data)
	if l.width != 3840 {
		t.Errorf("width %d", l.width)
	}
	want := [][3]int{{0, 806, 900}, {rightWindow, 1920 + 794, 900}}
	if len(l.objects) != 2 || l.objects[0] != want[0] || l.objects[1] != want[1] {
		t.Errorf("objects %v, want %v", l.objects, want)
	}
	if len(l.windows) != 2 || l.windows[0] != [3]int{0, 806, 100} || l.windows[1] != [3]int{rightWindow, 2714, 100} {
		t.Errorf("windows %v", l.windows)
	}
	if l.objW != 100 || !bytes.Equal(l.pix, pix) {
		t.Errorf("the object changed side by side: width %d", l.objW)
	}
	if p.Track().Name != "3D" {
		t.Errorf("track named %q", p.Track().Name)
	}

	// At the plane's edge the shift stops there, in each eye.
	src = &framesSource{frames: [][]byte{displaySet(1815, 900, 100, 10, pix)}}
	p = newPGS3D(src, false, func(time.Duration) int { return 20 })
	f, _ = p.Next()
	if l := readLayout(t, f.Data); l.objects[0][1] != 1820 || l.objects[1][1] != 1920+1795 {
		t.Errorf("at the edge: %v", l.objects)
	}
}

// Half side by side, the object is squeezed: each pair of pixels keeps its
// more opaque one, so a one-pixel line survives.
func TestPGS3DHalf(t *testing.T) {
	const w, h = 9, 2
	pix := []byte{
		2, 1, 2, 2, 1, 2, 2, 2, 1, // opaque pixels at odd and even columns
		1, 1, 1, 1, 1, 1, 1, 1, 1,
	}
	src := &framesSource{frames: [][]byte{displaySet(800, 900, w, h, pix)}}
	p := newPGS3D(src, true, func(time.Duration) int { return 6 })
	f, err := p.Next()
	if err != nil {
		t.Fatal(err)
	}
	l := readLayout(t, f.Data)
	if l.width != 1920 || l.objW != 5 {
		t.Fatalf("width %d, object width %d", l.width, l.objW)
	}
	if want := []byte{1, 2, 1, 2, 1, 1, 1, 1, 1, 1}; !bytes.Equal(l.pix, want) {
		t.Errorf("squeezed %v, want %v", l.pix, want)
	}
	if l.objects[0] != [3]int{0, 403, 900} || l.objects[1] != [3]int{rightWindow, 960 + 397, 900} {
		t.Errorf("objects %v", l.objects)
	}
}

// An object split over several segments is gathered, squeezed and split
// again, each segment within a segment's size.
func TestPGS3DLargeObject(t *testing.T) {
	const w, h = 1800, 120
	r := rand.New(rand.NewPCG(7, 8))
	pix := make([]byte, w*h)
	for i := range pix {
		pix[i] = byte(1 + r.IntN(2))
	}
	ds := displaySet(60, 900, w, h, pix)
	if types, _, _ := segments(ds); bytes.Count(types, []byte{segODS}) < 3 {
		t.Fatalf("the object should span several segments: %v", types)
	}
	p := newPGS3D(&framesSource{frames: [][]byte{ds}}, true, func(time.Duration) int { return 0 })
	f, err := p.Next()
	if err != nil {
		t.Fatal(err)
	}
	types, payloads, err := segments(f.Data)
	if err != nil {
		t.Fatal(err)
	}
	var data []byte
	var nw, nh int
	for i, typ := range types {
		if typ != segODS {
			continue
		}
		s := payloads[i]
		if s[3]&0x80 != 0 {
			nw, nh = int(binary.BigEndian.Uint16(s[7:])), int(binary.BigEndian.Uint16(s[9:]))
			data = append(data, s[11:]...)
		} else {
			data = append(data, s[4:]...)
		}
	}
	got, err := decodeRLE(data, nw, nh)
	if err != nil || nw != w/2 || nh != h {
		t.Fatalf("%dx%d: %v", nw, nh, err)
	}
	for y := range h {
		for x := range nw {
			a, b := pix[y*w+2*x], pix[y*w+2*x+1]
			if want := max(a, b); want == 1 && got[y*nw+x] != 1 || want == 2 && a == 2 && b == 2 && got[y*nw+x] != 2 {
				t.Fatalf("pixel %d,%d: %d from %d,%d", x, y, got[y*nw+x], a, b)
			}
		}
	}
}

// framesSource gives fixed display sets.
type framesSource struct{ frames [][]byte }

func (s *framesSource) Track() mkv.Track {
	return mkv.Track{Type: mkv.TypeSubtitle, CodecID: "S_HDMV/PGS"}
}

func (s *framesSource) Next() (mkv.Frame, error) {
	if len(s.frames) == 0 {
		return mkv.Frame{}, io.EOF
	}
	f := s.frames[0]
	s.frames = s.frames[1:]
	return mkv.Frame{Data: f}, nil
}

// supFile writes display sets as a .sup, each at its time.
func supFile(t *testing.T, path string, at []time.Duration, sets [][]byte) {
	t.Helper()
	var out []byte
	for i, ds := range sets {
		types, payloads, err := segments(ds)
		if err != nil {
			t.Fatal(err)
		}
		pts := uint32(at[i] * 90000 / time.Second) //nolint:gosec // test times
		for k, typ := range types {
			out = append(out, 'P', 'G')
			out = binary.BigEndian.AppendUint32(out, pts)
			out = binary.BigEndian.AppendUint32(out, pts)
			out = appendSegment(out, typ, payloads[k])
		}
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

// With --subs-3d both, a subtitle track is muxed flat and then in 3D, the
// 3D one at the depth its offset sequence gives at each display set.
func TestMuxSubtitles3D(t *testing.T) {
	for _, mode := range []Subs3D{Subs3DOff, Subs3DOn, Subs3DBoth} {
		tmp := t.TempDir()
		video := filepath.Join(tmp, "v.264")
		if err := os.WriteFile(video, readFixture(t, fixtureDir(t), "mvc_base.264"), 0o600); err != nil {
			t.Fatal(err)
		}
		sup := filepath.Join(tmp, "s.sup")
		pix := bytes.Repeat([]byte{1}, 50*4)
		supFile(t, sup, []time.Duration{100 * time.Millisecond, 200 * time.Millisecond},
			[][]byte{displaySet(800, 900, 50, 4, pix), displaySet(800, 900, 50, 4, pix)})
		o := DefaultOptions()
		o.Output, o.Subs3D = filepath.Join(tmp, "out.mkv"), mode
		r := NewRunner(CurrentGOOS, o, nil)
		r.fpsNum, r.fpsDen = 25, 1
		r.depth = &depthMap{}
		r.depth.add(0, offsetGOP{offsets: [][]int8{{0}, {3, 3, 3, 3, 3, 9, 9}}})
		r.offsetSequence = func(Track) int { return 1 }
		err := r.muxBuiltin(t.Context(), []string{video}, []extra{
			{path: sup, track: Track{ID: 4608, StreamID: "S_HDMV/PGS", Type: "PGS", Lang: "eng"}},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(o.Output) //nolint:gosec // test
		if err != nil {
			t.Fatal(err)
		}
		rd, err := mkv.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		var names, defaults []string
		subs := map[uint64]string{}
		for _, tr := range rd.Tracks {
			if tr.Type == mkv.TypeSubtitle {
				names = append(names, tr.Name)
				subs[tr.Number] = tr.Name
				defaults = append(defaults, fmt.Sprint(tr.Default))
			}
		}
		// With both, the flat track is the default, so a player that
		// places subtitles in 3D itself does not take the 3D one.
		wantDefaults := map[Subs3D]string{Subs3DOff: "false", Subs3DOn: "false", Subs3DBoth: "true false"}[mode]
		if strings.Join(defaults, " ") != wantDefaults {
			t.Errorf("%s: default flags %v, want %s", mode, defaults, wantDefaults)
		}
		want := map[Subs3D]string{Subs3DOff: "[]", Subs3DOn: "[3D]", Subs3DBoth: "[ 3D]"}[mode]
		if fmt.Sprint(names) != want {
			t.Errorf("%s: subtitle tracks %q, want %s", mode, names, want)
		}
		var lefts []int
		for {
			p, err := rd.Next()
			if err != nil {
				break
			}
			if subs[p.Track] == "3D" {
				lefts = append(lefts, readLayout(t, p.Data).objects[0][1])
			}
		}
		_ = f.Close()
		if mode.threeD() && fmt.Sprint(lefts) != "[803 809]" {
			t.Errorf("%s: left eye x %v, want the offsets 3 then 9 applied", mode, lefts)
		}
	}
}

func TestSubs3DValidation(t *testing.T) {
	for v, ok := range map[Subs3D]bool{"": true, Subs3DOff: true, Subs3DOn: true, Subs3DBoth: true, "yes": false} {
		if err := opts("linux", func(o *Options) { o.Subs3D = v }).Validate("linux"); (err == nil) != ok {
			t.Errorf("%q: %v", v, err)
		}
	}
	o := opts("linux", func(o *Options) { o.Remux, o.Output, o.Subs3D = true, "/out/a.m2ts", Subs3DOn })
	if err := o.Validate("linux"); err == nil {
		t.Error("--subs-3d with --remux was accepted")
	}
}

// Whatever a display set holds, rewriting it fails or succeeds; it never
// panics.
func FuzzPGS3D(f *testing.F) {
	f.Add(displaySet(800, 900, 9, 2, bytes.Repeat([]byte{1}, 18)), false)
	f.Add(displaySet(10, 10, 3, 3, []byte{1, 2, 1, 2, 1, 2, 1, 2, 1}), true)
	f.Fuzz(func(t *testing.T, ds []byte, half bool) {
		p := newPGS3D(&framesSource{frames: [][]byte{ds, ds}}, half, func(time.Duration) int { return 7 })
		for range 2 {
			if _, err := p.Next(); err != nil {
				return
			}
		}
	})
}

// Offset metadata parsing takes any bytes.
func FuzzOffsetMetadata(f *testing.F) {
	payload := ofmdPayload(12345, [][]int8{{1, 2}, {3, 4}})
	f.Add(nal(6, append(seiMessage(37, append([]byte{0x40}, seiMessage(5, payload)...)), 0x80)))
	f.Fuzz(func(t *testing.T, au []byte) {
		_, _ = parseOffsetMetadata(au)
	})
}
