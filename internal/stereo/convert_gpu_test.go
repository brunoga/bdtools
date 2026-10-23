package stereo

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/brunoga/bdtools/internal/nn"
	"github.com/brunoga/bdtools/mvc"
)

// randomPicture is a w x h picture of random samples, its rows padded.
func randomPicture(r *rand.Rand, w, h int) *mvc.Frame {
	f := &mvc.Frame{Width: w, Height: h, StrideY: w + 16, StrideC: w/2 + 8}
	f.Y = make([]byte, f.StrideY*h)
	f.Cb, f.Cr = make([]byte, f.StrideC*h/2), make([]byte, f.StrideC*h/2)
	for _, p := range [][]byte{f.Y, f.Cb, f.Cr} {
		for i := range p {
			p[i] = byte(16 + r.IntN(220))
		}
	}
	return f
}

// worstOff is the largest difference of got from want, relative to
// want's largest magnitude.
func worstOff(got, want []float32) float64 {
	var worst, peak float64
	for i, v := range want {
		worst = max(worst, math.Abs(float64(got[i]-v)))
		peak = max(peak, math.Abs(float64(v)))
	}
	return worst / max(peak, 1e-30)
}

func TestNetInputOnTheGPU(t *testing.T) {
	g, err := nn.NewGPU()
	if err != nil {
		t.Skip("no GPU:", err)
	}
	defer g.Close()
	f := randomPicture(rand.New(rand.NewPCG(1, 2)), 200, 120)
	top, bottom, nw, nh := 11, 111, 84, 42
	want := netInput(f, top, bottom, nw, nh)
	p := g.UploadPicture(f.Y, f.Cb, f.Cr, f.Width, f.Height, f.StrideY, f.StrideC)
	got, err := g.Download(g.NetInput(p, top, bottom-top, nw, nh))
	g.Release()
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range want.Data {
		if got.Data[i] != v {
			t.Fatalf("value %d: the GPU's %v, the CPU's %v", i, got.Data[i], v)
		}
	}
}

func TestRefineInputOnTheGPU(t *testing.T) {
	g, err := nn.NewGPU()
	if err != nil {
		t.Skip("no GPU:", err)
	}
	defer g.Close()
	r := rand.New(rand.NewPCG(3, 4))
	f := randomPicture(r, 200, 120)
	top, bottom := 11, 111
	hw, hh := f.Width/2, (bottom-top)/2
	d := nn.New(1, hh, hw)
	for i := range d.Data {
		d.Data[i] = r.Float32()*3 + 1
	}
	// the CPU's, as refine makes it
	sc := f.StrideC
	y := make([]byte, hh*sc)
	for row := range hh {
		p, q := f.Y[(top+2*row)*f.StrideY:], f.Y[(top+2*row+1)*f.StrideY:]
		for x := range hw {
			y[row*sc+x] = byte((int(p[2*x]) + int(p[2*x+1]) + int(q[2*x]) + int(q[2*x+1]) + 2) / 4)
		}
	}
	co := top / 2 * sc
	want, _, _ := RefineInput(y, f.Cb[co:], f.Cr[co:], sc, d.Data, hw, hh)
	p := g.UploadPicture(f.Y, f.Cb, f.Cr, f.Width, f.Height, f.StrideY, f.StrideC)
	gd := g.Upload(d)
	got, err := g.Download(g.RefineInput(p, top, gd, g.Moments(gd, hh, hw, hw), hw, hh, want.Dim(2), want.Dim(1)))
	g.Release()
	if err != nil {
		t.Fatal(err)
	}
	if off := worstOff(got.Data, want.Data); off > 1e-6 {
		t.Errorf("the GPU's refinement input %.2g off the CPU's", off)
	}
}

func TestRefineOnTheGPUFromThePicture(t *testing.T) {
	g, err := nn.NewGPU()
	if err != nil {
		t.Skip("no GPU:", err)
	}
	defer g.Close()
	r := rand.New(rand.NewPCG(5, 6))
	f := randomPicture(r, 200, 120)
	top, bottom := 10, 110
	dm := nn.New(1, 42, 84)
	for i := range dm.Data {
		dm.Data[i] = r.Float32()*3 + 1
	}
	rw := randomRefine(r, 0.1)
	want, err := (&Converter{Refine: rw}).refine(nn.CPU, dm, f, top, bottom)
	if err != nil {
		t.Fatal(err)
	}
	p := g.UploadPicture(f.Y, f.Cb, f.Cr, f.Width, f.Height, f.StrideY, f.StrideC)
	got, err := g.Download((&Converter{Refine: rw}).refineGPU(g, p, g.Upload(dm), top, bottom))
	g.Release()
	if err != nil {
		t.Fatal(err)
	}
	if off := worstOff(got.Data, want.Data); off > 1e-3 {
		t.Errorf("the GPU's refined depth %.2g off the CPU's", off)
	}
}
