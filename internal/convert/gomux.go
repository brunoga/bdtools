package convert

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brunoga/bdtools/internal/hdr"
	"github.com/brunoga/bdtools/internal/mkv"
)

// muxBuiltin writes the MKV in process: the encoded video, then each audio
// and subtitle track in the order the disc lists them, with the chapters.
//
// The video is flagged side by side, left eye first; each track carries the language the
// disc gives it ("und" when it gives none); and a lossy core packed with a
// lossless track (the AC-3 inside a TrueHD stream, the AC-3 of a 7.1 E-AC-3
// track, the DTS of DTS-HD) is left out unless --keep-fallback asks for it,
// in which case it is its own track beside the main one.
func (r *Runner) muxBuiltin(ctx context.Context, video []string, extras []extra, chapters []time.Duration) error {
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
	vr, closeVideo, err := openSegments(video)
	if err != nil {
		return err
	}
	defer closeVideo()
	stereo := 1 // side by side, left eye first
	if r.flat {
		stereo = 0
	}
	v, err := mkv.NewVideoSource(vr, codec, num, den, stereo)
	if err != nil {
		return fmt.Errorf("muxing: %w", err)
	}
	// Each eye's shape, as players read it: 16:9 from square pixels for a
	// full-width pair, 16:9 from squeezed ones for a half-width pair. A 2D
	// picture is shown at its pixel size, Matroska's default.
	switch t := v.Track(); {
	case r.flat:
	case r.Opts.Layout == LayoutHalfSBS:
		v.SetDisplaySize(t.Width, t.Height)
	default:
		v.SetDisplaySize(t.Width, 2*t.Height)
	}
	r.carryHDR(v)
	r.carryDolbyVision(v, num, den)
	if r.videoDelay > 0 {
		v.SetDelay(r.videoDelay)
		r.Report.Report("the picture starts %.3f s in, as on the source", r.videoDelay.Seconds())
	}
	if r.depth != nil {
		r.depth.frame = time.Duration(int64(time.Second) * int64(den) / int64(num))
		r.depth.sort()
	}
	sources := []mkv.Source{v}
	var timed []timedAudio
	firstAudio, firstSubs := true, true
	for _, e := range extras {
		lang := strings.TrimSpace(e.track.Lang)
		if e.track.Kind() == KindSubtitle {
			pgs := func() (*mkv.PGSSource, error) {
				f, err := open(e.path)
				if err != nil {
					return nil, err
				}
				p := mkv.NewPGSSource(f, lang)
				p.SetName(e.track.Name)
				p.SetForced(e.track.Forced)
				return p, nil
			}
			if r.Opts.Subs3D.flat() {
				p, err := pgs()
				if err != nil {
					return err
				}
				// With 3D tracks beside them, a player left to choose can
				// take a 3D one, which a player placing subtitles in 3D
				// itself (Kodi showing the film frame packed) draws
				// squeezed into one eye: the first flat track is the
				// default.
				if r.Opts.Subs3D == Subs3DBoth && firstSubs {
					p.SetDefault(true)
					firstSubs = false
				}
				sources = append(sources, p)
			}
			if r.Opts.Subs3D.threeD() {
				p, err := pgs()
				if err != nil {
					return err
				}
				sources = append(sources, r.subtitles3D(p, e.track))
			}
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
	var spans []mkv.Span
	err = mkv.Mux(out, sources, mkv.Options{
		Spans:      &spans,
		Chapters:   chs,
		WritingApp: "bdtools",
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
	r.reportTimeline(sources, spans)
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

// reportTimeline says where each track starts and ends, and warns when the
// picture's length is not the source's: a whole-film offset or lost
// pictures show there, and nowhere else.
func (r *Runner) reportTimeline(sources []mkv.Source, spans []mkv.Span) {
	if len(spans) != len(sources) {
		return
	}
	var parts []string
	for i, s := range sources {
		if spans[i].Frames == 0 {
			continue
		}
		t := s.Track()
		name := map[mkv.TrackType]string{mkv.TypeVideo: "picture", mkv.TypeAudio: t.CodecID, mkv.TypeSubtitle: "subtitles"}[t.Type]
		if t.Type == mkv.TypeSubtitle {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %.3f-%.3f s", name, spans[i].First.Seconds(), spans[i].Last.Seconds()))
	}
	r.Report.Report("timeline: %s", strings.Join(parts, ", "))
	v := spans[0]
	if r.length > 0 && r.fpsNum > 0 {
		frame := time.Duration(int64(time.Second) * int64(r.fpsDen) / int64(r.fpsNum))
		if d := v.Last + frame - r.length; d > time.Second || d < -time.Second {
			r.Report.Report("warning: the picture runs %.3f s but the source plays %.3f s; check it is in step with the sound",
				(v.Last + frame).Seconds(), r.length.Seconds())
		}
	}
}

// carryHDR gives the output video the source's colour signalling and HDR
// metadata: the Colour element, and in the stream the static metadata on
// every keyframe and HDR10+'s on every frame it was on.
func (r *Runner) carryHDR(v *mkv.VideoSource) {
	if r.colour == nil && r.hdrStatic.Empty() && r.hdr10Plus == nil {
		return
	}
	if c := r.colour; c != nil {
		v.SetColour(&mkv.Colour{Matrix: c.Matrix, Transfer: c.Transfer, Primaries: c.Primaries, FullRange: c.FullRange,
			Static: r.hdrStatic})
	}
	if r.Opts.Codec == CodecH264 && (!r.hdrStatic.Empty() || r.hdr10Plus != nil) {
		r.Report.Report("warning: H.264 output: the source's HDR metadata is not carried (use --codec h265 or av1)")
		return
	}
	av1 := r.Opts.Codec == CodecAV1
	var static []byte
	if !r.hdrStatic.Empty() {
		if av1 {
			static = hdr.AV1StaticOBUs(r.hdrStatic)
		} else {
			static = hdr.HEVCStaticSEI(r.hdrStatic)
		}
	}
	var parts []string
	if static != nil {
		parts = append(parts, "HDR10 metadata")
	}
	if len(r.hdr10Plus) > 0 {
		parts = append(parts, fmt.Sprintf("HDR10+ on %d frames", len(r.hdr10Plus)))
	}
	if len(parts) > 0 {
		r.Report.Report("carrying the source's %s", strings.Join(parts, " and "))
	}
	v.SetExtras(func(display int64, key bool) [][]byte {
		var out [][]byte
		if key && static != nil {
			out = append(out, static)
		}
		if d := r.hdr10Plus[display]; d != nil {
			if av1 {
				out = append(out, hdr.AV1T35OBU(d))
			} else {
				out = append(out, hdr.HEVCT35SEI(d))
			}
		}
		return out
	})
}
