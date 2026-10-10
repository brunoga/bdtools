package convert

import (
	"fmt"
	"strings"
)

// CRFAuto is Options.CRF left for the source to decide: the quality
// measured to suit it (see sourceCRF).
const CRFAuto = -1

// The --crf a source gets by default, by kind. Measured with VMAF against
// the source over minute-long clips of each kind (see cmd/bdtools/README.md):
// at these an encode keeps every kind about as near its source, its worst
// pictures included, in an eighth to a quarter of the size on most films.
const (
	crf3D  = 20 // a 3D disc, both views side by side: seen through glasses
	crfUHD = 18 // Ultra HD, HDR10, HDR10+ or Dolby Vision, or SDR
	crfHD  = 16 // 1080p and below, scored against the stricter HD model
)

// sourceKind names the kind of source the defaults are chosen for, from
// its video track: 3D, Ultra HD (above 1080 lines; a Blu-ray's HEVC is
// Ultra HD's), with Dolby Vision when it has it, or HD.
func (r *Runner) sourceKind(video Track, dv bool) (kind string, crf int) {
	switch {
	case !r.flat:
		return "3D", crf3D
	case video.Height > 1080 || video.Height == 0 && video.StreamID == "V_MPEGH/ISO/HEVC":
		if dv {
			return "Ultra HD Dolby Vision", crfUHD
		}
		return "Ultra HD", crfUHD
	}
	return "HD", crfHD
}

// resolveDefaults settles what was left to the source, now its video is
// known, and says what it chose: --crf by the kind of source, and the bit
// depth (10 bits whenever the codec and encoder can: finer precision, less
// banding, from an 8-bit source too). The settings given are reported
// alongside, so the line is the whole of how the video is encoded.
func (r *Runner) resolveDefaults(video Track, dv bool) {
	kind, crf := r.sourceKind(video, dv)
	var chosen, given []string
	note := func(auto bool, s string) {
		if auto {
			chosen = append(chosen, s)
		} else {
			given = append(given, s)
		}
	}
	note(!r.Explicit["codec"], "--codec "+string(r.Opts.Codec))
	auto := r.Opts.BitDepth == 0 || r.Explicit != nil && !r.Explicit["bit-depth"]
	if r.Opts.BitDepth == 0 {
		r.Opts.BitDepth = 8
		if r.Opts.Codec != CodecH264 && r.Opts.Encoder != EncoderMediaFoundation {
			r.Opts.BitDepth = 10
		}
	}
	note(auto, fmt.Sprintf("--bit-depth %d", r.Opts.BitDepth))
	auto = r.Opts.CRF == CRFAuto
	if auto {
		r.Opts.CRF = crf
	}
	note(auto, fmt.Sprintf("--crf %d", r.Opts.CRF))
	if !r.flat {
		note(!r.Explicit["layout"], "--layout "+string(r.Opts.Layout))
		note(!r.Explicit["subs-3d"], "--subs-3d "+string(r.Opts.Subs3D))
	}
	switch {
	case len(chosen) == 0:
		r.Report.Report("video: %s, as given", strings.Join(given, ", "))
	case len(given) == 0:
		r.Report.Report("video: %s (defaults for %s)", strings.Join(chosen, ", "), kind)
	default:
		r.Report.Report("video: %s (defaults for %s), %s as given",
			strings.Join(chosen, ", "), kind, strings.Join(given, ", "))
	}
}
