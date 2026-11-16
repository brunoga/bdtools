package textdet

import (
	"math"
	"testing"

	"github.com/brunoga/bdtools/internal/nn"
)

func TestSightingLater(t *testing.T) {
	l := &Overlay{first: 10, sureAt: 30}
	for _, c := range []struct {
		at    int
		found bool
		lift  float64
		want  float64
	}{
		{at: 10, found: true, want: 1.0 / 8},        // the first picture: rising
		{at: 12, found: true, want: 3.0 / 8},        // rising from the first picture
		{at: 20, found: true, want: 1},              // up before the detector was sure
		{at: 12, found: false, want: 0},             // followed but not found: as it was
		{at: 33, found: true, lift: 0.5, want: 1},   // the lift's own ramp, caught up
		{at: 40, found: true, lift: 0.5, want: 0.5}, // past it: as it was
	} {
		s := Sighting{Line: l, Lift: c.lift, Found: c.found, at: c.at}
		if got := s.Later(); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("picture %d: %.3f, want %.3f", c.at, got, c.want)
		}
	}
	// a line the detector followed long before it was sure of it rises
	// from the first picture not yet made
	l = &Overlay{first: 0, sureAt: 100}
	if got, want := (Sighting{Line: l, Found: true, at: 100 - Ahead + 2}).Later(), 1.0/8; got != want {
		t.Errorf("long followed: %.3f, want %.3f", got, want)
	}
	if got := (Sighting{Line: &Overlay{first: 0, sureAt: -1}, Found: true, Lift: 0.25, at: 5}).Later(); got != 0.25 {
		t.Errorf("never sure: %.3f, want 0.25", got)
	}
}

// TestInputGrey checks the GPU's input of a grey picture against Input's.
func TestInputGrey(t *testing.T) {
	g, err := nn.NewGPU()
	if err != nil {
		t.Skip("no GPU:", err)
	}
	w, h := 70, 45
	y := make([]byte, w*h)
	for i := range y {
		y[i] = byte(i * 7)
	}
	want := Input(y, w, h, w)
	got, err := g.Download(g.TextInputGrey(y, w, h))
	if err != nil {
		t.Fatal(err)
	}
	g.Release()
	for i, v := range want.Data {
		if math.Abs(float64(v-got.Data[i])) > 1e-5 {
			t.Fatalf("at %d: %g, want %g", i, got.Data[i], v)
		}
	}
}
