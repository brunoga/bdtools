package gpu

import (
	"github.com/brunoga/bdtools/internal/deint"
	"github.com/brunoga/bdtools/internal/mpeg2"
)

// The decoders that give 8-bit 4:2:0 planes (MPEG-2 and VC-1, in Go or on
// VAAPI) give out their pictures through a Deinterlacing: interlaced ones
// deinterlaced, as NVDEC and the ffmpeg path do, and every one as NV12.

// PlanarPicture is a decoded picture in 8-bit 4:2:0 planes.
type PlanarPicture struct {
	Width, Height              int
	Y, Cb, Cr                  []byte
	StrideY, StrideC           int
	PTS                        int64
	Interlaced, TopFieldFirst  bool
	FrameRateNum, FrameRateDen int
	Color                      ColorInfo
}

// Deinterlacing deinterlaces a decoder's pictures and hands them on as
// NV12.
type Deinterlacing struct {
	di      *deint.Deinterlacer
	picture func(*DecodedPicture) error
	uv      []byte
	pool    [][]byte
	held    [2][]byte     // the input buffers of the last two frames given out
	info    PlanarPicture // the latest picture's stream information
}

// NewDeinterlacing gives the pictures taken to picture.
func NewDeinterlacing(picture func(*DecodedPicture) error) *Deinterlacing {
	g := &Deinterlacing{picture: picture}
	g.di = deint.New(g.give)
	return g
}

// Take copies a decoded picture (the decoders reuse their buffers) for the
// deinterlacer, which holds the one after the frame it gives out.
func (g *Deinterlacing) Take(p *PlanarPicture) error {
	g.info = *p
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
		Interlaced: p.Interlaced, TopFieldFirst: p.TopFieldFirst, Tag: ptsTag{p.PTS, b}})
}

type ptsTag struct {
	pts int64
	buf []byte // the input frame's buffer, free once the frame after it has gone out
}

// give hands a frame on as NV12.
func (g *Deinterlacing) give(f *deint.Frame) error {
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
	// The deinterlacer still holds the frame given out before this one;
	// the one before that is free.
	if g.held[0] != nil {
		g.pool = append(g.pool, g.held[0])
	}
	g.held[0], g.held[1] = g.held[1], tag.buf
	p := &DecodedPicture{Width: w, Height: h, Depth: 8, Y: y, UV: g.uv, Pitch: pitch, PTS: tag.pts,
		FrameRateNum: g.info.FrameRateNum, FrameRateDen: g.info.FrameRateDen, Color: g.info.Color}
	return g.picture(p)
}

// Flush gives out the frames the deinterlacer holds.
func (g *Deinterlacing) Flush() error { return g.di.Flush() }

// MPEG2Picture is an MPEG-2 decoder's picture as a PlanarPicture.
func MPEG2Picture(p *mpeg2.Picture) *PlanarPicture {
	return &PlanarPicture{Width: p.Width, Height: p.Height, Y: p.Y, Cb: p.Cb, Cr: p.Cr,
		StrideY: p.StrideY, StrideC: p.StrideC, PTS: p.PTS, Interlaced: !p.Progressive, TopFieldFirst: p.TopFieldFirst,
		FrameRateNum: p.FrameRateNum, FrameRateDen: p.FrameRateDen,
		Color: ColorInfo{Primaries: p.Primaries, Transfer: p.Transfer, Matrix: p.Matrix}}
}
