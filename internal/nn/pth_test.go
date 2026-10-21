package nn

import (
	"math"
	"os"
	"slices"
	"testing"
)

// A state dict torch.save wrote (testdata/state.pth): float32 tensors, a
// strided view (a transpose), float16 and bfloat16 ones, and a value that
// is not a tensor (left out).
func TestLoadPTH(t *testing.T) {
	sd, err := LoadPTH("testdata/state.pth")
	if err != nil {
		t.Fatal(err)
	}
	if len(sd) != 5 {
		t.Errorf("%d tensors, want 5", len(sd))
	}
	w := sd["a.weight"]
	if w == nil || !slices.Equal(w.Shape, []int{3, 4}) || w.Data[5] != 1.25 || w.Data[11] != 2.75 {
		t.Errorf("a.weight %v %v", w, w.Data)
	}
	if b := sd["a.bias"]; b == nil || !slices.Equal(b.Data, []float32{-1.5, 2.25, 1e-3}) {
		t.Errorf("a.bias %v", b.Data)
	}
	// arange(6).reshape(2, 3).t(): [[0 3] [1 4] [2 5]]
	if tr := sd["t"]; tr == nil || !slices.Equal(tr.Shape, []int{3, 2}) || !slices.Equal(tr.Data, []float32{0, 3, 1, 4, 2, 5}) {
		t.Errorf("t %v %v", tr, tr.Data)
	}
	want := []float32{1, -2.5, 65504, 6.002187728881836e-05, 1.1920928955078125e-07, 0}
	if h := sd["h"]; h == nil || !slices.Equal(h.Data, want) {
		t.Errorf("h %v, want %v", h.Data, want)
	}
	if bf := sd["bf"]; bf == nil || !slices.Equal(bf.Data, []float32{1, -3, 0.15625}) {
		t.Errorf("bf %v", bf.Data)
	}
}

// A checkpoint holding its state dict under "state_dict" gives that one.
func TestLoadPTHNested(t *testing.T) {
	sd, err := LoadPTH("testdata/nested.pth")
	if err != nil {
		t.Fatal(err)
	}
	if w := sd["w"]; len(sd) != 1 || w == nil || !slices.Equal(w.Data, []float32{1, 1}) {
		t.Errorf("%v", sd)
	}
}

// Video Depth Anything Small's checkpoint, when $VDA_WEIGHTS names it.
func TestLoadPTHVideoDepthAnything(t *testing.T) {
	file := os.Getenv("VDA_WEIGHTS")
	if file == "" {
		t.Skip("VDA_WEIGHTS unset")
	}
	sd, err := LoadPTH(file)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, v := range sd {
		n += len(v.Data)
	}
	if len(sd) != 351 || n != 29080193 {
		t.Errorf("%d tensors, %d values; want 351, 29080193", len(sd), n)
	}
	if p := sd["pretrained.patch_embed.proj.weight"]; p == nil || !slices.Equal(p.Shape, []int{384, 3, 14, 14}) {
		t.Errorf("patch embed %v", p)
	}
	// the sums and sums of squares PyTorch gives
	for _, c := range []struct {
		name    string
		sum, sq float64
	}{
		{"pretrained.pos_embed", 39.565012771516436, 56.470637363553195},
		{"pretrained.blocks.11.mlp.fc2.weight", -29.671377475159886, 120.57064994110183},
		{"head.scratch.output_conv2.2.weight", -0.33423774503171444, 0.46209330616425187},
		{"head.motion_modules.0.temporal_transformer.proj_in.weight", -11.396720864580857, 74.05335209101628},
	} {
		var s, q float64
		for _, v := range sd[c.name].Data {
			s, q = s+float64(v), q+float64(v)*float64(v)
		}
		if math.Abs(s-c.sum) > 1e-9*max(1, math.Abs(c.sum)) || math.Abs(q-c.sq) > 1e-9*c.sq {
			t.Errorf("%s: sum %v, squares %v; want %v, %v", c.name, s, q, c.sum, c.sq)
		}
	}
}

// A file that is not a checkpoint is an error, not a panic.
func TestLoadPTHNotACheckpoint(t *testing.T) {
	if _, err := LoadPTH("pth.go"); err == nil {
		t.Error("a Go file loaded")
	}
}
