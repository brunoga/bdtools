package convert

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brunoga/bdtools/internal/bdmv"
	"github.com/brunoga/bdtools/internal/mkv"
	"github.com/brunoga/bdtools/internal/udf"
)

// A Blu-ray 3D disc image (--output film.iso): the film's MVC video, its
// audio and subtitles in a disc's two clips, the base view's (with the
// audio and subtitles) and the dependent view's, interleaved on the disc
// as an SSIF; the clip infos, the playlist (its chapters) and a title
// that plays it; a UDF 2.50 file system around them. Players read it as
// they read a pressed disc (Kodi, CoreELEC's 3D among them, an ISO as it
// is; a disc player, burned to a BD-R).

// IsDiscImage reports whether an output is a disc image: a Blu-ray 3D.
func IsDiscImage(path string) bool { return strings.EqualFold(filepath.Ext(path), ".iso") }

// The disc's PIDs and rates.
const (
	pidBaseVideo = 0x1011
	pidDepVideo  = 0x1012
	pidAudio     = 0x1100
	pidPG        = 0x1200
	// the clips' highest rates: Blu-ray's 48 Mbit/s for the base view's,
	// 35 for the dependent's, as tsMuxeR records them
	rateBase = 48_000_000
	rateDep  = 35_000_000
	// discStart is the film's first picture on the clips' timeline, 90
	// kHz: ten minutes, as tsMuxeR has it
	discStart = 54_000_000
	// extentBytes is about how much of the base view's clip an extent of
	// the SSIF holds (a pressed 3D disc's are 12 to 18 MB)
	extentBytes = 16 << 20
)

// muxBluray writes the disc image.
func (r *Runner) muxBluray(ctx context.Context, video []string, extras []extra, chapters []time.Duration) error {
	if r.flat {
		return errors.New("a Blu-ray disc image of a 2D film: not yet (a .iso is a Blu-ray 3D)")
	}
	num, den := r.fpsNum, r.fpsDen
	if num <= 0 {
		num, den = 24000, 1001
	}
	frame := int64(90000) * int64(den) / int64(num)
	dir := filepath.Join(r.work.dir, "BDMV")
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // our work directory
		return err
	}
	r.Report.Report("muxing the Blu-ray 3D clips")
	// the base view's clip: the left eye, the audio, the subtitles
	var closers []func()
	defer func() {
		for _, c := range closers {
			c()
		}
	}()
	baseVideo, lastPTS, height, err := r.discVideo(video, num, den, false, &closers)
	if err != nil {
		return err
	}
	format, rate, err := discVideoFormat(height, num, den)
	if err != nil {
		return err
	}
	base := &bdmv.ClipMux{Rate: rateBase, Streams: []*bdmv.MuxStream{{PID: pidBaseVideo, Coding: bdmv.CodingAVC,
		Format: format, Rate: rate, Aspect: 3, Buffer: 4 << 20, Next: baseVideo}}}
	audio, pgs, err := r.discExtras(extras, &closers)
	if err != nil {
		return err
	}
	base.Streams = append(base.Streams, audio...)
	base.Streams = append(base.Streams, pgs...)
	start := int64(discStart)*300 - bdmv.Preload
	if err := muxClip(base, filepath.Join(dir, "00000.m2ts"), start); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// the dependent view's clip: the right eye
	depVideo, _, _, err := r.discVideo(video, num, den, true, &closers)
	if err != nil {
		return err
	}
	dep := &bdmv.ClipMux{Rate: rateDep, Streams: []*bdmv.MuxStream{{PID: pidDepVideo, Coding: bdmv.CodingMVC,
		Format: format, Rate: rate, Aspect: 3, Buffer: 4 << 20, Next: depVideo}}}
	if err := muxClip(dep, filepath.Join(dir, "00001.m2ts"), start); err != nil {
		return err
	}
	if len(base.EP) == 0 || len(dep.EP) == 0 {
		return errors.New("blu-ray: the video has no IDR picture")
	}
	in, out := uint32(base.EP[0].PTS/2), uint32((*lastPTS+discStart+frame)/2) //nolint:gosec // 45 kHz times
	baseCuts, depCuts := interleave(base.Units, dep.Units)
	r.Report.Report("the two clips interleaved in %d extents each", len(baseCuts))

	// the clip infos, the playlist, the navigation
	clipStreams := func(ss []*bdmv.MuxStream) []bdmv.ClipStream {
		var out []bdmv.ClipStream
		for _, s := range ss {
			out = append(out, bdmv.ClipStream{PID: s.PID, Coding: s.Coding, Lang: s.Lang, Format: s.Format, Rate: s.Rate, Aspect: s.Aspect})
		}
		return out
	}
	spns := func(cuts []int) []uint32 {
		var out []uint32
		for _, c := range cuts {
			out = append(out, uint32(c*32)) //nolint:gosec // packets
		}
		return out
	}
	baseClip := &bdmv.Clip{RecordingRate: rateBase / 8, Packets: base.Packets, PCRPID: 0x1001, Start: in, End: out,
		Streams: clipStreams(base.Streams), EPPID: pidBaseVideo, EP: base.EP, Extents: spns(baseCuts)}
	depClip := &bdmv.Clip{Dependent: true, RecordingRate: rateDep / 8, Packets: dep.Packets, PCRPID: 0x1001, Start: in, End: out,
		Streams: clipStreams(dep.Streams), EPPID: pidDepVideo, EP: dep.EP, Extents: spns(depCuts)}
	item := bdmv.PlayItem{Clip: "00000", Codec: "M2TS", ConnectionCondition: 1, InTime: in, OutTime: out,
		DependentPID: pidDepVideo, OffsetSequences: 1}
	for _, s := range base.Streams {
		kind := bdmv.KindAudio
		switch s.Coding {
		case bdmv.CodingAVC:
			kind = bdmv.KindVideo
		case bdmv.CodingPGS:
			kind = bdmv.KindPG
		}
		item.Streams = append(item.Streams, bdmv.Stream{Kind: kind, EntryType: 1, PID: s.PID, Coding: s.Coding,
			Format: s.Format, Rate: s.Rate, Lang: s.Lang, OffsetSequence: -1})
	}
	pl := &bdmv.Playlist{Items: []bdmv.PlayItem{item},
		SubPaths: []bdmv.SubPath{{Type: bdmv.SubPathMVC, Items: []bdmv.SubPlayItem{{Clip: "00001", Codec: "M2TS",
			ConnectionCondition: 1, InTime: in, OutTime: out, SyncPTS: in}}}}}
	marks := chapters
	if len(marks) == 0 {
		marks = []time.Duration{0}
	}
	for _, c := range marks {
		t := in + uint32(c.Milliseconds()*45) //nolint:gosec // a chapter's 45 kHz time
		if t < out {
			pl.Marks = append(pl.Marks, bdmv.Mark{Type: 1, Time: t, PID: 0xffff})
		}
	}
	return r.writeDisc(ctx, dir, pl, baseClip, depClip, base, dep, baseCuts, depCuts)
}

// discVideoFormat is a Blu-ray 3D picture's video_format and frame_rate
// codes: 1080p at 23.976 or 24, 720p at 50 or 59.94.
func discVideoFormat(height, num, den int) (format, rate byte, err error) {
	rates := map[[2]int]byte{{24000, 1001}: 1, {24, 1}: 2, {50, 1}: 6, {60000, 1001}: 7}
	rate = rates[[2]int{num, den}]
	switch {
	case height == 1080 && (rate == 1 || rate == 2):
		return 6, rate, nil
	case height == 720 && (rate == 6 || rate == 7):
		return 5, rate, nil
	}
	return 0, 0, fmt.Errorf("blu-ray 3D is 1080p at 23.976 or 24 fps, or 720p at 50 or 59.94: this film is %d lines at %d/%d (--scale changes the size)", height, num, den)
}

// muxClip writes a clip's file.
func muxClip(c *bdmv.ClipMux, path string, start int64) error {
	f, err := os.Create(path) //nolint:gosec // our work directory
	if err != nil {
		return err
	}
	c.W = f
	err = bdmv.Mux([]*bdmv.ClipMux{c}, start, discStart)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("blu-ray: %w", err)
	}
	return nil
}

// discVideo gives the MVC video's access units as one of the disc's two
// views (the dependent's when dep), their times on the clips' timeline
// less discStart; last is set, once read, to the last picture's. It
// gives the picture's height too.
func (r *Runner) discVideo(video []string, num, den int, dep bool, closers *[]func()) (func() (bdmv.MuxFrame, error), *int64, int, error) {
	vr, closeVideo, err := openSegments(video)
	if err != nil {
		return nil, nil, 0, err
	}
	*closers = append(*closers, closeVideo)
	v, err := mkv.NewVideoSource(vr, mkv.H264, num, den, 0)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("blu-ray: %w", err)
	}
	last := new(int64)
	return func() (bdmv.MuxFrame, error) {
		f, err := v.Next()
		if err != nil {
			return bdmv.MuxFrame{}, err
		}
		pts, dts := ticks90(f.PTS), ticks90(f.Order)
		*last = max(*last, pts)
		base, dv := splitMVC(lengthPrefixed(f.Data))
		data := base
		if dep {
			data = dv
		}
		return bdmv.MuxFrame{PTS: pts, DTS: dts, Data: data, Key: f.Keyframe}, nil
	}, last, v.Track().Height, nil
}

// ticks90 is a time in 90 kHz ticks, rounded.
func ticks90(d time.Duration) int64 { return (d.Nanoseconds()*9 + 50_000) / 100_000 }

// lengthPrefixed is a Matroska frame's NAL units (4-byte lengths before
// each).
func lengthPrefixed(b []byte) [][]byte {
	var out [][]byte
	for len(b) >= 4 {
		n := int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3])
		if n > len(b)-4 {
			break
		}
		out = append(out, b[4:4+n])
		b = b[4+n:]
	}
	return out
}

// splitMVC splits an MVC access unit's NAL units into its base view
// (plain H.264, an access unit delimiter first, as a disc's base view has
// it) and its dependent view (its parameter sets, the delimiter, its
// slices), Annex B.
func splitMVC(nals [][]byte) (base, dep []byte) {
	sc := []byte{0, 0, 0, 1}
	// The MVC parameter sets (a subset SPS and the PPS after it) come
	// ahead of the base view's slices; the delimiter and all after it
	// are the dependent view's.
	inDep, afterSubset := false, false
	first := true
	for _, n := range nals {
		if len(n) == 0 {
			continue
		}
		t := n[0] & 0x1f
		wasSubset := afterSubset
		afterSubset = t == 15
		switch {
		case inDep || t == 24 || t == 20:
			inDep = true
			dep = append(append(dep, sc...), n...)
		case t == 15 || t == 8 && wasSubset:
			dep = append(append(dep, sc...), n...)
		case t == 14:
			// a prefix NAL unit: not in a disc's base view
		default:
			if first && t != 9 {
				base = append(base, 0, 0, 0, 1, 0x09, 0xf0) // an access unit delimiter: any picture type
			}
			base = append(append(base, sc...), n...)
		}
		first = false
	}
	return base, dep
}

// discExtras is the audio and subtitle streams of the base view's clip.
func (r *Runner) discExtras(extras []extra, closers *[]func()) (audio, pgs []*bdmv.MuxStream, err error) {
	open := func(path string) (*os.File, error) {
		f, err := os.Open(path) //nolint:gosec // our work directory
		if err == nil {
			*closers = append(*closers, func() { _ = f.Close() })
		}
		return f, err
	}
	for _, e := range extras {
		lang := strings.TrimSpace(e.track.Lang)
		if len(lang) != 3 {
			lang = "und"
		}
		if e.track.Kind() == KindSubtitle {
			if e.track.StreamID != "S_HDMV/PGS" {
				r.Report.Report("leaving out %s subtitle track %d: a disc's subtitles are PGS", e.track.Type, e.track.ID)
				continue
			}
			f, err := open(e.path)
			if err != nil {
				return nil, nil, err
			}
			pgs = append(pgs, &bdmv.MuxStream{PID: pidPG + uint16(len(pgs)), Coding: bdmv.CodingPGS, Lang: lang, //nolint:gosec // a few
				Buffer: 1 << 20, Next: pgsSegments(mkv.NewPGSSource(f, lang))})
			continue
		}
		format := audioFormat(e)
		if format == mkv.WAV || format == mkv.AAC {
			return nil, nil, fmt.Errorf("blu-ray: audio track %d is %s, which a disc does not carry: --audio-transcode ac3", e.track.ID, e.track.Type)
		}
		f, err := open(e.path)
		if err != nil {
			return nil, nil, err
		}
		a, err := mkv.NewAudioSource(f, format, false, lang)
		if err != nil {
			r.Report.Report("warning: leaving out %s track %d: %v", e.track.Type, e.track.ID, err)
			continue
		}
		a.SetSyncPoints(e.sync)
		t := a.Track()
		s := &bdmv.MuxStream{PID: pidAudio + uint16(len(audio)), Lang: lang, Buffer: 64 << 10, //nolint:gosec // a few
			Format: presentation(t.Channels), Rate: sampling(t.SampleRate)}
		next := audioFrames(a)
		switch t.CodecID {
		case "A_EAC3":
			s.Coding = bdmv.CodingEAC3
		case "A_TRUEHD":
			s.Coding = bdmv.CodingTrueHD
			// its AC-3 core interleaved, as a disc carries it
			cf, err := open(e.path)
			if err != nil {
				return nil, nil, err
			}
			if mkv.HasCore(cf, format) {
				cf2, err := open(e.path)
				if err != nil {
					return nil, nil, err
				}
				core, err := mkv.NewAudioSource(cf2, format, true, lang)
				if err != nil {
					return nil, nil, fmt.Errorf("blu-ray: the AC-3 core of track %d: %w", e.track.ID, err)
				}
				core.SetSyncPoints(e.sync)
				next = interleaveFrames(audioFrames(core), next)
			}
		case "A_DTS":
			s.Coding = bdmv.CodingDTS
			switch typ := strings.ToUpper(e.track.Type); {
			case strings.Contains(typ, "MASTER") || strings.Contains(typ, "MA"):
				s.Coding = bdmv.CodingDTSHDMA
			case strings.Contains(typ, "HIGH") || strings.Contains(typ, "HR"):
				s.Coding = bdmv.CodingDTSHDHR
			}
		default:
			s.Coding = bdmv.CodingAC3
		}
		s.Next = next
		audio = append(audio, s)
	}
	return audio, pgs, nil
}

// presentation is an audio stream's audio_presentation_type.
func presentation(channels int) byte {
	switch {
	case channels <= 1:
		return 1
	case channels == 2:
		return 3
	}
	return 6
}

// sampling is an audio stream's sampling_frequency code.
func sampling(rate int) byte {
	switch rate {
	case 96000:
		return 4
	case 192000:
		return 5
	}
	return 1
}

// audioFrames is an audio source's frames as the disc's.
func audioFrames(a *mkv.AudioSource) func() (bdmv.MuxFrame, error) {
	return func() (bdmv.MuxFrame, error) {
		f, err := a.Next()
		if err != nil {
			return bdmv.MuxFrame{}, err
		}
		t := ticks90(f.PTS)
		return bdmv.MuxFrame{PTS: t, DTS: t, Data: f.Data}, nil
	}
}

// interleaveFrames merges two streams of frames by time, a's first of
// equals (a TrueHD stream's AC-3 core before the access unit it sits by).
func interleaveFrames(a, b func() (bdmv.MuxFrame, error)) func() (bdmv.MuxFrame, error) {
	var fa, fb *bdmv.MuxFrame
	doneA, doneB := false, false
	pull := func(next func() (bdmv.MuxFrame, error), f **bdmv.MuxFrame, done *bool) error {
		if *f != nil || *done {
			return nil
		}
		g, err := next()
		if errors.Is(err, io.EOF) {
			*done = true
			return nil
		}
		if err != nil {
			return err
		}
		*f = &g
		return nil
	}
	return func() (bdmv.MuxFrame, error) {
		if err := pull(a, &fa, &doneA); err != nil {
			return bdmv.MuxFrame{}, err
		}
		if err := pull(b, &fb, &doneB); err != nil {
			return bdmv.MuxFrame{}, err
		}
		switch {
		case fa == nil && fb == nil:
			return bdmv.MuxFrame{}, io.EOF
		case fb == nil || fa != nil && fa.DTS <= fb.DTS:
			f := *fa
			fa = nil
			return f, nil
		}
		f := *fb
		fb = nil
		return f, nil
	}
}

// pgsSegments is a PGS track's display sets as the disc carries them: each
// segment a PES packet, presented with its display set and decoded ahead
// of it by as long as the PG decoder (128 Mbit/s) takes over the set's
// objects.
func pgsSegments(p *mkv.PGSSource) func() (bdmv.MuxFrame, error) {
	var queue []bdmv.MuxFrame
	return func() (bdmv.MuxFrame, error) {
		for len(queue) == 0 {
			f, err := p.Next()
			if err != nil {
				return bdmv.MuxFrame{}, err
			}
			pts := ticks90(f.PTS)
			objects := 0
			var segs [][]byte
			for b := f.Data; len(b) >= 3; {
				n := 3 + int(b[1])<<8 | int(b[2])
				n = min(n, len(b))
				if b[0] == 0x15 { // an object definition
					objects += n
				}
				segs = append(segs, b[:n])
				b = b[n:]
			}
			dts := pts - int64(objects)*8*90000/128_000_000 - 1
			for _, s := range segs {
				queue = append(queue, bdmv.MuxFrame{PTS: pts, DTS: dts, Data: s})
			}
		}
		f := queue[0]
		queue = queue[1:]
		return f, nil
	}
}

// interleave plans the SSIF: the aligned units of the base view's clip
// (and the dependent's) its extents start at, cut where about extentBytes
// of the base view's have gone by, the dependent view's at the same time.
func interleave(base, dep []int64) (baseCuts, depCuts []int) {
	per := max(extentBytes/6144, 1)
	j := 0
	for i := 0; i < len(base); i += per {
		for j < len(dep) && dep[j] < base[i] {
			j++
		}
		if i > 0 && j >= len(dep) {
			break // the dependent view's clip has ended: the rest is one extent
		}
		baseCuts = append(baseCuts, i)
		if i == 0 {
			depCuts = append(depCuts, 0)
		} else {
			depCuts = append(depCuts, j)
		}
	}
	return baseCuts, depCuts
}

// writeDisc writes the disc image: its file system around the BDMV.
func (r *Runner) writeDisc(ctx context.Context, dir string, pl *bdmv.Playlist, baseClip, depClip *bdmv.Clip,
	base, dep *bdmv.ClipMux, baseCuts, depCuts []int) error {
	small := func(name string, b []byte) *udf.File {
		return &udf.File{Name: name, Size: int64(len(b)), Content: func() (io.Reader, error) { return strings.NewReader(string(b)), nil }}
	}
	stream := func(name string, packets uint32) *udf.File {
		path := filepath.Join(dir, name)
		return &udf.File{Name: name, Size: int64(packets) * 192, RealTime: true, Content: func() (io.Reader, error) {
			return os.Open(path) //nolint:gosec // our work directory
		}}
	}
	fb, fd := stream("00000.m2ts", base.Packets), stream("00001.m2ts", dep.Packets)
	var order []udf.Piece
	unit := int64(6144)
	for k := range baseCuts {
		bEnd, dEnd := int64(base.Packets)*192, int64(dep.Packets)*192
		if k+1 < len(baseCuts) {
			bEnd, dEnd = int64(baseCuts[k+1])*unit, int64(depCuts[k+1])*unit
		}
		dOff, bOff := int64(depCuts[k])*unit, int64(baseCuts[k])*unit
		order = append(order, udf.Piece{F: fd, Off: dOff, Len: dEnd - dOff}, udf.Piece{F: fb, Off: bOff, Len: bEnd - bOff})
	}
	ssif := &udf.File{Name: "00000.ssif", RealTime: true, Parts: order}
	files := func() []*udf.File {
		return []*udf.File{
			small("index.bdmv", bdmv.Index(true)),
			small("MovieObject.bdmv", bdmv.MovieObjects()),
			{Name: "PLAYLIST", Dir: true, Children: []*udf.File{small("00000.mpls", pl.MPLS())}},
			{Name: "CLIPINF", Dir: true, Children: []*udf.File{small("00000.clpi", baseClip.CLPI()), small("00001.clpi", depClip.CLPI())}},
		}
	}
	bdmvDir := &udf.File{Name: "BDMV", Dir: true}
	bdmvDir.Children = append(files(),
		&udf.File{Name: "STREAM", Dir: true, Children: []*udf.File{fb, fd, {Name: "SSIF", Dir: true, Children: []*udf.File{ssif}}}},
		&udf.File{Name: "AUXDATA", Dir: true}, &udf.File{Name: "BDJO", Dir: true}, &udf.File{Name: "JAR", Dir: true},
		&udf.File{Name: "META", Dir: true},
		&udf.File{Name: "BACKUP", Dir: true, Children: append(files(), &udf.File{Name: "BDJO", Dir: true}, &udf.File{Name: "JAR", Dir: true})})
	root := &udf.File{Dir: true, Children: []*udf.File{bdmvDir,
		{Name: "CERTIFICATE", Dir: true, Children: []*udf.File{{Name: "BACKUP", Dir: true}}}}}
	label := strings.ToUpper(strings.TrimSuffix(filepath.Base(r.Opts.Output), filepath.Ext(r.Opts.Output)))
	r.Report.Report("writing the Blu-ray 3D image %s", r.Opts.Output)
	tmp := r.Opts.Output + ".part"
	out, err := os.Create(tmp) //nolint:gosec // the operator's output path
	if err != nil {
		return err
	}
	err = udf.Write(out, root, order, udf.Options{Label: label, Time: time.Now()})
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("writing the disc image: %w", err)
	}
	return os.Rename(tmp, r.Opts.Output)
}
