package convert

import (
	"errors"
	"math"

	"github.com/brunoga/bdtools/internal/nn"
	"github.com/brunoga/bdtools/internal/stereo"
	"github.com/brunoga/bdtools/mvc"
)

// A 3D source's depth changed (--3d-depth, --3d-screen without --to-3d):
// each pair's eyes made again by stereo.Redepth, the subtitles' offsets
// scaled and moved to match.

// redepthAsked reports whether the options change a 3D source's depth.
func (o Options) redepthAsked() bool {
	return !o.To3D && (o.Depth3D > 0 && o.Depth3D != 1 || o.Screen3D != 0)
}

// redepthK is the depth's scale for a 3D source: 1 when not asked.
func (o Options) redepthK() float64 {
	if o.To3D || o.Depth3D <= 0 {
		return 1
	}
	return o.Depth3D
}

// redepthKey is what of the depth change changes the pictures (for
// resuming).
func (o Options) redepthKey() [2]float64 {
	if !o.redepthAsked() {
		return [2]float64{}
	}
	return [2]float64{o.redepthK(), o.Screen3D}
}

// useRedepth turns the depth change on for a 3D source, when asked.
func (r *Runner) useRedepth() error {
	if !r.Opts.redepthAsked() {
		return nil
	}
	if r.flat || r.to3D {
		return errors.New("--3d-depth and --3d-screen change a 3D source's depth (or --to-3d's): this source is converted 2D")
	}
	if r.Opts.Encoder == EncoderCopy {
		return errors.New("--3d-depth and --3d-screen make new pictures: the video cannot be copied")
	}
	r.redepth = true
	r.Report.Report("3D: the depth %g times the source's, moved %+g%% of the width (each pair's eyes made again from its measured disparity)",
		r.Opts.redepthK(), r.Opts.Screen3D)
	return nil
}

// redepthPicture is a pair with its depth changed.
func (r *Runner) redepthPicture(p picture) picture {
	st := p.stereo
	if st == nil || st.Base == nil || st.Dependent == nil {
		return p // a 2D picture in a 3D film: as it is
	}
	l, rt := st.Base, st.Dependent
	if r.Opts.SwapLR {
		l, rt = rt, l // the base view is the right eye
	}
	if !r.redepthTried {
		// the matching on the GPU when there is one
		r.redepthTried = true
		if g, err := nn.NewGPU(); err == nil {
			r.redepthGPU = g
			r.Report.Report("3D: the disparity measured on the GPU (%s)", g.Name())
		} else {
			r.Report.Report("3D: the disparity measured on the CPU (slow: no CUDA GPU, %v)", err)
		}
	}
	shift := r.Opts.Screen3D / 100 * float64(l.Width)
	nl, nr := stereo.Redepth(r.redepthGPU, l, rt, float32(r.Opts.redepthK()), float32(shift))
	if r.Opts.SwapLR {
		nl, nr = nr, nl
	}
	keep := func(dst, src *mvc.Frame) *mvc.Frame {
		dst.ViewID, dst.POC, dst.PTS = src.ViewID, src.POC, src.PTS
		dst.Interlaced, dst.TopFieldFirst = src.Interlaced, src.TopFieldFirst
		return dst
	}
	p.stereo = &mvc.StereoFrame{Base: keep(nl, st.Base), Dependent: keep(nr, st.Dependent)}
	return p
}

// closeRedepth lets the depth change's GPU go.
func (r *Runner) closeRedepth() {
	if r.redepthGPU != nil {
		r.redepthGPU.Close()
		r.redepthGPU = nil
	}
}

// redepthOffsets has the subtitles' depth follow the picture's: each
// offset (half a disparity, pixels of a picture width wide) scaled by
// the depth's scale and moved by half the shift.
func (r *Runner) redepthOffsets(width int) {
	if !r.redepth || r.depth == nil {
		return
	}
	r.depth.scale = r.Opts.redepthK()
	r.depth.shift = r.Opts.Screen3D / 100 * float64(width) / 2
}

// redepthOffset is an offset scaled and moved, within the metadata's
// range.
func redepthOffset(v int8, scale, shift float64) int8 {
	return int8(min(max(math.Round(float64(v)*scale+shift), -127), 127))
}

// videoWidth is a 3D source's picture width: as its track says, else a
// Blu-ray's (1280 for 720 lines, 1920 otherwise).
func videoWidth(t Track) int {
	switch {
	case t.Width > 0:
		return t.Width
	case t.Height == 720:
		return 1280
	}
	return 1920
}
