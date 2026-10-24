package main

import (
	"math"
	"math/rand/v2"

	"github.com/brunoga/bdtools/internal/nn"
	"github.com/brunoga/bdtools/internal/stereo"
)

// model is the refinement's weights and their gradients.
type model struct {
	stereo.RefineWeights
	gw, gb map[string]*nn.Tensor
}

// newModel is a model at its start: He's initialisation, the last layer
// zero (so the refinement starts as no change).
func newModel(r *rand.Rand) *model {
	m := &model{RefineWeights: stereo.RefineWeights{W: map[string]*nn.Tensor{}, B: map[string]*nn.Tensor{}}}
	for _, l := range stereo.RefineLayers {
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

func (m *model) zeroGrad() {
	if m.gw == nil {
		m.gw, m.gb = map[string]*nn.Tensor{}, map[string]*nn.Tensor{}
	}
	for _, l := range stereo.RefineLayers {
		m.gw[l.Name], m.gb[l.Name] = nn.New(l.Shape()...), nn.New(l.Out)
	}
}

func acc(dst, src *nn.Tensor) {
	for i, v := range src.Data {
		dst.Data[i] += v
	}
}

// skipOf is the level an upsampling adds, and which layer's output that is.
var skipOf = map[string]string{"u3": "e3b", "u2": "e2b", "u1": "e1b"}

// step runs a sample forward and back, adding to the gradients: the
// loss (the robust error left after fitting, in half-size pixels) and
// whether it counted.
func (m *model) step(s *sample) (float64, bool) {
	x, _, _ := stereo.RefineInput(s.y, s.cb, s.cr, s.w, s.depth, s.w, s.h)
	ph, pw := x.Dim(1), x.Dim(2)
	z := x.Data[3*ph*pw:]
	// forward, keeping each layer's input and output
	ins := map[string]*nn.Tensor{}
	outs := map[string]*nn.Tensor{}
	h := x
	for _, l := range stereo.RefineLayers {
		ins[l.Name] = h
		w, b := m.W[l.Name], m.B[l.Name]
		if l.Up {
			h = nn.ConvTranspose2dK(h, w, b)
			h = nn.ReLU(nn.Add(h, outs[skipOf[l.Name]]))
		} else {
			h = nn.Conv2d(h, w, b, l.Stride, 1)
			if !l.Residual {
				h = nn.ReLU(h)
			}
		}
		outs[l.Name] = h
	}
	g, loss, ok := lossGrad(s, h.Data, z, ph, pw)
	if !ok {
		return 0, false
	}
	// back
	skipGrad := map[string]*nn.Tensor{}
	for i := len(stereo.RefineLayers) - 1; i >= 0; i-- {
		l := stereo.RefineLayers[i]
		if sg := skipGrad[l.Name]; sg != nil {
			acc(g, sg)
		}
		if l.Up {
			g = nn.ReLUBackward(outs[l.Name], g)
			skipGrad[skipOf[l.Name]] = g.Clone()
			dx, dw, db := nn.ConvTranspose2dKBackward(ins[l.Name], m.W[l.Name], g)
			acc(m.gw[l.Name], dw)
			acc(m.gb[l.Name], db)
			g = dx
			continue
		}
		if !l.Residual {
			g = nn.ReLUBackward(outs[l.Name], g)
		}
		dx, dw, db := nn.Conv2dBackward(ins[l.Name], m.W[l.Name], g, l.Stride, 1)
		acc(m.gw[l.Name], dw)
		acc(m.gb[l.Name], db)
		g = dx
	}
	return loss, true
}

// lossGrad is the loss of a sample's refined depth, the network's output
// out plus the standardised depth z (the robust error left after fitting,
// in half-size pixels), and its gradient at out; ok says it counted.
func lossGrad(s *sample, out, z []float32, ph, pw int) (g *nn.Tensor, loss float64, ok bool) {
	y := make([]float32, ph*pw)
	for i, v := range out {
		y[i] = v + z[i]
	}
	// the loss: the mean of sqrt(r^2+1)-1 of the residual r = a y + b - d
	// after the least-squares fit (a, b)
	a, b, n := fit(s, y, pw)
	if n < 100 {
		return nil, 0, false
	}
	var my, md float64
	q := make([]float64, ph*pw) // dL/dr
	for r := range s.h {
		for c := range s.w {
			d := s.disp[r*s.w+c]
			if math.IsNaN(float64(d)) {
				continue
			}
			i := r*pw + c
			res := a*float64(y[i]) + b - float64(d)
			sq := math.Sqrt(res*res + 1)
			loss += sq - 1
			q[i] = res / sq / float64(n)
			my += float64(y[i])
			md += float64(d)
		}
	}
	my /= float64(n)
	md /= float64(n)
	// The fit moves with y: da/dy_i = ((d_i - md) - 2a(y_i - my)) / Sxx,
	// db/dy_i = -my da/dy_i - a/n, so
	// dL/dy_i = a q_i - a/n sum(q) + sum(q (y - my)) da/dy_i.
	var sq, sqy, sxx float64
	for r := range s.h {
		for c := range s.w {
			if d := s.disp[r*s.w+c]; !math.IsNaN(float64(d)) {
				i := r*pw + c
				dy := float64(y[i]) - my
				sq += q[i]
				sqy += q[i] * dy
				sxx += dy * dy
			}
		}
	}
	sxx = max(sxx, 1e-9)
	g = nn.New(1, ph, pw)
	for r := range s.h {
		for c := range s.w {
			if d := s.disp[r*s.w+c]; !math.IsNaN(float64(d)) {
				i := r*pw + c
				da := ((float64(d) - md) - 2*a*(float64(y[i])-my)) / sxx
				g.Data[i] = float32(a*q[i] - a/float64(n)*sq + sqy*da)
			}
		}
	}
	return g, loss / float64(n), true
}

// adam is Adam's state.
type adam struct {
	mw, vw, mb, vb map[string][]float32
	t              int
}

func newAdam(m *model) *adam {
	o := &adam{mw: map[string][]float32{}, vw: map[string][]float32{}, mb: map[string][]float32{}, vb: map[string][]float32{}}
	for _, l := range stereo.RefineLayers {
		n := len(m.W[l.Name].Data)
		o.mw[l.Name], o.vw[l.Name] = make([]float32, n), make([]float32, n)
		o.mb[l.Name], o.vb[l.Name] = make([]float32, l.Out), make([]float32, l.Out)
	}
	return o
}

// update takes a step of the gradients times scale.
func (o *adam) update(m *model, lr float64, scale float32) {
	const b1, b2, eps = 0.9, 0.999, 1e-8
	o.t++
	c1 := 1 - math.Pow(b1, float64(o.t))
	c2 := 1 - math.Pow(b2, float64(o.t))
	upd := func(p, g, mv, vv []float32) {
		for i := range p {
			gi := g[i] * scale
			mv[i] = b1*mv[i] + (1-b1)*gi
			vv[i] = b2*vv[i] + (1-b2)*gi*gi
			p[i] -= float32(lr * (float64(mv[i]) / c1) / (math.Sqrt(float64(vv[i])/c2) + eps))
		}
	}
	for _, l := range stereo.RefineLayers {
		upd(m.W[l.Name].Data, m.gw[l.Name].Data, o.mw[l.Name], o.vw[l.Name])
		upd(m.B[l.Name].Data, m.gb[l.Name].Data, o.mb[l.Name], o.vb[l.Name])
	}
}

// stepGPU is step on the GPU: the same forward and back (in single
// precision), the loss and its gradient on the CPU between.
func (m *model) stepGPU(g *nn.GPU, s *sample) (float64, bool) {
	defer g.Release()
	x, _, _ := stereo.RefineInput(s.y, s.cb, s.cr, s.w, s.depth, s.w, s.h)
	ph, pw := x.Dim(1), x.Dim(2)
	z := x.Data[3*ph*pw:]
	w, b := map[string]*nn.Tensor{}, map[string]*nn.Tensor{}
	for _, l := range stereo.RefineLayers {
		w[l.Name], b[l.Name] = g.Upload(m.W[l.Name]), g.Upload(m.B[l.Name])
	}
	ins := map[string]*nn.Tensor{}
	outs := map[string]*nn.Tensor{}
	h := g.Upload(x)
	for _, l := range stereo.RefineLayers {
		ins[l.Name] = h
		if l.Up {
			h = g.ReLU(g.Add(g.ConvTranspose2dK(h, w[l.Name], b[l.Name]), outs[skipOf[l.Name]]))
		} else {
			h = g.Conv2d(h, w[l.Name], b[l.Name], l.Stride, 1)
			if !l.Residual {
				h = g.ReLU(h)
			}
		}
		outs[l.Name] = h
	}
	out, err := g.Download(h)
	if err != nil {
		fail(err)
	}
	gc, loss, ok := lossGrad(s, out.Data, z, ph, pw)
	if !ok {
		return 0, false
	}
	gr := g.Upload(gc)
	down := func(t *nn.Tensor) *nn.Tensor {
		d, err := g.Download(t)
		if err != nil {
			fail(err)
		}
		return d
	}
	skipGrad := map[string]*nn.Tensor{}
	for i := len(stereo.RefineLayers) - 1; i >= 0; i-- {
		l := stereo.RefineLayers[i]
		if sg := skipGrad[l.Name]; sg != nil {
			gr = g.Add(gr, sg)
		}
		var dx, dw, db *nn.Tensor
		if l.Up {
			gr = g.ReLUBackward(outs[l.Name], gr)
			skipGrad[skipOf[l.Name]] = g.Clone(gr)
			dx, dw, db = g.ConvTranspose2dKBackward(ins[l.Name], w[l.Name], gr)
		} else {
			if !l.Residual {
				gr = g.ReLUBackward(outs[l.Name], gr)
			}
			dx, dw, db = g.Conv2dBackward(ins[l.Name], w[l.Name], gr, l.Stride, 1)
		}
		acc(m.gw[l.Name], down(dw))
		acc(m.gb[l.Name], down(db))
		gr = dx
	}
	return loss, true
}
