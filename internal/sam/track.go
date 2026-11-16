package sam

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"sort"
	"sync"

	"github.com/brunoga/bdtools/internal/nn"
)

// SAM 2's tracking: each picture's mask is encoded with its features into
// a memory, and the next picture's features attend to the memories of the
// pictures given hints (all of them) and of the six before it, and to the
// object pointers (a vector each picture's decoder gives for the object)
// of up to 15 pictures before, before they go to the decoder.

const (
	memDim  = 64 // a memory's channels
	numMem  = 7  // the memories a picture attends to: the hinted pictures', and six before it
	maxPtrs = 16 // the object pointers it attends to, its own picture's place counted
)

// Tracker follows one object through a shot's pictures, as SAM 2's video
// predictor does: Prompt gives it hints on a picture, Track the pictures
// after it (or before, Reverse) one at a time, in order.
type Tracker struct {
	m *Model
	// Frames is the shot's length in pictures (the video predictor's
	// num_frames): the object pointers' time encoding is scaled by it.
	Frames int
	// Reverse tracks towards the shot's start.
	Reverse bool
	out     map[int]*frameOut
}

// frameOut is what a picture leaves for the ones after it: its memory
// (rows [64 64, 64] on the backend, kept) and its object pointer.
type frameOut struct {
	mem  *nn.Tensor
	ptr  []float32
	cond bool // given hints
}

// NewTracker is a tracker of the model's for a shot of frames pictures.
func (m *Model) NewTracker(frames int) *Tracker {
	return &Tracker{m: m, Frames: frames, out: map[int]*frameOut{}}
}

// Close frees the tracker's memories.
func (t *Tracker) Close() {
	for _, o := range t.out {
		t.m.be.Free(o.mem)
	}
	t.out = map[int]*frameOut{}
}

// Prompt is the object's mask on picture idx (features f) from hints:
// SAM 2's conditioning frame, whose memory every picture after it attends
// to. Object is the decoder's logit that the object is there.
func (t *Tracker) Prompt(idx int, f *Features, box *Box, points []Point) (Mask, float32, error) {
	if box == nil && len(points) == 0 {
		return Mask{}, 0, errors.New("sam: no hints")
	}
	n := len(points)
	if box != nil {
		n += 2
	}
	return t.step(idx, f, t.m.tokens(box, points), n, true)
}

// Track is the object's mask on picture idx (features f), from the
// memories of the pictures before it. Object is the decoder's logit that
// the object is there (at most 0: it is not, the mask empty).
func (t *Tracker) Track(idx int, f *Features) (Mask, float32, error) {
	if len(t.out) == 0 {
		return Mask{}, 0, errors.New("sam: tracking with no picture hinted")
	}
	return t.step(idx, f, t.m.tokens(nil, nil), 0, false)
}

// step is a picture's mask, from its features with the memories (or, for
// the first picture hinted, no_mem_embed), for tokens with n hint points
// (a box's two corners counted).
func (t *Tracker) step(idx int, f *Features, tokens *nn.Tensor, n int, cond bool) (Mask, float32, error) {
	m := t.m
	be := m.be
	var pix *nn.Tensor
	if len(t.out) == 0 {
		pix = be.Add(be.Clone(f.Vision), m.constMap("no_mem_embed"))
	} else {
		pix = t.withMemory(idx, f)
	}
	d, err := m.decode(pix, f, tokens)
	if err != nil {
		return Mask{}, 0, err
	}
	// one hint point or none is ambiguous: the best of the three masks;
	// else the single mask, unless it is not stable (SAM 2's video
	// predictor's dynamic_multimask_via_stability), and its token
	best, tok := 0, 0
	if n <= 1 {
		best = 1
		for i := 2; i < 4; i++ {
			if d.iou[i] > d.iou[best] {
				best = i
			}
		}
		tok = best
	} else if !stable(d.logits[0]) {
		best = 1
		for i := 2; i < 4; i++ {
			if d.iou[i] > d.iou[best] {
				best = i
			}
		}
	}
	ptr := m.mlp(d.tokens[tok], "obj_ptr_proj", 3)
	if d.object <= 0 {
		ptr = append([]float32(nil), m.w["no_obj_ptr"].Data...)
	}
	mem, err := t.memory(f, d.logits[best], cond, d.object > 0)
	if err != nil {
		return Mask{}, 0, err
	}
	if old := t.out[idx]; old != nil {
		be.Free(old.mem)
	}
	t.out[idx] = &frameOut{mem: mem, ptr: ptr, cond: cond}
	return Mask{Logits: d.logits[best], IoU: d.iou[best]}, d.object, nil
}

// stable reports whether a mask's area barely changes with its threshold
// (from -0.05 to 0.05 of its logits, 98% of it kept).
func stable(lg []float32) bool {
	var in, un int
	for _, v := range lg {
		if v > 0.05 {
			in++
		}
		if v > -0.05 {
			un++
		}
	}
	return un == 0 || float64(in)/float64(un) >= 0.98
}

// memory is a picture's memory: its features with its mask (logits at
// 256 x 256), made 1024 x 1024 and a probability (or, for a hinted
// picture, 0 or 1), scaled to ±10, through the memory encoder; the
// no-object embedding added where there is no object; rounded to bfloat16
// as the video predictor keeps it. Rows [64 64, 64] on the backend, kept.
func (t *Tracker) memory(f *Features, logits []float32, binary, object bool) (*nn.Tensor, error) {
	m := t.m
	be := m.be
	hi := upsample4(logits, maskSide)
	bands(len(hi.Data), func(lo, hi2 int) {
		for i, v := range hi.Data[lo:hi2] {
			var p float32
			switch {
			case !binary:
				p = float32(1 / (1 + math.Exp(-float64(v))))
			case v > 0:
				p = 1
			}
			hi.Data[lo+i] = 20*p - 10
		}
	})
	const enc = "memory_encoder."
	x := be.Upload(hi)
	for k := range 4 {
		l := fmt.Sprintf(enc+"mask_downsampler.encoder.%d.", 3*k)
		x = be.Conv2d(x, m.t(l+"weight"), m.t(l+"bias"), 2, 1)
		x = be.GELU(m.ln2d(x, fmt.Sprintf(enc+"mask_downsampler.encoder.%d", 3*k+1)))
	}
	x = be.Conv2d(x, m.t(enc+"mask_downsampler.encoder.12.weight"), m.t(enc+"mask_downsampler.encoder.12.bias"), 1, 0)
	x = be.Add(be.Conv2d(f.Vision, m.t(enc+"pix_feat_proj.weight"), m.t(enc+"pix_feat_proj.bias"), 1, 0), x)
	for i := range 2 {
		x = m.cxBlock(x, fmt.Sprintf(enc+"fuser.layers.%d.", i))
	}
	x = be.Conv2d(x, m.t(enc+"out_proj.weight"), m.t(enc+"out_proj.bias"), 1, 0)
	rows, err := be.Download(be.Transpose(x.Reshape(memDim, grid*grid)))
	if err != nil {
		return nil, err
	}
	no := m.w["no_obj_embed_spatial"].Data
	bands(len(rows.Data), func(lo, hi int) {
		for i := lo; i < hi; i++ {
			v := rows.Data[i]
			if !object {
				v += no[i%memDim]
			}
			rows.Data[i] = bfloat16(v)
		}
	})
	mem := be.Upload(rows)
	be.Keep(mem)
	return mem, nil
}

// cxBlock is a ConvNeXt block of the memory encoder's fuser by name, on
// x [256, 64, 64].
func (m *Model) cxBlock(x *nn.Tensor, name string) *nn.Tensor {
	be := m.be
	c, h, w := x.Dim(0), x.Dim(1), x.Dim(2)
	d := be.DepthwiseConv2d(x, m.t(name+"dwconv.weight"), m.t(name+"dwconv.bias"), 1, 3)
	r := be.LayerNorm(be.Transpose(d.Reshape(c, h*w)), m.t(name+"norm.weight"), m.t(name+"norm.bias"), 1e-6)
	r = m.linear(be.GELU(m.linear(r, name+"pwconv1")), name+"pwconv2")
	xr := be.AddScaled(be.Transpose(x.Reshape(c, h*w)), r, m.t(name+"gamma"))
	return be.Transpose(xr).Reshape(c, h, w)
}

// withMemory is picture idx's features after the memory attention: they
// attend to the memories of the hinted pictures and of the six before it,
// and to the object pointers.
func (t *Tracker) withMemory(idx int, f *Features) *nn.Tensor {
	m := t.m
	be := m.be
	sign := 1
	if t.Reverse {
		sign = -1
	}
	var conds []int
	for i, o := range t.out {
		if o.cond {
			conds = append(conds, i)
		}
	}
	sort.Ints(conds)
	// the memories, each with its position encoding and its place in time
	type mem struct {
		rows *nn.Tensor
		tpos int // maskmem_tpos_enc's row
	}
	var mems []mem
	for _, i := range conds {
		mems = append(mems, mem{t.out[i].mem, numMem - 1})
	}
	for tp := 1; tp < numMem; tp++ {
		if o := t.out[idx-sign*(numMem-tp)]; o != nil && !o.cond {
			mems = append(mems, mem{o.mem, numMem - tp - 1})
		}
	}
	// the object pointers: the hinted pictures' up to this one, then up to
	// 15 pictures' before it
	maxP := min(t.Frames, maxPtrs)
	type ptr struct {
		v    []float32
		dist int
	}
	var ptrs []ptr
	for _, i := range conds {
		if (idx-i)*sign >= 0 {
			ptrs = append(ptrs, ptr{t.out[i].ptr, (idx - i) * sign})
		}
	}
	for d := 1; d < maxP; d++ {
		i := idx - sign*d
		if i < 0 || i >= t.Frames {
			break
		}
		if o := t.out[i]; o != nil && !o.cond {
			ptrs = append(ptrs, ptr{o.ptr, d})
		}
	}
	nk := len(mems) * grid * grid
	nptr := len(ptrs) * dModel / memDim
	memory := nn.New(nk+nptr, memDim) // the pointers' part (the memories' are on the backend)
	pos := nn.New(nk+nptr, memDim)
	mpe := m.memPE()
	tpos := m.w["maskmem_tpos_enc"].Data
	var rows []*nn.Tensor
	for k, mm := range mems {
		rows = append(rows, mm.rows.Reshape(grid*grid, memDim, 1))
		p := pos.Data[k*grid*grid*memDim : (k+1)*grid*grid*memDim]
		for i := range p {
			p[i] = mpe.Data[i] + tpos[mm.tpos*memDim+i%memDim]
		}
	}
	tproj := m.w["obj_ptr_tpos_proj.weight"]
	tb := m.w["obj_ptr_tpos_proj.bias"]
	for k, p := range ptrs {
		pe := sine1D(float32(p.dist)/float32(maxP-1), dModel)
		proj := nn.Linear(&nn.Tensor{Shape: []int{1, dModel}, Data: pe}, tproj, tb).Data
		// a pointer is four tokens of 64 channels, each with its place
		for j := range dModel / memDim {
			r := nk + k*(dModel/memDim) + j
			copy(memory.Data[r*memDim:(r+1)*memDim], p.v[j*memDim:(j+1)*memDim])
			copy(pos.Data[r*memDim:(r+1)*memDim], proj)
		}
	}
	if nptr > 0 {
		p := &nn.Tensor{Shape: []int{nptr, memDim, 1}, Data: memory.Data[nk*memDim:]}
		rows = append(rows, be.Upload(p))
	}
	all := rows[0]
	if len(rows) > 1 {
		all = be.Concat(rows...)
	}
	return m.memoryAttention(f.Vision, all.Reshape(nk+nptr, memDim), be.Upload(pos), nptr)
}

// memoryAttention is SAM 2's memory attention: the picture's features
// vision ([256, 64, 64]) attend to memory [M, 64] (pos, its position
// encoding), the last nptr rows of it object pointers' tokens, which
// get no rotary encoding. The features with the memory, [256, 64, 64].
func (m *Model) memoryAttention(vision, memory, pos *nn.Tensor, nptr int) *nn.Tensor {
	be := m.be
	n := grid * grid
	cs := m.keep("rope", func() *nn.Tensor { return ropeTable(dModel, grid) })
	x := be.Transpose(vision.Reshape(dModel, n))
	x = be.AddScaled(x, m.keep("vision.pe", func() *nn.Tensor { return sinePE(dModel, grid) }), m.keep("tenth", func() *nn.Tensor { return constVec(dModel, 0.1) }))
	kin := be.Add(be.Clone(memory), pos)
	nk := memory.Dim(0)
	ln := func(x *nn.Tensor, name string) *nn.Tensor {
		return be.LayerNorm(x, m.t(name+".weight"), m.t(name+".bias"), 1e-5)
	}
	scale := float32(1 / math.Sqrt(dModel))
	for i := range 4 {
		l := fmt.Sprintf("memory_attention.layers.%d.", i)
		t2 := ln(x, l+"norm1")
		q := be.Rope(m.linear(t2, l+"self_attn.q_proj"), cs, n)
		k := be.Rope(m.linear(t2, l+"self_attn.k_proj"), cs, n)
		v := m.linear(t2, l+"self_attn.v_proj")
		x = be.Add(x, m.linear(be.Attention(q, k, v, 1, scale), l+"self_attn.out_proj"))
		t2 = ln(x, l+"norm2")
		q = be.Rope(m.linear(t2, l+"cross_attn_image.q_proj"), cs, n)
		k = be.Rope(m.linear(kin, l+"cross_attn_image.k_proj"), cs, nk-nptr)
		v = m.linear(memory, l+"cross_attn_image.v_proj")
		x = be.Add(x, m.linear(be.Attention(q, k, v, 1, scale), l+"cross_attn_image.out_proj"))
		t2 = ln(x, l+"norm3")
		x = be.Add(x, m.linear(be.ReLU(m.linear(t2, l+"linear1")), l+"linear2"))
	}
	x = ln(x, "memory_attention.norm")
	return be.Transpose(x).Reshape(dModel, grid, grid)
}

// memPE is the memories' position encoding, rows [64 64, 64].
func (m *Model) memPE() *nn.Tensor {
	if pe, ok := m.w["mem.pe"]; ok {
		return pe
	}
	m.w["mem.pe"] = sinePE(memDim, grid)
	return m.w["mem.pe"]
}

// sinePE is PositionEmbeddingSine's encoding of a side x side grid in d
// channels (normalised, 2π), as rows [side side, d]: the row's d/2, then
// the column's, each a sine and a cosine in turn.
func sinePE(d, side int) *nn.Tensor {
	npf := d / 2
	out := nn.New(side*side, d)
	dimT := make([]float64, npf)
	for i := range npf {
		dimT[i] = math.Pow(10000, float64(2*(i/2))/float64(npf))
	}
	at := func(v int) float64 { return float64(float32(v) / (float32(side) + 1e-6) * (2 * math.Pi)) }
	for y := range side {
		for x := range side {
			row := out.Data[(y*side+x)*d : (y*side+x+1)*d]
			for i := range npf {
				py, px := at(y+1)/dimT[i], at(x+1)/dimT[i]
				if i%2 == 0 {
					row[i], row[npf+i] = float32(math.Sin(py)), float32(math.Sin(px))
				} else {
					row[i], row[npf+i] = float32(math.Cos(py)), float32(math.Cos(px))
				}
			}
		}
	}
	return out
}

// sine1D is get_1d_sine_pe of one position in d channels: the sines, then
// the cosines.
func sine1D(p float32, d int) []float32 {
	h := d / 2
	out := make([]float32, d)
	for i := range h {
		v := float64(p) / math.Pow(10000, float64(2*(i/2))/float64(h))
		out[i], out[h+i] = float32(math.Sin(v)), float32(math.Cos(v))
	}
	return out
}

// ropeTable is the memory attention's axial rotary encoding of a side x
// side grid for d channels (compute_axial_cis, theta 10000): for each
// place, the cosine and sine of each channel pair's angle, the first half
// of the pairs by its column, the second by its row; [side side, d].
func ropeTable(d, side int) *nn.Tensor {
	q := d / 4
	freqs := make([]float32, q)
	for i := range q {
		freqs[i] = float32(1 / math.Pow(10000, float64(float32(4*i)/float32(d))))
	}
	out := nn.New(side*side, d)
	for p := range side * side {
		tx, ty := float32(p%side), float32(p/side)
		row := out.Data[p*d : (p+1)*d]
		for j := range q {
			ax, ay := float64(tx*freqs[j]), float64(ty*freqs[j])
			row[2*j], row[2*j+1] = float32(math.Cos(ax)), float32(math.Sin(ax))
			row[2*(q+j)], row[2*(q+j)+1] = float32(math.Cos(ay)), float32(math.Sin(ay))
		}
	}
	return out
}

func constVec(n int, v float32) *nn.Tensor {
	t := nn.New(n)
	for i := range t.Data {
		t.Data[i] = v
	}
	return t
}

// upsample4 is a side x side map made 4 times larger bilinearly
// (F.interpolate, align_corners false): [1, 4 side, 4 side].
func upsample4(x []float32, side int) *nn.Tensor {
	o := 4 * side
	out := nn.New(1, o, o)
	src := func(i int) (int, int, float32) {
		f := max((float32(i)+0.5)/4-0.5, 0)
		a := min(int(f), side-1)
		return a, min(a+1, side-1), f - float32(a)
	}
	bands(o, func(from, to int) {
		for oy := from; oy < to; oy++ {
			y0, y1, ly := src(oy)
			for ox := range o {
				x0, x1, lx := src(ox)
				top := x[y0*side+x0]*(1-lx) + x[y0*side+x1]*lx
				bot := x[y1*side+x0]*(1-lx) + x[y1*side+x1]*lx
				out.Data[oy*o+ox] = top*(1-ly) + bot*ly
			}
		}
	})
	return out
}

// bfloat16 is v rounded to bfloat16 (to nearest, ties to even).
func bfloat16(v float32) float32 {
	b := math.Float32bits(v)
	if b&0x7f800000 == 0x7f800000 {
		return v
	}
	b += 0x7fff + (b>>16)&1
	return math.Float32frombits(b &^ 0xffff)
}

// bands calls f on n split into a band for each CPU, at once.
func bands(n int, f func(lo, hi int)) {
	k := min(runtime.GOMAXPROCS(0), max(n/4096, 1))
	var wg sync.WaitGroup
	for b := range k {
		wg.Go(func() { f(b*n/k, (b+1)*n/k) })
	}
	wg.Wait()
}
