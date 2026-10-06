package convert

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"

	"github.com/brunoga/mvc"
	"github.com/brunoga/mvc/internal/hwenc"
)

// GPUAPI selects how a hardware encoder is driven.
type GPUAPI string

const (
	// GPUBuiltin talks to the GPU's own library (NVENC, VAAPI,
	// VideoToolbox) in process, falling back to ffmpeg when it is not there.
	GPUBuiltin GPUAPI = "builtin"
	// GPUFFmpeg always encodes through ffmpeg, as before.
	GPUFFmpeg GPUAPI = "ffmpeg"
)

// Valid reports whether g is a known value.
func (g GPUAPI) Valid() bool { return g == GPUBuiltin || g == GPUFFmpeg }

func hwKind(e Encoder) (hwenc.Kind, bool) {
	switch e {
	case EncoderNVENC:
		return hwenc.NVENC, true
	case EncoderVAAPI:
		return hwenc.VAAPI, true
	case EncoderVideoToolbox:
		return hwenc.VideoToolbox, true
	}
	return "", false
}

// ProbeNative reports whether a hardware encoder works in process for the
// codec: a short trial encode, since a library that loads is not proof of a
// GPU that encodes. A variable, so tests can decide what the machine has.
var ProbeNative = probeNative

// hw is the codec as the GPU encoders name it.
func (c Codec) hw() hwenc.Codec {
	switch c {
	case CodecH265:
		return hwenc.HEVC
	case CodecAV1:
		return hwenc.AV1
	}
	return hwenc.H264
}

func probeNative(enc Encoder, codec Codec, device string) bool {
	k, ok := hwKind(enc)
	if !ok {
		return false
	}
	const w, h = 256, 128
	e, err := hwenc.Open(k, hwenc.Config{Codec: codec.hw(), Width: w, Height: h, FPSNum: 24, FPSDen: 1, QP: 30, Device: device}, io.Discard)
	if err != nil {
		return false
	}
	for i := 0; i < 2; i++ {
		if e.Encode(func(p *hwenc.Picture) {
			for y := 0; y < h; y++ {
				clear(p.Y[y*p.Pitch : y*p.Pitch+w])
			}
			for y := 0; y < h/2; y++ {
				row := p.UV[y*p.Pitch : y*p.Pitch+w]
				for x := range row {
					row[x] = 128
				}
			}
		}) != nil {
			_ = e.Close()
			return false
		}
	}
	return e.Close() == nil
}

// ResolveGPU decides whether the chosen hardware encoder runs in process:
// it does unless ffmpeg was asked for or the trial encode fails.
func ResolveGPU(o *Options) {
	o.NativeGPU = false
	if _, hw := hwKind(o.Encoder); !hw || o.GPUAPI == GPUFFmpeg {
		return
	}
	o.NativeGPU = ProbeNative(o.Encoder, o.Codec, o.VAAPIDevice)
}

// encodeNative is decodeAndEncode with the GPU encoder in process: each
// stacked frame is drawn straight into the encoder's input buffer.
func (r *Runner) encodeNative(ctx context.Context, src mvc.Source, keep func(int64) bool, out string) error {
	k, _ := hwKind(r.Opts.Encoder)
	f, err := os.Create(out) //nolint:gosec // our work directory
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	r.Report.Report("decoding and encoding (%s, %s on the GPU, in process)", r.Opts.Codec, r.Opts.Encoder)

	dec := mvc.NewDecoder(mvc.Options{Threads: r.Opts.DecodeThreads})
	var (
		enc        hwenc.Encoder
		decodeErrs int
		frames     int
		skipped    int
		prog       = r.newProgress("encoded")
	)
	half := r.Opts.Layout == LayoutHalfSBS
	st, decErr := dec.DecodeStream(src, mvc.DecodeOptions{
		OnError: func(err error) {
			decodeErrs++
			if decodeErrs <= 5 {
				r.Report.Report("warning: %v", err)
			}
		},
	}, func(sf *mvc.StereoFrame) error {
		if keep != nil && !keep(sf.Base.PTS) {
			skipped++
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if enc == nil {
			r.noteFirstPicture(sf.Base.PTS)
			r.Height = sf.Base.Height
			num, den := dec.FrameRate()
			if num <= 0 {
				num, den = 24000, 1001
				r.Report.Report("warning: the stream carries no frame rate; assuming 24000/1001")
			} else {
				r.Report.Report("frame rate %d/%d", num, den)
			}
			r.fpsNum, r.fpsDen = num, den
			prog.begin(num, den)
			w, h := 2*sf.Base.Width, sf.Base.Height
			if half {
				w = sf.Base.Width
			}
			var err error
			enc, err = hwenc.Open(k, hwenc.Config{Codec: r.Opts.Codec.hw(), Width: w, Height: h, FPSNum: num, FPSDen: den,
				QP: r.Opts.CRF, Device: r.Opts.VAAPIDevice}, f)
			if err != nil {
				return fmt.Errorf("opening the %s encoder: %w", r.Opts.Encoder, err)
			}
		}
		frames++
		prog.frame(frames)
		return enc.Encode(func(p *hwenc.Picture) { drawSBS(p, sf, r.Opts.SwapLR, half) })
	})
	var closeErr error
	if enc != nil {
		closeErr = enc.Close()
	}
	switch {
	case decErr != nil:
		return fmt.Errorf("decoding: %w", decErr)
	case closeErr != nil:
		return fmt.Errorf("encoding: %w", closeErr)
	case frames == 0:
		return fmt.Errorf("the video decoded to no frames")
	case st.DependentFrames == 0:
		return fmt.Errorf("the dependent view decoded to nothing: the source does not look like 3D")
	}
	if skipped > 0 {
		r.Report.Report("left out %d pictures outside the playlist's IN/OUT times", skipped)
	}
	r.Report.Report("encoded %d frames (%.1f fps)", frames, prog.rate(frames))
	return f.Close()
}

// drawSBS writes a stereo frame side by side into an NV12 picture: the two
// luma planes row by row, and the two chroma planes with Cb and Cr
// interleaved. With half, each view is squeezed to half its width. Bands of
// rows go to a few goroutines: one core spends milliseconds a frame here,
// which a GPU encoding at 150+ fps notices.
func drawSBS(p *hwenc.Picture, sf *mvc.StereoFrame, swap, half bool) {
	a, b := sf.Base, sf.Dependent
	if b == nil {
		b = a
	}
	if swap {
		a, b = b, a
	}
	bands := min(4, runtime.GOMAXPROCS(0), a.Height/16)
	if bands <= 1 {
		drawRows(p, a, b, half, 0, a.Height/2)
		return
	}
	var wg sync.WaitGroup
	rows := a.Height / 2 // in chroma rows, two luma rows each
	for i := range bands {
		wg.Go(func() { drawRows(p, a, b, half, rows*i/bands, rows*(i+1)/bands) })
	}
	wg.Wait()
}

// drawRows draws chroma rows [from, to) and the luma rows they cover.
func drawRows(p *hwenc.Picture, a, b *mvc.Frame, half bool, from, to int) {
	w := a.Width
	ow := w
	if half {
		ow = w / 2
	}
	for y := 2 * from; y < 2*to; y++ {
		dst := p.Y[y*p.Pitch:]
		ra, rb := a.Y[y*a.StrideY:y*a.StrideY+w], b.Y[y*b.StrideY:y*b.StrideY+w]
		if half {
			squeeze(dst[:ow], ra, 1)
			squeeze(dst[ow:2*ow], rb, 1)
		} else {
			copy(dst[:w], ra)
			copy(dst[w:2*w], rb)
		}
	}
	cw, ocw := w/2, ow/2
	for y := from; y < to; y++ {
		dst := p.UV[y*p.Pitch:]
		for i, f := range [2]*mvc.Frame{a, b} {
			u, v := f.Cb[y*f.StrideC:y*f.StrideC+cw], f.Cr[y*f.StrideC:y*f.StrideC+cw]
			d := dst[i*2*ocw : (i+1)*2*ocw]
			if half {
				squeeze(d, u, 2)
				squeeze(d[1:], v, 2)
				continue
			}
			for x := range ocw {
				d[2*x], d[2*x+1] = u[x], v[x]
			}
		}
	}
}

// squeeze halves a row with a 4-tap filter (-1, 5, 5, -1)/8, close to the
// bicubic scaling ffmpeg used for half-SBS, writing every step-th byte of
// dst (2 to fill one half of interleaved chroma).
func squeeze(dst, src []byte, step int) {
	n := len(src) / 2
	if n == 0 {
		return
	}
	at := func(i int) int { return int(src[max(0, min(len(src)-1, i))]) }
	tap := func(x int) byte {
		i := 2 * x
		return clip8((-at(i-1) + 5*at(i) + 5*at(i+1) - at(i+2) + 4) >> 3)
	}
	dst[0] = tap(0)
	last := n - 1
	if last > 0 {
		dst[last*step] = tap(last)
	}
	// The inner pixels need no clamping of the source index.
	if last < 2 {
		return
	}
	if step == 1 {
		d := dst[1:last]
		s := src[1 : 2*last+1]
		for x := range d {
			q := s[2*x : 2*x+4 : 2*x+4]
			d[x] = clip8((5*(int(q[1])+int(q[2])) - int(q[0]) - int(q[3]) + 4) >> 3)
		}
		return
	}
	for x := 1; x < last; x++ {
		q := src[2*x-1 : 2*x+3 : 2*x+3]
		dst[x*step] = clip8((5*(int(q[1])+int(q[2])) - int(q[0]) - int(q[3]) + 4) >> 3)
	}
}

func clip8(v int) byte {
	if uint(v) > 255 {
		if v < 0 {
			return 0
		}
		return 255
	}
	return byte(v)
}
