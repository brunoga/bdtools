package convert

import (
	"fmt"
	"strings"
	"testing"
)

func TestResolveDefaults(t *testing.T) {
	uhd := Track{StreamID: "V_MPEGH/ISO/HEVC", Height: 2160}
	for _, c := range []struct {
		name     string
		flat     bool
		video    Track
		dv       bool
		crf      int
		codec    Codec
		explicit map[string]bool
		wantCRF  int
		wantBits int
		report   string
	}{
		{"3D", false, Track{StreamID: "V_MPEG4/ISO/AVC"}, false, CRFAuto, CodecH265, map[string]bool{}, 20, 10,
			"video: --codec h265, --bit-depth 10, --crf 20, --layout full, --subs-3d both (defaults for 3D)"},
		{"Ultra HD Dolby Vision", true, uhd, true, CRFAuto, CodecH265, map[string]bool{}, 18, 10,
			"video: --codec h265, --bit-depth 10, --crf 18 (defaults for Ultra HD Dolby Vision)"},
		{"a disc's HEVC", true, Track{StreamID: "V_MPEGH/ISO/HEVC"}, false, CRFAuto, CodecH265, nil, 18, 10,
			"video: --codec h265, --bit-depth 10, --crf 18 (defaults for Ultra HD)"},
		{"HD", true, Track{StreamID: "V_MS/VFW/FOURCC", Height: 1080}, false, CRFAuto, CodecH265, map[string]bool{}, 16, 10,
			"video: --codec h265, --bit-depth 10, --crf 16 (defaults for HD)"},
		{"an HEVC 1080p file", true, Track{StreamID: "V_MPEGH/ISO/HEVC", Height: 1080}, false, CRFAuto, CodecH265, nil, 16, 10,
			"--crf 16 (defaults for HD)"},
		{"given", true, uhd, false, 22, CodecH264, map[string]bool{"codec": true, "crf": true}, 22, 8,
			"video: --bit-depth 8 (defaults for Ultra HD), --codec h264, --crf 22 as given"},
		{"all given", true, uhd, false, 14, CodecH265, map[string]bool{"codec": true, "crf": true, "bit-depth": true}, 14, 10,
			"video: --codec h265, --bit-depth 10, --crf 14, as given"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var said []string
			o := DefaultOptions()
			o.CRF, o.Codec, o.Subs3D = c.crf, c.codec, Subs3DBoth // as bdtools defaults it
			if c.explicit["bit-depth"] {
				o.BitDepth = 10
			}
			r := NewRunner("linux", o, func(f string, a ...any) { said = append(said, fmt.Sprintf(f, a...)) })
			r.flat, r.Explicit = c.flat, c.explicit
			r.resolveDefaults(c.video, c.dv)
			if r.Opts.CRF != c.wantCRF || r.Opts.BitDepth != c.wantBits {
				t.Errorf("--crf %d, --bit-depth %d; want %d, %d", r.Opts.CRF, r.Opts.BitDepth, c.wantCRF, c.wantBits)
			}
			if len(said) != 1 || !strings.Contains(said[0], c.report) {
				t.Errorf("said %q, want %q", said, c.report)
			}
		})
	}
}
