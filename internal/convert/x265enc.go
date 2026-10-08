package convert

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/brunoga/bdtools/internal/gpu"
)

// x265 as an encoder in process: a gpu.Encoder over an x265 process, fed
// raw pictures on its standard input, its bitstream read from its
// standard output. It is what keeps Dolby Vision's layers apart (profile 7)
// without NVENC: the in-process pipeline needs the base layer's bitstream
// as it is written (to decode it again) and a second encoder for the
// enhancement layer.
//
// fixedGOP sets x265 up to code every stream the same way: no scene cut
// keyframes, no adaptive B-frames, keyframes every GOP pictures, closed
// GOPs. Two streams of the same length then get the same picture types in
// the same order, which Dolby Vision's two layers must have.
type x265Encoder struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	pic    []byte // a picture as the encoders take it (NV12 or P010)
	planar []byte // and as x265 does (I420, 10 bits low)
	cfg    gpu.Config
	copied chan error
	stderr strings.Builder
	err    error
}

func openX265(bin string, cfg gpu.Config, preset string, fixedGOP bool, w io.Writer) (gpu.Encoder, error) {
	if cfg.Codec != gpu.HEVC {
		return nil, fmt.Errorf("x265 encodes HEVC, not %v", cfg.Codec)
	}
	if cfg.GOP == 0 {
		cfg.GOP = gpu.DefaultGOP
	}
	depth := max(cfg.BitDepth, 8)
	args := []string{"--input", "-", "--input-res", fmt.Sprintf("%dx%d", cfg.Width, cfg.Height),
		"--fps", fmt.Sprintf("%d/%d", cfg.FPSNum, cfg.FPSDen), "--input-depth", fmt.Sprint(depth), "--input-csp", "i420",
		"--crf", fmt.Sprint(cfg.QP), "--preset", preset, "--aud", "--log-level", "error"}
	if depth > 8 {
		args = append(args, "--output-depth", "10")
	}
	if c := cfg.Color; c != nil {
		rng := "limited"
		if c.FullRange {
			rng = "full"
		}
		args = append(args, "--colorprim", fmt.Sprint(c.Primaries), "--transfer", fmt.Sprint(c.Transfer),
			"--colormatrix", fmt.Sprint(c.Matrix), "--range", rng)
	}
	if fixedGOP {
		args = append(args, "--no-scenecut", "--b-adapt", "0", "--bframes", "3", "--no-open-gop",
			"--keyint", fmt.Sprint(cfg.GOP), "--min-keyint", fmt.Sprint(cfg.GOP))
	}
	e := &x265Encoder{cfg: cfg, copied: make(chan error, 1)}
	// Close ends it: the encoder's life is its caller's, not a context's.
	e.cmd = exec.CommandContext(context.Background(), bin, append(args, "--output", "-")...) //nolint:gosec // the x265 we resolved
	e.cmd.Stderr = &e.stderr
	var err error
	if e.stdin, err = e.cmd.StdinPipe(); err != nil {
		return nil, err
	}
	out, err := e.cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := e.cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting x265: %w", err)
	}
	go func() {
		_, err := io.Copy(w, bufio.NewReaderSize(out, 1<<20))
		e.copied <- err
	}()
	bps := 1
	if depth > 8 {
		bps = 2
	}
	e.pic = make([]byte, cfg.Width*cfg.Height*bps*3/2)
	e.planar = make([]byte, len(e.pic))
	return e, nil
}

func (e *x265Encoder) Encode(fill func(*gpu.Picture)) error {
	if e.err != nil {
		return e.err
	}
	w, h := e.cfg.Width, e.cfg.Height
	bps, depth := 1, max(e.cfg.BitDepth, 8)
	if depth > 8 {
		bps = 2
	}
	ys := w * h * bps
	fill(&gpu.Picture{Y: e.pic[:ys], UV: e.pic[ys:], Pitch: w * bps, Depth: depth})
	toPlanar(e.planar, e.pic, w, h, bps)
	if _, err := e.stdin.Write(e.planar); err != nil {
		e.err = e.failure(err)
		return e.err
	}
	return nil
}

// toPlanar turns NV12 or P010 into I420 (10 bits in a sample's low bits).
func toPlanar(dst, src []byte, w, h, bps int) {
	ys, cs := w*h*bps, w/2*(h/2)*bps
	y, uv := src[:ys], src[ys:]
	cb, cr := dst[ys:ys+cs], dst[ys+cs:ys+2*cs]
	if bps == 1 {
		copy(dst, y)
		for i := range cs {
			cb[i], cr[i] = uv[2*i], uv[2*i+1]
		}
		return
	}
	for i := 0; i < ys; i += 2 {
		v := (uint16(y[i]) | uint16(y[i+1])<<8) >> 6
		dst[i], dst[i+1] = byte(v), byte(v>>8)
	}
	for i := 0; i < cs; i += 2 {
		b := (uint16(uv[2*i]) | uint16(uv[2*i+1])<<8) >> 6
		r := (uint16(uv[2*i+2]) | uint16(uv[2*i+3])<<8) >> 6
		cb[i], cb[i+1], cr[i], cr[i+1] = byte(b), byte(b>>8), byte(r), byte(r>>8)
	}
}

// Close finishes the stream: x265 encodes what it holds, and its output is
// all written before Close returns.
func (e *x265Encoder) Close() error {
	if e.stdin == nil {
		return e.err
	}
	_ = e.stdin.Close()
	e.stdin = nil
	cerr := <-e.copied
	if err := e.cmd.Wait(); err != nil && e.err == nil {
		e.err = e.failure(err)
	}
	if e.err == nil && cerr != nil {
		e.err = fmt.Errorf("x265's output: %w", cerr)
	}
	return e.err
}

func (e *x265Encoder) failure(err error) error {
	if msg := strings.TrimSpace(e.stderr.String()); msg != "" {
		return fmt.Errorf("x265: %w\n%s", err, msg)
	}
	return fmt.Errorf("x265: %w", err)
}

// auSplitter takes an HEVC bitstream in writes of any size and keeps it as
// access units (each beginning at an access unit delimiter, which the
// encoders here write, or at a parameter set, prefix SEI or first slice
// after a picture), for a decoder that takes one at a time. Safe for one
// writer and one reader on different goroutines.
type auSplitter struct {
	mu   sync.Mutex
	buf  []byte
	aus  [][]byte
	done bool
}

func (a *auSplitter) Write(b []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.buf = append(a.buf, b...)
	a.split(false)
	return len(b), nil
}

// close takes the last access unit: the stream has ended.
func (a *auSplitter) close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.split(true)
	a.done = true
}

// take gives the access units complete so far.
func (a *auSplitter) take() [][]byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.aus
	a.aus = nil
	return out
}

func (a *auSplitter) split(final bool) {
	b := a.buf
	start, seenVCL := -1, false
	for i := 0; i+3 < len(b); i++ {
		if b[i] != 0 || b[i+1] != 0 || b[i+2] != 1 {
			continue
		}
		at := i
		if at > 0 && b[at-1] == 0 {
			at--
		}
		if i+5 >= len(b) {
			break // the headers are not all here
		}
		t := b[i+3] >> 1 & 0x3f
		vcl := t < 32
		first := vcl && b[i+5]&0x80 != 0
		starter := t == 35 || t == 32 || t == 33 || t == 34 || t == 39
		if start < 0 {
			start = at
		} else if seenVCL && (first || starter) {
			a.aus = append(a.aus, append([]byte(nil), b[start:at]...))
			start, seenVCL = at, false
		}
		if vcl {
			seenVCL = true
		}
		i += 2
	}
	switch {
	case start < 0:
	case final && seenVCL:
		a.aus = append(a.aus, append([]byte(nil), b[start:]...))
		start = len(b)
	}
	if start > 0 {
		a.buf = append(a.buf[:0], b[start:]...)
	}
}

var errNoX265 = errors.New("x265 is needed to keep Dolby Vision's layers apart without NVENC")
