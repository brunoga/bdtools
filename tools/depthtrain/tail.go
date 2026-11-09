package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"math/rand/v2"
	"time"

	"github.com/brunoga/bdtools/internal/depth"
	"github.com/brunoga/bdtools/internal/nn"
)

// Learning the depth network's head, or a part of it, from the inputs
// stereobench -samples recorded beside each sample, the encoder and the
// rest of the head as the checkpoint has them: -tail CHECKPOINT learns
// the tail (depth.TailNames) from the tail's inputs (NAME.bht); -head
// CHECKPOINT learns a set of the head's weights (-learn) from the
// pictures' windows (NAME.bhw; head.go). The loss is the refinement's
// (the error left after fitting the depth to the disc's disparity a
// picture at a time, or a stretch at a time), plus a term holding the
// depth's mean and deviation to the network's own, so the depth's
// mapping to disparity (stereo.Shape) still holds.

// tailMoments is the weight of the moments' term.
const tailMoments = 1.0

// An item is a training sample as a mode reads it: the sample, and the
// mode's inputs.
type item interface{ base() *sample }

// A mode reads the samples' inputs and runs the network on them.
type mode interface {
	// read is the item of a sample (NAME.bds, the mode's inputs beside it).
	read(path string) (item, error)
	// output is the depth the weights as they are give the item, at the
	// sample's size (no gradients).
	output(g *nn.GPU, it item) []float32
	// step is the item's loss and, when train, its gradients added to the
	// weights' (fixed the fit to use, if not the picture's own).
	step(g *nn.GPU, it item, train bool, fixed *[2]float64) (float64, bool)
	// updated says the weights changed.
	updated()
}

// prefetch reads items ahead, in order (nil for one unreadable).
func prefetchItems(md mode, paths []string) <-chan item {
	ch := make(chan item, 4)
	go func() {
		for _, p := range paths {
			it, err := md.read(p)
			if err != nil {
				fmt.Fprintln(os.Stderr, "depthtrain:", err)
				ch <- nil
				continue
			}
			ch <- it
		}
		close(ch)
	}()
	return ch
}

// tailSample is a sample with its tail inputs.
type tailSample struct {
	*sample
	in   [3]*nn.Tensor
	h, w int // the network's input
}

func (t *tailSample) base() *sample { return t.sample }

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

// headModel is the learnt weights (on the CPU), their gradients, and
// the optimiser's state, in the whole checkpoint.
type headModel struct {
	w, gw map[string]*nn.Tensor
	w0    map[string]*nn.Tensor // the weights as the checkpoint had them (-anchor)
	m, v  map[string][]float32
	t     int
	names []string
	learn map[string]bool
	all   map[string]*nn.Tensor // the whole checkpoint, the learnt weights' tensors in it
}

// newHeadModel is the checkpoint's weights, those named learnt.
func newHeadModel(ckpt string, names []string) (*headModel, error) {
	all, err := nn.LoadPTH(ckpt)
	if err != nil {
		return nil, err
	}
	hm := &headModel{w: map[string]*nn.Tensor{}, w0: map[string]*nn.Tensor{}, gw: map[string]*nn.Tensor{}, m: map[string][]float32{}, v: map[string][]float32{}, learn: map[string]bool{}, all: all}
	for _, n := range names {
		t, ok := all[n]
		if !ok {
			return nil, fmt.Errorf("%s: no weight %s", ckpt, n)
		}
		hm.w[n] = t.Clone()
		hm.w0[n] = t
		hm.all[n] = hm.w[n]
		hm.learn[n] = true
		hm.names = append(hm.names, n)
		hm.m[n], hm.v[n] = make([]float32, len(t.Data)), make([]float32, len(t.Data))
	}
	return hm, nil
}

// expand is the layers' weights and biases, those the checkpoint has.
func expand(all map[string]*nn.Tensor, layers []string) []string {
	var names []string
	for _, l := range layers {
		for _, suf := range []string{".weight", ".bias"} {
			if _, ok := all[l+suf]; ok {
				names = append(names, l+suf)
			}
		}
	}
	return names
}

// isLearnt says whether a weight is learnt.
func (hm *headModel) isLearnt(name string) bool { return hm.learn[name] }

// none learns nothing: for a pass without gradients.
func none(string) bool { return false }

// tailMode learns the tail from its recorded inputs.
type tailMode struct{ hm *headModel }

func (md tailMode) read(path string) (item, error) { return readTail(path) }
func (tailMode) updated()                          {}

// forward is the tail on the item's inputs, the depth at the sample's
// size.
func (md tailMode) forward(tp *nn.Tape, learn func(string) bool, t *tailSample) *nn.Tensor {
	p := depth.TapeParams(tp, md.hm.all, learn)
	in := depth.TailInput{Feat0: tp.Upload(t.in[0]), Feat1: tp.Upload(t.in[1]), P3: tp.Upload(t.in[2]), H: t.h, W: t.w}
	return tp.Bilinear(depth.Tail(tp, p, in), t.sample.h, t.sample.w) // the sample's size (half the picture's)
}

func (md tailMode) output(g *nn.GPU, it item) []float32 {
	defer g.Release()
	out, err := g.Download(md.forward(nn.NewTape(g), none, it.(*tailSample)))
	if err != nil {
		fail(err)
	}
	return out.Data
}

func (md tailMode) step(g *nn.GPU, it item, train bool, fixed *[2]float64) (float64, bool) {
	defer g.Release()
	t := it.(*tailSample)
	learn := none
	if train {
		learn = md.hm.isLearnt
	}
	tp := nn.NewTape(g)
	d := md.forward(tp, learn, t)
	return md.hm.finish(g, tp, d, t.sample, train, fixed)
}

// finish is the rest of a step once the depth d (the tape's, at the
// sample's size) is made: the loss, and when train, the backward pass
// and the gradients gathered.
func (hm *headModel) finish(g *nn.GPU, tp *nn.Tape, d *nn.Tensor, s *sample, train bool, fixed *[2]float64) (float64, bool) {
	out, err := g.Download(d)
	if err != nil {
		fail(err)
	}
	gc, loss, ok := hm.loss(s, out.Data, train, fixed)
	if !ok || !train {
		return loss, ok
	}
	tp.Backward(d, g.Upload(gc.Reshape(1, s.h, s.w)))
	for n, gt := range tp.Grads() {
		dg, err := g.Download(gt)
		if err != nil {
			fail(err)
		}
		if hm.gw[n] == nil {
			hm.gw[n] = nn.New(dg.Shape...)
		}
		acc(hm.gw[n], dg)
	}
	return loss, true
}

// loss is a sample's loss given the depth out (at the sample's size)
// and, when train, its gradient at out: the fit error (the sample's own
// fit, or fixed's) plus the moments' term, (mean - mean0)^2 + (sd -
// sd0)^2 over sd0^2, the network's own depth's, and the keep term where
// the disc is unsure.
func (hm *headModel) loss(s *sample, out []float32, train bool, fixed *[2]float64) (gc *nn.Tensor, loss float64, ok bool) {
	if fixed != nil {
		gc, loss, ok = lossGradFixed(s, out, fixed[0], fixed[1], s.h, s.w)
	} else {
		zero := make([]float32, len(out))
		gc, loss, ok = lossGrad(s, out, zero, s.h, s.w)
	}
	if !ok {
		return nil, 0, false
	}
	m0, s0 := moments(s.depth)
	m1, s1 := moments(out)
	n := float64(len(out))
	loss += tailMoments * ((m1-m0)*(m1-m0) + (s1-s0)*(s1-s0)) / (s0 * s0)
	var kg []float32
	var nk int
	if keep > 0 {
		// where the disc is unsure, the robust residual to the network's
		// own depth as recorded, in units of its deviation
		kg = make([]float32, len(out))
		var kl float64
		for i, v := range out {
			if !math.IsNaN(float64(s.disp[i])) || math.IsNaN(float64(s.depth[i])) {
				continue
			}
			r := (float64(v) - float64(s.depth[i])) / s0
			q := math.Sqrt(r*r + 1)
			kl += q - 1
			kg[i] = float32(r / q / s0)
			nk++
		}
		if nk > 0 {
			loss += keep * kl / float64(nk)
		}
	}
	if !train {
		return nil, loss, true
	}
	if nk > 0 {
		for i := range out {
			gc.Data[i] += float32(keep/float64(nk)) * kg[i]
		}
	}
	for i, v := range out {
		dm := 2 * (m1 - m0) / n
		ds := 2 * (s1 - s0) * (float64(v) - m1) / (n * max(s1, 1e-9))
		gc.Data[i] += float32(tailMoments * (dm + ds) / (s0 * s0))
	}
	return gc, loss, true
}

// lossGradFixed is lossGrad with the fit (a, b) given: the loss is the
// mean of sqrt(r^2+1)-1 of the residual r = a y + b - d, its gradient
// a r / sqrt(r^2+1) / n.
func lossGradFixed(s *sample, out []float32, a, b float64, ph, pw int) (g *nn.Tensor, loss float64, ok bool) {
	g = nn.New(1, ph, pw)
	var n int
	for r := range s.h {
		for c := range s.w {
			d := s.disp[r*s.w+c]
			if math.IsNaN(float64(d)) {
				continue
			}
			res := a*float64(out[r*pw+c]) + b - float64(d)
			q := math.Sqrt(res*res + 1)
			loss += q - 1
			g.Data[r*pw+c] = float32(a * res / q)
			n++
		}
	}
	if n < 100 {
		return nil, 0, false
	}
	for i := range g.Data {
		g.Data[i] /= float32(n)
	}
	return g, loss / float64(n), true
}

// stretchFit is the least-squares fit of the depths of a stretch's
// samples, together, to the disc's disparity: disparity = a depth + b.
func stretchFit(ss []*sample, outs [][]float32) (a, b float64, n int) {
	var sx, sy, sxx, sxy float64
	for k, s := range ss {
		z, pw := outs[k], s.w
		for r := range s.h {
			for c := range s.w {
				d := s.disp[r*s.w+c]
				if math.IsNaN(float64(d)) {
					continue
				}
				x := float64(z[r*pw+c])
				sx += x
				sy += float64(d)
				sxx += x * x
				sxy += x * float64(d)
				n++
			}
		}
	}
	if n < 100 {
		return 0, 0, n
	}
	fn := float64(n)
	a = (sxy - sx*sy/fn) / max(sxx-sx*sx/fn, 1e-9)
	b = (sy - a*sx) / fn
	return a, b, n
}

// stretches groups sample paths by the window they were recorded from
// (NAME-000123.bds: NAME), each group in its pictures' order.
func stretches(paths []string) [][]string {
	groups := map[string][]string{}
	var order []string
	for _, p := range paths {
		base := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		key := base
		if i := strings.LastIndex(base, "-"); i > 0 {
			key = base[:i]
		}
		if groups[key] == nil {
			order = append(order, key)
		}
		groups[key] = append(groups[key], p)
	}
	out := make([][]string, 0, len(order))
	for _, k := range order {
		g := groups[k]
		sort.Strings(g)
		out = append(out, g)
	}
	return out
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
// keep is -keep: the weight of holding the depth, where the disc is
// unsure (a reflection, glass, the sky), to the network's own as it was
// recorded, so that the discs' silence there does not free the network
// to read those regions anew.
var keep float64

// anchor is -anchor: how hard each update pulls the weights back towards
// the checkpoint's, as a multiple of the learning rate (decoupled, like
// weight decay, but to the weights as they were rather than to zero), so
// that where the discs say nothing the network stays as it was.
var anchor float64

func (hm *headModel) update(lr float64, scale float32) {
	const b1, b2, eps = 0.9, 0.999, 1e-8
	hm.t++
	c1 := 1 - math.Pow(b1, float64(hm.t))
	c2 := 1 - math.Pow(b2, float64(hm.t))
	for _, n := range hm.names {
		g := hm.gw[n]
		if g == nil {
			continue
		}
		p, p0, mv, vv := hm.w[n].Data, hm.w0[n].Data, hm.m[n], hm.v[n]
		pull := float32(lr * anchor)
		for i := range p {
			gi := g.Data[i] * scale
			mv[i] = b1*mv[i] + (1-b1)*gi
			vv[i] = b2*vv[i] + (1-b2)*gi*gi
			p[i] -= float32(lr * (float64(mv[i]) / c1) / (math.Sqrt(float64(vv[i])/c2) + eps))
			p[i] -= pull * (p[i] - p0[i])
		}
		clear(g.Data)
	}
}

// validateMode is the mean fit error of the network's depth as it was (the
// sample's) and of the weights as they are.
func validateMode(g *nn.GPU, md mode, paths []string) (base, tuned float64) {
	var n int
	for it := range prefetchItems(md, paths) {
		if it == nil {
			continue
		}
		s := it.base()
		e0, ok := fitError(s, s.depth, s.w)
		if !ok {
			continue
		}
		e1, _ := fitError(s, md.output(g, it), s.w)
		base += e0
		tuned += e1
		n++
	}
	return base / float64(max(n, 1)), tuned / float64(max(n, 1))
}

// saveHead writes the learnt weights as depth.ReadTail reads them.
func saveHead(hm *headModel, path string) error {
	f, err := os.Create(path + ".tmp") //nolint:gosec // the operator's file
	if err != nil {
		return err
	}
	if err := depth.WriteTail(f, hm.w); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// trainTail is main's for -tail.
func trainTail(ckpt, trainDir, valDir, out string, epochs int, lr float64, batch, limit int, byStretch bool) {
	g, err := nn.NewGPU()
	if err != nil {
		fail(err)
	}
	defer g.Close()
	all, err := nn.LoadPTH(ckpt)
	if err != nil {
		fail(err)
	}
	hm, err := newHeadModel(ckpt, expand(all, depth.TailNames))
	if err != nil {
		fail(err)
	}
	fmt.Printf("the tail: %d weights' tensors, on %s\n", len(hm.names), g.Name())
	trainLoop(g, hm, tailMode{hm}, trainDir, valDir, out, epochs, lr, batch, limit, byStretch)
}

// trainLoop learns hm's weights in a mode: epochs passes over the
// training samples, Adam at a rate decayed along a cosine to a tenth,
// the weights saved to out after each pass.
func trainLoop(g *nn.GPU, hm *headModel, md mode, trainDir, valDir, out string, epochs int, lr float64, batch, limit int, byStretch bool) {
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
	fmt.Printf("%d training pictures, %d validation\n", len(train), len(val))
	base, tuned := validateMode(g, md, val)
	fmt.Printf("epoch 0: validation error after fitting %.3f px as the network gives it, %.3f with the weights\n", base, tuned)
	steps := epochs * ((len(train) + batch - 1) / batch)
	step := 0
	var groups [][]string
	if byStretch {
		groups = stretches(train)
		fmt.Printf("fitted a stretch at a time: %d stretches\n", len(groups))
	}
	for e := 1; e <= epochs; e++ {
		t0 := time.Now()
		var lossSum float64
		var n, k int
		update := func() {
			rate := lr * (0.1 + 0.9*0.5*(1+math.Cos(math.Pi*float64(step)/float64(steps))))
			hm.update(rate, float32(1/float64(k)))
			md.updated()
			k = 0
			if step++; step%100 == 0 {
				fmt.Printf("  step %d: loss %.4f (%.3f s a picture)\n", step, lossSum/float64(max(n, 1)), time.Since(t0).Seconds()/float64(max(n, 1)))
			}
		}
		if byStretch {
			r.Shuffle(len(groups), func(i, j int) { groups[i], groups[j] = groups[j], groups[i] })
			for _, paths := range groups {
				var items []item
				var ss []*sample
				var outs [][]float32
				for it := range prefetchItems(md, paths) {
					if it != nil {
						items = append(items, it)
						ss = append(ss, it.base())
						outs = append(outs, md.output(g, it))
					}
				}
				a, b, cnt := stretchFit(ss, outs)
				if cnt < 100 {
					continue
				}
				fixed := [2]float64{a, b}
				for _, it := range items {
					if loss, ok := md.step(g, it, true, &fixed); ok {
						lossSum += loss
						n++
						k++
					}
					if k == batch {
						update()
					}
				}
			}
		} else {
			r.Shuffle(len(train), func(i, j int) { train[i], train[j] = train[j], train[i] })
			for it := range prefetchItems(md, train) {
				if it == nil {
					continue
				}
				if loss, ok := md.step(g, it, true, nil); ok {
					lossSum += loss
					n++
					k++
				}
				if k == batch {
					update()
				}
			}
		}
		base, tuned := validateMode(g, md, val)
		fmt.Printf("epoch %d: loss %.4f; validation error after fitting %.3f px as the network gives it, %.3f with the weights (%v)\n",
			e, lossSum/float64(max(n, 1)), base, tuned, time.Since(t0).Round(time.Second))
		if err := saveHead(hm, out); err != nil {
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
	md := tailMode{&headModel{all: w}} // the tail's weights are all the tail reads
	paths, err := list(dir)
	if err != nil {
		fail(err)
	}
	if err := os.MkdirAll(out, 0o750); err != nil {
		fail(err)
	}
	n := 0
	for i, p := range paths { // in order
		t, err := readTail(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, "depthtrain:", err)
			continue
		}
		d := md.output(g, t)
		b, err := os.ReadFile(paths[i])
		if err != nil {
			fail(err)
		}
		// the header (magic, W, H, Top, Bottom), the planes, then the
		// depth map: its active rows replaced
		le := binary.LittleEndian
		pw, ph, top := int(le.Uint32(b[4:])), int(le.Uint32(b[8:])), int(le.Uint32(b[12:]))
		at := 20 + 3*pw*ph + 4*top*pw
		for j, v := range d {
			le.PutUint32(b[at+4*j:], math.Float32bits(v))
		}
		if err := os.WriteFile(filepath.Join(out, filepath.Base(paths[i])), b, 0o600); err != nil { //nolint:gosec // the operator's directory
			fail(err)
		}
		n++
	}
	fmt.Printf("%d samples retuned into %s\n", n, out)
}
