package stereo

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/brunoga/bdtools/internal/nn"
	"github.com/brunoga/bdtools/mvc"
)

// randomPicture is a w x h picture of random samples, its rows padded.
func randomPicture(r *rand.Rand, w, h int) *mvc.Frame {
	f := &mvc.Frame{Width: w, Height: h, StrideY: w + 16, StrideC: w/2 + 8}
	f.Y = make([]byte, f.StrideY*h)
	f.Cb, f.Cr = make([]byte, f.StrideC*((h+1)/2)), make([]byte, f.StrideC*((h+1)/2))
	for _, p := range [][]byte{f.Y, f.Cb, f.Cr} {
		for i := range p {
			p[i] = byte(16 + r.IntN(220))
		}
	}
	return f
}

// worstOff is the largest difference of got from want, relative to
// want's largest magnitude.
func worstOff(got, want []float32) float64 {
	var worst, peak float64
	for i, v := range want {
		worst = max(worst, math.Abs(float64(got[i]-v)))
		peak = max(peak, math.Abs(float64(v)))
	}
	return worst / max(peak, 1e-30)
}

func TestNetInputOnTheGPU(t *testing.T) {
	g, err := nn.NewGPU()
	if err != nil {
		t.Skip("no GPU:", err)
	}
	defer g.Close()
	f := randomPicture(rand.New(rand.NewPCG(1, 2)), 200, 120)
	top, bottom, nw, nh := 11, 111, 84, 42
	want := netInput(f, top, bottom, nw, nh)
	p := g.UploadPicture(f.Y, f.Cb, f.Cr, f.Width, f.Height, f.StrideY, f.StrideC)
	got, err := g.Download(g.NetInput(p, top, bottom-top, nw, nh))
	g.Release()
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range want.Data {
		if got.Data[i] != v {
			t.Fatalf("value %d: the GPU's %v, the CPU's %v", i, got.Data[i], v)
		}
	}
}

func TestRefineInputOnTheGPU(t *testing.T) {
	g, err := nn.NewGPU()
	if err != nil {
		t.Skip("no GPU:", err)
	}
	defer g.Close()
	r := rand.New(rand.NewPCG(3, 4))
	f := randomPicture(r, 200, 120)
	top, bottom := 11, 111
	hw, hh := f.Width/2, (bottom-top)/2
	d := nn.New(1, hh, hw)
	for i := range d.Data {
		d.Data[i] = r.Float32()*3 + 1
	}
	// the CPU's, as refine makes it
	sc := f.StrideC
	y := make([]byte, hh*sc)
	for row := range hh {
		p, q := f.Y[(top+2*row)*f.StrideY:], f.Y[(top+2*row+1)*f.StrideY:]
		for x := range hw {
			y[row*sc+x] = byte((int(p[2*x]) + int(p[2*x+1]) + int(q[2*x]) + int(q[2*x+1]) + 2) / 4)
		}
	}
	co := top / 2 * sc
	want, _, _ := RefineInput(y, f.Cb[co:], f.Cr[co:], sc, d.Data, hw, hh)
	p := g.UploadPicture(f.Y, f.Cb, f.Cr, f.Width, f.Height, f.StrideY, f.StrideC)
	gd := g.Upload(d)
	got, err := g.Download(g.RefineInput(p, top, gd, g.Moments(gd, hh, hw, hw), hw, hh, want.Dim(2), want.Dim(1)))
	g.Release()
	if err != nil {
		t.Fatal(err)
	}
	if off := worstOff(got.Data, want.Data); off > 1e-6 {
		t.Errorf("the GPU's refinement input %.2g off the CPU's", off)
	}
}

func TestRefineOnTheGPUFromThePicture(t *testing.T) {
	g, err := nn.NewGPU()
	if err != nil {
		t.Skip("no GPU:", err)
	}
	defer g.Close()
	r := rand.New(rand.NewPCG(5, 6))
	f := randomPicture(r, 200, 120)
	top, bottom := 10, 110
	dm := nn.New(1, 42, 84)
	for i := range dm.Data {
		dm.Data[i] = r.Float32()*3 + 1
	}
	rw := randomRefine(r, 0.1)
	want, err := (&Converter{Refine: rw}).refine(nn.CPU, dm, f, top, bottom)
	if err != nil {
		t.Fatal(err)
	}
	p := g.UploadPicture(f.Y, f.Cb, f.Cr, f.Width, f.Height, f.StrideY, f.StrideC)
	got, err := g.Download((&Converter{Refine: rw}).refineGPU(g, p, g.Upload(dm), top, bottom))
	g.Release()
	if err != nil {
		t.Fatal(err)
	}
	if off := worstOff(got.Data, want.Data); off > 1e-3 {
		t.Errorf("the GPU's refined depth %.2g off the CPU's", off)
	}
}

func TestEyesOnTheGPU(t *testing.T) {
	g, err := nn.NewGPU()
	if err != nil {
		t.Skip("no GPU:", err)
	}
	defer g.Close()
	r := rand.New(rand.NewPCG(7, 8))
	sq, sqd := scene(101, 47)
	noisy := randomPicture(r, 203, 61)
	// smooth slopes, steps both ways (holes and fold-overs) and the
	// row's ends pushed out
	nd := make([]float32, noisy.Width*noisy.Height)
	for i := range nd {
		x, y := i%noisy.Width, i/noisy.Width
		nd[i] = float32(math.Sin(float64(x)/9+float64(y)/5)*12) + r.Float32()
		if (x/23)%3 == 1 {
			nd[i] += 25
		}
	}
	// rows too wide for shared memory: a thread a row
	wide := randomPicture(r, 6200, 9)
	wd := make([]float32, wide.Width*wide.Height)
	for i := range wd {
		wd[i] = float32(math.Sin(float64(i%wide.Width)/7)*20) + r.Float32()
	}
	for _, c := range []struct {
		f *mvc.Frame
		d []float32
	}{{sq, sqd}, {noisy, nd}, {wide, wd}} {
		f := c.f
		wl, wr := Pair(f, c.d)
		p := g.UploadPicture(f.Y, f.Cb, f.Cr, f.Width, f.Height, f.StrideY, f.StrideC)
		buf := make([]byte, nn.EyesSize(f.Width, f.Height))
		err := g.DownloadBytes(buf, g.Eyes(p, g.Upload(&nn.Tensor{Shape: []int{f.Height, f.Width}, Data: c.d}), gapPixels))
		g.Release()
		if err != nil {
			t.Fatal(err)
		}
		gl, gr := eyeFrames(buf, f)
		for _, e := range []struct {
			name      string
			got, want *mvc.Frame
		}{{"left", gl, wl}, {"right", gr, wr}} {
			for pl, ps := range [][2][]byte{{e.got.Y, e.want.Y}, {e.got.Cb, e.want.Cb}, {e.got.Cr, e.want.Cr}} {
				for i, v := range ps[1] {
					if ps[0][i] != v {
						t.Fatalf("%dx%d, the %s eye's plane %d, sample %d: the GPU's %d, the CPU's %d",
							f.Width, f.Height, e.name, pl, i, ps[0][i], v)
					}
				}
			}
		}
	}
}

func TestDisparityOnTheGPU(t *testing.T) {
	g, err := nn.NewGPU()
	if err != nil {
		t.Skip("no GPU:", err)
	}
	defer g.Close()
	r := rand.New(rand.NewPCG(9, 10))
	w, h, top, bottom := 64, 40, 5, 33
	up := nn.New(1, bottom-top, w)
	for i := range up.Data {
		up.Data[i] = r.Float32() * 80
	}
	s := Shaper{Shape: RefinedShape}
	s.Shape.Offset, s.Shape.Far = -0.05, 0.03 // values behind the knee: the easing measured too
	want := s.Disparity(up.Data, w)
	ww := float64(w)
	far := float32(s.Shape.Far * ww)
	got, err := g.Download(g.Disparity(g.Upload(up), w, h, top, bottom, float32(s.Shape.Scale*ww), float32(s.Shape.Offset*ww), far, far*FarKnee, float32(0.04*ww)))
	g.Release()
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range got.Data {
		y := i / w
		exp := float32(0)
		if y >= top && y < bottom {
			exp = want[i-top*w]
		}
		if v != exp {
			t.Fatalf("value %d: the GPU's %v, the CPU's %v", i, v, exp)
		}
	}
}

func BenchmarkEyesOnTheGPU(b *testing.B) {
	g, err := nn.NewGPU()
	if err != nil {
		b.Skip("no GPU:", err)
	}
	defer g.Close()
	r := rand.New(rand.NewPCG(7, 8))
	f := randomPicture(r, 1920, 1080)
	d := nn.New(1080, 1920)
	for i := range d.Data {
		d.Data[i] = float32(math.Sin(float64(i%1920)/40)*15) - 3
	}
	buf := make([]byte, nn.EyesSize(f.Width, f.Height))
	for b.Loop() {
		p := g.UploadPicture(f.Y, f.Cb, f.Cr, f.Width, f.Height, f.StrideY, f.StrideC)
		if err := g.DownloadBytes(buf, g.Eyes(p, g.Upload(d), gapPixels)); err != nil {
			b.Fatal(err)
		}
		g.Release()
	}
}

func TestSamplesOnTheGPU(t *testing.T) {
	g, err := nn.NewGPU()
	if err != nil {
		t.Skip("no GPU:", err)
	}
	defer g.Close()
	r := rand.New(rand.NewPCG(11, 12))
	for _, c := range []struct{ w, h, top, bottom int }{{200, 120, 11, 111}, {203, 61, 0, 61}, {1920, 1080, 138, 942}} {
		d := nn.New(c.h, c.w)
		for i := range d.Data {
			d.Data[i] = r.Float32()*40 - 10
		}
		want := SamplesOf(d.Data, c.w, c.h, c.top, c.bottom)
		got, err := g.Download(g.Samples(g.Upload(d), c.w, c.h, c.top, c.bottom, sampleEdge(c.w)))
		g.Release()
		if err != nil {
			t.Fatal(err)
		}
		if n := len(want.Left) + len(want.Right) + len(want.Low); len(got.Data) != n {
			t.Fatalf("%v: %d samples, want %d", c, len(got.Data), n)
		}
		s := samplesSplit(got.Data, c.w, c.top, c.bottom)
		for _, p := range [][2][]float32{{s.Left, want.Left}, {s.Right, want.Right}, {s.Low, want.Low}} {
			for i, v := range p[1] {
				if p[0][i] != v {
					t.Fatalf("%v: sample %d the GPU's %v, the CPU's %v", c, i, p[0][i], v)
				}
			}
		}
	}
}

// The GPU's matching is the CPU's, disparity for disparity.
func TestMatchOnTheGPU(t *testing.T) {
	g, err := nn.NewGPU()
	if err != nil {
		t.Skip("no GPU:", err)
	}
	defer g.Close()
	rnd := rand.New(rand.NewPCG(3, 4)) //nolint:gosec // test data
	sl, sr, _ := redepthScene(640, 360, 6, 24)
	for _, pair := range [][2]*mvc.Frame{{sl, sr}, {randomPicture(rnd, 322, 180), randomPicture(rnd, 322, 180)}} {
		l, r := pair[0], pair[1]
		wl, wr := MatchBoth(l, r)
		gl, gr, err := MatchBothGPU(g, l, r)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct {
			name      string
			want, got *Disparity
		}{{"left", wl, gl}, {"right", wr, gr}} {
			bad, badV := 0, 0
			for i := range c.want.D {
				if c.got.D[i] != c.want.D[i] {
					bad++
				}
				if c.got.Valid[i] != c.want.Valid[i] {
					badV++
				}
			}
			if bad > 0 || badV > 0 {
				t.Errorf("%dx%d, %s eye: %d disparities and %d sure flags of %d differ", l.Width, l.Height, c.name, bad, badV, len(c.want.D))
			}
		}
	}
}

func BenchmarkMatchGPU(b *testing.B) {
	g, err := nn.NewGPU()
	if err != nil {
		b.Skip("no GPU:", err)
	}
	defer g.Close()
	l, r, _ := redepthScene(1920, 1080, 6, 24)
	for b.Loop() {
		if _, _, err := MatchBothGPU(g, l, r); err != nil {
			b.Fatal(err)
		}
	}
}
