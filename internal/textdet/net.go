// Package textdet finds the text in a picture: the titles, credits and
// logos that a 2D-to-3D conversion should keep in front of the scene
// instead of sinking them into it. The detector is PaddleOCR's PP-OCRv4
// text detection network (Apache-2.0), read from its ONNX model and run
// on an nn.Backend.
package textdet

import (
	"fmt"
	"math"

	"github.com/brunoga/bdtools/internal/nn"
)

// The kinds of a step.
const (
	opConv = iota
	opDepthwise
	opConvT
	opAct
	opGAP
	opMulCh
	opAdd
	opNearest
	opConcat
)

// step is one layer of a Net: the slots it reads and the slot it writes.
type step struct {
	op          int
	in          []int
	out         int
	w, b        *nn.Tensor
	stride, pad int
	act         nn.Act
	f           int
	inPlace     bool // opAdd: the first input is not read again
}

// Net is an ONNX graph made into the layers the backend has: each
// convolution with the batch normalisation and the affine operations
// after it folded into its weights, and the chains of elementwise
// operations made into one nn.Act each.
type Net struct {
	steps  []step
	slots  int
	input  int
	output int
	on     nn.Backend
	w, b   map[*nn.Tensor]*nn.Tensor // the weights on the backend
}

// Compile makes a Net of a graph of the operators the detector uses.
func Compile(g *nn.Graph) (*Net, error) {
	c := &compiler{g: g, slot: map[string]int{}, users: map[string][]int{}, done: make([]bool, len(g.Nodes))}
	for i, n := range g.Nodes {
		for _, in := range n.In {
			c.users[in] = append(c.users[in], i)
		}
	}
	if len(g.Inputs) != 1 || len(g.Outputs) != 1 {
		return nil, fmt.Errorf("textdet: %d inputs and %d outputs", len(g.Inputs), len(g.Outputs))
	}
	c.net.input = c.slotOf(g.Inputs[0])
	for i := range g.Nodes {
		if c.done[i] {
			continue
		}
		if err := c.node(i); err != nil {
			return nil, fmt.Errorf("textdet: node %d (%s): %w", i, g.Nodes[i].Op, err)
		}
	}
	out, ok := c.slot[g.Outputs[0]]
	if !ok {
		return nil, fmt.Errorf("textdet: the output %s is never made", g.Outputs[0])
	}
	c.net.output = out
	c.net.slots = len(c.slot)
	c.markInPlace()
	return &c.net, nil
}

type compiler struct {
	g     *nn.Graph
	net   Net
	slot  map[string]int
	users map[string][]int
	done  []bool
}

func (c *compiler) slotOf(name string) int {
	s, ok := c.slot[name]
	if !ok {
		s = len(c.slot)
		c.slot[name] = s
	}
	return s
}

// sole is the node that is the only reader of a tensor, or -1.
func (c *compiler) sole(name string) int {
	u := c.users[name]
	if len(u) != 1 || c.done[u[0]] || name == c.g.Outputs[0] {
		return -1
	}
	return u[0]
}

// constArg is the constant input of a two-input node and the index of
// its other input, or nil.
func (c *compiler) constArg(n *nn.Node) (*nn.Tensor, int) {
	if len(n.In) != 2 {
		return nil, -1
	}
	if t, ok := c.g.Consts[n.In[1]]; ok {
		return t, 0
	}
	if t, ok := c.g.Consts[n.In[0]]; ok {
		return t, 1
	}
	return nil, -1
}

func scalar(t *nn.Tensor) (float32, bool) {
	if t != nil && len(t.Data) == 1 {
		return t.Data[0], true
	}
	return 0, false
}

func (c *compiler) node(i int) error {
	n := &c.g.Nodes[i]
	switch n.Op {
	case "Conv", "ConvTranspose":
		return c.conv(i)
	case "Add", "Mul", "Div", "Relu", "HardSigmoid", "Sigmoid", "Clip":
		if n.Op == "Add" || n.Op == "Mul" {
			if t, _ := c.constArg(n); t == nil {
				return c.binary(i)
			}
		}
		return c.act(i)
	case "GlobalAveragePool":
		c.emit(i, step{op: opGAP, in: []int{c.slotOf(n.In[0])}}, n.Out[0])
	case "Resize":
		if len(n.In) < 3 {
			return fmt.Errorf("resize without scales")
		}
		s := c.g.Consts[n.In[2]]
		if s == nil || len(s.Data) != 4 || s.Data[2] != s.Data[3] || s.Data[2] != float32(int(s.Data[2])) {
			return fmt.Errorf("resize by %v", s)
		}
		if n.Attr["mode"].S != "nearest" {
			return fmt.Errorf("resize mode %q", n.Attr["mode"].S)
		}
		c.emit(i, step{op: opNearest, in: []int{c.slotOf(n.In[0])}, f: int(s.Data[2])}, n.Out[0])
	case "Concat":
		if n.Int("axis", 0) != 1 {
			return fmt.Errorf("concat on axis %d", n.Int("axis", 0))
		}
		st := step{op: opConcat}
		for _, in := range n.In {
			st.in = append(st.in, c.slotOf(in))
		}
		c.emit(i, st, n.Out[0])
	default:
		return fmt.Errorf("unsupported operator")
	}
	return nil
}

func (c *compiler) emit(i int, st step, out string) {
	c.done[i] = true
	st.out = c.slotOf(out)
	c.net.steps = append(c.net.steps, st)
}

// conv is a convolution with what can be folded into it after.
func (c *compiler) conv(i int) error {
	n := &c.g.Nodes[i]
	w := c.g.Consts[n.In[1]]
	if w == nil {
		return fmt.Errorf("weights not constant")
	}
	w = w.Clone()
	var b *nn.Tensor
	if len(n.In) > 2 && n.In[2] != "" {
		b = c.g.Consts[n.In[2]].Clone()
	}
	for _, d := range n.Ints("dilations") {
		if d != 1 {
			return fmt.Errorf("dilation %d", d)
		}
	}
	st := step{op: opConv, stride: 1}
	k := n.Ints("kernel_shape")
	if len(k) != 2 || k[0] != k[1] {
		return fmt.Errorf("kernel %v", k)
	}
	if s := n.Ints("strides"); len(s) == 2 {
		if s[0] != s[1] {
			return fmt.Errorf("strides %v", s)
		}
		st.stride = s[0]
	}
	if p := n.Ints("pads"); len(p) == 4 {
		if p[0] != p[1] || p[0] != p[2] || p[0] != p[3] {
			return fmt.Errorf("pads %v", p)
		}
		st.pad = p[0]
	}
	outs := w.Dim(0) // per output channel: [O, C/g, k, k]
	if n.Op == "ConvTranspose" {
		if st.stride != k[0] || st.pad != 0 || n.Int("group", 1) != 1 {
			return fmt.Errorf("transposed convolution other than k = stride")
		}
		st.op = opConvT
		outs = w.Dim(1) // [C, O, k, k]
	} else if g := n.Int("group", 1); g != 1 {
		if g != w.Dim(0) || w.Dim(1) != 1 {
			return fmt.Errorf("grouped convolution of %d groups", g)
		}
		st.op = opDepthwise
	}
	if b == nil {
		b = nn.New(outs)
	}
	c.done[i] = true
	out := n.Out[0]
	// fold y -> y s + t (per channel) while it is all that reads y
	for {
		j := c.sole(out)
		if j < 0 {
			break
		}
		m := &c.g.Nodes[j]
		s, t, ok := c.affine(m, outs)
		if !ok {
			break
		}
		scaleOut(w, b, s, t, st.op == opConvT)
		c.done[j] = true
		out = m.Out[0]
	}
	st.w, st.b = w, b
	st.in = []int{c.slotOf(n.In[0])}
	st.out = c.slotOf(out)
	c.net.steps = append(c.net.steps, st)
	return nil
}

// affine is a node that is y s + t for per-channel (or single) constants,
// as s and t of n channels.
func (c *compiler) affine(m *nn.Node, n int) (s, t []float32, ok bool) {
	ones := func(v float32) []float32 {
		r := make([]float32, n)
		for i := range r {
			r[i] = v
		}
		return r
	}
	per := func(x *nn.Tensor) ([]float32, bool) {
		switch len(x.Data) {
		case 1:
			return ones(x.Data[0]), true
		case n:
			return append([]float32(nil), x.Data...), true
		}
		return nil, false
	}
	switch m.Op {
	case "BatchNormalization":
		sc, bi, mu, va := c.g.Consts[m.In[1]], c.g.Consts[m.In[2]], c.g.Consts[m.In[3]], c.g.Consts[m.In[4]]
		if sc == nil || bi == nil || mu == nil || va == nil || len(sc.Data) != n {
			return nil, nil, false
		}
		eps := float64(m.Float("epsilon", 1e-5))
		s, t = make([]float32, n), make([]float32, n)
		for i := range n {
			s[i] = float32(float64(sc.Data[i]) / math.Sqrt(float64(va.Data[i])+eps))
			t[i] = bi.Data[i] - mu.Data[i]*s[i]
		}
		return s, t, true
	case "Add", "Mul", "Div":
		k, _ := c.constArg(m)
		if k == nil {
			return nil, nil, false
		}
		v, ok := per(k)
		if !ok {
			return nil, nil, false
		}
		switch m.Op {
		case "Add":
			return ones(1), v, true
		case "Mul":
			return v, ones(0), true
		default:
			if _, first := c.g.Consts[m.In[0]]; first {
				return nil, nil, false // k / y
			}
			for i := range v {
				v[i] = 1 / v[i]
			}
			return v, ones(0), true
		}
	}
	return nil, nil, false
}

// scaleOut makes a convolution's output y s + t: its weights and bias of
// each output channel times s, plus t.
func scaleOut(w, b *nn.Tensor, s, t []float32, transposed bool) {
	if transposed { // [C, O, k, k]
		c, o, kk := w.Dim(0), w.Dim(1), w.Dim(2)*w.Dim(3)
		for ci := range c {
			for oi := range o {
				for j := range kk {
					w.Data[(ci*o+oi)*kk+j] *= s[oi]
				}
			}
		}
	} else {
		per := len(w.Data) / w.Dim(0)
		for oi := range w.Dim(0) {
			for j := range per {
				w.Data[oi*per+j] *= s[oi]
			}
		}
	}
	for oi := range b.Data {
		b.Data[oi] = b.Data[oi]*s[oi] + t[oi]
	}
}

// act is a chain of elementwise operations, from node i on: affine
// operations by single constants, and at most one activation.
func (c *compiler) act(i int) error {
	n := &c.g.Nodes[i]
	in := n.In[0]
	if k, other := c.constArg(n); k != nil {
		in = n.In[other]
	}
	a := nn.Identity
	cur := in
	j := i
	for j >= 0 {
		m := &c.g.Nodes[j]
		next, ok := c.fold(&a, m, cur, j == i)
		if !ok {
			break
		}
		cur = next
		j = c.next(cur, &a)
	}
	if cur == in {
		return fmt.Errorf("not an elementwise operation the detector has")
	}
	c.net.steps = append(c.net.steps, step{op: opAct, in: []int{c.slotOf(in)}, out: c.slotOf(cur), act: a})
	return nil
}

// next is the node to fold after the tensor cur: its only reader, or the
// Add of a hard swish (x clip(x+3, 0, 6) / 6, which reads x twice).
func (c *compiler) next(cur string, a *nn.Act) int {
	if j := c.sole(cur); j >= 0 {
		return j
	}
	u := c.users[cur]
	if len(u) == 2 && a.Fn == nn.ActNone && !c.done[u[0]] && !c.done[u[1]] {
		for _, j := range u {
			if c.g.Nodes[j].Op == "Add" {
				return j
			}
		}
	}
	return -1
}

// fold adds node m, which reads cur, to the act a, and is the tensor it
// makes; false when it is not foldable. first says m is the chain's first
// node, whose input is made whatever else reads it.
func (c *compiler) fold(a *nn.Act, m *nn.Node, cur string, first bool) (string, bool) {
	affine := func(s, t float32) {
		if a.Fn == nn.ActNone {
			a.PreS, a.PreT = a.PreS*s, a.PreT*s+t
		} else {
			a.PostS, a.PostT = a.PostS*s, a.PostT*s+t
		}
	}
	activation := func(fn int) bool {
		if a.Fn != nn.ActNone {
			return false
		}
		a.Fn = fn
		return true
	}
	j := c.index(m)
	switch m.Op {
	case "Add", "Mul", "Div":
		k, other := c.constArg(m)
		v, ok := scalar(k)
		if !ok || m.In[other] != cur {
			return "", false
		}
		if m.Op == "Add" && v == 3 && a.Fn == nn.ActNone && len(c.users[cur]) == 2 {
			if out, ok := c.hardSwish(j, cur); ok {
				a.Fn = nn.ActHardSwish
				return out, true
			}
		}
		if len(c.users[cur]) != 1 && !first {
			return "", false // cur is read again, so it must be made
		}
		switch m.Op {
		case "Add":
			affine(1, v)
		case "Mul":
			affine(v, 0)
		default:
			if other != 0 {
				return "", false
			}
			affine(1/v, 0)
		}
	case "Relu":
		if !activation(nn.ActReLU) {
			return "", false
		}
	case "Sigmoid":
		if !activation(nn.ActSigmoid) {
			return "", false
		}
	case "HardSigmoid":
		if !activation(nn.ActHardSigmoid) {
			return "", false
		}
		a.A, a.B = m.Float("alpha", 0.2), m.Float("beta", 0.5)
	default:
		return "", false
	}
	c.done[j] = true
	return m.Out[0], true
}

// hardSwish matches x+3 -> clip(0, 6) -> x * that -> / 6 from the Add at
// node j, and is the tensor the Div makes.
func (c *compiler) hardSwish(j int, x string) (string, bool) {
	add := &c.g.Nodes[j]
	cl := c.sole(add.Out[0])
	if cl < 0 || c.g.Nodes[cl].Op != "Clip" {
		return "", false
	}
	clip := &c.g.Nodes[cl]
	lo, ok1 := scalar(c.g.Consts[clip.In[1]])
	hi, ok2 := scalar(c.g.Consts[clip.In[2]])
	if !ok1 || !ok2 || lo != 0 || hi != 6 {
		return "", false
	}
	mu := c.sole(clip.Out[0])
	if mu < 0 || c.g.Nodes[mu].Op != "Mul" {
		return "", false
	}
	mul := &c.g.Nodes[mu]
	if pair := [2]string{mul.In[0], mul.In[1]}; pair != [2]string{x, clip.Out[0]} && pair != [2]string{clip.Out[0], x} {
		return "", false
	}
	dv := c.sole(mul.Out[0])
	if dv < 0 || c.g.Nodes[dv].Op != "Div" {
		return "", false
	}
	div := &c.g.Nodes[dv]
	if v, ok := scalar(c.g.Consts[div.In[1]]); !ok || v != 6 || div.In[0] != mul.Out[0] {
		return "", false
	}
	for _, k := range []int{j, cl, mu, dv} {
		c.done[k] = true
	}
	return div.Out[0], true
}

func (c *compiler) index(m *nn.Node) int {
	for i := range c.g.Nodes {
		if &c.g.Nodes[i] == m {
			return i
		}
	}
	return -1
}

// binary is an Add or a Mul of two computed tensors: a residual sum, or a
// squeeze-and-excitation's channel weights (one side [C, 1, 1]).
func (c *compiler) binary(i int) error {
	n := &c.g.Nodes[i]
	a, b := c.slotOf(n.In[0]), c.slotOf(n.In[1])
	if n.Op == "Add" {
		c.emit(i, step{op: opAdd, in: []int{a, b}}, n.Out[0])
	} else {
		c.emit(i, step{op: opMulCh, in: []int{a, b}}, n.Out[0])
	}
	return nil
}

// markInPlace lets an Add write over its first input when no later step
// reads it.
func (c *compiler) markInPlace() {
	last := make([]int, c.net.slots)
	for i := range last {
		last[i] = -1
	}
	for i, st := range c.net.steps {
		for _, s := range st.in {
			last[s] = i
		}
	}
	last[c.net.output] = len(c.net.steps)
	for i := range c.net.steps {
		st := &c.net.steps[i]
		if st.op == opAdd && last[st.in[0]] == i && st.in[0] != st.in[1] && st.in[0] != c.net.input {
			st.inPlace = true
		}
	}
}

// Layers is the number of steps the net runs.
func (n *Net) Layers() int { return len(n.steps) }

// upload puts the weights on a backend, once.
func (n *Net) upload(be nn.Backend) {
	if n.on == be {
		return
	}
	n.w, n.b = map[*nn.Tensor]*nn.Tensor{}, map[*nn.Tensor]*nn.Tensor{}
	for _, st := range n.steps {
		for _, p := range []struct {
			t *nn.Tensor
			m map[*nn.Tensor]*nn.Tensor
		}{{st.w, n.w}, {st.b, n.b}} {
			if p.t != nil {
				t := be.Upload(p.t)
				be.Keep(t)
				p.m[p.t] = t
			}
		}
	}
	n.on = be
}

// Run runs the net on be, from x [3, H, W] (on be) to its output.
func (n *Net) Run(be nn.Backend, x *nn.Tensor) *nn.Tensor {
	n.upload(be)
	v := make([]*nn.Tensor, n.slots)
	v[n.input] = x
	for _, st := range n.steps {
		a := v[st.in[0]]
		var y *nn.Tensor
		switch st.op {
		case opConv:
			y = be.Conv2d(a, n.w[st.w], n.b[st.b], st.stride, st.pad)
		case opDepthwise:
			y = be.DepthwiseConv2d(a, n.w[st.w], n.b[st.b], st.stride, st.pad)
		case opConvT:
			y = be.ConvTranspose2dK(a, n.w[st.w], n.b[st.b])
		case opAct:
			y = be.Activate(a, st.act)
		case opGAP:
			y = be.GlobalAvgPool(a)
		case opMulCh:
			b := v[st.in[1]]
			if a.Dim(1)*a.Dim(2) == 1 {
				a, b = b, a
			}
			y = be.MulChannels(a, b)
		case opAdd:
			if !st.inPlace {
				a = be.Clone(a)
			}
			y = be.Add(a, v[st.in[1]])
		case opNearest:
			y = be.Nearest(a, st.f)
		case opConcat:
			xs := make([]*nn.Tensor, len(st.in))
			for i, s := range st.in {
				xs[i] = v[s]
			}
			y = be.Concat(xs...)
		}
		v[st.out] = y
	}
	return v[n.output]
}
