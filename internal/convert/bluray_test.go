package convert

import (
	"bytes"
	"testing"
)

// An MVC access unit (as the encoder here writes it: the base view's
// units, the MVC parameter sets after its own and ahead of its slices,
// the delimiter and the dependent view's units) splits into a disc's two
// views.
func TestSplitMVC(t *testing.T) {
	nal := func(b ...byte) []byte { return b }
	au := [][]byte{nal(7, 1), nal(8, 1), nal(6, 1), nal(15, 2), nal(8, 2), nal(14, 1), nal(5, 1), nal(24, 3), nal(6, 3), nal(20, 3)}
	base, dep := splitMVC(au)
	sc := func(ns ...[]byte) []byte {
		var b []byte
		for _, n := range ns {
			b = append(append(b, 0, 0, 0, 1), n...)
		}
		return b
	}
	if want := sc(nal(9, 0xf0), nal(7, 1), nal(8, 1), nal(6, 1), nal(5, 1)); !bytes.Equal(base, want) {
		t.Errorf("the base view %x, want %x", base, want)
	}
	if want := sc(nal(15, 2), nal(8, 2), nal(24, 3), nal(6, 3), nal(20, 3)); !bytes.Equal(dep, want) {
		t.Errorf("the dependent view %x, want %x", dep, want)
	}
}

// The SSIF's extents: each pair starts at the same time in both clips,
// the base view's about extentBytes long.
func TestInterleave(t *testing.T) {
	var base, dep []int64
	for i := range 20000 {
		base = append(base, int64(i)*100)
	}
	for i := range 7000 {
		dep = append(dep, int64(i)*290)
	}
	bc, dc := interleave(base, dep)
	per := extentBytes / 6144
	if len(bc) != len(dc) || len(bc) != (20000+per-1)/per || bc[0] != 0 || dc[0] != 0 {
		t.Fatalf("cuts %v / %v", bc, dc)
	}
	for k := 1; k < len(bc); k++ {
		if bc[k]-bc[k-1] != per || dep[dc[k]] < base[bc[k]] || dc[k] > 0 && dep[dc[k]-1] >= base[bc[k]] {
			t.Fatalf("cut %d: base unit %d (at %d), dependent %d (at %d)", k, bc[k], base[bc[k]], dc[k], dep[dc[k]])
		}
	}
}

// A disc image is MVC: its default layout, and refused with another, or
// for a film that stays 2D.
func TestDiscImageOutput(t *testing.T) {
	ok := Options{Input: "in.iso", Output: "film.iso", Layout: LayoutMVC, Codec: CodecH264, Encoder: EncoderBuiltin, BitDepth: 8, CRF: 20}
	if err := ok.Validate(CurrentGOOS); err != nil {
		t.Errorf("an MVC disc image refused: %v", err)
	}
	sbs := ok
	sbs.Layout = LayoutFullSBS
	if err := sbs.Validate(CurrentGOOS); err == nil {
		t.Error("a side-by-side disc image accepted")
	}
	if !IsDiscImage("Film (2024).ISO") || IsDiscImage("film.mkv") {
		t.Error("IsDiscImage")
	}
	if _, _, err := discVideoFormat(1080, 25, 1); err == nil {
		t.Error("1080p25 accepted for Blu-ray 3D")
	}
	if f, r, err := discVideoFormat(1080, 24000, 1001); err != nil || f != 6 || r != 1 {
		t.Errorf("1080p23.976: %d %d %v", f, r, err)
	}
}
