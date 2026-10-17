package ocr

// pass is one line's forward pass through the network, holding what the
// backward pass needs.
type pass struct {
	w int // the picture's width
	// For each convolution layer: its input (C x H x W), the output after
	// ReLU before pooling, and where each pooled value came from.
	in      [][]float32
	act     [][]float32
	argmax  [][]int32
	hs, ws  []int // each layer's input size
	t       int   // steps: the width over 4
	seq     []float32
	gates   [2][]float32 // t x 4*hidden, after their activations
	cells   [2][]float32 // t x hidden
	hiddens [2][]float32 // t x hidden
	hcat    []float32    // t x 2*hidden
	logits  []float32    // t x classes
}

// forward runs a picture (Height x w, w a multiple of 4, values 0 to 1)
// through the network.
func (m *Model) forward(img []float32, w int) *pass {
	p := &pass{w: w}
	x, h, wd := img, Height, w
	for i, c := range convs {
		p.in = append(p.in, x)
		p.hs, p.ws = append(p.hs, h), append(p.ws, wd)
		n := h * wd
		cols := im2col(x, c.in, h, wd)
		out := make([]float32, c.out*n)
		wts := m.Conv[i][:c.out*c.in*9]
		bias := m.Conv[i][c.out*c.in*9:]
		for o := range c.out {
			row := out[o*n : (o+1)*n]
			for j := range row {
				row[j] = bias[o]
			}
		}
		gemm(out, wts, cols, c.out, c.in*9, n)
		for j, v := range out {
			if v < 0 {
				out[j] = 0
			}
		}
		p.act = append(p.act, out)
		pooled, arg := maxPool(out, c.out, h, wd, c.ph, c.pw)
		p.argmax = append(p.argmax, arg)
		x, h, wd = pooled, h/c.ph, wd/c.pw
	}
	// The columns, a step each: channel by row.
	p.t = wd
	p.seq = make([]float32, p.t*features)
	for c := range 64 {
		for r := range 2 {
			for t := range p.t {
				p.seq[t*features+c*2+r] = x[(c*h+r)*wd+t]
			}
		}
	}
	p.hcat = make([]float32, p.t*2*hidden)
	for d := range 2 {
		m.lstmForward(p, d)
	}
	// The linear layer.
	wo := m.Out[:classes*2*hidden]
	bo := m.Out[classes*2*hidden:]
	p.logits = make([]float32, p.t*classes)
	for t := range p.t {
		copy(p.logits[t*classes:(t+1)*classes], bo)
	}
	gemmNT(p.logits, p.hcat, wo, p.t, 2*hidden, classes)
	return p
}

// im2col lays out a C x H x W input's 3x3 neighbourhoods (zero beyond the
// edges) as C*9 rows of H*W.
func im2col(x []float32, c, h, w int) []float32 {
	n := h * w
	cols := make([]float32, c*9*n)
	for ch := range c {
		src := x[ch*n : (ch+1)*n]
		for ky := range 3 {
			for kx := range 3 {
				row := cols[(ch*9+ky*3+kx)*n : (ch*9+ky*3+kx+1)*n]
				for y := range h {
					sy := y + ky - 1
					if sy < 0 || sy >= h {
						continue
					}
					dst := row[y*w : (y+1)*w]
					s := src[sy*w : (sy+1)*w]
					switch kx {
					case 0:
						copy(dst[1:], s[:w-1])
					case 1:
						copy(dst, s)
					case 2:
						copy(dst[:w-1], s[1:])
					}
				}
			}
		}
	}
	return cols
}

// col2im adds im2col's rows back onto a C x H x W gradient.
func col2im(dx, cols []float32, c, h, w int) {
	n := h * w
	for ch := range c {
		dst := dx[ch*n : (ch+1)*n]
		for ky := range 3 {
			for kx := range 3 {
				row := cols[(ch*9+ky*3+kx)*n : (ch*9+ky*3+kx+1)*n]
				for y := range h {
					sy := y + ky - 1
					if sy < 0 || sy >= h {
						continue
					}
					src := row[y*w : (y+1)*w]
					d := dst[sy*w : (sy+1)*w]
					switch kx {
					case 0:
						axpy(d[:w-1], src[1:], 1)
					case 1:
						axpy(d, src, 1)
					case 2:
						axpy(d[1:], src[:w-1], 1)
					}
				}
			}
		}
	}
}

// maxPool pools a C x H x W input over ph x pw windows, noting the input
// index each output came from.
func maxPool(x []float32, c, h, w, ph, pw int) ([]float32, []int32) {
	oh, ow := h/ph, w/pw
	out := make([]float32, c*oh*ow)
	arg := make([]int32, c*oh*ow)
	for ch := range c {
		for y := range oh {
			for xx := range ow {
				best, at := float32(-1), 0
				for dy := range ph {
					for dx := range pw {
						i := (ch*h+y*ph+dy)*w + xx*pw + dx
						if x[i] > best {
							best, at = x[i], i
						}
					}
				}
				o := (ch*oh+y)*ow + xx
				out[o], arg[o] = best, int32(at) //nolint:gosec // an index
			}
		}
	}
	return out, arg
}

// lstmForward runs direction d (0 forward, 1 backward) over the steps.
func (m *Model) lstmForward(p *pass, d int) {
	w := m.LSTM[d]
	wx := w[:4*hidden*features]
	wh := w[4*hidden*features : 4*hidden*(features+hidden)]
	b := w[4*hidden*(features+hidden):]
	T := p.t
	xw := make([]float32, 4*hidden*T) // 4*hidden x T
	gemmNT(xw, wx, p.seq, 4*hidden, features, T)
	gates := make([]float32, T*4*hidden)
	cells := make([]float32, T*hidden)
	hs := make([]float32, T*hidden)
	z := make([]float32, 4*hidden)
	prevH := make([]float32, hidden)
	prevC := make([]float32, hidden)
	for k := range T {
		t := k
		if d == 1 {
			t = T - 1 - k
		}
		for j := range 4 * hidden {
			z[j] = xw[j*T+t] + b[j] + dot(wh[j*hidden:(j+1)*hidden], prevH)
		}
		g := gates[t*4*hidden : (t+1)*4*hidden]
		c := cells[t*hidden : (t+1)*hidden]
		h := hs[t*hidden : (t+1)*hidden]
		for j := range hidden {
			i, f, gg, o := sigmoid(z[j]), sigmoid(z[hidden+j]), tanh(z[2*hidden+j]), sigmoid(z[3*hidden+j])
			g[j], g[hidden+j], g[2*hidden+j], g[3*hidden+j] = i, f, gg, o
			c[j] = f*prevC[j] + i*gg
			h[j] = o * tanh(c[j])
		}
		prevH, prevC = h, c
		copy(p.hcat[t*2*hidden+d*hidden:t*2*hidden+(d+1)*hidden], h)
	}
	p.gates[d], p.cells[d], p.hiddens[d] = gates, cells, hs
}

// backward adds the gradients of the loss to grad, given its gradient by
// the logits (t x classes).
func (m *Model) backward(p *pass, dlogits []float32, grad *Model) {
	T := p.t
	// The linear layer.
	wo := m.Out[:classes*2*hidden]
	gemmTN(grad.Out[:classes*2*hidden], dlogits, p.hcat, classes, T, 2*hidden)
	gb := grad.Out[classes*2*hidden:]
	for t := range T {
		axpy(gb, dlogits[t*classes:(t+1)*classes], 1)
	}
	dhcat := make([]float32, T*2*hidden)
	gemm(dhcat, dlogits, wo, T, classes, 2*hidden)
	dseq := make([]float32, T*features)
	for d := range 2 {
		m.lstmBackward(p, d, dhcat, dseq, grad)
	}
	// Back through the convolutions.
	n := len(convs) - 1
	h, wd := p.hs[n]/convs[n].ph, p.ws[n]/convs[n].pw
	dy := make([]float32, 64*h*wd)
	for c := range 64 {
		for r := range 2 {
			for t := range T {
				dy[(c*h+r)*wd+t] = dseq[t*features+c*2+r]
			}
		}
	}
	for i := n; i >= 0; i-- {
		c := convs[i]
		h, wd := p.hs[i], p.ws[i]
		nn := h * wd
		dout := make([]float32, c.out*nn)
		for o, at := range p.argmax[i] {
			dout[at] += dy[o]
		}
		for j, v := range p.act[i] {
			if v <= 0 {
				dout[j] = 0
			}
		}
		cols := im2col(p.in[i], c.in, h, wd)
		gw := grad.Conv[i][:c.out*c.in*9]
		gemmNT(gw, dout, cols, c.out, nn, c.in*9)
		gbias := grad.Conv[i][c.out*c.in*9:]
		for o := range c.out {
			var s float32
			for _, v := range dout[o*nn : (o+1)*nn] {
				s += v
			}
			gbias[o] += s
		}
		if i == 0 {
			break
		}
		dcols := make([]float32, c.in*9*nn)
		gemmTN(dcols, m.Conv[i][:c.out*c.in*9], dout, c.in*9, c.out, nn)
		dy = make([]float32, c.in*nn)
		col2im(dy, dcols, c.in, h, wd)
	}
}

// lstmBackward runs direction d back through time.
func (m *Model) lstmBackward(p *pass, d int, dhcat, dseq []float32, grad *Model) {
	T := p.t
	w := m.LSTM[d]
	wx := w[:4*hidden*features]
	wh := w[4*hidden*features : 4*hidden*(features+hidden)]
	gw := grad.LSTM[d]
	gwx := gw[:4*hidden*features]
	gwh := gw[4*hidden*features : 4*hidden*(features+hidden)]
	gb := gw[4*hidden*(features+hidden):]
	dxw := make([]float32, 4*hidden*T)
	dhNext := make([]float32, hidden)
	dcNext := make([]float32, hidden)
	dz := make([]float32, 4*hidden)
	zero := make([]float32, hidden)
	for k := T - 1; k >= 0; k-- {
		t := k
		if d == 1 {
			t = T - 1 - k
		}
		prevT := t - 1
		if d == 1 {
			prevT = t + 1
		}
		prevH, prevC := zero, zero
		if k > 0 {
			prevH = p.hiddens[d][prevT*hidden : (prevT+1)*hidden]
			prevC = p.cells[d][prevT*hidden : (prevT+1)*hidden]
		}
		g := p.gates[d][t*4*hidden : (t+1)*4*hidden]
		c := p.cells[d][t*hidden : (t+1)*hidden]
		for j := range hidden {
			dh := dhcat[t*2*hidden+d*hidden+j] + dhNext[j]
			i, f, gg, o := g[j], g[hidden+j], g[2*hidden+j], g[3*hidden+j]
			tc := tanh(c[j])
			do := dh * tc
			dc := dh*o*(1-tc*tc) + dcNext[j]
			di, dg, df := dc*gg, dc*i, dc*prevC[j]
			dcNext[j] = dc * f
			dz[j] = di * i * (1 - i)
			dz[hidden+j] = df * f * (1 - f)
			dz[2*hidden+j] = dg * (1 - gg*gg)
			dz[3*hidden+j] = do * o * (1 - o)
		}
		for j := range 4 * hidden {
			dxw[j*T+t] = dz[j]
			gb[j] += dz[j]
			axpy(gwh[j*hidden:(j+1)*hidden], prevH, dz[j])
		}
		clear(dhNext)
		for j := range 4 * hidden {
			axpy(dhNext, wh[j*hidden:(j+1)*hidden], dz[j])
		}
	}
	// The input weights, and the gradient by the inputs.
	gemm(gwx, dxw, p.seq, 4*hidden, T, features)
	gemmTN(dseq, dxw, wx, T, 4*hidden, features)
}
