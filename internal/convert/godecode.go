package convert

import (
	"github.com/brunoga/bdtools/internal/deint"
	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/internal/mpeg2"
	"github.com/brunoga/bdtools/internal/vc1"
)

// The decoders written here, as gpu.Decoders: MPEG-2 and VC-1 (with
// interlaced pictures deinterlaced, as NVDEC and the ffmpeg path do).

// planarPicture is a decoded picture as the decoders here give it: 8-bit
// 4:2:0 planes.
type planarPicture struct {
	width, height int
	y, cb, cr     []byte
	strideY       int
	strideC       int
	pts           int64
	interlaced    bool
	tff           bool
	rateNum       int
	rateDen       int
	color         gpu.ColorInfo
}

// goPictures deinterlaces a decoder's pictures and hands them on as NV12.
type goPictures struct {
	di      *deint.Deinterlacer
	picture func(*gpu.DecodedPicture) error
	uv      []byte
	pool    [][]byte
	held    [2][]byte     // the input buffers of the last two frames given out
	info    planarPicture // the latest picture's stream information
}

func newGoPictures(picture func(*gpu.DecodedPicture) error) *goPictures {
	g := &goPictures{picture: picture}
	g.di = deint.New(g.give)
	return g
}

// take copies a decoded picture (the decoders reuse their buffers) for the
// deinterlacer, which holds the one after the frame it gives out.
func (g *goPictures) take(p *planarPicture) error {
	g.info = *p
	w, h := p.width, p.height
	cw, ch := (w+1)/2, (h+1)/2
	var b []byte
	if n := len(g.pool); n > 0 && len(g.pool[n-1]) == w*h+2*cw*ch {
		b, g.pool = g.pool[n-1], g.pool[:n-1]
	} else {
		b = make([]byte, w*h+2*cw*ch)
	}
	for y := range h {
		copy(b[y*w:y*w+w], p.y[y*p.strideY:])
	}
	cb, cr := b[w*h:w*h+cw*ch], b[w*h+cw*ch:]
	for y := range ch {
		copy(cb[y*cw:y*cw+cw], p.cb[y*p.strideC:])
		copy(cr[y*cw:y*cw+cw], p.cr[y*p.strideC:])
	}
	return g.di.Push(&deint.Frame{Planes: [3][]byte{b[:w*h], cb, cr}, Strides: [3]int{w, cw, cw}, Width: w, Height: h,
		Interlaced: p.interlaced, TopFieldFirst: p.tff, Tag: ptsTag{p.pts, b}})
}

type ptsTag struct {
	pts int64
	buf []byte // the input frame's buffer, free once the frame after it has gone out
}

// give hands a frame on as NV12.
func (g *goPictures) give(f *deint.Frame) error {
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
	p := &gpu.DecodedPicture{Width: w, Height: h, Depth: 8, Y: y, UV: g.uv, Pitch: pitch, PTS: tag.pts,
		FrameRateNum: g.info.rateNum, FrameRateDen: g.info.rateDen, Color: g.info.color}
	return g.picture(p)
}

func (g *goPictures) flush() error { return g.di.Flush() }

type goMPEG2 struct {
	d *mpeg2.Decoder
	*goPictures
}

func openGoMPEG2(_ gpu.DecodeConfig, picture func(*gpu.DecodedPicture) error) (gpu.Decoder, error) {
	return &goMPEG2{d: mpeg2.New(), goPictures: newGoPictures(picture)}, nil
}

func (g *goMPEG2) takeMPEG2(p *mpeg2.Picture) error {
	return g.take(&planarPicture{width: p.Width, height: p.Height, y: p.Y, cb: p.Cb, cr: p.Cr,
		strideY: p.StrideY, strideC: p.StrideC, pts: p.PTS, interlaced: !p.Progressive, tff: p.TopFieldFirst,
		rateNum: p.FrameRateNum, rateDen: p.FrameRateDen,
		color: gpu.ColorInfo{Primaries: p.Primaries, Transfer: p.Transfer, Matrix: p.Matrix}})
}

func (g *goMPEG2) Decode(au []byte, pts int64) error { return g.d.Decode(au, pts, g.takeMPEG2) }

func (g *goMPEG2) Flush() error {
	if err := g.d.Flush(g.takeMPEG2); err != nil {
		return err
	}
	return g.flush()
}

func (g *goMPEG2) Close() error { return nil }

type goVC1 struct {
	d *vc1.Decoder
	*goPictures
}

func openGoVC1(_ gpu.DecodeConfig, picture func(*gpu.DecodedPicture) error) (gpu.Decoder, error) {
	return &goVC1{d: vc1.New(), goPictures: newGoPictures(picture)}, nil
}

func (g *goVC1) takeVC1(p *vc1.Picture) error {
	return g.take(&planarPicture{width: p.Width, height: p.Height, y: p.Y, cb: p.Cb, cr: p.Cr,
		strideY: p.StrideY, strideC: p.StrideC, pts: p.PTS, interlaced: p.Interlaced, tff: p.TopFieldFirst,
		rateNum: p.FrameRateNum, rateDen: p.FrameRateDen,
		color: gpu.ColorInfo{Primaries: p.Primaries, Transfer: p.Transfer, Matrix: p.Matrix}})
}

func (g *goVC1) Decode(au []byte, pts int64) error { return g.d.Decode(au, pts, g.takeVC1) }

func (g *goVC1) Flush() error {
	if err := g.d.Flush(g.takeVC1); err != nil {
		return err
	}
	return g.flush()
}

func (g *goVC1) Close() error { return nil }
