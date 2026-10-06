package convert

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brunoga/mvc"
	"github.com/brunoga/mvc/internal/esinfo"
	"github.com/brunoga/mvc/internal/mkv"
)

// Matroska sources: a Blu-ray 3D remuxed to MKV (MakeMKV, mkvmerge), read in
// place like a disc. The video track carries each MVC access unit whole —
// the base view's NAL units, then the dependent view's — so it is both the
// base and the dependent track of the selection. The audio and subtitle
// tracks are written out in the same formats the Blu-ray demuxer writes, so
// the mux treats them alike, and their names and forced flags are kept.

// isMatroska reports whether a source is a Matroska file.
func isMatroska(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mkv", ".mk3d":
		return true
	}
	return false
}

// mkvSource is a Matroska input.
type mkvSource struct {
	path     string
	name     string
	tracks   []mkv.ReadTrack
	chapters []time.Duration
	duration time.Duration
	// dependent is the track number of a separate MVC track, 0 when the
	// video track carries both views.
	dependent uint64
}

func openMatroska(path string) (*os.File, *mkv.Reader, error) {
	f, err := os.Open(path) //nolint:gosec // the operator's input
	if err != nil {
		return nil, nil, err
	}
	r, err := mkv.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return f, r, nil
}

// mkvProbeFrames is how many frames per track the probe looks at: enough
// for an audio track's first frame set and the video's parameter sets.
const mkvProbeFrames = 32

// probeMatroska lists a Matroska source's tracks.
func probeMatroska(ctx context.Context, path string) (*mkvSource, []Track, error) {
	f, r, err := openMatroska(path)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()
	src := &mkvSource{path: path, name: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
		tracks: r.Tracks, chapters: r.Chapters, duration: r.Duration}

	samples := map[uint64][]byte{}
	counts := map[uint64]int{}
	wanted := 0
	for _, t := range r.Tracks {
		if t.Type == mkv.TypeVideo || t.Type == mkv.TypeAudio {
			wanted++
		}
	}
	done := 0
	for read := 0; done < wanted && read < 20000; read++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		p, err := r.Next()
		if err != nil {
			break
		}
		t := r.Track(p.Track)
		if t == nil || counts[p.Track] >= mkvProbeFrames {
			continue
		}
		if t.Type == mkv.TypeVideo {
			samples[p.Track] = append(samples[p.Track], annexB(p.Data, nalLengthSize(t.CodecPrivate))...)
		} else {
			samples[p.Track] = append(samples[p.Track], p.Data...)
		}
		if counts[p.Track]++; counts[p.Track] == mkvProbeFrames && (t.Type == mkv.TypeVideo || t.Type == mkv.TypeAudio) {
			done++
		}
	}

	var tracks []Track
	for _, t := range r.Tracks {
		tr := Track{ID: int(t.Number), Lang: t.Language, Name: t.Name, Forced: t.Forced} //nolint:gosec // track numbers are small
		b := samples[t.Number]
		switch t.CodecID {
		case "V_MPEG4/ISO/AVC":
			es := append(avcCParams(t.CodecPrivate), b...)
			tr.Type, tr.StreamID, tr.Info = "H.264", "V_MPEG4/ISO/AVC", describeVideoES(es, false)
			tracks = append(tracks, tr)
			if hasNAL(b, 15) || hasNAL(b, 20) {
				// The same track is the dependent view too.
				dep := tr
				dep.Type, dep.StreamID, dep.Info = "MVC", "V_MPEG4/ISO/MVC", describeVideoES(es, true)
				tracks = append(tracks, dep)
			}
			continue
		case "V_MPEG4/ISO/MVC":
			// A separate dependent-view track, paired by timestamp.
			tr.Type, tr.StreamID = "MVC", "V_MPEG4/ISO/MVC"
			tr.Info = describeVideoES(append(avcCParams(t.CodecPrivate), b...), true)
			src.dependent = t.Number
		case "A_AC3", "A_EAC3":
			a, _ := esinfo.ParseAC3(b, t.CodecID == "A_EAC3")
			tr.StreamID, tr.Type, tr.Info = "A_AC3", a.Codec, a.Describe()
		case "A_TRUEHD":
			a, _ := esinfo.ParseTrueHD(b)
			tr.StreamID, tr.Type, tr.Info = "A_AC3", a.Codec, a.Describe()
		case "A_DTS":
			hd := ""
			if bytes.Contains(b, []byte{0x64, 0x58, 0x20, 0x25}) { // DTS-HD substream
				hd = "DTS-HD High Resolution"
				if bytes.Contains(b, []byte{0x41, 0xa2, 0x95, 0x47}) { // lossless extension
					hd = "DTS-HD Master Audio"
				}
			}
			a, _ := esinfo.ParseDTS(b, hd)
			tr.StreamID, tr.Type, tr.Info = "A_DTS", a.Codec, a.Describe()
		case "A_PCM/INT/LIT":
			a := esinfo.Audio{Codec: "LPCM", Channels: t.Channels, LFE: t.Channels == 6 || t.Channels == 8,
				SampleRate: int(t.SampleRate), Bits: t.BitDepth}
			tr.StreamID, tr.Type, tr.Info = "A_LPCM", "LPCM", a.Describe()
		case "S_HDMV/PGS":
			tr.Type, tr.StreamID = "PGS", "S_HDMV/PGS"
			tr.Info = fmt.Sprintf("Presentation Graphic Stream #%d", countKind(tracks, KindSubtitle))
		default:
			continue // codecs the mux does not carry (AAC, FLAC, text subtitles)
		}
		tracks = append(tracks, tr)
	}
	if len(tracks) == 0 {
		return nil, nil, fmt.Errorf("%s has no tracks this understands", path)
	}
	return src, tracks, nil
}

// nalLengthSize is the NAL length field size an avcC gives (4 if absent).
func nalLengthSize(avcC []byte) int {
	if len(avcC) < 5 {
		return 4
	}
	return int(avcC[4]&3) + 1
}

// avcCParams returns an avcC's parameter sets in Annex B.
func avcCParams(avcC []byte) []byte {
	if len(avcC) < 7 {
		return nil
	}
	var out []byte
	b := avcC[5:]
	for set := 0; set < 2; set++ {
		if len(b) < 1 {
			break
		}
		n := int(b[0])
		if set == 0 {
			n &= 0x1f
		}
		b = b[1:]
		for i := 0; i < n && len(b) >= 2; i++ {
			l := int(binary.BigEndian.Uint16(b))
			if 2+l > len(b) {
				return out
			}
			out = append(out, 0, 0, 0, 1)
			out = append(out, b[2:2+l]...)
			b = b[2+l:]
		}
	}
	return out
}

// annexB turns length-prefixed NAL units into Annex B.
func annexB(b []byte, size int) []byte {
	out := make([]byte, 0, len(b)+16)
	for len(b) >= size {
		var n int
		for _, c := range b[:size] {
			n = n<<8 | int(c)
		}
		b = b[size:]
		if n > len(b) {
			n = len(b)
		}
		out = append(out, 0, 0, 0, 1)
		out = append(out, b[:n]...)
		b = b[n:]
	}
	return out
}

// hasNAL reports whether an Annex B stream holds a NAL unit of a type.
func hasNAL(b []byte, typ byte) bool {
	for i := 0; i+3 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 && b[i+3]&0x1f == typ {
			return true
		}
	}
	return false
}

// mkvDemux reads a Matroska source: it is the decoder's access-unit source,
// and writes the selected audio and subtitle tracks as a side effect.
type mkvDemux struct {
	src     *mkvSource
	sel     Selection
	report  Reporter
	tmp     string
	f       *os.File
	r       *mkv.Reader
	video   uint64
	nalSize int
	params  []byte // the avcC's parameter sets, ahead of the first picture
	writers map[uint64]*esWriter
	// t0 is the first block's time, the output's zero; anything earlier
	// (out of order) is dropped.
	t0      time.Duration
	t0Known bool
	deps    map[time.Duration][]byte // a separate dependent track, by time
	aus     int
	noDep   int
	dropped map[uint64]int64
}

func newMkvDemux(src *mkvSource, sel Selection, tmp string, report Reporter) *mkvDemux {
	return &mkvDemux{src: src, sel: sel, report: report, tmp: tmp, video: uint64(sel.Base.ID), //nolint:gosec // a track number
		writers: map[uint64]*esWriter{}, deps: map[time.Duration][]byte{}, dropped: map[uint64]int64{}}
}

func (g *mkvDemux) start() error {
	f, r, err := openMatroska(g.src.path)
	if err != nil {
		return err
	}
	g.f, g.r = f, r
	if t := r.Track(g.video); t != nil {
		g.nalSize = nalLengthSize(t.CodecPrivate)
		g.params = avcCParams(t.CodecPrivate)
	}
	if err := os.MkdirAll(g.tmp, 0o750); err != nil { //nolint:gosec // the operator's work directory
		return err
	}
	for _, t := range append(append([]Track(nil), g.sel.Audio...), g.sel.Subtitles...) {
		w, err := createESWriter(g.tmp, g.src.name, t)
		if err != nil {
			return err
		}
		if w.lpcm != nil {
			// Matroska's PCM is already little-endian WAV samples.
			rt := r.Track(uint64(t.ID)) //nolint:gosec // a track number
			w.lpcm = nil
			w.wav = &rawWAV{f: w.f, channels: rt.Channels, rate: int(rt.SampleRate), bits: rt.BitDepth}
			if err := w.wav.header(0); err != nil {
				return err
			}
		}
		g.writers[uint64(t.ID)] = w //nolint:gosec // a track number
	}
	return nil
}

// Next returns the next access unit, both views in one.
func (g *mkvDemux) Next() (base, dep []byte, pts int64, err error) {
	for {
		p, err := g.r.Next()
		if err != nil {
			if err == io.EOF {
				return nil, nil, 0, io.EOF
			}
			return nil, nil, 0, err
		}
		if !g.t0Known {
			// The output starts with the file's first block, whichever track
			// it belongs to; a track that starts later starts later.
			g.t0, g.t0Known = p.Time, true
		}
		switch {
		case p.Track == g.video:
			au := annexB(p.Data, g.nalSize)
			if g.params != nil {
				au = append(g.params, au...)
				g.params = nil
			}
			pts := int64(p.Time) * 9 / 100000 // 90 kHz
			if g.src.dependent != 0 {
				d, ok := g.deps[p.Time]
				if ok {
					delete(g.deps, p.Time)
				} else if hasSlice(au) {
					g.noDep++
				}
				if hasSlice(au) {
					g.aus++
				}
				return au, d, pts, nil
			}
			if hasSlice(au) {
				g.aus++
				if !hasNAL(au, 20) {
					g.noDep++
				}
			}
			return au, nil, pts, nil
		case p.Track == g.src.dependent && g.src.dependent != 0:
			g.deps[p.Time] = annexB(p.Data, g.nalSize)
		default:
			if err := g.write(p); err != nil {
				return nil, nil, 0, err
			}
		}
	}
}

// write puts an audio or subtitle frame in its track's file, dropping what
// comes before the first picture.
func (g *mkvDemux) write(p mkv.Packet) error {
	w := g.writers[p.Track]
	if w == nil || w.err != nil {
		return nil
	}
	if p.Time < g.t0 {
		g.dropped[p.Track] += int64(len(p.Data))
		return nil
	}
	var err error
	switch {
	case w.track.StreamID == "S_HDMV/PGS":
		at := int64(p.Time-g.t0) * 9 / 100000
		err = writeSup(w, at, at, p.Data)
	case w.wav != nil:
		err = w.wav.write(p.Data)
		w.n += int64(len(p.Data))
	default:
		w.mark(p.Time-g.t0, p.Data)
		_, err = w.w.Write(p.Data)
		w.n += int64(len(p.Data))
	}
	if err != nil {
		w.err = err
		return fmt.Errorf("writing %s: %w", filepath.Base(w.path), err)
	}
	return nil
}

func (g *mkvDemux) finish() ([]extra, error) {
	if g.f != nil {
		_ = g.f.Close()
		g.f = nil
	}
	var extras []extra
	var empty []Track
	var firstErr error
	for _, t := range append(append([]Track(nil), g.sel.Audio...), g.sel.Subtitles...) {
		w := g.writers[uint64(t.ID)] //nolint:gosec // a track number
		if w == nil {
			continue
		}
		if w.wav != nil {
			if err := w.wav.header(uint32(min(w.n, 1<<32-1))); err != nil && firstErr == nil { //nolint:gosec // bounded
				firstErr = err
			}
		}
		if err := w.f.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		if w.err != nil && firstErr == nil {
			firstErr = w.err
		}
		if w.n == 0 {
			empty = append(empty, t)
			continue
		}
		extras = append(extras, extra{path: w.path, track: t, sync: w.sync})
	}
	reportEmpty(g.report, empty)
	if len(g.dropped) > 0 {
		g.report.Report("dropped audio and subtitle frames timed before the file's start")
	}
	if g.noDep > 0 {
		g.report.Report("warning: %d of %d pictures had no dependent view", g.noDep, g.aus)
	}
	return extras, firstErr
}

// rawWAV writes PCM that is already in WAV's sample layout.
type rawWAV struct {
	f                    *os.File
	channels, rate, bits int
}

func (w *rawWAV) write(b []byte) error {
	_, err := w.f.Write(b)
	return err
}

// header writes (or, at the end, rewrites) the WAVE_FORMAT_EXTENSIBLE
// header for dataLen bytes of samples.
func (w *rawWAV) header(dataLen uint32) error {
	if w.channels <= 0 || w.rate <= 0 || w.bits <= 0 {
		return errors.New("a PCM track does not give its channels, rate and sample size")
	}
	h := wavHeader(w.channels, w.rate, w.bits, wavMask(w.channels, w.channels == 6 || w.channels == 8), dataLen)
	_, err := w.f.WriteAt(h, 0)
	if err == nil && dataLen == 0 {
		_, err = w.f.Seek(int64(len(h)), io.SeekStart)
	}
	return err
}

// runMatroska is runBuiltin for a Matroska source.
func (r *Runner) runMatroska(ctx context.Context, tmp string) error {
	if r.Opts.Remux {
		return fmt.Errorf("--remux copies a disc's own MVC stream into an m2ts; %s is already a remux", filepath.Base(r.Opts.Input))
	}
	r.Report.Report("probing %s", r.Opts.Input)
	src, tracks, err := probeMatroska(ctx, r.Opts.Input)
	if err != nil {
		return err
	}
	sel, err := SelectTracks(tracks)
	if err != nil {
		return err
	}
	all := sel.Audio
	if sel, err = sel.Apply(r.Opts.Audio, r.Opts.Subs); err != nil {
		return err
	}
	if r.Opts.KeepFallback {
		sel.Audio = withFallbacks(sel.Audio, all, r.Report)
	}
	r.Selected = sel
	r.Report.Report("source: video track %d (both views), %d audio, %d subtitle", sel.Base.ID, len(sel.Audio), len(sel.Subtitles))
	for _, a := range sel.Audio {
		r.Report.Report("audio: %s", DescribeAudio(a))
	}
	r.length = src.duration
	g := newMkvDemux(src, sel, tmp, r.Report)
	r.timeline = func(pts int64) time.Duration { return ticks90k(pts) - g.t0 }
	if err := g.start(); err != nil {
		if g.f != nil {
			_ = g.f.Close()
		}
		return err
	}
	video := filepath.Join(tmp, "stacked"+r.Opts.Codec.streamExt())
	decErr := r.decodeAndEncode(ctx, mvc.Source{Format: mvc.FormatAccessUnits, AccessUnits: g.Next}, nil, video)
	extras, finErr := g.finish()
	if decErr != nil {
		return decErr
	}
	if finErr != nil {
		return finErr
	}
	return r.mux(ctx, video, extras, src.chapters)
}

// withFallbacks adds, for each kept lossless track without a lossy core
// inside it, the best lossy track in the same language. A remux keeps
// TrueHD's AC-3 core (or an E-AC-3 "compatibility" track) as a track of its
// own, and that is what --keep-fallback keeps on a disc.
func withFallbacks(kept, all []Track, report Reporter) []Track {
	out := append([]Track(nil), kept...)
	has := map[int]bool{}
	for _, t := range kept {
		has[t.ID] = true
	}
	for _, t := range kept {
		// DTS-HD carries its DTS core inside, and the mux keeps that.
		if audioTier(t) != tierLossless || strings.Contains(strings.ToUpper(t.Type), "DTS") {
			continue
		}
		var lossy []Track
		for _, c := range all {
			if !has[c.ID] && audioTier(c) != tierLossless && audioTier(c) != tierUnknown && strings.EqualFold(c.Lang, t.Lang) {
				lossy = append(lossy, c)
			}
		}
		if f, ok := BestAudio(lossy); ok {
			has[f.ID] = true
			out = append(out, f)
			report.Report("keeping %s as the fallback for %s", DescribeAudio(f), DescribeAudio(t))
		}
	}
	return out
}
