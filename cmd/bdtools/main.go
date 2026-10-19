// Command bdtools converts a frame-packed Blu-ray 3D source (MVC) into a
// side-by-side MKV that an ordinary decoder can play.
//
// MVC keeps the second eye as a dependent view of an AVC base view. Very few
// players decode it — Plex does not — so a 3D Blu-ray sits in a library
// unwatchable despite carrying a full image per eye. Side-by-side puts both
// eyes in one frame that any H.264 decoder handles.
//
// The conversion is done by external programs, and installing them is left to
// the operator. What this command promises is that `--check` will say exactly
// which are missing and what each is for, rather than failing partway through
// a run that takes hours.
//
// Usage:
//
//	bdtools --check
//	bdtools --dry-run --input 00800.m2ts --output "Life of Pi (2012).mkv"
//	bdtools --input BDMV/PLAYLIST/00800.mpls --output "Life of Pi (2012).mkv"
//
// The source is a .iso disc image, a BDMV directory (or its parent), a .mpls
// playlist, an .m2ts, or a Matroska remux of a 3D disc. Everything is read in
// place — an image with no mounting and no root — and for a disc the feature
// playlist is chosen by reading the playlists.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"syscall"
	"time"

	"github.com/brunoga/bdtools/internal/convert"
	pversion "github.com/brunoga/bdtools/internal/version"
)

// version is overridden at build time with
// -ldflags="-X main.version=...". Left alone, version.Resolve falls back to
// the module version the Go toolchain embeds, so a `go install` of a tagged
// version reports that tag rather than the placeholder.
var version = pversion.Placeholder

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(argv []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("bdtools", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		check    = fs.Bool("check", false, "report which external tools are present and which are missing, then exit")
		dryRun   = fs.Bool("dry-run", false, "print the commands that would run, without running them")
		input    = fs.String("input", "", "source: a .iso disc image, a BDMV directory, an .m2ts, a .mpls playlist, an MKV or MP4; or a directory of them (a batch, into --output a directory)")
		playlst  = fs.String("playlist", "", "the title to read from a disc image or folder, by playlist (e.g. 00800), instead of the one the playlists suggest")
		output   = fs.String("output", "", "destination .mkv (or .m2ts, .ts: a --remux keeping a 3D disc's MVC)")
		tempDir  = fs.String("temp", "", "scratch directory for the audio, subtitles and encoded video (default: alongside the output)")
		spFOV    = fs.Float64("spatial-fov", 90, "--layout mvhevc: the horizontal field of view of each eye's picture, in degrees, as the file states it")
		spBase   = fs.Float64("spatial-baseline", 63, "--layout mvhevc: the distance between the two cameras, in millimetres, as the file states it (Apple's Photos marks a file spatial only with it or a disparity)")
		spDisp   = fs.Float64("spatial-disparity", 0, "--layout mvhevc: a shift of the two views, -1 to 1 of the picture's width (0: none)")
		aq       = fs.Float64("aq", 0, "the built-in encoder's adaptive quantisation strength (--layout mvc): each macroblock's quantiser moved by its energy, flat and dark ones lower; 0 (the default) leaves every macroblock at the picture's")
		layout   = fs.String("layout", string(convert.LayoutFullSBS), "full (1080p per eye side by side), half (960p per eye, ~half the size), ou (1080p per eye over-under, the left on top), half-ou (540p per eye over-under), mvhevc (MV-HEVC in an .mp4 or .mov for Apple devices and the Meta Quest, coded by NVENC or, without it, the encoder here) or mvc (Blu-ray 3D's format: an H.264 left eye any player shows in 2D, with the right eye as a second view that 3D players frame pack; --codec h264, x264 for the left eye)")
		encoder  = fs.String("encoder", string(convert.EncoderAuto), "auto, software, vaapi, videotoolbox, nvenc, mediafoundation (Windows; also mf), builtin (the encoders here: H.264, HEVC and AV1) or copy (--layout mvc: the source's left eye as it is, only the right eye coded)")
		codec    = fs.String("codec", string(convert.CodecH265), "output video codec: h265 (HDR10, HDR10+ and Dolby Vision kept; what home theatre players play), h264 (plays anywhere, 8-bit, larger) or av1 (smaller again, newest decoders)")
		crf      = fs.Int("crf", 0, "quality target, 0-51; lower is better (not comparable between codecs); default: by the source, 20 for 3D, 18 for Ultra HD, 16 for HD")
		depth    = fs.Int("bit-depth", 0, "output bit depth: 8, or 10 for h265 and av1 (finer precision in the encoder: less banding, a few percent smaller); default: 10 when the codec and encoder can, 8 for --layout mvhevc")
		preset   = fs.String("preset", "slow", "software encoder speed/efficiency preset")
		decThr   = fs.Int("decode-threads", 0, "pictures the MVC decoder works on at once (0 = all CPUs)")
		gpuAPI   = fs.String("gpu-api", string(convert.GPUBuiltin), "how a GPU encoder is driven: builtin (its system library, in process; ffmpeg when that is missing) or ffmpeg")
		vaapi    = fs.String("vaapi-device", "/dev/dri/renderD128", "render node for VAAPI encoding")
		swapLR   = fs.Bool("swap-lr", false, "exchange the eyes (default: taken from the disc's own base-view marking)")
		list     = fs.Bool("list", false, "print the source's tracks and exit, to see what the track filters can select")
		listJSON = fs.Bool("json", false, "with --list: the tracks (and the source's running time) as JSON, for a script")
		noComm   = fs.Bool("no-commentary", false, "leave out commentary audio and subtitle tracks (flagged so, or with comment in their name)")
		defAudio = fs.String("default-audio", "", "make the first kept audio track in this language the default, e.g. jpn for a film watched in its own language (default: the first track)")
		defSubs  = fs.String("default-subs", "", "make the first full subtitle track in this language (not forced, SDH only if there is no other) the default, e.g. eng (default: none)")
		audioLng = fs.String("audio-lang", "", "keep only audio in these languages, e.g. eng or eng,fra (default: every track)")
		audioCdc = fs.String("audio-codec", "", "keep only audio matching these codecs, e.g. truehd or dts,ac3 (default: every track)")
		audioBst = fs.Bool("audio-best", false, "of the audio tracks that match, keep only the highest quality one (lossless, then channels, then bitrate), of each --audio-lang language")
		nameCdc  = fs.Bool("name-audio-codec", false, "append the kept audio codec to the output filename, e.g. \"Film 3D FSBS.TrueHD-Atmos.mkv\"")
		nameDet  = fs.Bool("name-details", false, "append the layout, resolution, HDR (DV, HDR10+, HDR10, HLG), codec, quality, encoder and main audio track to the output filename, e.g. \"Film (2016) 3D FSBS 1080p HEVC 10bit CQ26 NVENC TrueHD-Atmos 7.1.mkv\"")
		keepFall = fs.Bool("keep-fallback", false, "keep the lossy core embedded in a lossless track (the AC-3 inside TrueHD, the DTS inside DTS-HD) instead of dropping it")
		remux    = fs.Bool("remux", false, "copy the disc's MVC video out with no re-encoding, keeping only the selected tracks; output must be .m2ts and needs a player that decodes MVC")
		subsLng  = fs.String("subs-lang", "", "keep only subtitles in these languages, e.g. eng, or none for no subtitles (default: every track)")
		subsCdc  = fs.String("subs-codec", "", "keep only subtitles matching these codecs, e.g. pgs (default: every track)")
		twoD     = fs.Bool("2d", false, "convert as a 2D film: the picture of a 2D source (which is converted so anyway), or a 3D source's base view on its own")
		decoder  = fs.String("decoder", "auto", "how a 2D source's video is decoded: auto (on the GPU when one can, else the decoders here), gpu, or cpu (never the GPU); 3D always decodes here")
		dvFEL    = fs.String("dv-fel", "compose", "a Dolby Vision full enhancement layer (FEL) in a conversion: compose it into the picture (profile 8.1 out); keep it as a layer, rebuilt for the encoded base layer, or reencode the source's (profile 7 out, --codec h265, --encoder nvenc, software or builtin); or drop it (the HDR10 base layer as it is)")
		cropF    = fs.String("crop", "none", "take TOP:BOTTOM:LEFT:RIGHT samples (even numbers) off a 2D conversion's picture, black bars say, before any --scale; auto finds the black bars")
		toneMap  = fs.Bool("tonemap", false, "make an HDR picture (HDR10, HLG, Dolby Vision, HDR10+) SDR, BT.709, for screens that show HDR wrongly: BT.2390's curve from each shot's brightest where the source says, its colours into BT.709's")
		scaleF   = fs.String("scale", "none", "fit a 2D conversion's picture in a box, keeping its shape and never enlarging it: 2160p, 1440p, 1080p, 720p, 576p, 480p or WIDTHxHEIGHT")
		dvELCRF  = fs.Int("dv-el-crf", 0, "the enhancement layer's quality when --dv-fel keeps it as a layer, 0-51 (default: --crf plus 12)")
		audioTx  = fs.String("audio-transcode", "ac3", "the codec an MP4 output's DTS and PCM tracks are coded in: ac3 (640 kb/s for 5.1) or aac (96 kb/s a channel)")
		inLayout = fs.String("input-layout", "auto", "how a 2D-coded source's frame holds a 3D pair: auto (as a Matroska file says), 2d (one picture), full or half (side by side), ou or half-ou (over-under, the left eye on top)")
		burnSubs = fs.Int("burn-subs", 0, "an MP4 or QuickTime output (which carries no subtitle track): draw this subtitle track (its id, as --list shows it) into both eyes at the disc's depth; an MV-HEVC file carries no subtitle track")
		subs3D   = fs.String("subs-3d", "both", "off: subtitles as the disc has them, for a player that places them in 3D itself; on: drawn in both halves of the frame at the disc's depth, for players that show the frame as it is; both: the 3D track after each flat one")
		keepTemp = fs.Bool("keep-temp", false, "leave the work directory's files behind instead of deleting them")
		restart  = fs.Bool("restart", false, "encode from the start, ignoring the video an interrupted run of the same command left to resume from")
		quiet    = fs.Bool("quiet", false, "only report errors")
		showVer  = fs.Bool("version", false, "print the version and exit")
	)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "bdtools %s — Blu-ray (or MKV, MP4, MOV) to MKV or MP4: 3D or 2D\n\n", pversion.Resolve(version))
		fmt.Fprintf(stderr, "usage: bdtools [--check] [--dry-run] [--remux] --input SRC --output DST.mkv (.mp4, .mov; .m2ts for an MVC --remux)\n\n"+
			"Converts a Blu-ray into an MKV: a 3D (MVC) source side by side, so an\n"+
			"ordinary decoder can play it, or a 2D one (Ultra HD included, HDR10,\n"+
			"HDR10+ and Dolby Vision kept) as it is, with the tracks you choose;\n"+
			"or remuxes it (--remux) with no re-encoding. The disc is read, decoded\n"+
			"and muxed in process, and a GPU decodes and encodes in process; without\n"+
			"one, the decoders here (H.264/MVC, HEVC, MPEG-2, VC-1) and x264, x265 or\n"+
			"SVT-AV1 do. Run --check to see what is installed.\n\n"+
			"examples:\n"+
			"  bdtools --list --input disc.iso\n"+
			"  bdtools --input disc.iso --output \"Film (2012) 3D FSBS.mkv\" --audio-best\n"+
			"  bdtools --input uhd.iso --output \"Film (2021).mkv\"\n"+
			"  bdtools --remux --input uhd.iso --output \"Film (2021).mkv\" --audio-lang eng\n"+
			"More: https://github.com/brunoga/bdtools/tree/main/cmd/bdtools#common-tasks\n\nflags:\n")
		fs.PrintDefaults()
	}
	vsize := fs.String("video-size", "", "the video's size to aim at (4.5G, 700M, 3GiB): the CRF is found for it by encoding a few seconds at several places first")
	startF := fs.String("start", "", "convert from this time of the title or file on: [[H:]M:]S, or 1h2m3s")
	endF := fs.String("end", "", "convert up to this time (the end not included): as --start")
	durF := fs.String("duration", "", "convert this long, from --start: as --start")
	compareF := fs.String("compare", "", "compare this file (an output) with --input (its source), and stop: PSNR, SSIM and VMAF, measured here as libvmaf measures them")
	compareSkip := fs.Int("compare-skip", 0, "with --compare: source pictures before the output's first")
	compareEvery := fs.Int("compare-every", 1, "with --compare: measure one picture in this many")
	compareCSV := fs.String("compare-csv", "", "with --compare: each picture's measures to this CSV file")
	watch := fs.Bool("watch", false, "with --input a directory: go on converting the sources that arrive in it")
	batchExt := fs.String("ext", "", "with --input a directory: the outputs' kind (mkv, mp4, mov; default mkv, or the --for device's)")
	forDev := fs.String("for", "", "a player's settings, where the command line gives none: "+convert.DeviceNames())
	subsOCR := fs.Bool("subs-ocr", false, "read each PGS subtitle track's pictures as text: an SRT track beside it in Matroska, the only one in MP4 (tx3g); Latin-script languages")
	var subsFiles []string
	fs.Func("subs-file", "add a subtitle file (SRT, ASS, SSA, WebVTT) as a track, LANG:PATH or PATH (the language then from its name: film.en.srt, film.eng.forced.srt); repeat for more", func(v string) error {
		subsFiles = append(subsFiles, v)
		return nil
	})
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	if *compareF != "" {
		return runCompare(*input, *compareF, *compareSkip, *compareEvery, *compareCSV, *tempDir, *quiet, stdout, stderr)
	}
	if *input != "" && isBatch(*input) {
		ext := ".mkv"
		if d, err := convert.DeviceFor(*forDev); err == nil && *forDev != "" && d.Ext != "" {
			ext = d.Ext
		}
		if *batchExt != "" {
			ext = "." + strings.TrimPrefix(*batchExt, ".")
		}
		return runBatch(argv, *input, *output, ext, *watch, stdout, stderr)
	}
	if *showVer {
		fmt.Fprintf(stdout, "bdtools %s\n", pversion.Resolve(version))
		return 0
	}

	// A device preset: its settings where the command line has none.
	if *forDev != "" {
		d, err := convert.DeviceFor(*forDev)
		if err != nil {
			fmt.Fprintln(stderr, "bdtools:", err)
			return 2
		}
		set := map[string]bool{}
		fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
		for name, v := range d.Flags {
			if !set[name] {
				if err := fs.Set(name, v); err != nil {
					fmt.Fprintln(stderr, "bdtools:", err)
					return 2
				}
			}
		}
		if d.Ext != "" && *output != "" && !strings.EqualFold(filepath.Ext(*output), d.Ext) {
			fmt.Fprintf(stderr, "bdtools: warning: --for %s plays %s files best, not %s\n", *forDev, d.Ext, filepath.Ext(*output))
		}
	}

	// The options given, as opposed to left at their defaults: what the
	// source does not decide, and what the run reports as chosen.
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })

	goos := runtime.GOOS
	// BDTOOLS_CPUPROFILE names a file for a CPU profile of the run, for
	// finding where the time goes (go tool pprof).
	if path := os.Getenv("BDTOOLS_CPUPROFILE"); path != "" {
		f, err := os.Create(path) //nolint:gosec // the user's own path
		if err != nil {
			fmt.Fprintf(stderr, "bdtools: %v\n", err)
			return 2
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			fmt.Fprintf(stderr, "bdtools: %v\n", err)
			return 2
		}
		defer func() { pprof.StopCPUProfile(); _ = f.Close() }()
	}
	// Interrupted, a conversion stops cleanly: the video segments it
	// finished stay in the work directory for the same command to resume
	// from, and the rest is removed.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cod := convert.Codec(*codec)
	if convert.Layout(*layout) == convert.LayoutMVC && !given["codec"] {
		cod = convert.CodecH264 // MVC is H.264's
	}
	if convert.Layout(*layout) == convert.LayoutMVHEVC && !given["codec"] {
		cod = convert.CodecH265 // MV-HEVC is HEVC's
	}
	if !cod.Valid() {
		fmt.Fprintf(stderr, "bdtools: unknown codec %q\n", cod)
		return 2
	}
	enc := convert.ParseEncoder(*encoder)
	if !convert.SupportsEncoder(goos, enc) {
		fmt.Fprintf(stderr, "bdtools: encoder %q is not available on %s\n", enc, goos)
		return 2
	}
	if enc == convert.EncoderAuto && convert.Layout(*layout) == convert.LayoutMVC {
		// Players' MVC decoders play the left eye coded here (or by
		// x264), never a GPU-encoded one.
		enc = convert.EncoderBuiltin
	}
	if enc == convert.EncoderAuto && convert.Layout(*layout) == convert.LayoutMVHEVC {
		// The two encoders that code MV-HEVC: NVENC, much the faster, when
		// it works here, else the encoder in this program.
		enc = convert.EncoderBuiltin
		if convert.ProbeNative(convert.EncoderNVENC, convert.CodecH265, 8, *vaapi) {
			enc = convert.EncoderNVENC
		}
	}
	if enc == convert.EncoderAuto {
		// An encoder for the depth the run will settle on: 10 bits by
		// default, unless H.264.
		d := *depth
		if d == 0 && cod != convert.CodecH264 {
			d = 10
		}
		enc = convert.DefaultEncoder(ctx, goos, cod, d, *vaapi)
	}

	ga := convert.GPUAPI(*gpuAPI)
	if !ga.Valid() {
		fmt.Fprintf(stderr, "bdtools: unknown GPU API %q (want builtin or ffmpeg)\n", ga)
		return 2
	}

	o := convert.DefaultOptions()
	o.GPUAPI = ga
	o.Input, o.Output, o.TempDir, o.Playlist = *input, *output, *tempDir, *playlst
	o.Layout, o.Encoder, o.Codec = convert.Layout(*layout), enc, cod
	o.CRF, o.Preset, o.VAAPIDevice = *crf, *preset, *vaapi
	if !given["crf"] {
		o.CRF = convert.CRFAuto
	}
	o.BitDepth = *depth
	if o.BitDepth == 0 && !*remux && cod != convert.CodecH264 && enc != convert.EncoderMediaFoundation {
		// 10 bits by default, except for a remux, which keeps the disc's,
		// and for MV-HEVC, whose players take 8 (the Quest's decode 10
		// slowly or not at all; iPhone files are 8-bit)
		o.BitDepth = 10
		if o.Layout == convert.LayoutMVHEVC {
			o.BitDepth = 8
		}
	}
	o.Subs3D = convert.Subs3D(*subs3D)
	o.BurnSubs = *burnSubs
	o.AudioTranscode = *audioTx
	switch l := convert.Layout(*inLayout); l {
	case "2d":
	case convert.InputAuto, convert.LayoutFullSBS, convert.LayoutHalfSBS, convert.LayoutFullOU, convert.LayoutHalfOU:
		o.InputLayout = l
	default:
		fmt.Fprintf(stderr, "bdtools: --input-layout %q: want auto, 2d, full, half, ou or half-ou\n", *inLayout)
		return 2
	}
	if (*remux || o.Layout == convert.LayoutMVC || o.Layout == convert.LayoutMVHEVC) && !given["subs-3d"] {
		// A remux keeps the disc's own; MVC's 3D player places them; an
		// MV-HEVC file carries none.
		o.Subs3D = convert.Subs3DOff
	}
	o.TwoD = *twoD
	o.Decoder = convert.Decoder(*decoder)
	if o.Decoder == "auto" {
		o.Decoder = convert.DecoderAuto
	}
	o.DVFEL, o.DVELCRF = convert.FEL(*dvFEL), *dvELCRF
	var err error
	if o.Crop, err = convert.ParseCrop(*cropF); err != nil {
		fmt.Fprintln(stderr, "bdtools:", err)
		return 2
	}
	o.ToneMap = *toneMap
	if *vsize != "" {
		n, err := convert.ParseSize(*vsize)
		if err != nil {
			fmt.Fprintln(stderr, "bdtools: --video-size", err)
			return 2
		}
		o.VideoSize = n
	}
	for _, t := range []struct {
		name string
		v    *string
		to   *time.Duration
	}{{"start", startF, &o.Trim.Start}, {"end", endF, &o.Trim.End}} {
		if *t.v == "" {
			continue
		}
		d, err := convert.ParseClock(*t.v)
		if err != nil {
			fmt.Fprintf(stderr, "bdtools: --%s %v\n", t.name, err)
			return 2
		}
		*t.to = d
	}
	if *durF != "" {
		d, err := convert.ParseClock(*durF)
		if err != nil || d <= 0 || *endF != "" {
			fmt.Fprintln(stderr, "bdtools: --duration wants a length, and no --end beside it")
			return 2
		}
		o.Trim.End = o.Trim.Start + d
	}
	o.SubsOCR = *subsOCR
	for _, f := range subsFiles {
		o.SubsFiles = append(o.SubsFiles, convert.ParseSubsFile(f))
	}
	if o.Scale, err = convert.ParseScale(*scaleF); err != nil {
		fmt.Fprintln(stderr, "bdtools:", err)
		return 2
	}
	if o.DVFEL == "compose" {
		o.DVFEL = convert.FELCompose
	}
	o.SwapLR = *swapLR
	o.DecodeThreads = *decThr
	o.AQ = *aq
	o.SpatialFOV, o.SpatialBaseline, o.SpatialDisparity = *spFOV, *spBase, *spDisp
	o.KeepFallback = *keepFall
	o.Remux = *remux
	if !o.Remux {
		convert.ResolveGPU(&o)
	}

	// After the options are assembled, so --check answers for the settings
	// given: half-SBS or an eye swap moves software encoding onto ffmpeg,
	// which is a different tool to look for.
	if *check {
		rep := convert.DetectFor(ctx, goos, o)
		fmt.Fprint(stdout, rep.String())
		if !rep.OK() {
			return 1
		}
		return 0
	}
	o.Audio = convert.TrackFilter{Langs: convert.ParseList(*audioLng), Codecs: convert.ParseList(*audioCdc), Best: *audioBst, NoCommentary: *noComm}
	o.Subs = convert.TrackFilter{Langs: convert.ParseList(*subsLng), Codecs: convert.ParseList(*subsCdc), NoCommentary: *noComm}
	o.DefaultAudio, o.DefaultSubs = strings.TrimSpace(*defAudio), strings.TrimSpace(*defSubs)

	// Listing comes before the plan is built, because it needs no output file
	// and asking for one to see what a disc holds would be a silly thing to
	// require.
	if *list {
		if o.Input == "" {
			fmt.Fprintf(stderr, "bdtools: --list needs --input\n")
			return 2
		}
		tracks, dur, err := convert.NewRunner(goos, o, nil).ListSource(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "bdtools: %v\n", err)
			return 1
		}
		if *listJSON {
			return writeListJSON(stdout, stderr, tracks, dur)
		}
		fmt.Fprintf(stdout, "%-5s %-18s %-5s %-20s %s\n", "track", "kind", "lang", "codec", "info")
		fmt.Fprint(stdout, convert.DescribeTracks(tracks))
		return 0
	}

	plan, err := convert.BuildPlan(goos, o)
	if err != nil {
		fmt.Fprintf(stderr, "bdtools: %v\n", err)
		fs.Usage()
		return 2
	}

	if *dryRun {
		fmt.Fprint(stdout, plan.String())
		return 0
	}

	// Refuse to start rather than fail hours in. A conversion is long enough
	// that a missing tool discovered at step three is a wasted evening.
	if rep := convert.DetectFor(ctx, goos, o); !rep.OK() {
		fmt.Fprint(stderr, rep.String())
		fmt.Fprintf(stderr, "\nbdtools: refusing to start with tools missing; see --check\n")
		return 1
	}

	report := convert.Reporter(func(format string, args ...any) {
		fmt.Fprintf(stderr, "bdtools: "+format+"\n", args...)
	})
	if *quiet {
		report = nil
	}

	runner := convert.NewRunner(goos, o, report)
	runner.Version = pversion.Resolve(version)
	runner.KeepTemp = *keepTemp
	runner.Restart = *restart
	runner.Explicit = given
	// Whether --swap-lr was given, as opposed to merely defaulting to false:
	// a disc that marks its base view as the right eye sets this itself, but
	// must not override someone who said otherwise.
	runner.SwapLRSet = given["swap-lr"]
	if err := runner.Run(ctx); err != nil {
		fmt.Fprintf(stderr, "bdtools: %v\n", err)
		return 1
	}

	final := o.Output
	if *nameCdc {
		// Renamed after the fact rather than decided up front, because the
		// codec is not known until the source has been probed, and probing a
		// disc image twice to learn a filename would cost as much as the
		// first stage of the conversion.
		if renamed, err := appendCodecToName(o.Output, runner.Selected.Audio); err != nil {
			report.Report("warning: keeping %s: %v", o.Output, err)
		} else {
			final = renamed
		}
	}
	if *nameDet {
		// What the run settled: the encoder auto chose, the bit depth of
		// the source, 2D or 3D.
		done := runner.Opts
		target := convert.WithDetails(final, convert.DetailTags(done, runner.Selected.Audio, runner.Height, runner.PictureTags()))
		if target != final {
			if err := os.Rename(final, target); err != nil {
				report.Report("warning: keeping %s: %v", final, err)
			} else {
				final = target
			}
		}
	}
	report.Report("done: %s", final)
	// The final path on stdout, so a script driving this does not have to
	// guess at the name or parse the progress output.
	fmt.Fprintln(stdout, final)
	return 0
}

// appendCodecToName inserts the kept audio codec before the extension:
// "Film 3D FSBS.mkv" becomes "Film 3D FSBS.TrueHD-Atmos.mkv".
//
// With several audio tracks kept the first is used, which is the disc's own
// order and so its primary mix. With none kept the name is left alone: a file
// labelled with a codec it does not contain would be worse than an unlabelled
// one.
func appendCodecToName(path string, audio []convert.Track) (string, error) {
	if len(audio) == 0 {
		return path, nil
	}
	slug := convert.CodecSlug(audio[0])
	if slug == "" {
		return path, nil
	}
	ext := filepath.Ext(path)
	target := strings.TrimSuffix(path, ext) + "." + slug + ext
	if target == path {
		return path, nil
	}
	if err := os.Rename(path, target); err != nil {
		return path, fmt.Errorf("renaming to %s: %w", filepath.Base(target), err)
	}
	return target, nil
}

// listedTrack is a track as --list --json gives it.
type listedTrack struct {
	ID         int    `json:"id"`
	Kind       string `json:"kind"`
	Lang       string `json:"lang"`
	Codec      string `json:"codec"`
	StreamID   string `json:"stream_id"`
	Info       string `json:"info"`
	Name       string `json:"name,omitempty"`
	Forced     bool   `json:"forced,omitempty"`
	Commentary bool   `json:"commentary,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
}

// writeListJSON writes --list --json: the running time in seconds and the
// tracks.
func writeListJSON(stdout, stderr io.Writer, tracks []convert.Track, dur time.Duration) int {
	out := struct {
		Duration float64       `json:"duration"`
		Tracks   []listedTrack `json:"tracks"`
	}{Duration: dur.Seconds(), Tracks: []listedTrack{}}
	for _, t := range tracks {
		lang := strings.TrimSpace(t.Lang)
		if lang == "" {
			lang = "und"
		}
		out.Tracks = append(out.Tracks, listedTrack{ID: t.ID, Kind: convert.TrackKind(t), Lang: lang, Codec: t.Type,
			StreamID: t.StreamID, Info: t.Info, Name: t.Name, Forced: t.Forced, Commentary: t.IsCommentary(),
			Width: t.Width, Height: t.Height})
	}
	e := json.NewEncoder(stdout)
	e.SetIndent("", "  ")
	if err := e.Encode(out); err != nil {
		fmt.Fprintf(stderr, "bdtools: %v\n", err)
		return 1
	}
	return 0
}
