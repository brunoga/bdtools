package convert

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/mvc"
)

// mvcSink writes a 3D picture as MVC, Blu-ray 3D's format: the left eye is
// an ordinary H.264 stream, made by x264, the encoder here or a GPU
// encoder, which any player shows in 2D; the right eye is coded here,
// predicted from the left eye and from its own earlier pictures, as a
// second view a 3D player decodes with it. The left eye's encoder runs as
// the frames come; its output is read as it is written and each access
// unit gets its right eye there and then, so only the frames the encoder
// has not caught up with are held.
type mvcSink struct {
	r       *Runner
	ctx     context.Context
	bin     string   // x264, for software encoding
	builtin bool     // the encoder here
	copy    bool     // the source's left eye as it is
	kind    gpu.Kind // the GPU encoder, otherwise

	// with copy: the left eye's access units from the source, in decode
	// order with their timestamps, and the right eyes by timestamp
	baseQ    chan baseAU
	quit     chan struct{}
	byPTS    map[int64]int
	started  bool  // the first IDR picture has been fed
	firstPTS int64 // its timestamp: pictures before it are left out
	dropped  int

	path string
	f    *os.File

	// the left eye's encoder: a program reading Y4M, the encoder here, or
	// a GPU in process
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	y4m    *flatY4M
	stderr strings.Builder
	base   *mvc.BaseEncoder
	enc    gpu.Encoder
	pw     *io.PipeWriter
	// aus queues the builtin encoder's access units for the pipe, so that
	// feeding frames, coding the left eye and coding the right eye
	// overlap; pumped reports the pump's end.
	aus    chan []byte
	pumped chan struct{}

	// right eyes waiting for their left eye's access unit, by display index
	mu      sync.Mutex
	cond    *sync.Cond
	frames  map[int]*mvc.DepSource
	given   int   // frames given so far
	failed  error // the right eye's coding stopped
	done    chan error
	w, h    int // coded size: whole macroblocks
	srcW    int
	srcH    int
	stopped bool
}

// baseAU is a left eye access unit on its way from the source to the
// right eye's coding.
type baseAU struct {
	au  []byte
	pts int64
}

// feedBase gives the sink the source's base view access units (the
// Runner's hook): those the playlist keeps, stripped of MVC NAL units,
// from the first IDR picture on (the output starts there; a disc starts
// at one, a cut from the middle of a stream may not).
func (s *mvcSink) feedBase(keep func(int64) bool) func(au []byte, pts int64) error {
	return func(au []byte, pts int64) error {
		if keep != nil && !keep(pts) {
			return nil
		}
		b := baseAU{au: mvc.BaseView(au), pts: pts}
		s.mu.Lock()
		if !s.started {
			if !mvc.IsIDR(b.au) {
				s.dropped++
				s.mu.Unlock()
				return nil
			}
			s.started, s.firstPTS = true, pts
		}
		s.mu.Unlock()
		select {
		case s.baseQ <- b:
			return nil
		case <-s.quit:
			return errAborted
		}
	}
}

// skips reports whether a decoded picture is left out: with a copied left
// eye, those before its first IDR picture (the sink sees every access
// unit before the decoder gives its picture, so it knows).
func (s *mvcSink) skips(pts int64) bool {
	if !s.copy {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.started || pts < s.firstPTS
}

// mvcDepQP is the right eye's quantiser for a --crf: x264's CRF runs at a
// few steps below its own QP.
func mvcDepQP(crf int) int { return min(max(crf+3, 0), 51) }

// mvcX264Args are x264's settings for a left eye an MVC decoder plays as a
// Blu-ray's: no weighted prediction (players' MVC decoders garble it), no
// B-frames as references, a Blu-ray level and buffer.
func mvcX264Args(o Options) []string {
	argv := []string{
		"--demuxer", "y4m",
		"--crf", fmt.Sprint(o.CRF),
		"--preset", o.Preset,
		"--profile", "high", "--level", "4.1",
		"--weightp", "0", "--no-weightb", "--b-pyramid", "none",
		"--vbv-maxrate", "40000", "--vbv-bufsize", "30000",
	}
	if c := o.Color; c != nil {
		rng := "tv"
		if c.FullRange {
			rng = "pc"
		}
		argv = append(argv, "--colorprim", x264Colour(colourPrimaries, c.Primaries),
			"--transfer", x264Colour(colourTransfers, c.Transfer),
			"--colormatrix", x264Colour(colourMatrices, c.Matrix), "--range", rng)
	}
	return append(argv, "--output", "-", "-")
}

// x264 names colour code points (H.273).
var (
	colourPrimaries = map[int]string{1: "bt709", 4: "bt470m", 5: "bt470bg", 6: "smpte170m", 7: "smpte240m", 9: "bt2020"}
	colourTransfers = map[int]string{1: "bt709", 4: "bt470m", 5: "bt470bg", 6: "smpte170m", 7: "smpte240m", 14: "bt2020-10", 15: "bt2020-12", 16: "smpte2084", 18: "arib-std-b67"}
	colourMatrices  = map[int]string{0: "GBR", 1: "bt709", 4: "fcc", 5: "bt470bg", 6: "smpte170m", 7: "smpte240m", 9: "bt2020nc", 10: "bt2020c"}
)

func x264Colour(names map[int]string, v int) string {
	if n, ok := names[v]; ok {
		return n
	}
	return "undef"
}

func (s *mvcSink) start(path string, first picture, num, den int) error {
	if first.stereo == nil || first.stereo.Dependent == nil {
		return errors.New("MVC output needs a 3D source")
	}
	f, err := os.Create(path) //nolint:gosec // our work directory
	if err != nil {
		return err
	}
	w, h, _ := first.size()
	s.path, s.f = path, f
	s.srcW, s.srcH = w, h
	s.w, s.h = (w+15)&^15, (h+15)&^15
	s.frames = map[int]*mvc.DepSource{}
	s.byPTS = map[int64]int{}
	s.cond = sync.NewCond(&s.mu)
	s.given, s.failed, s.stopped = 0, nil, false
	s.done = make(chan error, 1)

	var out io.Reader
	switch {
	case s.copy:
		// the left eye comes through baseQ; nothing to start
	case s.builtin:
		pr, pw := io.Pipe()
		o := s.r.Opts
		bo := mvc.BaseOptions{Width: w, Height: h, QP: o.CRF, Keyint: 250, BFrames: 3, Level: 41, FPSNum: num, FPSDen: den, AQ: o.AQ}
		if c := o.Color; c != nil {
			bo.Color = &mvc.ColorInfo{Primaries: c.Primaries, Transfer: c.Transfer, Matrix: c.Matrix, FullRange: c.FullRange}
		}
		enc, err := mvc.NewBaseEncoder(bo)
		if err != nil {
			_ = pw.Close()
			return s.fail(err)
		}
		s.base, s.pw = enc, pw
		s.aus, s.pumped = make(chan []byte, 64), make(chan struct{})
		go s.pump()
		out = pr
	case s.bin != "":
		s.cmd = exec.CommandContext(s.ctx, s.bin, mvcX264Args(s.r.Opts)...) //nolint:gosec // bin came from LookPath
		if s.stdin, err = s.cmd.StdinPipe(); err != nil {
			return s.fail(err)
		}
		stdout, err := s.cmd.StdoutPipe()
		if err != nil {
			return s.fail(err)
		}
		s.stderr.Reset()
		s.cmd.Stderr = &s.stderr
		if err := s.cmd.Start(); err != nil {
			return s.fail(fmt.Errorf("starting x264: %w", err))
		}
		s.y4m = newFlatY4M(s.stdin, 8, num, den)
		out = stdout
	default:
		pr, pw := io.Pipe()
		o := s.r.Opts
		cfg := gpu.Config{Codec: gpu.H264, Width: w, Height: h, FPSNum: num, FPSDen: den,
			QP: o.CRF, CQ: o.nvencCQ(), Device: o.VAAPIDevice, BitDepth: 8, Color: o.Color}
		enc, err := gpu.Open(s.kind, cfg, pw)
		if err != nil {
			_ = pw.Close()
			return s.fail(fmt.Errorf("opening the %s encoder: %w", o.Encoder, err))
		}
		s.enc, s.pw = enc, pw
		out = pr
	}
	go func() { s.done <- s.code(out) }()
	return nil
}

func (s *mvcSink) fail(err error) error {
	_ = s.f.Close()
	_ = os.Remove(s.path)
	return err
}

// code reads the left eye's access units as the encoder writes them and
// writes each with its right eye.
func (s *mvcSink) code(out io.Reader) (err error) {
	defer func() {
		if err != nil {
			s.mu.Lock()
			s.failed = err
			s.cond.Broadcast()
			s.mu.Unlock()
			_, _ = io.Copy(io.Discard, out) // let the encoder finish
		}
	}()
	bw := bufio.NewWriterSize(s.f, 4<<20)
	dw := mvc.NewDepWriter(mvcDepQP(s.r.Opts.CRF))
	dw.AQ = s.r.Opts.AQ
	// Players' MVC decoders (Amlogic) distort the right eye coded with B
	// slices on an x264 left eye, while on the left eye coded here it
	// plays; P slices play on both.
	dw.NoBSlices = !s.builtin
	// the left eye's access units: from the encoder's pipe, or, copied,
	// from the source with their timestamps
	var next func() (au []byte, pts int64, err error)
	if s.copy {
		next = func() ([]byte, int64, error) {
			select {
			case b, ok := <-s.baseQ:
				if !ok {
					return nil, 0, io.EOF
				}
				return b.au, b.pts, nil
			case <-s.quit:
				return nil, 0, errAborted
			}
		}
	} else {
		r := mvc.NewAUReader(out)
		next = func() ([]byte, int64, error) {
			au, err := r.Next()
			return au, -1, err
		}
	}
	coded := 0
	// pictures picked so far, in decode order: the writer asks for them
	// as it starts coding, ahead of what it has finished
	picked, cvsStart := 0, 0
	var curPTS int64 // the access unit being submitted, when copied
	pick := func(poc int32, idr bool) (*mvc.DepSource, error) {
		if idr {
			// Everything before an IDR picture is shown before it.
			cvsStart = picked
		}
		picked++
		s.mu.Lock()
		defer s.mu.Unlock()
		var i int
		if s.copy {
			// the right eye of the picture with the access unit's timestamp
			for {
				var ok bool
				if i, ok = s.byPTS[curPTS]; ok {
					break
				}
				if s.stopped {
					return nil, fmt.Errorf("the left eye's picture at %d has no right eye", curPTS)
				}
				s.cond.Wait()
			}
		} else {
			if poc < 0 || poc%2 != 0 {
				return nil, fmt.Errorf("the left eye's encoder numbered a picture %d: MVC needs its frames' order counts at twice their position", poc)
			}
			i = cvsStart + int(poc/2)
		}
		for s.frames[i] == nil {
			if s.stopped || i < s.given-512 {
				return nil, fmt.Errorf("the left eye's picture %d has no right eye", i)
			}
			s.cond.Wait()
		}
		ds := s.frames[i]
		delete(s.frames, i)
		delete(s.byPTS, curPTS)
		s.cond.Broadcast()
		return ds, nil
	}
	write := func(maus [][]byte) error {
		for _, mau := range maus {
			coded++
			if _, err := bw.Write(mau); err != nil {
				return err
			}
		}
		return nil
	}
	for {
		au, pts, err := next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		curPTS = pts
		// several pictures are coded at once; those finished come back
		maus, err := dw.Submit(au, pick)
		if err != nil {
			return err
		}
		if err := write(maus); err != nil {
			return err
		}
	}
	maus, err := dw.Flush()
	if err != nil {
		return err
	}
	if err := write(maus); err != nil {
		return err
	}
	s.mu.Lock()
	left := len(s.frames)
	s.mu.Unlock()
	if left > 0 {
		return fmt.Errorf("%d right eyes had no left eye coded", left)
	}
	return bw.Flush()
}

func (s *mvcSink) put(p picture) error {
	left, right := p.stereo.Base, p.stereo.Dependent
	if right == nil {
		// A 2D access unit (a disc's last pictures, after the dependent
		// view's stream ends) shows its one view to both eyes, as the
		// other layouts do.
		right = left
	}
	if s.r.Opts.SwapLR {
		left, right = right, left
	}
	ds := &mvc.DepSource{Y: make([]byte, s.w*s.h), Cb: make([]byte, s.w*s.h/4), Cr: make([]byte, s.w*s.h/4), StrideY: s.w, StrideC: s.w / 2}
	copyPadded(ds.Y, s.w, s.h, right.Y, right.StrideY, s.srcW, s.srcH)
	copyPadded(ds.Cb, s.w/2, s.h/2, right.Cb, right.StrideC, s.srcW/2, s.srcH/2)
	copyPadded(ds.Cr, s.w/2, s.h/2, right.Cr, right.StrideC, s.srcW/2, s.srcH/2)
	s.mu.Lock()
	// Hold back while the right eye's coding is far behind the encoder.
	for len(s.frames) > 256 && s.failed == nil {
		s.cond.Wait()
	}
	if err := s.failed; err != nil {
		s.mu.Unlock()
		return err
	}
	s.frames[s.given] = ds
	s.byPTS[p.pts] = s.given
	s.given++
	s.cond.Broadcast()
	s.mu.Unlock()
	if s.copy {
		return nil // the left eye came through on its own
	}

	lp := p
	lp.stereo = &mvc.StereoFrame{Base: left}
	switch {
	case s.base != nil:
		// the encoder holds the frame until it codes it
		ls := &mvc.DepSource{Y: make([]byte, s.w*s.h), Cb: make([]byte, s.w*s.h/4), Cr: make([]byte, s.w*s.h/4), StrideY: s.w, StrideC: s.w / 2}
		copyPadded(ls.Y, s.w, s.h, left.Y, left.StrideY, s.srcW, s.srcH)
		copyPadded(ls.Cb, s.w/2, s.h/2, left.Cb, left.StrideC, s.srcW/2, s.srcH/2)
		copyPadded(ls.Cr, s.w/2, s.h/2, left.Cr, left.StrideC, s.srcW/2, s.srcH/2)
		aus, err := s.base.Encode(ls, p.pts)
		if err != nil {
			return err
		}
		return s.writeAUs(aus)
	case s.enc != nil:
		return s.enc.Encode(func(dst *gpu.Picture) { drawFlat(dst, lp) })
	}
	return s.y4m.write(lp)
}

// writeAUs queues the builtin encoder's access units for the right eye's
// coding.
func (s *mvcSink) writeAUs(aus [][]byte) error {
	for _, au := range aus {
		select {
		case s.aus <- au:
		case <-s.pumped:
			return errors.New("the right eye's coding stopped")
		}
	}
	return nil
}

// pump writes the queued access units into the pipe the right eye's
// coding reads, and closes it after the last.
func (s *mvcSink) pump() {
	defer close(s.pumped)
	for au := range s.aus {
		if _, err := s.pw.Write(au); err != nil {
			return
		}
	}
	_ = s.pw.Close()
}

// copyPadded copies a w x h plane into a dw x dh one, repeating the last
// column and row into the padding.
func copyPadded(dst []byte, dw, dh int, src []byte, stride, w, h int) {
	for y := 0; y < dh; y++ {
		sy := min(y, h-1)
		row := dst[y*dw : y*dw+dw]
		copy(row, src[sy*stride:sy*stride+w])
		for x := w; x < dw; x++ {
			row[x] = row[w-1]
		}
	}
}

// stop tells the right eye's coding that no more frames come.
func (s *mvcSink) stop() {
	s.mu.Lock()
	s.stopped = true
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *mvcSink) finish() error {
	var encErr error
	switch {
	case s.copy:
		close(s.baseQ) // the source is done feeding
		if s.dropped > 0 {
			s.r.Report.Report("warning: the source's left eye starts %d pictures before its first IDR picture: those are left out", s.dropped)
		}
	case s.base != nil:
		aus, err := s.base.Flush()
		if err == nil {
			err = s.writeAUs(aus)
		}
		encErr = err
		close(s.aus)
		<-s.pumped
	case s.enc != nil:
		encErr = s.enc.Close()
		_ = s.pw.Close()
	default:
		flushErr := s.y4m.flush()
		_ = s.stdin.Close()
		if flushErr != nil {
			encErr = fmt.Errorf("feeding x264: %w", flushErr)
		}
	}
	codeErr := <-s.done
	s.stop()
	if s.cmd != nil {
		if err := s.cmd.Wait(); err != nil && encErr == nil {
			encErr = fmt.Errorf("x264: %w\n%s", err, strings.TrimSpace(s.stderr.String()))
		}
	}
	err := encErr
	if err == nil {
		err = codeErr
	}
	if err == nil {
		err = s.f.Sync()
	}
	if cerr := s.f.Close(); err == nil {
		err = cerr
	}
	return err
}

// errAborted ends the right eye's coding when the conversion stops for
// another reason; it is that reason the user needs, not this.
var errAborted = errors.New("aborted")

// abort stops the coding after a failure elsewhere and gives the right
// eye's own failure, when it had one, or nil.
func (s *mvcSink) abort() error {
	s.stop()
	if s.copy {
		close(s.quit) // the feed may be blocked on a full queue; the coding reads nothing more
	} else if s.base != nil {
		// no more access units come: let the pump end, failing its write
		// if it is in one
		_ = s.pw.CloseWithError(errAborted)
		close(s.aus)
		<-s.pumped
	} else if s.enc != nil {
		_ = s.pw.CloseWithError(errAborted)
		_ = s.enc.Close()
	} else if s.cmd != nil {
		_ = s.cmd.Process.Kill()
		_ = s.stdin.Close()
	}
	<-s.done
	if s.cmd != nil {
		_ = s.cmd.Wait()
	}
	_ = s.f.Close()
	_ = os.Remove(s.path)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil && !errors.Is(s.failed, errAborted) {
		return s.failed
	}
	return nil
}
