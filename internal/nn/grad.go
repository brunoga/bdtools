package nn

import "math"

// The layers' gradients, for training on the CPU: given a layer's input
// and the loss's gradient at its output, the gradients at its input and
// its parameters.

// Conv2dBackward is the gradients of Conv2d(x, w, b, stride, pad) given
// dy, the gradient at its output: at x, at w and at b.
func Conv2dBackward(x, w, dy *Tensor, stride, pad int) (dx, dw, db *Tensor) {
	c, h, wd := x.Dim(0), x.Dim(1), x.Dim(2)
	o, kh, kw := w.Dim(0), w.Dim(2), w.Dim(3)
	ho, wo := dy.Dim(1), dy.Dim(2)
	k, n := c*kh*kw, ho*wo
	band := max(1, 2048/wo)
	jobs := (ho + band - 1) / band
	// Each band's gradients alone (the bands' input rows overlap), summed
	// after.
	dws := make([][]float32, jobs)
	dxs := make([][]float32, jobs)
	parallel(jobs, func(job int) {
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
		dyb := dy.Data[y0*wo:]
		// dw += dy's band times the columns transposed
		g := make([]float32, o*k)
		packBInto(&cb.pb, col, 1, nb, nb, k)
		mulPacked(g, k, dyb, n, 1, &cb.pb, o)
		dws[job] = g
		// the columns' gradient: w transposed times dy's band
		dcol := make([]float32, k*nb)
		packBInto(&cb.pb, dyb, n, 1, o, nb)
		mulPacked(dcol, nb, w.Data, 1, k, &cb.pb, k)
		// back to the input rows the band reads
		r0 := y0*stride - pad
		rows := (y1-1-y0)*stride + kh
		part := make([]float32, c*rows*wd)
		for ci := range c {
			for i := range kh {
				for j := range kw {
					row := dcol[((ci*kh+i)*kw+j)*nb:]
					for yy := y0; yy < y1; yy++ {
						sy := yy*stride - pad + i
						if sy < 0 || sy >= h {
							continue
						}
						dst := part[(ci*rows+sy-r0)*wd : (ci*rows+sy-r0+1)*wd]
						src := row[(yy-y0)*wo : (yy-y0+1)*wo]
						for xx, v := range src {
							if sx := xx*stride - pad + j; sx >= 0 && sx < wd {
								dst[sx] += v
							}
						}
					}
				}
			}
		}
		dxs[job] = part
	})
	dx, dw, db = New(c, h, wd), New(w.Shape...), New(o)
	parallel(c, func(ci int) {
		plane := dx.Data[ci*h*wd : (ci+1)*h*wd]
		for job, part := range dxs {
			y0 := job * band
			y1 := min(y0+band, ho)
			r0 := y0*stride - pad
			rows := (y1-1-y0)*stride + kh
			for r := range rows {
				sy := r0 + r
				if sy < 0 || sy >= h {
					continue
				}
				dst := plane[sy*wd : (sy+1)*wd]
				for x, v := range part[(ci*rows+r)*wd : (ci*rows+r+1)*wd] {
					dst[x] += v
				}
			}
		}
	})
	parallel(o, func(oi int) {
		row := dw.Data[oi*k : (oi+1)*k]
		for _, g := range dws {
			for i, v := range g[oi*k : (oi+1)*k] {
				row[i] += v
			}
		}
		var s float32
		for _, v := range dy.Data[oi*n : (oi+1)*n] {
			s += v
		}
		db.Data[oi] = s
	})
	return dx, dw, db
}

// ConvTranspose2dKBackward is the gradients of ConvTranspose2dK(x, w, b)
// given dy, the gradient at its output: at x, at w and at b.
func ConvTranspose2dKBackward(x, w, dy *Tensor) (dx, dw, db *Tensor) {
	c, h, wd := x.Dim(0), x.Dim(1), x.Dim(2)
	o, k := w.Dim(1), w.Dim(2)
	dx, dw, db = New(c, h, wd), New(w.Shape...), New(o)
	ow := wd * k
	parallel(c, func(ci int) {
		src := x.Data[(ci*h)*wd : (ci+1)*h*wd]
		dst := dx.Data[(ci*h)*wd : (ci+1)*h*wd]
		for oi := range o {
			for i := range k {
				for j := range k {
					wv := w.Data[((ci*o+oi)*k+i)*k+j]
					var g float32
					for yy := range h {
						d := dy.Data[(oi*h*k+yy*k+i)*ow+j:]
						s := src[yy*wd : (yy+1)*wd]
						t := dst[yy*wd : (yy+1)*wd]
						for xx := range wd {
							v := d[xx*k]
							t[xx] += wv * v
							g += s[xx] * v
						}
					}
					dw.Data[((ci*o+oi)*k+i)*k+j] = g
				}
			}
		}
	})
	n := h * k * ow
	for oi := range o {
		var s float32
		for _, v := range dy.Data[oi*n : (oi+1)*n] {
			s += v
		}
		db.Data[oi] = s
	}
	return dx, dw, db
}

// ReLUBackward zeroes dy, in place, where ReLU's output y is zero.
func ReLUBackward(y, dy *Tensor) *Tensor {
	for i, v := range y.Data {
		if v <= 0 {
			dy.Data[i] = 0
		}
	}
	return dy
}

// GELUBackward is the gradient of GELU at its input x given dy.
func GELUBackward(x, dy *Tensor) *Tensor {
	dx := New(x.Shape...)
	for i, v := range x.Data {
		f := float64(v)
		cdf := 0.5 * (1 + math.Erf(f/math.Sqrt2))
		pdf := math.Exp(-0.5*f*f) / math.Sqrt(2*math.Pi)
		dx.Data[i] = float32(float64(dy.Data[i]) * (cdf + f*pdf))
	}
	return dx
}

// AddScaledBackward is the gradients of AddScaled(x, y, s) = x + y s (s a
// channel's) given dy: at y and at s (at x it is dy itself).
func AddScaledBackward(y, s, dy *Tensor) (dyIn, ds *Tensor) {
	d := y.Dim(-1)
	dyIn, ds = New(y.Shape...), New(d)
	acc := make([]float64, d)
	for i, v := range y.Data {
		dyIn.Data[i] = dy.Data[i] * s.Data[i%d]
		acc[i%d] += float64(dy.Data[i]) * float64(v)
	}
	for c, v := range acc {
		ds.Data[c] = float32(v)
	}
	return dyIn, ds
}

// AttentionBackward is the gradients of Attention(q, k, v, heads, scale)
// given dy: at q, at k and at v.
func AttentionBackward(q, k, v *Tensor, heads int, scale float32, dy *Tensor) (dq, dk, dv *Tensor) {
	nq, nk, d := q.Dim(0), k.Dim(0), q.Dim(1)
	hd := d / heads
	dq, dk, dv = New(nq, d), New(nk, d), New(nk, d)
	dqa, dka, dva := make([]float64, nq*d), make([]float64, nk*d), make([]float64, nk*d)
	p, dp := make([]float64, nk), make([]float64, nk)
	for h := range heads {
		o := h * hd
		for i := range nq {
			m := math.Inf(-1)
			for j := range nk {
				var s float64
				for c := range hd {
					s += float64(q.Data[i*d+o+c]) * float64(k.Data[j*d+o+c])
				}
				p[j] = s * float64(scale)
				m = max(m, p[j])
			}
			var sum float64
			for j := range nk {
				p[j] = math.Exp(p[j] - m)
				sum += p[j]
			}
			var dot float64
			for j := range nk {
				p[j] /= sum
				var s float64
				for c := range hd {
					s += float64(dy.Data[i*d+o+c]) * float64(v.Data[j*d+o+c])
				}
				dp[j] = s
				dot += p[j] * s
			}
			for j := range nk {
				g := p[j] * (dp[j] - dot) * float64(scale)
				for c := range hd {
					dva[j*d+o+c] += p[j] * float64(dy.Data[i*d+o+c])
					dqa[i*d+o+c] += g * float64(k.Data[j*d+o+c])
					dka[j*d+o+c] += g * float64(q.Data[i*d+o+c])
				}
			}
		}
	}
	for i, x := range dqa {
		dq.Data[i] = float32(x)
	}
	for i, x := range dka {
		dk.Data[i] = float32(x)
	}
	for i, x := range dva {
		dv.Data[i] = float32(x)
	}
	return dq, dk, dv
}

// LinearBackward is the gradients of Linear(x, w, b) given dy, the
// gradient at its output: at x (x's shape), at w and at b.
func LinearBackward(x, w, dy *Tensor) (dx, dw, db *Tensor) {
	in, out := x.Dim(-1), w.Dim(0)
	n := Numel(x.Shape) / in
	dx, dw, db = New(x.Shape...), New(w.Shape...), New(out)
	for p := range n {
		xr, dyr, dxr := x.Data[p*in:(p+1)*in], dy.Data[p*out:(p+1)*out], dx.Data[p*in:(p+1)*in]
		for o, g := range dyr {
			db.Data[o] += g
			wr, dwr := w.Data[o*in:(o+1)*in], dw.Data[o*in:(o+1)*in]
			for i := range in {
				dxr[i] += g * wr[i]
				dwr[i] += g * xr[i]
			}
		}
	}
	return dx, dw, db
}

// LayerNormBackward is the gradients of LayerNorm(x, w, b, eps) given dy:
// at x, at w and at b.
func LayerNormBackward(x, w, dy *Tensor, eps float32) (dx, dw, db *Tensor) {
	d := x.Dim(-1)
	n := len(x.Data) / d
	dx, dw, db = New(x.Shape...), New(d), New(d)
	xhat, dxhat := make([]float64, d), make([]float64, d)
	for r := range n {
		row, g, out := x.Data[r*d:(r+1)*d], dy.Data[r*d:(r+1)*d], dx.Data[r*d:(r+1)*d]
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
		var m1, m2 float64
		for j, v := range row {
			xhat[j] = (float64(v) - mean) * inv
			dxhat[j] = float64(g[j]) * float64(w.Data[j])
			m1 += dxhat[j]
			m2 += dxhat[j] * xhat[j]
			dw.Data[j] += float32(float64(g[j]) * xhat[j])
			db.Data[j] += g[j]
		}
		m1, m2 = m1/float64(d), m2/float64(d)
		for j := range out {
			out[j] = float32(inv * (dxhat[j] - m1 - xhat[j]*m2))
		}
	}
	return dx, dw, db
}

// GroupNormBackward is the gradients of GroupNorm(x, groups, w, b, eps)
// given dy: at x, at w and at b.
func GroupNormBackward(x *Tensor, groups int, w, dy *Tensor, eps float32) (dx, dw, db *Tensor) {
	c := x.Dim(0)
	per := len(x.Data) / c
	cg := c / groups
	dx, dw, db = New(x.Shape...), New(c), New(c)
	for g := range groups {
		lo, hi := g*cg*per, (g+1)*cg*per
		vals, gs := x.Data[lo:hi], dy.Data[lo:hi]
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
		var m1, m2 float64
		for i, v := range vals {
			ci := g*cg + i/per
			xh, dxh := (float64(v)-mean)*inv, float64(gs[i])*float64(w.Data[ci])
			m1 += dxh
			m2 += dxh * xh
			dw.Data[ci] += float32(float64(gs[i]) * xh)
			db.Data[ci] += gs[i]
		}
		m1, m2 = m1/float64(len(vals)), m2/float64(len(vals))
		for i, v := range vals {
			ci := g*cg + i/per
			xh, dxh := (float64(v)-mean)*inv, float64(gs[i])*float64(w.Data[ci])
			dx.Data[lo+i] = float32(inv * (dxh - m1 - xh*m2))
		}
	}
	return dx, dw, db
}

// GEGLUBackward is the gradient of GEGLU(x) at x given dy.
func GEGLUBackward(x, dy *Tensor) *Tensor {
	n, d := dy.Dim(0), dy.Dim(1)
	dx := New(x.Shape...)
	for r := range n {
		src, out, g := x.Data[r*2*d:(r+1)*2*d], dx.Data[r*2*d:(r+1)*2*d], dy.Data[r*d:(r+1)*d]
		for j, dyv := range g {
			a, gv := float64(src[j]), float64(src[d+j])
			cdf := 0.5 * (1 + math.Erf(gv/math.Sqrt2))
			pdf := math.Exp(-0.5*gv*gv) / math.Sqrt(2*math.Pi)
			out[j] = float32(float64(dyv) * gv * cdf)
			out[d+j] = float32(float64(dyv) * a * (cdf + gv*pdf))
		}
	}
	return dx
}

// TemporalAttentionBackward is the gradients of the CPU's
// TemporalAttention(q, peQ, peK, peV, ks, vs, heads, scale) given dy: at
// q and peQ, at peK and peV, and at every frame's keys and values.
func TemporalAttentionBackward(q, peQ, peK, peV *Tensor, ks, vs []*Tensor, heads int, scale float32, dy *Tensor) (dq, dpeQ, dpeK, dpeV *Tensor, dks, dvs []*Tensor) {
	d, c := q.Dim(0), q.Dim(1)
	t := len(ks)
	hd := c / heads
	dq, dpeQ, dpeK, dpeV = New(d, c), New(c), New(t, c), New(t, c)
	dks, dvs = make([]*Tensor, t), make([]*Tensor, t)
	for f := range t {
		dks[f], dvs[f] = New(d, c), New(d, c)
	}
	scores, dr := make([]float64, t), make([]float64, t)
	for p := range d {
		for h := range heads {
			lo := h * hd
			for f := range t {
				var s float64
				for i := lo; i < lo+hd; i++ {
					s += float64(q.Data[p*c+i]+peQ.Data[i]) * float64(ks[f].Data[p*c+i]+peK.Data[f*c+i])
				}
				scores[f] = s * float64(scale)
			}
			mx := scores[0]
			for _, v := range scores {
				mx = max(mx, v)
			}
			var sum float64
			for f, v := range scores {
				scores[f] = math.Exp(v - mx)
				sum += scores[f]
			}
			var sr float64
			for f := range t {
				scores[f] /= sum
				dr[f] = 0
				for i := lo; i < lo+hd; i++ {
					dr[f] += float64(dy.Data[p*c+i]) * float64(vs[f].Data[p*c+i]+peV.Data[f*c+i])
				}
				sr += scores[f] * dr[f]
			}
			for f := range t {
				ds := scores[f] * (dr[f] - sr) * float64(scale)
				for i := lo; i < lo+hd; i++ {
					qi := float64(q.Data[p*c+i] + peQ.Data[i])
					dk, dv := float32(ds*qi), float32(scores[f]*float64(dy.Data[p*c+i]))
					dks[f].Data[p*c+i] += dk
					dpeK.Data[f*c+i] += dk
					dvs[f].Data[p*c+i] += dv
					dpeV.Data[f*c+i] += dv
					dqv := float32(ds * float64(ks[f].Data[p*c+i]+peK.Data[f*c+i]))
					dq.Data[p*c+i] += dqv
					dpeQ.Data[i] += dqv
				}
			}
		}
	}
	return dq, dpeQ, dpeK, dpeV, dks, dvs
}
