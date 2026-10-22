package nn

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
