package convert

import (
	"bytes"
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
	// baseAU, when set (--encoder copy), gets each base view access unit
	// as the source gives it, before the decoder does.
	baseAU func(au []byte, pts int64) error
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
	// Explicit holds the options the operator gave (by flag name), so the
	// report of the settings tells them apart from the defaults chosen.
	Explicit map[string]bool

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
	// carried is the high dynamic range the output was given, as the mux
	// settled it: for naming the output (see PictureTags).
	carried struct{ dv, hdr10Plus, hdr10, hlg bool }
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
	// sizeFeed samples the video for --video-size.
	sizeFeed func() (sampleFeed, func(), error)
	// xform crops and scales the pictures (--crop, --scale), once the
	// first gives their size.
	xform *picTransform
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
	// burn draws the subtitle track --burn-subs names into the picture,
	// at offset sequence burnSeq.
	burn         *pgsBurner
	burnSeq      int
	burnFrameDur time.Duration // a picture's, for the depth map
	// framedIn is how a 2D-coded source's frame holds both eyes (a framed
	// layout), framedSwap that the right eye comes first.
	framedIn   Layout
	framedSwap bool
	// to3D is a 2D source made 3D (Options.To3D), by conv.
	to3D bool
	conv *to3DState

	// Version is bdtools' version, which the files written say made them.
	Version string

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
	askedTwoD := r.Opts.TwoD
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
	if !threeD(tracks) && !r.Opts.Remux {
		r.resolveFramed(sel.Base)
		if r.framedIn != "" && !askedTwoD {
			r.flat, r.Opts.TwoD = false, false
		}
	}
	if err := r.useTo3D(threeD(tracks)); err != nil {
		return err
	}
	if r.Opts.Remux && !isMKVOutput(r.Opts.Output) {
		return r.remuxBuiltin(ctx, src, sel)
	}
	if r.Opts.Remux && !r.flat {
		return fmt.Errorf("a 3D source's MVC video has no home in Matroska that players agree on: " +
			"remux to .m2ts, or add --2d for its base view alone")
	}
	if (!r.Opts.Crop.IsZero() || !r.Opts.Scale.IsZero() || r.Opts.ToneMap) && !r.flat && !r.to3D {
		return fmt.Errorf("--crop, --scale and --tonemap convert a 2D picture for now: add --2d for a 3D source's base view")
	}
	switch {
	case r.to3D:
		r.Report.Report("source: 2D, made 3D (its depth estimated), video track %d (%s), %d audio, %d subtitle",
			sel.Base.ID, sel.Base.Type, len(sel.Audio), len(sel.Subtitles))
	case r.framedIn != "" && !r.flat:
		r.Report.Report("source: 3D in one frame (%s), video track %d (%s), %d audio, %d subtitle",
			r.framedIn, sel.Base.ID, sel.Base.Type, len(sel.Audio), len(sel.Subtitles))
	case r.flat:
		r.Report.Report("source: 2D, video track %d (%s), %d audio, %d subtitle",
			sel.Base.ID, sel.Base.Type, len(sel.Audio), len(sel.Subtitles))
		r.check2D()
	default:
		r.Report.Report("source: base view track %d, dependent view track %d, %d audio, %d subtitle",
			sel.Base.ID, sel.Dependent.ID, len(sel.Audio), len(sel.Subtitles))
	}
	for _, a := range sel.Audio {
		r.Report.Report("audio: %s", DescribeAudio(a))
	}
	if !r.Opts.Remux {
		r.resolveDefaults(sel.Base, sel.Dependent.StreamID == streamDVEL)
	}
	r.length = src.duration
	r.rateNum, r.rateDen = src.frameRate()
	g := newGoDemux(src, sel, tmp, r.Report)
	g.setTrim(r.Opts.Trim)
	r.timeline = g.timeline
	// the subtitles' depth: for drawing them in 3D, or a disc's offset
	// metadata
	if r.Opts.Subs3D.threeD() || r.Opts.BurnSubs != 0 && !r.flat || IsDiscImage(r.Opts.Output) {
		r.depth = &depthMap{}
		g.depth = r.depth
		r.offsetSequence = func(t Track) int { return src.offsetSequence(uint16(t.ID)) } //nolint:gosec // a PID
	}
	if r.to3D && r.depth != nil {
		// the conversion gives the subtitles their depth, as sequence 0
		r.offsetSequence = func(Track) int { return 0 }
	}
	if err := g.start(); err != nil {
		return err
	}
	if err := r.setupBurn(g.tapSubtitles); err != nil {
		return err
	}
	if r.Opts.Remux {
		return r.remuxToMKV(ctx, sel.Base, g.Next, g.KeepFrame, g.finish, g.src.chapters)
	}
	if r.Opts.Crop.Auto {
		feed, done, err := r.discFeed(src, sel)
		if err := r.resolveCrop(sel.Base, feed, done, err); err != nil {
			return err
		}
	}
	r.sizeFeed = func() (sampleFeed, func(), error) { return r.discFeed(src, sel) }
	pics, err := r.pictures(sel.Base, g.Next)
	if err != nil {
		return err
	}
	if r.framedIn != "" {
		pics = r.framedSource(pics)
	}
	video, decErr := r.decodeAndEncode(ctx, pics, g.KeepFrame)
	extras, finErr := g.finish()
	if decErr != nil {
		return decErr
	}
	if finErr != nil {
		return finErr
	}
	return r.mux(ctx, video, extras, trimChapters(g.src.chapters, r.Opts.Trim))
}

// pictures chooses the decoder for a source's video. A 3D pair goes to the
// MVC decoder. A 2D picture goes to a GPU decoder when one decodes its codec
// (NVDEC, VideoToolbox, VAAPI for HEVC, H.264 and MPEG-2); else to the
// decoders here, with ffmpeg for what they do not decode (HEVC's format
// range extensions).
func (r *Runner) pictures(video Track, next func() (base, dep []byte, pts int64, err error)) (pictureSource, error) {
	if r.Opts.Encoder == EncoderCopy {
		// the left eye's access units go to the sink as they are, as
		// well as to the decoder
		inner := next
		next = func() (base, dep []byte, pts int64, err error) {
			base, dep, pts, err = inner()
			if err == nil && base != nil && r.baseAU != nil {
				if err := r.baseAU(base, pts); err != nil {
					return nil, nil, 0, err
				}
			}
			return base, dep, pts, err
		}
	}
	cpu := func() pictureSource {
		return newMVCPictures(mvc.Source{Format: mvc.FormatAccessUnits, AccessUnits: next}, r.Opts.DecodeThreads, r.Report)
	}
	if !r.flat && r.Selected.Dependent.StreamID == streamMVHEVC {
		r.Report.Report("decoding MV-HEVC here, both views")
		return &mvhevcPictures{next: next, threads: r.Opts.DecodeThreads, colour: func(c gpu.ColorInfo) {
			if c.Primaries != 2 || c.Transfer != 2 || c.Matrix != 2 {
				r.colour, r.Opts.Color = &c, &c
			}
		}}, nil
	}
	if !r.flat && r.framedIn == "" && !r.to3D {
		return cpu(), nil
	}
	codec, ok := map[string]gpu.VideoCodec{"V_MPEG4/ISO/AVC": gpu.DecodeH264, "V_MPEGH/ISO/HEVC": gpu.DecodeHEVC,
		"V_MS/VFW/FOURCC": gpu.DecodeVC1, "V_MPEG2": gpu.DecodeMPEG2}[video.StreamID]
	if !ok {
		return nil, fmt.Errorf("no decoder for %s video", video.Type)
	}
	// Interlaced H.264 (field pictures, MBAFF: 1080i discs) VAAPI does
	// not decode through the decoder here: the GPU's own parsers (NVDEC,
	// VideoToolbox) do, or the decoder here, deinterlacing.
	interlaced := false
	if codec == gpu.DecodeH264 {
		var err error
		if next, interlaced, err = peekInterlacedH264(next); err != nil {
			return nil, err
		}
	}
	decoded := func(open decoderOpener) pictureSource {
		r.openDecoder = open
		return &gpuPictures{open: open, codec: codec, next: next, report: r.Report,
			composeFEL: r.Opts.DVFEL != FELDrop, passFEL: r.Opts.DVFEL.layered()}
	}
	if r.Opts.Decoder != DecoderCPU {
		for _, k := range []struct {
			kind gpu.Kind
			name string
		}{{gpu.NVENC, "NVDEC"}, {gpu.VideoToolbox, "VideoToolbox"}, {gpu.VAAPI, "VAAPI"}} {
			if interlaced && k.kind == gpu.VAAPI {
				continue
			}
			if ProbeDecoder(k.kind, codec) {
				r.Report.Report("decoding %s on the GPU (%s)", video.Type, k.name)
				return decoded(func(cfg gpu.DecodeConfig, picture func(*gpu.DecodedPicture) error) (gpu.Decoder, error) {
					return openGPUWithFallback(k.kind, codec, cfg, picture, softDecoder(codec, cfg), r.Report)
				}), nil
			}
		}
	}
	if r.Opts.Decoder == DecoderGPU {
		return nil, fmt.Errorf("--decoder gpu: no GPU decoder for %s here", video.Type)
	}
	switch codec {
	case gpu.DecodeH264:
		if interlaced {
			r.Report.Report("decoding interlaced %s here, deinterlaced", video.Type)
			return decoded(func(cfg gpu.DecodeConfig, picture func(*gpu.DecodedPicture) error) (gpu.Decoder, error) {
				return openGoH264(picture, func() (gpu.Decoder, error) {
					dec := toolFFmpeg
					dec.Purpose = "decode interlaced H.264 video of a feature not decoded here"
					bin, err := r.resolve(dec)
					if err != nil {
						return nil, err
					}
					r.Report.Report("decoding %s with ffmpeg (a feature not decoded here)", video.Type)
					return openFFDecoder(bin, cfg, picture)
				}), nil
			}), nil
		}
		return cpu(), nil
	case gpu.DecodeMPEG2:
		r.Report.Report("decoding %s here", video.Type)
		return decoded(openGoMPEG2), nil
	case gpu.DecodeVC1:
		r.Report.Report("decoding %s here", video.Type)
		return decoded(openGoVC1), nil
	}
	r.Report.Report("decoding %s here", video.Type)
	return decoded(func(cfg gpu.DecodeConfig, picture func(*gpu.DecodedPicture) error) (gpu.Decoder, error) {
		return openGoHEVC(picture, func() (gpu.Decoder, error) {
			dec := toolFFmpeg
			dec.Purpose = "decode " + video.Type + " video of a profile not decoded here"
			bin, err := r.resolve(dec)
			if err != nil {
				return nil, err
			}
			r.Report.Report("decoding %s with ffmpeg (a profile not decoded here)", video.Type)
			return openFFDecoder(bin, cfg, picture)
		}), nil
	}), nil
}

// peekInterlacedH264 reads the first access unit of an H.264 stream and
// reports whether its sequence parameter set allows interlaced pictures,
// giving back a next that starts with that access unit again.
func peekInterlacedH264(next func() (base, dep []byte, pts int64, err error)) (func() (base, dep []byte, pts int64, err error), bool, error) {
	base, dep, pts, err := next()
	if err != nil && base == nil {
		return func() ([]byte, []byte, int64, error) { return nil, nil, 0, err }, false, nil
	}
	interlaced := false
	for i := 0; i+4 < len(base); i++ {
		if base[i] != 0 || base[i+1] != 0 || base[i+2] != 1 || base[i+3]&0x1f != 7 {
			continue
		}
		end := len(base)
		if j := bytes.Index(base[i+4:], []byte{0, 0, 1}); j >= 0 {
			end = i + 4 + j
		}
		if info, e := mvc.ParseSPSInfo(base[i:end]); e == nil {
			interlaced = !info.FrameMbsOnly
			break
		}
	}
	first := true
	return func() ([]byte, []byte, int64, error) {
		if first {
			first = false
			return base, dep, pts, err
		}
		return next()
	}, interlaced, nil
}

// decoderOpener opens a decoder of a codec: the GPU's, ffmpeg's or one here.
type decoderOpener func(cfg gpu.DecodeConfig, picture func(*gpu.DecodedPicture) error) (gpu.Decoder, error)

// setupBurn readies --burn-subs: the track's display sets followed as
// tap hands them over.
func (r *Runner) setupBurn(tap func(id int, fn func(int64, []byte)) bool) error {
	if r.Opts.BurnSubs == 0 {
		return nil
	}
	if r.flat && !isMP4Output(r.Opts.Output) {
		r.Report.Report("2D: --burn-subs does not apply; the subtitles are kept as they are")
		return nil
	}
	var t *Track
	for i := range r.Selected.Subtitles {
		if r.Selected.Subtitles[i].ID == r.Opts.BurnSubs {
			t = &r.Selected.Subtitles[i]
		}
	}
	if t == nil {
		return fmt.Errorf("--burn-subs %d: not a subtitle track of the title kept (see --list, and --subs-lang)", r.Opts.BurnSubs)
	}
	b := newPGSBurner()
	if !tap(t.ID, b.feed) {
		return fmt.Errorf("--burn-subs %d: not a PGS subtitle track", t.ID)
	}
	r.burn, r.burnSeq = b, -1
	if r.offsetSequence != nil {
		r.burnSeq = r.offsetSequence(*t)
	}
	switch {
	case r.to3D:
		r.Report.Report("burning subtitle track %d into the picture, just in front of the made 3D picture beneath it", t.ID)
	case r.burnSeq == frontMost:
		r.Report.Report("burning subtitle track %d into the picture, at the offset nearest the viewer at each moment (the source does not say which it follows)", t.ID)
	case r.burnSeq < 0:
		r.Report.Report("burning subtitle track %d into the picture, at the screen (it follows no offset sequence)", t.ID)
	default:
		r.Report.Report("burning subtitle track %d into the picture, at the depth of offset sequence %d", t.ID, r.burnSeq)
	}
	return nil
}

// check2D says what a 2D conversion leaves aside.
func (r *Runner) check2D() {
	if r.Opts.Subs3D.threeD() {
		if r.Explicit["subs-3d"] {
			r.Report.Report("2D: --subs-3d does not apply; the subtitles are kept as they are")
		}
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
	tracks, _, err := r.ListSource(ctx)
	return tracks, err
}

// ListSource is ListTracks with the source's running time: a Matroska
// file's, a disc title's playlist's (0 when the source does not say).
func (r *Runner) ListSource(ctx context.Context) ([]Track, time.Duration, error) {
	if unsupportedContainer(r.Opts.Input) {
		return nil, 0, fmt.Errorf("cannot read %s: the source must be a Blu-ray, a Matroska file or an MP4 or QuickTime file", filepath.Base(r.Opts.Input))
	}
	if isMatroska(r.Opts.Input) {
		src, tracks, err := probeMatroska(ctx, r.Opts.Input)
		if err != nil {
			return nil, 0, err
		}
		return tracks, src.duration, nil
	}
	// Reading the playlists and the first megabytes of the feature's stream
	// is enough, wherever the disc is: nothing is extracted.
	src, err := resolveGo(r.Opts.Input, r.Opts.Playlist, r.Opts.TwoD, r.Report)
	if err != nil {
		return nil, 0, err
	}
	defer src.close()
	tracks, err := probeGo(ctx, src)
	return tracks, src.duration, err
}

// TrackKind is the --list name of a track's kind.
func TrackKind(t Track) string {
	switch t.Kind() {
	case KindBaseView:
		return "video (base view)"
	case KindDependentView:
		return "video (dependent)"
	case KindEnhancement:
		return "video (DV layer)"
	case KindAudio:
		return "audio"
	case KindSubtitle:
		return "subtitle"
	}
	return "other"
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
	if r.Opts.SubsOCR {
		dir, err := os.MkdirTemp(r.Opts.TempDir, "bdtools-ocr-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(dir) }()
		var out []extra
		for i, e := range extras {
			out = append(out, e)
			if e.track.StreamID != "S_HDMV/PGS" {
				continue
			}
			t, ok, err := r.ocrTrack(e, 20000+i, dir)
			if err != nil {
				return fmt.Errorf("--subs-ocr: track %d: %w", e.track.ID, err)
			}
			if ok {
				out = append(out, t)
			}
		}
		extras = out
	}
	if len(r.Opts.SubsFiles) > 0 {
		dir, err := os.MkdirTemp(r.Opts.TempDir, "bdtools-subs-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(dir) }()
		for i, f := range r.Opts.SubsFiles {
			e, err := subsFileTrack(f, 10000+i, dir)
			if err != nil {
				return fmt.Errorf("--subs-file: %w", err)
			}
			lang := e.track.Lang
			if lang == "" {
				lang = "und"
			}
			r.Report.Report("subtitles: adding %s (%s, %s)", filepath.Base(f.Path), e.track.Type, lang)
			extras = append(extras, e)
		}
	}
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
