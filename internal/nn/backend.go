package nn

import "math"

// Backend runs the layers somewhere: on the CPU (CPU, the package's
// functions) or on a GPU (NewGPU). A backend's ops take and give its own
// tensors: Upload makes one of a tensor in memory, Download the reverse.
// The ops that say "in place" change their first operand and give it
// back.
type Backend interface {
	// Upload is t as the backend's tensor (the CPU's: t itself).
	Upload(t *Tensor) *Tensor
	// Download is the backend's tensor in memory (waiting for the ops
	// before to have run).
	Download(t *Tensor) (*Tensor, error)

	Linear(x, w, b *Tensor) *Tensor
	Conv2d(x, w, b *Tensor, stride, pad int) *Tensor
	ConvTranspose2dK(x, w, b *Tensor) *Tensor
	LayerNorm(x, w, b *Tensor, eps float32) *Tensor
	GroupNorm(x *Tensor, groups int, w, b *Tensor, eps float32) *Tensor
	GELU(x *Tensor) *Tensor   // in place
	ReLU(x *Tensor) *Tensor   // in place
	Add(a, b *Tensor) *Tensor // in place
	Clone(x *Tensor) *Tensor
	Attention(q, k, v *Tensor, heads int, scale float32) *Tensor
	Bilinear(x *Tensor, oh, ow int) *Tensor

	// Tokens is a vision transformer's tokens: a class token cls [D]
	// first, then the patches [D, N] (channels first) as rows, plus the
	// position embedding pos [1+N, D].
	Tokens(patches, cls, pos *Tensor) *Tensor
	// Split3 splits [N, 3D] rows into their three [N, D] thirds.
	Split3(x *Tensor) (q, k, v *Tensor)
	// AddScaled adds y times a per-channel g [D] to x [N, D], in place.
	AddScaled(x, y, g *Tensor) *Tensor
	// Transpose is x [R, C] as [C, R].
	Transpose(x *Tensor) *Tensor
	// GEGLU is a GEGLU's activation: x [N, 2D]'s first half of each row
	// times the GELU of its second ([N, D]).
	GEGLU(x *Tensor) *Tensor
	// Rows is x's rows from the from-th on ([N-from, D]).
	Rows(x *Tensor, from int) *Tensor
	// TemporalAttention attends, at each of D positions, from the query
	// q [D, C] plus peQ [C] over T frames' keys ks[f] [D, C] plus peK[f]
	// and values vs[f] plus peV[f] (peK, peV [T, C]), the C channels in
	// heads.
	TemporalAttention(q, peQ, peK, peV *Tensor, ks, vs []*Tensor, heads int, scale float32) *Tensor

	// Keep keeps a tensor past Release (the GPU's are otherwise reused).
	Keep(t *Tensor)
	// Free frees a kept tensor.
	Free(t *Tensor)
	// Release gives back every tensor made since the last Release that is
	// not kept: a network's intermediates, once its output is downloaded.
	Release()
}

// CPU is the CPU backend: the package's functions.
var CPU Backend = cpu{}

type cpu struct{}

func (cpu) Upload(t *Tensor) *Tensor                 { return t }
func (cpu) Download(t *Tensor) (*Tensor, error)      { return t, nil }
func (cpu) Linear(x, w, b *Tensor) *Tensor           { return Linear(x, w, b) }
func (cpu) ConvTranspose2dK(x, w, b *Tensor) *Tensor { return ConvTranspose2dK(x, w, b) }
func (cpu) Conv2d(x, w, b *Tensor, stride, pad int) *Tensor {
	return Conv2d(x, w, b, stride, pad)
}
func (cpu) LayerNorm(x, w, b *Tensor, eps float32) *Tensor { return LayerNorm(x, w, b, eps) }
func (cpu) GroupNorm(x *Tensor, groups int, w, b *Tensor, eps float32) *Tensor {
	return GroupNorm(x, groups, w, b, eps)
}
func (cpu) GELU(x *Tensor) *Tensor                 { return GELU(x) }
func (cpu) ReLU(x *Tensor) *Tensor                 { return ReLU(x) }
func (cpu) Add(a, b *Tensor) *Tensor               { return Add(a, b) }
func (cpu) Clone(x *Tensor) *Tensor                { return x.Clone() }
func (cpu) Keep(*Tensor)                           {}
func (cpu) Free(*Tensor)                           {}
func (cpu) Release()                               {}
func (cpu) Bilinear(x *Tensor, oh, ow int) *Tensor { return Bilinear(x, oh, ow) }
func (cpu) Attention(q, k, v *Tensor, heads int, scale float32) *Tensor {
	return Attention(q, k, v, heads, scale)
}

func (cpu) Tokens(patches, cls, pos *Tensor) *Tensor {
	d, n := patches.Dim(0), patches.Dim(1)
	out := New(1+n, d)
	for c := range d {
		out.Data[c] = cls.Data[c] + pos.Data[c]
	}
	parallel(n, func(i int) {
		row := out.Data[(1+i)*d : (2+i)*d]
		for c := range d {
			row[c] = patches.Data[c*n+i] + pos.Data[(1+i)*d+c]
		}
	})
	return out
}

func (cpu) Split3(x *Tensor) (*Tensor, *Tensor, *Tensor) {
	n, d := x.Dim(0), x.Dim(1)/3
	q, k, v := New(n, d), New(n, d), New(n, d)
	for i := range n {
		row := x.Data[i*3*d:]
		copy(q.Data[i*d:(i+1)*d], row[:d])
		copy(k.Data[i*d:(i+1)*d], row[d:2*d])
		copy(v.Data[i*d:(i+1)*d], row[2*d:3*d])
	}
	return q, k, v
}

func (cpu) AddScaled(x, y, g *Tensor) *Tensor {
	d := x.Dim(-1)
	for i, v := range y.Data {
		x.Data[i] += v * g.Data[i%d]
	}
	return x
}

func (cpu) Transpose(x *Tensor) *Tensor {
	r, c := x.Dim(0), x.Dim(1)
	out := New(c, r)
	parallel(c, func(j int) {
		row := out.Data[j*r : (j+1)*r]
		for i := range r {
			row[i] = x.Data[i*c+j]
		}
	})
	return out
}

func (cpu) GEGLU(x *Tensor) *Tensor {
	n, d := x.Dim(0), x.Dim(1)/2
	out := New(n, d)
	parallel(n, func(i int) {
		src, dst := x.Data[i*2*d:(i+1)*2*d], out.Data[i*d:(i+1)*d]
		for j := range dst {
			g := src[d+j]
			dst[j] = src[j] * (0.5 * g * (1 + erff(g*(1/math.Sqrt2))))
		}
	})
	return out
}

func (cpu) Rows(x *Tensor, from int) *Tensor {
	d := x.Dim(-1)
	return &Tensor{Shape: []int{x.Dim(0) - from, d}, Data: x.Data[from*d:]}
}

func (cpu) TemporalAttention(q, peQ, peK, peV *Tensor, ks, vs []*Tensor, heads int, scale float32) *Tensor {
	d, c := q.Dim(0), q.Dim(1)
	t := len(ks)
	hd := c / heads
	out := New(d, c)
	const chunk = 64
	parallel((d+chunk-1)/chunk, func(job int) {
		qq := make([]float32, c)
		scores := make([]float32, t)
		for p := job * chunk; p < min(job*chunk+chunk, d); p++ {
			for i := range qq {
				qq[i] = q.Data[p*c+i] + peQ.Data[i]
			}
			for h := range heads {
				lo := h * hd
				for f := range t {
					kf, pk := ks[f].Data[p*c:(p+1)*c], peK.Data[f*c:]
					var s float32
					for i := lo; i < lo+hd; i++ {
						s += qq[i] * (kf[i] + pk[i])
					}
					scores[f] = s * scale
				}
				softmax64(scores)
				oh := out.Data[p*c+lo : p*c+lo+hd]
				for f, pr := range scores {
					vf, pv := vs[f].Data[p*c:(p+1)*c], peV.Data[f*c:]
					for i := range oh {
						oh[i] += pr * (vf[lo+i] + pv[lo+i])
					}
				}
			}
		}
	})
	return out
}

// softmax64 is a row's softmax, summed in float64.
func softmax64(r []float32) {
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
