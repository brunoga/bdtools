package nn

// A Tape records a forward pass on a GPU, op by op, to run it backwards
// for the gradients at the weights it is told are learnt (Param). Its
// ops are the GPU's, recorded, so a network written against an interface
// they satisfy runs on the GPU as it is for inference and on a Tape for
// learning. Two differences: the ops that are in place on a backend
// (ReLU, Add) give new tensors here and leave their operands as they
// were, so a network must use what they give; and the gradients are
// single precision whatever the GPU's Half says. An op none of whose
// inputs is on a path from a learnt weight is not recorded. The tensors
// are the GPU's: released at its Release, with the gradients.
type Tape struct {
	*GPU
	back  []func()
	grad  map[*devBuf]*Tensor // the gradient at a tensor (and its reshapes)
	needs map[*devBuf]bool    // on a path from a learnt weight
	param map[*devBuf]string  // the learnt weights
	gw    map[string]*Tensor  // their gradients
}

// NewTape is a tape of g's ops.
func NewTape(g *GPU) *Tape {
	return &Tape{GPU: g, grad: map[*devBuf]*Tensor{}, needs: map[*devBuf]bool{}, param: map[*devBuf]string{}, gw: map[string]*Tensor{}}
}

// Param uploads a learnt weight: its gradient is gathered under name
// (Grads).
func (tp *Tape) Param(name string, w *Tensor) *Tensor {
	t := tp.Upload(w)
	tp.needs[t.dev] = true
	tp.param[t.dev] = name
	return t
}

// Watch asks for the gradient at a tensor of the GPU's (Grad), as at a
// learnt weight.
func (tp *Tape) Watch(t *Tensor) *Tensor {
	tp.needs[t.dev] = true
	return t
}

// Grads are the learnt weights' gradients, by name, after Backward: one
// the pass never reached is absent.
func (tp *Tape) Grads() map[string]*Tensor { return tp.gw }

// Grad is the gradient at a tensor of the pass after Backward (nil when
// none reached it), in the tensor's shape.
func (tp *Tape) Grad(t *Tensor) *Tensor {
	d := tp.grad[t.dev]
	if d == nil {
		return nil
	}
	return &Tensor{Shape: append([]int(nil), t.Shape...), dev: d.dev}
}

// Backward runs the pass backwards from out, dy the loss's gradient
// there (the GPU's tensor, out's shape).
func (tp *Tape) Backward(out, dy *Tensor) {
	tp.grad[out.dev] = dy
	for i := len(tp.back) - 1; i >= 0; i-- {
		tp.back[i]()
	}
	tp.back = nil
}

// rec says whether an op with these inputs is recorded (any on a path
// from a learnt weight), and marks its output so when it is.
func (tp *Tape) rec(y *Tensor, ins ...*Tensor) bool {
	for _, x := range ins {
		if x != nil && tp.needs[x.dev] {
			tp.needs[y.dev] = true
			return true
		}
	}
	return false
}

// add adds d to the gradient at t, when t's is wanted. d is taken as it
// is (never a tensor the caller goes on using).
func (tp *Tape) add(t, d *Tensor) {
	if t == nil || d == nil || !tp.needs[t.dev] {
		return
	}
	if old := tp.grad[t.dev]; old != nil {
		tp.GPU.Add(old, d)
		return
	}
	tp.grad[t.dev] = d
}

// addParam adds d to a learnt weight's gradient.
func (tp *Tape) addParam(w, d *Tensor) {
	if w == nil || d == nil {
		return
	}
	name, ok := tp.param[w.dev]
	if !ok {
		return
	}
	if old := tp.gw[name]; old != nil {
		tp.GPU.Add(old, d)
		return
	}
	tp.gw[name] = d
}

// Conv2d is the GPU's, recorded.
func (tp *Tape) Conv2d(x, w, b *Tensor, stride, pad int) *Tensor {
	y := tp.GPU.Conv2d(x, w, b, stride, pad)
	if tp.rec(y, x, w, b) {
		tp.back = append(tp.back, func() {
			if dy := tp.Grad(y); dy != nil {
				dx, dw, db := tp.Conv2dBackward(x, w, dy, stride, pad)
				tp.add(x, dx)
				tp.addParam(w, dw)
				tp.addParam(b, db)
			}
		})
	}
	return y
}

// ConvTranspose2dK is the GPU's, recorded.
func (tp *Tape) ConvTranspose2dK(x, w, b *Tensor) *Tensor {
	y := tp.GPU.ConvTranspose2dK(x, w, b)
	if tp.rec(y, x, w, b) {
		tp.back = append(tp.back, func() {
			if dy := tp.Grad(y); dy != nil {
				dx, dw, db := tp.ConvTranspose2dKBackward(x, w, dy)
				tp.add(x, dx)
				tp.addParam(w, dw)
				tp.addParam(b, db)
			}
		})
	}
	return y
}

// Linear is the GPU's, recorded.
func (tp *Tape) Linear(x, w, b *Tensor) *Tensor {
	y := tp.GPU.Linear(x, w, b)
	if tp.rec(y, x, w, b) {
		tp.back = append(tp.back, func() {
			if dy := tp.Grad(y); dy != nil {
				dx, dw, db := tp.LinearBackward(x, w, dy)
				tp.add(x, dx)
				tp.addParam(w, dw)
				tp.addParam(b, db)
			}
		})
	}
	return y
}

// LayerNorm is the GPU's, recorded.
func (tp *Tape) LayerNorm(x, w, b *Tensor, eps float32) *Tensor {
	y := tp.GPU.LayerNorm(x, w, b, eps)
	if tp.rec(y, x, w, b) {
		tp.back = append(tp.back, func() {
			if dy := tp.Grad(y); dy != nil {
				dx, dw, db := tp.LayerNormBackward(x, w, dy, eps)
				tp.add(x, dx)
				tp.addParam(w, dw)
				tp.addParam(b, db)
			}
		})
	}
	return y
}

// GroupNorm is the GPU's, recorded.
func (tp *Tape) GroupNorm(x *Tensor, groups int, w, b *Tensor, eps float32) *Tensor {
	y := tp.GPU.GroupNorm(x, groups, w, b, eps)
	if tp.rec(y, x, w, b) {
		tp.back = append(tp.back, func() {
			if dy := tp.Grad(y); dy != nil {
				dx, dw, db := tp.GroupNormBackward(x, groups, w, dy, eps)
				tp.add(x, dx)
				tp.addParam(w, dw)
				tp.addParam(b, db)
			}
		})
	}
	return y
}

// GEGLU is the GPU's, recorded.
func (tp *Tape) GEGLU(x *Tensor) *Tensor {
	y := tp.GPU.GEGLU(x)
	if tp.rec(y, x) {
		tp.back = append(tp.back, func() {
			if dy := tp.Grad(y); dy != nil {
				tp.add(x, tp.GEGLUBackward(x, dy))
			}
		})
	}
	return y
}

// ReLU is the GPU's on a copy: x stays as it was.
func (tp *Tape) ReLU(x *Tensor) *Tensor {
	y := tp.GPU.ReLU(tp.GPU.Clone(x))
	if tp.rec(y, x) {
		tp.back = append(tp.back, func() {
			if dy := tp.Grad(y); dy != nil {
				tp.add(x, tp.ReLUBackward(y, tp.GPU.Clone(dy)))
			}
		})
	}
	return y
}

// Add is the GPU's on a copy of a: a stays as it was.
func (tp *Tape) Add(a, b *Tensor) *Tensor {
	y := tp.GPU.Add(tp.GPU.Clone(a), b)
	if tp.rec(y, a, b) {
		tp.back = append(tp.back, func() {
			if dy := tp.Grad(y); dy != nil {
				tp.add(a, tp.GPU.Clone(dy))
				tp.add(b, tp.GPU.Clone(dy))
			}
		})
	}
	return y
}

// Attention is the GPU's, recorded.
func (tp *Tape) Attention(q, k, v *Tensor, heads int, scale float32) *Tensor {
	y := tp.GPU.Attention(q, k, v, heads, scale)
	if tp.rec(y, q, k, v) {
		tp.back = append(tp.back, func() {
			if dy := tp.Grad(y); dy != nil {
				dq, dk, dv := tp.AttentionBackward(q, k, v, heads, scale, dy)
				tp.add(q, dq)
				tp.add(k, dk)
				tp.add(v, dv)
			}
		})
	}
	return y
}

// GELU is the GPU's on a copy of x: x stays as it was.
func (tp *Tape) GELU(x *Tensor) *Tensor {
	y := tp.GPU.GELU(tp.GPU.Clone(x))
	if tp.rec(y, x) {
		tp.back = append(tp.back, func() {
			if dy := tp.Grad(y); dy != nil {
				tp.add(x, tp.GELUBackward(x, dy))
			}
		})
	}
	return y
}

// AddScaled is the GPU's on a copy of x: x + y s, x as it was.
func (tp *Tape) AddScaled(x, y, s *Tensor) *Tensor {
	out := tp.GPU.AddScaled(tp.GPU.Clone(x), y, s)
	if tp.rec(out, x, y, s) {
		tp.back = append(tp.back, func() {
			if dy := tp.Grad(out); dy != nil {
				tp.add(x, tp.GPU.Clone(dy))
				dyIn, ds := tp.AddScaledBackward(y, s, dy)
				tp.add(y, dyIn)
				tp.addParam(s, ds)
			}
		})
	}
	return out
}

// Split3 is the GPU's, recorded.
func (tp *Tape) Split3(x *Tensor) (q, k, v *Tensor) {
	q, k, v = tp.GPU.Split3(x)
	if tp.needs[x.dev] {
		tp.needs[q.dev], tp.needs[k.dev], tp.needs[v.dev] = true, true, true
		tp.back = append(tp.back, func() {
			dq, dk, dv := tp.Grad(q), tp.Grad(k), tp.Grad(v)
			if dq == nil && dk == nil && dv == nil {
				return
			}
			zero := func(t *Tensor) *Tensor {
				if t != nil {
					return t
				}
				return tp.Upload(New(q.Shape...))
			}
			dq, dk, dv = zero(dq), zero(dk), zero(dv)
			dx := tp.tensor(x.Shape...)
			n, d := q.Dim(0), q.Dim(1)
			gr, bl := each(n * d)
			tp.launch("join3", gr, bl, 0, int64(n*d), d, ptr(dq), ptr(dk), ptr(dv), ptr(dx))
			tp.add(x, dx)
		})
	}
	return q, k, v
}

// Rows is the GPU's view, recorded: the gradient at the view lands in
// the rows from on of the gradient at x.
func (tp *Tape) Rows(x *Tensor, from int) *Tensor {
	y := tp.GPU.Rows(x, from)
	if tp.needs[x.dev] {
		tp.needs[y.dev] = true
		tp.back = append(tp.back, func() {
			dy := tp.Grad(y)
			if dy == nil {
				return
			}
			gx := tp.grad[x.dev]
			if gx == nil {
				gx = tp.Upload(New(x.Shape...))
				tp.grad[x.dev] = gx
			}
			tp.GPU.Add(tp.GPU.Rows(gx, from), dy)
		})
	}
	return y
}

// Tokens is the GPU's, recorded for the patches (the class token and
// the position embedding get no gradient: they are not learnt).
func (tp *Tape) Tokens(patches, cls, pos *Tensor) *Tensor {
	y := tp.GPU.Tokens(patches, cls, pos)
	if tp.rec(y, patches) {
		tp.back = append(tp.back, func() {
			if dy := tp.Grad(y); dy != nil {
				tp.add(patches, tp.GPU.Transpose(tp.GPU.Rows(dy, 1)))
			}
		})
	}
	return y
}

// Clone is the GPU's, recorded.
func (tp *Tape) Clone(x *Tensor) *Tensor {
	y := tp.GPU.Clone(x)
	if tp.rec(y, x) {
		tp.back = append(tp.back, func() {
			if dy := tp.Grad(y); dy != nil {
				tp.add(x, tp.GPU.Clone(dy))
			}
		})
	}
	return y
}

// Bilinear is the GPU's, recorded.
func (tp *Tape) Bilinear(x *Tensor, oh, ow int) *Tensor {
	y := tp.GPU.Bilinear(x, oh, ow)
	if tp.rec(y, x) {
		tp.back = append(tp.back, func() {
			if dy := tp.Grad(y); dy != nil {
				tp.add(x, tp.BilinearBackward(dy, x.Dim(1), x.Dim(2)))
			}
		})
	}
	return y
}

// Transpose is the GPU's, recorded.
func (tp *Tape) Transpose(x *Tensor) *Tensor {
	y := tp.GPU.Transpose(x)
	if tp.rec(y, x) {
		tp.back = append(tp.back, func() {
			if dy := tp.Grad(y); dy != nil {
				tp.add(x, tp.GPU.Transpose(dy))
			}
		})
	}
	return y
}

// TemporalAttention is the GPU's, recorded: the gradients reach the
// query, the position encodings, and the keys and values of the frames
// on a path from a learnt weight.
func (tp *Tape) TemporalAttention(q, peQ, peK, peV *Tensor, ks, vs []*Tensor, heads int, scale float32) *Tensor {
	y := tp.GPU.TemporalAttention(q, peQ, peK, peV, ks, vs, heads, scale)
	want := make([]bool, len(ks))
	any := false
	for f := range ks {
		want[f] = tp.needs[ks[f].dev] || tp.needs[vs[f].dev]
		any = any || want[f]
	}
	if tp.rec(y, q, peQ, peK, peV) || any {
		tp.needs[y.dev] = true
		tp.back = append(tp.back, func() {
			dy := tp.Grad(y)
			if dy == nil {
				return
			}
			dq, dpeQ, dpeK, dpeV, dks, dvs := tp.TemporalAttentionBackward(q, peQ, peK, peV, ks, vs, heads, scale, dy, want)
			tp.add(q, dq)
			tp.add(peQ, dpeQ)
			tp.add(peK, dpeK)
			tp.add(peV, dpeV)
			for f := range ks {
				tp.add(ks[f], dks[f])
				tp.add(vs[f], dvs[f])
			}
		})
	}
	return y
}
