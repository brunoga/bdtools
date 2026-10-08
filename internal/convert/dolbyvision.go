package convert

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

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
	if r.felLayered {
		// Profile 7 out: the source's RPU as it is.
		if r.rpus == nil {
			r.rpus = map[int64][]byte{}
		}
		r.rpus[n] = nal
		return
	}
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
func (r *Runner) carryDolbyVision(v *mkv.VideoSource, num, den int) error {
	cfg := r.dovi
	if cfg == nil && r.felLayered {
		c, err := r.carryLayers(v, num, den)
		if err != nil {
			return err
		}
		cfg = c
	} else if cfg == nil && len(r.rpus) > 0 {
		if r.Opts.Codec != CodecH265 {
			r.Report.Report("warning: Dolby Vision is carried in HEVC output only (--codec h265); this keeps the HDR10 picture")
			return nil
		}
		c := dovi.Profile81()
		cfg = &c
		v.SetTrailer(func(display int64) ([][]byte, error) {
			if nal := r.rpus[display]; nal != nil {
				return [][]byte{nal}, nil
			}
			return nil, nil
		})
		r.Report.Report("carrying Dolby Vision profile 8.1 on %d frames", len(r.rpus))
		if r.rpuFailed > 0 {
			r.Report.Report("warning: %d frames' RPUs could not be carried", r.rpuFailed)
		}
	}
	if cfg == nil {
		return nil
	}
	c := *cfg
	if c.Level == 0 {
		t := v.Track()
		c.Level = dovi.Level(t.Width, t.Height, num, den)
	}
	v.SetBlockAdditions([]mkv.BlockAddition{{Name: "Dolby Vision configuration",
		Type: mkv.FourCC(c.FourCC()), ExtraData: c.Bytes()}})
	return nil
}

// carryLayers ends each frame of the base layer with its enhancement layer
// picture (the one in the same place in decoding order, which the mux
// checks is the one in the same place in display order: the layers were
// coded alike) and the source's RPU, as profile 7.
func (r *Runner) carryLayers(v *mkv.VideoSource, num, den int) (*dovi.Config, error) {
	paths, err := r.work.elSegmentPaths()
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, errors.New("the enhancement layer's segments are missing: start over with --restart")
	}
	er, closeEL, err := openSegments(paths)
	if err != nil {
		return nil, err
	}
	r.closers = append(r.closers, closeEL)
	el, err := mkv.NewVideoSource(er, mkv.HEVC, num, den, 0)
	if err != nil {
		return nil, fmt.Errorf("reading the enhancement layer: %w", err)
	}
	frame := time.Duration(int64(time.Second) * int64(den) / int64(num))
	v.SetTrailer(func(display int64) ([][]byte, error) {
		f, err := el.Next()
		if err != nil {
			return nil, fmt.Errorf("the enhancement layer ends before the base layer: %w", err)
		}
		if d := int64((f.PTS + frame/2) / frame); d != display {
			return nil, fmt.Errorf("%w: picture %d of one is picture %d of the other", errLayersMisaligned, display, d)
		}
		var out [][]byte
		for b := f.Data; len(b) >= 4; {
			n := int(binary.BigEndian.Uint32(b))
			if n > len(b)-4 {
				break
			}
			out = append(out, append([]byte{dovi.NALEL << 1, 1}, b[4:4+n]...))
			b = b[4+n:]
		}
		if rpu := r.rpus[display]; rpu != nil {
			out = append(out, rpu)
		}
		return out, nil
	})
	r.Report.Report("carrying Dolby Vision profile 7: the full enhancement layer and the RPU on %d frames", len(r.rpus))
	c := dovi.UHDBluRay(0, 0, 0, 0)
	return &c, nil
}
