package sam

import (
	"fmt"
	"math"
	"sync"

	"github.com/brunoga/bdtools/internal/nn"
)

// Features are a picture's features, on the model's backend: the coarse
// level the decoder and the memory take ([256, 64, 64]), and the two finer
// levels, already projected by the decoder's convolutions ([32, 256, 256]
// and [64, 128, 128]).
type Features struct {
	Vision, High0, High1 *nn.Tensor
	// Stages are the encoder's four stages' outputs ([C, H, W]), for the
	// tests.
	Stages [4]*nn.Tensor
}

// Encode is a picture's features: x is the picture as the network takes
// it ([3, 1024, 1024], Input's), on the backend.
func (m *Model) Encode(x *nn.Tensor) *Features {
	be := m.be
	side := Size / 4
	p := be.Conv2d(x, m.t("image_encoder.trunk.patch_embed.proj.weight"), m.t("image_encoder.trunk.patch_embed.proj.bias"), 4, 3)
	t := be.Add(be.Transpose(p.Reshape(embedDim, side*side)), m.keep("trunk.pos", func() *nn.Tensor { return m.pos }))
	var f Features
	stage := 0
	for i, b := range blocks() {
		t, side = m.block(t, i, b, side)
		if b.stageEnd {
			f.Stages[stage] = be.Transpose(t).Reshape(b.out, side, side)
			stage++
		}
	}
	n := "image_encoder.neck.convs.%d.conv."
	lat := func(level int, x *nn.Tensor) *nn.Tensor {
		c := fmt.Sprintf(n, 3-level)
		return be.Conv2d(x, m.t(c+"weight"), m.t(c+"bias"), 1, 0)
	}
	// the coarsest level only goes into the next one down (the decoder
	// does not take it)
	f.Vision = be.Add(lat(2, f.Stages[2]), be.Nearest(lat(3, f.Stages[3]), 2))
	f.High0 = be.Conv2d(lat(0, f.Stages[0]), m.t("sam_mask_decoder.conv_s0.weight"), m.t("sam_mask_decoder.conv_s0.bias"), 1, 0)
	f.High1 = be.Conv2d(lat(1, f.Stages[1]), m.t("sam_mask_decoder.conv_s1.weight"), m.t("sam_mask_decoder.conv_s1.bias"), 1, 0)
	return &f
}

// block is the encoder's block i on the tokens t of a side x side grid
// (rows [side side, dim]): its output, and its grid's side.
func (m *Model) block(t *nn.Tensor, i int, b block, side int) (*nn.Tensor, int) {
	be := m.be
	name := fmt.Sprintf("image_encoder.trunk.blocks.%d.", i)
	x := be.LayerNorm(t, m.t(name+"norm1.weight"), m.t(name+"norm1.bias"), 1e-6)
	short := t
	if b.dim != b.out {
		short = be.WindowPool(m.linear(x, name+"proj"), side)
	}
	ws, nb := side, 1
	if b.window > 0 {
		ws = b.window
		_, _, nb = nn.WindowGrid(side, side, ws)
		x = be.WindowPartition(x, side, side, ws)
	}
	q, k, v := be.Split3(m.linear(x, name+"attn.qkv"))
	out := side
	if b.pool {
		q = be.WindowPool(q, ws)
		ws /= 2
		out = side / 2
	}
	hd := b.out / b.heads
	a := m.linear(be.AttentionBatch(q, k, v, nb, b.heads, float32(1/math.Sqrt(float64(hd)))), name+"attn.proj")
	if b.window > 0 {
		a = be.WindowUnpartition(a, out, out, ws)
	}
	t = be.Add(short, a) // t itself when the block keeps its width: not read again
	h := m.linear(be.LayerNorm(t, m.t(name+"norm2.weight"), m.t(name+"norm2.bias"), 1e-6), name+"mlp.layers.0")
	h = m.linear(be.GELU(h), name+"mlp.layers.1")
	return be.Add(t, h), out
}

// Picture is a picture as the network takes it, before its
// normalisation: its RGB planes, 1024 x 1024 bytes each, the picture
// stretched to the square as SAM 2's predictor does. It is a quarter of
// Input's size, for keeping many.
type Picture []byte

// NewPicture is a picture's planes (8-bit 4:2:0, BT.709 limited range)
// as a Picture, resized bilinearly.
func NewPicture(y, cb, cr []byte, w, h, strideY, strideC int) Picture {
	p := make(Picture, 3*Size*Size)
	var wg sync.WaitGroup
	const band = 64
	for b0 := 0; b0 < Size; b0 += band {
		wg.Go(func() {
			for oy := b0; oy < b0+band; oy++ {
				fy := max((float32(oy)+0.5)*float32(h)/Size-0.5, 0)
				y0 := min(int(fy), h-1)
				y1 := min(y0+1, h-1)
				ly := fy - float32(y0)
				for ox := range Size {
					fx := max((float32(ox)+0.5)*float32(w)/Size-0.5, 0)
					x0 := min(int(fx), w-1)
					x1 := min(x0+1, w-1)
					lx := fx - float32(x0)
					bil := func(pl []byte, s, div int) float32 {
						at := func(yy, xx int) float32 { return float32(pl[yy/div*s+xx/div]) }
						return (at(y0, x0)*(1-lx)+at(y0, x1)*lx)*(1-ly) + (at(y1, x0)*(1-lx)+at(y1, x1)*lx)*ly
					}
					Y := (bil(y, strideY, 1) - 16) * (255.0 / 219)
					U := (bil(cb, strideC, 2) - 128) * (255.0 / 224)
					V := (bil(cr, strideC, 2) - 128) * (255.0 / 224)
					rgb := [3]float32{Y + 1.5748*V, Y - 0.1873*U - 0.4681*V, Y + 1.8556*U}
					for c, v := range rgb {
						p[(c*Size+oy)*Size+ox] = byte(min(max(v+0.5, 0), 255))
					}
				}
			}
		})
	}
	wg.Wait()
	return p
}

// Tensor is the picture as the network takes it: [3, 1024, 1024],
// normalised by ImageNet's mean and deviation.
func (p Picture) Tensor() *nn.Tensor {
	x := nn.New(3, Size, Size)
	mean := [3]float32{0.485, 0.456, 0.406}
	sd := [3]float32{0.229, 0.224, 0.225}
	bands(len(p), func(lo, hi int) {
		for i := lo; i < hi; i++ {
			c := i / (Size * Size)
			x.Data[i] = (float32(p[i])/255 - mean[c]) / sd[c]
		}
	})
	return x
}

// Input is a picture's planes as the network takes them (NewPicture's
// Tensor).
func Input(y, cb, cr []byte, w, h, strideY, strideC int) *nn.Tensor {
	return NewPicture(y, cb, cr, w, h, strideY, strideC).Tensor()
}
