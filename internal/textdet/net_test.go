package textdet

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"

	"github.com/brunoga/bdtools/internal/nn"
)

// TestAgainstOnnxRuntime compares the net's map with onnxruntime's for a
// picture (TEXTDET_CHECK: a directory with y.raw, 960 x 540 grey, and
// p.raw, its map as float32) and the model TEXTDET_MODEL.
func TestAgainstOnnxRuntime(t *testing.T) {
	dir, model := os.Getenv("TEXTDET_CHECK"), os.Getenv("TEXTDET_MODEL")
	if dir == "" || model == "" {
		t.Skip("TEXTDET_CHECK and TEXTDET_MODEL not set")
	}
	b, err := os.ReadFile(model)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nn.LoadONNX(b)
	if err != nil {
		t.Fatal(err)
	}
	net, err := Compile(g)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d nodes as %d layers", len(g.Nodes), net.Layers())
	y, _ := os.ReadFile(dir + "/y.raw")
	pr, _ := os.ReadFile(dir + "/p.raw")
	w, h := 960, 540
	want := make([]float32, w*h)
	for i := range want {
		want[i] = math.Float32frombits(binary.LittleEndian.Uint32(pr[4*i:]))
	}
	x := Input(y, w, h, w)
	check := func(name string, be nn.Backend) {
		in := be.Upload(x)
		t0 := time.Now()
		out, err := be.Download(net.Run(be, in))
		if err != nil {
			t.Fatal(err)
		}
		d := time.Since(t0)
		t0 = time.Now()
		for range 5 {
			_, _ = be.Download(net.Run(be, in))
			be.Release()
		}
		W := out.Dim(2)
		var worst float64
		var over int
		for r := range h {
			for c := range w {
				e := math.Abs(float64(out.Data[r*W+c] - want[r*w+c]))
				worst = max(worst, e)
				if (out.Data[r*W+c] > 0.3) != (want[r*w+c] > 0.3) {
					over++
				}
			}
		}
		t.Logf("%s: worst difference %.2g, %d pixels on the other side of 0.3; first run %v, then %v a picture",
			name, worst, over, d, time.Since(t0)/5)
		if worst > 0.02 {
			t.Errorf("%s: the map differs from onnxruntime's by up to %.3g", name, worst)
		}
	}
	check("cpu", nn.CPU)
	if gpu, err := nn.NewGPU(); err == nil {
		defer gpu.Close()
		check("gpu", gpu)
	} else {
		t.Logf("no GPU: %v", err)
	}
}

// TestAgainstPrototype compares the regions, their measures and the
// classifier with the Python prototype's (TEXTDET_CHECK: regions.json,
// trees.X and trees.p, and the classifier TEXTDET_TREES).
func TestAgainstPrototype(t *testing.T) {
	dir, trees := os.Getenv("TEXTDET_CHECK"), os.Getenv("TEXTDET_TREES")
	if dir == "" || trees == "" {
		t.Skip("TEXTDET_CHECK and TEXTDET_TREES not set")
	}
	y, _ := os.ReadFile(dir + "/y.raw")
	pr, _ := os.ReadFile(dir + "/p.raw")
	w, h := 960, 540
	p := make([]float32, w*h)
	for i := range p {
		p[i] = math.Float32frombits(binary.LittleEndian.Uint32(pr[4*i:]))
	}
	var want struct {
		Regions  [][5]int
		Features [][]float64
	}
	b, _ := os.ReadFile(dir + "/regions.json")
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	got := Regions(p, w, h, w)
	if len(got) != len(want.Regions) {
		t.Fatalf("%d regions, want %d", len(got), len(want.Regions))
	}
	for i, r := range got {
		wr := want.Regions[i]
		if (Region{wr[0], wr[1], wr[2], wr[3], wr[4], r.MX0, r.MX1}) != r {
			t.Fatalf("region %d is %+v, want %v", i, r, wr)
		}
		f := Features(Grey{y, w, h, w}, p, w, r, nil, nil, nil)
		for j, v := range want.Features[i] {
			if math.Abs(float64(f[j])-v) > 1e-3*(1+math.Abs(v)) {
				t.Errorf("region %d measure %d is %g, want %g", i, j, f[j], v)
			}
		}
	}
	b, err := os.ReadFile(trees)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := LoadTrees(b)
	if err != nil {
		t.Fatal(err)
	}
	xs, _ := os.ReadFile(dir + "/trees.X")
	ps, _ := os.ReadFile(dir + "/trees.p")
	n := len(ps) / 8
	for i := range n {
		var f [numFeatures]float32
		for j, c := range tr.Cols {
			f[c] = math.Float32frombits(binary.LittleEndian.Uint32(xs[4*(i*len(tr.Cols)+j):]))
		}
		wantP := math.Float64frombits(binary.LittleEndian.Uint64(ps[8*i:]))
		if g := tr.Prob(f); math.Abs(g-wantP) > 1e-6 {
			t.Errorf("row %d: probability %g, want %g", i, g, wantP)
		}
	}
}
