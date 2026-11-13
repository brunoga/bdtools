// Command inpainttrain learns the hole filler (internal/stereo's
// InpaintWeights) from crops tools/stereobench -holes records: a rendered
// eye, the mask of its holes and the disc's own eye. The loss is the
// absolute error, in the holes, of the eye corrected by the filler
// against the disc's (Y, and the chroma at half weight); Adam on a GPU,
// the rate decayed along a cosine. It prints the holes' PSNR on the
// validation crops as the renderer fills them and as the filler does,
// and writes the weights after each pass.
package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/brunoga/bdtools/internal/nn"
	"github.com/brunoga/bdtools/internal/stereo"
	"github.com/brunoga/bdtools/mvc"
)

func main() {
	trainDir := flag.String("train", "", "the training crops' directory (.bhl)")
	valDir := flag.String("val", "", "the validation crops' directory")
	out := flag.String("out", "inpaint.bin", "the weights' file")
	from := flag.String("from", "", "start from these weights")
	epochs := flag.Int("epochs", 6, "passes over the training crops")
	lr := flag.Float64("lr", 1e-3, "the learning rate at the start")
	batch := flag.Int("batch", 8, "crops an update")
	limit := flag.Int("limit", 0, "use only this many training crops (0: all)")
	chroma := flag.Float64("chroma", 0.5, "the chroma's weight in the loss")
	flag.Parse()
	if *trainDir == "" || *valDir == "" {
		fail(errors.New("give -train and -val"))
	}
	g, err := nn.NewGPU()
	if err != nil {
		fail(fmt.Errorf("a GPU is needed: %w", err))
	}
	r := rand.New(rand.NewPCG(1, 1)) //nolint:gosec // repeatable
	m := newModel(r)
	if *from != "" {
		iw, err := stereo.ReadInpaint(*from)
		if err != nil {
			fail(err)
		}
		m.InpaintWeights = *iw
	}
	m.chroma = float32(*chroma)
	train, err := list(*trainDir)
	if err != nil {
		fail(err)
	}
	val, err := list(*valDir)
	if err != nil {
		fail(err)
	}
	r.Shuffle(len(train), func(i, j int) { train[i], train[j] = train[j], train[i] })
	if *limit > 0 && *limit < len(train) {
		train = train[:*limit]
	}
	fmt.Printf("%d training crops, %d validation\n", len(train), len(val))
	base, filled := validate(g, m, val)
	fmt.Printf("epoch 0: validation holes PSNR %.2f dB as rendered, %.2f filled\n", base, filled)
	steps := *epochs * ((len(train) + *batch - 1) / *batch)
	step := 0
	for e := 1; e <= *epochs; e++ {
		t0 := time.Now()
		var lossSum float64
		n, k := 0, 0
		r.Shuffle(len(train), func(i, j int) { train[i], train[j] = train[j], train[i] })
		for s := range prefetch(train) {
			if s == nil {
				continue
			}
			loss := m.step(g, s, true)
			lossSum += loss
			n++
			if k++; k == *batch {
				rate := *lr * (0.1 + 0.9*0.5*(1+math.Cos(math.Pi*float64(step)/float64(steps))))
				m.update(rate, float32(1/float64(k)))
				k = 0
				if step++; step%100 == 0 {
					fmt.Printf("  step %d: loss %.2f (%.3f s a crop)\n", step, 219*lossSum/float64(max(n, 1)), time.Since(t0).Seconds()/float64(max(n, 1)))
				}
			}
		}
		if k > 0 {
			m.update(*lr*0.1, float32(1/float64(k)))
		}
		base, filled := validate(g, m, val)
		fmt.Printf("epoch %d: loss %.2f; validation holes PSNR %.2f dB as rendered, %.2f filled (%v)\n",
			e, 219*lossSum/float64(max(n, 1)), base, filled, time.Since(t0).Round(time.Second))
		if err := save(m, *out); err != nil {
			fail(err)
		}
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "inpainttrain:", err)
	os.Exit(1)
}

func list(dir string) ([]string, error) {
	es, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, e := range es {
		if strings.HasSuffix(e.Name(), ".bhl") {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func save(m *model, path string) error {
	f, err := os.Create(path) //nolint:gosec // the operator's file
	if err != nil {
		return err
	}
	if _, err := m.WriteTo(f); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// sample is a crop: the rendered eye, its holes, and the disc's eye.
type sample struct {
	w, h  int
	eye   *mvc.Frame
	holes []bool
	disc  *mvc.Frame
	n     int // hole pixels
}

func read(path string) (*sample, error) {
	f, err := os.Open(path) //nolint:gosec // the operator's file
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReaderSize(f, 1<<20)
	magic := make([]byte, 4)
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != "BHL1" {
		return nil, fmt.Errorf("%s: not a hole crop", path)
	}
	var dims [2]uint32
	if err := binary.Read(r, binary.LittleEndian, &dims); err != nil {
		return nil, err
	}
	w, h := int(dims[0]), int(dims[1])
	frame := func() (*mvc.Frame, error) {
		fr := &mvc.Frame{Width: w, Height: h, StrideY: w, StrideC: w / 2, Y: make([]byte, w*h), Cb: make([]byte, w*h/4), Cr: make([]byte, w*h/4)}
		for _, p := range [][]byte{fr.Y, fr.Cb, fr.Cr} {
			if _, err := io.ReadFull(r, p); err != nil {
				return nil, err
			}
		}
		return fr, nil
	}
	s := &sample{w: w, h: h}
	if s.eye, err = frame(); err != nil {
		return nil, err
	}
	mask := make([]byte, w*h)
	if _, err := io.ReadFull(r, mask); err != nil {
		return nil, err
	}
	s.holes = make([]bool, w*h)
	for i, v := range mask {
		if v != 0 {
			s.holes[i] = true
			s.n++
		}
	}
	if s.disc, err = frame(); err != nil {
		return nil, err
	}
	return s, nil
}

func prefetch(paths []string) <-chan *sample {
	ch := make(chan *sample, 4)
	go func() {
		defer close(ch)
		for _, p := range paths {
			s, err := read(p)
			if err != nil {
				fmt.Fprintln(os.Stderr, "inpainttrain:", err)
				ch <- nil
				continue
			}
			ch <- s
		}
	}()
	return ch
}

// model is the filler's weights, their gradients and Adam's state.
type model struct {
	stereo.InpaintWeights
	chroma float32
	gw, gb map[string]*nn.Tensor
	mw, vw map[string]*nn.Tensor
	mb, vb map[string]*nn.Tensor
	t      int
}

// newModel is a model at its start: He's initialisation, the last layer
// zero (so the filler starts as no change).
func newModel(r *rand.Rand) *model {
	m := &model{InpaintWeights: stereo.InpaintWeights{W: map[string]*nn.Tensor{}, B: map[string]*nn.Tensor{}},
		gw: map[string]*nn.Tensor{}, gb: map[string]*nn.Tensor{}, mw: map[string]*nn.Tensor{}, vw: map[string]*nn.Tensor{},
		mb: map[string]*nn.Tensor{}, vb: map[string]*nn.Tensor{}}
	for _, l := range stereo.InpaintLayers {
		w := nn.New(l.Shape()...)
		if !l.Residual {
			fan := l.In * 9
			if l.Up {
				fan = l.In
			}
			sd := math.Sqrt(2 / float64(fan))
			for i := range w.Data {
				w.Data[i] = float32(r.NormFloat64() * sd)
			}
		}
		m.W[l.Name], m.B[l.Name] = w, nn.New(l.Out)
	}
	return m
}

// forward is the filler on a crop on a tape, its weights learnt when
// learn: the corrections [3, h, w].
func (m *model) forward(tp *nn.Tape, s *sample, learn bool) *nn.Tensor {
	iw := stereo.InpaintWeights{W: map[string]*nn.Tensor{}, B: map[string]*nn.Tensor{}}
	for _, l := range stereo.InpaintLayers {
		if learn {
			iw.W[l.Name], iw.B[l.Name] = tp.Param("w."+l.Name, m.W[l.Name]), tp.Param("b."+l.Name, m.B[l.Name])
		} else {
			iw.W[l.Name], iw.B[l.Name] = tp.Upload(m.W[l.Name]), tp.Upload(m.B[l.Name])
		}
	}
	x := stereo.InpaintInput(s.eye, s.holes, 0, s.h)
	return iw.Inpaint(tp, tp.Upload(x))
}

// step runs a crop forward (and, when train, back, adding to the
// gradients): the loss, the mean absolute error in the holes in the
// input's units (Y's, the chroma's at the chroma weight).
func (m *model) step(g *nn.GPU, s *sample, train bool) float64 {
	defer g.Release()
	tp := nn.NewTape(g)
	out := m.forward(tp, s, train)
	d, err := g.Download(out)
	if err != nil {
		fail(err)
	}
	loss, grad := m.loss(s, d.Data)
	if !train {
		return loss
	}
	tp.Backward(out, g.Upload(grad))
	for name, gt := range tp.Grads() {
		dg, err := g.Download(gt)
		if err != nil {
			fail(err)
		}
		dst := m.gw
		if strings.HasPrefix(name, "b.") {
			dst = m.gb
		}
		key := name[2:]
		if dst[key] == nil {
			dst[key] = nn.New(dg.Shape...)
		}
		for i, v := range dg.Data {
			dst[key].Data[i] += v
		}
	}
	return loss
}

// loss is a crop's loss given the corrections delta ([3, h, w]) and its
// gradient there: the absolute error of the corrected eye against the
// disc's in the holes, Y in Y's units and the chroma in its own at
// m.chroma, each over the holes' count.
func (m *model) loss(s *sample, delta []float32) (float64, *nn.Tensor) {
	w, h := s.w, s.h
	grad := nn.New(3, h, w)
	if s.n == 0 {
		return 0, grad
	}
	inv := 1 / float32(s.n)
	var loss float64
	for r := range h {
		for c := range w {
			i := r*w + c
			if !s.holes[i] {
				continue
			}
			ci := (r/2)*s.eye.StrideC + c/2
			e := (float32(s.eye.Y[i])-16)/219 + delta[i] - (float32(s.disc.Y[i])-16)/219
			loss += float64(abs(e))
			grad.Data[i] = sign(e) * inv
			ecb := (float32(s.eye.Cb[ci])-128)/112 + delta[h*w+i] - (float32(s.disc.Cb[ci])-128)/112
			ecr := (float32(s.eye.Cr[ci])-128)/112 + delta[2*h*w+i] - (float32(s.disc.Cr[ci])-128)/112
			loss += float64(m.chroma * (abs(ecb) + abs(ecr)))
			grad.Data[h*w+i] = m.chroma * sign(ecb) * inv
			grad.Data[2*h*w+i] = m.chroma * sign(ecr) * inv
		}
	}
	return loss / float64(s.n), grad
}

func abs(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

func sign(v float32) float32 {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}

// update is Adam's step on the gradients times scale, which it then zeroes.
func (m *model) update(lr float64, scale float32) {
	const b1, b2, eps = 0.9, 0.999, 1e-8
	m.t++
	c1 := 1 - math.Pow(b1, float64(m.t))
	c2 := 1 - math.Pow(b2, float64(m.t))
	for _, l := range stereo.InpaintLayers {
		for _, set := range []struct {
			p, g   *nn.Tensor
			mv, vv map[string]*nn.Tensor
		}{{m.W[l.Name], m.gw[l.Name], m.mw, m.vw}, {m.B[l.Name], m.gb[l.Name], m.mb, m.vb}} {
			if set.g == nil {
				continue
			}
			if set.mv[l.Name] == nil {
				set.mv[l.Name], set.vv[l.Name] = nn.New(set.p.Shape...), nn.New(set.p.Shape...)
			}
			p, mv, vv := set.p.Data, set.mv[l.Name].Data, set.vv[l.Name].Data
			for i := range p {
				gi := set.g.Data[i] * scale
				mv[i] = b1*mv[i] + (1-b1)*gi
				vv[i] = b2*vv[i] + (1-b2)*gi*gi
				p[i] -= float32(lr * (float64(mv[i]) / c1) / (math.Sqrt(float64(vv[i])/c2) + eps))
			}
			clear(set.g.Data)
		}
	}
}

// validate is the holes' luma PSNR over the validation crops, as
// rendered and as the filler corrects them.
func validate(g *nn.GPU, m *model, paths []string) (base, filled float64) {
	var se0, se1, n float64
	for s := range prefetch(paths) {
		if s == nil || s.n == 0 {
			continue
		}
		var d *nn.Tensor
		func() {
			defer g.Release()
			var err error
			if d, err = g.Download(m.forward(nn.NewTape(g), s, false)); err != nil {
				fail(err)
			}
		}()
		eye := &mvc.Frame{Width: s.w, Height: s.h, StrideY: s.w, StrideC: s.w / 2,
			Y: append([]byte(nil), s.eye.Y...), Cb: append([]byte(nil), s.eye.Cb...), Cr: append([]byte(nil), s.eye.Cr...)}
		stereo.ApplyInpaint(eye, s.holes, d.Data, 0, s.h)
		for i, hole := range s.holes {
			if !hole {
				continue
			}
			e0 := float64(s.eye.Y[i]) - float64(s.disc.Y[i])
			e1 := float64(eye.Y[i]) - float64(s.disc.Y[i])
			se0 += e0 * e0
			se1 += e1 * e1
			n++
		}
	}
	psnr := func(mse float64) float64 { return 10 * math.Log10(255*255/max(mse, 1e-9)) }
	return psnr(se0 / max(n, 1)), psnr(se1 / max(n, 1))
}
