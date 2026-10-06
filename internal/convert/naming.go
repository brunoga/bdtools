package convert

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/brunoga/mvc/internal/hwenc"
)

// DetailTags describes a finished conversion for its file name, in the
// order release names use: layout, resolution per eye, video codec and its
// quality setting, encoder, then the main audio track and its channels,
// e.g. "3D FSBS 1080p HEVC QP20 NVENC TrueHD-Atmos 7.1". height is the
// source picture height (one eye).
func DetailTags(o Options, audio []Track, height int) string {
	var tags []string
	layout := "3D FSBS"
	if o.Layout == LayoutHalfSBS {
		layout = "3D HSBS"
	}
	tags = append(tags, layout)
	if height > 0 {
		tags = append(tags, fmt.Sprintf("%dp", height))
	}
	tags = append(tags, map[Codec]string{CodecH264: "H264", CodecH265: "HEVC"}[o.Codec])
	switch {
	case o.Encoder == EncoderVideoToolbox:
		// VideoToolbox takes a quality, not a quantiser: name what it got.
		tags = append(tags, fmt.Sprintf("Q%d", qualityPercent(o.CRF)))
	case o.Encoder == EncoderSoftware:
		tags = append(tags, fmt.Sprintf("CRF%d", o.CRF))
	default:
		tags = append(tags, fmt.Sprintf("QP%d", o.CRF))
	}
	tags = append(tags, map[Encoder]string{EncoderNVENC: "NVENC", EncoderVAAPI: "VAAPI",
		EncoderVideoToolbox: "VideoToolbox", EncoderSoftware: map[Codec]string{CodecH264: "x264", CodecH265: "x265"}[o.Codec]}[o.Encoder])
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

func qualityPercent(crf int) int { return int(100*hwenc.VTQuality(crf) + 0.5) }

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
