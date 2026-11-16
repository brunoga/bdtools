package sam

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/brunoga/bdtools/internal/nn"
)

// TestTrackTiming times a tracking step's parts on the reference's
// pictures (SAM_TRACK, SAM_MODEL), with and without half precision.
func TestTrackTiming(t *testing.T) {
	dir, file := os.Getenv("SAM_TRACK"), os.Getenv("SAM_MODEL")
	if dir == "" || os.Getenv("SAM_TIMING") == "" {
		t.Skip("SAM_TRACK, SAM_MODEL and SAM_TIMING not set")
	}
	m, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nn.NewGPU()
	if err != nil {
		t.Skip(err)
	}
	defer g.Close()
	pic := make(Picture, 3*Size*Size)
	for i := range pic {
		pic[i] = byte(i * 7)
	}
	for _, half := range []bool{false, true} {
		g.Half = half
		m.Use(g)
		tr := m.NewTracker(1 << 20)
		var tt, te, tk, tm time.Duration
		sync := func(x *nn.Tensor) { _, _ = g.Download(x) }
		f0 := m.Encode(g.Upload(pic.Tensor()))
		if _, _, err := tr.Prompt(100, f0, &Box{300, 600, 700, 900}, nil); err != nil {
			t.Fatal(err)
		}
		g.Release()
		const n = 20
		for i := range n {
			s := time.Now()
			x := g.Upload(pic.Tensor())
			sync(x)
			a := time.Now()
			f := m.Encode(x)
			sync(f.High0)
			b := time.Now()
			pix := tr.withMemory(101+i, f)
			sync(pix)
			c := time.Now()
			if _, _, err := tr.Track(101+i, f); err != nil {
				t.Fatal(err)
			}
			d := time.Now()
			g.Release()
			if i >= 3 {
				tt, te, tm, tk = tt+a.Sub(s), te+b.Sub(a), tm+c.Sub(b), tk+d.Sub(c)
			}
		}
		k := time.Duration(n - 3)
		fmt.Printf("half %v: input %v, encode %v, memory attention %v, track step (memory attention again, decode, memory) %v\n",
			half, tt/k, te/k, tm/k, tk/k)
		tr.Close()
	}
}

// TestMemoryAttentionTiming times the memory attention's parts for seven
// memories and 16 pointers, in half precision.
func TestMemoryAttentionTiming(t *testing.T) {
	file := os.Getenv("SAM_MODEL")
	if file == "" || os.Getenv("SAM_TIMING") == "" {
		t.Skip("SAM_MODEL and SAM_TIMING not set")
	}
	m, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nn.NewGPU()
	if err != nil {
		t.Skip(err)
	}
	defer g.Close()
	g.Half = true
	m.Use(g)
	n := grid * grid
	nk := 7*n + 64
	rnd := func(shape ...int) *nn.Tensor {
		x := nn.New(shape...)
		for i := range x.Data {
			x.Data[i] = float32(i%97)/97 - 0.5
		}
		return g.Upload(x)
	}
	vision, memory, pos := rnd(dModel, grid, grid), rnd(nk, memDim), rnd(nk, memDim)
	q, k, v := rnd(n, dModel), rnd(nk, dModel), rnd(nk, dModel)
	timeIt := func(name string, f func() *nn.Tensor) {
		run := func(reps int) time.Duration {
			var last *nn.Tensor
			s := time.Now()
			for range reps {
				last = f()
			}
			_, _ = g.Download(last)
			return time.Since(s)
		}
		run(1)
		one, ten := run(1), run(11)
		fmt.Printf("%s: %v\n", name, (ten-one)/10)
		g.Release()
	}
	l := "memory_attention.layers.0."
	timeIt("whole", func() *nn.Tensor { return m.memoryAttention(vision, memory, pos, 64) })
	timeIt("cross attention alone", func() *nn.Tensor { return g.Attention(q, k, v, 1, 0.0625) })
	timeIt("self attention alone", func() *nn.Tensor { return g.Attention(q, q, q, 1, 0.0625) })
	timeIt("k_proj of the memory", func() *nn.Tensor { return m.linear(memory, l+"cross_attn_image.k_proj") })
	timeIt("rope of the keys", func() *nn.Tensor { return g.Rope(k, m.keep("rope", func() *nn.Tensor { return ropeTable(dModel, grid) }), nk-64) })
	timeIt("mlp", func() *nn.Tensor {
		return m.linear(g.ReLU(m.linear(q, l+"linear1")), l+"linear2")
	})
}
