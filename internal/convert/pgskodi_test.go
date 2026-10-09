package convert

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/brunoga/bdtools/internal/mkv"
)

// compositor draws display sets as a player does: objects kept by id
// across the epoch, each composition's objects drawn (cropped) on a blank
// plane. The plane holds palette indices, the palettes passing through
// untouched.
type compositor struct {
	objects map[uint16][]byte
	sizes   map[uint16][2]int
	partial map[uint16][]byte
}

func newCompositor() *compositor {
	return &compositor{objects: map[uint16][]byte{}, sizes: map[uint16][2]int{}, partial: map[uint16][]byte{}}
}

// draw takes a display set, returning its picture and how many objects its
// composition lists (widest object width too); nil without a composition.
func (c *compositor) draw(t *testing.T, ds []byte) (plane []byte, objs, widest int) {
	t.Helper()
	types, payloads, err := segments(ds)
	if err != nil {
		t.Fatal(err)
	}
	var pcs []byte
	for i, typ := range types {
		s := payloads[i]
		switch typ {
		case segPCS:
			pcs = s
			if s[7]&0x80 != 0 {
				clear(c.objects)
				clear(c.sizes)
			}
		case segODS:
			id := binary.BigEndian.Uint16(s)
			if s[3]&0x80 != 0 {
				c.sizes[id] = [2]int{int(binary.BigEndian.Uint16(s[7:])), int(binary.BigEndian.Uint16(s[9:]))}
				c.partial[id] = append([]byte(nil), s[11:]...)
			} else {
				c.partial[id] = append(c.partial[id], s[4:]...)
			}
			if s[3]&0x40 != 0 {
				sz := c.sizes[id]
				pix, err := decodeRLE(c.partial[id], sz[0], sz[1])
				if err != nil {
					t.Fatal(err)
				}
				c.objects[id] = pix
			}
		}
	}
	if pcs == nil {
		return nil, 0, 0
	}
	w, h := int(binary.BigEndian.Uint16(pcs)), int(binary.BigEndian.Uint16(pcs[2:]))
	plane = make([]byte, w*h)
	n := int(pcs[10])
	o := pcs[11:]
	for range n {
		size := 8
		if o[3]&0x80 != 0 {
			size = 16
		}
		id := binary.BigEndian.Uint16(o)
		x, y := int(binary.BigEndian.Uint16(o[4:])), int(binary.BigEndian.Uint16(o[6:]))
		sz := c.sizes[id]
		widest = max(widest, sz[0])
		cx, cy, cw, ch := 0, 0, sz[0], sz[1]
		if size == 16 {
			cx, cy = int(binary.BigEndian.Uint16(o[8:])), int(binary.BigEndian.Uint16(o[10:]))
			cw, ch = int(binary.BigEndian.Uint16(o[12:])), int(binary.BigEndian.Uint16(o[14:]))
		}
		pix := c.objects[id]
		for r := range ch {
			for k := range cw {
				if v := pix[(cy+r)*sz[0]+cx+k]; v != 0 && y+r < h && x+k < w {
					plane[(y+r)*w+x+k] = v
				}
			}
		}
		o = o[size:]
	}
	return plane, n, widest
}

// sliceSource gives display sets from memory.
type sliceSource struct{ sets [][]byte }

func (s *sliceSource) Track() mkv.Track {
	return mkv.Track{Type: mkv.TypeSubtitle, CodecID: "S_HDMV/PGS"}
}

func (s *sliceSource) Next() (mkv.Frame, error) {
	if len(s.sets) == 0 {
		return mkv.Frame{}, io.EOF
	}
	d := s.sets[0]
	s.sets = s.sets[1:]
	return mkv.Frame{Data: d}, nil
}

// checkKodi runs sets through kodiPGS and checks each composition draws as
// before, with no object wider than half the plane and at most two
// objects; it returns how many sets changed.
func checkKodi(t *testing.T, sets [][]byte) int {
	t.Helper()
	k := newKodiPGS(&sliceSource{sets: append([][]byte(nil), sets...)})
	before, after := newCompositor(), newCompositor()
	changed := 0
	for i, ds := range sets {
		f, err := k.Next()
		if err != nil {
			t.Fatalf("set %d: %v", i, err)
		}
		if !bytes.Equal(f.Data, ds) {
			changed++
		}
		want, _, _ := before.draw(t, ds)
		got, n, widest := after.draw(t, f.Data)
		if !bytes.Equal(got, want) {
			t.Fatalf("set %d draws otherwise once split", i)
		}
		if got != nil && (n > 2 || widest > len(got)/1080/2) {
			t.Fatalf("set %d: %d objects, the widest %d", i, n, widest)
		}
	}
	return changed
}

// wideSet builds an epoch start showing one object of w x h at (x, y),
// its pixels a pattern, its data in segments as long as maxSeg allows.
func wideSet(w, h, x, y int) []byte {
	pix := make([]byte, w*h)
	for i := range pix {
		pix[i] = byte(1 + i*7%13)
		if i%11 == 0 {
			pix[i] = 0
		}
	}
	pcs := []byte{0x07, 0x80, 0x04, 0x38, 0x10, 0, 0, 0x80, 0, 0, 1, 0, 0, 0, 0,
		byte(x >> 8), byte(x), byte(y >> 8), byte(y)}
	wds := []byte{1, 0, byte(x >> 8), byte(x), byte(y >> 8), byte(y), byte(w >> 8), byte(w), byte(h >> 8), byte(h)}
	pds := []byte{0, 0, 1, 0x80, 0x80, 0x80, 0xff}
	ds := appendSegment(nil, segPCS, pcs)
	ds = appendSegment(ds, segWDS, wds)
	ds = appendSegment(ds, segPDS, pds)
	for _, s := range objectSegments([]byte{0, 0, 0}, w, h, encodeRLE(pix, w, h)) {
		ds = appendSegment(ds, segODS, s)
	}
	return appendSegment(ds, segEND, nil)
}

// clearSet ends what is shown, and a set showing object 0 again at another
// place with no object data (a later composition of the epoch's object).
func clearSet() []byte {
	return appendSegment(appendSegment(nil, segPCS, []byte{0x07, 0x80, 0x04, 0x38, 0x10, 0, 1, 0, 0, 0, 0}), segEND, nil)
}

func moveSet(x, y int) []byte {
	pcs := []byte{0x07, 0x80, 0x04, 0x38, 0x10, 0, 2, 0, 0, 0, 1, 0, 0, 0, 0, byte(x >> 8), byte(x), byte(y >> 8), byte(y)}
	return appendSegment(appendSegment(nil, segPCS, pcs), segEND, nil)
}

func TestKodiPGSSplitsWideObjects(t *testing.T) {
	sets := [][]byte{
		wideSet(1068, 145, 427, 786), // wide, its data in two segments
		clearSet(),
		wideSet(531, 64, 695, 863), // narrow: as it is
		clearSet(),
		wideSet(1500, 40, 210, 900), // wide again, then shown elsewhere
		moveSet(100, 700),
		clearSet(),
	}
	// The two with wide objects, and the one showing the second again.
	if n := checkKodi(t, sets); n != 3 {
		t.Errorf("%d sets changed, want 3", n)
	}
}

// A real disc's subtitles ($BDTOOLS_PGS_SAMPLE, a .sup file) draw the same
// once split.
func TestKodiPGSSample(t *testing.T) {
	path := os.Getenv("BDTOOLS_PGS_SAMPLE")
	if path == "" {
		t.Skip("BDTOOLS_PGS_SAMPLE not set")
	}
	f, err := os.Open(path) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // read only
	src := mkv.NewPGSSource(f, "")
	var sets [][]byte
	for {
		fr, err := src.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		sets = append(sets, fr.Data)
	}
	t.Logf("%d of %d display sets split", checkKodi(t, sets), len(sets))
}
