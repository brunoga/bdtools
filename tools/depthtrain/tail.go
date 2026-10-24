package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"math/rand/v2"
	"time"

	"github.com/brunoga/bdtools/internal/depth"
	"github.com/brunoga/bdtools/internal/nn"
)

// Learning the depth network's tail (-tail CHECKPOINT): the head's last
// stages (depth.TailNames) from the inputs stereobench -samples recorded
// beside each sample (NAME.bht), the encoder and the rest of the head as
// the checkpoint has them. The loss is the refinement's (the error left
// after fitting the depth to the disc's disparity a picture at a time),
// plus a term holding the depth's mean and deviation to the network's
// own, so the depth's mapping to disparity (stereo.Shape) still holds.

// tailMoments is the weight of the moments' term.
const tailMoments = 1.0

// tailSample is a sample with its tail inputs.
type tailSample struct {
	*sample
	in   [3]*nn.Tensor
	h, w int // the network's input
}

func readTail(path string) (*tailSample, error) {
	s, err := read(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(strings.TrimSuffix(path, ".bds") + ".bht") //nolint:gosec // the operator's files
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReaderSize(f, 1<<20)
	var hdr struct {
		Magic [4]byte
		H, W  uint32
	}
	if err := binary.Read(r, binary.LittleEndian, &hdr); err != nil || string(hdr.Magic[:]) != "BHT1" {
		return nil, fmt.Errorf("%s: not tail inputs", path)
	}
	t := &tailSample{sample: s, h: int(hdr.H), w: int(hdr.W)}
	for i := range t.in {
		var nd uint32
		if err := binary.Read(r, binary.LittleEndian, &nd); err != nil || nd > 4 {
			return nil, fmt.Errorf("%s: bad tail inputs", path)
		}
		dims := make([]uint32, nd)
		if err := binary.Read(r, binary.LittleEndian, dims); err != nil {
			return nil, err
		}
		shape := make([]int, nd)
		for j, d := range dims {
			shape[j] = int(d)
		}
		x := nn.New(shape...)
		if err := binary.Read(r, binary.LittleEndian, x.Data); err != nil {
			return nil, err
		}
		t.in[i] = x
	}
	return t, nil
}

func prefetchTail(paths []string) <-chan *tailSample {
	ch := make(chan *tailSample, 4)
	go func() {
		for _, p := range paths {
			s, err := readTail(p)
			if err != nil {
				fmt.Fprintln(os.Stderr, "depthtrain:", err)
			}
			ch <- s
		}
		close(ch)
	}()
	return ch
}

// tape is the forward pass's record, run backwards for the gradients.
type tape struct {
	g    *nn.GPU
	back []func()
	grad map[*nn.Tensor]*nn.Tensor // the gradient at each tensor
	w    map[string]*nn.Tensor     // the weights, on the GPU
	gw   map[string]*nn.Tensor     // their gradients, on the GPU
}

// addGrad adds d to t's gradient.
func (tp *tape) addGrad(t, d *nn.Tensor) {
	if old := tp.grad[t]; old != nil {
		tp.g.Add(old, d)
		return
	}
	tp.grad[t] = d
}

func (tp *tape) addW(name string, d *nn.Tensor) {
	if d == nil {
		return
	}
	if old := tp.gw[name]; old != nil {
		tp.g.Add(old, d)
		return
	}
	tp.gw[name] = d
}

func (tp *tape) conv(x *nn.Tensor, name string, stride, pad int) *nn.Tensor {
	w, b := tp.w[name+".weight"], tp.w[name+".bias"]
	y := tp.g.Conv2d(x, w, b, stride, pad)
	tp.back = append(tp.back, func() {
		dy := tp.grad[y]
		if dy == nil {
			return
		}
		dx, dw, db := tp.g.Conv2dBackward(x, w, dy, stride, pad)
		tp.addGrad(x, dx)
		tp.addW(name+".weight", dw)
		if b != nil {
			tp.addW(name+".bias", db)
		}
	})
	return y
}

func (tp *tape) convT(x *nn.Tensor, name string) *nn.Tensor {
	w, b := tp.w[name+".weight"], tp.w[name+".bias"]
	y := tp.g.ConvTranspose2dK(x, w, b)
	tp.back = append(tp.back, func() {
		dy := tp.grad[y]
		if dy == nil {
			return
		}
		dx, dw, db := tp.g.ConvTranspose2dKBackward(x, w, dy)
		tp.addGrad(x, dx)
		tp.addW(name+".weight", dw)
		if b != nil {
			tp.addW(name+".bias", db)
		}
	})
	return y
}

func (tp *tape) relu(x *nn.Tensor) *nn.Tensor {
	y := tp.g.ReLU(tp.g.Clone(x))
	tp.back = append(tp.back, func() {
		if dy := tp.grad[y]; dy != nil {
			tp.addGrad(x, tp.g.ReLUBackward(y, tp.g.Clone(dy)))
		}
	})
	return y
}

func (tp *tape) add(a, b *nn.Tensor) *nn.Tensor {
	y := tp.g.Add(tp.g.Clone(a), b)
	tp.back = append(tp.back, func() {
		if dy := tp.grad[y]; dy != nil {
			tp.addGrad(a, tp.g.Clone(dy))
			tp.addGrad(b, tp.g.Clone(dy))
		}
	})
	return y
}

func (tp *tape) bilinear(x *nn.Tensor, oh, ow int) *nn.Tensor {
	y := tp.g.Bilinear(x, oh, ow)
	tp.back = append(tp.back, func() {
		if dy := tp.grad[y]; dy != nil {
			tp.addGrad(x, tp.g.BilinearBackward(dy, x.Dim(1), x.Dim(2)))
		}
	})
	return y
}

// residual is depth's ResidualConvUnit: x + conv2(relu(conv1(relu(x)))).
func (tp *tape) residual(pre string, x *nn.Tensor) *nn.Tensor {
	h := tp.conv(tp.relu(x), pre+"conv1", 1, 1)
	h = tp.conv(tp.relu(h), pre+"conv2", 1, 1)
	return tp.add(h, x)
}

// fusion is depth's FeatureFusionBlock.
func (tp *tape) fusion(k int, x, skip *nn.Tensor, oh, ow int) *nn.Tensor {
	pre := fmt.Sprintf("head.scratch.refinenet%d.", k)
	out := x
	if skip != nil {
		out = tp.add(x, tp.residual(pre+"resConfUnit1.", skip))
	}
	out = tp.residual(pre+"resConfUnit2.", out)
	out = tp.bilinear(out, oh, ow)
	return tp.conv(out, pre+"out_conv", 1, 0)
}

// forward is the tail (as depth's head has it from P3 on) and Infer's
// last step: the depth, [1, H, W].
func (tp *tape) forward(t *tailSample) *nn.Tensor {
	f0, f1, p3 := tp.g.Upload(t.in[0]), tp.g.Upload(t.in[1]), tp.g.Upload(t.in[2])
	l0 := tp.convT(tp.conv(f0, "head.projects.0", 1, 0), "head.resize_layers.0")
	l1 := tp.convT(tp.conv(f1, "head.projects.1", 1, 0), "head.resize_layers.1")
	rn0 := tp.conv(l0, "head.scratch.layer1_rn", 1, 1)
	rn1 := tp.conv(l1, "head.scratch.layer2_rn", 1, 1)
	p2 := tp.fusion(2, p3, rn1, rn0.Dim(1), rn0.Dim(2))
	p1 := tp.fusion(1, p2, rn0, 2*rn0.Dim(1), 2*rn0.Dim(2))
	out := tp.conv(p1, "head.scratch.output_conv1", 1, 1)
	out = tp.bilinear(out, t.h, t.w)
	out = tp.relu(tp.conv(out, "head.scratch.output_conv2.0", 1, 1))
	out = tp.relu(tp.conv(out, "head.scratch.output_conv2.2", 1, 0))
	return tp.relu(tp.bilinear(out, t.h, t.w))
}

// tailModel is the tail's weights (on the CPU), their gradients, and the
// optimiser's state.
type tailModel struct {
	w, gw  map[string]*nn.Tensor
	m, v   map[string][]float32
	t      int
	names  []string
	loaded map[string]*nn.Tensor // the whole checkpoint
}

func newTailModel(ckpt string) (*tailModel, error) {
	all, err := nn.LoadPTH(ckpt)
	if err != nil {
		return nil, err
	}
	tm := &tailModel{w: map[string]*nn.Tensor{}, gw: map[string]*nn.Tensor{}, m: map[string][]float32{}, v: map[string][]float32{}, loaded: all}
	for _, n := range depth.TailNames {
		for _, suf := range []string{".weight", ".bias"} {
			if t, ok := all[n+suf]; ok {
				tm.w[n+suf] = t.Clone()
				tm.names = append(tm.names, n+suf)
				tm.m[n+suf], tm.v[n+suf] = make([]float32, len(t.Data)), make([]float32, len(t.Data))
			}
		}
	}
	return tm, nil
}

// step is a sample forward and back, its gradients added to gw: the loss.
func (tm *tailModel) step(g *nn.GPU, t *tailSample, train bool) (float64, bool) {
	defer g.Release()
	tp := &tape{g: g, grad: map[*nn.Tensor]*nn.Tensor{}, w: map[string]*nn.Tensor{}, gw: map[string]*nn.Tensor{}}
	for _, n := range tm.names {
		tp.w[n] = g.Upload(tm.w[n])
	}
	d := tp.forward(t)
	d = tp.bilinear(d, t.sample.h, t.sample.w) // the sample's size (half the picture's)
	out, err := g.Download(d)
	if err != nil {
		fail(err)
	}
	zero := make([]float32, len(out.Data))
	gc, loss, ok := lossGrad(t.sample, out.Data, zero, t.sample.h, t.sample.w)
	if !ok {
		return 0, false
	}
	// the moments' term: (mean - mean0)^2 + (sd - sd0)^2, over sd0^2
	m0, s0 := moments(t.depth)
	m1, s1 := moments(out.Data)
	n := float64(len(out.Data))
	loss += tailMoments * ((m1-m0)*(m1-m0) + (s1-s0)*(s1-s0)) / (s0 * s0)
	if !train {
		return loss, true
	}
	for i, v := range out.Data {
		dm := 2 * (m1 - m0) / n
		ds := 2 * (s1 - s0) * (float64(v) - m1) / (n * max(s1, 1e-9))
		gc.Data[i] += float32(tailMoments * (dm + ds) / (s0 * s0))
	}
	tp.grad[d] = g.Upload(gc.Reshape(1, t.sample.h, t.sample.w))
	for i := len(tp.back) - 1; i >= 0; i-- {
		tp.back[i]()
	}
	for n, gt := range tp.gw {
		dg, err := g.Download(gt)
		if err != nil {
			fail(err)
		}
		if tm.gw[n] == nil {
			tm.gw[n] = nn.New(dg.Shape...)
		}
		acc(tm.gw[n], dg)
	}
	return loss, true
}

func moments(v []float32) (mean, sd float64) {
	var s, ss float64
	for _, x := range v {
		s += float64(x)
		ss += float64(x) * float64(x)
	}
	n := float64(max(len(v), 1))
	mean = s / n
	return mean, math.Sqrt(max(ss/n-mean*mean, 1e-12))
}

// update is Adam's step on the gradients times scale, which it then zeroes.
func (tm *tailModel) update(lr float64, scale float32) {
	const b1, b2, eps = 0.9, 0.999, 1e-8
	tm.t++
	c1 := 1 - math.Pow(b1, float64(tm.t))
	c2 := 1 - math.Pow(b2, float64(tm.t))
	for _, n := range tm.names {
		g := tm.gw[n]
		if g == nil {
			continue
		}
		p, mv, vv := tm.w[n].Data, tm.m[n], tm.v[n]
		for i := range p {
			gi := g.Data[i] * scale
			mv[i] = b1*mv[i] + (1-b1)*gi
			vv[i] = b2*vv[i] + (1-b2)*gi*gi
			p[i] -= float32(lr * (float64(mv[i]) / c1) / (math.Sqrt(float64(vv[i])/c2) + eps))
		}
		clear(g.Data)
	}
}

// validateTail is the mean fit error of the network's depth as it was
// (the sample's) and of the tail's.
func validateTail(g *nn.GPU, tm *tailModel, paths []string) (base, tuned float64) {
	var n int
	for t := range prefetchTail(paths) {
		if t == nil {
			continue
		}
		e0, ok := fitError(t.sample, t.depth, t.sample.w) // not t.w, the network's input
		if !ok {
			continue
		}
		tp := &tape{g: g, grad: map[*nn.Tensor]*nn.Tensor{}, w: map[string]*nn.Tensor{}}
		for _, nm := range tm.names {
			tp.w[nm] = g.Upload(tm.w[nm])
		}
		d, err := g.Download(tp.bilinear(tp.forward(t), t.sample.h, t.sample.w))
		g.Release()
		if err != nil {
			fail(err)
		}
		e1, _ := fitError(t.sample, d.Data, t.sample.w)
		base += e0
		tuned += e1
		n++
	}
	return base / float64(max(n, 1)), tuned / float64(max(n, 1))
}

// saveTail writes the tail's weights as depth.ReadTail reads them.
func saveTail(tm *tailModel, path string) error {
	f, err := os.Create(path + ".tmp") //nolint:gosec // the operator's file
	if err != nil {
		return err
	}
	if err := depth.WriteTail(f, tm.w); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// trainTail is main's for -tail.
func trainTail(ckpt, trainDir, valDir, out string, epochs int, lr float64, batch, limit int) {
	g, err := nn.NewGPU()
	if err != nil {
		fail(err)
	}
	defer g.Close()
	tm, err := newTailModel(ckpt)
	if err != nil {
		fail(err)
	}
	train, err := list(trainDir)
	if err != nil {
		fail(err)
	}
	val, err := list(valDir)
	if err != nil {
		fail(err)
	}
	r := rand.New(rand.NewPCG(1, 1)) //nolint:gosec // shuffling, repeatably
	r.Shuffle(len(train), func(i, j int) { train[i], train[j] = train[j], train[i] })
	if limit > 0 && limit < len(train) {
		train = train[:limit]
	}
	fmt.Printf("the tail: %d weights' tensors; %d training pictures, %d validation, on %s\n", len(tm.names), len(train), len(val), g.Name())
	base, tuned := validateTail(g, tm, val)
	fmt.Printf("epoch 0: validation error after fitting %.3f px as the network gives it, %.3f with the tail\n", base, tuned)
	steps := epochs * ((len(train) + batch - 1) / batch)
	step := 0
	for e := 1; e <= epochs; e++ {
		r.Shuffle(len(train), func(i, j int) { train[i], train[j] = train[j], train[i] })
		t0 := time.Now()
		var lossSum float64
		var n, k int
		for t := range prefetchTail(train) {
			if t == nil {
				continue
			}
			if loss, ok := tm.step(g, t, true); ok {
				lossSum += loss
				n++
				k++
			}
			if k == batch {
				rate := lr * (0.1 + 0.9*0.5*(1+math.Cos(math.Pi*float64(step)/float64(steps))))
				tm.update(rate, float32(1/float64(k)))
				k = 0
				if step++; step%100 == 0 {
					fmt.Printf("  step %d: loss %.4f (%.3f s a picture)\n", step, lossSum/float64(max(n, 1)), time.Since(t0).Seconds()/float64(max(n, 1)))
				}
			}
		}
		base, tuned := validateTail(g, tm, val)
		fmt.Printf("epoch %d: loss %.4f; validation error after fitting %.3f px as the network gives it, %.3f with the tail (%v)\n",
			e, lossSum/float64(max(n, 1)), base, tuned, time.Since(t0).Round(time.Second))
		if err := saveTail(tm, out); err != nil {
			fail(err)
		}
	}
}

// retune is main's for -retune: each sample of dir written to out with
// the depth the tail's weights (depth.ReadTail's) give in place of the
// network's, for a refinement learnt on the tuned depth.
func retune(tail, dir, out string) {
	g, err := nn.NewGPU()
	if err != nil {
		fail(err)
	}
	defer g.Close()
	w, err := depth.ReadTail(tail)
	if err != nil {
		fail(err)
	}
	paths, err := list(dir)
	if err != nil {
		fail(err)
	}
	if err := os.MkdirAll(out, 0o750); err != nil {
		fail(err)
	}
	n, i := 0, -1
	for t := range prefetchTail(paths) { // in order
		i++
		if t == nil {
			continue
		}
		tp := &tape{g: g, grad: map[*nn.Tensor]*nn.Tensor{}, w: map[string]*nn.Tensor{}}
		for nm, x := range w {
			tp.w[nm] = g.Upload(x)
		}
		d, err := g.Download(tp.bilinear(tp.forward(t), t.sample.h, t.sample.w))
		g.Release()
		if err != nil {
			fail(err)
		}
		b, err := os.ReadFile(paths[i])
		if err != nil {
			fail(err)
		}
		// the header (magic, W, H, Top, Bottom), the planes, then the
		// depth map: its active rows replaced
		le := binary.LittleEndian
		pw, ph, top := int(le.Uint32(b[4:])), int(le.Uint32(b[8:])), int(le.Uint32(b[12:]))
		at := 20 + 3*pw*ph + 4*top*pw
		for j, v := range d.Data {
			le.PutUint32(b[at+4*j:], math.Float32bits(v))
		}
		if err := os.WriteFile(filepath.Join(out, filepath.Base(paths[i])), b, 0o600); err != nil { //nolint:gosec // the operator's directory
			fail(err)
		}
		n++
	}
	fmt.Printf("%d samples retuned into %s\n", n, out)
}
