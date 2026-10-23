package nn

//go:generate nvcc -ptx -arch=compute_80 -O3 -o kernels.ptx kernels.cu

import (
	_ "embed"
	"fmt"

	"github.com/brunoga/bdtools/internal/cuda"
)

//go:embed kernels.ptx
var kernelsPTX []byte

// GPU is the CUDA backend: tensors in the GPU's memory, the ops its
// kernels (kernels.cu). Memory is reused: what an op allocates is given
// back at Release unless kept, to be handed out again to the next op
// asking for as many bytes (a network's frames ask for the same each
// time); what no op asked for again between two Releases is freed (the
// sizes change with the picture's). An op's failure is kept and reported
// by the next Download.
type GPU struct {
	c     *cuda.Context
	mod   *cuda.Module
	k     map[string]*cuda.Kernel
	free  map[int][]spare
	arena []*alloc
	gen   int // Releases so far
	err   error

	// Half rounds the matrix products' inputs to half precision, for the
	// tensor cores (their sums stay single precision).
	Half bool
}

// spare is a free buffer and the Release it was given back at.
type spare struct {
	buf *cuda.Buffer
	gen int
}

// alloc is a device allocation; devBuf a tensor's place in one.
type alloc struct {
	buf  *cuda.Buffer
	kept bool
}

type devBuf struct {
	a   *alloc
	off int // bytes
}

// NewGPU is a backend on the first CUDA GPU (cuda.ErrUnavailable when
// there is none).
func NewGPU() (*GPU, error) {
	c, err := cuda.New()
	if err != nil {
		return nil, err
	}
	m, err := c.Load(kernelsPTX)
	if err != nil {
		c.Close()
		return nil, err
	}
	g := &GPU{c: c, mod: m, k: map[string]*cuda.Kernel{}, free: map[int][]spare{}}
	for _, name := range []string{"gemm_nn", "gemm_nt", "conv2d", "layernorm", "groupnorm", "softmax", "gelu", "relu",
		"add", "addscaled", "geglu", "split3", "transpose", "tokens", "bilinear", "convt", "temporal",
		"gemm_nn_tc", "gemm_nt_tc", "conv2d_tc", "net_input", "moments", "refine_input", "renorm", "attention_tc",
		"disparity", "eyes"} {
		if g.k[name], err = m.Kernel(name); err != nil {
			c.Close()
			return nil, err
		}
	}
	return g, nil
}

// Name is the GPU's.
func (g *GPU) Name() string { return g.c.Name }

// Close frees the GPU's memory and context.
func (g *GPU) Close() { g.c.Close() }

func (g *GPU) fail(err error) {
	if g.err == nil && err != nil {
		g.err = err
	}
}

// tensor is a new tensor of a shape on the GPU (its contents undefined).
func (g *GPU) tensor(shape ...int) *Tensor {
	n := 4 * max(Numel(shape), 1)
	var b *cuda.Buffer
	if l := g.free[n]; len(l) > 0 {
		b, g.free[n] = l[len(l)-1].buf, l[:len(l)-1]
	} else {
		var err error
		if b, err = g.c.Alloc(n); err != nil {
			// the spares first, then again
			g.drop(func(spare) bool { return true })
			if b, err = g.c.Alloc(n); err != nil {
				g.fail(err)
				b = &cuda.Buffer{Size: n}
			}
		}
	}
	a := &alloc{buf: b}
	g.arena = append(g.arena, a)
	return &Tensor{Shape: append([]int(nil), shape...), dev: &devBuf{a: a}}
}

// ptr is a tensor's device address (0 for nil: an absent bias).
func ptr(t *Tensor) uintptr {
	if t == nil {
		return 0
	}
	if t.dev == nil {
		panic(fmt.Sprintf("nn: a %v tensor not on the GPU", t.Shape))
	}
	return t.dev.a.buf.Ptr() + uintptr(t.dev.off)
}

func (g *GPU) launch(name string, grid, block cuda.Dim, shared int, args ...any) {
	if g.err != nil {
		return
	}
	g.fail(g.k[name].Launch(grid, block, shared, args...))
}

// each is the grid for n elements, 256 threads a block, a stride loop
// past 64K blocks.
func each(n int) (cuda.Dim, cuda.Dim) {
	return cuda.Dim{X: min((n+255)/256, 65535)}, cuda.Dim{X: 256}
}

func (g *GPU) Upload(t *Tensor) *Tensor {
	out := g.tensor(t.Shape...)
	g.fail(g.c.Upload(out.dev.a.buf, t.Data))
	return out
}

func (g *GPU) Download(t *Tensor) (*Tensor, error) {
	if g.err != nil {
		return nil, g.err
	}
	if err := g.c.Sync(); err != nil {
		g.fail(err)
		return nil, err
	}
	out := New(t.Shape...)
	if t.dev.off == 0 {
		return out, g.c.Download(out.Data, t.dev.a.buf)
	}
	tmp := g.tensor(t.Shape...)
	if err := g.c.Copy(tmp.dev.a.buf, 0, t.dev.a.buf, t.dev.off, 4*Numel(t.Shape)); err != nil {
		return nil, err
	}
	return out, g.c.Download(out.Data, tmp.dev.a.buf)
}

func (g *GPU) Keep(t *Tensor) { t.dev.a.kept = true }

func (g *GPU) Free(t *Tensor) {
	if a := t.dev.a; a.kept {
		a.kept = false
		g.free[a.buf.Size] = append(g.free[a.buf.Size], spare{a.buf, g.gen})
	}
}

func (g *GPU) Release() {
	g.gen++
	for _, a := range g.arena {
		if !a.kept {
			g.free[a.buf.Size] = append(g.free[a.buf.Size], spare{a.buf, g.gen})
		}
	}
	g.arena = g.arena[:0]
	// what lay unused through the last frame is not needed any more
	g.drop(func(s spare) bool { return s.gen < g.gen-1 })
}

// drop frees the spare buffers gone says go.
func (g *GPU) drop(gone func(spare) bool) {
	for n, l := range g.free {
		k := 0
		for _, s := range l {
			if gone(s) {
				g.c.Free(s.buf)
			} else {
				l[k] = s
				k++
			}
		}
		if k == 0 {
			delete(g.free, n)
		} else {
			g.free[n] = l[:k]
		}
	}
}

// gemm queues c = alpha a b (+ bias) through kernel nn or nt (see
// kernels.cu), batch times.
func (g *GPU) gemm(kernel string, m, n, k int, a uintptr, lda, sa int, b uintptr, ldb, sb int, c uintptr, ldc, sc int,
	alpha float32, bias uintptr, biasMode, batch int) {
	grid := cuda.Dim{X: (n + 127) / 128, Y: (m + 127) / 128, Z: batch}
	if g.Half {
		kernel += "_tc"
	}
	g.launch(kernel, grid, cuda.Dim{X: 256}, 0, m, n, k, a, lda, int64(sa), b, ldb, int64(sb), c, ldc, int64(sc),
		alpha, bias, biasMode)
}

func biasMode(b *Tensor, mode int) int {
	if b == nil {
		return 0
	}
	return mode
}

func (g *GPU) Linear(x, w, b *Tensor) *Tensor {
	in, out := x.Dim(-1), w.Dim(0)
	n := Numel(x.Shape) / in
	y := g.tensor(n, out)
	g.gemm("gemm_nt", n, out, in, ptr(x), in, 0, ptr(w), in, 0, ptr(y), out, 0, 1, ptr(b), biasMode(b, 1), 1)
	return y
}

func (g *GPU) Conv2d(x, w, b *Tensor, stride, pad int) *Tensor {
	c, h, wd := x.Dim(0), x.Dim(1), x.Dim(2)
	o, kh, kw := w.Dim(0), w.Dim(2), w.Dim(3)
	ho, wo := (h+2*pad-kh)/stride+1, (wd+2*pad-kw)/stride+1
	y := g.tensor(o, ho, wo)
	if kh == 1 && kw == 1 && stride == 1 && pad == 0 {
		g.gemm("gemm_nn", o, h*wd, c, ptr(w), c, 0, ptr(x), h*wd, 0, ptr(y), h*wd, 0, 1, ptr(b), biasMode(b, 2), 1)
		return y
	}
	n, k := ho*wo, c*kh*kw
	grid := cuda.Dim{X: (n + 127) / 128, Y: (o + 127) / 128}
	kernel := "conv2d"
	if g.Half {
		kernel += "_tc"
	}
	g.launch(kernel, grid, cuda.Dim{X: 256}, 0, o, n, k, ptr(w), ptr(x), ptr(y), ptr(b), biasMode(b, 2),
		c, h, wd, kh, kw, stride, pad, wo)
	return y
}

func (g *GPU) ConvTranspose2dK(x, w, b *Tensor) *Tensor {
	c, h, wd := x.Dim(0), x.Dim(1), x.Dim(2)
	o, k := w.Dim(1), w.Dim(2)
	y := g.tensor(o, h*k, wd*k)
	gr, bl := each(o * h * k * wd * k)
	g.launch("convt", gr, bl, 0, c, h, wd, o, k, ptr(x), ptr(w), ptr(b), ptr(y))
	return y
}

func (g *GPU) LayerNorm(x, w, b *Tensor, eps float32) *Tensor {
	d := x.Dim(-1)
	y := g.tensor(x.Shape...)
	rows := Numel(x.Shape) / d
	g.launch("layernorm", cuda.Dim{X: (rows + 7) / 8}, cuda.Dim{X: 256}, 0, rows, d, ptr(x), ptr(w), ptr(b), ptr(y), eps)
	return y
}

func (g *GPU) GroupNorm(x *Tensor, groups int, w, b *Tensor, eps float32) *Tensor {
	c := x.Dim(0)
	per := Numel(x.Shape) / c
	y := g.tensor(x.Shape...)
	g.launch("groupnorm", cuda.Dim{X: groups}, cuda.Dim{X: 1024}, 0, c/groups, int64(per), ptr(x), ptr(w), ptr(b), ptr(y), eps)
	return y
}

func (g *GPU) GELU(x *Tensor) *Tensor {
	n := Numel(x.Shape)
	gr, bl := each(n)
	g.launch("gelu", gr, bl, 0, int64(n), ptr(x))
	return x
}

func (g *GPU) ReLU(x *Tensor) *Tensor {
	n := Numel(x.Shape)
	gr, bl := each(n)
	g.launch("relu", gr, bl, 0, int64(n), ptr(x))
	return x
}

func (g *GPU) Add(a, b *Tensor) *Tensor {
	n := Numel(a.Shape)
	gr, bl := each(n)
	g.launch("add", gr, bl, 0, int64(n), ptr(a), ptr(b))
	return a
}

func (g *GPU) Clone(x *Tensor) *Tensor {
	y := g.tensor(x.Shape...)
	g.fail(g.c.Copy(y.dev.a.buf, 0, x.dev.a.buf, x.dev.off, 4*Numel(x.Shape)))
	return y
}

func (g *GPU) Attention(q, k, v *Tensor, heads int, scale float32) *Tensor {
	nq, nk, d := q.Dim(0), k.Dim(0), q.Dim(1)
	hd := d / heads
	out := g.tensor(nq, d)
	if g.Half && hd == 64 && (ptr(q)|ptr(k)|ptr(v))%8 == 0 {
		// in one pass on the tensor cores, the scores never stored
		g.launch("attention_tc", cuda.Dim{X: (nq + 63) / 64, Y: heads}, cuda.Dim{X: 128}, 0,
			nq, nk, d, ptr(q), ptr(k), ptr(v), ptr(out), scale)
		return out
	}
	scores := g.tensor(heads, nq, nk)
	g.gemm("gemm_nt", nq, nk, hd, ptr(q), d, hd, ptr(k), d, hd, ptr(scores), nk, nq*nk, 1, 0, 0, heads)
	g.launch("softmax", cuda.Dim{X: heads * nq}, cuda.Dim{X: 256}, 0, nk, ptr(scores), scale)
	g.gemm("gemm_nn", nq, hd, nk, ptr(scores), nk, nq*nk, ptr(v), d, hd, ptr(out), d, hd, 1, 0, 0, heads)
	return out
}

func (g *GPU) Bilinear(x *Tensor, oh, ow int) *Tensor {
	c, h, w := x.Dim(0), x.Dim(1), x.Dim(2)
	y := g.tensor(c, oh, ow)
	ratio := func(in, out int) float32 {
		if out <= 1 {
			return 0
		}
		return float32(float64(in-1) / float64(out-1))
	}
	gr, bl := each(c * oh * ow)
	g.launch("bilinear", gr, bl, 0, c, h, w, oh, ow, ratio(h, oh), ratio(w, ow), ptr(x), ptr(y))
	return y
}

func (g *GPU) Tokens(patches, cls, pos *Tensor) *Tensor {
	d, n := patches.Dim(0), patches.Dim(1)
	out := g.tensor(1+n, d)
	gr, bl := each((1 + n) * d)
	g.launch("tokens", gr, bl, 0, n, d, ptr(patches), ptr(cls), ptr(pos), ptr(out))
	return out
}

func (g *GPU) Split3(x *Tensor) (*Tensor, *Tensor, *Tensor) {
	n, d := x.Dim(0), x.Dim(1)/3
	q, k, v := g.tensor(n, d), g.tensor(n, d), g.tensor(n, d)
	gr, bl := each(n * d)
	g.launch("split3", gr, bl, 0, int64(n*d), d, ptr(x), ptr(q), ptr(k), ptr(v))
	return q, k, v
}

func (g *GPU) AddScaled(x, y, s *Tensor) *Tensor {
	n := Numel(x.Shape)
	gr, bl := each(n)
	g.launch("addscaled", gr, bl, 0, int64(n), x.Dim(-1), ptr(x), ptr(y), ptr(s))
	return x
}

func (g *GPU) Transpose(x *Tensor) *Tensor {
	r, c := x.Dim(0), x.Dim(1)
	y := g.tensor(c, r)
	g.launch("transpose", cuda.Dim{X: (c + 31) / 32, Y: (r + 31) / 32}, cuda.Dim{X: 32, Y: 8}, 0, r, c, ptr(x), ptr(y))
	return y
}

func (g *GPU) GEGLU(x *Tensor) *Tensor {
	n, d := x.Dim(0), x.Dim(1)/2
	y := g.tensor(n, d)
	gr, bl := each(n * d)
	g.launch("geglu", gr, bl, 0, int64(n*d), d, ptr(x), ptr(y))
	return y
}

func (g *GPU) Rows(x *Tensor, from int) *Tensor {
	d := x.Dim(-1)
	return &Tensor{Shape: []int{x.Dim(0) - from, d}, dev: &devBuf{a: x.dev.a, off: x.dev.off + 4*from*d}}
}

func (g *GPU) TemporalAttention(q, peQ, peK, peV *Tensor, ks, vs []*Tensor, heads int, scale float32) *Tensor {
	d, c := q.Dim(0), q.Dim(1)
	t := len(ks)
	const maxFrames = 32 // kernels.cu's MAXFRAMES
	if c > 1024 || t > maxFrames {
		g.fail(fmt.Errorf("nn: a temporal attention %d channels wide over %d frames", c, t))
		return g.tensor(d, c)
	}
	// the frames' addresses, by value: struct frames
	addrs := make([]uint64, 2*maxFrames)
	for f := range t {
		addrs[f], addrs[maxFrames+f] = uint64(ptr(ks[f])), uint64(ptr(vs[f])) //nolint:gosec // G602: f < t <= maxFrames
	}
	out := g.tensor(d, c)
	shared := 4 * (c + heads*t)
	g.launch("temporal", cuda.Dim{X: d}, cuda.Dim{X: c}, shared, d, c, c/heads, t, ptr(q), ptr(peQ),
		addrs, ptr(peK), ptr(peV), scale, ptr(out))
	return out
}
