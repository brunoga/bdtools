package convert

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/brunoga/bdtools/internal/mkv"
	"github.com/brunoga/bdtools/internal/mp4"
)

// muxMP4 assembles an MV-HEVC file: the video with Apple's stereo
// signalling, and the audio tracks the file format and the players carry
// (AC-3 and E-AC-3; a TrueHD track's AC-3 core). Subtitles have no place
// in it yet.
func (r *Runner) muxMP4(ctx context.Context, video []string, extras []extra) error {
	num, den := r.fpsNum, r.fpsDen
	if num <= 0 {
		num, den = 24000, 1001
	}
	var files []*os.File
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()
	open := func(path string) (*os.File, error) {
		f, err := os.Open(path) //nolint:gosec // our work directory
		if err == nil {
			files = append(files, f)
		}
		return f, err
	}
	vr, closeVideo, err := openSegments(video)
	if err != nil {
		return err
	}
	defer closeVideo()
	mvhevc := !r.flat && r.Opts.Layout == LayoutMVHEVC
	codec := mkv.HEVC
	switch r.Opts.Codec {
	case CodecH264:
		codec = mkv.H264
	case CodecAV1:
		codec = mkv.AV1
	}
	stereo := 0
	if !r.flat && r.Opts.Layout.framed() {
		stereo = 1 // side by side, left eye first
		if r.Opts.Layout.overUnder() {
			stereo = 3
		}
	}
	v, err := mkv.NewVideoSource(vr, codec, num, den, stereo)
	if err != nil {
		return fmt.Errorf("muxing: %w", err)
	}
	// Each eye 16:9 from square pixels, as in Matroska.
	switch t := v.Track(); {
	case stereo == 0:
	case r.Opts.Layout.halved():
		v.SetDisplaySize(t.Width, t.Height)
	case r.Opts.Layout.overUnder():
		v.SetDisplaySize(2*t.Width, t.Height)
	default:
		v.SetDisplaySize(t.Width, 2*t.Height)
	}
	if r.dovi != nil || r.Opts.DVFEL.layered() {
		r.Report.Report("warning: an MP4 file here carries no Dolby Vision: the HDR10 base layer alone")
	}
	r.carryHDR(v)
	if r.videoDelay > 0 {
		v.SetDelay(r.videoDelay)
		r.Report.Report("the picture starts %.3f s in, as on the source", r.videoDelay.Seconds())
	}
	sources := []mkv.Source{v}
	subs := 0
	for _, e := range extras {
		if e.track.Kind() == KindSubtitle {
			subs++
			continue
		}
		format := audioFormat(e)
		lang := strings.TrimSpace(e.track.Lang)
		f, err := open(e.path)
		if err != nil {
			return err
		}
		core := false
		switch format {
		case mkv.AC3:
		case mkv.TrueHD:
			if !mkv.HasCore(f, format) {
				r.Report.Report("leaving out %s track %d: no AC-3 core to carry", e.track.Type, e.track.ID)
				continue
			}
			if f, err = open(e.path); err != nil { // HasCore read from the first
				return err
			}
			core = true
			r.Report.Report("carrying the AC-3 core of the %s track %d (the file format and the players take AC-3 and E-AC-3)", e.track.Type, e.track.ID)
		default:
			// DTS (DTS-HD's whole) and PCM: AC-3, coded by ffmpeg.
			path, sync, err := r.toAC3(ctx, e, format)
			if err != nil {
				r.Report.Report("leaving out %s track %d: an MV-HEVC file carries AC-3 and E-AC-3 here, and %v", e.track.Type, e.track.ID, err)
				continue
			}
			if f, err = open(path); err != nil {
				return err
			}
			format, e.sync = mkv.AC3, sync
		}
		a, err := mkv.NewAudioSource(f, format, core, lang)
		if err != nil {
			r.Report.Report("warning: leaving out %s track %d: %v", e.track.Type, e.track.ID, err)
			continue
		}
		a.SetName(e.track.Name)
		a.SetSyncPoints(e.sync)
		sources = append(sources, a)
	}
	if subs > 0 {
		if r.burn != nil {
			r.Report.Report("leaving out %d subtitle track(s): an MP4 file carries none here (track %d is drawn into the picture)", subs, r.Opts.BurnSubs)
		} else {
			r.Report.Report("leaving out %d subtitle track(s): an MP4 file carries none here", subs)
		}
	}
	r.Report.Report("muxing %s", r.Opts.Output)
	tmp := r.Opts.Output + ".part"
	out, err := os.Create(tmp) //nolint:gosec // the operator's output path
	if err != nil {
		return err
	}
	o := r.Opts
	mo := mp4.Options{FPSNum: num, FPSDen: den, QuickTime: strings.ToLower(filepath.Ext(o.Output)) == ".mov"}
	if mvhevc {
		mo.Spatial = &mp4.Spatial{FOV: o.SpatialFOV, Baseline: o.SpatialBaseline, Disparity: o.SpatialDisparity}
	}
	err = mp4.Mux(out, sources, mo)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("muxing: %w", err)
	}
	return os.Rename(tmp, r.Opts.Output)
}

// isMP4Output reports whether the output is an MP4 or QuickTime file.
func isMP4Output(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".mov", ".m4v":
		return true
	}
	return false
}
