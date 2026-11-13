package main

import (
	"image"
	"image/color"
	"image/png"
	"math/rand/v2"
	"os"
	"testing"

	"github.com/brunoga/bdtools/internal/nn"
	"github.com/brunoga/bdtools/internal/stereo"
	"github.com/brunoga/bdtools/mvc"
)

// synthetic is a crop whose disc eye is the rendered one lifted by lift
// in the holes: a filler should learn the lift.
func synthetic(r *rand.Rand, lift byte) *sample {
	w, h := 64, 64
	mk := func() *mvc.Frame {
		return &mvc.Frame{Width: w, Height: h, StrideY: w, StrideC: w / 2, Y: make([]byte, w*h), Cb: make([]byte, w*h/4), Cr: make([]byte, w*h/4)}
	}
	s := &sample{w: w, h: h, eye: mk(), disc: mk(), holes: make([]bool, w*h)}
	for i := range s.eye.Y {
		s.eye.Y[i] = byte(40 + r.IntN(150))
		s.disc.Y[i] = s.eye.Y[i]
		if (i/w)%8 < 3 && (i%w)%16 < 5 {
			s.holes[i] = true
			s.disc.Y[i] += lift
			s.n++
		}
	}
	for i := range s.eye.Cb {
		s.eye.Cb[i], s.eye.Cr[i] = 128, 128
		s.disc.Cb[i], s.disc.Cr[i] = 128, 128
	}
	return s
}

// TestLearnsALift: the trainer's gradients move the filler towards a
// constant lift in the holes, and finite differences agree with them.
func TestLearnsALift(t *testing.T) {
	g, err := nn.NewGPU()
	if err != nil {
		t.Skip("no GPU")
	}
	defer g.Close()
	r := rand.New(rand.NewPCG(3, 4))
	m := newModel(r)
	m.chroma, m.l1 = 0.5, true
	s := synthetic(r, 30)
	// the gradient at the output bias against a finite difference
	l0 := m.step(g, s, true)
	gb := float64(m.gb["out"].Data[0])
	const eps = 1e-3
	m.B["out"].Data[0] += eps
	l1 := m.step(g, s, false)
	m.B["out"].Data[0] -= eps
	fd := (l1 - l0) / eps
	t.Logf("loss %.4f, bias gradient %.4f, finite difference %.4f", l0, gb, fd)
	if fd == 0 || gb/fd < 0.8 || gb/fd > 1.25 {
		t.Fatalf("the output bias's gradient %g against the finite difference %g", gb, fd)
	}
	for name := range m.gw {
		clear(m.gw[name].Data)
		clear(m.gb[name].Data)
	}
	// a few hundred updates: the loss falls well below the start
	start := m.step(g, s, false)
	for i := 0; i < 300; i++ {
		m.step(g, s, true)
		m.update(2e-3, 1)
	}
	end := m.step(g, s, false)
	t.Logf("loss %.4f -> %.4f", start, end)
	if end > start/3 {
		t.Fatalf("the loss did not fall: %.4f -> %.4f", start, end)
	}
}

// TestOverfitsACrop: a real crop (INPAINT_CROP) is learnt by heart.
func TestOverfitsACrop(t *testing.T) {
	path := os.Getenv("INPAINT_CROP")
	if path == "" {
		t.Skip("set INPAINT_CROP to a .bhl file")
	}
	g, err := nn.NewGPU()
	if err != nil {
		t.Skip("no GPU")
	}
	defer g.Close()
	s, err := read(path)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(rand.New(rand.NewPCG(3, 4)))
	m.chroma = 0.5
	start := m.step(g, s, false)
	for i := 0; i < 300; i++ {
		l := m.step(g, s, true)
		m.update(2e-3, 1)
		if i%50 == 0 {
			t.Logf("step %d: loss %.4f", i, 219*l)
		}
	}
	end := m.step(g, s, false)
	t.Logf("holes %d: loss %.3f -> %.3f (Y units)", s.n, 219*start, 219*end)
	if end > start/2 {
		t.Fatalf("the loss did not fall by half")
	}
}

// TestDumpCrop writes a crop (INPAINT_CROP) as a PNG (INPAINT_PNG): the
// rendered eye, its holes and the disc's eye side by side, to look at.
func TestDumpCrop(t *testing.T) {
	path, out := os.Getenv("INPAINT_CROP"), os.Getenv("INPAINT_PNG")
	if path == "" || out == "" {
		t.Skip("set INPAINT_CROP and INPAINT_PNG")
	}
	s, err := read(path)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewGray(image.Rect(0, 0, 3*s.w, s.h))
	for y := range s.h {
		for x := range s.w {
			img.SetGray(x, y, color.Gray{s.eye.Y[y*s.w+x]})
			v := byte(0)
			if s.holes[y*s.w+x] {
				v = 255
			}
			img.SetGray(s.w+x, y, color.Gray{v})
			img.SetGray(2*s.w+x, y, color.Gray{s.disc.Y[y*s.w+x]})
		}
	}
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// TestDumpFilled writes a crop (INPAINT_CROP) filled by weights
// (INPAINT_WEIGHTS) as a PNG (INPAINT_PNG): rendered, filled, the disc.
func TestDumpFilled(t *testing.T) {
	path, out, wpath := os.Getenv("INPAINT_CROP"), os.Getenv("INPAINT_PNG"), os.Getenv("INPAINT_WEIGHTS")
	if path == "" || out == "" || wpath == "" {
		t.Skip("set INPAINT_CROP, INPAINT_PNG and INPAINT_WEIGHTS")
	}
	g, err := nn.NewGPU()
	if err != nil {
		t.Skip("no GPU")
	}
	defer g.Close()
	s, err := read(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := stereo.ReadInpaint(wpath)
	if err != nil {
		t.Fatal(err)
	}
	filled := &mvc.Frame{Width: s.w, Height: s.h, StrideY: s.w, StrideC: s.w / 2,
		Y: append([]byte(nil), s.eye.Y...), Cb: append([]byte(nil), s.eye.Cb...), Cr: append([]byte(nil), s.eye.Cr...)}
	if err := stereo.NewFiller(w).Fill(g, filled, s.holes, 0, s.h); err != nil {
		t.Fatal(err)
	}
	g.Release()
	img := image.NewGray(image.Rect(0, 0, 3*s.w, s.h))
	for y := range s.h {
		for x := range s.w {
			img.SetGray(x, y, color.Gray{s.eye.Y[y*s.w+x]})
			img.SetGray(s.w+x, y, color.Gray{filled.Y[y*s.w+x]})
			img.SetGray(2*s.w+x, y, color.Gray{s.disc.Y[y*s.w+x]})
		}
	}
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}
