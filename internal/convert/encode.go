package convert

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/brunoga/mvc"
	"github.com/brunoga/mvc/internal/hwenc"
)

// encoderSink is where a segment's stacked pictures go: a GPU encoder in
// process, or an encoder program reading Y4M from its stdin.
type encoderSink interface {
	// start begins a segment written to path; first is its first picture,
	// num/den the frame rate.
	start(path string, first *mvc.StereoFrame, num, den int) error
	put(sf *mvc.StereoFrame) error
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
func (r *Runner) decodeAndEncode(ctx context.Context, src mvc.Source, keep func(int64) bool) ([]string, error) {
	var (
		sink encoderSink
		verb = "decoded"
	)
	switch {
	case r.sink != nil: // a test's
		sink = r.sink
	case r.Opts.NativeGPU:
		k, _ := hwKind(r.Opts.Encoder)
		sink = &gpuSink{r: r, kind: k}
		verb = "encoded"
		r.Report.Report("decoding and encoding (%s, %s on the GPU, in process)", r.Opts.Codec, r.Opts.Encoder)
	default:
		bin, err := r.resolve(encoderTool(r.Opts.Encoder, r.Opts.Codec, r.Opts.EncodesViaFFmpeg()))
		if err != nil {
			return nil, err
		}
		sink = &programSink{r: r, ctx: ctx, bin: bin}
		r.Report.Report("decoding and encoding (%s, %s)", r.Opts.Codec, r.Opts.Encoder)
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

	dec := mvc.NewDecoder(mvc.Options{Threads: r.Opts.DecodeThreads})
	var (
		decodeErrs int
		prog       = r.newProgress(verb)
		n          int // pictures kept so far, encoded or skipped
		skipped    int // pictures outside the playlist's IN/OUT
		num, den   int
		open       bool
		inSegment  int
	)
	finishSegment := func() error {
		open = false
		if err := sink.finish(); err != nil {
			return &encodeError{err}
		}
		i := len(r.work.m.Segments)
		return r.work.add(segment{File: fmt.Sprintf("video-%04d%s", i, ext), Frames: inSegment})
	}
	st, decErr := dec.DecodeStream(src, mvc.DecodeOptions{
		OnError: func(err error) {
			// A damaged access unit is concealed and the decode goes on; a
			// few are worth a line, a flood is not.
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
		if n == 0 {
			r.noteFirstPicture(sf.Base.PTS)
			r.Height = sf.Base.Height
			num, den = dec.FrameRate()
			if num <= 0 {
				num, den = 24000, 1001
				r.Report.Report("warning: the stream carries no frame rate; assuming 24000/1001")
			} else {
				r.Report.Report("frame rate %d/%d", num, den)
			}
			r.fpsNum, r.fpsDen = num, den
			prog.begin(num, den, skip)
		}
		n++
		if n <= skip {
			return nil
		}
		if open && inSegment == segmentFrames {
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
	})
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
	switch {
	case n == 0:
		return nil, fmt.Errorf("the video decoded to no frames")
	case st.DependentFrames == 0:
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
	kind hwenc.Kind
	path string
	f    *os.File
	enc  hwenc.Encoder
}

func (s *gpuSink) start(path string, first *mvc.StereoFrame, num, den int) error {
	f, err := os.Create(path) //nolint:gosec // our work directory
	if err != nil {
		return err
	}
	o := s.r.Opts
	w, h := 2*first.Base.Width, first.Base.Height
	if o.Layout == LayoutHalfSBS {
		w = first.Base.Width
	}
	enc, err := hwenc.Open(s.kind, hwenc.Config{Codec: o.Codec.hw(), Width: w, Height: h, FPSNum: num, FPSDen: den,
		QP: o.CRF, Device: o.VAAPIDevice, BitDepth: o.BitDepth}, f)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("opening the %s encoder: %w", o.Encoder, err)
	}
	s.path, s.f, s.enc = path, f, enc
	return nil
}

func (s *gpuSink) put(sf *mvc.StereoFrame) error {
	return s.enc.Encode(func(p *hwenc.Picture) { drawSBS(p, sf, s.r.Opts.SwapLR, s.r.Opts.Layout == LayoutHalfSBS) })
}

func (s *gpuSink) finish() error {
	err := s.enc.Close()
	if err == nil {
		err = s.f.Sync()
	}
	if cerr := s.f.Close(); err == nil {
		err = cerr
	}
	return err
}

func (s *gpuSink) abort() error {
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
	y4m    *mvc.Y4MWriter
	stderr strings.Builder
	failed bool // writing to it failed: it has stopped
}

func (s *programSink) start(path string, _ *mvc.StereoFrame, num, den int) error {
	step := encodeStep(s.r.Opts, path)
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
	s.y4m = mvc.NewY4MWriter(stdin, mvc.LayoutSideBySide)
	s.y4m.SwapViews = s.r.Opts.SwapLR
	s.y4m.Depth = s.r.Opts.BitDepth
	s.y4m.FPSNum, s.y4m.FPSDen = num, den
	return nil
}

func (s *programSink) put(sf *mvc.StereoFrame) error {
	if err := s.y4m.Write(sf); err != nil {
		s.failed = true
		return err
	}
	return nil
}

func (s *programSink) finish() error {
	flushErr := s.y4m.Flush()
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
