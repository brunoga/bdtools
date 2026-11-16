package stereo

import (
	"errors"
	"math"
	"testing"

	"github.com/brunoga/bdtools/internal/cuda"
	"github.com/brunoga/bdtools/internal/nn"
)

// An object's push moves the disparity under its mask and nothing else,
// on the CPU and on a GPU alike.
func TestPushObjects(t *testing.T) {
	w, h, top, bottom := 320, 180, 20, 160
	left := make([]byte, ObjectSide*ObjectSide) // the left half
	ring := make([]byte, ObjectSide*ObjectSide) // a soft-edged square
	for y := range ObjectSide {
		for x := range ObjectSide {
			if x < ObjectSide/2 {
				left[y*ObjectSide+x] = 255
			}
			if x > 100 && x < 200 && y > 60 && y < 180 {
				ring[y*ObjectSide+x] = 128
			}
		}
	}
	objs := []ObjectMask{{Mask: left, Push: 0.01}, {Mask: ring, Push: -0.02}}
	d := make([]float32, w*(bottom-top))
	pushObjects(objs, d, w, h, top, bottom)
	at := func(x, y int) float32 { return d[(y-top)*w+x] }
	if v := at(10, 100); math.Abs(float64(v-3.2)) > 1e-4 {
		t.Errorf("left: %g, want 3.2 (1%% of 320)", v)
	}
	if v := at(300, 30); v != 0 {
		t.Errorf("right, outside both: %g, want 0", v)
	}
	if v := at(200, 100); math.Abs(float64(v+6.4*128/255)) > 1e-3 {
		t.Errorf("in the square: %g, want %g", v, -6.4*128/255)
	}
	g, err := nn.NewGPU()
	if errors.Is(err, cuda.ErrUnavailable) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	full := nn.New(w * h)
	gd := g.Upload(full)
	g.PushObjects(gd, w, h, top, bottom, g.UploadBytes(objectBytes(objs, w)), ObjectSide, len(objs))
	got, err := g.Download(gd)
	if err != nil {
		t.Fatal(err)
	}
	for y := top; y < bottom; y++ {
		for x := range w {
			if a, b := got.Data[y*w+x], at(x, y); math.Abs(float64(a-b)) > 1e-4 {
				t.Fatalf("at %d,%d: GPU %g, CPU %g", x, y, a, b)
			}
		}
	}
	for _, v := range got.Data[:top*w] {
		if v != 0 {
			t.Fatal("the bars were moved")
		}
	}
}
