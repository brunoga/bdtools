package depth

import (
	"errors"
	"fmt"
	"math"
	"os"
	"testing"

	"github.com/brunoga/bdtools/internal/cuda"

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
	m, err := Load("testdata/tiny.pth")
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
	m, err := Load(wf)
	if err != nil {
		t.Fatal(err)
	}
	io, err := nn.LoadPTH(rf)
	if err != nil {
		t.Fatal(err)
	}
	stream(t, m, io, 1e-3)
}

// One 518x924 frame (a 1080p frame's network size), when $VDA_WEIGHTS
// names the small model: go test -bench Frame -run ^$.
func BenchmarkFrame(b *testing.B) {
	wf := os.Getenv("VDA_WEIGHTS")
	if wf == "" {
		b.Skip("VDA_WEIGHTS unset")
	}
	m, err := Load(wf)
	if err != nil {
		b.Fatal(err)
	}
	x := nn.New(3, 518, 924)
	for i := range x.Data {
		x.Data[i] = float32(i%97) / 97
	}
	m.Frame(x)
	b.ResetTimer()
	for range b.N {
		m.Frame(x)
	}
}

// gpu is a GPU backend for a test, or a skip without one.
func gpu(tb testing.TB) *nn.GPU {
	tb.Helper()
	g, err := nn.NewGPU()
	if errors.Is(err, cuda.ErrUnavailable) {
		tb.Skip(err)
	}
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(g.Close)
	return g
}

// The small random model on the GPU: PyTorch's depth too.
func TestTinyModelOnTheGPU(t *testing.T) {
	g := gpu(t)
	m, err := Load("testdata/tiny.pth")
	if err != nil {
		t.Fatal(err)
	}
	io, err := nn.LoadPTH("testdata/tiny-io.pth")
	if err != nil {
		t.Fatal(err)
	}
	m.Use(g)
	stream(t, m, io, 1e-4)
}

// The published model on the GPU, against PyTorch's on film frames.
func TestPublishedModelOnTheGPU(t *testing.T) {
	wf, rf := os.Getenv("VDA_WEIGHTS"), os.Getenv("VDA_REF")
	if wf == "" || rf == "" {
		t.Skip("VDA_WEIGHTS or VDA_REF unset")
	}
	g := gpu(t)
	m, err := Load(wf)
	if err != nil {
		t.Fatal(err)
	}
	io, err := nn.LoadPTH(rf)
	if err != nil {
		t.Fatal(err)
	}
	m.Use(g)
	stream(t, m, io, 1e-3)
}

// The published model on the tensor cores (its products' inputs in half
// precision), against PyTorch's single precision: off by a little
// everywhere, more at a few pixels.
func TestPublishedModelOnTheGPUInHalf(t *testing.T) {
	wf, rf := os.Getenv("VDA_WEIGHTS"), os.Getenv("VDA_REF")
	if wf == "" || rf == "" {
		t.Skip("VDA_WEIGHTS or VDA_REF unset")
	}
	g := gpu(t)
	g.Half = true
	m, err := Load(wf)
	if err != nil {
		t.Fatal(err)
	}
	io, err := nn.LoadPTH(rf)
	if err != nil {
		t.Fatal(err)
	}
	m.Use(g)
	for i := 0; ; i++ {
		x, ok := io[fmt.Sprintf("in.%d", i)]
		if !ok {
			break
		}
		got, want := m.Frame(x), io[fmt.Sprintf("out.%d", i)]
		var peak, sq float64
		for j, v := range want.Data {
			peak = max(peak, math.Abs(float64(v)))
			d := float64(got.Data[j] - v)
			sq += d * d
		}
		rms := math.Sqrt(sq/float64(len(want.Data))) / peak
		worst := compare(got, want)
		// measured: 2.4e-4 and 9.3e-3 (single precision: 2e-5 at worst)
		if rms > 1e-3 || worst > 2e-2 {
			t.Errorf("frame %d: off by %.3g of the peak (root mean square), %.3g at worst", i, rms, worst)
		}
	}
}

// BenchmarkFrameGPU is BenchmarkFrame on the GPU (on the tensor cores
// with VDA_HALF set).
func BenchmarkFrameGPU(b *testing.B) {
	wf := os.Getenv("VDA_WEIGHTS")
	if wf == "" {
		b.Skip("VDA_WEIGHTS unset")
	}
	g := gpu(b)
	g.Half = os.Getenv("VDA_HALF") != ""
	m, err := Load(wf)
	if err != nil {
		b.Fatal(err)
	}
	m.Use(g)
	x := nn.New(3, 518, 924)
	for i := range x.Data {
		x.Data[i] = float32(i%97) / 97
	}
	m.Frame(x)
	b.ResetTimer()
	for range b.N {
		m.Frame(x)
	}
}
