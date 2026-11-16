package sam

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brunoga/bdtools/internal/cuda"
	"github.com/brunoga/bdtools/internal/nn"
)

// The blocks are Hiera tiny's: its widths, heads, windows and pooling.
func TestBlocks(t *testing.T) {
	bs := blocks()
	if len(bs) != 12 {
		t.Fatalf("%d blocks", len(bs))
	}
	want := []block{
		{96, 96, 1, 8, false, true},
		{96, 192, 2, 8, true, false}, {192, 192, 2, 4, false, true},
		{192, 384, 4, 4, true, false}, {384, 384, 4, 14, false, false}, {384, 384, 4, 0, false, false},
		{384, 384, 4, 14, false, false}, {384, 384, 4, 0, false, false}, {384, 384, 4, 14, false, false},
		{384, 384, 4, 0, false, true},
		{384, 768, 8, 14, true, false}, {768, 768, 8, 7, false, true},
	}
	for i, b := range bs {
		if b != want[i] {
			t.Errorf("block %d: %+v, want %+v", i, b, want[i])
		}
	}
}

// The model gives the PyTorch reference's activations (SAM_CHECK, the
// directory ~/mvc-scratch/sam/ref.py wrote, and SAM_MODEL, the
// checkpoint), on the CPU and on a GPU.
func TestAgainstReference(t *testing.T) {
	dir, file := os.Getenv("SAM_CHECK"), os.Getenv("SAM_MODEL")
	if dir == "" || file == "" {
		t.Skip("SAM_CHECK and SAM_MODEL not set")
	}
	m, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	var shapes map[string][]int
	b, err := os.ReadFile(filepath.Join(dir, "shapes.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &shapes); err != nil {
		t.Fatal(err)
	}
	ref := func(name string) *nn.Tensor {
		b, err := os.ReadFile(filepath.Join(dir, name+".raw"))
		if err != nil {
			t.Fatal(err)
		}
		x := nn.New(shapes[name]...)
		for i := range x.Data {
			x.Data[i] = math.Float32frombits(uint32(b[4*i]) | uint32(b[4*i+1])<<8 | uint32(b[4*i+2])<<16 | uint32(b[4*i+3])<<24)
		}
		return x
	}
	// close is the largest difference relative to the reference's spread
	close := func(name string, got *nn.Tensor, want *nn.Tensor, tol float64) {
		t.Helper()
		g := got
		if got.Data == nil {
			if g, err = m.be.Download(got); err != nil {
				t.Fatal(err)
			}
		}
		if len(g.Data) != len(want.Data) {
			t.Fatalf("%s: %v, want %v", name, g.Shape, want.Shape)
		}
		var ss, worst float64
		for _, v := range want.Data {
			ss += float64(v) * float64(v)
		}
		rms := math.Sqrt(ss / float64(len(want.Data)))
		for i, v := range want.Data {
			worst = max(worst, math.Abs(float64(g.Data[i]-v)))
		}
		t.Logf("%s: largest difference %.2g, %.2g of its rms %.3g", name, worst, worst/rms, rms)
		if worst > tol*rms {
			t.Errorf("%s: largest difference %.3g, more than %g of its rms %.3g", name, worst, tol, rms)
		}
	}
	pts := ref("points").Data
	box := ref("box").Data
	// the predictor's frame is the picture's, scaled to the network's
	const w, h = 1920, 1080
	sx, sy := float32(Size)/w, float32(Size)/h
	// the predictor gives its logits clamped to ±32
	clamp := func(x []float32) *nn.Tensor {
		y := nn.New(len(x))
		for i, v := range x {
			y.Data[i] = min(max(v, -32), 32)
		}
		return y
	}
	run := func(be string) {
		start := time.Now()
		f := m.Encode(m.be.Upload(ref("input")))
		for i := range 4 {
			close(be+" stage", f.Stages[i], ref([]string{"trunk0", "trunk1", "trunk2", "trunk3"}[i]), 2e-3)
		}
		// the reference's without the decoder's no-mask embedding, which
		// Embed has
		embed := ref("image_embed")
		for i := range embed.Data {
			embed.Data[i] += m.w["sam_prompt_encoder.no_mask_embed.weight"].Data[i/(grid*grid)]
		}
		close(be+" embed", f.Embed, embed, 2e-3)
		close(be+" high0", f.High0, ref("high_res0"), 2e-3)
		close(be+" high1", f.High1, ref("high_res1"), 2e-3)
		t.Logf("%s: encoded in %v", be, time.Since(start))
		start = time.Now()
		bx := &Box{box[0] * sx, box[1] * sy, box[2] * sx, box[3] * sy}
		masks, obj, err := m.Decode(f, bx, []Point{{pts[0] * sx, pts[1] * sy, true}, {pts[2] * sx, pts[3] * sy, false}}, true)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: decoded in %v, object %.2f", be, time.Since(start), obj)
		want, ious := ref("low_masks"), ref("ious")
		for i, mk := range masks {
			n := len(mk.Logits)
			close(be+" mask", clamp(mk.Logits), &nn.Tensor{Shape: []int{n}, Data: want.Data[i*n : (i+1)*n]}, 5e-3)
			if math.Abs(float64(mk.IoU-ious.Data[i])) > 2e-3 {
				t.Errorf("%s: mask %d's IoU %.4f, want %.4f", be, i, mk.IoU, ious.Data[i])
			}
		}
		masks, _, err = m.Decode(f, nil, []Point{{pts[0] * sx, pts[1] * sy, true}}, true)
		if err != nil {
			t.Fatal(err)
		}
		want, ious = ref("low_point"), ref("ious_point")
		for i, mk := range masks {
			n := len(mk.Logits)
			close(be+" point mask", clamp(mk.Logits), &nn.Tensor{Shape: []int{n}, Data: want.Data[i*n : (i+1)*n]}, 5e-3)
			if math.Abs(float64(mk.IoU-ious.Data[i])) > 2e-3 {
				t.Errorf("%s: point mask %d's IoU %.4f, want %.4f", be, i, mk.IoU, ious.Data[i])
			}
		}
		m.be.Release()
	}
	run("cpu")
	g, err := nn.NewGPU()
	if errors.Is(err, cuda.ErrUnavailable) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	m.Use(g)
	run("gpu")
	run("gpu")
}

// A white picture is white in every channel of the input, normalised.
func TestInputWhite(t *testing.T) {
	w, h := 64, 36
	y := make([]byte, w*h)
	c := make([]byte, w/2*h/2)
	for i := range y {
		y[i] = 235
	}
	for i := range c {
		c[i] = 128
	}
	x := Input(y, c, c, w, h, w, w/2)
	mean := [3]float32{0.485, 0.456, 0.406}
	sd := [3]float32{0.229, 0.224, 0.225}
	for ch := range 3 {
		want := (1 - mean[ch]) / sd[ch]
		for _, i := range []int{0, Size*Size/2 + 17, Size*Size - 1} {
			if got := x.Data[ch*Size*Size+i]; math.Abs(float64(got-want)) > 1e-4 {
				t.Fatalf("channel %d at %d: %g, want %g", ch, i, got, want)
			}
		}
	}
}
