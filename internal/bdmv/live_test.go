package bdmv

import (
	"bytes"
	"io"
	"testing"

	"github.com/brunoga/bdtools/m2ts"
)

// TestLive writes a few frames and reads them back: the program names the
// video, the frames come whole with their times, a late joiner starts at
// a keyframe with the parameter sets ahead of it.
func TestLive(t *testing.T) {
	sps := []byte{0, 0, 0, 1, 0x67, 1, 2, 3}
	pps := []byte{0, 0, 0, 1, 0x68, 4}
	idr := func(fill byte) []byte { return append([]byte{0, 0, 1, 0x65}, bytes.Repeat([]byte{fill}, 500)...) }
	p := func(fill byte) []byte { return append([]byte{0, 0, 1, 0x41}, bytes.Repeat([]byte{fill}, 300)...) }
	first := append(append(append([]byte(nil), sps...), pps...), idr(1)...)
	frames := [][]byte{first, p(2), p(3), idr(4), p(5)}

	var buf bytes.Buffer
	l := &Live{W: &buf, Coding: CodingAVC, FromKey: true}
	pts := int64(90000)
	for i, f := range frames {
		if i == 0 {
			// what a late joiner sees first: dropped until a keyframe
			if ok, err := l.Frame(p(9), pts); err != nil || ok {
				t.Fatalf("a P-frame before any keyframe: written %v, %v", ok, err)
			}
		}
		if ok, err := l.Frame(f, pts); err != nil || !ok {
			t.Fatalf("frame %d: written %v, %v", i, ok, err)
		}
		pts += 3754
	}
	if buf.Len()%188 != 0 {
		t.Fatalf("%d bytes: not whole packets", buf.Len())
	}
	rd := m2ts.NewReader(bytes.NewReader(buf.Bytes()))
	prog, err := rd.ReadProgram()
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := prog.Stream(LiveVideoPID); !ok || s.Type != CodingAVC {
		t.Fatalf("program: %+v", prog)
	}
	rd.Select(LiveVideoPID)
	var got [][]byte
	var times []int64
	for {
		pes, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, append([]byte(nil), pes.Payload...))
		times = append(times, pes.PTS)
	}
	if len(got) != len(frames) {
		t.Fatalf("%d frames back, want %d", len(got), len(frames))
	}
	for i, f := range frames {
		want := f
		if i == 3 {
			// the second keyframe gets the parameter sets of the first
			want = append(append(append([]byte(nil), sps...), pps...), f...)
		}
		if !bytes.Equal(got[i], want) {
			t.Errorf("frame %d: %d bytes back, want %d", i, len(got[i]), len(want))
		}
		if times[i] != 90000+int64(i)*3754 {
			t.Errorf("frame %d at %d", i, times[i])
		}
	}
}

// TestLiveLateParams: a writer that joins late, given the stream's
// parameter sets, puts them ahead of its first keyframe.
func TestLiveLateParams(t *testing.T) {
	sps := []byte{0, 0, 0, 1, 0x67, 1}
	idr := []byte{0, 0, 1, 0x65, 9, 9, 9}
	var buf bytes.Buffer
	l := &Live{W: &buf, Coding: CodingAVC, FromKey: true, Params: sps}
	if ok, err := l.Frame(idr, 90000); err != nil || !ok {
		t.Fatal(ok, err)
	}
	rd := m2ts.NewReader(bytes.NewReader(buf.Bytes()))
	if _, err := rd.ReadProgram(); err != nil {
		t.Fatal(err)
	}
	rd.Select(LiveVideoPID)
	pes, err := rd.Next()
	if err != nil {
		t.Fatal(err)
	}
	if want := append(append([]byte(nil), sps...), idr...); !bytes.Equal(pes.Payload, want) {
		t.Fatalf("%x back, want %x", pes.Payload, want)
	}
}

func TestAnnexBKeyHEVC(t *testing.T) {
	vps := []byte{0, 0, 0, 1, 0x40, 1, 9}
	sps := []byte{0, 0, 0, 1, 0x42, 1, 9}
	cra := []byte{0, 0, 1, 0x2a, 1, 7, 7}
	trail := []byte{0, 0, 1, 0x02, 1, 7}
	au := append(append(append([]byte(nil), vps...), sps...), cra...)
	key, params := AnnexBKey(CodingHEVC, au)
	if !key || !bytes.Equal(params, append(append([]byte(nil), vps...), sps...)) {
		t.Fatalf("CRA with parameter sets: key %v, params %x", key, params)
	}
	if key, params := AnnexBKey(CodingHEVC, trail); key || params != nil {
		t.Fatalf("a trailing picture: key %v, params %x", key, params)
	}
}
