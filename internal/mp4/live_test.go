package mp4

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestLive writes a header and two frames and reads the boxes back.
func TestLive(t *testing.T) {
	sps := []byte{0x67, 0x64, 0x00, 0x33, 1, 2}
	pps := []byte{0x68, 0xee}
	var buf bytes.Buffer
	l := &Live{W: &buf}
	if err := l.Init(sps, pps, 3840, 1080); err != nil {
		t.Fatal(err)
	}
	idr := bytes.Join([][]byte{{0, 0, 0, 1}, sps, {0, 0, 0, 1}, pps, {0, 0, 1, 0x65, 9, 9, 9}}, nil)
	p := []byte{0, 0, 1, 0x41, 7, 7}
	if err := l.Frame(idr, 90000, true); err != nil {
		t.Fatal(err)
	}
	if err := l.Frame(p, 90000+3754, false); err != nil {
		t.Fatal(err)
	}
	bs := children(buf.Bytes())
	var types []string
	for _, b := range bs {
		types = append(types, b.typ)
	}
	want := []string{"ftyp", "moov", "moof", "mdat", "moof", "mdat"}
	if len(types) != len(want) {
		t.Fatalf("boxes %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("boxes %v, want %v", types, want)
		}
	}
	moov := children(bs[1].body)
	if find(moov, "mvex") == nil || find(children(find(moov, "trak")), "mdia") == nil {
		t.Fatal("the movie lacks its track or mvex")
	}
	// the first sample: the IDR alone, length-prefixed, no parameter sets
	if mdat := bs[3].body; !bytes.Equal(mdat, []byte{0, 0, 0, 4, 0x65, 9, 9, 9}) {
		t.Fatalf("the first sample: %x", mdat)
	}
	// the data offset of each trun points at its mdat's data
	for _, i := range []int{2, 4} {
		traf := children(find(children(bs[i].body), "traf"))
		trun := find(traf, "trun")
		off := int(binary.BigEndian.Uint32(trun[8:12])) // after the version, flags and count
		if off != len(bs[i].body)+8+8 {
			t.Errorf("fragment %d: data offset %d, want %d", i, off, len(bs[i].body)+16)
		}
	}
	if c := AVCCodec(sps); c != "avc1.640033" {
		t.Errorf("codec %s", c)
	}
	nals := SplitAnnexB(idr)
	if len(nals) != 3 || nals[2][0] != 0x65 {
		t.Errorf("split: %x", nals)
	}
}
