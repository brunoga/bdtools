package mvc

import (
	"math/rand"
	"testing"
)

// treeTestFrame is a 128x64 frame: a fixed textured background (seed
// bg) with a bright 16x16 square at x.
func treeTestFrame(bg int64, x int) *DepSource {
	const w, h = 128, 64
	s := &DepSource{Y: make([]byte, w*h), Cb: make([]byte, w*h/4), Cr: make([]byte, w*h/4), StrideY: w, StrideC: w / 2}
	rng := rand.New(rand.NewSource(bg))
	for i := range s.Y {
		s.Y[i] = byte(64 + rng.Intn(128))
	}
	for i := range s.Cb {
		s.Cb[i], s.Cr[i] = 128, 128
	}
	for y := 24; y < 40; y++ {
		for i := x; i < x+16 && i < w; i++ {
			s.Y[y*w+i] = 250
		}
	}
	return s
}

// A frame unlike the one before starts a new scene: it is coded as an
// IDR picture, and the frames before it end their group.
func TestSceneCut(t *testing.T) {
	e, err := NewBaseEncoder(BaseOptions{Width: 128, Height: 64, QP: 26, BFrames: 3})
	if err != nil {
		t.Fatal(err)
	}
	var out []byte
	for i := range 30 {
		bg := int64(1)
		if i >= 13 {
			bg = 2
		}
		aus, err := e.Encode(treeTestFrame(bg, 4+i*2), int64(i))
		if err != nil {
			t.Fatal(err)
		}
		for _, au := range aus {
			out = append(out, au...)
		}
	}
	aus, err := e.Flush()
	if err != nil {
		t.Fatal(err)
	}
	for _, au := range aus {
		out = append(out, au...)
	}
	idr := 0
	_ = forEachNAL(out, func(nal []byte) error {
		if len(nal) > 0 && nal[0]&31 == nalSliceIDR {
			idr++
		}
		return nil
	})
	if idr != 2 {
		t.Errorf("%d IDR pictures, want 2 (the first frame and the cut)", idr)
	}
}

// The macroblock tree lowers the quantiser where the pictures after
// predict from: a still background, against a square that moves.
func TestMBTreeOffsets(t *testing.T) {
	e, err := NewBaseEncoder(BaseOptions{Width: 128, Height: 64, QP: 26, BFrames: 3})
	if err != nil {
		t.Fatal(err)
	}
	for i := range treeWindow {
		f := &baseFrame{src: treeTestFrame(1, 4+i*2), index: i}
		e.analyse(f)
		e.queue = append(e.queue, f)
	}
	e.buildTree()
	f := e.queue[0] // the IDR picture
	if len(f.treeQP) != 8*4 {
		t.Fatalf("%d offsets, want 32", len(f.treeQP))
	}
	still := f.treeQP[0]
	if still > -1 {
		t.Errorf("the still background's offset %.2f, want well below 0", still)
	}
	for _, v := range f.treeQP {
		if v > 0 {
			t.Errorf("an offset above 0: %.2f", v)
		}
	}
}
