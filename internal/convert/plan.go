package convert

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/brunoga/mvc/internal/hwenc"
)

// Layout is how the two eyes are arranged in the output frame.
type Layout string

const (
	// LayoutFullSBS keeps both eyes at source width: 3840x1080 from a 1080p
	// disc, so 1920x1080 per eye. This is what a 3D library normally wants —
	// it is the only layout that preserves the disc's resolution.
	LayoutFullSBS Layout = "full"
	// LayoutHalfSBS squeezes both eyes into one source-width frame:
	// 1920x1080 total, so 960x1080 per eye. Half the horizontal detail, and
	// roughly half the bitrate.
	LayoutHalfSBS Layout = "half"
)

// Options configure a conversion.
type Options struct {
	Input   string // .iso, a BDMV directory, a playlist or m2ts, or an MKV remux
	Output  string // destination .mkv
	TempDir string // scratch space for the audio, subtitles and encoded video
	Layout  Layout
	Encoder Encoder
	// Codec is the output video codec. H.264 plays on anything; HEVC is
	// materially smaller for a double-width side-by-side frame.
	Codec Codec
	// Audio and Subs narrow which tracks are carried into the output. Zero
	// values keep every track the disc has, which is the default. Lossless
	// audio dominates the output of a well-compressed conversion — on a clean
	// CG feature the TrueHD track alone can be more than twice the video — so
	// dropping the tracks you will never play is the largest saving available
	// that costs no picture quality.
	Audio TrackFilter
	Subs  TrackFilter
	// KeepFallback keeps the lossy core embedded in a lossless stream — the
	// AC-3 inside TrueHD, the DTS inside DTS-HD — instead of dropping it.
	// It costs space for audio that duplicates the track beside it, and buys
	// a fallback for a player that cannot decode the lossless one.
	KeepFallback bool
	// Remux writes the disc's own streams back out with no re-encoding, so
	// the MVC video survives bit for bit and only the filtered-out tracks are
	// lost. The output is an MPEG-2 transport stream, since that is what MVC
	// travels in; nothing is decoded, stacked or encoded, so the encoder,
	// codec, layout and CRF settings do not apply.
	Remux bool
	// CRF is the quality target. Lower is better; 18 is visually transparent
	// for most sources.
	//
	// It is not comparable across codecs: x265 at a given CRF is roughly a
	// step higher quality — and larger — than x264 at the same number, so the
	// same value yields a better-looking HEVC file rather than a smaller one.
	// Nothing here adjusts it, because silently re-interpreting a number the
	// operator typed is worse than documenting what it means.
	CRF int
	// Preset is the encoder's speed/efficiency trade-off. x264 and x265 take
	// the same preset names.
	Preset string
	// VAAPIDevice is the render node for VAAPI encoding.
	VAAPIDevice string
	// SwapLR exchanges the eyes. Most discs put the left eye in the base view,
	// but not all, and a swapped pair is unwatchable rather than subtly wrong.
	// The decoder stacks the views the other way round, so no filter is
	// involved and any encoder will do.
	SwapLR bool
	// DecodeThreads is how many pictures the decoder works on at once; 0 uses
	// every CPU.
	DecodeThreads int
	// GPUAPI says how a hardware encoder is driven: through its system
	// library in process (the default; empty means it too), or ffmpeg.
	GPUAPI GPUAPI
	// NativeGPU is set by ResolveGPU when the hardware encoder runs in
	// process.
	NativeGPU bool
}

// unsupportedContainer reports whether a source is a container nothing
// here reads. MVC travels on Blu-rays and in Matroska remuxes of them; a VOB
// is a DVD's, which is never 3D.
func unsupportedContainer(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".m4v", ".mov", ".vob":
		return true
	}
	return false
}

// Step is one external command in the conversion.
type Step struct {
	// Name is a short label for progress reporting.
	Name string
	// Argv is the program and its arguments, ready for exec. No shell is
	// involved, so nothing here is quoted or escaped for one.
	Argv []string
	// PipeTo, when set, names the step this one's stdout feeds. The decoder
	// streams raw frames to the encoder rather than writing an intermediate
	// file, which for a feature film is hundreds of gigabytes saved.
	PipeTo string
	// Builtin marks a step that runs inside this process rather than as a
	// program: the MVC decode. Its Argv is the equivalent mvcdec command,
	// shown so a dry run still reads as a transcript.
	Builtin bool
}

// Plan is the full sequence for one conversion.
type Plan struct {
	Steps []Step
	// Intermediates are the files the steps create, for cleanup.
	Intermediates []string
}

// DefaultOptions returns sensible starting settings. Nothing here varies by
// platform — the encoder is resolved separately, by what is installed.
func DefaultOptions() Options {
	return Options{
		Layout:      LayoutFullSBS,
		Encoder:     EncoderAuto,
		Codec:       CodecH264,
		CRF:         18,
		Preset:      "slow",
		VAAPIDevice: "/dev/dri/renderD128",
	}
}

// Validate checks the options are coherent before anything is run. A
// conversion takes hours, so a configuration error must surface immediately
// rather than at the step that trips over it.
func (o Options) Validate(goos string) error {
	if o.Input == "" {
		return fmt.Errorf("no input given")
	}
	if o.Output == "" {
		return fmt.Errorf("no output given")
	}
	if strings.EqualFold(o.Input, o.Output) {
		return fmt.Errorf("input and output are the same file")
	}
	if unsupportedContainer(o.Input) {
		return fmt.Errorf("cannot read %s: the source must be a Blu-ray (a disc image, a BDMV folder, "+
			"a playlist or an m2ts) or a Matroska remux of one", filepath.Base(o.Input))
	}
	if o.Remux {
		// MVC has no home in Matroska that players agree on, so a remux
		// stays in the transport stream the disc already uses.
		if ext := strings.ToLower(filepath.Ext(o.Output)); ext != ".m2ts" && ext != ".ts" {
			return fmt.Errorf("a remux output must be a .m2ts or .ts (got %q); "+
				"MVC video cannot go into a .mkv that players agree on", ext)
		}
		// Everything below describes the decode-and-encode path, which a
		// remux does not take. Saying so beats silently ignoring settings.
		if o.Layout == LayoutHalfSBS {
			return fmt.Errorf("--remux cannot change the layout: it copies the disc's " +
				"MVC video without re-encoding, and half-SBS needs a rescale")
		}
		if o.SwapLR {
			return fmt.Errorf("--remux cannot swap the eyes: it copies the disc's " +
				"MVC video without re-encoding, and the eyes are swapped while stacking them")
		}
		return nil
	}
	if ext := strings.ToLower(filepath.Ext(o.Output)); ext != ".mkv" {
		return fmt.Errorf("output must be a .mkv (got %q)", ext)
	}
	switch o.Layout {
	case LayoutFullSBS, LayoutHalfSBS:
	default:
		return fmt.Errorf("unknown layout %q (want %q or %q)", o.Layout, LayoutFullSBS, LayoutHalfSBS)
	}
	if !o.Codec.Valid() {
		return fmt.Errorf("unknown codec %q (want %s)", o.Codec, codecList(Codecs()))
	}
	if !SupportsEncoder(goos, o.Encoder) {
		return fmt.Errorf("encoder %q is not available on %s (have: %s)",
			o.Encoder, goos, encoderList(Encoders(goos)))
	}
	if o.Codec == CodecAV1 && o.Encoder == EncoderVideoToolbox {
		return fmt.Errorf("VideoToolbox cannot encode AV1: use --encoder software (SVT-AV1) or another codec")
	}
	if o.CRF < 0 || o.CRF > 51 {
		return fmt.Errorf("crf %d out of range 0-51", o.CRF)
	}
	return nil
}

// NeedsFilters reports whether the output requires a filter on the stacked
// frame, which is squeezing it to half width. (Exchanging the eyes is done
// by the decoder while stacking, so it needs none.)
//
// It matters because neither x264 nor x265 has filters. Software encoding that
// needs one is therefore driven through ffmpeg's libx264 or libx265 instead of
// the standalone binary — the same encoder library, reached by a route that
// can filter — rather than being refused, which is what used to happen.
func (o Options) NeedsFilters() bool {
	return o.Layout == LayoutHalfSBS
}

// EncodesViaFFmpeg reports whether ffmpeg runs the encode: always for a
// hardware encoder, for software encoding that needs a filter, and for
// software AV1 when SvtAv1EncApp is not installed but ffmpeg (with its
// libsvtav1) is.
func (o Options) EncodesViaFFmpeg() bool {
	if o.NativeGPU {
		return false
	}
	if o.Encoder.UsesFFmpeg() {
		return true
	}
	if o.Encoder != EncoderSoftware {
		return false
	}
	return o.NeedsFilters() || o.Codec == CodecAV1 && svtViaFFmpeg()
}

// svtViaFFmpeg reports whether software AV1 has to go through ffmpeg: the
// standalone SvtAv1EncApp is missing and ffmpeg is there.
func svtViaFFmpeg() bool {
	if _, err := LookPath("SvtAv1EncApp"); err == nil {
		return false
	}
	_, err := LookPath("ffmpeg")
	return err == nil
}

func codecList(cs []Codec) string {
	s := make([]string, len(cs))
	for i, c := range cs {
		s[i] = string(c)
	}
	return strings.Join(s, " or ")
}

func encoderList(encs []Encoder) string {
	s := make([]string, len(encs))
	for i, e := range encs {
		s[i] = string(e)
	}
	return strings.Join(s, ", ")
}

// halfFilter squeezes a stacked pair back to source width, which is half-SBS.
const halfFilter = "scale=iw/2:ih"

// videoFilters is the chain the encoder applies to the stacked frames, in the
// order they must happen: squeeze, then anything the encoder itself needs
// (VAAPI's upload). Empty when there is nothing to do.
func videoFilters(opts Options, encoderChain string) string {
	var parts []string
	if opts.Layout == LayoutHalfSBS {
		parts = append(parts, halfFilter)
	}
	if encoderChain != "" {
		parts = append(parts, encoderChain)
	}
	return strings.Join(parts, ",")
}

// BuildPlan assembles the conversion for opts. It does not touch the
// filesystem or run anything — the result can be printed for a dry run and is
// what the tests assert against.
func BuildPlan(goos string, opts Options) (*Plan, error) {
	if err := opts.Validate(goos); err != nil {
		return nil, err
	}
	tmp := opts.TempDir
	if tmp == "" {
		tmp = filepath.Dir(opts.Output)
	}
	videoOut := filepath.Join(tmp, "stacked"+opts.Codec.streamExt())
	return builtinPlan(opts, tmp, videoOut), nil
}

// nativeEncodeStep is the encode with the GPU driven in process.
func nativeEncodeStep(opts Options, out string) Step {
	argv := []string{string(opts.Encoder), string(opts.Codec), "qp", strconv.Itoa(opts.CRF)}
	if opts.Layout == LayoutHalfSBS {
		argv = append(argv, "half-SBS")
	}
	return Step{Name: "encode", Builtin: true, Argv: append(argv, "->", out)}
}

// muxStep is the final step: the built-in muxer.
func muxStep(opts Options, videoOut string) Step {
	return Step{Name: "mux", Builtin: true,
		Argv: []string{"write", opts.Output, "(with the audio, subtitles and chapters) from", videoOut}}
}

// builtinPlan is the plan: the source is read once, in place — no extraction from an image, no demuxed copy of either
// view — with the video decoded on the way and the other tracks written to
// the work directory for the mux.
func builtinPlan(opts Options, tmp, videoOut string) *Plan {
	if opts.Remux {
		return &Plan{Steps: []Step{{Name: "remux", Builtin: true,
			Argv: []string{"copy", opts.Input, "->", opts.Output, "(selected tracks, packets untouched)"}}}}
	}
	decode := []string{"read", opts.Input, "->", "mvcdec", "-y4m", "-", "-layout", "sbs"}
	if opts.SwapLR {
		decode = append(decode, "-swap")
	}
	decode = append(decode, "(audio, subtitles and chapters to "+tmp+")")
	p := &Plan{Intermediates: []string{videoOut}}
	enc := encodeStep(opts, videoOut)
	if opts.NativeGPU {
		enc = nativeEncodeStep(opts, videoOut)
	}
	p.Steps = append(p.Steps,
		Step{Name: "demux and decode", Argv: decode, Builtin: true, PipeTo: "encode"},
		enc,
		muxStep(opts, videoOut),
	)
	return p
}

// encodeStep builds the encoder invocation. Every variant reads Y4M on stdin,
// which is what lets the decode stream straight into it. The codec changes the
// encoder name and, for software, which binary is driven; the rate-control
// flags belong to the backend and do not vary with it.
func encodeStep(opts Options, out string) Step {
	name := opts.Codec.ffmpegEncoder(opts.Encoder)
	ff := func(codecArgs []string, chain string) Step {
		argv := []string{"ffmpeg", "-hide_banner", "-y", "-f", "yuv4mpegpipe", "-i", "-"}
		if f := videoFilters(opts, chain); f != "" {
			argv = append(argv, "-vf", f)
		}
		argv = append(argv, codecArgs...)
		return Step{Name: "encode", Argv: append(argv, out)}
	}
	// The GPU encoders take AV1's quantiser as its 0-255 index.
	qp := opts.CRF
	if opts.Codec == CodecAV1 {
		qp = hwenc.AV1QIndex(opts.CRF)
	}
	switch opts.Encoder {
	case EncoderVAAPI:
		// The upload has to come last: the filters before it work on software
		// frames, and once uploaded they cannot.
		return Step{Name: "encode", Argv: append([]string{
			"ffmpeg", "-hide_banner", "-y",
			"-vaapi_device", opts.VAAPIDevice,
			"-f", "yuv4mpegpipe", "-i", "-",
			"-vf", videoFilters(opts, "format=nv12,hwupload"),
			"-c:v", name, "-qp", fmt.Sprint(qp),
		}, out)}
	case EncoderVideoToolbox:
		// VideoToolbox's quality runs the other way, 1 to 100 with higher
		// better; -q:v takes it, and the in-process encoder maps the same.
		return ff([]string{"-c:v", name, "-q:v", fmt.Sprint(int(100*hwenc.VTQuality(opts.CRF) + 0.5))}, "")
	case EncoderNVENC:
		return ff([]string{"-c:v", name, "-rc", "constqp", "-qp", fmt.Sprint(qp)}, "")
	default:
		if opts.Codec == CodecAV1 {
			if opts.EncodesViaFFmpeg() {
				return ff([]string{
					"-c:v", "libsvtav1",
					"-crf", fmt.Sprint(svtCRF(opts.CRF)),
					"-preset", fmt.Sprint(svtPreset(opts.Preset)),
				}, "")
			}
			// SvtAv1EncApp reads Y4M from stdin and writes IVF, which the
			// muxer reads as well as bare OBUs.
			return Step{Name: "encode", Argv: []string{
				"SvtAv1EncApp", "-i", "stdin",
				"--crf", fmt.Sprint(svtCRF(opts.CRF)),
				"--preset", fmt.Sprint(svtPreset(opts.Preset)),
				"-b", out,
			}}
		}
		if opts.NeedsFilters() {
			// The standalone encoders cannot rescale or rearrange the frame,
			// so the same encoder library is reached through ffmpeg, which
			// can. The quality settings mean the same thing to both: ffmpeg
			// passes -crf and -preset straight to the library.
			return ff([]string{
				"-c:v", opts.Codec.ffmpegSoftwareEncoder(),
				"-crf", fmt.Sprint(opts.CRF),
				"-preset", opts.Preset,
			}, "")
		}
		if opts.Codec == CodecH265 {
			// x265 reads stdin through --input, and needs --y4m told to it:
			// it infers the format from the file extension, which "-" has not
			// got.
			return Step{Name: "encode", Argv: []string{
				"x265", "--y4m", "--input", "-",
				"--crf", fmt.Sprint(opts.CRF),
				"--preset", opts.Preset,
				"--output", out,
			}}
		}
		return Step{Name: "encode", Argv: []string{
			"x264", "--demuxer", "y4m",
			"--crf", fmt.Sprint(opts.CRF),
			"--preset", opts.Preset,
			"--output", out, "-",
		}}
	}
}

// String renders a plan as the shell-ish transcript a dry run prints. It is
// for reading, not for execution — the steps are run directly, never a shell.
func (p *Plan) String() string {
	var b strings.Builder
	for i, s := range p.Steps {
		fmt.Fprintf(&b, "# step %d: %s", i+1, s.Name)
		if s.Builtin {
			b.WriteString(" (built in, in this process)")
		}
		b.WriteString("\n")
		b.WriteString(strings.Join(s.Argv, " "))
		if s.PipeTo != "" {
			fmt.Fprintf(&b, "   | (into %q)", s.PipeTo)
		}
		b.WriteString("\n")
	}
	return b.String()
}
