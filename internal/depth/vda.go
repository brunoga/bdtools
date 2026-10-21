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

// intermediate are the encoder blocks whose outputs the head reads.
var intermediate = [4]int{2, 5, 8, 11}

// Model is a loaded Video Depth Anything and its stream's state.
type Model struct {
	w     map[string]*nn.Tensor
	heads int // the encoder's attention heads
	dim   int // its width

	pos map[[2]int]*nn.Tensor // the position embedding, by patch grid

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
	return &Model{w: w, heads: dim / hd, dim: dim, pos: map[[2]int]*nn.Tensor{}}, nil
}

func (m *Model) t(name string) *nn.Tensor {
	v, ok := m.w[name]
	if !ok {
		panic("depth: no weight " + name)
	}
	return v
}

// opt is a weight that may be missing (a bias): nil then.
func (m *Model) opt(name string) *nn.Tensor { return m.w[name] }

// Reset forgets the frames before: the next frame starts a new stream (a
// new shot, say).
func (m *Model) Reset() { m.cache, m.n = nil, 0 }

// Frame gives the depth of the next frame of the stream: x is the frame
// as the network takes it ([3, H, W], H and W multiples of 14, the colours
// normalised by ImageNet's mean and deviation); the depth is [H, W], a
// relative inverse depth (larger nearer).
func (m *Model) Frame(x *nn.Tensor) *nn.Tensor {
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
	if m.n == 0 {
		m.cache = make([][8]kv, inferLen)
		for i := range m.cache {
			m.cache[i] = states
		}
	} else {
		m.cache = append(m.cache, states)
	}
	if m.n+inferLen > cacheLen {
		m.cache = append(m.cache[:1], m.cache[2:]...)
	}
	m.n++
	d = nn.Bilinear(d, h, w)
	return nn.ReLU(d).Reshape(h, w)
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
	pe := m.t("pretrained.pos_embed")
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
	m.pos[[2]int{ph, pw}] = out
	return out
}

// encode runs DINOv2: the patch tokens of the intermediate blocks'
// outputs, normed ([ph pw, D] each).
func (m *Model) encode(x *nn.Tensor, ph, pw int) [4]*nn.Tensor {
	d := m.dim
	p := nn.Conv2d(x, m.t("pretrained.patch_embed.proj.weight"), m.t("pretrained.patch_embed.proj.bias"), patch, 0)
	tok := nn.New(1+ph*pw, d)
	copy(tok.Data[:d], m.t("pretrained.cls_token").Data)
	for i := range ph * pw {
		for c := range d {
			tok.Data[(1+i)*d+c] = p.Data[c*ph*pw+i]
		}
	}
	nn.Add(tok, m.posEmbed(ph, pw))
	var out [4]*nn.Tensor
	k := 0
	hd := d / m.heads
	for b := range 12 {
		pre := fmt.Sprintf("pretrained.blocks.%d.", b)
		// x + ls1 * attn(norm1(x))
		h := nn.LayerNorm(tok, m.t(pre+"norm1.weight"), m.t(pre+"norm1.bias"), 1e-6)
		qkv := nn.Linear(h, m.t(pre+"attn.qkv.weight"), m.opt(pre+"attn.qkv.bias"))
		q, kk, v := split3(qkv, d)
		a := nn.Attention(q, kk, v, m.heads, float32(1/math.Sqrt(float64(hd))))
		a = nn.Linear(a, m.t(pre+"attn.proj.weight"), m.opt(pre+"attn.proj.bias"))
		addScaled(tok, a, m.t(pre+"ls1.gamma"))
		// x + ls2 * mlp(norm2(x))
		h = nn.LayerNorm(tok, m.t(pre+"norm2.weight"), m.t(pre+"norm2.bias"), 1e-6)
		h = nn.GELU(nn.Linear(h, m.t(pre+"mlp.fc1.weight"), m.opt(pre+"mlp.fc1.bias")))
		h = nn.Linear(h, m.t(pre+"mlp.fc2.weight"), m.opt(pre+"mlp.fc2.bias"))
		addScaled(tok, h, m.t(pre+"ls2.gamma"))
		if k < 4 && b == intermediate[k] {
			n := nn.LayerNorm(tok, m.t("pretrained.norm.weight"), m.t("pretrained.norm.bias"), 1e-6)
			out[k] = &nn.Tensor{Shape: []int{ph * pw, d}, Data: n.Data[d:]}
			k++
		}
	}
	return out
}

// split3 splits [N, 3D] rows into their three [N, D] thirds.
func split3(x *nn.Tensor, d int) (*nn.Tensor, *nn.Tensor, *nn.Tensor) {
	n := x.Dim(0)
	q, k, v := nn.New(n, d), nn.New(n, d), nn.New(n, d)
	for i := range n {
		row := x.Data[i*3*d:]
		copy(q.Data[i*d:(i+1)*d], row[:d])
		copy(k.Data[i*d:(i+1)*d], row[d:2*d])
		copy(v.Data[i*d:(i+1)*d], row[2*d:3*d])
	}
	return q, k, v
}

// addScaled adds y times a per-channel scale g to x ([N, D]), in place.
func addScaled(x, y, g *nn.Tensor) {
	d := x.Dim(-1)
	for i, v := range y.Data {
		x.Data[i] += v * g.Data[i%d]
	}
}

// tokensToMap turns [H W, C] tokens into a [C, H, W] map.
func tokensToMap(t *nn.Tensor, h, w int) *nn.Tensor {
	c := t.Dim(-1)
	out := nn.New(c, h, w)
	for i := range h * w {
		for ch := range c {
			out.Data[ch*h*w+i] = t.Data[i*c+ch]
		}
	}
	return out
}

// mapToTokens is tokensToMap's inverse.
func mapToTokens(x *nn.Tensor) *nn.Tensor {
	c, n := x.Dim(0), x.Dim(1)*x.Dim(2)
	out := nn.New(n, c)
	for ch := range c {
		for i := range n {
			out.Data[i*c+ch] = x.Data[ch*n+i]
		}
	}
	return out
}

// head is the temporal DPT head: the depth at the patch grid times 14
// ([1, H, W]) and the frame's eight temporal states.
func (m *Model) head(feats [4]*nn.Tensor, ph, pw int, win [][8]kv) (*nn.Tensor, [8]kv) {
	var layers [4]*nn.Tensor
	for i, f := range feats {
		x := tokensToMap(f, ph, pw)
		x = nn.Conv2d(x, m.t(fmt.Sprintf("head.projects.%d.weight", i)), m.t(fmt.Sprintf("head.projects.%d.bias", i)), 1, 0)
		rw, rb := m.opt(fmt.Sprintf("head.resize_layers.%d.weight", i)), m.opt(fmt.Sprintf("head.resize_layers.%d.bias", i))
		switch i { // 2 is the identity
		case 0, 1:
			x = nn.ConvTranspose2dK(x, rw, rb)
		case 3:
			x = nn.Conv2d(x, rw, rb, 2, 1)
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
		rn[i] = nn.Conv2d(layers[i], m.t(fmt.Sprintf("head.scratch.layer%d_rn.weight", i+1)), nil, 1, 1)
	}
	p4 := m.fusion(4, rn[3], nil, rn[2].Dim(1), rn[2].Dim(2))
	p4, s = m.temporal(2, p4, column(win, 4), column(win, 5))
	states[4], states[5] = s[0], s[1]
	p3 := m.fusion(3, p4, rn[2], rn[1].Dim(1), rn[1].Dim(2))
	p3, s = m.temporal(3, p3, column(win, 6), column(win, 7))
	states[6], states[7] = s[0], s[1]
	p2 := m.fusion(2, p3, rn[1], rn[0].Dim(1), rn[0].Dim(2))
	p1 := m.fusion(1, p2, rn[0], 2*rn[0].Dim(1), 2*rn[0].Dim(2))
	out := nn.Conv2d(p1, m.t("head.scratch.output_conv1.weight"), m.opt("head.scratch.output_conv1.bias"), 1, 1)
	out = nn.Bilinear(out, ph*patch, pw*patch)
	out = nn.ReLU(nn.Conv2d(out, m.t("head.scratch.output_conv2.0.weight"), m.opt("head.scratch.output_conv2.0.bias"), 1, 1))
	out = nn.ReLU(nn.Conv2d(out, m.t("head.scratch.output_conv2.2.weight"), m.opt("head.scratch.output_conv2.2.bias"), 1, 0))
	return out, states
}

// fusion is refinenet k (a FeatureFusionBlock): x plus the residual unit
// of skip (when there is one), a residual unit, resized to oh x ow, a 1x1
// convolution.
func (m *Model) fusion(k int, x, skip *nn.Tensor, oh, ow int) *nn.Tensor {
	pre := fmt.Sprintf("head.scratch.refinenet%d.", k)
	out := x.Clone()
	if skip != nil {
		nn.Add(out, m.residual(pre+"resConfUnit1.", skip))
	}
	out = m.residual(pre+"resConfUnit2.", out)
	out = nn.Bilinear(out, oh, ow)
	return nn.Conv2d(out, m.t(pre+"out_conv.weight"), m.opt(pre+"out_conv.bias"), 1, 0)
}

// residual is a ResidualConvUnit: x + conv2(relu(conv1(relu(x)))).
func (m *Model) residual(pre string, x *nn.Tensor) *nn.Tensor {
	h := nn.ReLU(x.Clone())
	h = nn.Conv2d(h, m.t(pre+"conv1.weight"), m.opt(pre+"conv1.bias"), 1, 1)
	h = nn.ReLU(h)
	h = nn.Conv2d(h, m.t(pre+"conv2.weight"), m.opt(pre+"conv2.bias"), 1, 1)
	return nn.Add(h, x)
}

// temporal is motion module k on one frame's map x [C, H, W]: a group
// norm, a projection to tokens, the transformer block (two temporal
// attentions over the frames' cached states, then a GEGLU feed forward),
// a projection back, plus x. It gives the map and the two attentions'
// states for the frame (their inputs).
func (m *Model) temporal(k int, x *nn.Tensor, past0, past1 []kv) (*nn.Tensor, [2]kv) {
	pre := fmt.Sprintf("head.motion_modules.%d.temporal_transformer.", k)
	c, h, w := x.Dim(0), x.Dim(1), x.Dim(2)
	n := nn.GroupNorm(x, 32, m.t(pre+"norm.weight"), m.t(pre+"norm.bias"), 1e-6)
	tok := nn.Linear(mapToTokens(n), m.t(pre+"proj_in.weight"), m.opt(pre+"proj_in.bias"))
	bp := pre + "transformer_blocks.0."
	var states [2]kv
	for i, past := range [2][]kv{past0, past1} {
		ap := fmt.Sprintf("%sattention_blocks.%d.", bp, i)
		normed := nn.LayerNorm(tok, m.t(fmt.Sprintf("%snorms.%d.weight", bp, i)), m.t(fmt.Sprintf("%snorms.%d.bias", bp, i)), 1e-5)
		out, cur := m.temporalAttention(ap, normed, past)
		states[i] = cur
		nn.Add(tok, out)
	}
	ff := nn.LayerNorm(tok, m.t(bp+"ff_norm.weight"), m.t(bp+"ff_norm.bias"), 1e-5)
	ff = geglu(nn.Linear(ff, m.t(bp+"ff.net.0.proj.weight"), m.opt(bp+"ff.net.0.proj.bias")))
	ff = nn.Linear(ff, m.t(bp+"ff.net.2.weight"), m.opt(bp+"ff.net.2.bias"))
	nn.Add(tok, ff)
	tok = nn.Linear(tok, m.t(pre+"proj_out.weight"), m.opt(pre+"proj_out.bias"))
	return nn.Add(tokensToMap(tok, h, w), x).Reshape(c, h, w), states
}

// geglu is a GEGLU's activation: the first half of each row times the
// GELU of its second.
func geglu(x *nn.Tensor) *nn.Tensor {
	n, d := x.Dim(0), x.Dim(1)/2
	out := nn.New(n, d)
	gate := nn.New(n, d)
	for i := range n {
		copy(out.Data[i*d:(i+1)*d], x.Data[i*2*d:i*2*d+d])
		copy(gate.Data[i*d:(i+1)*d], x.Data[i*2*d+d:(i+1)*2*d])
	}
	nn.GELU(gate)
	for i, g := range gate.Data {
		out.Data[i] *= g
	}
	return out
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

// temporalAttention is a TemporalAttention on the current frame's states
// x [D, C]: at each position, the frame attends over the window's frames
// and itself, each with its place's position encoding; the query is the
// current frame's alone. As (x + pe) W = x W + pe W, a frame's keys and
// values are its states' projections (kept: the frame's kv, returned) plus
// the projected encoding of its place.
func (m *Model) temporalAttention(ap string, x *nn.Tensor, past []kv) (*nn.Tensor, kv) {
	d, c := x.Dim(0), x.Dim(1)
	t := len(past) + 1
	pe := m.pe(ap, c)
	wq, wk, wv := m.t(ap+"to_q.weight"), m.t(ap+"to_k.weight"), m.t(ap+"to_v.weight")
	cur := kv{nn.Linear(x, wk, m.opt(ap+"to_k.bias")), nn.Linear(x, wv, m.opt(ap+"to_v.bias"))}
	qx := nn.Linear(x, wq, m.opt(ap+"to_q.bias"))
	pt := &nn.Tensor{Shape: []int{t, c}, Data: pe.Data[:t*c]}
	peK, peV := nn.Linear(pt, wk, nil), nn.Linear(pt, wv, nil)
	peQ := nn.Linear(&nn.Tensor{Shape: []int{1, c}, Data: pe.Data[(t-1)*c : t*c]}, wq, nil)
	hd := c / temporalHead
	scale := float32(1 / math.Sqrt(float64(hd)))
	out := nn.New(d, c)
	const chunk = 64
	nn.Parallel((d+chunk-1)/chunk, func(job int) {
		q := make([]float32, c)
		scores := make([]float32, t)
		key := func(f, p int) []float32 {
			if f < t-1 {
				return past[f].k.Data[p*c : (p+1)*c]
			}
			return cur.k.Data[p*c : (p+1)*c]
		}
		val := func(f, p int) []float32 {
			if f < t-1 {
				return past[f].v.Data[p*c : (p+1)*c]
			}
			return cur.v.Data[p*c : (p+1)*c]
		}
		for p := job * chunk; p < min(job*chunk+chunk, d); p++ {
			for i := range q {
				q[i] = qx.Data[p*c+i] + peQ.Data[i]
			}
			for h := range temporalHead {
				lo := h * hd
				for f := range t {
					kf, pk := key(f, p), peK.Data[f*c:]
					var s float32
					for i := lo; i < lo+hd; i++ {
						s += q[i] * (kf[i] + pk[i])
					}
					scores[f] = s * scale
				}
				softmax(scores)
				oh := out.Data[p*c+lo : p*c+lo+hd]
				for f, pr := range scores {
					vf, pv := val(f, p), peV.Data[f*c:]
					for i := range oh {
						oh[i] += pr * (vf[lo+i] + pv[lo+i])
					}
				}
			}
		}
	})
	return nn.Linear(out, m.t(ap+"to_out.0.weight"), m.opt(ap+"to_out.0.bias")), cur
}

func softmax(r []float32) {
	mx := r[0]
	for _, v := range r {
		mx = max(mx, v)
	}
	var s float64
	for i, v := range r {
		e := math.Exp(float64(v - mx))
		r[i] = float32(e)
		s += e
	}
	for i := range r {
		r[i] = float32(float64(r[i]) / s)
	}
}
