package stereo

import (
	"bytes"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/brunoga/bdtools/internal/nn"
)

func randomRefine(r *rand.Rand, sd float64) *RefineWeights {
	rw := &RefineWeights{W: map[string]*nn.Tensor{}, B: map[string]*nn.Tensor{}}
	for _, l := range RefineLayers {
		w, b := nn.New(l.Shape()...), nn.New(l.Out)
		for i := range w.Data {
			w.Data[i] = float32(r.NormFloat64() * sd)
		}
		for i := range b.Data {
			b.Data[i] = float32(r.NormFloat64() * 0.01)
		}
		rw.W[l.Name], rw.B[l.Name] = w, b
	}
	return rw
}

func refineInput(r *rand.Rand, w, h int) *nn.Tensor {
	y, cb, cr := make([]byte, w*h), make([]byte, w*h), make([]byte, w*h)
	d := make([]float32, w*h)
	for i := range y {
		y[i], cb[i], cr[i] = byte(r.IntN(256)), byte(r.IntN(256)), byte(r.IntN(256))
		d[i] = float32(i%w) + float32(r.NormFloat64())
	}
	x, _, _ := RefineInput(y, cb, cr, w, d, w, h)
	return x
}

func TestRefineZeroIsNoChange(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	rw := randomRefine(r, 0.1)
	clear(rw.W["out"].Data)
	clear(rw.B["out"].Data)
	x := refineInput(r, 37, 21)
	if x.Dim(1) != 24 || x.Dim(2) != 40 {
		t.Fatalf("input %v, want padded to 24 x 40", x.Shape)
	}
	z := x.Data[3*24*40:]
	y := rw.Refine(nn.CPU, x)
	for i, v := range y.Data {
		if v != z[i] {
			t.Fatalf("at %d: %v, want the depth as it is (%v)", i, v, z[i])
		}
	}
}

func TestRefineWeightsRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	rw := randomRefine(r, 0.1)
	var buf bytes.Buffer
	if _, err := rw.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "w.bin")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRefine(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range RefineLayers {
		for i, v := range rw.W[l.Name].Data {
			if got.W[l.Name].Data[i] != v {
				t.Fatalf("%s: weight %d read %v, written %v", l.Name, i, got.W[l.Name].Data[i], v)
			}
		}
	}
}

func TestRefineOnTheGPU(t *testing.T) {
	g, err := nn.NewGPU()
	if err != nil {
		t.Skip("no GPU:", err)
	}
	r := rand.New(rand.NewPCG(5, 6))
	rw := randomRefine(r, 0.1)
	x := refineInput(r, 120, 64)
	want := rw.Refine(nn.CPU, x)
	on := &RefineWeights{W: map[string]*nn.Tensor{}, B: map[string]*nn.Tensor{}}
	for _, l := range RefineLayers {
		on.W[l.Name], on.B[l.Name] = g.Upload(rw.W[l.Name]), g.Upload(rw.B[l.Name])
	}
	got, err := g.Download(on.Refine(g, g.Upload(x)))
	if err != nil {
		t.Fatal(err)
	}
	var worst float64
	for i, v := range want.Data {
		worst = max(worst, math.Abs(float64(got.Data[i]-v)))
	}
	if worst > 1e-3 {
		t.Errorf("the GPU's refinement %v off the CPU's", worst)
	}
}
