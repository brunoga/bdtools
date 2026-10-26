package nn

import "encoding/binary"

// The decoded picture ops: convert's --scale and --tonemap on the GPU
// (kernels.cu's scale_h, scale_v and tonemap), to the values convert's own
// code gives on the CPU.

// Planes is a decoded picture on the GPU as the decoders give it: NV12, or
// P010 when Depth is over 8, Y's H rows then the interleaved chroma's H/2,
// Pitch bytes apart.
type Planes struct {
	buf         *Tensor
	uv          int // the chroma's byte offset
	W, H, Pitch int
	Depth       int
}

func (p *Planes) bps() int {
	if p.Depth > 8 {
		return 2
	}
	return 1
}

// Size is the bytes of the picture's planes, as DownloadPlanes gives them.
func (p *Planes) Size() int { return p.uv + p.H/2*p.Pitch }

// UploadPlanes puts a w x h picture's planes on the GPU (until Release):
// y's rows and uv's pitch bytes apart, from their slices' starts.
func (g *GPU) UploadPlanes(y, uv []byte, w, h, pitch, depth int) *Planes {
	p := &Planes{W: w, H: h, Pitch: pitch, Depth: depth}
	row := w * p.bps()
	ny, nuv := (h-1)*pitch+row, (h/2-1)*pitch+row
	p.uv = h * pitch
	p.buf = g.tensor((p.uv + h/2*pitch + 3) / 4)
	if g.err == nil {
		g.fail(g.c.UploadBytes(p.buf.dev.a.buf, 0, y[:ny]))
	}
	if g.err == nil {
		g.fail(g.c.UploadBytes(p.buf.dev.a.buf, p.uv, uv[:nuv]))
	}
	return p
}

// DownloadPlanes copies p's planes into dst (Size bytes: Y's rows, then
// the chroma's), waiting for the ops before to have run.
func (g *GPU) DownloadPlanes(dst []byte, p *Planes) error {
	return g.DownloadBytes(dst[:p.Size()], p.buf)
}

// Taps is one dimension of a resampling filter, kept on the GPU: for each
// output sample, the first input sample it reads and the weights of the N
// it reads from there.
type Taps struct {
	first, w *Tensor
	N        int
}

// UploadTaps puts a filter on the GPU, kept until Close.
func (g *GPU) UploadTaps(first []int, w []int32, n int) *Taps {
	t := &Taps{N: n}
	up := func(v []int32) *Tensor {
		b := make([]byte, 4*len(v))
		for i, x := range v {
			binary.LittleEndian.PutUint32(b[4*i:], uint32(x))
		}
		d := g.tensor(len(v))
		g.Keep(d)
		if g.err == nil {
			g.fail(g.c.UploadBytes(d.dev.a.buf, 0, b))
		}
		return d
	}
	f := make([]int32, len(first))
	for i, x := range first {
		f[i] = int32(x)
	}
	t.first, t.w = up(f), up(w)
	return t
}

// Scale resamples p to ow x oh: lx and ly the luma's filters, cx and cy
// the chroma's (on chroma samples).
func (g *GPU) Scale(p *Planes, ow, oh int, lx, ly, cx, cy *Taps) *Planes {
	bps := p.bps()
	o := &Planes{W: ow, H: oh, Pitch: ow * bps, Depth: p.Depth}
	o.uv = oh * o.Pitch
	o.buf = g.tensor((o.Size() + 3) / 4)
	src, dst := ptr(p.buf), ptr(o.buf)
	maxV := 1<<p.Depth - 1
	ty, tc := g.tensor(p.H*ow), g.tensor(p.H/2*ow)
	gr, bl := each(p.H * ow)
	g.launch("scale_h", gr, bl, 0, p.H, p.W, 1, ow, src, p.Pitch, bps, ptr(lx.first), ptr(lx.w), lx.N, ptr(ty))
	gr, bl = each(p.H / 2 * ow)
	g.launch("scale_h", gr, bl, 0, p.H/2, p.W/2, 2, ow/2, src+uintptr(p.uv), p.Pitch, bps, ptr(cx.first), ptr(cx.w), cx.N, ptr(tc))
	gr, bl = each(oh * ow)
	g.launch("scale_v", gr, bl, 0, oh, ow, p.H, ptr(ty), ptr(ly.first), ptr(ly.w), ly.N, maxV, bps, dst, o.Pitch)
	gr, bl = each(oh / 2 * ow)
	g.launch("scale_v", gr, bl, 0, oh/2, ow, p.H/2, ptr(tc), ptr(cy.first), ptr(cy.w), cy.N, maxV, bps, dst+uintptr(o.uv), o.Pitch)
	return o
}

// ToneMap is convert's tone mapping of p: luts its toLight, curve and
// gamma tables (4097 values each), scale the code values' over 8 bits',
// full whether p is full range, hlg whether it is HLG (else PQ).
func (g *GPU) ToneMap(p *Planes, luts []float32, scale float32, full, hlg bool) *Planes {
	bps := p.bps()
	o := &Planes{W: p.W, H: p.H, Pitch: p.W * bps, Depth: p.Depth}
	o.uv = p.H * o.Pitch
	o.buf = g.tensor((o.Size() + 3) / 4)
	lt := g.Upload(&Tensor{Shape: []int{len(luts)}, Data: luts})
	flag := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}
	src, dst := ptr(p.buf), ptr(o.buf)
	gr, bl := each(p.H / 2 * (p.W / 2))
	g.launch("tonemap", gr, bl, 0, p.W, p.H, src, src+uintptr(p.uv), p.Pitch, bps, scale, flag(full), flag(hlg),
		ptr(lt), dst, dst+uintptr(o.uv), o.Pitch)
	return o
}
