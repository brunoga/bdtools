package convert

import (
	"github.com/brunoga/bdtools/internal/dovi"
	"github.com/brunoga/bdtools/internal/mkv"
)

// Dolby Vision through a conversion: profile 8.1.
//
// A conversion encodes the base layer alone, so what it can carry is
// profile 8.1: the HDR10 picture with each frame's RPU, made for the base
// layer alone. A UHD Blu-ray's profile 7 RPUs are converted as dovi_tool's
// mode 2 converts them (the enhancement layer's quantisation dropped, a
// full enhancement layer's mapping made the identity, profile 8.1's colour
// matrices), and go at the end of their frame's access unit; the track gets
// the profile 8.1 configuration record. Players that know Dolby Vision tone
// map with the RPU; others play the HDR10 picture as before.

// keepRPU converts a source picture's RPU for the output frame at display
// index n.
func (r *Runner) keepRPU(n int64, nal []byte) {
	u, err := dovi.ParseNAL(nal)
	var profile int
	var fel bool
	if err == nil {
		profile, fel = u.Profile(), u.FEL()
		err = u.ToProfile81()
	}
	if err != nil {
		if r.rpuFailed++; r.rpuFailed == 1 {
			r.Report.Report("warning: a Dolby Vision RPU cannot be carried: %v", err)
		}
		return
	}
	if r.rpus == nil {
		r.rpus = map[int64][]byte{}
		layer := ""
		switch {
		case profile == 7 && fel:
			layer = " (FEL)"
		case profile == 7:
			layer = " (MEL)"
		}
		r.Report.Report("Dolby Vision profile %d%s: the RPUs go into the encode as profile 8.1", profile, layer)
	}
	r.rpus[n] = u.NAL()
}

// carryDolbyVision gives the video track the Dolby Vision configuration
// record players look for, and a conversion's frames their RPUs.
func (r *Runner) carryDolbyVision(v *mkv.VideoSource, num, den int) {
	cfg := r.dovi
	if cfg == nil && len(r.rpus) > 0 {
		if r.Opts.Codec != CodecH265 {
			r.Report.Report("warning: Dolby Vision is carried in HEVC output only (--codec h265); this keeps the HDR10 picture")
			return
		}
		c := dovi.Profile81()
		cfg = &c
		v.SetTrailer(func(display int64) [][]byte {
			if nal := r.rpus[display]; nal != nil {
				return [][]byte{nal}
			}
			return nil
		})
		r.Report.Report("carrying Dolby Vision profile 8.1 on %d frames", len(r.rpus))
		if r.rpuFailed > 0 {
			r.Report.Report("warning: %d frames' RPUs could not be carried", r.rpuFailed)
		}
	}
	if cfg == nil {
		return
	}
	c := *cfg
	if c.Level == 0 {
		t := v.Track()
		c.Level = dovi.Level(t.Width, t.Height, num, den)
	}
	v.SetBlockAdditions([]mkv.BlockAddition{{Name: "Dolby Vision configuration",
		Type: mkv.FourCC(c.FourCC()), ExtraData: c.Bytes()}})
}
