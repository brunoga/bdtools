package nn

import (
	"errors"
	"testing"

	"github.com/brunoga/bdtools/internal/cuda"
)

// Buffers of sizes no longer asked for are freed: a stream whose
// pictures change size does not keep every size's.
func TestGPUFreesUnusedSizes(t *testing.T) {
	g, err := NewGPU()
	if errors.Is(err, cuda.ErrUnavailable) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	spares := func() int {
		n := 0
		for _, l := range g.free {
			n += len(l)
		}
		return n
	}
	for size := 1000; size < 1100; size++ {
		g.tensor(size)
		g.Release()
	}
	if n := spares(); n > 2 {
		t.Errorf("%d spare buffers after 100 sizes, each used once; want at most 2", n)
	}
	// the same size every frame is reused, not reallocated
	g.tensor(5000)
	g.Release()
	b := g.free[4*5000][0].buf
	x := g.tensor(5000)
	if x.dev.a.buf != b {
		t.Error("a frame's buffer not reused by the next")
	}
	g.Release()
}
