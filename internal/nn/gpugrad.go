package nn

import "github.com/brunoga/bdtools/internal/cuda"

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
