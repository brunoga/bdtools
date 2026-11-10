package nn

import (
	"fmt"

	"github.com/brunoga/bdtools/internal/cuda"
)

// The layers' gradients on the GPU, as grad.go's on the CPU (in single
// precision whatever Half says).

// Conv2dBackward is the gradients of Conv2d(x, w, b, stride, pad) given
// dy, the gradient at its output: at x, at w and at b. The input's
// patches as columns, dw is dy times them, the columns' gradient w's
// transpose times dy, summed back into the input's.
func (g *GPU) Conv2dBackward(x, w, dy *Tensor, stride, pad int) (dx, dw, db *Tensor) {
	c, h, wd := x.Dim(0), x.Dim(1), x.Dim(2)
	o, kh, kw := w.Dim(0), w.Dim(2), w.Dim(3)
	ho, wo := dy.Dim(1), dy.Dim(2)
	k, n := c*kh*kw, ho*wo
	col := g.tensor(k, n)
	gr, bl := each(k * n)
	g.launch("im2col", gr, bl, 0, c, h, wd, kh, kw, stride, pad, ho, wo, ptr(x), ptr(col))
	dw = g.sumK(o, k, n, dy, col)
	dw.Shape = append([]int(nil), w.Shape...)
	wt := g.Transpose(&Tensor{Shape: []int{o, k}, dev: w.dev})
	g.gemm32("gemm_nn", k, n, o, ptr(wt), o, ptr(dy), n, ptr(col), n) // col now the columns' gradient
	dx = g.tensor(c, h, wd)
	gr, bl = each(c * h * wd)
	g.launch("col2im", gr, bl, 0, c, h, wd, kh, kw, stride, pad, ho, wo, ptr(col), ptr(dx))
	db = g.rowsum(dy, o)
	return dx, dw, db
}

// ConvTranspose2dKBackward is the gradients of ConvTranspose2dK(x, w, b)
// given dy, the gradient at its output: at x, at w and at b.
func (g *GPU) ConvTranspose2dKBackward(x, w, dy *Tensor) (dx, dw, db *Tensor) {
	c, h, wd := x.Dim(0), x.Dim(1), x.Dim(2)
	o, k := w.Dim(1), w.Dim(2)
	dx = g.tensor(c, h, wd)
	gr, bl := each(c * h * wd)
	g.launch("convt_bx", gr, bl, 0, c, h, wd, o, k, ptr(dy), ptr(w), ptr(dx))
	dw = g.tensor(w.Shape...)
	g.launch("convt_bw", cuda.Dim{X: c * o * k * k}, cuda.Dim{X: 256}, 0, c, h, wd, o, k, ptr(x), ptr(dy), ptr(dw))
	db = g.rowsum(dy, o)
	return dx, dw, db
}

// ReLUBackward zeroes dy, in place, where ReLU's output y is zero.
func (g *GPU) ReLUBackward(y, dy *Tensor) *Tensor {
	n := Numel(y.Shape)
	gr, bl := each(n)
	g.launch("relu_back", gr, bl, 0, int64(n), ptr(y), ptr(dy))
	return dy
}

// BilinearBackward is the gradient of Bilinear(x, oh, ow) at x ([c, h,
// w]) given dy, the gradient at its output.
func (g *GPU) BilinearBackward(dy *Tensor, h, w int) *Tensor {
	c, oh, ow := dy.Dim(0), dy.Dim(1), dy.Dim(2)
	dx := g.Zeros(c, h, w)
	ratio := func(in, out int) float32 {
		if out <= 1 {
			return 0
		}
		return float32(float64(in-1) / float64(out-1))
	}
	gr, bl := each(c * oh * ow)
	g.launch("bilinear_back", gr, bl, 0, c, h, w, oh, ow, ratio(h, oh), ratio(w, ow), ptr(dy), ptr(dx))
	return dx
}

// Zeros is a new tensor of zeros on the GPU.
func (g *GPU) Zeros(shape ...int) *Tensor {
	t := g.tensor(shape...)
	n := Numel(shape)
	gr, bl := each(n)
	g.launch("fill", gr, bl, 0, int64(n), ptr(t), float32(0))
	return t
}

// rowsum is the sums of x's rows, x seen as rows of its size over rows.
func (g *GPU) rowsum(x *Tensor, rows int) *Tensor {
	out := g.tensor(rows)
	g.launch("rowsum", cuda.Dim{X: rows}, cuda.Dim{X: 256}, 0, int64(Numel(x.Shape)/rows), ptr(x), ptr(out))
	return out
}

// gradChunk is how many of a product's sums each part of sumK takes.
const gradChunk = 4096

// sumK is a b' (a m x n, b k x n, both rows of n) for a short and narrow
// result over a long n (a weights' gradient over every pixel): in parts
// of gradChunk of n, side by side, then summed.
func (g *GPU) sumK(m, k, n int, a, b *Tensor) *Tensor {
	full := n / gradChunk
	parts := full
	if n%gradChunk != 0 {
		parts++
	}
	part := g.tensor(parts, m, k)
	if full > 0 {
		grid, block := cuda.Dim{X: (k + 127) / 128, Y: (m + 127) / 128, Z: full}, cuda.Dim{X: 256}
		g.launch("gemm_nt", grid, block, 0, m, k, gradChunk, ptr(a), n, int64(gradChunk), ptr(b), n, int64(gradChunk),
			ptr(part), k, int64(m*k), float32(1), uintptr(0), 0)
	}
	if rest := n - full*gradChunk; rest > 0 {
		off := uintptr(4 * full * gradChunk)
		g.gemm32("gemm_nt", m, k, rest, ptr(a)+off, n, ptr(b)+off, n, ptr(part)+uintptr(4*full*m*k), k)
	}
	out := g.tensor(m, k)
	gr, bl := each(m * k)
	g.launch("sumparts", gr, bl, 0, int64(m*k), parts, ptr(part), ptr(out))
	return out
}

// gemm32 is gemm in single precision (never the tensor cores): c = a b
// (gemm_nn) or a b' (gemm_nt), m x n, over k.
func (g *GPU) gemm32(kernel string, m, n, k int, a uintptr, lda int, b uintptr, ldb int, c uintptr, ldc int) {
	grid, block := cuda.Dim{X: (n + 127) / 128, Y: (m + 127) / 128, Z: 1}, cuda.Dim{X: 256}
	g.launch(kernel, grid, block, 0, m, n, k, a, lda, int64(0), b, ldb, int64(0), c, ldc, int64(0), float32(1), uintptr(0), 0)
}

// colsum is the sums of x's columns, x seen as rows of cols values.
func (g *GPU) colsum(x *Tensor, rows, cols int) *Tensor {
	return g.rowsum(g.Transpose(&Tensor{Shape: []int{rows, cols}, dev: x.dev}), cols)
}

// LinearBackward is the gradients of Linear(x, w, b) given dy, the
// gradient at its output: at x (x's shape), at w and at b.
func (g *GPU) LinearBackward(x, w, dy *Tensor) (dx, dw, db *Tensor) {
	in, out := x.Dim(-1), w.Dim(0)
	n := Numel(x.Shape) / in
	dx = g.tensor(x.Shape...)
	g.gemm32("gemm_nn", n, in, out, ptr(dy), out, ptr(w), in, ptr(dx), in)
	dyT := g.Transpose(&Tensor{Shape: []int{n, out}, dev: dy.dev})
	xT := g.Transpose(&Tensor{Shape: []int{n, in}, dev: x.dev})
	dw = g.sumK(out, in, n, dyT, xT)
	db = g.rowsum(dyT, out)
	return dx, dw, db
}

// LayerNormBackward is the gradients of LayerNorm(x, w, b, eps) given dy:
// at x, at w and at b.
func (g *GPU) LayerNormBackward(x, w, dy *Tensor, eps float32) (dx, dw, db *Tensor) {
	d := x.Dim(-1)
	rows := Numel(x.Shape) / d
	dx, prod := g.tensor(x.Shape...), g.tensor(rows, d)
	g.launch("layernorm_back", cuda.Dim{X: (rows + 7) / 8}, cuda.Dim{X: 256}, 0, rows, d, ptr(x), ptr(w), ptr(dy), ptr(dx), ptr(prod), eps)
	return dx, g.colsum(prod, rows, d), g.colsum(dy, rows, d)
}

// GroupNormBackward is the gradients of GroupNorm(x, groups, w, b, eps)
// given dy: at x, at w and at b.
func (g *GPU) GroupNormBackward(x *Tensor, groups int, w, dy *Tensor, eps float32) (dx, dw, db *Tensor) {
	c := x.Dim(0)
	per := Numel(x.Shape) / c
	dx, prod := g.tensor(x.Shape...), g.tensor(x.Shape...)
	g.launch("groupnorm_back", cuda.Dim{X: groups}, cuda.Dim{X: 1024}, 0, c/groups, int64(per), ptr(x), ptr(w), ptr(dy), ptr(dx), ptr(prod), eps)
	return dx, g.rowsum(prod, c), g.rowsum(dy, c)
}

// GEGLUBackward is the gradient of GEGLU(x) at x given dy.
func (g *GPU) GEGLUBackward(x, dy *Tensor) *Tensor {
	n, d := dy.Dim(0), dy.Dim(1)
	dx := g.tensor(x.Shape...)
	gr, bl := each(n * d)
	g.launch("geglu_back", gr, bl, 0, int64(n*d), d, ptr(x), ptr(dy), ptr(dx))
	return dx
}

// GELUBackward is the gradient of GELU at its input x given dy.
func (g *GPU) GELUBackward(x, dy *Tensor) *Tensor {
	n := Numel(x.Shape)
	dx := g.tensor(x.Shape...)
	gr, bl := each(n)
	g.launch("gelu_back", gr, bl, 0, int64(n), ptr(x), ptr(dy), ptr(dx))
	return dx
}

// AddScaledBackward is the gradients of AddScaled(x, y, s) = x + y s (s a
// channel's) given dy: at y and at s (at x it is dy itself).
func (g *GPU) AddScaledBackward(y, s, dy *Tensor) (dyIn, ds *Tensor) {
	n, d := Numel(y.Shape), y.Dim(-1)
	gr, bl := each(n)
	dyIn = g.Clone(dy)
	g.launch("mulcols", gr, bl, 0, int64(n), d, ptr(dyIn), ptr(s))
	prod := g.Clone(dy)
	g.launch("mul", gr, bl, 0, int64(n), ptr(prod), ptr(y))
	ds = g.colsum(prod, n/d, d)
	return dyIn, ds
}

// gemm32b is gemm32 in batches, each operand its stride apart.
func (g *GPU) gemm32b(kernel string, m, n, k int, a uintptr, lda, sa int, b uintptr, ldb, sb int, c uintptr, ldc, sc, batch int) {
	grid, block := cuda.Dim{X: (n + 127) / 128, Y: (m + 127) / 128, Z: batch}, cuda.Dim{X: 256}
	g.launch(kernel, grid, block, 0, m, n, k, a, lda, int64(sa), b, ldb, int64(sb), c, ldc, int64(sc), float32(1), uintptr(0), 0)
}

// transposeEach transposes each of x's batch matrices [r, c].
func (g *GPU) transposeEach(x *Tensor, batch, r, c int) *Tensor {
	y := g.tensor(batch, c, r)
	for b := range batch {
		off := uintptr(4 * b * r * c)
		g.launch("transpose", cuda.Dim{X: (c + 31) / 32, Y: (r + 31) / 32}, cuda.Dim{X: 32, Y: 8}, 0, r, c, ptr(x)+off, ptr(y)+off)
	}
	return y
}

// AttentionBackward is the gradients of Attention(q, k, v, heads, scale)
// given dy: at q, at k and at v. The scores are made again (never kept
// by the forward pass).
func (g *GPU) AttentionBackward(q, k, v *Tensor, heads int, scale float32, dy *Tensor) (dq, dk, dv *Tensor) {
	nq, nk, d := q.Dim(0), k.Dim(0), q.Dim(1)
	hd := d / heads
	p := g.tensor(heads, nq, nk)
	g.gemm32b("gemm_nt", nq, nk, hd, ptr(q), d, hd, ptr(k), d, hd, ptr(p), nk, nq*nk, heads)
	g.launch("softmax", cuda.Dim{X: heads * nq}, cuda.Dim{X: 256}, 0, nk, ptr(p), scale)
	dp := g.tensor(heads, nq, nk)
	g.gemm32b("gemm_nt", nq, nk, hd, ptr(dy), d, hd, ptr(v), d, hd, ptr(dp), nk, nq*nk, heads)
	pt := g.transposeEach(p, heads, nq, nk)
	dv = g.tensor(nk, d)
	g.gemm32b("gemm_nn", nk, hd, nq, ptr(pt), nq, nk*nq, ptr(dy), d, hd, ptr(dv), d, hd, heads)
	g.launch("softmax_back", cuda.Dim{X: heads * nq}, cuda.Dim{X: 256}, 0, nk, ptr(p), ptr(dp), scale)
	dq = g.tensor(nq, d)
	g.gemm32b("gemm_nn", nq, hd, nk, ptr(dp), nk, nq*nk, ptr(k), d, hd, ptr(dq), d, hd, heads)
	dst := g.transposeEach(dp, heads, nq, nk)
	dk = g.tensor(nk, d)
	g.gemm32b("gemm_nn", nk, hd, nq, ptr(dst), nq, nk*nq, ptr(q), d, hd, ptr(dk), d, hd, heads)
	return dq, dk, dv
}

// TemporalAttentionBackward is the gradients of TemporalAttention(q, peQ,
// peK, peV, ks, vs, heads, scale) given dy: at q and peQ, at peK and peV,
// and at the keys and values of the frames want says (nil for the others).
func (g *GPU) TemporalAttentionBackward(q, peQ, peK, peV *Tensor, ks, vs []*Tensor, heads int, scale float32, dy *Tensor,
	want []bool) (dq, dpeQ, dpeK, dpeV *Tensor, dks, dvs []*Tensor) {
	d, c := q.Dim(0), q.Dim(1)
	t := len(ks)
	const maxFrames = 32 // kernels.cu's MAXFRAMES
	dks, dvs = make([]*Tensor, t), make([]*Tensor, t)
	if c > 1024 || t > maxFrames {
		g.fail(fmt.Errorf("nn: a temporal attention %d channels wide over %d frames", c, t))
		return g.tensor(d, c), g.tensor(c), g.tensor(t, c), g.tensor(t, c), dks, dvs
	}
	addrs, daddrs := make([]uint64, 2*maxFrames), make([]uint64, 2*maxFrames)
	for f := range t {
		addrs[f], addrs[maxFrames+f] = uint64(ptr(ks[f])), uint64(ptr(vs[f])) //nolint:gosec // G602: f < t <= maxFrames
		if want[f] {
			dks[f], dvs[f] = g.tensor(d, c), g.tensor(d, c)
			daddrs[f], daddrs[maxFrames+f] = uint64(ptr(dks[f])), uint64(ptr(dvs[f])) //nolint:gosec // G602: as above
		}
	}
	hd := c / heads
	dq, r, ds := g.tensor(d, c), g.tensor(d, heads*t), g.tensor(d, heads*t)
	g.launch("temporal_back", cuda.Dim{X: d}, cuda.Dim{X: c}, 4*(c+2*heads*t), d, c, hd, t, ptr(q), ptr(peQ), addrs,
		ptr(peK), ptr(peV), scale, ptr(dy), ptr(dq), daddrs, ptr(r), ptr(ds))
	dpeK, dpeV = g.tensor(t, c), g.tensor(t, c)
	gr, bl := each(t * c)
	g.launch("temporal_pe_back", gr, bl, 0, d, c, hd, t, ptr(q), ptr(peQ), ptr(dy), ptr(r), ptr(ds), scale, ptr(dpeK), ptr(dpeV))
	return dq, g.colsum(dq, d, c), dpeK, dpeV, dks, dvs
}
