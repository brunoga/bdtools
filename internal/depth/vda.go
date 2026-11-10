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
	cache [][8]KV
	n     int // the frames done

	// Tap, when set, is given each frame's head inputs to its trainable
	// tail (TailInput), on the backend: for learning the tail.
	Tap func(TailInput)
}

// TailInput is what the head's tail (Tail) takes: the encoder's first two
// features as maps ([C, ph, pw]: the projections' inputs), and the
// fusion's third stage after its temporal module ([F, H, W]), with the
// network's input size (the depth's). Tap is also given the frame as the
// network took it (Input, [3, H, W]; Head alone leaves it nil).
type TailInput struct {
	Feat0, Feat1, P3 *nn.Tensor
	H, W             int
	Input            *nn.Tensor
	Fresh            bool // the stream's first frame (after Reset): the frames before are not its window's
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
	if _, ok := w["pretrained.cls_token"]; !ok {
		return nil, errors.New("depth: not a Video Depth Anything checkpoint (no pretrained.cls_token)")
	}
	for _, k := range []string{"pretrained.pos_embed", "pretrained.patch_embed.proj.weight", "head.scratch.output_conv2.2.weight"} {
		if _, ok := w[k]; !ok {
			return nil, fmt.Errorf("depth: the checkpoint has no %s", k)
		}
	}
	blocks, heads, dim, err := arch(w)
	if err != nil {
		return nil, err
	}
	m := &Model{w: w, heads: heads, dim: dim, blocks: blocks}
	m.Use(nn.CPU)
	return m, nil
}

// arch is the encoder's depth, heads and width from its weights.
func arch(w map[string]*nn.Tensor) (blocks, heads, dim int, err error) {
	cls := w["pretrained.cls_token"]
	if cls == nil {
		return 0, 0, 0, fmt.Errorf("depth: no class token")
	}
	dim = cls.Dim(-1)
	hd := 64
	if dim < 64 {
		hd = 12
	}
	if dim%hd != 0 {
		return 0, 0, 0, fmt.Errorf("depth: an encoder %d wide is not in heads of %d", dim, hd)
	}
	for w[fmt.Sprintf("pretrained.blocks.%d.norm1.weight", blocks)] != nil {
		blocks++
	}
	if _, ok := intermediate[blocks]; !ok {
		return 0, 0, 0, fmt.Errorf("depth: an encoder of %d blocks (Small's 12 and Large's 24 are known)", blocks)
	}
	return blocks, dim / hd, dim, nil
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
			for _, t := range [2]*nn.Tensor{st.K, st.V} {
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
	var win [][8]KV
	if m.n > 0 {
		win = window(m.cache)
	}
	tap := m.Tap
	if tap != nil {
		tap = func(t TailInput) {
			t.Input, t.Fresh = x, m.n == 0
			m.Tap(t)
		}
	}
	d, states := Head(m.be, m, feats, ph, pw, win, tap)
	for _, s := range states {
		m.be.Keep(s.K)
		m.be.Keep(s.V)
	}
	if m.n == 0 {
		m.cache = make([][8]KV, inferLen)
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
func (m *Model) drop(s [8]KV) {
	for _, c := range m.cache {
		if c == s {
			return // the first frame's, repeated at the start
		}
	}
	for _, st := range s {
		m.be.Free(st.K)
		m.be.Free(st.V)
	}
}

// posEmbed is the position embedding for a ph x pw grid: the class
// token's, then the patches', bicubically resized from the 37x37 grid it
// was trained on (DINOv2's interpolate_pos_encoding, offset 0.1).
func (m *Model) posEmbed(ph, pw int) *nn.Tensor {
	if p, ok := m.pos[[2]int{ph, pw}]; ok {
		return p
	}
	d2 := m.be.Upload(positionEmbedding(m.w["pretrained.pos_embed"], ph, pw))
	m.be.Keep(d2)
	m.pos[[2]int{ph, pw}] = d2
	return d2
}

// encode runs DINOv2 (Encoder) on the backend.
func (m *Model) encode(x *nn.Tensor, _, _ int) [4]*nn.Tensor {
	return Encoder(m.be, m, x)
}

// Pos is the encoder's position embedding for a grid of patches, the
// backend's (Params).
func (m *Model) Pos(ph, pw int) *nn.Tensor { return m.posEmbed(ph, pw) }

// Arch is the encoder's depth, heads and width (Params).
func (m *Model) Arch() (blocks, heads, dim int) { return m.blocks, m.heads, m.dim }

// Encode runs the encoder on a frame x ([3, H, W], the backend's): its
// features for the head ([ph pw, C] each, the backend's; see Head).
func (m *Model) Encode(x *nn.Tensor) [4]*nn.Tensor {
	return m.encode(x, x.Dim(1)/patch, x.Dim(2)/patch)
}

// Weights are the model's, in memory, by their checkpoint names.
func (m *Model) Weights() map[string]*nn.Tensor { return m.w }

// T is a weight on the backend (Params).
func (m *Model) T(name string) *nn.Tensor { return m.t(name) }

// Opt is a weight that may be absent (Params).
func (m *Model) Opt(name string) *nn.Tensor { return m.opt(name) }

// peKey is an attention block's projected position encodings' key: the
// block and the window's length.
type peKey struct {
	ap string
	t  int
}

// PE is a temporal attention block's projected position encodings for a
// window of t frames (Params), computed once a window length and kept on
// the backend.
func (m *Model) PE(block string, c, t int) [3]*nn.Tensor {
	if pes, ok := m.pes[peKey{block, t}]; ok {
		return pes
	}
	pe := PositionEncoding(m.w, block, c)
	wq, wk, wv := m.w[block+"to_q.weight"], m.w[block+"to_k.weight"], m.w[block+"to_v.weight"]
	pt := &nn.Tensor{Shape: []int{t, c}, Data: pe.Data[:t*c]}
	host := [3]*nn.Tensor{
		nn.Linear(&nn.Tensor{Shape: []int{1, c}, Data: pe.Data[(t-1)*c : t*c]}, wq, nil),
		nn.Linear(pt, wk, nil), nn.Linear(pt, wv, nil),
	}
	var pes [3]*nn.Tensor
	for i, h := range host {
		pes[i] = m.be.Upload(h)
		m.be.Keep(pes[i])
	}
	m.pes[peKey{block, t}] = pes
	return pes
}
