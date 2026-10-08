package dovi

import (
	"bytes"
	"testing"
)

// The record of a UHD Blu-ray's 4K, 23.976 fps profile 7 stream, as ffmpeg
// writes it into Matroska (from a FEL sample).
var uhdRecord = []byte{0x01, 0x00, 0x0e, 0x37, 0x60, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}

func TestConfigRoundTrip(t *testing.T) {
	c := UHDBluRay(3840, 2160, 24000, 1001)
	if got := c.Bytes(); !bytes.Equal(got, uhdRecord) {
		t.Fatalf("Bytes = % x, want % x", got, uhdRecord)
	}
	p, err := ParseConfig(uhdRecord)
	if err != nil || p != c {
		t.Fatalf("ParseConfig = %+v, %v; want %+v", p, err, c)
	}
	if c.FourCC() != "dvcC" {
		t.Errorf("FourCC = %s", c.FourCC())
	}
}

func TestLevel(t *testing.T) {
	for _, tc := range []struct{ w, h, num, den, want int }{
		{1920, 1080, 24000, 1001, 3},
		{1920, 1080, 60, 1, 5},
		{3840, 2160, 24000, 1001, 6},
		{3840, 2160, 30, 1, 7},
		{3840, 2160, 60, 1, 9},
	} {
		if got := Level(tc.w, tc.h, tc.num, tc.den); got != tc.want {
			t.Errorf("Level(%dx%d@%d/%d) = %d, want %d", tc.w, tc.h, tc.num, tc.den, got, tc.want)
		}
	}
}

func TestMerge(t *testing.T) {
	sc := []byte{0, 0, 0, 1}
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	bl := cat(sc, []byte{35 << 1, 1, 0x50}, sc, []byte{1 << 1, 1, 0xaa})
	el := cat(sc, []byte{35 << 1, 1, 0x50}, sc, []byte{33 << 1, 1, 0xbb}, sc, []byte{1 << 1, 1, 0xcc},
		sc, []byte{62 << 1, 1, 0xdd})
	want := cat(bl, sc, []byte{0x7e, 1, 33 << 1, 1, 0xbb}, sc, []byte{0x7e, 1, 1 << 1, 1, 0xcc},
		sc, []byte{62 << 1, 1, 0xdd})
	got := Merge(bl, el)
	if !bytes.Equal(got, want) {
		t.Fatalf("Merge =\n% x\nwant\n% x", got, want)
	}
	if !HasRPU(got) || HasRPU(bl) {
		t.Error("HasRPU wrong")
	}
	// And back: the enhancement layer as the disc carries it, less its
	// access unit delimiter.
	wantEL := cat(sc, []byte{33 << 1, 1, 0xbb}, sc, []byte{1 << 1, 1, 0xcc}, sc, []byte{62 << 1, 1, 0xdd})
	if el := SplitEL(got); !bytes.Equal(el, wantEL) {
		t.Errorf("SplitEL =\n% x\nwant\n% x", el, wantEL)
	}
	if SplitEL(bl) != nil {
		t.Error("an enhancement layer out of a base layer")
	}
}
