package nn

import "github.com/brunoga/bdtools/internal/cuda"

// The picture ops: a decoded picture made into the depth network's input
// and its refinement's on the GPU (kernels.cu's net_input and
// refine_input), as stereo makes them on the CPU, so that the picture,
// and not the networks' tensors, crosses to the GPU.

// Picture is an 8-bit 4:2:0 picture's planes on the GPU.
type Picture struct {
	buf              *Tensor
	w, h             int
	strideY, strideC int
	cb, cr           int // the planes' byte offsets (Y's is 0)
}

// Width is the picture's.
func (p *Picture) Width() int { return p.w }

// UploadPicture puts a w x h picture's planes on the GPU (until Release):
// y's rows strideY apart, cb's and cr's strideC.
func (g *GPU) UploadPicture(y, cb, cr []byte, w, h, strideY, strideC int) *Picture {
	n := len(y) + len(cb) + len(cr)
	p := &Picture{buf: g.tensor((n + 3) / 4), w: w, h: h, strideY: strideY, strideC: strideC, cb: len(y), cr: len(y) + len(cb)}
	for _, pl := range []struct {
		b   []byte
		off int
	}{{y, 0}, {cb, p.cb}, {cr, p.cr}} {
		if g.err == nil {
			g.fail(g.c.UploadBytes(p.buf.dev.a.buf, pl.off, pl.b))
		}
	}
	return p
}

// NetInput is the picture's rows [top, top+ah) as the depth network takes
// them: RGB (BT.709, limited range), resized bilinearly to nw x nh,
// normalised by ImageNet's mean and deviation, [3, nh, nw].
func (g *GPU) NetInput(p *Picture, top, ah, nw, nh int) *Tensor {
	x := g.tensor(3, nh, nw)
	gr, bl := each(nw * nh)
	base := ptr(p.buf)
	g.launch("net_input", gr, bl, 0, p.w, p.h, top, ah, nw, nh, base, p.strideY,
		base+uintptr(p.cb), base+uintptr(p.cr), p.strideC, ptr(x))
	return x
}

// Moments is the sum of x's values and that of their squares (two
// float64s, in a tensor of four float32s): x's rows rows of cols values,
// stride apart.
func (g *GPU) Moments(x *Tensor, rows, cols, stride int) *Tensor {
	m := g.tensor(4)
	g.launch("moments", cuda.Dim{X: 1}, cuda.Dim{X: 1024}, 0, rows, cols, stride, ptr(x), ptr(m))
	return m
}

// RefineInput is the refinement's input, [4, ph, pw], for the picture's
// active rows from top at half size (w x h): their Y (2x2 averages), Cb
// and Cr, and the depth d [w x h] standardised by its moments mom
// (Moments'); the sides padded to pw x ph by repeating the last row and
// column.
func (g *GPU) RefineInput(p *Picture, top int, d, mom *Tensor, w, h, pw, ph int) *Tensor {
	x := g.tensor(4, ph, pw)
	gr, bl := each(pw * ph)
	base := ptr(p.buf)
	co := uintptr(top / 2 * p.strideC)
	g.launch("refine_input", gr, bl, 0, w, h, pw, ph, base+uintptr(top*p.strideY), p.strideY,
		base+uintptr(p.cb)+co, base+uintptr(p.cr)+co, p.strideC, ptr(d), ptr(mom), ptr(x))
	return x
}

// Renorm is out (w x h of rows pw apart, its moments got) put back to the
// mean and deviation of the moments want: [1, h, w].
func (g *GPU) Renorm(out *Tensor, w, h, pw int, want, got *Tensor) *Tensor {
	d := g.tensor(1, h, w)
	gr, bl := each(w * h)
	g.launch("renorm", gr, bl, 0, w, h, pw, ptr(out), ptr(want), ptr(got), ptr(d))
	return d
}

// Disparity is the disparity of a w x h picture from the depth up of its
// rows [top, bottom) (w a row): a times the depth plus b, kept within lim
// either way; the other rows 0. [h, w].
func (g *GPU) Disparity(up *Tensor, w, h, top, bottom int, a, b, lim float32) *Tensor {
	d := g.tensor(h, w)
	gr, bl := each(w * h)
	g.launch("disparity", gr, bl, 0, w, h, top, bottom, a, b, lim, ptr(up), ptr(d))
	return d
}

// Eyes renders the picture's two eyes for its disparity d [h, w], as
// stereo.Pair does (gap its stereo.gapPixels): EyesSize bytes, the left
// eye's Y, Cb and Cr planes then the right's, their rows w and (w+1)/2
// apart.
func (g *GPU) Eyes(p *Picture, d *Tensor, gap float32) *Tensor {
	n := EyesSize(p.w, p.h)
	out := g.tensor((n + 3) / 4)
	rows := 2 * (p.h + 2*((p.h+1)/2))
	z, v := g.tensor(rows, p.w), g.tensor(rows, p.w)
	base := ptr(p.buf)
	g.launch("eyes", cuda.Dim{X: (rows + 63) / 64}, cuda.Dim{X: 64}, 0, p.w, p.h, base, p.strideY,
		base+uintptr(p.cb), base+uintptr(p.cr), p.strideC, ptr(d), gap, ptr(out), ptr(z), ptr(v))
	return out
}

// EyesSize is the bytes of a w x h picture's two eyes (Eyes').
func EyesSize(w, h int) int {
	cw, ch := (w+1)/2, (h+1)/2
	return 2 * (w*h + 2*cw*ch)
}

// DownloadBytes copies the start of the GPU's tensor t to dst (waiting
// for the ops before to have run).
func (g *GPU) DownloadBytes(dst []byte, t *Tensor) error {
	if g.err != nil {
		return g.err
	}
	if err := g.c.Sync(); err != nil {
		g.fail(err)
		return err
	}
	return g.c.DownloadBytes(dst, t.dev.a.buf)
}
