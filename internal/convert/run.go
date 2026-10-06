package convert

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/brunoga/mvc"
)

// Reporter receives progress. A conversion runs for hours, so it has to say
// what it is doing; nil discards.
type Reporter func(format string, args ...any)

// Report emits a line, doing nothing when the reporter is nil.
func (r Reporter) Report(format string, args ...any) {
	if r != nil {
		r(format, args...)
	}
}

// Runner executes a conversion.
type Runner struct {
	Opts Options
	// GOOS is the platform whose toolchain and encoders apply.
	GOOS string
	// Report receives progress lines.
	Report Reporter
	// KeepTemp leaves the demuxed streams behind, for looking at a bad result
	// without paying for the demux again.
	KeepTemp bool
	// SwapLRSet records that the operator gave --swap-lr explicitly, which
	// stops the disc's own base-view marking from overriding them.
	SwapLRSet bool

	// fpsNum and fpsDen are the frame rate the decode found, for the mux.
	fpsNum, fpsDen int
	// length is how long the output plays, when the source says (a
	// playlist, a Matroska file's duration), for the progress lines.
	length time.Duration

	// tool resolves a program name to a path. Indirected for tests.
	tool func(string) (string, error)

	// Selected records what the probe chose, readable once Run returns. A
	// caller naming its output after the audio it got needs this: the codec
	// is not known until the source has been probed.
	Selected Selection
}

// NewRunner returns a runner for opts.
func NewRunner(goos string, opts Options, report Reporter) *Runner {
	return &Runner{Opts: opts, GOOS: goos, Report: report, tool: LookPath}
}

// Run performs the conversion: read the source in place, decode both views
// into stacked frames, encode those, and mux the result with the audio and
// subtitles the source carried — or, for a remux, copy the disc's streams.
func (r *Runner) Run(ctx context.Context) error {
	if err := r.Opts.Validate(r.GOOS); err != nil {
		return err
	}
	tmp, cleanup, err := r.workDir()
	if err != nil {
		return err
	}
	defer cleanup()
	return r.runBuiltin(ctx, tmp)
}

// runBuiltin is Run proper: the source is read once, in
// place, with the video going straight into the decoder and the other
// tracks to their files on the way.
func (r *Runner) runBuiltin(ctx context.Context, tmp string) error {
	if isMatroska(r.Opts.Input) {
		return r.runMatroska(ctx, tmp)
	}
	src, err := resolveGo(r.Opts.Input, r.Report)
	if err != nil {
		return err
	}
	// Take the eye order from the disc unless it was given explicitly.
	if src.knownEye && !r.SwapLRSet {
		r.Opts.SwapLR = src.baseViewIsRight
	}
	r.Report.Report("probing %s", src.clips[0].path)
	tracks, err := probeGo(ctx, src)
	if err != nil {
		return err
	}
	sel, err := SelectTracks(tracks)
	if err != nil {
		return err
	}
	if sel, err = sel.Apply(r.Opts.Audio, r.Opts.Subs); err != nil {
		return err
	}
	r.Selected = sel
	if r.Opts.Remux {
		return r.remuxBuiltin(ctx, src, sel)
	}
	r.Report.Report("source: base view track %d, dependent view track %d, %d audio, %d subtitle",
		sel.Base.ID, sel.Dependent.ID, len(sel.Audio), len(sel.Subtitles))
	for _, a := range sel.Audio {
		r.Report.Report("audio: %s", DescribeAudio(a))
	}
	r.length = src.duration
	g := newGoDemux(src, sel, tmp, r.Report)
	if err := g.start(); err != nil {
		return err
	}
	video := filepath.Join(tmp, "stacked"+r.Opts.Codec.streamExt())
	decErr := r.decodeAndEncode(ctx, mvc.Source{Format: mvc.FormatAccessUnits, AccessUnits: g.Next}, g.KeepFrame, video)
	extras, finErr := g.finish()
	if decErr != nil {
		return decErr
	}
	if finErr != nil {
		return finErr
	}
	return r.mux(ctx, video, extras, g.src.chapters)
}

// workDir returns the scratch directory and a cleanup. A conversion writes tens
// of gigabytes of demuxed streams, so leaving them behind by accident is not a
// small mistake.
func (r *Runner) workDir() (string, func(), error) {
	tmp := r.Opts.TempDir
	if tmp == "" && r.Opts.Output != "" {
		tmp = filepath.Dir(r.Opts.Output)
	}
	if tmp == "" {
		// Only reachable from ListTracks, which needs no output file. A
		// conversion always has one, because Validate insists on it.
		tmp = os.TempDir()
	}
	dir, err := os.MkdirTemp(tmp, "mvctools-")
	if err != nil {
		return "", func() {}, fmt.Errorf("creating a work directory under %s: %w", tmp, err)
	}
	return dir, func() {
		if r.KeepTemp {
			r.Report.Report("keeping %s", dir)
			return
		}
		if err := os.RemoveAll(dir); err != nil {
			r.Report.Report("could not remove %s: %v", dir, err)
		}
	}, nil
}

// ListTracks resolves the source and returns everything it contains, with no
// filter applied, so an operator can see what the track filters have to work
// with.
//
// It reads the playlists and the first megabytes of the feature, wherever
// the source is: nothing is extracted.
func (r *Runner) ListTracks(ctx context.Context) ([]Track, error) {
	if unsupportedContainer(r.Opts.Input) {
		return nil, fmt.Errorf("cannot read %s: the source must be a Blu-ray or a Matroska remux of one", filepath.Base(r.Opts.Input))
	}
	if isMatroska(r.Opts.Input) {
		_, tracks, err := probeMatroska(ctx, r.Opts.Input)
		return tracks, err
	}
	// Reading the playlists and the first megabytes of the feature's stream
	// is enough, wherever the disc is: nothing is extracted.
	src, err := resolveGo(r.Opts.Input, r.Report)
	if err != nil {
		return nil, err
	}
	return probeGo(ctx, src)
}

// DescribeTracks renders a track listing for --list: one line per track, with
// the track number a filter can also select on.
func DescribeTracks(tracks []Track) string {
	var b strings.Builder
	for _, t := range tracks {
		kind := "other"
		switch t.Kind() {
		case KindBaseView:
			kind = "video (base view)"
		case KindDependentView:
			kind = "video (dependent)"
		case KindAudio:
			kind = "audio"
		case KindSubtitle:
			kind = "subtitle"
		}
		lang := strings.TrimSpace(t.Lang)
		if lang == "" {
			lang = "und"
		}
		fmt.Fprintf(&b, "  %-5d %-18s %-5s %-20s %s\n", t.ID, kind, lang, t.Type, t.Info)
	}
	if b.Len() == 0 {
		return "  (no tracks found)\n"
	}
	return b.String()
}

// extra is one demuxed audio or subtitle file and the track it came from.
type extra struct {
	path  string
	track Track
}

// decodeAndEncode decodes the source in process and streams the stacked
// frames into the encoder as Y4M. The raw frames never touch the disk: for
// a feature film that is hundreds of gigabytes. keep, when set, decides by
// timestamp which decoded pictures are output.
func (r *Runner) decodeAndEncode(ctx context.Context, src mvc.Source, keep func(int64) bool, out string) error {
	if r.Opts.NativeGPU {
		return r.encodeNative(ctx, src, keep, out)
	}
	encStep := encodeStep(r.Opts, out)
	encBin, err := r.resolve(encoderTool(r.Opts.Encoder, r.Opts.Codec, r.Opts.EncodesViaFFmpeg()))
	if err != nil {
		return err
	}

	enc := exec.CommandContext(ctx, encBin, encStep.Argv[1:]...) //nolint:gosec // encBin came from LookPath
	stdin, err := enc.StdinPipe()
	if err != nil {
		return err
	}
	var encErr strings.Builder
	enc.Stderr = &encErr
	r.Report.Report("decoding and encoding (%s, %s)", r.Opts.Codec, r.Opts.Encoder)
	if err := enc.Start(); err != nil {
		return fmt.Errorf("starting the encoder: %w", err)
	}

	dec := mvc.NewDecoder(mvc.Options{Threads: r.Opts.DecodeThreads})
	y4m := mvc.NewY4MWriter(stdin, mvc.LayoutSideBySide)
	y4m.SwapViews = r.Opts.SwapLR
	var (
		decodeErrs int
		prog       = r.newProgress("decoded")
		frames     int
	)
	var skipped int
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
		if frames == 0 {
			if num, den := dec.FrameRate(); num > 0 {
				y4m.FPSNum, y4m.FPSDen = num, den
				r.fpsNum, r.fpsDen = num, den
				r.Report.Report("frame rate %d/%d", num, den)
				prog.begin(num, den)
			} else {
				r.Report.Report("warning: the stream carries no frame rate; assuming 24000/1001")
				prog.begin(24000, 1001)
			}
		}
		frames++
		prog.frame(frames)
		return y4m.Write(sf)
	})
	flushErr := y4m.Flush()
	_ = stdin.Close()
	encWait := enc.Wait()

	// The encoder's own message is the useful one when it died: a write error
	// on its stdin only says that it did.
	switch {
	case encWait != nil:
		return fmt.Errorf("encoding: %w\n%s", encWait, strings.TrimSpace(encErr.String()))
	case decErr != nil:
		return fmt.Errorf("decoding: %w", decErr)
	case flushErr != nil:
		return fmt.Errorf("feeding the encoder: %w", flushErr)
	case st.Frames == 0:
		return fmt.Errorf("the video decoded to no frames")
	case st.DependentFrames == 0:
		return fmt.Errorf("the dependent view decoded to nothing: the source does not look like 3D")
	}
	if skipped > 0 {
		r.Report.Report("left out %d pictures outside the playlist's IN/OUT times", skipped)
	}
	if decodeErrs > 0 {
		r.Report.Report("decoded %d frames, %d access units had errors and were concealed", frames, decodeErrs)
	} else {
		r.Report.Report("decoded %d frames", frames)
	}
	return nil
}

// mux assembles the final file, with the chapters given.
func (r *Runner) mux(ctx context.Context, video string, extras []extra, chapters []time.Duration) error {
	return r.muxBuiltin(ctx, video, extras, chapters)
}

// resolve finds a tool, naming what is missing and what it is for rather than
// reporting "executable file not found".
func (r *Runner) resolve(t Tool) (string, error) {
	look := r.tool
	if look == nil {
		look = LookPath
	}
	for _, name := range t.Binaries {
		if p, err := look(name); err == nil {
			return p, nil
		}
	}
	hint := t.InstallHint(r.GOOS)
	if hint != "" {
		hint = "\n  try: " + hint
	}
	return "", fmt.Errorf("%s is not installed — needed to %s%s", t.Name, t.Purpose, hint)
}
