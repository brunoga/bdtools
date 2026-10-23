// Package depth estimates a video's depth, frame by frame: Video Depth
// Anything (Chen et al., CVPR 2025), its streaming form, from the
// published checkpoint as it is. A frame goes through a DINOv2 vision
// transformer; a DPT head makes a depth map of its features, with four
// temporal attention modules in it that look at the frames before (their
// states cached: one frame in, one depth map out).
package depth

import (
	"errors"
	"fmt"
	"math"

	"github.com/brunoga/bdtools/internal/nn"
)

const (
	patch        = 14
	inferLen     = 32 // the temporal window
	cacheLen     = 42 // the frames' states kept (gap + 1 in the reference)
	temporalHead = 8  // the temporal attention's heads
)

// intermediate are the encoder blocks whose outputs the head reads, by
// the encoder's depth: Small's (and Base's) 12 blocks, Large's 24.
var intermediate = map[int][4]int{12: {2, 5, 8, 11}, 24: {4, 11, 17, 23}}

// Model is a loaded Video Depth Anything and its stream's state.
type Model struct {
	w      map[string]*nn.Tensor // in memory
	heads  int                   // the encoder's attention heads
	dim    int                   // its width
	blocks int                   // its depth

	be  nn.Backend
	dw  map[string]*nn.Tensor   // the weights on the backend
	pos map[[2]int]*nn.Tensor   // the position embedding, by patch grid
	pes map[peKey][3]*nn.Tensor // the projected position encodings

	// cache holds the frames' temporal states, oldest first: for each, the
	// keys and values of the head's eight temporal attention blocks (their
	// projections less the position encoding's, which is added at its
	// place in the window: the projections are linear).
	cache [][8]kv
	n     int // the frames done
}

// Load reads a checkpoint (video_depth_anything_vits.pth, or another
// model of the same make).
func Load(file string) (*Model, error) {
	w, err := nn.LoadPTH(file)
	if err != nil {
		return nil, err
	}
	return New(w)
}

// New is a model of loaded weights. Its encoder's attention heads are not
// in them: every DINOv2's are 64 channels each (the small model's 384 in
// 6), a model narrower than 64's 12 (the tests').
func New(w map[string]*nn.Tensor) (*Model, error) {
	cls, ok := w["pretrained.cls_token"]
	if !ok {
		return nil, errors.New("depth: not a Video Depth Anything checkpoint (no pretrained.cls_token)")
	}
	for _, k := range []string{"pretrained.pos_embed", "pretrained.patch_embed.proj.weight", "head.scratch.output_conv2.2.weight"} {
		if _, ok := w[k]; !ok {
			return nil, fmt.Errorf("depth: the checkpoint has no %s", k)
		}
	}
	dim := cls.Dim(-1)
	hd := 64
	if dim < 64 {
		hd = 12
	}
	if dim%hd != 0 {
		return nil, fmt.Errorf("depth: an encoder %d wide is not in heads of %d", dim, hd)
	}
	blocks := 0
	for w[fmt.Sprintf("pretrained.blocks.%d.norm1.weight", blocks)] != nil {
		blocks++
	}
	if _, ok := intermediate[blocks]; !ok {
		return nil, fmt.Errorf("depth: an encoder of %d blocks (Small's 12 and Large's 24 are known)", blocks)
	}
	m := &Model{w: w, heads: dim / hd, dim: dim, blocks: blocks}
	m.Use(nn.CPU)
	return m, nil
}

// Size is the model's: "small", "base" or "large" (by its encoder's
// width; "" for another, the tests' small random ones), each its own
// scale of depth.
func (m *Model) Size() string {
	switch m.dim {
	case 384:
		return "small"
	case 768:
		return "base"
	case 1024:
		return "large"
	}
	return ""
}

// HalfSafe reports whether the model's matrix products may take their
// inputs in half precision (nn.GPU's Half): Small's and Base's lose
// nothing measurable; Large's activations overflow it (NaN on glass).
func (m *Model) HalfSafe() bool { return m.blocks <= 12 }

// Backend is the backend the model runs on.
func (m *Model) Backend() nn.Backend { return m.be }

// Use runs the model on a backend (nn.CPU at first; an nn.GPU, say): the
// weights go there as they are needed. It starts a new stream.
func (m *Model) Use(be nn.Backend) {
	if m.be != nil {
		m.Reset()
		for _, t := range m.dw {
			m.be.Free(t)
		}
		for _, t := range m.pos {
			m.be.Free(t)
		}
		for _, p := range m.pes {
			for _, t := range p {
				m.be.Free(t)
			}
		}
	}
	m.be = be
	m.dw, m.pos, m.pes = map[string]*nn.Tensor{}, map[[2]int]*nn.Tensor{}, map[peKey][3]*nn.Tensor{}
}

// t is a weight on the backend.
func (m *Model) t(name string) *nn.Tensor {
	if d, ok := m.dw[name]; ok {
		return d
	}
	v, ok := m.w[name]
	if !ok {
		panic("depth: no weight " + name)
	}
	d := m.be.Upload(v)
	m.be.Keep(d)
	m.dw[name] = d
	return d
}

// opt is a weight that may be missing (a bias): nil then.
func (m *Model) opt(name string) *nn.Tensor {
	if _, ok := m.w[name]; !ok {
		return nil
	}
	return m.t(name)
}

// Reset forgets the frames before: the next frame starts a new stream (a
// new shot, say).
func (m *Model) Reset() {
	seen := map[*nn.Tensor]bool{}
	for _, s := range m.cache {
		for _, st := range s {
			for _, t := range [2]*nn.Tensor{st.k, st.v} {
				if !seen[t] {
					seen[t] = true
					m.be.Free(t)
				}
			}
		}
	}
	m.cache, m.n = nil, 0
}

// Frame gives the depth of the next frame of the stream: x is the frame
// as the network takes it ([3, H, W], H and W multiples of 14, the colours
// normalised by ImageNet's mean and deviation); the depth is [H, W], a
// relative inverse depth (larger nearer). It panics on the backend's
// failure (a GPU's); FrameErr reports it.
func (m *Model) Frame(x *nn.Tensor) *nn.Tensor {
	d, err := m.FrameErr(x)
	if err != nil {
		panic(err)
	}
	return d
}

// FrameErr is Frame, the backend's failure an error.
func (m *Model) FrameErr(x *nn.Tensor) (*nn.Tensor, error) {
	h, w := x.Dim(1), x.Dim(2)
	out, err := m.be.Download(m.Infer(m.be.Upload(x)))
	m.be.Release()
	if err != nil {
		return nil, err
	}
	return out.Reshape(h, w), nil
}

// Infer is Frame on the backend: x the backend's tensor, the depth [1, H,
// W] the backend's, the intermediates not released.
func (m *Model) Infer(x *nn.Tensor) *nn.Tensor {
	h, w := x.Dim(1), x.Dim(2)
	ph, pw := h/patch, w/patch
	feats := m.encode(x, ph, pw)
	var win [][8]kv
	if m.n > 0 {
		// the window: the first two frames' states and the last
		// inferLen-3, as the reference does
		win = append(append(win, m.cache[0:2]...), m.cache[len(m.cache)-(inferLen-3):]...)
	}
	d, states := m.head(feats, ph, pw, win)
	for _, s := range states {
		m.be.Keep(s.k)
		m.be.Keep(s.v)
	}
	if m.n == 0 {
		m.cache = make([][8]kv, inferLen)
		for i := range m.cache {
			m.cache[i] = states
		}
	} else {
		m.cache = append(m.cache, states)
	}
	if m.n+inferLen > cacheLen {
		gone := m.cache[1]
		m.cache = append(m.cache[:1], m.cache[2:]...)
		m.drop(gone)
	}
	m.n++
	return m.be.ReLU(m.be.Bilinear(d, h, w))
}

// drop frees a frame's states no longer in the cache.
func (m *Model) drop(s [8]kv) {
	for _, c := range m.cache {
		if c == s {
			return // the first frame's, repeated at the start
		}
	}
	for _, st := range s {
		m.be.Free(st.k)
		m.be.Free(st.v)
	}
}

// kv is a frame's keys and values for a temporal attention block ([D, C]
// each, D the positions), less the position encoding's part.
type kv struct{ k, v *nn.Tensor }

// column is block b's states in a window.
func column(win [][8]kv, b int) []kv {
	out := make([]kv, len(win))
	for i, w := range win {
		out[i] = w[b]
	}
	return out
}

// posEmbed is the position embedding for a ph x pw grid: the class
// token's, then the patches', bicubically resized from the 37x37 grid it
// was trained on (DINOv2's interpolate_pos_encoding, offset 0.1).
func (m *Model) posEmbed(ph, pw int) *nn.Tensor {
	if p, ok := m.pos[[2]int{ph, pw}]; ok {
		return p
	}
	pe := m.w["pretrained.pos_embed"]
	n := pe.Dim(1) - 1
	side := int(math.Round(math.Sqrt(float64(n))))
	d := m.dim
	grid := nn.New(d, side, side) // [D, 37, 37], channels first
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
	d2 := m.be.Upload(out)
	m.be.Keep(d2)
	m.pos[[2]int{ph, pw}] = d2
	return d2
}

// encode runs DINOv2: the patch tokens of the intermediate blocks'
// outputs, normed ([ph pw, D] each).
func (m *Model) encode(x *nn.Tensor, ph, pw int) [4]*nn.Tensor {
	d, be := m.dim, m.be
	p := be.Conv2d(x, m.t("pretrained.patch_embed.proj.weight"), m.t("pretrained.patch_embed.proj.bias"), patch, 0)
	tok := be.Tokens(p.Reshape(d, ph*pw), m.t("pretrained.cls_token"), m.posEmbed(ph, pw))
	var out [4]*nn.Tensor
	k := 0
	hd := d / m.heads
	at := intermediate[m.blocks]
	for b := range m.blocks {
		pre := fmt.Sprintf("pretrained.blocks.%d.", b)
		// x + ls1 * attn(norm1(x))
		h := be.LayerNorm(tok, m.t(pre+"norm1.weight"), m.t(pre+"norm1.bias"), 1e-6)
		qkv := be.Linear(h, m.t(pre+"attn.qkv.weight"), m.opt(pre+"attn.qkv.bias"))
		q, kk, v := be.Split3(qkv)
		a := be.Attention(q, kk, v, m.heads, float32(1/math.Sqrt(float64(hd))))
		a = be.Linear(a, m.t(pre+"attn.proj.weight"), m.opt(pre+"attn.proj.bias"))
		be.AddScaled(tok, a, m.t(pre+"ls1.gamma"))
		// x + ls2 * mlp(norm2(x))
		h = be.LayerNorm(tok, m.t(pre+"norm2.weight"), m.t(pre+"norm2.bias"), 1e-6)
		h = be.GELU(be.Linear(h, m.t(pre+"mlp.fc1.weight"), m.opt(pre+"mlp.fc1.bias")))
		h = be.Linear(h, m.t(pre+"mlp.fc2.weight"), m.opt(pre+"mlp.fc2.bias"))
		be.AddScaled(tok, h, m.t(pre+"ls2.gamma"))
		if k < 4 && b == at[k] {
			n := be.LayerNorm(tok, m.t("pretrained.norm.weight"), m.t("pretrained.norm.bias"), 1e-6)
			out[k] = be.Rows(n, 1)
			k++
		}
	}
	return out
}

// tokensToMap turns [H W, C] tokens into a [C, H, W] map.
func (m *Model) tokensToMap(t *nn.Tensor, h, w int) *nn.Tensor {
	return m.be.Transpose(t).Reshape(t.Dim(-1), h, w)
}

// mapToTokens is tokensToMap's inverse.
func (m *Model) mapToTokens(x *nn.Tensor) *nn.Tensor {
	return m.be.Transpose(x.Reshape(x.Dim(0), x.Dim(1)*x.Dim(2)))
}

// head is the temporal DPT head: the depth at the patch grid times 14
// ([1, H, W]) and the frame's eight temporal states.
func (m *Model) head(feats [4]*nn.Tensor, ph, pw int, win [][8]kv) (*nn.Tensor, [8]kv) {
	be := m.be
	var layers [4]*nn.Tensor
	for i, f := range feats {
		x := m.tokensToMap(f, ph, pw)
		x = be.Conv2d(x, m.t(fmt.Sprintf("head.projects.%d.weight", i)), m.t(fmt.Sprintf("head.projects.%d.bias", i)), 1, 0)
		rw, rb := m.opt(fmt.Sprintf("head.resize_layers.%d.weight", i)), m.opt(fmt.Sprintf("head.resize_layers.%d.bias", i))
		switch i { // 2 is the identity
		case 0, 1:
			x = be.ConvTranspose2dK(x, rw, rb)
		case 3:
			x = be.Conv2d(x, rw, rb, 2, 1)
		}
		layers[i] = x
	}
	var states [8]kv
	var s [2]kv
	layers[2], s = m.temporal(0, layers[2], column(win, 0), column(win, 1))
	states[0], states[1] = s[0], s[1]
	layers[3], s = m.temporal(1, layers[3], column(win, 2), column(win, 3))
	states[2], states[3] = s[0], s[1]
	var rn [4]*nn.Tensor
	for i := range rn {
		rn[i] = be.Conv2d(layers[i], m.t(fmt.Sprintf("head.scratch.layer%d_rn.weight", i+1)), nil, 1, 1)
	}
	p4 := m.fusion(4, rn[3], nil, rn[2].Dim(1), rn[2].Dim(2))
	p4, s = m.temporal(2, p4, column(win, 4), column(win, 5))
	states[4], states[5] = s[0], s[1]
	p3 := m.fusion(3, p4, rn[2], rn[1].Dim(1), rn[1].Dim(2))
	p3, s = m.temporal(3, p3, column(win, 6), column(win, 7))
	states[6], states[7] = s[0], s[1]
	p2 := m.fusion(2, p3, rn[1], rn[0].Dim(1), rn[0].Dim(2))
	p1 := m.fusion(1, p2, rn[0], 2*rn[0].Dim(1), 2*rn[0].Dim(2))
	out := be.Conv2d(p1, m.t("head.scratch.output_conv1.weight"), m.opt("head.scratch.output_conv1.bias"), 1, 1)
	out = be.Bilinear(out, ph*patch, pw*patch)
	out = be.ReLU(be.Conv2d(out, m.t("head.scratch.output_conv2.0.weight"), m.opt("head.scratch.output_conv2.0.bias"), 1, 1))
	out = be.ReLU(be.Conv2d(out, m.t("head.scratch.output_conv2.2.weight"), m.opt("head.scratch.output_conv2.2.bias"), 1, 0))
	return out, states
}

// fusion is refinenet k (a FeatureFusionBlock): x plus the residual unit
// of skip (when there is one), a residual unit, resized to oh x ow, a 1x1
// convolution.
func (m *Model) fusion(k int, x, skip *nn.Tensor, oh, ow int) *nn.Tensor {
	pre := fmt.Sprintf("head.scratch.refinenet%d.", k)
	be := m.be
	out := be.Clone(x)
	if skip != nil {
		be.Add(out, m.residual(pre+"resConfUnit1.", skip))
	}
	out = m.residual(pre+"resConfUnit2.", out)
	out = be.Bilinear(out, oh, ow)
	return be.Conv2d(out, m.t(pre+"out_conv.weight"), m.opt(pre+"out_conv.bias"), 1, 0)
}

// residual is a ResidualConvUnit: x + conv2(relu(conv1(relu(x)))).
func (m *Model) residual(pre string, x *nn.Tensor) *nn.Tensor {
	be := m.be
	h := be.ReLU(be.Clone(x))
	h = be.Conv2d(h, m.t(pre+"conv1.weight"), m.opt(pre+"conv1.bias"), 1, 1)
	h = be.ReLU(h)
	h = be.Conv2d(h, m.t(pre+"conv2.weight"), m.opt(pre+"conv2.bias"), 1, 1)
	return be.Add(h, x)
}

// temporal is motion module k on one frame's map x [C, H, W]: a group
// norm, a projection to tokens, the transformer block (two temporal
// attentions over the frames' cached states, then a GEGLU feed forward),
// a projection back, plus x. It gives the map and the two attentions'
// states for the frame (their inputs).
func (m *Model) temporal(k int, x *nn.Tensor, past0, past1 []kv) (*nn.Tensor, [2]kv) {
	pre := fmt.Sprintf("head.motion_modules.%d.temporal_transformer.", k)
	be := m.be
	c, h, w := x.Dim(0), x.Dim(1), x.Dim(2)
	n := be.GroupNorm(x, 32, m.t(pre+"norm.weight"), m.t(pre+"norm.bias"), 1e-6)
	tok := be.Linear(m.mapToTokens(n), m.t(pre+"proj_in.weight"), m.opt(pre+"proj_in.bias"))
	bp := pre + "transformer_blocks.0."
	var states [2]kv
	for i, past := range [2][]kv{past0, past1} {
		ap := fmt.Sprintf("%sattention_blocks.%d.", bp, i)
		normed := be.LayerNorm(tok, m.t(fmt.Sprintf("%snorms.%d.weight", bp, i)), m.t(fmt.Sprintf("%snorms.%d.bias", bp, i)), 1e-5)
		out, cur := m.temporalAttention(ap, normed, past)
		states[i] = cur
		be.Add(tok, out)
	}
	ff := be.LayerNorm(tok, m.t(bp+"ff_norm.weight"), m.t(bp+"ff_norm.bias"), 1e-5)
	ff = be.GEGLU(be.Linear(ff, m.t(bp+"ff.net.0.proj.weight"), m.opt(bp+"ff.net.0.proj.bias")))
	ff = be.Linear(ff, m.t(bp+"ff.net.2.weight"), m.opt(bp+"ff.net.2.bias"))
	be.Add(tok, ff)
	tok = be.Linear(tok, m.t(pre+"proj_out.weight"), m.opt(pre+"proj_out.bias"))
	return be.Add(m.tokensToMap(tok, h, w), x).Reshape(c, h, w), states
}

// pe is an attention block's sinusoidal position encoding ([frames, C]):
// the checkpoint's, else made as PositionalEncoding makes it.
func (m *Model) pe(ap string, c int) *nn.Tensor {
	if p, ok := m.w[ap+"pos_encoder.pe"]; ok {
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
	m.w[ap+"pos_encoder.pe"] = p
	return p
}

// peKey is an attention block's projected position encodings' key: the
// block and the window's length.
type peKey struct {
	ap string
	t  int
}

// temporalAttention is a TemporalAttention on the current frame's states
// x [D, C]: at each position, the frame attends over the window's frames
// and itself, each with its place's position encoding; the query is the
// current frame's alone. As (x + pe) W = x W + pe W, a frame's keys and
// values are its states' projections (kept: the frame's kv, returned) plus
// the projected encoding of its place (computed once a window length, in
// memory).
func (m *Model) temporalAttention(ap string, x *nn.Tensor, past []kv) (*nn.Tensor, kv) {
	be := m.be
	c := x.Dim(1)
	t := len(past) + 1
	cur := kv{be.Linear(x, m.t(ap+"to_k.weight"), m.opt(ap+"to_k.bias")), be.Linear(x, m.t(ap+"to_v.weight"), m.opt(ap+"to_v.bias"))}
	qx := be.Linear(x, m.t(ap+"to_q.weight"), m.opt(ap+"to_q.bias"))
	pes, ok := m.pes[peKey{ap, t}]
	if !ok {
		pe := m.pe(ap, c)
		wq, wk, wv := m.w[ap+"to_q.weight"], m.w[ap+"to_k.weight"], m.w[ap+"to_v.weight"]
		pt := &nn.Tensor{Shape: []int{t, c}, Data: pe.Data[:t*c]}
		host := [3]*nn.Tensor{
			nn.Linear(&nn.Tensor{Shape: []int{1, c}, Data: pe.Data[(t-1)*c : t*c]}, wq, nil),
			nn.Linear(pt, wk, nil), nn.Linear(pt, wv, nil),
		}
		for i, h := range host {
			pes[i] = be.Upload(h)
			be.Keep(pes[i])
		}
		m.pes[peKey{ap, t}] = pes
	}
	ks, vs := make([]*nn.Tensor, t), make([]*nn.Tensor, t)
	for f, p := range past {
		ks[f], vs[f] = p.k, p.v
	}
	ks[t-1], vs[t-1] = cur.k, cur.v
	hd := c / temporalHead
	out := be.TemporalAttention(qx, pes[0], pes[1], pes[2], ks, vs, temporalHead, float32(1/math.Sqrt(float64(hd))))
	return be.Linear(out, m.t(ap+"to_out.0.weight"), m.opt(ap+"to_out.0.bias")), cur
}
