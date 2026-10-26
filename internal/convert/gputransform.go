package convert

import (
	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/internal/nn"
)

// gpuTransform does a transform's scaling and tone mapping on a CUDA GPU
// (nn's Scale and ToneMap): the same pictures as the CPU's scaler and
// toneMapper (the tone mapping's arithmetic rounded op by op as theirs),
// the CPU left for the rest of a conversion. A 4K HDR film made 1080p and
// SDR (as --to-3d does) spent most of its CPU time there.
type gpuTransform struct {
	g              *nn.GPU
	lx, ly, cx, cy *nn.Taps
	luts           []float32
	buf            []byte
	out            gpu.DecodedPicture
}

// newGPUTransform readies t's scaling and tone mapping on the GPU, or
// gives nil when there is no CUDA GPU.
func newGPUTransform(t *picTransform) *gpuTransform {
	g, err := nn.NewGPU()
	if err != nil {
		return nil
	}
	x := &gpuTransform{g: g}
	if s := t.sc; s != nil {
		up := func(f *filterTaps) *nn.Taps { return g.UploadTaps(f.first, f.w, f.n) }
		x.lx, x.ly, x.cx, x.cy = up(&s.lx), up(&s.ly), up(&s.cx), up(&s.cy)
	}
	return x
}

// apply scales and tone maps src as t says, into the transform's own
// picture, valid until the next call.
func (x *gpuTransform) apply(t *picTransform, src *gpu.DecodedPicture) (*gpu.DecodedPicture, error) {
	g := x.g
	defer g.Release()
	p := g.UploadPlanes(src.Y, src.UV, src.Width, src.Height, src.Pitch, src.Depth)
	color := src.Color
	if s := t.sc; s != nil {
		p = g.Scale(p, s.outW, s.outH, x.lx, x.ly, x.cx, x.cy)
	}
	if tm := t.tm; tm != nil {
		x.luts = append(append(append(x.luts[:0], tm.toLight[:]...), tm.curve[:]...), tm.gamma[:]...)
		scale := float32(int(1) << (src.Depth - 8))
		p = g.ToneMap(p, x.luts, scale, src.Color.FullRange, tm.hlg)
		color = gpu.ColorInfo{Primaries: 1, Transfer: 1, Matrix: 1}
	}
	if n := p.Size(); len(x.buf) < n {
		x.buf = make([]byte, n)
	}
	if err := g.DownloadPlanes(x.buf, p); err != nil {
		return nil, err
	}
	o := &x.out
	o.Width, o.Height, o.Depth, o.Pitch = p.W, p.H, p.Depth, p.Pitch
	o.Y, o.UV = x.buf[:p.H*p.Pitch], x.buf[p.H*p.Pitch:p.Size()]
	o.PTS, o.Color, o.FrameRateNum, o.FrameRateDen = src.PTS, color, src.FrameRateNum, src.FrameRateDen
	return o, nil
}

// what says what of t the GPU does, for the report.
func (x *gpuTransform) what(t *picTransform) string {
	switch {
	case t.sc != nil && t.tm != nil:
		return "the scaling and tone mapping"
	case t.sc != nil:
		return "the scaling"
	}
	return "the tone mapping"
}

func (x *gpuTransform) close() { x.g.Close() }
