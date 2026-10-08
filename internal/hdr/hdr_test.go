package hdr

import (
	"bytes"
	"reflect"
	"testing"
)

// frankenstein is the static metadata of a real HDR10 stream: a P3 display
// of 1000 cd/m² down to 0.0001, MaxCLL 538, MaxFALL 219.
var frankenstein = Static{
	Mastering: &Mastering{Primaries: [3][2]uint16{{13250, 34500}, {7500, 3000}, {34000, 16000}},
		WhitePoint: [2]uint16{15635, 16450}, MaxLuma: 10000000, MinLuma: 1},
	Light: &LightLevel{MaxCLL: 538, MaxFALL: 219},
}

// What is written reads back, through emulation prevention and among other
// NAL units of an access unit.
func TestHEVCRoundTrip(t *testing.T) {
	t35 := append(append([]byte(nil), hdr10PlusPrefix...), 0, 0, 0, 1, 0, 0, 2, 3) // zeros: needs escaping
	au := bytes.Join([][]byte{
		{0, 0, 0, 1, 0x40, 1, 0xc}, // a VPS
		append([]byte{0, 0, 1}, HEVCStaticSEI(frankenstein)...),
		append([]byte{0, 0, 1}, HEVCT35SEI(t35)...),
		{0, 0, 1, 0x26, 1, 0xaf}, // a slice
	}, nil)
	if !bytes.Contains(HEVCT35SEI(t35), []byte{0, 0, 3}) {
		t.Fatal("the test means the T.35 payload to need emulation prevention")
	}
	md := ParseHEVC(au)
	if !reflect.DeepEqual(md.Static, frankenstein) || !bytes.Equal(md.HDR10Plus, t35) {
		t.Errorf("read back %+v %+v, %x", md.Static.Mastering, md.Static.Light, md.HDR10Plus)
	}
	if HEVCStaticSEI(Static{}) != nil {
		t.Error("an empty SEI for no metadata")
	}
	// Other T.35 messages are not HDR10+.
	if md := ParseHEVC(append([]byte{0, 0, 1}, HEVCT35SEI([]byte{0xb5, 0, 0x31, 0x47, 0x41, 0x39, 0x34})...)); md.HDR10Plus != nil {
		t.Error("ATSC captions taken for HDR10+")
	}
}

// AV1's metadata OBUs: sizes, types, and the units converted.
func TestAV1OBUs(t *testing.T) {
	b := AV1StaticOBUs(frankenstein)
	// CLL: header, size 6, type 1, 4 bytes, trailing.
	if !bytes.Equal(b[:9], []byte{0x2a, 6, 1, 0x02, 0x1a, 0x00, 0xdb, 0x80, 0x2a}) {
		t.Errorf("CLL OBU % x", b[:9])
	}
	mdcv := b[8:]
	if mdcv[1] != 26 || mdcv[2] != 2 {
		t.Fatalf("MDCV OBU header % x", mdcv[:3])
	}
	p := mdcv[3:]
	be := func(i int) int { return int(p[i])<<8 | int(p[i+1]) }
	// Red x 0.68 is 44564.48 in 0.16, 44564; max 1000 cd/m² is 256000 in 24.8.
	if be(0) != 44564 || int(p[16])<<24|int(p[17])<<16|int(p[18])<<8|int(p[19]) != 256000 {
		t.Errorf("red x %d, max %v", be(0), p[16:20])
	}
}
