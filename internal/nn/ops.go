package nn

// The layers, as PyTorch computes them, on single samples: a [C, H, W]
// picture for the convolutions and normalisations of images, [N, D] rows
// for linear layers and attention.

import (
	"math"
	"runtime"
	"sync"
)

// Parallel runs f(i) for i < n over the CPUs.
func Parallel(n int, f func(i int)) { parallel(n, f) }

// parallel runs f(i) for i < n over the CPUs.
func parallel(n int, f func(i int)) {
	w := min(runtime.GOMAXPROCS(0), n)
	if w <= 1 {
		for i := range n {
			f(i)
		}
		return
	}
	var next sync.Mutex
	i := 0
	var wg sync.WaitGroup
	for range w {
		wg.Go(func() {
			for {
				next.Lock()
				k := i
				i++
				next.Unlock()
				if k >= n {
					return
				}
				f(k)
			}
		})
	}
	wg.Wait()
}

// Linear is x [N, in] times w [out, in] transposed, plus b [out] (nil for
// none): nn.Linear.
func Linear(x, w, b *Tensor) *Tensor {
	in, out := x.Dim(-1), w.Dim(0)
	x = x.Reshape(-1, in)
	n := x.Dim(0)
	y := New(n, out)
	gemmNT(y.Data, x.Data, w.Data, n, in, out)
	if b != nil {
		parallel(n, func(i int) {
			row := y.Data[i*out : (i+1)*out]
			for j := range row {
				row[j] += b.Data[j]
			}
		})
	}
	return y
}

// convBand is a band of a convolution's patches, packed, for a job.
type convBand struct {
	col []float32
	pb  packedB
}

var convPool = sync.Pool{New: func() any { return &convBand{} }}

// Conv2d is nn.Conv2d on x [C, H, W] with w [O, C, kh, kw] and b [O] (nil
// for none), the stride and padding the same in both directions. A 1x1
// convolution is a matrix product as it is; any other is done in bands of
// output rows, each band's patches gathered as columns ([C kh kw, the
// band's outputs]) and multiplied by w, so no buffer holds them all.
func Conv2d(x, w, b *Tensor, stride, pad int) *Tensor {
	c, h, wd := x.Dim(0), x.Dim(1), x.Dim(2)
	o, kh, kw := w.Dim(0), w.Dim(2), w.Dim(3)
	ho, wo := (h+2*pad-kh)/stride+1, (wd+2*pad-kw)/stride+1
	k := c * kh * kw
	y := New(o, ho, wo)
	if kh == 1 && kw == 1 && stride == 1 && pad == 0 {
		gemmNN(y.Data, w.Data, x.Data, o, c, h*wd)
	} else {
		band := max(1, 2048/wo) // output rows a job
		parallel((ho+band-1)/band, func(job int) {
			y0 := job * band
			y1 := min(y0+band, ho)
			nb := (y1 - y0) * wo
			cb := convPool.Get().(*convBand)
			defer convPool.Put(cb)
			if cap(cb.col) < k*nb {
				cb.col = make([]float32, k*nb)
			}
			col := cb.col[:k*nb]
			im2colBand(col, x, kh, kw, stride, pad, wo, y0, y1)
			packBInto(&cb.pb, col, nb, 1, k, nb)
			mulPacked(y.Data[y0*wo:], ho*wo, w.Data, k, 1, &cb.pb, o)
		})
	}
	if b != nil {
		parallel(o, func(oi int) {
			row := y.Data[oi*ho*wo : (oi+1)*ho*wo]
			for j := range row {
				row[j] += b.Data[oi]
			}
		})
	}
	return y
}

// im2colBand lays out the kh x kw neighbourhoods of x [C, H, W] (zero
// beyond its edges) for the output rows [y0, y1) of a convolution wo wide:
// C*kh*kw rows of (y1-y0)*wo.
func im2colBand(col []float32, x *Tensor, kh, kw, stride, pad, wo, y0, y1 int) {
	c, h, wd := x.Dim(0), x.Dim(1), x.Dim(2)
	nb := (y1 - y0) * wo
	for ci := range c {
		for i := range kh {
			for j := range kw {
				row := col[((ci*kh+i)*kw+j)*nb : ((ci*kh+i)*kw+j+1)*nb]
				for yy := y0; yy < y1; yy++ {
					out := row[(yy-y0)*wo : (yy-y0+1)*wo]
					sy := yy*stride - pad + i
					if sy < 0 || sy >= h {
						clear(out)
						continue
					}
					src := x.Data[(ci*h+sy)*wd : (ci*h+sy+1)*wd]
					for xx := range out {
						if sx := xx*stride - pad + j; sx >= 0 && sx < wd {
							out[xx] = src[sx]
						} else {
							out[xx] = 0
						}
					}
				}
			}
		}
	}
}

// ConvTranspose2dK is nn.ConvTranspose2d on x [C, H, W] for a kernel as
// large as its stride and no padding (each input value spreads to its own
// k x k outputs): w [C, O, k, k], b [O].
func ConvTranspose2dK(x, w, b *Tensor) *Tensor {
	c, h, wd := x.Dim(0), x.Dim(1), x.Dim(2)
	o, k := w.Dim(1), w.Dim(2)
	y := New(o, h*k, wd*k)
	parallel(o, func(oi int) {
		for i := range k {
			for j := range k {
				for yy := range h {
					out := y.Data[(oi*h*k+yy*k+i)*wd*k+j:]
					for ci := range c {
						wv := w.Data[((ci*o+oi)*k+i)*k+j]
						src := x.Data[(ci*h+yy)*wd : (ci*h+yy+1)*wd]
						for xx, v := range src {
							out[xx*k] += wv * v
						}
					}
				}
			}
		}
		if b != nil {
			plane := y.Data[oi*h*k*wd*k : (oi+1)*h*k*wd*k]
			for i := range plane {
				plane[i] += b.Data[oi]
			}
		}
	})
	return y
}

// LayerNorm normalises x's last dimension (nn.LayerNorm), w and b its
// affine parameters.
func LayerNorm(x, w, b *Tensor, eps float32) *Tensor {
	d := x.Dim(-1)
	n := len(x.Data) / d
	y := &Tensor{Shape: append([]int(nil), x.Shape...), Data: make([]float32, len(x.Data))}
	parallel(n, func(i int) {
		row, out := x.Data[i*d:(i+1)*d], y.Data[i*d:(i+1)*d]
		var mean float64
		for _, v := range row {
			mean += float64(v)
		}
		mean /= float64(d)
		var vr float64
		for _, v := range row {
			dv := float64(v) - mean
			vr += dv * dv
		}
		inv := 1 / math.Sqrt(vr/float64(d)+float64(eps))
		for j, v := range row {
			out[j] = float32((float64(v)-mean)*inv)*w.Data[j] + b.Data[j]
		}
	})
	return y
}

// GroupNorm is nn.GroupNorm on x [C, ...]: the channels in groups, each
// normalised over its channels and positions, then w and b per channel.
func GroupNorm(x *Tensor, groups int, w, b *Tensor, eps float32) *Tensor {
	c := x.Dim(0)
	per := len(x.Data) / c // positions a channel
	cg := c / groups
	y := &Tensor{Shape: append([]int(nil), x.Shape...), Data: make([]float32, len(x.Data))}
	parallel(groups, func(g int) {
		vals := x.Data[g*cg*per : (g+1)*cg*per]
		var mean float64
		for _, v := range vals {
			mean += float64(v)
		}
		mean /= float64(len(vals))
		var vr float64
		for _, v := range vals {
			dv := float64(v) - mean
			vr += dv * dv
		}
		inv := 1 / math.Sqrt(vr/float64(len(vals))+float64(eps))
		for ci := g * cg; ci < (g+1)*cg; ci++ {
			src, out := x.Data[ci*per:(ci+1)*per], y.Data[ci*per:(ci+1)*per]
			for j, v := range src {
				out[j] = float32((float64(v)-mean)*inv)*w.Data[ci] + b.Data[ci]
			}
		}
	})
	return y
}

// GELU is the exact GELU, in place: x Φ(x).
func GELU(x *Tensor) *Tensor {
	parallel(len(x.Data)/4096+1, func(k int) {
		d := x.Data[k*4096 : min((k+1)*4096, len(x.Data))]
		for i, v := range d {
			d[i] = 0.5 * v * (1 + erff(v*(1/math.Sqrt2)))
		}
	})
	return x
}

// ReLU is max(x, 0), in place.
func ReLU(x *Tensor) *Tensor {
	for i, v := range x.Data {
		if v < 0 {
			x.Data[i] = 0
		}
	}
	return x
}

// Add adds b to a, in place (the same shapes).
func Add(a, b *Tensor) *Tensor {
	for i, v := range b.Data {
		a.Data[i] += v
	}
	return a
}

// softmaxRow is the softmax of a row, in place.
func softmaxRow(r []float32) {
	m := r[0]
	for _, v := range r {
		m = max(m, v)
	}
	var s float32
	for i, v := range r {
		e := expf(v - m)
		r[i] = e
		s += e
	}
	inv := 1 / s
	for i := range r {
		r[i] *= inv
	}
}

// Attention is multi-head scaled dot product attention: q [Nq, D], k and v
// [Nk, D], the D channels in heads of D/heads, each head's softmax(q k^T
// scale) v; out [Nq, D]. Each head's keys and values are packed once; the
// jobs are a head's 64 query rows each: their scores, softmax and output.
func Attention(q, k, v *Tensor, heads int, scale float32) *Tensor {
	nq, nk, d := q.Dim(0), k.Dim(0), q.Dim(1)
	hd := d / heads
	out := New(nq, d)
	pk, pv := make([]*packedB, heads), make([]*packedB, heads)
	for h := range heads {
		pk[h] = packB(k.Data[h*hd:], 1, d, hd, nk) // k_h transposed
		pv[h] = packB(v.Data[h*hd:], d, 1, nk, hd)
	}
	const rows = 64
	blocks := (nq + rows - 1) / rows
	parallel(heads*blocks, func(job int) {
		h, blk := job/blocks, job%blocks
		i0 := blk * rows
		n := min(rows, nq-i0)
		scores := make([]float32, n*nk)
		mulPacked(scores, nk, q.Data[i0*d+h*hd:], d, 1, pk[h], n)
		for i := range n {
			r := scores[i*nk : (i+1)*nk]
			for j := range r {
				r[j] *= scale
			}
			softmaxRow(r)
		}
		mulPacked(out.Data[i0*d+h*hd:], d, scores, nk, 1, pv[h], n)
	})
	return out
}

// Bilinear resizes x [C, H, W] to oh x ow, align_corners true
// (F.interpolate mode "bilinear", align_corners=True).
func Bilinear(x *Tensor, oh, ow int) *Tensor {
	c, h, w := x.Dim(0), x.Dim(1), x.Dim(2)
	y := New(c, oh, ow)
	ratio := func(in, out int) float64 {
		if out <= 1 {
			return 0
		}
		return float64(in-1) / float64(out-1)
	}
	ry, rx := ratio(h, oh), ratio(w, ow)
	const band = 16 // output rows a job
	bands := (oh + band - 1) / band
	parallel(c*bands, func(job int) {
		ci, b := job/bands, job%bands
		src, dst := x.Data[ci*h*w:(ci+1)*h*w], y.Data[ci*oh*ow:(ci+1)*oh*ow]
		for oy := b * band; oy < min(b*band+band, oh); oy++ {
			fy := float64(oy) * ry
			y0 := min(int(fy), h-1)
			y1 := min(y0+1, h-1)
			ly := float32(fy - float64(y0))
			for ox := range ow {
				fx := float64(ox) * rx
				x0 := min(int(fx), w-1)
				x1 := min(x0+1, w-1)
				lx := float32(fx - float64(x0))
				top := src[y0*w+x0]*(1-lx) + src[y0*w+x1]*lx
				bot := src[y1*w+x0]*(1-lx) + src[y1*w+x1]*lx
				dst[oy*ow+ox] = top*(1-ly) + bot*ly
			}
		}
	})
	return y
}

// BicubicScale resizes x [C, H, W] by scale factors (F.interpolate mode
// "bicubic", align_corners false, a scale factor given): the output
// floor(H sy) x floor(W sx), each output sampled at (o + 0.5)/s - 0.5 with
// the cubic convolution kernel (A = -0.75), the edges repeated.
func BicubicScale(x *Tensor, sy, sx float64) *Tensor {
	c, h, w := x.Dim(0), x.Dim(1), x.Dim(2)
	oh, ow := int(math.Floor(float64(h)*sy)), int(math.Floor(float64(w)*sx))
	y := New(c, oh, ow)
	const a = -0.75
	weights := func(t float64) [4]float64 {
		cc1 := func(x float64) float64 { return ((a+2)*x-(a+3))*x*x + 1 }
		cc2 := func(x float64) float64 { return ((a*x-5*a)*x+8*a)*x - 4*a }
		return [4]float64{cc2(t + 1), cc1(t), cc1(1 - t), cc2(2 - t)}
	}
	clamp := func(v, n int) int { return min(max(v, 0), n-1) }
	parallel(c, func(ci int) {
		src, dst := x.Data[ci*h*w:(ci+1)*h*w], y.Data[ci*oh*ow:(ci+1)*oh*ow]
		for oy := range oh {
			fy := (float64(oy)+0.5)/sy - 0.5
			iy := int(math.Floor(fy))
			wy := weights(fy - float64(iy))
			for ox := range ow {
				fx := (float64(ox)+0.5)/sx - 0.5
				ix := int(math.Floor(fx))
				wx := weights(fx - float64(ix))
				var s float64
				for m := range 4 {
					row := src[clamp(iy-1+m, h)*w:]
					var r float64
					for n := range 4 {
						r += wx[n] * float64(row[clamp(ix-1+n, w)])
					}
					s += wy[m] * r
				}
				dst[oy*ow+ox] = float32(s)
			}
		}
	})
	return y
}
