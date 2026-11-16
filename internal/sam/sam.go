// Package sam finds an object in a picture from a few hints, a box around
// it and points on it or off it: Segment Anything 2.1 (Ravi et al., 2024),
// its tiny model, from the published checkpoint as it is. The picture goes
// through a hierarchical vision transformer (Hiera) once; the hints go
// through a small two-way transformer against its features, as often as
// they change, and give the object's mask.
package sam

import (
	"errors"
	"fmt"
	"math"

	"github.com/brunoga/bdtools/internal/nn"
)

// Size is the side of the square picture the network takes.
const Size = 1024

// The tiny model's encoder: its blocks' stages, the windows each stage
// attends within, and the blocks that attend over the whole picture.
var (
	stages      = [4]int{1, 2, 7, 2}
	windowSpec  = [4]int{8, 4, 14, 7}
	globalBlock = map[int]bool{5: true, 7: true, 9: true}
)

const (
	embedDim = 96  // the encoder's first stage's width
	dModel   = 256 // the features' and the decoder's width
	grid     = Size / 16
	maskSide = 4 * grid // the masks' side (256)
	// noObject is the mask's logit where the decoder sees no object.
	noObject = -1024
)

// block is an encoder block's shape.
type block struct {
	dim, out, heads int
	window          int  // 0: global attention
	pool            bool // the queries pooled 2 x 2: the block halves the grid
	stageEnd        bool
}

// blocks is the tiny encoder's blocks, as Hiera builds them.
func blocks() []block {
	var bs []block
	dim, heads, stage := embedDim, 1, 0
	ends := map[int]bool{}
	n := 0
	for _, s := range stages {
		n += s
		ends[n-1] = true
	}
	for i := range n {
		b := block{dim: dim, out: dim, heads: heads, window: windowSpec[stage], stageEnd: ends[i]}
		if globalBlock[i] {
			b.window = 0
		}
		if ends[i-1] {
			b.out, heads = 2*dim, 2*heads
			b.heads = heads
			b.pool = true
			stage++
		}
		dim = b.out
		bs = append(bs, b)
	}
	return bs
}

// Model is a loaded SAM 2.1.
type Model struct {
	w  map[string]*nn.Tensor // in memory
	be nn.Backend
	dw map[string]*nn.Tensor // the weights on the backend

	pos   *nn.Tensor // the encoder's position embedding [256 256, 96]
	dense *nn.Tensor // the decoder's position encoding of the features [64 64, 256]
}

// Load reads a checkpoint (sam2.1_hiera_tiny.pt).
func Load(file string) (*Model, error) {
	w, err := nn.LoadPTH(file)
	if err != nil {
		return nil, err
	}
	return New(w)
}

// New is a model of loaded weights.
func New(w map[string]*nn.Tensor) (*Model, error) {
	for _, k := range []string{"image_encoder.trunk.pos_embed", "image_encoder.trunk.blocks.11.attn.qkv.weight",
		"sam_mask_decoder.conv_s0.weight", "no_mem_embed", "sam_prompt_encoder.pe_layer.positional_encoding_gaussian_matrix"} {
		if _, ok := w[k]; !ok {
			return nil, fmt.Errorf("sam: the checkpoint has no %s (not SAM 2.1 tiny)", k)
		}
	}
	if w["image_encoder.trunk.pos_embed"].Dim(1) != embedDim || w["image_encoder.trunk.blocks.12.norm1.weight"] != nil {
		return nil, errors.New("sam: not SAM 2.1's tiny model")
	}
	m := &Model{w: w}
	// the features' level the decoder takes gets no_mem_embed (the image
	// predictor adds it to them) and the decoder's no_mask_embed (no mask
	// is given as a hint): both into its lateral convolution's bias
	b := w["image_encoder.neck.convs.1.conv.bias"].Clone()
	for i := range b.Data {
		b.Data[i] += w["no_mem_embed"].Data[i] + w["sam_prompt_encoder.no_mask_embed.weight"].Data[i]
	}
	w["neck1.bias"] = b
	m.pos = trunkPos(w["image_encoder.trunk.pos_embed"], w["image_encoder.trunk.pos_embed_window"], Size/4)
	m.dense = densePE(w["sam_prompt_encoder.pe_layer.positional_encoding_gaussian_matrix"], grid)
	m.Use(nn.CPU)
	return m, nil
}

// Use runs the model on a backend.
func (m *Model) Use(be nn.Backend) {
	if m.be != nil {
		for _, t := range m.dw {
			m.be.Free(t)
		}
	}
	m.be = be
	m.dw = map[string]*nn.Tensor{}
}

// Backend is the backend the model runs on.
func (m *Model) Backend() nn.Backend { return m.be }

// t is a weight on the backend.
func (m *Model) t(name string) *nn.Tensor {
	if d, ok := m.dw[name]; ok {
		return d
	}
	w, ok := m.w[name]
	if !ok {
		panic("sam: no weight " + name)
	}
	d := m.be.Upload(w)
	m.be.Keep(d)
	m.dw[name] = d
	return d
}

// keep is a tensor of the model's own (not the checkpoint's) on the
// backend, by name.
func (m *Model) keep(name string, x *nn.Tensor) *nn.Tensor {
	if _, ok := m.w[name]; !ok {
		m.w[name] = x
	}
	return m.t(name)
}

// linear is nn.Linear by a layer's name.
func (m *Model) linear(x *nn.Tensor, name string) *nn.Tensor {
	return m.be.Linear(x, m.t(name+".weight"), m.t(name+".bias"))
}

// trunkPos is the encoder's position embedding for a side x side grid of
// patches: the background embedding (bg [1, C, 7, 7]) resized bicubically,
// plus the window's (win [1, C, 8, 8]) tiled over it, as rows [side side,
// C].
func trunkPos(bg, win *nn.Tensor, side int) *nn.Tensor {
	c := bg.Dim(1)
	up := bicubic(bg.Data, c, bg.Dim(2), bg.Dim(3), side, side)
	ws := win.Dim(2)
	out := nn.New(side*side, c)
	for y := range side {
		for x := range side {
			row := out.Data[(y*side+x)*c : (y*side+x+1)*c]
			for ch := range c {
				row[ch] = up[(ch*side+y)*side+x] + win.Data[(ch*ws+y%ws)*ws+x%ws]
			}
		}
	}
	return out
}

// bicubic is F.interpolate's bicubic resize (align_corners false, the
// cubic's A -0.75) of c planes h x w to oh x ow.
func bicubic(x []float32, c, h, w, oh, ow int) []float32 {
	out := make([]float32, c*oh*ow)
	cubic := func(t float32) [4]float32 {
		const a = -0.75
		f1 := func(x float32) float32 { return ((a+2)*x-(a+3))*x*x + 1 }
		f2 := func(x float32) float32 { return ((a*x-5*a)*x+8*a)*x - 4*a }
		return [4]float32{f2(t + 1), f1(t), f1(1 - t), f2(2 - t)}
	}
	taps := func(in, n int) ([]int, [][4]float32) {
		scale := float32(in) / float32(n)
		idx, ws := make([]int, n), make([][4]float32, n)
		for o := range n {
			src := scale*(float32(o)+0.5) - 0.5
			f := float32(math.Floor(float64(src)))
			idx[o], ws[o] = int(f), cubic(src-f)
		}
		return idx, ws
	}
	clamp := func(i, n int) int { return min(max(i, 0), n-1) }
	iy, wy := taps(h, oh)
	ix, wx := taps(w, ow)
	for ch := range c {
		p := x[ch*h*w : (ch+1)*h*w]
		for oy := range oh {
			for ox := range ow {
				var s float32
				for j := range 4 {
					yy := clamp(iy[oy]-1+j, h)
					var r float32
					for i := range 4 {
						r += p[yy*w+clamp(ix[ox]-1+i, w)] * wx[ox][i]
					}
					s += r * wy[oy][j]
				}
				out[(ch*oh+oy)*ow+ox] = s
			}
		}
	}
	return out
}

// pe is the prompt encoder's random Fourier features of a point (x, y in
// 0..1): [sin, cos] of 2π (2p-1) g, g [2, C/2].
func pe(g *nn.Tensor, x, y float32, out []float32) {
	n := g.Dim(1)
	cx, cy := 2*x-1, 2*y-1
	for i := range n {
		v := 2 * math.Pi * float64(cx*g.Data[i]+cy*g.Data[n+i])
		out[i] = float32(math.Sin(v))
		out[n+i] = float32(math.Cos(v))
	}
}

// densePE is the prompt encoder's position encoding of a side x side
// grid of features, as rows [side side, C].
func densePE(g *nn.Tensor, side int) *nn.Tensor {
	c := 2 * g.Dim(1)
	out := nn.New(side*side, c)
	for y := range side {
		for x := range side {
			i := y*side + x
			pe(g, (float32(x)+0.5)/float32(side), (float32(y)+0.5)/float32(side), out.Data[i*c:(i+1)*c])
		}
	}
	return out
}
