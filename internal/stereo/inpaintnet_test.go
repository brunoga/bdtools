package stereo

import (
	"bytes"
	"math"
	"testing"

	"github.com/brunoga/bdtools/internal/nn"
	"github.com/brunoga/bdtools/mvc"
)

// TestInpaintApply: the input carries the eye and its holes, the
// corrections land in the holes only, the chroma where any of its luma
// is a hole; weights written are read back.
func TestInpaintApply(t *testing.T) {
	w, h := 32, 16
	eye := &mvc.Frame{Width: w, Height: h, StrideY: w, StrideC: w / 2, Y: make([]byte, w*h), Cb: make([]byte, w*h/4), Cr: make([]byte, w*h/4)}
	for i := range eye.Y {
		eye.Y[i] = 100
	}
	for i := range eye.Cb {
		eye.Cb[i], eye.Cr[i] = 120, 130
	}
	holes := make([]bool, w*h)
	holes[5*w+10] = true
	x := InpaintInput(eye, holes, 0, h)
	pw := pad8(w)
	if got := x.Data[3*pad8(h)*pw+5*pw+10]; got != 1 || x.Data[3*pad8(h)*pw+5*pw+11] != 0 {
		t.Fatalf("the mask: %g", got)
	}
	if got := x.Data[5*pw+10]; math.Abs(float64(got)-((100-16)/219.0-0.5)) > 1e-6 {
		t.Fatalf("the luma: %g", got)
	}
	delta := make([]float32, 3*pad8(h)*pw)
	for i := range delta {
		delta[i] = 0.1
	}
	ApplyInpaint(eye, holes, delta, 0, h)
	if eye.Y[5*w+10] != 122 || eye.Y[5*w+11] != 100 {
		t.Errorf("luma: %d %d", eye.Y[5*w+10], eye.Y[5*w+11])
	}
	if eye.Cb[2*(w/2)+5] != 131 || eye.Cb[2*(w/2)+6] != 120 || eye.Cr[2*(w/2)+5] != 141 {
		t.Errorf("chroma: %d %d %d", eye.Cb[2*(w/2)+5], eye.Cb[2*(w/2)+6], eye.Cr[2*(w/2)+5])
	}
	iw := &InpaintWeights{W: map[string]*nn.Tensor{}, B: map[string]*nn.Tensor{}}
	for _, l := range InpaintLayers {
		iw.W[l.Name], iw.B[l.Name] = nn.New(l.Shape()...), nn.New(l.Out)
		iw.W[l.Name].Data[0] = 0.5
	}
	var buf bytes.Buffer
	if _, err := iw.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	back, err := DecodeInpaint(&buf)
	if err != nil || back.W["e1a"].Data[0] != 0.5 || len(back.W["out"].Data) != 3*16*9 {
		t.Fatalf("read back: %v", err)
	}
}

// TestInpaintForward: the filler runs on the CPU and the GPU to the same
// corrections, at a padded size.
func TestInpaintForward(t *testing.T) {
	w, h := 40, 24
	eye := &mvc.Frame{Width: w, Height: h, StrideY: w, StrideC: w / 2, Y: make([]byte, w*h), Cb: make([]byte, w*h/4), Cr: make([]byte, w*h/4)}
	holes := make([]bool, w*h)
	for i := range eye.Y {
		eye.Y[i] = byte(i % 251)
		holes[i] = i%7 == 0
	}
	iw := &InpaintWeights{W: map[string]*nn.Tensor{}, B: map[string]*nn.Tensor{}}
	k := 0
	for _, l := range InpaintLayers {
		iw.W[l.Name], iw.B[l.Name] = nn.New(l.Shape()...), nn.New(l.Out)
		for i := range iw.W[l.Name].Data {
			k++
			iw.W[l.Name].Data[i] = float32(k%13-6) / 40
		}
	}
	x := InpaintInput(eye, holes, 0, h)
	cpu := iw.Inpaint(nn.CPU, x)
	if cpu.Dim(0) != 3 || cpu.Dim(1) != pad8(h) || cpu.Dim(2) != pad8(w) {
		t.Fatalf("shape %v", cpu.Shape)
	}
	g, err := nn.NewGPU()
	if err != nil {
		t.Skip("no GPU")
	}
	defer g.Close()
	gw := &InpaintWeights{W: map[string]*nn.Tensor{}, B: map[string]*nn.Tensor{}}
	for _, l := range InpaintLayers {
		gw.W[l.Name], gw.B[l.Name] = g.Upload(iw.W[l.Name]), g.Upload(iw.B[l.Name])
	}
	out, err := g.Download(gw.Inpaint(g, g.Upload(x)))
	if err != nil {
		t.Fatal(err)
	}
	for i := range cpu.Data {
		if math.Abs(float64(cpu.Data[i]-out.Data[i])) > 1e-3+1e-3*math.Abs(float64(cpu.Data[i])) {
			t.Fatalf("at %d: CPU %g, GPU %g", i, cpu.Data[i], out.Data[i])
		}
	}
}
