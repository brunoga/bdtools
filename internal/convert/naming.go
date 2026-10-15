package convert

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/brunoga/bdtools/internal/gpu"
)

// DetailTags describes a finished conversion for its file name, in the
// order release names use: layout, resolution per eye, video codec and its
// quality setting, encoder, then the main audio track and its channels,
// e.g. "3D FSBS 1080p HEVC CQ26 NVENC TrueHD-Atmos 7.1", or without the
// layout for a 2D conversion, and with the high dynamic range the output
// carries after the resolution ("2160p DV HDR10 HEVC 10bit CQ24 NVENC").
// height is the source picture height (one eye); picture is the run's
// PictureTags.
func DetailTags(o Options, audio []Track, height int, picture []string) string {
	var tags []string
	switch {
	case o.TwoD:
	case o.Layout == LayoutHalfSBS:
		tags = append(tags, "3D HSBS")
	case o.Layout == LayoutMVC:
		tags = append(tags, "3D MVC")
	default:
		tags = append(tags, "3D FSBS")
	}
	if height > 0 {
		tags = append(tags, fmt.Sprintf("%dp", height))
	}
	tags = append(tags, picture...)
	tags = append(tags, map[Codec]string{CodecH264: "H264", CodecH265: "HEVC", CodecAV1: "AV1"}[o.Codec])
	if o.tenBit() {
		tags = append(tags, "10bit")
	}
	if o.Remux {
		// The disc's own video: no quality setting or encoder to name.
		tags = append(tags, "Remux")
		return joinTags(tags, audio)
	}
	switch o.Encoder {
	case EncoderVideoToolbox:
		// VideoToolbox takes a quality, not a quantiser: name what it got.
		tags = append(tags, fmt.Sprintf("Q%d", qualityPercent(o.CRF)))
	case EncoderSoftware:
		tags = append(tags, fmt.Sprintf("CRF%d", o.CRF))
	case EncoderNVENC:
		// A constant quality, its own number (-cq), for H.264 and HEVC.
		if cq := o.nvencCQ(); cq > 0 {
			tags = append(tags, fmt.Sprintf("CQ%d", cq))
			break
		}
		tags = append(tags, fmt.Sprintf("QP%d", o.CRF))
	default:
		tags = append(tags, fmt.Sprintf("QP%d", o.CRF))
	}
	tags = append(tags, map[Encoder]string{EncoderNVENC: "NVENC", EncoderVAAPI: "VAAPI",
		EncoderVideoToolbox: "VideoToolbox", EncoderMediaFoundation: "MF", EncoderBuiltin: "bdtools", EncoderCopy: "copy",
		EncoderSoftware: map[Codec]string{CodecH264: "x264", CodecH265: "x265", CodecAV1: "SVT-AV1"}[o.Codec]}[o.Encoder])
	return joinTags(tags, audio)
}

// joinTags adds the main audio track's tag and joins them.
func joinTags(tags []string, audio []Track) string {
	if len(audio) > 0 {
		if a := audioTag(audio[0]); a != "" {
			tags = append(tags, a)
		}
	}
	var out []string
	for _, t := range tags {
		if t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, " ")
}

// audioTag names an audio track as releases do: "TrueHD-Atmos 7.1",
// "DTS-HD-MA 7.1", "DDP 5.1".
func audioTag(t Track) string {
	typ, info := strings.ToUpper(t.Type+" "+t.StreamID), strings.ToUpper(t.Info)
	var name string
	switch {
	case strings.Contains(typ, "TRUE"):
		name = "TrueHD"
		if strings.Contains(info, "ATMOS") {
			name += "-Atmos"
		}
	case strings.Contains(typ, "MASTER"):
		name = "DTS-HD-MA"
	case strings.Contains(typ, "DTS-HD"):
		name = "DTS-HD-HRA"
	case strings.Contains(typ, "DTS"):
		name = "DTS"
	case strings.Contains(typ, "E-AC3") || strings.Contains(typ, "EAC3"):
		name = "DDP"
	case strings.Contains(typ, "AC3"):
		name = "DD"
	case strings.Contains(typ, "LPCM") || strings.Contains(typ, "PCM"):
		name = "LPCM"
	default:
		name = CodecSlug(t)
	}
	if m := reChannels.FindStringSubmatch(t.Info); m != nil {
		layout := m[1]
		if m[2] != "" {
			layout += "." + m[2]
		} else {
			layout += ".0"
		}
		name += " " + layout
	}
	return name
}

func qualityPercent(crf int) int { return int(100*gpu.VTQuality(crf) + 0.5) }

// WithDetails puts tags into a file name before its extension, unless the
// name already carries them.
func WithDetails(path, tags string) string {
	if tags == "" {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	// "Film 3D FSBS" + "3D FSBS 1080p ..." must not say 3D FSBS twice.
	for _, lead := range []string{"3D FSBS", "3D HSBS"} {
		if strings.HasSuffix(strings.TrimSpace(base), lead) && strings.HasPrefix(tags, lead) {
			tags = strings.TrimSpace(strings.TrimPrefix(tags, lead))
		}
	}
	return strings.TrimSpace(base) + " " + tags + ext
}

// PictureTags names the high dynamic range a finished run's output
// carries, as release names do: DV (Dolby Vision, profile 8.1 or 7),
// HDR10+, HDR10 (a PQ picture), HLG. None for SDR, or for HDR the output
// could not carry (H.264 keeps no HDR metadata).
func (r *Runner) PictureTags() []string {
	var tags []string
	for _, t := range []struct {
		on   bool
		name string
	}{{r.carried.dv, "DV"}, {r.carried.hdr10Plus, "HDR10+"}, {r.carried.hdr10, "HDR10"}, {r.carried.hlg, "HLG"}} {
		if t.on {
			tags = append(tags, t.name)
		}
	}
	return tags
}
