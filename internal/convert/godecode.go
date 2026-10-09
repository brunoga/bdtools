package convert

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/internal/hevc"
	"github.com/brunoga/bdtools/internal/mpeg2"
	"github.com/brunoga/bdtools/internal/vc1"
)

// The decoders written here, as gpu.Decoders: MPEG-2 and VC-1 (with
// interlaced pictures deinterlaced, as NVDEC and the ffmpeg path do), and
// HEVC.

type goMPEG2 struct {
	d *mpeg2.Decoder
	*gpu.Deinterlacing
}

func openGoMPEG2(_ gpu.DecodeConfig, picture func(*gpu.DecodedPicture) error) (gpu.Decoder, error) {
	return &goMPEG2{d: mpeg2.New(), Deinterlacing: gpu.NewDeinterlacing(picture)}, nil
}

func (g *goMPEG2) takeMPEG2(p *mpeg2.Picture) error { return g.Take(gpu.MPEG2Picture(p)) }

func (g *goMPEG2) Decode(au []byte, pts int64) error { return g.d.Decode(au, pts, g.takeMPEG2) }

func (g *goMPEG2) Flush() error {
	if err := g.d.Flush(g.takeMPEG2); err != nil {
		return err
	}
	return g.Deinterlacing.Flush()
}

func (g *goMPEG2) Close() error { return nil }

type goVC1 struct {
	d *vc1.Decoder
	*gpu.Deinterlacing
}

func openGoVC1(_ gpu.DecodeConfig, picture func(*gpu.DecodedPicture) error) (gpu.Decoder, error) {
	return &goVC1{d: vc1.New(), Deinterlacing: gpu.NewDeinterlacing(picture)}, nil
}

func (g *goVC1) takeVC1(p *vc1.Picture) error {
	return g.Take(&gpu.PlanarPicture{Width: p.Width, Height: p.Height, Y: p.Y, Cb: p.Cb, Cr: p.Cr,
		StrideY: p.StrideY, StrideC: p.StrideC, PTS: p.PTS, Interlaced: p.Interlaced, TopFieldFirst: p.TopFieldFirst,
		FrameRateNum: p.FrameRateNum, FrameRateDen: p.FrameRateDen,
		Color: gpu.ColorInfo{Primaries: p.Primaries, Transfer: p.Transfer, Matrix: p.Matrix}})
}

func (g *goVC1) Decode(au []byte, pts int64) error { return g.d.Decode(au, pts, g.takeVC1) }

func (g *goVC1) Flush() error {
	if err := g.d.Flush(g.takeVC1); err != nil {
		return err
	}
	return g.Deinterlacing.Flush()
}

func (g *goVC1) Close() error { return nil }

// goHEVC is the HEVC decoder here. Blu-ray HEVC is progressive, so its
// pictures go straight out: NV12 at 8 bits, P010 above. A stream it does
// not decode (the format range extensions) goes to fallback from there.
type goHEVC struct {
	d        *hevc.Decoder
	picture  func(*gpu.DecodedPicture) error
	y, uv    []byte
	fallback func() (gpu.Decoder, error)
	other    gpu.Decoder
}

func openGoHEVC(picture func(*gpu.DecodedPicture) error, fallback func() (gpu.Decoder, error)) *goHEVC {
	return &goHEVC{d: hevc.New(), picture: picture, fallback: fallback}
}

func (g *goHEVC) take(p *hevc.Picture) error {
	w, h := p.Width, p.Height
	ch := (h + 1) / 2
	bps, depth := 1, 8
	if p.BitDepth > 8 {
		bps, depth = 2, 10
	}
	pitch := (w + w&1) * bps
	if len(g.y) != pitch*h || len(g.uv) != pitch*ch {
		g.y, g.uv = make([]byte, pitch*h), make([]byte, pitch*ch)
	}
	// In bands of rows, a 4K picture being a few dozen megabytes to move.
	inBands(h, func(r0, r1 int) { p.Pack(g.y, g.uv, pitch, r0, r1) })
	return g.picture(&gpu.DecodedPicture{Width: w, Height: h, Depth: depth, Y: g.y, UV: g.uv, Pitch: pitch, PTS: p.PTS,
		FrameRateNum: p.FrameRateNum, FrameRateDen: p.FrameRateDen,
		Color: gpu.ColorInfo{Primaries: p.Primaries, Transfer: p.Transfer, Matrix: p.Matrix, FullRange: p.FullRange}})
}

func (g *goHEVC) Decode(au []byte, pts int64) error {
	if g.other != nil {
		return g.other.Decode(au, pts)
	}
	err := g.d.Decode(au, pts, g.take)
	if !hevc.IsUnsupported(err) || g.fallback == nil {
		return err
	}
	// The stream changes to one not decoded here at an IRAP access unit
	// (its first, as a rule), which the fallback can start from.
	if err := g.d.Flush(g.take); err != nil {
		return err
	}
	other, ferr := g.fallback()
	if ferr != nil {
		return fmt.Errorf("%w, and %w", err, ferr)
	}
	g.other = other
	return other.Decode(au, pts)
}

func (g *goHEVC) Flush() error {
	if g.other != nil {
		return g.other.Flush()
	}
	return g.d.Flush(g.take)
}

func (g *goHEVC) Close() error {
	if g.other != nil {
		return g.other.Close()
	}
	return nil
}

// inBands runs fn over rows [0, h) in bands of 64 (even, for the chroma
// rows), on as many goroutines as there are processors.
func inBands(h int, fn func(r0, r1 int)) {
	const band = 64
	n := (h + band - 1) / band
	workers := min(runtime.GOMAXPROCS(0), n)
	if workers < 2 {
		fn(0, h)
		return
	}
	var next atomic.Int32
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := int(next.Add(1)) - 1; b < n; b = int(next.Add(1)) - 1 {
				fn(b*band, min(h, (b+1)*band))
			}
		}()
	}
	wg.Wait()
}
