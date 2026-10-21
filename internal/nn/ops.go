package nn

// The layers, as PyTorch computes them, on single samples: a [C, H, W]
// picture for the convolutions and normalisations of images, [N, D] rows
// for linear layers and attention.

import (
	"math"
	"runtime"
	"sync"
)

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

// Conv2d is nn.Conv2d on x [C, H, W] with w [O, C, kh, kw] and b [O] (nil
// for none), the stride and padding the same in both directions.
func Conv2d(x, w, b *Tensor, stride, pad int) *Tensor {
	c, h, wd := x.Dim(0), x.Dim(1), x.Dim(2)
	o, kh, kw := w.Dim(0), w.Dim(2), w.Dim(3)
	ho, wo := (h+2*pad-kh)/stride+1, (wd+2*pad-kw)/stride+1
	// the patches as columns: [C kh kw, ho wo]
	k := c * kh * kw
	col := make([]float32, k*ho*wo)
	parallel(c, func(ci int) {
		for i := range kh {
			for j := range kw {
				row := col[((ci*kh+i)*kw+j)*ho*wo:]
				for y := range ho {
					sy := y*stride - pad + i
					if sy < 0 || sy >= h {
						continue
					}
					src := x.Data[(ci*h+sy)*wd:]
					for xx := range wo {
						if sx := xx*stride - pad + j; sx >= 0 && sx < wd {
							row[y*wo+xx] = src[sx]
						}
					}
				}
			}
		}
	})
	y := New(o, ho, wo)
	gemmNN(y.Data, w.Data, col, o, k, ho*wo)
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
			d[i] = float32(0.5 * float64(v) * (1 + math.Erf(float64(v)/math.Sqrt2)))
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
	var s float64
	for i, v := range r {
		e := math.Exp(float64(v - m))
		r[i] = float32(e)
		s += e
	}
	inv := float32(1 / s)
	for i := range r {
		r[i] *= inv
	}
}

// Attention is multi-head scaled dot product attention: q [Nq, D], k and v
// [Nk, D], the D channels in heads of D/heads, each head's softmax(q k^T
// scale) v; out [Nq, D].
func Attention(q, k, v *Tensor, heads int, scale float32) *Tensor {
	nq, nk, d := q.Dim(0), k.Dim(0), q.Dim(1)
	hd := d / heads
	out := New(nq, d)
	parallel(heads*((nq+63)/64), func(job int) {
		h, blk := job%heads, job/heads
		r0, r1 := blk*64, min(blk*64+64, nq)
		scores := make([]float32, nk)
		for i := r0; i < r1; i++ {
			qi := q.Data[i*d+h*hd : i*d+h*hd+hd]
			for j := range nk {
				kj := k.Data[j*d+h*hd : j*d+h*hd+hd]
				var s float32
				for c, qv := range qi {
					s += qv * kj[c]
				}
				scores[j] = s * scale
			}
			softmaxRow(scores)
			oi := out.Data[i*d+h*hd : i*d+h*hd+hd]
			for j, p := range scores {
				vj := v.Data[j*d+h*hd : j*d+h*hd+hd]
				for c := range oi {
					oi[c] += p * vj[c]
				}
			}
		}
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
	parallel(c, func(ci int) {
		src, dst := x.Data[ci*h*w:(ci+1)*h*w], y.Data[ci*oh*ow:(ci+1)*oh*ow]
		for oy := range oh {
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
