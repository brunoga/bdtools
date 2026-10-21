package nn

import (
	"math"
	"slices"
	"testing"
)

// The ops against PyTorch's outputs on random inputs (testdata/ops.pth,
// written by testdata/gen/ops.py).
func TestOpsMatchPyTorch(t *testing.T) {
	f, err := LoadPTH("testdata/ops.pth")
	if err != nil {
		t.Fatal(err)
	}
	g := func(name string) *Tensor {
		v, ok := f[name]
		if !ok {
			t.Fatalf("no %s in the fixtures", name)
		}
		return v.Clone()
	}
	check := func(name string, got, want *Tensor) {
		t.Helper()
		if !slices.Equal(got.Shape, want.Shape) {
			t.Errorf("%s: shape %v, want %v", name, got.Shape, want.Shape)
			return
		}
		var worst float64
		for i, w := range want.Data {
			d := math.Abs(float64(got.Data[i]-w)) / max(1, math.Abs(float64(w)))
			worst = max(worst, d)
		}
		if worst > 2e-5 {
			t.Errorf("%s: differs by %.3g", name, worst)
		}
	}
	check("linear", Linear(g("linear.x"), g("linear.w"), g("linear.b")), g("linear.y"))
	for name, c := range map[string][2]int{"conv3": {1, 1}, "conv3s2": {2, 1}, "conv1": {1, 0}, "patch": {14, 0}} {
		check(name, Conv2d(g(name+".x"), g(name+".w"), g(name+".b"), c[0], c[1]), g(name+".y"))
	}
	for _, name := range []string{"convt4", "convt2"} {
		check(name, ConvTranspose2dK(g(name+".x"), g(name+".w"), g(name+".b")), g(name+".y"))
	}
	check("layernorm", LayerNorm(g("ln.x"), g("ln.w"), g("ln.b"), 1e-6), g("ln.y"))
	check("groupnorm", GroupNorm(g("gn.x"), 4, g("gn.w"), g("gn.b"), 1e-6), g("gn.y"))
	check("gelu", GELU(g("gelu.x")), g("gelu.y"))
	check("attention", Attention(g("attn.q"), g("attn.k"), g("attn.v"), 4, 0.5), g("attn.y"))
	check("bilinear up", Bilinear(g("bil.x"), 9, 13), g("bil.up"))
	check("bilinear down", Bilinear(g("bil.x"), 3, 2), g("bil.down"))
	check("bicubic", BicubicScale(g("bic.x"), (4+0.1)/6, (9+0.1)/6), g("bic.y"))
}
