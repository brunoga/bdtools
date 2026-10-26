package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brunoga/bdtools/internal/nn"
	"github.com/brunoga/bdtools/internal/stereo"
)

// -fill: the hole filler (stereo.FillLayers) learnt from the crops
// stereobench -fill-samples writes: each crop's holes, as Render filled
// them, corrected towards the disc's own eye there. The loss, in the holes
// only: the error of Y, half that of Cb and Cr, and half the error of Y's
// steps across a row (so that a flat fill, which a mean would give, costs
// what the texture it misses is worth).

// fillSample is a crop: the filler's input [5, n, n], its holes, and the
// disc's eye there (Y, Cb, Cr as the input has them).
type fillSample struct {
	in    *nn.Tensor
	holes []bool
	want  [3][]float32
	n     int
}

func readFill(path string) (*fillSample, error) {
	f, err := os.Open(path) //nolint:gosec // the operator's samples
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReaderSize(f, 1<<18)
	var head [4]byte
	var size uint32
	if _, err := io.ReadFull(r, head[:]); err != nil || string(head[:]) != "BFS1" {
		return nil, fmt.Errorf("%s: not a filler crop", path)
	}
	if err := binary.Read(r, binary.LittleEndian, &size); err != nil {
		return nil, err
	}
	n := int(size)
	N := n * n
	pix := make([]byte, 3*N)
	mask := make([]byte, N)
	d := make([]float32, N)
	want := make([]byte, 3*N)
	if err := errors.Join(binary.Read(r, binary.LittleEndian, pix), binary.Read(r, binary.LittleEndian, mask),
		binary.Read(r, binary.LittleEndian, d), binary.Read(r, binary.LittleEndian, want)); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s := &fillSample{in: nn.New(stereo.FillChannels, n, n), holes: make([]bool, N), n: n}
	norm := func(v byte, c int) float32 {
		if c == 0 {
			return (float32(v)-16)/219 - 0.5
		}
		return (float32(v) - 128) / 112
	}
	for c := range 3 {
		s.want[c] = make([]float32, N)
		for i := range N {
			s.in.Data[c*N+i] = norm(pix[c*N+i], c)
			s.want[c][i] = norm(want[c*N+i], c)
		}
	}
	for i := range N {
		if mask[i] != 0 {
			s.holes[i] = true
			s.in.Data[3*N+i] = 1
		}
		s.in.Data[4*N+i] = d[i] / stereo.FillDisparityScale
	}
	return s, nil
}

// flip mirrors a crop left to right: the other eye's holes (the left
// eye's open on the other side of what is nearer), as the conversion
// makes both. The disparity's sign is the depth's, which a mirror keeps.
func (s *fillSample) flip() {
	n := s.n
	rev := func(p []float32) {
		for r := range n {
			row := p[r*n : (r+1)*n]
			for a, b := 0, n-1; a < b; a, b = a+1, b-1 {
				row[a], row[b] = row[b], row[a]
			}
		}
	}
	for c := range stereo.FillChannels {
		rev(s.in.Data[c*n*n : (c+1)*n*n])
	}
	for c := range 3 {
		rev(s.want[c])
	}
	for r := range n {
		row := s.holes[r*n : (r+1)*n]
		for a, b := 0, n-1; a < b; a, b = a+1, b-1 {
			row[a], row[b] = row[b], row[a]
		}
	}
}

// fillLoss is the loss of a crop's correction (out, [3, n, n]) and its
// gradient; false when the crop has no holes.
func fillLoss(s *fillSample, out []float32) (g *nn.Tensor, loss float64, ok bool) {
	n := s.n
	N := n * n
	g = nn.New(3, n, n)
	var holes float64
	for _, h := range s.holes {
		if h {
			holes++
		}
	}
	if holes == 0 {
		return nil, 0, false
	}
	made := func(c, i int) float32 { return s.in.Data[c*N+i] + out[c*N+i] }
	for c, wt := range []float32{1, 0.5, 0.5} {
		for i, h := range s.holes {
			if !h {
				continue
			}
			e := made(c, i) - s.want[c][i]
			loss += float64(wt) * float64(e) * float64(e)
			g.Data[c*N+i] += 2 * wt * e
		}
	}
	// Y's steps across a row, where either end is a hole (the other the
	// picture as it is when it is not one)
	const wg = 0.5
	for r := range n {
		for x := range n - 1 {
			i, j := r*n+x, r*n+x+1
			if !s.holes[i] && !s.holes[j] {
				continue
			}
			yi, yj := s.in.Data[i], s.in.Data[j]
			if s.holes[i] {
				yi = made(0, i)
			}
			if s.holes[j] {
				yj = made(0, j)
			}
			e := (yj - yi) - (s.want[0][j] - s.want[0][i])
			loss += wg * float64(e) * float64(e)
			if s.holes[j] {
				g.Data[j] += 2 * wg * e
			}
			if s.holes[i] {
				g.Data[i] -= 2 * wg * e
			}
		}
	}
	inv := float32(1 / holes)
	for i := range g.Data {
		g.Data[i] *= inv
	}
	return g, loss / holes, true
}

// fillModel is the filler's weights, their gradients and Adam's state.
type fillModel struct {
	stereo.FillWeights
	gw, gb         map[string]*nn.Tensor
	mw, vw, mb, vb map[string][]float32
	t              int
}

// newFillModel is a filler at its start: He's initialisation, the last
// layer zero (so it starts as Render's filling).
func newFillModel(r *rand.Rand) *fillModel {
	m := &fillModel{FillWeights: stereo.FillWeights{W: map[string]*nn.Tensor{}, B: map[string]*nn.Tensor{}}}
	for _, l := range stereo.FillLayers {
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
	m.adamInit()
	return m
}

func (m *fillModel) adamInit() {
	m.mw, m.vw, m.mb, m.vb = map[string][]float32{}, map[string][]float32{}, map[string][]float32{}, map[string][]float32{}
	for _, l := range stereo.FillLayers {
		n := len(m.W[l.Name].Data)
		m.mw[l.Name], m.vw[l.Name] = make([]float32, n), make([]float32, n)
		m.mb[l.Name], m.vb[l.Name] = make([]float32, l.Out), make([]float32, l.Out)
	}
}

func (m *fillModel) zeroGrad() {
	m.gw, m.gb = map[string]*nn.Tensor{}, map[string]*nn.Tensor{}
	for _, l := range stereo.FillLayers {
		m.gw[l.Name], m.gb[l.Name] = nn.New(l.Shape()...), nn.New(l.Out)
	}
}

// fillGrads is what a step needs of a backend beyond nn.Backend: the
// backward passes (the GPU's, or the CPU's of internal/nn).
type fillGrads interface {
	nn.Backend
	Conv2dBackward(x, w, dy *nn.Tensor, stride, pad int) (dx, dw, db *nn.Tensor)
	ConvTranspose2dKBackward(x, w, dy *nn.Tensor) (dx, dw, db *nn.Tensor)
	ReLUBackward(y, dy *nn.Tensor) *nn.Tensor
}

// cpuGrads is the CPU as a fillGrads.
type cpuGrads struct{ nn.Backend }

func (cpuGrads) Conv2dBackward(x, w, dy *nn.Tensor, stride, pad int) (dx, dw, db *nn.Tensor) {
	return nn.Conv2dBackward(x, w, dy, stride, pad)
}

func (cpuGrads) ConvTranspose2dKBackward(x, w, dy *nn.Tensor) (dx, dw, db *nn.Tensor) {
	return nn.ConvTranspose2dKBackward(x, w, dy)
}

func (cpuGrads) ReLUBackward(y, dy *nn.Tensor) *nn.Tensor { return nn.ReLUBackward(y, dy) }

// step runs a crop forward and back on be, adding to the gradients: the
// loss, and whether it counted.
func (m *fillModel) step(be fillGrads, s *fillSample) (float64, bool) {
	defer be.Release()
	w, b := map[string]*nn.Tensor{}, map[string]*nn.Tensor{}
	for _, l := range stereo.FillLayers {
		w[l.Name], b[l.Name] = be.Upload(m.W[l.Name]), be.Upload(m.B[l.Name])
	}
	ins := map[string]*nn.Tensor{}
	outs := map[string]*nn.Tensor{}
	h := be.Upload(s.in)
	for _, l := range stereo.FillLayers {
		ins[l.Name] = h
		if l.Up {
			h = be.ReLU(be.Add(be.ConvTranspose2dK(h, w[l.Name], b[l.Name]), outs[stereo.FillSkips[l.Name]]))
		} else {
			h = be.Conv2d(h, w[l.Name], b[l.Name], l.Stride, 1)
			if !l.Residual {
				h = be.ReLU(h)
			}
		}
		outs[l.Name] = h
	}
	out, err := be.Download(h)
	if err != nil {
		fail(err)
	}
	gc, loss, ok := fillLoss(s, out.Data)
	if !ok {
		return 0, false
	}
	down := func(t *nn.Tensor) *nn.Tensor {
		d, err := be.Download(t)
		if err != nil {
			fail(err)
		}
		return d
	}
	gr := be.Upload(gc)
	skipGrad := map[string]*nn.Tensor{}
	for i := len(stereo.FillLayers) - 1; i >= 0; i-- {
		l := stereo.FillLayers[i]
		if sg := skipGrad[l.Name]; sg != nil {
			gr = be.Add(gr, sg)
		}
		var dx, dw, db *nn.Tensor
		if l.Up {
			gr = be.ReLUBackward(outs[l.Name], gr)
			skipGrad[stereo.FillSkips[l.Name]] = be.Clone(gr)
			dx, dw, db = be.ConvTranspose2dKBackward(ins[l.Name], w[l.Name], gr)
		} else {
			if !l.Residual {
				gr = be.ReLUBackward(outs[l.Name], gr)
			}
			dx, dw, db = be.Conv2dBackward(ins[l.Name], w[l.Name], gr, l.Stride, 1)
		}
		acc(m.gw[l.Name], down(dw))
		acc(m.gb[l.Name], down(db))
		gr = dx
	}
	return loss, true
}

// update takes an Adam step of the gradients times scale.
func (m *fillModel) update(lr float64, scale float32) {
	const b1, b2, eps = 0.9, 0.999, 1e-8
	m.t++
	c1 := 1 - math.Pow(b1, float64(m.t))
	c2 := 1 - math.Pow(b2, float64(m.t))
	upd := func(p, g, mv, vv []float32) {
		for i := range p {
			gi := g[i] * scale
			mv[i] = b1*mv[i] + (1-b1)*gi
			vv[i] = b2*vv[i] + (1-b2)*gi*gi
			p[i] -= float32(lr * (float64(mv[i]) / c1) / (math.Sqrt(float64(vv[i])/c2) + eps))
		}
	}
	for _, l := range stereo.FillLayers {
		upd(m.W[l.Name].Data, m.gw[l.Name].Data, m.mw[l.Name], m.vw[l.Name])
		upd(m.B[l.Name].Data, m.gb[l.Name].Data, m.mb[l.Name], m.vb[l.Name])
	}
}

// validateFill is the validation crops' PSNR (luma) in the holes, as
// Render fills them and as the filler corrects them.
func validateFill(m *fillModel, be nn.Backend, paths []string) (base, filled float64) {
	var se0, se1, n float64
	for _, p := range paths {
		s, err := readFill(p)
		if err != nil {
			fail(err)
		}
		t, err := be.Download(m.Fill(be, be.Upload(s.in)))
		if err != nil {
			fail(err)
		}
		be.Release()
		for i, h := range s.holes {
			if !h {
				continue
			}
			e0 := float64(s.in.Data[i]-s.want[0][i]) * 219
			e1 := float64(s.in.Data[i]+t.Data[i]-s.want[0][i]) * 219
			se0 += e0 * e0
			se1 += e1 * e1
			n++
		}
	}
	p := func(se float64) float64 { return 10 * math.Log10(255*255/max(se/max(n, 1), 1e-12)) }
	return p(se0), p(se1)
}

func listFill(dir string) ([]string, error) {
	es, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range es {
		if strings.HasSuffix(e.Name(), ".bfs") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out, nil
}

func prefetchFill(paths []string) <-chan *fillSample {
	ch := make(chan *fillSample, 64)
	go func() {
		for _, p := range paths {
			s, err := readFill(p)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				ch <- nil
				continue
			}
			ch <- s
		}
		close(ch)
	}()
	return ch
}

func saveFill(m *fillModel, path string) error {
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

// trainFill learns the filler.
func trainFill(trainDir, valDir, out, from string, epochs int, lr float64, batch, limit int) {
	train, err := listFill(trainDir)
	if err != nil {
		fail(err)
	}
	val, err := listFill(valDir)
	if err != nil {
		fail(err)
	}
	r := rand.New(rand.NewPCG(1, 1)) //nolint:gosec // shuffling and initialising, repeatably
	r.Shuffle(len(train), func(i, j int) { train[i], train[j] = train[j], train[i] })
	if limit > 0 && limit < len(train) {
		train = train[:limit]
	}
	m := newFillModel(r)
	if from != "" {
		w, err := stereo.ReadFill(from)
		if err != nil {
			fail(err)
		}
		m.FillWeights = *w
	}
	var be fillGrads = cpuGrads{nn.CPU}
	var vbe = nn.CPU
	if g, err := nn.NewGPU(); err == nil {
		defer g.Close()
		be, vbe = g, g
		fmt.Printf("training on %s\n", g.Name())
	}
	fmt.Printf("the filler: %d training crops, %d validation\n", len(train), len(val))
	base, filled := validateFill(m, vbe, val)
	fmt.Printf("epoch 0: validation PSNR in the holes %.3f dB as Render fills them, %.3f filled\n", base, filled)
	steps := epochs * ((len(train) + batch - 1) / batch)
	step := 0
	for e := 1; e <= epochs; e++ {
		r.Shuffle(len(train), func(i, j int) { train[i], train[j] = train[j], train[i] })
		t0 := time.Now()
		var lossSum float64
		var n int
		samples := prefetchFill(train)
		for b := 0; b < len(train); b += batch {
			m.zeroGrad()
			k := 0
			for range min(batch, len(train)-b) {
				s := <-samples
				if s == nil {
					continue
				}
				if r.IntN(2) == 1 {
					s.flip()
				}
				if loss, ok := m.step(be, s); ok {
					lossSum += loss
					n++
					k++
				}
			}
			if k == 0 {
				continue
			}
			rate := lr * (0.1 + 0.9*0.5*(1+math.Cos(math.Pi*float64(step)/float64(steps))))
			m.update(rate, float32(1/float64(k)))
			step++
			if step%500 == 0 {
				fmt.Printf("  step %d: loss %.4f (%.3f s a crop)\n", step, lossSum/float64(max(n, 1)), time.Since(t0).Seconds()/float64(max(n, 1)))
			}
		}
		base, filled := validateFill(m, vbe, val)
		fmt.Printf("epoch %d: loss %.4f; validation PSNR in the holes %.3f dB as Render fills them, %.3f filled (%v)\n",
			e, lossSum/float64(max(n, 1)), base, filled, time.Since(t0).Round(time.Second))
		if err := saveFill(m, out); err != nil {
			fail(err)
		}
	}
}
