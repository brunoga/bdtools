package depth

import (
	"fmt"
	"math"

	"github.com/brunoga/bdtools/internal/nn"
)

// The DPT head, written against Ops and Params so that the one code runs
// on a backend for inference (Model) and on an nn.Tape for learning
// (tools/depthtrain).

// Ops are the layers the head is made of: a backend's (every nn.Backend
// has them) or an nn.Tape's. The head uses what each op gives, never
// counting on one being in place, and gives the ops that are in place on
// a backend (ReLU, Add) nothing it still needs.
type Ops interface {
	Upload(t *nn.Tensor) *nn.Tensor
	Conv2d(x, w, b *nn.Tensor, stride, pad int) *nn.Tensor
	ConvTranspose2dK(x, w, b *nn.Tensor) *nn.Tensor
	Linear(x, w, b *nn.Tensor) *nn.Tensor
	LayerNorm(x, w, b *nn.Tensor, eps float32) *nn.Tensor
	GroupNorm(x *nn.Tensor, groups int, w, b *nn.Tensor, eps float32) *nn.Tensor
	GEGLU(x *nn.Tensor) *nn.Tensor
	ReLU(x *nn.Tensor) *nn.Tensor
	Add(a, b *nn.Tensor) *nn.Tensor
	Clone(x *nn.Tensor) *nn.Tensor
	Bilinear(x *nn.Tensor, oh, ow int) *nn.Tensor
	Transpose(x *nn.Tensor) *nn.Tensor
	TemporalAttention(q, peQ, peK, peV *nn.Tensor, ks, vs []*nn.Tensor, heads int, scale float32) *nn.Tensor
	// the encoder's
	Attention(q, k, v *nn.Tensor, heads int, scale float32) *nn.Tensor
	GELU(x *nn.Tensor) *nn.Tensor
	AddScaled(x, y, s *nn.Tensor) *nn.Tensor
	Tokens(patches, cls, pos *nn.Tensor) *nn.Tensor
	Split3(x *nn.Tensor) (*nn.Tensor, *nn.Tensor, *nn.Tensor)
	Rows(x *nn.Tensor, from int) *nn.Tensor
}

// Params are the network's weights, as the ops' tensors: T a weight by
// its checkpoint name, Opt one that may be absent (a bias: nil then), PE
// a temporal attention block's position encodings projected by its
// query, key and value weights, for a window of t frames: the query's
// (the last place's, [C]) and the keys' and values' ([t, C]); Pos the
// encoder's position embedding for a grid of patches ([1 + ph pw, D],
// the class token's first), and Arch the encoder's depth, heads and
// width.
type Params interface {
	T(name string) *nn.Tensor
	Opt(name string) *nn.Tensor
	PE(block string, c, t int) [3]*nn.Tensor
	Pos(ph, pw int) *nn.Tensor
	Arch() (blocks, heads, dim int)
}

// Encoder runs DINOv2 on o with p: x the frame as the network takes it
// ([3, H, W], o's tensor); the patch tokens of the intermediate blocks'
// outputs, normed ([ph pw, D] each).
func Encoder(o Ops, p Params, x *nn.Tensor) [4]*nn.Tensor {
	ph, pw := x.Dim(1)/patch, x.Dim(2)/patch
	blocks, heads, d := p.Arch()
	pe := o.Conv2d(x, p.T("pretrained.patch_embed.proj.weight"), p.T("pretrained.patch_embed.proj.bias"), patch, 0)
	tok := o.Tokens(pe.Reshape(d, ph*pw), p.T("pretrained.cls_token"), p.Pos(ph, pw))
	var out [4]*nn.Tensor
	k := 0
	hd := d / heads
	at := intermediate[blocks]
	for b := range blocks {
		pre := fmt.Sprintf("pretrained.blocks.%d.", b)
		// x + ls1 * attn(norm1(x))
		h := o.LayerNorm(tok, p.T(pre+"norm1.weight"), p.T(pre+"norm1.bias"), 1e-6)
		q, kk, v := o.Split3(o.Linear(h, p.T(pre+"attn.qkv.weight"), p.Opt(pre+"attn.qkv.bias")))
		a := o.Attention(q, kk, v, heads, float32(1/math.Sqrt(float64(hd))))
		a = o.Linear(a, p.T(pre+"attn.proj.weight"), p.Opt(pre+"attn.proj.bias"))
		tok = o.AddScaled(tok, a, p.T(pre+"ls1.gamma"))
		// x + ls2 * mlp(norm2(x))
		h = o.LayerNorm(tok, p.T(pre+"norm2.weight"), p.T(pre+"norm2.bias"), 1e-6)
		h = o.GELU(o.Linear(h, p.T(pre+"mlp.fc1.weight"), p.Opt(pre+"mlp.fc1.bias")))
		h = o.Linear(h, p.T(pre+"mlp.fc2.weight"), p.Opt(pre+"mlp.fc2.bias"))
		tok = o.AddScaled(tok, h, p.T(pre+"ls2.gamma"))
		if k < 4 && b == at[k] {
			n := o.LayerNorm(tok, p.T("pretrained.norm.weight"), p.T("pretrained.norm.bias"), 1e-6)
			out[k] = o.Rows(n, 1)
			k++
		}
	}
	return out
}

// positionEmbedding is the encoder's position embedding (pe, the
// checkpoint's [1, 1 + side^2, D]) for a grid of ph x pw patches, the
// patches' part scaled to it bicubically as DINOv2 does: [1 + ph pw, D],
// the class token's first.
func positionEmbedding(pe *nn.Tensor, ph, pw int) *nn.Tensor {
	d := pe.Dim(-1)
	n := pe.Dim(1) - 1
	side := int(math.Round(math.Sqrt(float64(n))))
	grid := nn.New(d, side, side) // channels first
	for i := range n {
		for c := range d {
			grid.Data[c*n+i] = pe.Data[(1+i)*d+c]
		}
	}
	r := nn.BicubicScale(grid, (float64(ph)+0.1)/float64(side), (float64(pw)+0.1)/float64(side))
	out := nn.New(1+ph*pw, d)
	copy(out.Data[:d], pe.Data[:d])
	for i := range ph * pw {
		for c := range d {
			out.Data[(1+i)*d+c] = r.Data[c*ph*pw+i]
		}
	}
	return out
}

// KV is a frame's keys and values for a temporal attention block ([D, C]
// each, D the positions), less the position encoding's part.
type KV struct{ K, V *nn.Tensor }

// column is block b's states in a window.
func column(win [][8]KV, b int) []KV {
	out := make([]KV, len(win))
	for i, w := range win {
		out[i] = w[b]
	}
	return out
}

// StartWindow is the window a stream's frame sees when states are the
// frames' before it (oldest first), as Infer builds it from the frames it
// has seen: nil for a stream's first frame.
func StartWindow(states [][8]KV) [][8]KV {
	if len(states) == 0 {
		return nil
	}
	var cache [][8]KV
	for n, s := range states {
		if n == 0 {
			cache = make([][8]KV, inferLen)
			for i := range cache {
				cache[i] = s
			}
		} else {
			cache = append(cache, s)
		}
		if n+inferLen > cacheLen {
			cache = append(cache[:1], cache[2:]...)
		}
	}
	return window(cache)
}

// window is the frames a frame attends to, of the cache of the frames
// before it: the first two and the last inferLen-3, as the reference does.
func window(cache [][8]KV) [][8]KV {
	var win [][8]KV
	return append(append(win, cache[0:2]...), cache[len(cache)-(inferLen-3):]...)
}

// head is a pass of the head on ops with params.
type head struct {
	o Ops
	p Params
}

// Head runs the DPT head on a frame's encoder features (feats, [ph pw, C]
// each) with the window of the frames before it (win, oldest first; see
// StartWindow): the depth at the patch grid times 14 ([1, H, W], Infer's
// before its final resize) and the frame's eight temporal states. tap,
// when not nil, is given the tail's inputs.
func Head(o Ops, p Params, feats [4]*nn.Tensor, ph, pw int, win [][8]KV, tap func(TailInput)) (*nn.Tensor, [8]KV) {
	h := head{o, p}
	var layers, maps [4]*nn.Tensor
	for i, f := range feats {
		x := h.tokensToMap(f, ph, pw)
		maps[i] = x
		layers[i] = h.project(i, x)
	}
	rn0, rn1 := h.rn(0, layers[0]), h.rn(1, layers[1])
	var states [8]KV
	var s [2]KV
	layers[2], s = h.temporal(0, layers[2], column(win, 0), column(win, 1))
	states[0], states[1] = s[0], s[1]
	layers[3], s = h.temporal(1, layers[3], column(win, 2), column(win, 3))
	states[2], states[3] = s[0], s[1]
	rn2, rn3 := h.rn(2, layers[2]), h.rn(3, layers[3])
	p4 := h.fusion(4, rn3, nil, rn2.Dim(1), rn2.Dim(2))
	p4, s = h.temporal(2, p4, column(win, 4), column(win, 5))
	states[4], states[5] = s[0], s[1]
	p3 := h.fusion(3, p4, rn2, rn1.Dim(1), rn1.Dim(2))
	p3, s = h.temporal(3, p3, column(win, 6), column(win, 7))
	states[6], states[7] = s[0], s[1]
	if tap != nil {
		tap(TailInput{Feat0: maps[0], Feat1: maps[1], P3: p3, H: ph * patch, W: pw * patch})
	}
	return h.tail(rn0, rn1, p3, ph*patch, pw*patch), states
}

// Tail runs the head's tail (TailNames' layers) on its inputs: the depth
// at the patch grid times 14 ([1, H, W]), as Head gives it.
func Tail(o Ops, p Params, in TailInput) *nn.Tensor {
	h := head{o, p}
	return h.tail(h.rn(0, h.project(0, in.Feat0)), h.rn(1, h.project(1, in.Feat1)), in.P3, in.H, in.W)
}

// tail is the head from the third fusion stage on: the last two fusion
// blocks and the output convolutions, the depth [1, H, W].
func (h head) tail(rn0, rn1, p3 *nn.Tensor, hh, w int) *nn.Tensor {
	o, p := h.o, h.p
	p2 := h.fusion(2, p3, rn1, rn0.Dim(1), rn0.Dim(2))
	p1 := h.fusion(1, p2, rn0, 2*rn0.Dim(1), 2*rn0.Dim(2))
	out := o.Conv2d(p1, p.T("head.scratch.output_conv1.weight"), p.Opt("head.scratch.output_conv1.bias"), 1, 1)
	out = o.Bilinear(out, hh, w)
	out = o.ReLU(o.Conv2d(out, p.T("head.scratch.output_conv2.0.weight"), p.Opt("head.scratch.output_conv2.0.bias"), 1, 1))
	return o.ReLU(o.Conv2d(out, p.T("head.scratch.output_conv2.2.weight"), p.Opt("head.scratch.output_conv2.2.bias"), 1, 0))
}

// project is feature i's projection and resizing (the second's is the
// identity): a map [C, ph, pw] to the fusion's layer i.
func (h head) project(i int, x *nn.Tensor) *nn.Tensor {
	o, p := h.o, h.p
	x = o.Conv2d(x, p.T(fmt.Sprintf("head.projects.%d.weight", i)), p.T(fmt.Sprintf("head.projects.%d.bias", i)), 1, 0)
	rw, rb := p.Opt(fmt.Sprintf("head.resize_layers.%d.weight", i)), p.Opt(fmt.Sprintf("head.resize_layers.%d.bias", i))
	switch i {
	case 0, 1:
		x = o.ConvTranspose2dK(x, rw, rb)
	case 3:
		x = o.Conv2d(x, rw, rb, 2, 1)
	}
	return x
}

// rn is layer i's convolution into the fusion's width.
func (h head) rn(i int, x *nn.Tensor) *nn.Tensor {
	return h.o.Conv2d(x, h.p.T(fmt.Sprintf("head.scratch.layer%d_rn.weight", i+1)), nil, 1, 1)
}

// fusion is refinenet k (a FeatureFusionBlock): x plus the residual unit
// of skip (when there is one), a residual unit, resized to oh x ow, a 1x1
// convolution.
func (h head) fusion(k int, x, skip *nn.Tensor, oh, ow int) *nn.Tensor {
	pre := fmt.Sprintf("head.scratch.refinenet%d.", k)
	o, p := h.o, h.p
	out := x
	if skip != nil {
		out = o.Add(o.Clone(x), h.residual(pre+"resConfUnit1.", skip))
	}
	out = h.residual(pre+"resConfUnit2.", out)
	out = o.Bilinear(out, oh, ow)
	return o.Conv2d(out, p.T(pre+"out_conv.weight"), p.Opt(pre+"out_conv.bias"), 1, 0)
}

// residual is a ResidualConvUnit: x + conv2(relu(conv1(relu(x)))).
func (h head) residual(pre string, x *nn.Tensor) *nn.Tensor {
	o, p := h.o, h.p
	y := o.Conv2d(o.ReLU(o.Clone(x)), p.T(pre+"conv1.weight"), p.Opt(pre+"conv1.bias"), 1, 1)
	y = o.Conv2d(o.ReLU(y), p.T(pre+"conv2.weight"), p.Opt(pre+"conv2.bias"), 1, 1)
	return o.Add(y, x)
}

// tokensToMap turns [H W, C] tokens into a [C, H, W] map.
func (h head) tokensToMap(t *nn.Tensor, hh, w int) *nn.Tensor {
	return h.o.Transpose(t).Reshape(t.Dim(-1), hh, w)
}

// mapToTokens is tokensToMap's inverse.
func (h head) mapToTokens(x *nn.Tensor) *nn.Tensor {
	return h.o.Transpose(x.Reshape(x.Dim(0), x.Dim(1)*x.Dim(2)))
}

// temporal is motion module k on one frame's map x [C, H, W]: a group
// norm, a projection to tokens, the transformer block (two temporal
// attentions over the frames' cached states, then a GEGLU feed forward),
// a projection back, plus x. It gives the map and the two attentions'
// states for the frame (their inputs).
func (h head) temporal(k int, x *nn.Tensor, past0, past1 []KV) (*nn.Tensor, [2]KV) {
	pre := fmt.Sprintf("head.motion_modules.%d.temporal_transformer.", k)
	o, p := h.o, h.p
	c, hh, w := x.Dim(0), x.Dim(1), x.Dim(2)
	n := o.GroupNorm(x, 32, p.T(pre+"norm.weight"), p.T(pre+"norm.bias"), 1e-6)
	tok := o.Linear(h.mapToTokens(n), p.T(pre+"proj_in.weight"), p.Opt(pre+"proj_in.bias"))
	bp := pre + "transformer_blocks.0."
	var states [2]KV
	for i, past := range [2][]KV{past0, past1} {
		ap := fmt.Sprintf("%sattention_blocks.%d.", bp, i)
		normed := o.LayerNorm(tok, p.T(fmt.Sprintf("%snorms.%d.weight", bp, i)), p.T(fmt.Sprintf("%snorms.%d.bias", bp, i)), 1e-5)
		out, cur := h.temporalAttention(ap, normed, past)
		states[i] = cur
		tok = o.Add(tok, out)
	}
	ff := o.LayerNorm(tok, p.T(bp+"ff_norm.weight"), p.T(bp+"ff_norm.bias"), 1e-5)
	ff = o.GEGLU(o.Linear(ff, p.T(bp+"ff.net.0.proj.weight"), p.Opt(bp+"ff.net.0.proj.bias")))
	ff = o.Linear(ff, p.T(bp+"ff.net.2.weight"), p.Opt(bp+"ff.net.2.bias"))
	tok = o.Add(tok, ff)
	tok = o.Linear(tok, p.T(pre+"proj_out.weight"), p.Opt(pre+"proj_out.bias"))
	return o.Add(h.tokensToMap(tok, hh, w), x).Reshape(c, hh, w), states
}

// temporalAttention is a TemporalAttention on the current frame's states
// x [D, C]: at each position, the frame attends over the window's frames
// and itself, each with its place's position encoding; the query is the
// current frame's alone. As (x + pe) W = x W + pe W, a frame's keys and
// values are its states' projections (kept: the frame's KV, returned)
// plus the projected encoding of its place (Params.PE).
func (h head) temporalAttention(ap string, x *nn.Tensor, past []KV) (*nn.Tensor, KV) {
	o, p := h.o, h.p
	c := x.Dim(1)
	t := len(past) + 1
	cur := KV{o.Linear(x, p.T(ap+"to_k.weight"), p.Opt(ap+"to_k.bias")), o.Linear(x, p.T(ap+"to_v.weight"), p.Opt(ap+"to_v.bias"))}
	qx := o.Linear(x, p.T(ap+"to_q.weight"), p.Opt(ap+"to_q.bias"))
	pes := p.PE(ap, c, t)
	ks, vs := make([]*nn.Tensor, t), make([]*nn.Tensor, t)
	for f, past := range past {
		ks[f], vs[f] = past.K, past.V
	}
	ks[t-1], vs[t-1] = cur.K, cur.V
	hd := c / temporalHead
	out := o.TemporalAttention(qx, pes[0], pes[1], pes[2], ks, vs, temporalHead, float32(1/math.Sqrt(float64(hd))))
	return o.Linear(out, p.T(ap+"to_out.0.weight"), p.Opt(ap+"to_out.0.bias")), cur
}

// PositionEncoding is an attention block's sinusoidal position encoding
// ([frames, C]): the checkpoint's (w's), else made as the reference's
// PositionalEncoding makes it.
func PositionEncoding(w map[string]*nn.Tensor, block string, c int) *nn.Tensor {
	if p, ok := w[block+"pos_encoder.pe"]; ok {
		return p.Reshape(-1, c)
	}
	p := nn.New(inferLen, c)
	for pos := range inferLen {
		for i := 0; i < c; i += 2 {
			div := math.Exp(float64(i) * -math.Log(10000) / float64(c))
			p.Data[pos*c+i] = float32(math.Sin(float64(pos) * div))
			if i+1 < c {
				p.Data[pos*c+i+1] = float32(math.Cos(float64(pos) * div))
			}
		}
	}
	return p
}

// TapeParams are Params on an nn.Tape for learning: the weights w (in
// memory, by their checkpoint names), those learn says of as the tape's
// learnt weights (their gradients gathered under their names), the rest
// uploaded as they are; the position encodings' projections computed on
// the tape, so the gradients reach the attention weights through them
// too. They serve one pass: the tape's tensors go at its Release.
func TapeParams(tp *nn.Tape, w map[string]*nn.Tensor, learn func(name string) bool) Params {
	return &tapeParams{tp: tp, w: w, learn: learn, on: map[string]*nn.Tensor{}, pes: map[peKey][3]*nn.Tensor{}}
}

type tapeParams struct {
	tp    *nn.Tape
	w     map[string]*nn.Tensor
	learn func(string) bool
	on    map[string]*nn.Tensor
	pes   map[peKey][3]*nn.Tensor
	pos   map[[2]int]*nn.Tensor
}

func (p *tapeParams) Pos(ph, pw int) *nn.Tensor {
	if t, ok := p.pos[[2]int{ph, pw}]; ok {
		return t
	}
	if p.pos == nil {
		p.pos = map[[2]int]*nn.Tensor{}
	}
	t := p.tp.Upload(positionEmbedding(p.w["pretrained.pos_embed"], ph, pw))
	p.pos[[2]int{ph, pw}] = t
	return t
}

func (p *tapeParams) Arch() (blocks, heads, dim int) {
	blocks, heads, dim, err := arch(p.w)
	if err != nil {
		panic(err)
	}
	return blocks, heads, dim
}

func (p *tapeParams) T(name string) *nn.Tensor {
	if t, ok := p.on[name]; ok {
		return t
	}
	w, ok := p.w[name]
	if !ok {
		panic("depth: no weight " + name)
	}
	var t *nn.Tensor
	if p.learn(name) {
		t = p.tp.Param(name, w)
	} else {
		t = p.tp.Upload(w)
	}
	p.on[name] = t
	return t
}

func (p *tapeParams) Opt(name string) *nn.Tensor {
	if _, ok := p.w[name]; !ok {
		return nil
	}
	return p.T(name)
}

func (p *tapeParams) PE(block string, c, t int) [3]*nn.Tensor {
	if pes, ok := p.pes[peKey{block, t}]; ok {
		return pes
	}
	pe := PositionEncoding(p.w, block, c)
	pt := p.tp.Upload(&nn.Tensor{Shape: []int{t, c}, Data: pe.Data[:t*c]})
	last := p.tp.Upload(&nn.Tensor{Shape: []int{1, c}, Data: pe.Data[(t-1)*c : t*c]})
	pes := [3]*nn.Tensor{
		p.tp.Linear(last, p.T(block+"to_q.weight"), nil).Reshape(c),
		p.tp.Linear(pt, p.T(block+"to_k.weight"), nil),
		p.tp.Linear(pt, p.T(block+"to_v.weight"), nil),
	}
	p.pes[peKey{block, t}] = pes
	return pes
}
