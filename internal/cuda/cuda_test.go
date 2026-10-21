package cuda

import (
	_ "embed"
	"errors"
	"testing"
)

//go:embed testdata/add.ptx
var addPTX []byte

func TestAXPY(t *testing.T) {
	c, err := New()
	if errors.Is(err, ErrUnavailable) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	m, err := c.Load(addPTX)
	if err != nil {
		t.Fatal(err)
	}
	k, err := m.Kernel("axpy")
	if err != nil {
		t.Fatal(err)
	}
	const n = 1000
	x, y := make([]float32, n), make([]float32, n)
	for i := range x {
		x[i], y[i] = float32(i), 1
	}
	bx, err := c.Alloc(4 * n)
	if err != nil {
		t.Fatal(err)
	}
	by, err := c.Alloc(4 * n)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Free(bx)
	defer c.Free(by)
	if err := c.Upload(bx, x); err != nil {
		t.Fatal(err)
	}
	if err := c.Upload(by, y); err != nil {
		t.Fatal(err)
	}
	if err := k.Launch(Dim{X: (n + 255) / 256}, Dim{X: 256}, 0, n, float32(2), bx, by); err != nil {
		t.Fatal(err)
	}
	if err := c.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := c.Download(y, by); err != nil {
		t.Fatal(err)
	}
	for i, v := range y {
		if v != 2*float32(i)+1 {
			t.Fatalf("y[%d] = %g, want %g", i, v, 2*float32(i)+1)
		}
	}
	t.Logf("%s, %d MiB", c.Name, c.Mem>>20)
}
