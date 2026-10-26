package convert

import (
	"fmt"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/internal/hevc"
	"github.com/brunoga/bdtools/internal/mpeg2"
	"github.com/brunoga/bdtools/internal/vc1"
	"github.com/brunoga/bdtools/mvc"
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

// goH264 is the H.264 decoder here for interlaced video (field pictures,
// MBAFF frames), its frames deinterlaced as NVDEC and the ffmpeg path do;
// a stream it does not decode goes to fallback from there.
type goH264 struct {
	d *mvc.Decoder
	*gpu.Deinterlacing
	fallback func() (gpu.Decoder, error)
	other    gpu.Decoder
}

func openGoH264(picture func(*gpu.DecodedPicture) error, fallback func() (gpu.Decoder, error)) *goH264 {
	return &goH264{d: mvc.NewDecoder(mvc.Options{BaseOnly: true}), Deinterlacing: gpu.NewDeinterlacing(picture), fallback: fallback}
}

// drain hands the frames decoded so far to the deinterlacer.
func (g *goH264) drain() error {
	for {
		sf, ok := g.d.NextFrame()
		if !ok {
			return nil
		}
		f := sf.Base
		num, den := g.d.FrameRate()
		pr, tr, mx, full := g.d.ColorInfo()
		err := g.Take(&gpu.PlanarPicture{Width: f.Width, Height: f.Height, Y: f.Y, Cb: f.Cb, Cr: f.Cr,
			StrideY: f.StrideY, StrideC: f.StrideC, PTS: f.PTS, Interlaced: f.Interlaced, TopFieldFirst: f.TopFieldFirst,
			FrameRateNum: num, FrameRateDen: den,
			Color: gpu.ColorInfo{Primaries: pr, Transfer: tr, Matrix: mx, FullRange: full}})
		sf.Release()
		if err != nil {
			return err
		}
	}
}

func (g *goH264) Decode(au []byte, pts int64) error {
	if g.other != nil {
		return g.other.Decode(au, pts)
	}
	err := g.d.DecodeAU(au, pts)
	if mvc.IsUnsupported(err) && g.fallback != nil {
		if err := g.Flush(); err != nil {
			return err
		}
		other, ferr := g.fallback()
		if ferr != nil {
			return fmt.Errorf("%w, and %w", err, ferr)
		}
		g.other = other
		return other.Decode(au, pts)
	}
	if err != nil {
		return err
	}
	return g.drain()
}

func (g *goH264) Flush() error {
	if g.other != nil {
		return g.other.Flush()
	}
	if err := g.d.Flush(); err != nil {
		return err
	}
	if err := g.drain(); err != nil {
		return err
	}
	return g.Deinterlacing.Flush()
}

func (g *goH264) Close() error {
	if g.other != nil {
		return g.other.Close()
	}
	return nil
}

// gpuFallback is a GPU decoder that, failing, hands the stream to a
// decoder here. Before its first picture (a virtual machine's media
// engine, a driver turning the stream down after all) the access units fed
// so far go to it again. After, for H.264 and HEVC (a media engine failing
// a frame mid-stream, as a virtual machine's can), those from the last IDR
// do, after every parameter set seen, the pictures already out dropped.
type gpuFallback struct {
	dec     gpu.Decoder
	open    func() (gpu.Decoder, error) // the decoder here
	codec   gpu.VideoCodec
	report  Reporter
	shown   bool  // a picture has come out
	last    int64 // the last one's PTS
	fed     [][]byte
	fedPTS  []int64
	fromIDR bool     // fed starts at an IDR
	params  [][]byte // the parameter sets seen (H.264, HEVC)
	replace bool     // on the decoder here now
	halted  bool     // a picture's taker returned an error: not the decoder's
}

// maxFallbackAUs is how many access units a GPU decoder may take before
// its first picture and still be replaced: past them it is trusted.
const maxFallbackAUs = 64

// maxRestartAUs is how many access units after an IDR are kept to start
// the decoder here from.
const maxRestartAUs = 600

func (g *gpuFallback) fall(err error) error {
	if g.replace || g.open == nil || g.halted {
		return err
	}
	restart := g.fromIDR && len(g.fed) > 0
	if !restart && (g.shown || len(g.fed) > maxFallbackAUs) {
		return err
	}
	if g.shown {
		g.report.Report("warning: the GPU decoder failed (%v): decoding here from the last IDR instead", err)
	} else {
		g.report.Report("warning: the GPU decoder failed before its first picture (%v): decoding here instead", err)
	}
	_ = g.dec.Close()
	dec, oerr := g.open()
	if oerr != nil {
		return fmt.Errorf("%w (and decoding here: %w)", err, oerr)
	}
	g.dec, g.replace = dec, true
	for i, au := range g.fed {
		if i == 0 && restart {
			au = append(slices.Concat(g.params...), au...)
		}
		if err := g.dec.Decode(au, g.fedPTS[i]); err != nil {
			return err
		}
	}
	g.fed, g.fedPTS, g.params = nil, nil, nil
	return nil
}

func (g *gpuFallback) Decode(au []byte, pts int64) error {
	if !g.replace {
		g.keep(au, pts)
	}
	if err := g.dec.Decode(au, pts); err != nil {
		return g.fall(err)
	}
	return nil
}

// keep keeps an access unit for a decoder here to take over with.
func (g *gpuFallback) keep(au []byte, pts int64) {
	idr := false
	if g.codec == gpu.DecodeH264 || g.codec == gpu.DecodeHEVC {
		for _, nal := range annexBNALs(au) {
			switch t := nalType(g.codec, nal); {
			case isParamSet(g.codec, t):
				if !slices.ContainsFunc(g.params, func(p []byte) bool { return slices.Equal(p[4:], nal) }) {
					g.params = append(g.params, append([]byte{0, 0, 0, 1}, nal...))
				}
			case isIDR(g.codec, t):
				idr = true
			}
		}
	}
	switch {
	case idr && (g.shown || len(g.fed) == 0):
		// a window from here (before any picture, only at the start: the
		// pictures before an IDR would be lost)
		g.fed, g.fedPTS, g.fromIDR = nil, nil, true
	case g.fromIDR && len(g.fed) >= maxRestartAUs:
		g.fed, g.fedPTS, g.fromIDR = nil, nil, false
		return
	case !g.fromIDR && (g.shown || len(g.fed) > maxFallbackAUs):
		return
	}
	g.fed = append(g.fed, append([]byte(nil), au...))
	g.fedPTS = append(g.fedPTS, pts)
}

func (g *gpuFallback) Flush() error {
	if err := g.dec.Flush(); err != nil {
		if err = g.fall(err); err != nil {
			return err
		}
		return g.dec.Flush()
	}
	return nil
}

func (g *gpuFallback) Close() error { return g.dec.Close() }

// annexBNALs are an Annex B access unit's NAL units, start codes left out.
func annexBNALs(au []byte) [][]byte {
	var nals [][]byte
	start := -1
	for i := 0; i+2 < len(au); i++ {
		if au[i] != 0 || au[i+1] != 0 || au[i+2] != 1 {
			continue
		}
		if start >= 0 {
			end := i
			for end > start && au[end-1] == 0 {
				end--
			}
			nals = append(nals, au[start:end])
		}
		start = i + 3
		i += 2
	}
	if start >= 0 && start < len(au) {
		nals = append(nals, au[start:])
	}
	return nals
}

func nalType(codec gpu.VideoCodec, nal []byte) int {
	if len(nal) == 0 {
		return -1
	}
	if codec == gpu.DecodeHEVC {
		return int(nal[0]>>1) & 0x3f
	}
	return int(nal[0]) & 0x1f
}

func isParamSet(codec gpu.VideoCodec, t int) bool {
	if codec == gpu.DecodeHEVC {
		return t >= 32 && t <= 34 // VPS, SPS, PPS
	}
	return t == 7 || t == 8 // SPS, PPS
}

func isIDR(codec gpu.VideoCodec, t int) bool {
	if codec == gpu.DecodeHEVC {
		return t == 19 || t == 20 // IDR_W_RADL, IDR_N_LP
	}
	return t == 5
}

// openGPUWithFallback opens a GPU decoder of kind k, the decoder here
// (soft) taking over should it fail.
func openGPUWithFallback(k gpu.Kind, codec gpu.VideoCodec, cfg gpu.DecodeConfig, picture func(*gpu.DecodedPicture) error,
	soft func(picture func(*gpu.DecodedPicture) error) (gpu.Decoder, error), report Reporter) (gpu.Decoder, error) {
	return withFallback(codec, picture, func(shown func(*gpu.DecodedPicture) error) (gpu.Decoder, error) {
		return gpu.OpenDecoder(k, cfg, shown)
	}, soft, report)
}

// withFallback is openGPUWithFallback with the GPU decoder opened by
// open.
func withFallback(codec gpu.VideoCodec, picture func(*gpu.DecodedPicture) error, open func(func(*gpu.DecodedPicture) error) (gpu.Decoder, error),
	soft func(picture func(*gpu.DecodedPicture) error) (gpu.Decoder, error), report Reporter) (gpu.Decoder, error) {
	g := &gpuFallback{codec: codec, report: report}
	shown := func(p *gpu.DecodedPicture) error {
		if g.replace && g.shown && p.PTS <= g.last {
			return nil // out before the GPU decoder failed
		}
		if !g.shown && !g.fromIDR {
			g.fed, g.fedPTS = nil, nil
		}
		g.shown, g.last = true, p.PTS
		if err := picture(p); err != nil {
			g.halted = true
			return err
		}
		return nil
	}
	dec, err := open(shown)
	if err != nil {
		return nil, err
	}
	g.dec = dec
	if soft != nil {
		g.open = func() (gpu.Decoder, error) { return soft(shown) }
	}
	return g, nil
}

// softDecoder opens the decoder here for a codec, to take over from a GPU
// decoder (nil for none).
func softDecoder(codec gpu.VideoCodec, cfg gpu.DecodeConfig) func(func(*gpu.DecodedPicture) error) (gpu.Decoder, error) {
	switch codec {
	case gpu.DecodeH264:
		return func(p func(*gpu.DecodedPicture) error) (gpu.Decoder, error) { return openGoH264(p, nil), nil }
	case gpu.DecodeHEVC:
		return func(p func(*gpu.DecodedPicture) error) (gpu.Decoder, error) { return openGoHEVC(p, nil), nil }
	case gpu.DecodeMPEG2:
		return func(p func(*gpu.DecodedPicture) error) (gpu.Decoder, error) { return openGoMPEG2(cfg, p) }
	case gpu.DecodeVC1:
		return func(p func(*gpu.DecodedPicture) error) (gpu.Decoder, error) { return openGoVC1(cfg, p) }
	}
	return nil
}
