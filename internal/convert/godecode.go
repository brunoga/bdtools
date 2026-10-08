package convert

import (
	"github.com/brunoga/bdtools/internal/deint"
	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/internal/mpeg2"
)

// The decoders written here, as gpu.Decoders: MPEG-2 (with interlaced
// pictures deinterlaced, as NVDEC and the ffmpeg path do).

type goMPEG2 struct {
	d       *mpeg2.Decoder
	di      *deint.Deinterlacer
	picture func(*gpu.DecodedPicture) error
	uv      []byte
	pool    [][]byte
	info    *mpeg2.Picture // the latest picture's stream information
}

func openGoMPEG2(_ gpu.DecodeConfig, picture func(*gpu.DecodedPicture) error) (gpu.Decoder, error) {
	g := &goMPEG2{d: mpeg2.New(), picture: picture}
	g.di = deint.New(g.give)
	return g, nil
}

// take copies a decoded picture (the decoder reuses its buffers) for the
// deinterlacer, which holds the one after the frame it gives out.
func (g *goMPEG2) take(p *mpeg2.Picture) error {
	g.info = p
	w, h := p.Width, p.Height
	cw, ch := (w+1)/2, (h+1)/2
	var b []byte
	if n := len(g.pool); n > 0 && len(g.pool[n-1]) == w*h+2*cw*ch {
		b, g.pool = g.pool[n-1], g.pool[:n-1]
	} else {
		b = make([]byte, w*h+2*cw*ch)
	}
	for y := range h {
		copy(b[y*w:y*w+w], p.Y[y*p.StrideY:])
	}
	cb, cr := b[w*h:w*h+cw*ch], b[w*h+cw*ch:]
	for y := range ch {
		copy(cb[y*cw:y*cw+cw], p.Cb[y*p.StrideC:])
		copy(cr[y*cw:y*cw+cw], p.Cr[y*p.StrideC:])
	}
	return g.di.Push(&deint.Frame{Planes: [3][]byte{b[:w*h], cb, cr}, Strides: [3]int{w, cw, cw}, Width: w, Height: h,
		Interlaced: !p.Progressive, TopFieldFirst: p.TopFieldFirst, Tag: ptsTag{p.PTS, b}})
}

type ptsTag struct {
	pts int64
	buf []byte // the input frame's buffer, free once the frame after it has gone out
}

// give hands a frame on as NV12.
func (g *goMPEG2) give(f *deint.Frame) error {
	w, h := f.Width, f.Height
	cw, ch := (w+1)/2, (h+1)/2
	pitch := w + w&1
	y := make([]byte, pitch*h)
	for r := range h {
		copy(y[r*pitch:r*pitch+w], f.Planes[0][r*f.Strides[0]:])
	}
	if len(g.uv) != pitch*ch {
		g.uv = make([]byte, pitch*ch)
	}
	for r := range ch {
		cb, cr := f.Planes[1][r*f.Strides[1]:], f.Planes[2][r*f.Strides[2]:]
		row := g.uv[r*pitch:]
		for x := range cw {
			row[2*x], row[2*x+1] = cb[x], cr[x]
		}
	}
	tag := f.Tag.(ptsTag) //nolint:forcetypeassert // ours
	p := &gpu.DecodedPicture{Width: w, Height: h, Depth: 8, Y: y, UV: g.uv, Pitch: pitch, PTS: tag.pts}
	if i := g.info; i != nil {
		p.FrameRateNum, p.FrameRateDen = i.FrameRateNum, i.FrameRateDen
		p.Color = gpu.ColorInfo{Primaries: i.Primaries, Transfer: i.Transfer, Matrix: i.Matrix}
	}
	return g.picture(p)
}

func (g *goMPEG2) Decode(au []byte, pts int64) error { return g.d.Decode(au, pts, g.take) }

func (g *goMPEG2) Flush() error {
	if err := g.d.Flush(g.take); err != nil {
		return err
	}
	return g.di.Flush()
}

func (g *goMPEG2) Close() error { return nil }
