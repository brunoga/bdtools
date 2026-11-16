package sam

import (
	"fmt"
	"math"

	"github.com/brunoga/bdtools/internal/nn"
)

// Point is a hint on the picture, in the network's 1024 x 1024 frame: on
// the object (Positive) or off it.
type Point struct {
	X, Y     float32
	Positive bool
}

// Box is a box around the object, in the network's frame.
type Box struct{ X0, Y0, X1, Y1 float32 }

// Mask is a mask the decoder gives: its logits over the picture at 256 x
// 256 (in the network's square frame; over 0 is the object), and how good
// the decoder takes it to be (its IoU estimate).
type Mask struct {
	Logits []float32
	IoU    float32
}

// MaskSide is a Mask's side.
const MaskSide = maskSide

// Decode is the object the hints show in a picture whose features are f:
// with multi, the decoder's three masks for an ambiguous hint (a part, the
// whole, and between), else its one. Object is the decoder's logit that
// there is an object at all (at most 0: none, the masks empty).
func (m *Model) Decode(f *Features, box *Box, points []Point, multi bool) (masks []Mask, object float32, err error) {
	// a picture alone gets no_mem_embed in place of a memory
	d, err := m.decode(m.be.Add(m.be.Clone(f.Vision), m.constMap("no_mem_embed")), f, m.tokens(box, points))
	if err != nil {
		return nil, 0, err
	}
	from, to := 0, 1
	if multi {
		from, to = 1, 4
	}
	for i := from; i < to; i++ {
		masks = append(masks, Mask{Logits: d.logits[i], IoU: d.iou[i]})
	}
	return masks, d.object, nil
}

// decoded is all the decoder gives: its four masks' logits (the single
// mask's, then the three of an ambiguous hint), their IoU estimates and
// output tokens, and the object score's logit.
type decoded struct {
	logits [4][]float32
	iou    [4]float32
	tokens [4][]float32
	object float32
}

// decode runs the decoder on the pixel features pix ([256, 64, 64], the
// picture's own with no_mem_embed, or with its memory) and the finer
// levels of f, for the tokens (tokens'). Where it sees no object the masks
// are noObject.
func (m *Model) decode(pix *nn.Tensor, f *Features, tokens *nn.Tensor) (*decoded, error) {
	be := m.be
	tpe := be.Upload(tokens)
	queries := be.Clone(tpe)
	// no mask is given as a hint: its embedding goes on every feature
	keys := be.Transpose(be.Add(be.Clone(pix), m.constMap("sam_prompt_encoder.no_mask_embed.weight")).Reshape(dModel, grid*grid))
	kpe := m.keep("dense.pe", func() *nn.Tensor { return m.dense })
	plus := func(a, b *nn.Tensor) *nn.Tensor { return be.Add(be.Clone(a), b) }
	ln := func(x *nn.Tensor, name string) *nn.Tensor {
		return be.LayerNorm(x, m.t(name+".weight"), m.t(name+".bias"), 1e-5)
	}
	const tr = "sam_mask_decoder.transformer."
	for i := range 2 {
		l := fmt.Sprintf(tr+"layers.%d.", i)
		if i == 0 {
			queries = m.attn(queries, queries, queries, l+"self_attn")
		} else {
			q := plus(queries, tpe)
			queries = be.Add(queries, m.attn(q, q, queries, l+"self_attn"))
		}
		queries = ln(queries, l+"norm1")
		queries = be.Add(queries, m.attn(plus(queries, tpe), plus(keys, kpe), keys, l+"cross_attn_token_to_image"))
		queries = ln(queries, l+"norm2")
		h := m.linear(be.ReLU(m.linear(queries, l+"mlp.layers.0")), l+"mlp.layers.1")
		queries = ln(be.Add(queries, h), l+"norm3")
		keys = be.Add(keys, m.attn(plus(keys, kpe), plus(queries, tpe), queries, l+"cross_attn_image_to_token"))
		keys = ln(keys, l+"norm4")
	}
	queries = be.Add(queries, m.attn(plus(queries, tpe), plus(keys, kpe), keys, tr+"final_attn_token_to_image"))
	queries = ln(queries, tr+"norm_final_attn")
	hs, err := be.Download(queries)
	if err != nil {
		return nil, err
	}
	// the mask's features, upscaled with the finer levels' to 256 x 256
	const up = "sam_mask_decoder.output_upscaling."
	src := be.Transpose(keys).Reshape(dModel, grid, grid)
	u := be.Add(be.ConvTranspose2dK(src, m.t(up+"0.weight"), m.t(up+"0.bias")), f.High1)
	u = be.GELU(m.ln2d(u, up+"1"))
	u = be.Add(be.ConvTranspose2dK(u, m.t(up+"3.weight"), m.t(up+"3.bias")), f.High0)
	u = be.GELU(u)
	c0 := u.Dim(0)
	// each mask token's hypernetwork gives the weights of the features
	var d decoded
	hyper := nn.New(4, c0)
	for i := range 4 {
		d.tokens[i] = hs.Data[(2+i)*dModel : (3+i)*dModel]
		o := m.mlp(d.tokens[i], fmt.Sprintf("sam_mask_decoder.output_hypernetworks_mlps.%d", i), 3)
		copy(hyper.Data[i*c0:], o)
	}
	lg := be.Linear(be.Transpose(u.Reshape(c0, maskSide*maskSide)), be.Upload(hyper), nil)
	all, err := be.Download(be.Transpose(lg))
	if err != nil {
		return nil, err
	}
	iou := m.mlp(hs.Data[dModel:2*dModel], "sam_mask_decoder.iou_prediction_head", 3)
	d.object = m.mlp(hs.Data[:dModel], "sam_mask_decoder.pred_obj_score_head", 3)[0]
	n := maskSide * maskSide
	for i := range 4 {
		d.logits[i] = append([]float32(nil), all.Data[i*n:(i+1)*n]...)
		if d.object <= 0 {
			for j := range d.logits[i] {
				d.logits[i][j] = noObject
			}
		}
		d.iou[i] = float32(1 / (1 + math.Exp(-float64(iou[i]))))
	}
	return &d, nil
}

// ln2d is LayerNorm2d by a layer's name on x [C, H, W]: over the
// channels at each place, its eps 1e-6.
func (m *Model) ln2d(x *nn.Tensor, name string) *nn.Tensor {
	be := m.be
	c, h, w := x.Dim(0), x.Dim(1), x.Dim(2)
	y := be.LayerNorm(be.Transpose(x.Reshape(c, h*w)), m.t(name+".weight"), m.t(name+".bias"), 1e-6)
	return be.Transpose(y).Reshape(c, h, w)
}

// tokens are the decoder's tokens: its output tokens (the object score's,
// the IoU's and the four masks'), then the hints' embeddings (the prompt
// encoder's sparse embeddings): the box's corners first, then the points,
// then a padding point (two with no hints at all).
func (m *Model) tokens(box *Box, points []Point) *nn.Tensor {
	type hint struct {
		x, y  float32
		label int
	}
	var hs []hint
	if box != nil {
		hs = append(hs, hint{box.X0, box.Y0, 2}, hint{box.X1, box.Y1, 3})
	}
	for _, p := range points {
		l := 0
		if p.Positive {
			l = 1
		}
		hs = append(hs, hint{p.X, p.Y, l})
	}
	if len(hs) == 0 {
		hs = append(hs, hint{0, 0, -1}) // no hints: SAM 2 gives one "not a point"
	}
	hs = append(hs, hint{0, 0, -1}) // SAM 2 gives its box as points: always padded
	const dec = "sam_mask_decoder."
	out := nn.New(6+len(hs), dModel)
	copy(out.Data, m.w[dec+"obj_score_token.weight"].Data)
	copy(out.Data[dModel:], m.w[dec+"iou_token.weight"].Data)
	copy(out.Data[2*dModel:], m.w[dec+"mask_tokens.weight"].Data)
	g := m.w["sam_prompt_encoder.pe_layer.positional_encoding_gaussian_matrix"]
	for i, h := range hs {
		row := out.Data[(6+i)*dModel : (7+i)*dModel]
		if h.label < 0 {
			copy(row, m.w["sam_prompt_encoder.not_a_point_embed.weight"].Data)
			continue
		}
		pe(g, (h.x+0.5)/Size, (h.y+0.5)/Size, row)
		e := m.w[fmt.Sprintf("sam_prompt_encoder.point_embeddings.%d.weight", h.label)].Data
		for j := range row {
			row[j] += e[j]
		}
	}
	return out
}

// attn is the decoder's attention by its layer's name: q, k and v
// projected (to 256 or 128 channels), 8 heads, projected back.
func (m *Model) attn(q, k, v *nn.Tensor, name string) *nn.Tensor {
	q, k, v = m.linear(q, name+".q_proj"), m.linear(k, name+".k_proj"), m.linear(v, name+".v_proj")
	const heads = 8
	hd := q.Dim(1) / heads
	return m.linear(m.be.Attention(q, k, v, heads, float32(1/math.Sqrt(float64(hd)))), name+".out_proj")
}

// mlp is an MLP of n layers by name on one row, on the CPU: ReLU between
// its layers.
func (m *Model) mlp(x []float32, name string, n int) []float32 {
	t := &nn.Tensor{Shape: []int{1, len(x)}, Data: x}
	for i := range n {
		l := fmt.Sprintf("%s.layers.%d.", name, i)
		t = nn.Linear(t, m.w[l+"weight"], m.w[l+"bias"])
		if i < n-1 {
			t = nn.ReLU(t)
		}
	}
	return t.Data
}
