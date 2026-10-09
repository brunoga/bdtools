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
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

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
		input    = fs.String("input", "", "source: a .iso disc image, a BDMV directory, an .m2ts, a .mpls playlist, or an MKV")
		playlst  = fs.String("playlist", "", "the title to read from a disc image or folder, by playlist (e.g. 00800), instead of the one the playlists suggest")
		output   = fs.String("output", "", "destination .mkv (or .m2ts, .ts: a --remux keeping a 3D disc's MVC)")
		tempDir  = fs.String("temp", "", "scratch directory for the audio, subtitles and encoded video (default: alongside the output)")
		layout   = fs.String("layout", string(convert.LayoutFullSBS), "full (1080p per eye) or half (960p per eye, ~half the size)")
		encoder  = fs.String("encoder", string(convert.EncoderAuto), "auto, software, vaapi, videotoolbox, nvenc or mediafoundation (Windows; also mf)")
		codec    = fs.String("codec", string(convert.CodecH264), "output video codec: h264 (plays anywhere), h265 (smaller) or av1 (smaller again, newest decoders)")
		crf      = fs.Int("crf", 18, "quality target, 0-51; lower is better (not comparable between codecs)")
		depth    = fs.Int("bit-depth", 0, "output bit depth: 8, or 10 for h265 and av1 (finer precision in the encoder: less banding, a few percent smaller); default: the source's (10 for a 10-bit source such as an Ultra HD Blu-ray, when the codec can)")
		preset   = fs.String("preset", "slow", "software encoder speed/efficiency preset")
		decThr   = fs.Int("decode-threads", 0, "pictures the MVC decoder works on at once (0 = all CPUs)")
		gpuAPI   = fs.String("gpu-api", string(convert.GPUBuiltin), "how a GPU encoder is driven: builtin (its system library, in process; ffmpeg when that is missing) or ffmpeg")
		vaapi    = fs.String("vaapi-device", "/dev/dri/renderD128", "render node for VAAPI encoding")
		swapLR   = fs.Bool("swap-lr", false, "exchange the eyes (default: taken from the disc's own base-view marking)")
		list     = fs.Bool("list", false, "print the source's tracks and exit, to see what the track filters can select")
		audioLng = fs.String("audio-lang", "", "keep only audio in these languages, e.g. eng or eng,fra (default: every track)")
		audioCdc = fs.String("audio-codec", "", "keep only audio matching these codecs, e.g. truehd or dts,ac3 (default: every track)")
		audioBst = fs.Bool("audio-best", false, "of the audio tracks that match, keep only the highest quality one (lossless, then channels, then bitrate)")
		nameCdc  = fs.Bool("name-audio-codec", false, "append the kept audio codec to the output filename, e.g. \"Film 3D FSBS.TrueHD-Atmos.mkv\"")
		nameDet  = fs.Bool("name-details", false, "append the layout, resolution, codec, quality, encoder and main audio track to the output filename, e.g. \"Film (2016) 3D FSBS 1080p HEVC QP20 NVENC TrueHD-Atmos 7.1.mkv\"")
		keepFall = fs.Bool("keep-fallback", false, "keep the lossy core embedded in a lossless track (the AC-3 inside TrueHD, the DTS inside DTS-HD) instead of dropping it")
		remux    = fs.Bool("remux", false, "copy the disc's MVC video out with no re-encoding, keeping only the selected tracks; output must be .m2ts and needs a player that decodes MVC")
		subsLng  = fs.String("subs-lang", "", "keep only subtitles in these languages, e.g. eng (default: every track)")
		subsCdc  = fs.String("subs-codec", "", "keep only subtitles matching these codecs, e.g. pgs (default: every track)")
		twoD     = fs.Bool("2d", false, "convert as a 2D film: the picture of a 2D source (which is converted so anyway), or a 3D source's base view on its own")
		decoder  = fs.String("decoder", "auto", "how a 2D source's video is decoded: auto (on the GPU when one can, else the decoders here), gpu, or cpu (never the GPU); 3D always decodes here")
		dvFEL    = fs.String("dv-fel", "compose", "a Dolby Vision full enhancement layer (FEL) in a conversion: compose it into the picture (profile 8.1 out); keep it as a layer, rebuilt for the encoded base layer, or reencode the source's (profile 7 out, --codec h265, --encoder nvenc or software); or drop it (the HDR10 base layer as it is)")
		dvELCRF  = fs.Int("dv-el-crf", 0, "the enhancement layer's quality when --dv-fel keeps it as a layer, 0-51 (default: --crf less 6)")
		subs3D   = fs.String("subs-3d", "off", "off: subtitles as the disc has them, for a player that places them in 3D itself; on: drawn in both halves of the frame at the disc's depth, for players that show the frame as it is; both: the 3D track after each flat one")
		keepTemp = fs.Bool("keep-temp", false, "leave the work directory's files behind instead of deleting them")
		restart  = fs.Bool("restart", false, "encode from the start, ignoring the video an interrupted run of the same command left to resume from")
		quiet    = fs.Bool("quiet", false, "only report errors")
		showVer  = fs.Bool("version", false, "print the version and exit")
	)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "bdtools %s — Blu-ray to MKV: 3D side by side, or 2D\n\n", pversion.Resolve(version))
		fmt.Fprintf(stderr, "usage: bdtools [--check] [--dry-run] [--remux] --input SRC --output DST.mkv (or .m2ts for an MVC --remux)\n\n"+
			"Converts a Blu-ray into an MKV: a 3D (MVC) source side by side, so an\n"+
			"ordinary decoder can play it, or a 2D one (Ultra HD included, HDR10,\n"+
			"HDR10+ and Dolby Vision kept) as it is, with the tracks you choose;\n"+
			"or remuxes it (--remux) with no re-encoding. The disc is read, decoded\n"+
			"and muxed in process, and a GPU decodes and encodes in process; without\n"+
			"one, the decoders here (H.264/MVC, HEVC, MPEG-2, VC-1) and x264, x265 or\n"+
			"SVT-AV1 do. Run --check to see what is installed.\n\n"+
			"examples:\n"+
			"  bdtools --list --input disc.iso\n"+
			"  bdtools --input disc.iso --output \"Film (2012) 3D FSBS.mkv\" --codec h265 --audio-best\n"+
			"  bdtools --input uhd.iso --output \"Film (2021).mkv\" --codec h265\n"+
			"  bdtools --remux --input uhd.iso --output \"Film (2021).mkv\" --audio-lang eng\n"+
			"More: https://github.com/brunoga/bdtools/tree/main/cmd/bdtools#common-tasks\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	if *showVer {
		fmt.Fprintf(stdout, "bdtools %s\n", pversion.Resolve(version))
		return 0
	}

	goos := runtime.GOOS
	// Interrupted, a conversion stops cleanly: the video segments it
	// finished stay in the work directory for the same command to resume
	// from, and the rest is removed.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cod := convert.Codec(*codec)
	if !cod.Valid() {
		fmt.Fprintf(stderr, "bdtools: unknown codec %q\n", cod)
		return 2
	}
	enc := convert.ParseEncoder(*encoder)
	if !convert.SupportsEncoder(goos, enc) {
		fmt.Fprintf(stderr, "bdtools: encoder %q is not available on %s\n", enc, goos)
		return 2
	}
	if enc == convert.EncoderAuto {
		enc = convert.DefaultEncoder(ctx, goos, cod, *depth, *vaapi)
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
	o.BitDepth = *depth
	o.Subs3D = convert.Subs3D(*subs3D)
	o.TwoD = *twoD
	o.Decoder = convert.Decoder(*decoder)
	if o.Decoder == "auto" {
		o.Decoder = convert.DecoderAuto
	}
	o.DVFEL, o.DVELCRF = convert.FEL(*dvFEL), *dvELCRF
	if o.DVFEL == "compose" {
		o.DVFEL = convert.FELCompose
	}
	o.SwapLR = *swapLR
	o.DecodeThreads = *decThr
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
	o.Audio = convert.TrackFilter{Langs: convert.ParseList(*audioLng), Codecs: convert.ParseList(*audioCdc), Best: *audioBst}
	o.Subs = convert.TrackFilter{Langs: convert.ParseList(*subsLng), Codecs: convert.ParseList(*subsCdc)}

	// Listing comes before the plan is built, because it needs no output file
	// and asking for one to see what a disc holds would be a silly thing to
	// require.
	if *list {
		if o.Input == "" {
			fmt.Fprintf(stderr, "bdtools: --list needs --input\n")
			return 2
		}
		tracks, err := convert.NewRunner(goos, o, nil).ListTracks(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "bdtools: %v\n", err)
			return 1
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
	// Whether --swap-lr was given, as opposed to merely defaulting to false:
	// a disc that marks its base view as the right eye sets this itself, but
	// must not override someone who said otherwise.
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "swap-lr" {
			runner.SwapLRSet = true
		}
	})
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
		target := convert.WithDetails(final, convert.DetailTags(done, runner.Selected.Audio, runner.Height))
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
