package sam

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brunoga/bdtools/internal/cuda"
	"github.com/brunoga/bdtools/internal/nn"
)

// The tracker follows the Toy Story 2 wordmark as SAM 2's video predictor
// does (SAM_TRACK, the directory ~/mvc-scratch/sam/ref_track.py wrote, and
// SAM_MODEL, the checkpoint): each picture's mask, object score, object
// pointer and memory, on a GPU (the CPU's are the same code, and slow).
func TestTrackAgainstReference(t *testing.T) {
	trackReference(t, "SAM_TRACK", 0)
}

// The tracker follows it from a box on picture 12 to the end, then back
// to the start (SAM_TRACK2, ~/mvc-scratch/sam/ref_track2.py's directory).
func TestTrackBothWays(t *testing.T) {
	trackReference(t, "SAM_TRACK2", 12)
}

func trackReference(t *testing.T, env string, from int) {
	dir, file := os.Getenv(env), os.Getenv("SAM_MODEL")
	if dir == "" || file == "" {
		t.Skip(env + " and SAM_MODEL not set")
	}
	m, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nn.NewGPU()
	if errors.Is(err, cuda.ErrUnavailable) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	m.Use(g)
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
	// rel is the largest difference relative to the reference's rms
	rel := func(got, want []float32) float64 {
		var ss, worst float64
		for i, v := range want {
			ss += float64(v) * float64(v)
			worst = max(worst, math.Abs(float64(got[i]-v)))
		}
		return worst / math.Sqrt(ss/float64(len(want))+1e-12)
	}
	size, box := ref("size").Data, ref("box").Data
	sx, sy := Size/size[0], Size/size[1]
	n := 0
	for shapes[fmt.Sprintf("input%d", n)] != nil {
		n++
	}
	tr := m.NewTracker(n)
	defer tr.Close()
	start := time.Now()
	var order []int
	for i := from; i < n; i++ {
		order = append(order, i)
	}
	for i := from - 1; i >= 0; i-- {
		order = append(order, i)
	}
	for _, i := range order {
		f := m.Encode(g.Upload(ref(fmt.Sprintf("input%d", i))))
		var mk Mask
		var obj float32
		if i == from {
			mk, obj, err = tr.Prompt(i, f, &Box{box[0] * sx, box[1] * sy, box[2] * sx, box[3] * sy}, nil)
		} else {
			tr.Reverse = i < from
			mk, obj, err = tr.Track(i, f)
		}
		if err != nil {
			t.Fatal(err)
		}
		mem, err := g.Download(tr.out[i].mem)
		if err != nil {
			t.Fatal(err)
		}
		// the reference's memory is [64, 64, 64]: its rows are ours transposed
		wm := ref(fmt.Sprintf("mem%d", i))
		wr := make([]float32, len(wm.Data))
		for c := range memDim {
			for p := range grid * grid {
				wr[p*memDim+c] = wm.Data[c*grid*grid+p]
			}
		}
		dm, dp, dr := rel(mk.Logits, ref(fmt.Sprintf("mask%d", i)).Data), rel(tr.out[i].ptr, ref(fmt.Sprintf("ptr%d", i)).Data), rel(mem.Data, wr)
		ws := ref(fmt.Sprintf("score%d", i)).Data[0]
		t.Logf("picture %2d: mask %.2g, pointer %.2g, memory %.2g of their rms; object %.3f, want %.3f", i, dm, dp, dr, obj, ws)
		if dm > 1e-2 || dp > 1e-2 || dr > 2e-2 || math.Abs(float64(obj-ws)) > 0.05*(1+math.Abs(float64(ws))) {
			t.Errorf("picture %d differs", i)
		}
		g.Release()
	}
	t.Logf("%d pictures in %v", n, time.Since(start))
}
