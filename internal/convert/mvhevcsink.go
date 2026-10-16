package convert

import (
	"errors"
	"fmt"
	"os"

	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/mvc"
)

// mvhevcSink writes a 3D picture as MV-HEVC on NVENC: the left eye as the
// base layer, the right as a second layer predicted from it, in one
// Annex B stream the MP4 muxer takes apart.
type mvhevcSink struct {
	r    *Runner
	path string
	f    *os.File
	enc  gpu.StereoEncoder
}

func (s *mvhevcSink) start(path string, first picture, num, den int) error {
	if first.stereo == nil || first.stereo.Dependent == nil {
		return errors.New("MV-HEVC output needs a 3D source")
	}
	f, err := os.Create(path) //nolint:gosec // our work directory
	if err != nil {
		return err
	}
	o := s.r.Opts
	w, h, _ := first.size() // one eye's picture: the views are layers, not halves
	cfg := gpu.Config{Codec: gpu.HEVC, Width: w, Height: h, FPSNum: num, FPSDen: den,
		QP: o.CRF, CQ: o.nvencCQ(), BitDepth: o.BitDepth, Color: o.Color, Views: 2}
	enc, err := gpu.Open(gpu.NVENC, cfg, f)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("opening the NVENC MV-HEVC encoder: %w", err)
	}
	se, ok := enc.(gpu.StereoEncoder)
	if !ok {
		_ = enc.Close()
		_ = f.Close()
		_ = os.Remove(path)
		return errors.New("the NVENC encoder does not take two views")
	}
	s.path, s.f, s.enc = path, f, se
	return nil
}

func (s *mvhevcSink) put(p picture) error {
	left, right := p.stereo.Base, p.stereo.Dependent
	if right == nil {
		right = left // a 2D access unit (a disc's last pictures) shows its one view to both eyes
	}
	if s.r.Opts.SwapLR {
		left, right = right, left
	}
	lp, rp := p, p
	lp.stereo = &mvc.StereoFrame{Base: left}
	rp.stereo = &mvc.StereoFrame{Base: right}
	return s.enc.EncodeStereo(func(l, r *gpu.Picture) {
		drawFlat(l, lp)
		drawFlat(r, rp)
	})
}

func (s *mvhevcSink) finish() error {
	err := s.enc.Close()
	if err == nil {
		err = s.f.Sync()
	}
	if cerr := s.f.Close(); err == nil {
		err = cerr
	}
	return err
}

func (s *mvhevcSink) abort() error {
	_ = s.enc.Close()
	_ = s.f.Close()
	_ = os.Remove(s.path)
	return nil
}
