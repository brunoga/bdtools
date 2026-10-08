package convert

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brunoga/bdtools/internal/dovi"
	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/internal/hdr"
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
	// flat is set for a 2D conversion: one picture, not a stereo pair.
	flat bool
	// rateNum and rateDen are the frame rate the container states, for a
	// stream that does not.
	rateNum, rateDen int
	// colour is the source's colour signalling (a 2D GPU-decoded source),
	// hdrStatic its static HDR metadata and hdr10Plus its HDR10+ metadata by
	// output frame, all carried into the output.
	colour    *gpu.ColorInfo
	hdrStatic hdr.Static
	hdr10Plus map[int64][]byte
	// dovi is the Dolby Vision configuration the output's video carries
	// (a remux keeps it); its level, when 0, is worked out at the mux.
	dovi *dovi.Config
	// rpus are a conversion's Dolby Vision RPUs (profile 8.1 NAL units) by
	// display index, rpuFailed how many could not be converted.
	rpus      map[int64][]byte
	rpuFailed int
	// felLayered: the output keeps Dolby Vision's enhancement layer as a
	// layer (profile 7), and rpus are the source's.
	felLayered bool
	closers    []func() // what the mux opened and closes at its end
	// openDecoder opens decoders like the one decoding the source (for
	// Dolby Vision's enhancement layer and the profile 7 decode loop).
	openDecoder decoderOpener
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
	src, err := resolveGo(r.Opts.Input, r.Opts.Playlist, r.Opts.TwoD, r.Report)
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
	r.flat = r.Opts.TwoD || !threeD(tracks)
	r.Opts.TwoD = r.flat
	selectTracks := SelectTracks
	if r.flat {
		selectTracks = SelectTracks2D
	}
	sel, err := selectTracks(tracks)
	if err != nil {
		return err
	}
	if sel, err = sel.Apply(r.Opts.Audio, r.Opts.Subs); err != nil {
		return err
	}
	r.Selected = sel
	if r.Opts.Remux && !isMKVOutput(r.Opts.Output) {
		return r.remuxBuiltin(ctx, src, sel)
	}
	if r.Opts.Remux && !r.flat {
		return fmt.Errorf("a 3D source's MVC video has no home in Matroska that players agree on: " +
			"remux to .m2ts, or add --2d for its base view alone")
	}
	if r.flat {
		r.Report.Report("source: 2D, video track %d (%s), %d audio, %d subtitle",
			sel.Base.ID, sel.Base.Type, len(sel.Audio), len(sel.Subtitles))
		r.check2D()
	} else {
		r.Report.Report("source: base view track %d, dependent view track %d, %d audio, %d subtitle",
			sel.Base.ID, sel.Dependent.ID, len(sel.Audio), len(sel.Subtitles))
	}
	for _, a := range sel.Audio {
		r.Report.Report("audio: %s", DescribeAudio(a))
	}
	r.length = src.duration
	r.rateNum, r.rateDen = src.frameRate()
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
	if r.Opts.Remux {
		return r.remuxToMKV(ctx, sel.Base, g.Next, g.KeepFrame, g.finish, g.src.chapters)
	}
	pics, err := r.pictures(sel.Base, g.Next)
	if err != nil {
		return err
	}
	video, decErr := r.decodeAndEncode(ctx, pics, g.KeepFrame)
	extras, finErr := g.finish()
	if decErr != nil {
		return decErr
	}
	if finErr != nil {
		return finErr
	}
	return r.mux(ctx, video, extras, g.src.chapters)
}

// pictures chooses the decoder for a source's video. A 3D pair goes to the
// MVC decoder. A 2D picture goes to a GPU decoder when one decodes its codec
// (NVDEC now); else H.264 to the decoder here, and the other codecs to
// ffmpeg.
func (r *Runner) pictures(video Track, next func() (base, dep []byte, pts int64, err error)) (pictureSource, error) {
	cpu := func() pictureSource {
		return newMVCPictures(mvc.Source{Format: mvc.FormatAccessUnits, AccessUnits: next}, r.Opts.DecodeThreads, r.Report)
	}
	if !r.flat {
		return cpu(), nil
	}
	codec, ok := map[string]gpu.VideoCodec{"V_MPEG4/ISO/AVC": gpu.DecodeH264, "V_MPEGH/ISO/HEVC": gpu.DecodeHEVC,
		"V_MS/VFW/FOURCC": gpu.DecodeVC1, "V_MPEG2": gpu.DecodeMPEG2}[video.StreamID]
	if !ok {
		return nil, fmt.Errorf("no decoder for %s video", video.Type)
	}
	decoded := func(open decoderOpener) pictureSource {
		r.openDecoder = open
		return &gpuPictures{open: open, codec: codec, next: next, report: r.Report,
			composeFEL: r.Opts.DVFEL != FELDrop, passFEL: r.Opts.DVFEL.layered()}
	}
	if r.Opts.Decoder != DecoderCPU && ProbeDecoder(gpu.NVENC, codec) {
		r.Report.Report("decoding %s on the GPU (NVDEC)", video.Type)
		return decoded(func(cfg gpu.DecodeConfig, picture func(*gpu.DecodedPicture) error) (gpu.Decoder, error) {
			return gpu.OpenDecoder(gpu.NVENC, cfg, picture)
		}), nil
	}
	if r.Opts.Decoder == DecoderGPU {
		return nil, fmt.Errorf("--decoder gpu: no GPU decoder for %s here", video.Type)
	}
	if codec == gpu.DecodeH264 {
		return cpu(), nil
	}
	dec := toolFFmpeg
	dec.Purpose = "decode " + video.Type + " video without a GPU decoder"
	bin, err := r.resolve(dec)
	if err != nil {
		return nil, fmt.Errorf("decoding %s needs a GPU decoder (NVIDIA's, for now) or ffmpeg: %w", video.Type, err)
	}
	r.Report.Report("decoding %s with ffmpeg (no GPU decoder for it here)", video.Type)
	return decoded(func(cfg gpu.DecodeConfig, picture func(*gpu.DecodedPicture) error) (gpu.Decoder, error) {
		return openFFDecoder(bin, cfg, picture)
	}), nil
}

// decoderOpener opens a decoder of a codec: the GPU's or ffmpeg's.
type decoderOpener func(cfg gpu.DecodeConfig, picture func(*gpu.DecodedPicture) error) (gpu.Decoder, error)

// check2D says what a 2D conversion leaves aside.
func (r *Runner) check2D() {
	if r.Opts.Subs3D.threeD() {
		r.Report.Report("2D: --subs-3d does not apply; the subtitles are kept as they are")
		r.Opts.Subs3D = Subs3DOff
	}
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
	src, err := resolveGo(r.Opts.Input, r.Opts.Playlist, r.Opts.TwoD, r.Report)
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
		case KindEnhancement:
			kind = "video (DV layer)"
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
