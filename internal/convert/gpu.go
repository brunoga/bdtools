package convert

import (
	"context"
	"encoding/binary"
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
	case EncoderMediaFoundation:
		return hwenc.MediaFoundation, true
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

func probeNative(enc Encoder, codec Codec, depth int, device string) bool {
	k, ok := hwKind(enc)
	if !ok {
		return false
	}
	const w, h = 256, 128
	e, err := hwenc.Open(k, hwenc.Config{Codec: codec.hw(), Width: w, Height: h, FPSNum: 24, FPSDen: 1, QP: 30,
		Device: device, BitDepth: depth}, io.Discard)
	if err != nil {
		return false
	}
	grey := mvc.StereoFrame{Base: greyFrame(w/2, h)}
	for i := 0; i < 2; i++ {
		if e.Encode(func(p *hwenc.Picture) { drawSBS(p, &grey, false, false) }) != nil {
			_ = e.Close()
			return false
		}
	}
	return e.Close() == nil
}

// greyFrame is a mid-grey picture.
func greyFrame(w, h int) *mvc.Frame {
	f := &mvc.Frame{Width: w, Height: h, StrideY: w, StrideC: w / 2}
	f.Y, f.Cb, f.Cr = make([]byte, w*h), make([]byte, w*h/4), make([]byte, w*h/4)
	for _, p := range [][]byte{f.Y, f.Cb, f.Cr} {
		for i := range p {
			p[i] = 128
		}
	}
	return f
}

// ResolveGPU decides whether the chosen hardware encoder runs in process:
// it does unless ffmpeg was asked for or the trial encode fails.
func ResolveGPU(o *Options) {
	o.NativeGPU = false
	if _, hw := hwKind(o.Encoder); !hw || o.GPUAPI == GPUFFmpeg {
		return
	}
	o.NativeGPU = ProbeNative(o.Encoder, o.Codec, o.BitDepth, o.VAAPIDevice)
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
				QP: r.Opts.CRF, Device: r.Opts.VAAPIDevice, BitDepth: r.Opts.BitDepth}, f)
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

// drawSBS writes a stereo frame side by side into an NV12 picture (P010
// for a 10-bit one): the two luma planes row by row, and the two chroma
// planes with Cb and Cr interleaved. With half, each view is squeezed to
// half its width. Bands of rows go to a few goroutines: one core spends
// milliseconds a frame here, which a GPU encoding at 150+ fps notices.
func drawSBS(p *hwenc.Picture, sf *mvc.StereoFrame, swap, half bool) {
	a, b := sf.Base, sf.Dependent
	if b == nil {
		b = a
	}
	if swap {
		a, b = b, a
	}
	draw := drawRows
	if p.Depth == 10 {
		draw = drawRows10
	}
	bands := min(4, runtime.GOMAXPROCS(0), a.Height/16)
	if bands <= 1 {
		draw(p, a, b, half, 0, a.Height/2)
		return
	}
	var wg sync.WaitGroup
	rows := a.Height / 2 // in chroma rows, two luma rows each
	for i := range bands {
		wg.Go(func() { draw(p, a, b, half, rows*i/bands, rows*(i+1)/bands) })
	}
	wg.Wait()
}

// drawRows10 is drawRows for a P010 picture: each 8-bit sample becomes the
// 10-bit value four times it (what 8-bit video is at 10 bits), little-endian
// in the top ten bits of two bytes. Squeezing keeps the filter's two extra
// bits rather than rounding them away.
func drawRows10(p *hwenc.Picture, a, b *mvc.Frame, half bool, from, to int) {
	w := a.Width
	ow := w
	if half {
		ow = w / 2
	}
	for y := 2 * from; y < 2*to; y++ {
		dst := p.Y[y*p.Pitch:]
		ra, rb := a.Y[y*a.StrideY:y*a.StrideY+w], b.Y[y*b.StrideY:y*b.StrideY+w]
		if half {
			squeeze10(dst[:2*ow], ra, 1)
			squeeze10(dst[2*ow:4*ow], rb, 1)
		} else {
			widen(dst[:2*w], ra)
			widen(dst[2*w:4*w], rb)
		}
	}
	cw, ocw := w/2, ow/2
	for y := from; y < to; y++ {
		dst := p.UV[y*p.Pitch:]
		for i, f := range [2]*mvc.Frame{a, b} {
			u, v := f.Cb[y*f.StrideC:y*f.StrideC+cw], f.Cr[y*f.StrideC:y*f.StrideC+cw]
			d := dst[i*4*ocw : (i+1)*4*ocw]
			if half {
				squeeze10(d, u, 2)
				squeeze10(d[2:], v, 2)
				continue
			}
			for x := range ocw {
				d[4*x], d[4*x+1], d[4*x+2], d[4*x+3] = 0, u[x], 0, v[x]
			}
		}
	}
}

// widen writes 8-bit samples as P010: each sample's byte is the high byte
// of its 16 bits (the value times four, shifted up six), the low byte zero.
// Eight samples go at a time, as two 64-bit stores.
func widen(dst, src []byte) {
	n := len(src) &^ 7
	for i := 0; i < n; i += 8 {
		s := src[i : i+8 : i+8]
		d := dst[2*i : 2*i+16 : 2*i+16]
		binary.LittleEndian.PutUint64(d, uint64(s[0])<<8|uint64(s[1])<<24|uint64(s[2])<<40|uint64(s[3])<<56)
		binary.LittleEndian.PutUint64(d[8:], uint64(s[4])<<8|uint64(s[5])<<24|uint64(s[6])<<40|uint64(s[7])<<56)
	}
	for i := n; i < len(src); i++ {
		dst[2*i], dst[2*i+1] = 0, src[i]
	}
}

// squeeze10 is squeeze writing P010: the filter's sum, (-1, 5, 5, -1) over
// 8-bit samples, is eight times an 8-bit value, so half of it is the 10-bit
// one. step is in samples (2 for one half of interleaved chroma).
func squeeze10(dst, src []byte, step int) {
	n := len(src) / 2
	if n == 0 {
		return
	}
	at := func(i int) int { return int(src[max(0, min(len(src)-1, i))]) }
	put := func(x, sum int) {
		v := max(0, min(1023, (sum+1)>>1)) << 6
		dst[2*x*step], dst[2*x*step+1] = byte(v), byte(v>>8)
	}
	for x := range n {
		i := 2 * x
		if x == 0 || x == n-1 {
			put(x, -at(i-1)+5*at(i)+5*at(i+1)-at(i+2))
			continue
		}
		q := src[i-1 : i+3 : i+3]
		put(x, 5*(int(q[1])+int(q[2]))-int(q[0])-int(q[3]))
	}
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
