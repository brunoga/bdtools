package depth

import (
	"fmt"
	"math"
	"os"
	"testing"

	"github.com/brunoga/bdtools/internal/nn"
)

// compare reports the largest difference of got from want, relative to
// want's largest magnitude.
func compare(got, want *nn.Tensor) float64 {
	var peak, worst float64
	for _, v := range want.Data {
		peak = max(peak, math.Abs(float64(v)))
	}
	for i, v := range want.Data {
		worst = max(worst, math.Abs(float64(got.Data[i]-v)))
	}
	return worst / max(peak, 1e-9)
}

// stream runs the frames of a reference through the model and checks each
// depth map against PyTorch's.
func stream(t *testing.T, m *Model, io map[string]*nn.Tensor, tol float64) {
	t.Helper()
	for i := 0; ; i++ {
		x, ok := io[fmt.Sprintf("in.%d", i)]
		if !ok {
			if i == 0 {
				t.Fatal("no frames in the reference")
			}
			return
		}
		got := m.Frame(x)
		want := io[fmt.Sprintf("out.%d", i)]
		if d := compare(got, want); d > tol {
			t.Errorf("frame %d: differs by %.3g of the depth's range", i, d)
		}
	}
}

// A small Video Depth Anything, randomly initialised, streamed over 14
// frames (the temporal cache's window filled and slid): the same depth as
// PyTorch's (testdata, written by internal/nn/testdata/gen/vda.py tiny).
func TestTinyModelMatchesPyTorch(t *testing.T) {
	m, err := Load("testdata/tiny.pth", 2)
	if err != nil {
		t.Fatal(err)
	}
	io, err := nn.LoadPTH("testdata/tiny-io.pth")
	if err != nil {
		t.Fatal(err)
	}
	stream(t, m, io, 1e-4)

	// the frames' states matter: without them a later frame differs
	m.Reset()
	m.Frame(io["in.0"])
	m.Reset()
	if d := compare(m.Frame(io["in.5"]), io["out.5"]); d < 1e-2 {
		t.Errorf("frame 5 alone differs by only %.3g: the temporal attention is not tested", d)
	}
}

// The published small model on film frames, when $VDA_WEIGHTS and
// $VDA_REF (the reference vda.py real wrote) are set.
func TestSmallModelMatchesPyTorch(t *testing.T) {
	wf, rf := os.Getenv("VDA_WEIGHTS"), os.Getenv("VDA_REF")
	if wf == "" || rf == "" {
		t.Skip("VDA_WEIGHTS or VDA_REF unset")
	}
	m, err := Load(wf, 6)
	if err != nil {
		t.Fatal(err)
	}
	io, err := nn.LoadPTH(rf)
	if err != nil {
		t.Fatal(err)
	}
	stream(t, m, io, 1e-3)
}
