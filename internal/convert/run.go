package convert

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brunoga/bdtools/internal/mkv"
	"github.com/brunoga/bdtools/mvc"
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
	// Restart ignores what an interrupted run left in the work directory
	// and encodes from the start.
	Restart bool
	// SwapLRSet records that the operator gave --swap-lr explicitly, which
	// stops the disc's own base-view marking from overriding them.
	SwapLRSet bool

	// fpsNum and fpsDen are the frame rate the decode found, for the mux.
	fpsNum, fpsDen int
	// timeline places a decoded picture's timestamp on the output's
	// timeline, and videoDelay is where the first kept picture landed: a
	// source can start its picture after its sound.
	timeline   func(pts int64) time.Duration
	videoDelay time.Duration
	// Height is the source picture's height (one eye), known once the
	// first picture is decoded: for naming the output.
	Height int
	// length is how long the output plays, when the source says (a
	// playlist, a Matroska file's duration), for the progress lines.
	length time.Duration

	// tool resolves a program name to a path. Indirected for tests.
	tool func(string) (string, error)
	// work is the work directory, and the segments an earlier run left.
	work *work
	// sink, when set, takes the place of the encoder. For tests.
	sink encoderSink
	// depth is the source's offset metadata, when 3D subtitles are made,
	// and offsetSequence the sequence a subtitle track follows (-1: none).
	depth          *depthMap
	offsetSequence func(Track) int

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
//
// The work directory is beside the output (or under --temp), named after
// it. A run that fails or is stopped leaves the video segments it finished
// there, and the same command run again picks up after them; a run that
// succeeds removes it.
func (r *Runner) Run(ctx context.Context) (err error) {
	if err := r.Opts.Validate(r.GOOS); err != nil {
		return err
	}
	tmp := r.Opts.TempDir
	if tmp == "" {
		tmp = filepath.Dir(r.Opts.Output)
	}
	if r.work, err = openWork(workDirName(tmp, r.Opts.Output)); err != nil {
		return err
	}
	if r.Restart {
		r.work.m.Segments = nil
		r.work.tidy()
	}
	defer func() { r.closeWork(err == nil) }()
	return r.runBuiltin(ctx, r.work.dir)
}

// closeWork removes the work directory after a run, or keeps what a later
// run can resume from. A conversion writes tens of gigabytes of demuxed
// streams, so leaving them behind by accident is not a small mistake.
func (r *Runner) closeWork(ok bool) {
	dir := r.work.dir
	switch {
	case r.KeepTemp:
		r.Report.Report("keeping %s", dir)
		return
	case !ok && r.work.m.done() > 0:
		r.work.tidy()
		r.Report.Report("kept the %d frames encoded so far in %s: the same command continues from there "+
			"(--restart starts over)", r.work.m.done(), dir)
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		r.Report.Report("could not remove %s: %v", dir, err)
	}
}

// runBuiltin is Run proper: the source is read once, in
// place, with the video going straight into the decoder and the other
// tracks to their files on the way.
func (r *Runner) runBuiltin(ctx context.Context, tmp string) error {
	if isMatroska(r.Opts.Input) {
		return r.runMatroska(ctx, tmp)
	}
	src, err := resolveGo(r.Opts.Input, r.Opts.Playlist, r.Report)
	if err != nil {
		return err
	}
	defer src.close()
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
	r.timeline = g.timeline
	if r.Opts.Subs3D.threeD() {
		r.depth = &depthMap{}
		g.depth = r.depth
		r.offsetSequence = func(t Track) int { return src.offsetSequence(uint16(t.ID)) } //nolint:gosec // a PID
	}
	if err := g.start(); err != nil {
		return err
	}
	video, decErr := r.decodeAndEncode(ctx, mvc.Source{Format: mvc.FormatAccessUnits, AccessUnits: g.Next}, g.KeepFrame)
	extras, finErr := g.finish()
	if decErr != nil {
		return decErr
	}
	if finErr != nil {
		return finErr
	}
	return r.mux(ctx, video, extras, g.src.chapters)
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
	src, err := resolveGo(r.Opts.Input, r.Opts.Playlist, r.Report)
	if err != nil {
		return nil, err
	}
	defer src.close()
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
	// sync are the source's timestamps for an audio file, so the mux can
	// keep it in step with the picture (nil: start at zero, count samples).
	sync []mkv.SyncPoint
}

// mux assembles the final file from the video's segments, with the
// chapters given.
func (r *Runner) mux(ctx context.Context, video []string, extras []extra, chapters []time.Duration) error {
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

// noteFirstPicture records where the first kept picture plays, so the mux
// can start the video there rather than at zero.
func (r *Runner) noteFirstPicture(pts int64) {
	if r.timeline == nil || pts < 0 {
		return
	}
	if d := r.timeline(pts); d >= time.Millisecond {
		r.videoDelay = d
	}
}
