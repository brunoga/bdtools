package convert

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/internal/hdr"
	"github.com/brunoga/bdtools/mvc"
)

// encoderSink is where a segment's stacked pictures go: a GPU encoder in
// process, or an encoder program reading Y4M from its stdin.
type encoderSink interface {
	// start begins a segment written to path; first is its first picture,
	// num/den the frame rate.
	start(path string, first picture, num, den int) error
	put(p picture) error
	// finish ends the segment, complete on disk.
	finish() error
	// abort abandons the segment and removes it. It returns the encoder's
	// own failure when the encoder is what stopped, nil otherwise.
	abort() error
}

// encodeError marks an error from the encoder, as opposed to the decoder.
type encodeError struct{ err error }

func (e *encodeError) Error() string { return e.err.Error() }
func (e *encodeError) Unwrap() error { return e.err }

// decodeAndEncode decodes the source in process and encodes the stacked
// frames in segments into the work directory, skipping the pictures that
// an interrupted run's segments already hold (see resume.go). The raw
// frames never touch the disk: for a feature film that is hundreds of
// gigabytes. keep, when set, decides by timestamp which decoded pictures
// are output. It returns the segments, in order.
// newSink opens the encoder the options ask for, with the verb the
// progress reports its pictures by (keep: which pictures a copied left
// eye keeps).
func (r *Runner) newSink(ctx context.Context, keep func(int64) bool) (encoderSink, string, error) {
	var (
		sink encoderSink
		verb = "decoded"
	)
	switch {
	case r.sink != nil: // a test's
		sink = r.sink
	case !r.flat && r.Opts.Layout == LayoutMVHEVC:
		sink = &mvhevcSink{r: r}
		verb = "encoded"
		on := "NVENC"
		if r.Opts.Encoder == EncoderBuiltin {
			on = "the built-in encoder"
		}
		r.Report.Report("decoding and encoding (MV-HEVC: both eyes on " + on + ", the right predicted from the left)")
	case !r.flat && r.Opts.Layout == LayoutMVC:
		s := &mvcSink{r: r, ctx: ctx}
		switch r.Opts.Encoder {
		case EncoderSoftware:
			bin, err := r.resolve(toolX264)
			if err != nil {
				return nil, "", err
			}
			s.bin = bin
		case EncoderBuiltin:
			s.builtin = true
		case EncoderCopy:
			s.copy = true
			s.baseQ = make(chan baseAU, 64)
			s.quit = make(chan struct{})
			r.baseAU = s.feedBase(keep)
		default:
			s.kind, _ = hwKind(r.Opts.Encoder)
		}
		sink = s
		verb = "encoded"
		if s.copy {
			r.Report.Report("decoding and encoding (MVC: the left eye copied from the source, the right eye here)")
		} else {
			r.Report.Report("decoding and encoding (MVC: the left eye with %s, the right eye here)", r.Opts.Encoder)
		}
	case r.Opts.Encoder == EncoderBuiltin && r.Opts.Codec == CodecH264:
		sink = &gpuSink{r: r, open: openBuiltinAVC}
		verb = "encoded"
		r.Report.Report("decoding and encoding (h264, the encoder here, in process)")
	case r.Opts.Encoder == EncoderBuiltin && r.Opts.Codec == CodecH265:
		sink = &gpuSink{r: r, open: openBuiltinHEVC}
		verb = "encoded"
		r.Report.Report("decoding and encoding (h265, the encoder here, in process)")
	case r.Opts.Encoder == EncoderBuiltin && r.Opts.Codec == CodecAV1:
		speed := av1Speed(r.Opts.Preset)
		sink = &gpuSink{r: r, open: func(cfg gpu.Config, w io.Writer) (gpu.Encoder, error) { return openBuiltinAV1(cfg, w, speed) }}
		verb = "encoded"
		r.Report.Report("decoding and encoding (av1, the encoder here, in process)")
	case r.Opts.NativeGPU:
		k, _ := hwKind(r.Opts.Encoder)
		sink = &gpuSink{r: r, kind: k}
		verb = "encoded"
		r.Report.Report("decoding and encoding (%s, %s on the GPU, in process)", r.Opts.Codec, r.Opts.Encoder)
	case r.Opts.DVFEL.layered() && r.Opts.Encoder == EncoderSoftware && r.Opts.Codec == CodecH265:
		// Dolby Vision's layers kept apart without NVENC: x265 in process,
		// set up alike for both layers.
		bin, err := r.resolve(toolX265)
		if err != nil {
			return nil, "", fmt.Errorf("%w: %w", errNoX265, err)
		}
		preset := r.Opts.Preset
		sink = &gpuSink{r: r, open: func(cfg gpu.Config, w io.Writer) (gpu.Encoder, error) {
			return openX265(bin, cfg, preset, r.felLayered, w)
		}}
		verb = "encoded"
		r.Report.Report("decoding and encoding (h265, x265, in process)")
	default:
		bin, err := r.resolve(encoderTool(r.Opts.Encoder, r.Opts.Codec, r.Opts.EncodesViaFFmpeg()))
		if err != nil {
			return nil, "", err
		}
		sink = &programSink{r: r, ctx: ctx, bin: bin}
		r.Report.Report("decoding and encoding (%s, %s)", r.Opts.Codec, r.Opts.Encoder)
	}
	return sink, verb, nil
}

func (r *Runner) decodeAndEncode(ctx context.Context, src pictureSource, keep func(int64) bool) ([]string, error) {
	defer r.closeRedepth()
	defer r.closeTransform()
	sink, verb, err := r.newSink(ctx, keep)
	if err != nil {
		return nil, err
	}
	key, err := r.resumeKey()
	if err != nil {
		return nil, err
	}
	skip, err := r.work.begin(key, r.Report)
	if err != nil {
		return nil, err
	}
	if skip > 0 {
		r.Report.Report("resuming: %d frames were encoded by an earlier run; decoding up to them first", skip)
	}
	ext := r.Opts.Codec.streamExt()

	var (
		prog      = r.newProgress(verb)
		n         int // pictures kept so far, encoded or skipped
		skipped   int // pictures outside the playlist's IN/OUT
		num, den  int
		open      bool
		inSegment int
	)
	finishSegment := func() error {
		open = false
		if err := sink.finish(); err != nil {
			return &encodeError{err}
		}
		i := len(r.work.m.Segments)
		seg := segment{File: fmt.Sprintf("video-%04d%s", i, ext), Frames: inSegment}
		if l, ok := sink.(interface{ elSegment() string }); ok {
			seg.EL = l.elSegment()
		}
		return r.work.add(seg)
	}
	// post is what follows a picture's making: its encoding.
	post := func(sf picture) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if n == 0 {
			if sf.layered {
				// Dolby Vision's layers kept apart: profile 7 out.
				if _, ok := sink.(*gpuSink); !ok {
					return &encodeError{errors.New("--dv-fel " + string(r.Opts.DVFEL) +
						" needs NVENC in process (an NVIDIA GPU, --gpu-api builtin), x265 (--encoder software) or the encoder here (--encoder builtin)")}
				}
				r.felLayered = true
			}
			r.noteFirstPicture(sf.pts)
			_, h, depth := sf.size()
			r.Height = h
			r.resolveDepth(depth)
			if sf.gpu != nil {
				c := sf.gpu.Color
				r.colour = &c
				if c.Primaries != 2 || c.Transfer != 2 || c.Matrix != 2 { // something stated
					r.Opts.Color = &c
				}
				switch {
				case c.Transfer != 16 && c.Transfer != 18, r.Opts.BitDepth != 8:
				case r.Opts.Codec == CodecH264:
					r.Report.Report("warning: an HDR source into 8-bit H.264 bands visibly; --codec h265 or av1 keeps 10 bits")
				default:
					r.Report.Report("warning: an HDR source at 8 bits bands visibly; --bit-depth 10 (the default for it) keeps its precision")
				}
			} else if sf.stereo != nil {
				r.noteDiscColour(sf)
			}
			sn, sd := src.frameRate()
			var overruled bool
			num, den, overruled = pictureRate(sn, sd, r.rateNum, r.rateDen)
			if overruled {
				r.Report.Report("warning: the video stream states %d/%d frames a second but its timestamps run at %d/%d; following the timestamps", sn, sd, num, den)
			}
			if num <= 0 {
				num, den = 24000, 1001
				r.Report.Report("warning: the stream carries no frame rate; assuming 24000/1001")
			} else {
				r.Report.Report("frame rate %d/%d", num, den)
			}
			r.burnFrameDur = time.Duration(int64(time.Second) * int64(den) / int64(num))
			r.fpsNum, r.fpsDen = num, den
			prog.begin(num, den, skip)
			if r.Opts.VideoSize > 0 && r.sizeFeed != nil {
				feed, closeFeed, err := r.sizeFeed()
				if err != nil {
					return fmt.Errorf("--video-size: %w", err)
				}
				// The places within the part converted.
				length, lo, hi := r.length, 0.0, 1.0
				if !r.Opts.Trim.IsZero() && r.length > 0 {
					end := r.Opts.Trim.End
					if end == 0 || end > r.length {
						end = r.length
					}
					length = end - r.Opts.Trim.Start
					lo, hi = r.Opts.Trim.Start.Seconds()/r.length.Seconds(), end.Seconds()/r.length.Seconds()
				}
				within := func(frac float64) (func() ([]byte, []byte, int64, error), func(), error) {
					return feed(lo + (hi-lo)*frac)
				}
				pictures := length.Seconds() * float64(num) / float64(den)
				crf, err := r.estimateCRF(ctx, r.Selected.Base, within, pictures, num, den)
				closeFeed()
				if err != nil {
					return fmt.Errorf("--video-size: %w", err)
				}
				r.Opts.CRF = crf
			}
		}
		n++
		if sf.hdr10Plus != nil {
			if r.hdr10Plus == nil {
				r.hdr10Plus = map[int64][]byte{}
			}
			r.hdr10Plus[int64(n-1)] = sf.hdr10Plus
		}
		if sf.rpu != nil {
			r.keepRPU(int64(n-1), sf.rpu)
		}
		if n <= skip {
			return nil
		}
		if open && inSegment == segmentFrames && r.Opts.Encoder != EncoderCopy { // a copied left eye runs on as one stream
			if err := finishSegment(); err != nil {
				return err
			}
		}
		if !open {
			if err := sink.start(r.work.segmentPath(len(r.work.m.Segments), ext), sf, num, den); err != nil {
				return &encodeError{err}
			}
			open, inSegment = true, 0
		}
		if err := sink.put(sf); err != nil {
			return &encodeError{err}
		}
		inSegment++
		prog.frame(n - skip)
		return nil
	}
	var stages *stagePipe
	if r.to3D {
		stages = newStagePipe()
	}
	// push3D queues a picture's rest (convert3D's) for the stages
	push3D := func(finish func() (picture, error)) error {
		if finish == nil {
			return nil
		}
		return stages.push(func() error {
			sf, err := finish()
			if err != nil {
				return err
			}
			return post(sf)
		})
	}
	decErr := src.run(func(sf picture) error {
		if keep != nil && !keep(sf.pts) {
			skipped++
			return nil
		}
		if sk, ok := sink.(interface{ skips(int64) bool }); ok && sk.skips(sf.pts) {
			skipped++ // a copied left eye starts at its first IDR picture
			return nil
		}
		if r.Opts.Crop.Auto {
			r.Report.Report("--crop auto: this source cannot be looked at in places: not cropped")
			r.Opts.Crop = Crop{}
		}
		var err error
		if sf, err = r.transform(sf, src); err != nil {
			return err
		}
		if r.to3D {
			// the depth now, the eyes and the rest beside the next
			// picture's depth
			finish, err := r.convert3D(sf)
			if err != nil {
				return err
			}
			return push3D(finish)
		}
		if r.redepth {
			sf = r.redepthPicture(sf)
		}
		return post(sf)
	})
	if stages != nil {
		// the last picture's depth
		if finish, err := r.wait3D(); decErr == nil {
			if decErr = err; err == nil {
				decErr = push3D(finish)
			}
		}
		if err := stages.close(); decErr == nil {
			decErr = err
		}
	}
	if decErr == nil && open {
		decErr = finishSegment()
	}
	if decErr != nil {
		var encErr error
		if open {
			encErr = sink.abort()
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("interrupted: %w", ctx.Err())
		}
		// The encoder's own message is the useful one when it died: a write
		// error on its input only says that it did.
		if encErr != nil {
			return nil, fmt.Errorf("encoding: %w", encErr)
		}
		var ee *encodeError
		if errors.As(decErr, &ee) {
			return nil, fmt.Errorf("encoding: %w", ee.err)
		}
		return nil, fmt.Errorf("decoding: %w", decErr)
	}
	decodeErrs := src.errors()
	r.hdrStatic = src.hdrStatic()
	if r.xform != nil && r.xform.tm != nil {
		r.hdrStatic = hdr.Static{} // SDR now
	}
	switch {
	case n == 0:
		return nil, fmt.Errorf("the video decoded to no frames")
	case !r.flat && !r.to3D && src.dependentFrames() == 0:
		return nil, fmt.Errorf("the dependent view decoded to nothing: the source does not look like 3D")
	case n < skip:
		return nil, fmt.Errorf("the source has %d frames, fewer than the %d an earlier run encoded: "+
			"delete %s and start over", n, skip, r.work.dir)
	}
	if skipped > 0 {
		r.Report.Report("left out %d pictures outside the playlist's IN/OUT times", skipped)
	}
	switch {
	case decodeErrs > 0:
		r.Report.Report("decoded %d frames, %d access units had errors and were concealed", n, decodeErrs)
	case n > skip:
		r.Report.Report("%s %d frames (%.1f fps)", verb, n-skip, prog.rate(n-skip))
	}
	return r.work.segmentPaths(), nil
}

// gpuSink encodes on a GPU in process: each stacked frame is drawn straight
// into the encoder's input buffer.
type gpuSink struct {
	r    *Runner
	kind gpu.Kind
	path string
	f    *os.File
	enc  gpu.Encoder
	// layers is the Dolby Vision enhancement layer's side, when it is kept
	// as a layer.
	layers *felLayers
	// open opens the encoder (nil: the GPU's, of kind).
	open encoderOpener
}

func (s *gpuSink) opener() encoderOpener {
	if s.open != nil {
		return s.open
	}
	return func(cfg gpu.Config, w io.Writer) (gpu.Encoder, error) { return gpu.Open(s.kind, cfg, w) }
}

func (s *gpuSink) start(path string, first picture, num, den int) error {
	f, err := os.Create(path) //nolint:gosec // our work directory
	if err != nil {
		return err
	}
	o := s.r.Opts
	w, h := s.r.frameSize(first)
	cfg := gpu.Config{Codec: o.Codec.hw(), Width: w, Height: h, FPSNum: num, FPSDen: den,
		QP: o.CRF, CQ: o.nvencCQ(), Device: o.VAAPIDevice, BitDepth: o.BitDepth, Color: o.Color}
	var out io.Writer = f
	s.layers = nil
	if s.r.felLayered {
		l, bl, err := openLayers(o.DVFEL, s.opener(), s.r.openDecoder, cfg, o.elCRF(), path, f)
		if err != nil {
			_ = f.Close()
			_ = os.Remove(path)
			return err
		}
		s.layers, out = l, bl
	}
	enc, err := s.opener()(cfg, out)
	if err != nil {
		if s.layers != nil {
			s.layers.abort()
		}
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("opening the %s encoder: %w", o.Encoder, err)
	}
	s.path, s.f, s.enc = path, f, enc
	return nil
}

// elSegment is the enhancement layer's file for the segment, if any.
func (s *gpuSink) elSegment() string {
	if s.layers == nil {
		return ""
	}
	return filepath.Base(s.layers.elPath)
}

func (s *gpuSink) put(p picture) error {
	if s.layers != nil {
		if err := s.enc.Encode(func(dst *gpu.Picture) { drawFlat(dst, p) }); err != nil {
			return err
		}
		return s.layers.put(p)
	}
	if s.r.flat {
		return s.enc.Encode(func(dst *gpu.Picture) {
			drawFlat(dst, p)
			s.r.burnFrame(dst, p, false, "")
		})
	}
	return s.enc.Encode(func(dst *gpu.Picture) {
		drawFramed(dst, p.stereo, s.r.Opts.SwapLR, s.r.Opts.Layout)
		s.r.burnFrame(dst, p, true, s.r.Opts.Layout)
	})
}

func (s *gpuSink) finish() error {
	err := s.enc.Close()
	if s.layers != nil {
		if err == nil {
			err = s.layers.finish()
		} else {
			s.layers.abort()
		}
	}
	if err == nil {
		err = s.f.Sync()
	}
	if cerr := s.f.Close(); err == nil {
		err = cerr
	}
	return err
}

func (s *gpuSink) abort() error {
	if s.layers != nil {
		s.layers.abort()
	}
	_ = s.enc.Close()
	_ = s.f.Close()
	_ = os.Remove(s.path)
	return nil
}

// programSink runs an encoder program (x264, x265, SvtAv1EncApp or
// ffmpeg) for each segment and streams the stacked frames to it as Y4M.
type programSink struct {
	r      *Runner
	ctx    context.Context
	bin    string
	path   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	y4m    *mvc.Y4MWriter // a 3D pair, side by side
	flat   *flatY4M       // a 2D picture
	stderr strings.Builder
	failed bool // writing to it failed: it has stopped
}

func (s *programSink) start(path string, first picture, num, den int) error {
	if s.r.burn != nil {
		s.r.Report.Report("warning: --burn-subs draws with the encoders in process (builtin, GPU), not %s: no subtitles burned", filepath.Base(s.bin))
	}
	o := s.r.Opts
	o.frameW, o.frameH = s.r.frameSize(first)
	step := encodeStep(o, path)
	s.cmd = exec.CommandContext(s.ctx, s.bin, step.Argv[1:]...) //nolint:gosec // bin came from LookPath
	stdin, err := s.cmd.StdinPipe()
	if err != nil {
		return err
	}
	s.stderr.Reset()
	s.cmd.Stderr = &s.stderr
	if err := s.cmd.Start(); err != nil {
		return fmt.Errorf("starting the encoder: %w", err)
	}
	s.path, s.stdin, s.failed = path, stdin, false
	if s.r.flat {
		s.flat, s.y4m = newFlatY4M(stdin, s.r.Opts.BitDepth, num, den), nil
		return nil
	}
	s.flat = nil
	layout := mvc.LayoutSideBySide
	if s.r.Opts.Layout.overUnder() {
		layout = mvc.LayoutTopBottom
	}
	s.y4m = mvc.NewY4MWriter(stdin, layout)
	s.y4m.SwapViews = s.r.Opts.SwapLR
	s.y4m.Depth = s.r.Opts.BitDepth
	s.y4m.FPSNum, s.y4m.FPSDen = num, den
	return nil
}

func (s *programSink) put(p picture) error {
	var err error
	if s.flat != nil {
		err = s.flat.write(p)
	} else {
		err = s.y4m.Write(p.stereo)
	}
	if err != nil {
		s.failed = true
		return err
	}
	return nil
}

func (s *programSink) finish() error {
	var flushErr error
	if s.flat != nil {
		flushErr = s.flat.flush()
	} else {
		flushErr = s.y4m.Flush()
	}
	_ = s.stdin.Close()
	if err := s.cmd.Wait(); err != nil {
		return fmt.Errorf("%w\n%s", err, strings.TrimSpace(s.stderr.String()))
	}
	if flushErr != nil {
		return fmt.Errorf("feeding the encoder: %w", flushErr)
	}
	return nil
}

func (s *programSink) abort() error {
	var err error
	if s.failed {
		// It stopped by itself: what it said is the error.
		_ = s.stdin.Close()
		if werr := s.cmd.Wait(); werr != nil {
			err = fmt.Errorf("%w\n%s", werr, strings.TrimSpace(s.stderr.String()))
		}
	} else {
		_ = s.cmd.Process.Kill()
		_ = s.stdin.Close()
		_ = s.cmd.Wait()
	}
	_ = os.Remove(s.path)
	return err
}

// noteDiscColour records the colour signalling a 3D disc's stream states,
// for the output to repeat. A Blu-ray's HD video is BT.709 whether its
// stream says so or not, and the players an MV-HEVC file is for go by what
// the file states, so that output states BT.709 for an HD source that says
// nothing.
func (r *Runner) noteDiscColour(sf picture) {
	if r.colour != nil {
		return // a framed source's, from its decoder
	}
	p, t, m, full := sf.stereo.ColorInfo()
	c := gpu.ColorInfo{Primaries: p, Transfer: t, Matrix: m, FullRange: full}
	switch {
	case p != 2 || t != 2 || m != 2: // something stated
	case r.Opts.Layout == LayoutMVHEVC && sf.stereo.Base.Width <= 1920 && sf.stereo.Base.Height <= 1080:
		c = gpu.ColorInfo{Primaries: 1, Transfer: 1, Matrix: 1}
		r.Report.Report("the disc's video states no colour; the file states BT.709, a Blu-ray's HD colour")
	default:
		return
	}
	r.colour = &c
	r.Opts.Color = &c
}

// transform is a picture with --crop, --scale and --tonemap applied, the
// transform readied at the first picture (src their source); --to-3d
// makes an HDR source SDR.
func (r *Runner) transform(sf picture, src pictureSource) (picture, error) {
	if r.to3D && r.xform == nil && !r.Opts.ToneMap && hdrPicture(sf) {
		// the stereo pictures are 8-bit: an HDR source is made SDR first
		r.Report.Report("--to-3d: the 3D is 8-bit SDR: tone mapping the HDR source (--tonemap)")
		r.Opts.ToneMap = true
	}
	if r.xform == nil && (!r.Opts.Crop.IsZero() || !r.Opts.Scale.IsZero() || r.Opts.ToneMap) {
		w, h, _ := sf.size()
		t, err := newPicTransform(r.Opts.Crop, r.Opts.Scale, w, h)
		if err != nil {
			return picture{}, err
		}
		if r.Opts.ToneMap {
			if err := r.setupToneMap(t, sf, src); err != nil {
				return picture{}, err
			}
		}
		r.xform = t
		if d := t.describe(); d != "" {
			r.Report.Report("%s", d)
		}
		if t.sc != nil || t.tm != nil {
			if t.gx = newGPUTransform(t); t.gx != nil {
				r.Report.Report("%s on the GPU (%s)", t.gx.what(t), t.gx.g.Name())
			}
		}
	}
	if r.xform == nil {
		return sf, nil
	}
	p, err := r.xform.apply(sf)
	if e := r.xform.gpuErr; e != nil {
		r.Report.Report("the picture's scaling and tone mapping on the CPU from here: the GPU failed (%v)", e)
		r.xform.gpuErr = nil
	}
	return p, err
}

// closeTransform gives back the transform's GPU.
func (r *Runner) closeTransform() {
	if r.xform != nil {
		r.xform.close()
	}
}
