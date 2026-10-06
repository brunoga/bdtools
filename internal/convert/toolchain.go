// Package convert drives the conversion of a frame-packed Blu-ray 3D source (MVC)
// into a side-by-side MKV that an ordinary decoder can play.
//
// MVC stores the second eye as a dependent view of an AVC base view. Players
// that decode it are rare — Plex does not — so a 3D Blu-ray is unwatchable in
// most libraries even though it carries a full 1080p image per eye. Converting
// it to side-by-side keeps both eyes at full resolution in a single frame that
// any H.264/HEVC decoder handles.
//
// Everything but the encode runs in process:
//
//	demuxer   read a Blu-ray (image, folder, playlist, m2ts) or a Matroska
//	          remux in place: the video to the decoder, audio, subtitles and
//	          chapters to the work directory
//	decoder   the MVC decoder of the parent package: both views decoded and
//	          stacked side by side
//	encoder   a GPU through its system library, in process; or x264 / x265
//	          (ffmpeg for software half-SBS, or a GPU without its library)
//	muxer     write the MKV
//
// Installing an encoder program, when one is needed, is left to the
// operator. What this package guarantees is that it will say precisely what
// is missing, and why, rather than failing partway through a multi-hour run.
package convert

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// Tool is an external program the conversion depends on.
type Tool struct {
	// Name is how this package and its messages refer to the tool.
	Name string
	// Binaries are candidate executable names in preference order. Windows
	// builds are found through PATHEXT, so the bare name is enough there too.
	Binaries []string
	// Purpose says what the tool does in the pipeline, so a missing-tool
	// report explains itself without the reader consulting the source.
	Purpose string
	// VersionArgs prints a version. Many of these tools exit non-zero when
	// asked, so output is used whatever the exit status.
	VersionArgs []string
	// Install maps GOOS to a hint. A hint is a starting point, not a
	// guarantee: several of these have no distribution package anywhere.
	Install map[string]string
}

// Encoder selects how the stacked frames are compressed.
type Encoder string

const (
	// EncoderAuto picks the best available for the platform.
	EncoderAuto Encoder = "auto"
	// EncoderSoftware is CPU encoding through the codec's own command-line
	// tool — x264 or x265, depending on the Codec. Available everywhere,
	// slowest, and the only one whose output does not vary with the GPU.
	EncoderSoftware Encoder = "software"
	// EncoderVAAPI is ffmpeg VAAPI — Linux only, needs a supported GPU.
	EncoderVAAPI Encoder = "vaapi"
	// EncoderVideoToolbox is ffmpeg VideoToolbox — macOS only.
	EncoderVideoToolbox Encoder = "videotoolbox"
	// EncoderNVENC is ffmpeg NVENC — NVIDIA, Linux and Windows.
	EncoderNVENC Encoder = "nvenc"
)

// ParseEncoder resolves the name an operator typed. "x264" is accepted for
// EncoderSoftware: it was the name of this setting when H.264 was the only
// output, and it would otherwise silently become an unknown encoder.
func ParseEncoder(s string) Encoder {
	if s == "x264" || s == "x265" {
		return EncoderSoftware
	}
	return Encoder(s)
}

// Codec is the video codec the stacked frames are compressed with. It is a
// separate axis from the Encoder: every encoder here can produce either.
type Codec string

const (
	// CodecH264 is AVC. The safe default — a side-by-side H.264 file plays on
	// anything, including hardware too old to decode HEVC at all.
	CodecH264 Codec = "h264"
	// CodecH265 is HEVC. A full-SBS frame is double width (3840x1080 from a
	// 1080p disc), which is exactly where HEVC's larger coding units pay off,
	// so it is materially smaller at the same quality. The cost is decoder
	// support: HEVC is widely but not universally direct-played, and a client
	// that has to transcode a 3840x1080 stream is worse off than one
	// direct-playing H.264.
	CodecH265 Codec = "h265"
)

// Codecs returns the supported codecs, H.264 first.
func Codecs() []Codec { return []Codec{CodecH264, CodecH265} }

// Valid reports whether c is a codec this package knows.
func (c Codec) Valid() bool {
	for _, k := range Codecs() {
		if k == c {
			return true
		}
	}
	return false
}

// ffmpegEncoder names the ffmpeg encoder for a codec and hardware backend,
// e.g. "hevc_nvenc". Empty for EncoderSoftware, which does not go through
// ffmpeg.
func (c Codec) ffmpegEncoder(enc Encoder) string {
	family := "h264"
	if c == CodecH265 {
		// ffmpeg spells the HEVC encoders "hevc_*", not "h265_*".
		family = "hevc"
	}
	switch enc {
	case EncoderVAAPI:
		return family + "_vaapi"
	case EncoderNVENC:
		return family + "_nvenc"
	case EncoderVideoToolbox:
		return family + "_videotoolbox"
	default:
		return ""
	}
}

// ffmpegSoftwareEncoder names ffmpeg's build of the software encoder for a
// codec. It is the same library the standalone x264 and x265 binaries wrap, so
// the output is equivalent; what differs is that ffmpeg can filter on the way
// in, which is why this route exists at all.
func (c Codec) ffmpegSoftwareEncoder() string {
	if c == CodecH265 {
		return "libx265"
	}
	return "libx264"
}

// streamExt is the extension for the raw elementary stream the encoder
// writes.
func (c Codec) streamExt() string {
	if c == CodecH265 {
		return ".265"
	}
	return ".264"
}

// Encoders returns the encoders that make sense on this platform, best first.
// The list is what the platform *supports*, not what is installed; Detect
// reports availability.
func Encoders(goos string) []Encoder {
	switch goos {
	case "linux":
		return []Encoder{EncoderNVENC, EncoderVAAPI, EncoderSoftware}
	case "darwin":
		return []Encoder{EncoderVideoToolbox, EncoderSoftware}
	case "windows":
		return []Encoder{EncoderNVENC, EncoderSoftware}
	default:
		return []Encoder{EncoderSoftware}
	}
}

// UsesFFmpeg reports whether an encoder is driven through ffmpeg rather than
// the codec's own standalone binary.
func (e Encoder) UsesFFmpeg() bool { return e != EncoderSoftware && e != EncoderAuto }

var (
	toolX264 = Tool{
		Name:        "x264",
		Binaries:    []string{"x264"},
		Purpose:     "encode the stacked frames in software as H.264",
		VersionArgs: []string{"--version"},
		Install: map[string]string{
			"linux":   "install x264",
			"darwin":  "brew install x264",
			"windows": "https://www.videolan.org/developers/x264.html",
		},
	}
	toolX265 = Tool{
		Name:        "x265",
		Binaries:    []string{"x265"},
		Purpose:     "encode the stacked frames in software as HEVC",
		VersionArgs: []string{"--version"},
		Install: map[string]string{
			"linux":   "install x265",
			"darwin":  "brew install x265",
			"windows": "https://www.videolan.org/developers/x265.html",
		},
	}
	toolFFmpeg = Tool{
		Name:        "ffmpeg",
		Binaries:    []string{"ffmpeg"},
		Purpose:     "encode the stacked frames with a hardware encoder",
		VersionArgs: []string{"-version"},
		Install: map[string]string{
			"linux":   "install ffmpeg",
			"darwin":  "brew install ffmpeg",
			"windows": "https://ffmpeg.org/download.html",
		},
	}
)

// Required returns the program needed on goos to produce codec with the
// given encoder through an external program: ffmpeg for a hardware encoder
// (or software half-SBS), else whichever of x264 / x265 matches the codec.
// The demux, decode and mux are built in and need nothing.
//
// RequiredFor answers for any options, including a GPU encoding in process.
func Required(goos string, enc Encoder, codec Codec, viaFFmpeg bool) []Tool {
	return []Tool{encoderTool(enc, codec, viaFFmpeg)}
}

// RequiredFor returns the tools a run with these options needs: none for a
// remux or a GPU encoding in process, otherwise the encoder program.
func RequiredFor(goos string, o Options) []Tool {
	if o.Remux || o.NativeGPU {
		return nil
	}
	return Required(goos, o.Encoder, o.Codec, o.EncodesViaFFmpeg())
}

// encoderTool is the program that runs the encode. The runner resolves the
// binary from this and the plan builds the arguments from the same pair, so
// they cannot disagree about which program is being driven — a mismatch would
// invoke x264 with x265's flags.
func encoderTool(enc Encoder, codec Codec, viaFFmpeg bool) Tool {
	switch {
	case enc.UsesFFmpeg(), viaFFmpeg:
		return toolFFmpeg
	case codec == CodecH265:
		return toolX265
	default:
		return toolX264
	}
}

// Found is the outcome of looking for one tool.
type Found struct {
	Tool
	// Path is the resolved executable, empty when the tool was not found.
	Path string
	// Version is the first line the tool printed when asked, best-effort.
	Version string
	// Unusable says why a tool that is there cannot do the job: an ffmpeg
	// built without the GPU encoder asked for, say.
	Unusable string
}

// OK reports whether the tool was located and can do its job.
func (f Found) OK() bool { return f.Path != "" && f.Unusable == "" }

// Report is the result of a toolchain check.
type Report struct {
	GOOS    string
	Encoder Encoder
	Codec   Codec
	Tools   []Found
}

// Missing returns the tools that could not be found.
func (r Report) Missing() []Found {
	var out []Found
	for _, f := range r.Tools {
		if !f.OK() {
			out = append(out, f)
		}
	}
	return out
}

// OK reports whether every required tool was found.
func (r Report) OK() bool { return len(r.Missing()) == 0 }

// LookPath is the executable resolver. It is a variable so tests can supply
// their own without creating real executables, which would not be portable
// across the platforms this tool targets.
var LookPath = exec.LookPath

// Detect locates every tool required on this platform for enc.
//
// A version string is best-effort: several of these programs exit non-zero
// when asked for one, so a tool that was found but would not report a
// version is still reported as present.
func Detect(ctx context.Context, goos string, enc Encoder, codec Codec, viaFFmpeg bool) Report {
	return detect(ctx, goos, enc, codec, Required(goos, enc, codec, viaFFmpeg))
}

// DetectFor locates the tools a run with these options needs. A GPU
// encoder reached through ffmpeg is also tried there, with a short encode:
// an ffmpeg can be present and still lack it (Alpine's has no NVENC), which
// would otherwise only show at the encode, after the demux.
func DetectFor(ctx context.Context, goos string, o Options) Report {
	rep := detect(ctx, goos, o.Encoder, o.Codec, RequiredFor(goos, o))
	if _, hw := hwKind(o.Encoder); hw && !o.NativeGPU {
		for i, f := range rep.Tools {
			if f.Name == toolFFmpeg.Name && f.Path != "" && !ProbeEncoder(ctx, o.Encoder, o.Codec, o.VAAPIDevice, true) {
				rep.Tools[i].Unusable = fmt.Sprintf("this ffmpeg cannot encode %s with %s (no %s, or no GPU it can use)",
					o.Codec, o.Encoder, o.Codec.ffmpegEncoder(o.Encoder))
			}
		}
	}
	return rep
}

func detect(ctx context.Context, goos string, enc Encoder, codec Codec, tools []Tool) Report {
	rep := Report{GOOS: goos, Encoder: enc, Codec: codec}
	for _, t := range tools {
		f := Found{Tool: t}
		for _, bin := range t.Binaries {
			if p, err := LookPath(bin); err == nil {
				f.Path = p
				break
			}
		}
		if f.OK() && t.VersionArgs != nil {
			f.Version = probeVersion(ctx, f.Path, t.VersionArgs)
		}
		rep.Tools = append(rep.Tools, f)
	}
	return rep
}

// probeVersion returns the first non-empty line the tool printed, or "".
func probeVersion(ctx context.Context, path string, args []string) string {
	cmd := exec.CommandContext(ctx, path, args...) //nolint:gosec // path came from LookPath
	out, _ := cmd.CombinedOutput()                 // exit status is not meaningful here
	for _, line := range strings.Split(string(out), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	return ""
}

// InstallHint returns the hint for goos, falling back to any other platform's
// so a reader on an unlisted OS still gets a starting point.
func (t Tool) InstallHint(goos string) string {
	if h, ok := t.Install[goos]; ok {
		return h
	}
	if h, ok := t.Install["linux"]; ok {
		return h
	}
	return ""
}

// String renders a report as the operator-facing check output.
func (r Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "platform: %s   encoder: %s   codec: %s\n\n", r.GOOS, r.Encoder, r.Codec)
	for _, f := range r.Tools {
		switch {
		case f.OK() && f.Version != "":
			fmt.Fprintf(&b, "  ok       %-10s %s\n           %s\n", f.Name, f.Path, f.Version)
		case f.OK():
			fmt.Fprintf(&b, "  ok       %-10s %s\n", f.Name, f.Path)
		case f.Path != "":
			fmt.Fprintf(&b, "  UNUSABLE %-10s %s\n           %s\n", f.Name, f.Path, f.Unusable)
		default:
			fmt.Fprintf(&b, "  MISSING  %-10s %s\n", f.Name, f.Purpose)
			if h := f.InstallHint(r.GOOS); h != "" {
				fmt.Fprintf(&b, "           try: %s\n", h)
			}
		}
	}
	switch {
	case len(r.Tools) == 0:
		b.WriteString("  no external tools needed\n")
	case r.OK():
		fmt.Fprintf(&b, "\nall %d required tools present\n", len(r.Tools))
	default:
		fmt.Fprintf(&b, "\n%d of %d tools missing or unusable\n", len(r.Missing()), len(r.Tools))
	}
	return b.String()
}

// DefaultEncoder resolves EncoderAuto to the fastest encoder this machine can
// actually use, falling back to software encoding.
//
// Having ffmpeg is not the same as having a GPU: a stock build advertises
// h264_nvenc on a machine with no NVIDIA card. So each candidate is put through
// a one-frame trial encode rather than merely having its tools located — the
// alternative is choosing an encoder that fails at the encode step, hours into
// a conversion.
func DefaultEncoder(ctx context.Context, goos string, codec Codec, vaapiDevice string) Encoder {
	for _, enc := range Encoders(goos) {
		if enc == EncoderSoftware {
			continue
		}
		// The GPU's own library first: it needs no ffmpeg.
		if ProbeNative(enc, codec, vaapiDevice) {
			return enc
		}
		if !Detect(ctx, goos, enc, codec, false).OK() {
			continue
		}
		// Probed for the codec being produced, not for the encoder in the
		// abstract: a GPU generation can carry an H.264 encoder and no HEVC
		// one, so "nvenc works here" is not an answer on its own.
		if ProbeEncoder(ctx, enc, codec, vaapiDevice, false) {
			return enc
		}
	}
	return EncoderSoftware
}

// SupportsEncoder reports whether enc is meaningful on goos. VAAPI on macOS
// and VideoToolbox on Linux are configuration errors worth catching early
// rather than at the first ffmpeg invocation, hours into a run.
func SupportsEncoder(goos string, enc Encoder) bool {
	if enc == EncoderAuto {
		return true
	}
	for _, e := range Encoders(goos) {
		if e == enc {
			return true
		}
	}
	return false
}

// CurrentGOOS reports the running platform, indirected for tests.
var CurrentGOOS = runtime.GOOS
