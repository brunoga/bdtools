package mkv

import (
	"bytes"
	"testing"
)

// An MVC stream's dependent view PPS refers to a subset SPS: neither the
// avcC nor the parameter sets put ahead of a keyframe carry it.
func TestAVCCSkipsDependentViewPPS(t *testing.T) {
	sps := []byte{0x67, 0x64, 0x00, 0x29, 0xac, 0xd1, 0x40, 0x78, 0x02, 0x27, 0xe5, 0xc0, 0x44, 0x00, 0x00, 0x0f, 0xa4, 0x00, 0x02, 0xee, 0x02, 0x3c, 0x60, 0xc4, 0x58}
	pps := []byte{0x68, 0xe9, 0x20, 0x6c, 0xb2, 0x2c}
	depPPS := []byte{0x68, 0x4a, 0xe0, 0xfc, 0x30} // id 1, SPS 1
	var p paramSets
	p.addH264SPS(sps)
	p.addH264PPS(pps)
	p.addH264PPS(depPPS)
	if len(p.ppsList) != 2 {
		t.Fatalf("%d PPSs seen, want 2", len(p.ppsList))
	}
	c, err := p.avcC()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(c, depPPS) || !bytes.Contains(c, pps) {
		t.Errorf("avcC % x: want the base PPS only", c)
	}
	raw := p.raw(H264)
	if len(raw) != 2 || !bytes.Equal(raw[0], sps) || !bytes.Equal(raw[1], pps) {
		t.Errorf("parameter sets %x: want the SPS and the base PPS", raw)
	}
}
