package stereo

import (
	"bytes"
	"slices"
	"testing"

	"github.com/brunoga/bdtools/internal/nn"
)

func zeroFill() *FillWeights {
	fw := &FillWeights{W: map[string]*nn.Tensor{}, B: map[string]*nn.Tensor{}}
	for _, l := range FillLayers {
		fw.W[l.Name], fw.B[l.Name] = nn.New(l.Shape()...), nn.New(l.Out)
	}
	return fw
}

// A filler that corrects nothing leaves Render's eye as it is, holes and
// all.
func TestFillEyeNothing(t *testing.T) {
	l, _, _ := redepthScene(200, 120, 4, 20)
	d := make([]float32, 200*120)
	for i := range d {
		d[i] = 4
		if x, y := i%200, i/200; x > 70 && x < 130 && y > 40 && y < 80 {
			d[i] = 20
		}
	}
	eye, holes := Render(l, d, -1)
	if !slices.Contains(holes, true) {
		t.Fatal("no holes")
	}
	want := *eye
	want.Y, want.Cb, want.Cr = slices.Clone(eye.Y), slices.Clone(eye.Cb), slices.Clone(eye.Cr)
	if err := zeroFill().FillEye(nn.CPU, eye, holes, d); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(eye.Y, want.Y) || !slices.Equal(eye.Cb, want.Cb) || !slices.Equal(eye.Cr, want.Cr) {
		t.Error("the eye changed")
	}
}

// The weights read back as written.
func TestFillWeightsRoundTrip(t *testing.T) {
	fw := zeroFill()
	fw.W["e1a"].Data[3], fw.B["out"].Data[2] = 0.25, -1.5
	var b bytes.Buffer
	if _, err := fw.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	got, err := DecodeFill(&b)
	if err != nil {
		t.Fatal(err)
	}
	if got.W["e1a"].Data[3] != 0.25 || got.B["out"].Data[2] != -1.5 {
		t.Error("the weights differ")
	}
}
