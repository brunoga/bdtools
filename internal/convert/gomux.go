package convert

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brunoga/mvc/internal/mkv"
)

// muxBuiltin writes the MKV in process: the encoded video, then each audio
// and subtitle track in the order the disc lists them, with the chapters.
//
// The video is flagged side by side, left eye first; each track carries the language the
// disc gives it ("und" when it gives none); and a lossy core packed with a
// lossless track (the AC-3 inside a TrueHD stream, the AC-3 of a 7.1 E-AC-3
// track, the DTS of DTS-HD) is left out unless --keep-fallback asks for it,
// in which case it is its own track beside the main one.
func (r *Runner) muxBuiltin(ctx context.Context, video string, extras []extra, chapters []time.Duration) error {
	num, den := r.fpsNum, r.fpsDen
	if num <= 0 {
		num, den = 24000, 1001
	}
	codec := mkv.H264
	switch r.Opts.Codec {
	case CodecH265:
		codec = mkv.HEVC
	case CodecAV1:
		codec = mkv.AV1
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
	vf, err := open(video)
	if err != nil {
		return err
	}
	v, err := mkv.NewVideoSource(vf, codec, num, den, 1)
	if err != nil {
		return fmt.Errorf("muxing: %w", err)
	}
	if r.videoDelay > 0 {
		v.SetDelay(r.videoDelay)
		r.Report.Report("the picture starts %.3f s in, as on the source", r.videoDelay.Seconds())
	}
	sources := []mkv.Source{v}
	var timed []timedAudio
	firstAudio := true
	for _, e := range extras {
		lang := strings.TrimSpace(e.track.Lang)
		if e.track.Kind() == KindSubtitle {
			f, err := open(e.path)
			if err != nil {
				return err
			}
			p := mkv.NewPGSSource(f, lang)
			p.SetName(e.track.Name)
			p.SetForced(e.track.Forced)
			sources = append(sources, p)
			continue
		}
		format := audioFormat(e)
		f, err := open(e.path)
		if err != nil {
			return err
		}
		a, err := mkv.NewAudioSource(f, format, false, lang)
		if err != nil {
			r.Report.Report("warning: leaving out %s track %d: %v", e.track.Type, e.track.ID, err)
			continue
		}
		a.SetName(e.track.Name)
		a.SetSyncPoints(e.sync)
		timed = append(timed, timedAudio{a, e.track, ""})
		if firstAudio {
			a.SetDefault(true)
			firstAudio = false
		}
		sources = append(sources, a)
		cf, err := open(e.path)
		if err != nil {
			return err
		}
		if !mkv.HasCore(cf, format) {
			continue
		}
		coreName := map[mkv.AudioFormat]string{mkv.TrueHD: "AC-3", mkv.AC3: "AC-3", mkv.DTS: "DTS"}[format]
		if !r.Opts.KeepFallback {
			r.Report.Report("dropping %s embedded in the %s track", coreName, e.track.Type)
			continue
		}
		cf2, err := open(e.path)
		if err != nil {
			return err
		}
		core, err := mkv.NewAudioSource(cf2, format, true, lang)
		if err != nil {
			r.Report.Report("warning: could not keep the %s core of track %d: %v", coreName, e.track.ID, err)
			continue
		}
		core.SetSyncPoints(e.sync)
		timed = append(timed, timedAudio{core, e.track, coreName + " core of "})
		sources = append(sources, core)
	}
	var chs []mkv.Chapter
	if len(chapters) > 1 {
		for i, c := range chapters {
			chs = append(chs, mkv.Chapter{Start: c, Name: fmt.Sprintf("Chapter %d", i+1)})
		}
	}

	r.Report.Report("muxing %s", r.Opts.Output)
	tmp := r.Opts.Output + ".part"
	out, err := os.Create(tmp) //nolint:gosec // the operator's output path
	if err != nil {
		return err
	}
	err = mkv.Mux(out, sources, mkv.Options{
		Chapters:   chs,
		WritingApp: "mvctools",
		Progress: func(t time.Duration) {
			if ctx.Err() == nil && t%(10*time.Minute) < time.Minute {
				r.Report.Report("muxed %s", t.Round(time.Minute))
			}
		},
	})
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
	for _, t := range timed {
		what := fmt.Sprintf("%s%s track %d", t.prefix, t.track.Type, t.track.ID)
		if d := t.a.Delay(); d > 0 {
			r.Report.Report("%s starts %.3f s after the picture, as on the source", what, d.Seconds())
		}
		if gaps := t.a.Gaps(); len(gaps) > 0 {
			var total time.Duration
			for _, g := range gaps {
				total += g
			}
			r.Report.Report("%s has %d gaps (%.3f s in all), kept in step with the picture", what, len(gaps), total.Seconds())
		}
	}
	return os.Rename(tmp, r.Opts.Output)
}

// audioFormat says how a demuxed file's frames are laid out. A TrueHD
// track's stream ID is A_AC3 (tsMuxeR's spelling, which the listing keeps),
// so the type and the extension are both consulted.
func audioFormat(e extra) mkv.AudioFormat {
	ext := strings.ToLower(filepath.Ext(e.path))
	typ := strings.ToUpper(e.track.Type)
	switch {
	case ext == ".wav" || e.track.StreamID == "A_LPCM":
		return mkv.WAV
	case strings.Contains(typ, "TRUE") || strings.HasSuffix(ext, "thd"):
		return mkv.TrueHD
	case e.track.StreamID == "A_DTS" || strings.Contains(ext, "dts"):
		return mkv.DTS
	}
	return mkv.AC3
}

// timedAudio is an audio source whose timing the mux reports on.
type timedAudio struct {
	a      *mkv.AudioSource
	track  Track
	prefix string
}
