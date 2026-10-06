package convert

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/brunoga/mvc"
	"github.com/brunoga/mvc/internal/bdmv"
	"github.com/brunoga/mvc/internal/esinfo"
	"github.com/brunoga/mvc/m2ts"
)

// The built-in demuxer: reads a Blu-ray — an image, a BDMV, a playlist, or a
// bare .m2ts/.ssif — straight from where it is, hands the video access
// units to the decoder and writes the other tracks out as elementary
// streams, all in one pass. Nothing is extracted or copied.
//
// The track files are what tsMuxeR's demux wrote, which this replaced: raw
// PES payloads for audio, the AC-3 core kept inside a TrueHD stream, .sup
// entries as "PG" + PTS + DTS rebased to the first picture. Where tsMuxeR
// did something a player would not — keeping the audio that precedes the
// playlist's IN_time — this trims instead, since muxing that lead-in puts
// the sound ahead of the picture by exactly that much.

// clipRef is one stream file to read, in playlist order.
type clipRef struct {
	path      string // on the disc, for messages
	open      func() (io.ReadSeekCloser, int64, error)
	dependent *clipRef // the dependent view's own file, when not interleaved
	inTime    int64    // 90 kHz; -1 when unknown
	outTime   int64
}

// goSource is a resolved input for the built-in demuxer.
type goSource struct {
	disc     bdmv.Disc
	playlist *bdmv.Playlist
	name     string // base name for the demuxed files
	clips    []clipRef
	chapters []time.Duration
	duration time.Duration
	// langs maps a PID to the language the playlist gives it, for streams
	// whose PMT entry carries none.
	langs           map[uint16]string
	baseViewIsRight bool
	knownEye        bool
}

// resolveGo turns the input into the stream files to read.
func resolveGo(in string, report Reporter) (*goSource, error) {
	st, err := os.Stat(in) //nolint:gosec // the operator's input is the point
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", in, err)
	}
	ext := strings.ToLower(filepath.Ext(in))
	switch {
	case st.IsDir() || ext == ".iso":
		d, err := bdmv.Open(in)
		if err != nil {
			return nil, err
		}
		if ext == ".iso" {
			report.Report("reading the disc image in place (no mount, no extraction)")
		}
		return chooseGo(d, report)
	case ext == ".mpls":
		// The BDMV is the playlist's grandparent.
		d, err := bdmv.Open(filepath.Dir(filepath.Dir(in)))
		if err != nil {
			return nil, err
		}
		return playlistSource(d, filepath.Base(in), report)
	default:
		path := in
		src := &goSource{
			name:  strings.TrimSuffix(filepath.Base(in), filepath.Ext(in)),
			langs: clipLanguages(in),
			clips: []clipRef{{path: in, inTime: -1, outTime: -1, open: func() (io.ReadSeekCloser, int64, error) {
				f, err := os.Open(path) //nolint:gosec // the operator's input
				if err != nil {
					return nil, 0, err
				}
				fi, err := f.Stat()
				if err != nil {
					_ = f.Close()
					return nil, 0, err
				}
				return f, fi.Size(), nil
			}}},
		}
		return src, nil
	}
}

// clipLanguages finds the languages of a bare stream file that sits in a
// BDMV tree (BDMV/STREAM/00800.m2ts, or STREAM/SSIF/00800.ssif) in its clip
// info, which is where a disc keeps them: the stream's own tables usually
// do not say. Nil when the file is not in such a tree.
func clipLanguages(path string) map[uint16]string {
	dir := filepath.Dir(path)
	if strings.EqualFold(filepath.Base(dir), "SSIF") {
		dir = filepath.Dir(dir)
	}
	if !strings.EqualFold(filepath.Base(dir), "STREAM") {
		return nil
	}
	d, err := bdmv.Open(filepath.Dir(dir))
	if err != nil {
		return nil
	}
	clip := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	b, err := d.ReadFile(bdmv.ClipInfo(d, clip))
	if err != nil {
		return nil
	}
	streams, err := bdmv.ParseCLPI(b)
	if err != nil {
		return nil
	}
	langs := map[uint16]string{}
	for _, s := range streams {
		if s.Lang != "" {
			langs[s.PID] = s.Lang
		}
	}
	return langs
}

// chooseGo picks the title by reading the playlists themselves — instantly,
// and without touching a stream: the 3D playlist holding the most distinct
// content. Counting each stretch of a clip once matters: discs carry
// playlists that loop a short clip a hundred times (menus, demo loops,
// decoys for rippers), which run longer than the feature. Among equals the
// one with more chapters wins — a disc often has a bare copy of the feature
// playlist beside the real one — then the one with fewer items, then the
// lower number.
func chooseGo(d bdmv.Disc, report Reporter) (*goSource, error) {
	names, err := bdmv.Playlists(d)
	if err != nil {
		return nil, err
	}
	var cands []cand
	for _, n := range names {
		pl, err := bdmv.ReadPlaylist(d, n)
		if err != nil || !pl.ThreeD() {
			continue
		}
		cands = append(cands, cand{n, pl})
	}
	if len(cands) == 0 {
		// No MVC sub-path anywhere: the dependent view may be in the same
		// transport stream as the base view (AVCHD 3D recordings do this).
		// The program tables of the longest titles say.
		cands = inMuxCandidates(d, names)
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("none of the %d playlists in %s is 3D (no MVC dependent view in any of them)",
			len(names), d.Describe())
	}
	best := cands[0]
	for _, c := range cands[1:] {
		if betterTitle(c, best) {
			best = c
		}
	}
	report.Report("chose %s (%s) from %d playlists, %d of them 3D", best.name,
		best.pl.Duration().Round(time.Second), len(names), len(cands))
	if best.pl.BaseViewIsRight {
		report.Report("the disc's base view is the right eye, so the eyes will be swapped")
	}
	return playlistSource(d, best.name, report)
}

// betterTitle orders candidate titles: more distinct content, then more
// chapters, then fewer items, then the lower playlist number.
func betterTitle(a, b cand) bool {
	ua, ub := a.pl.UniqueDuration(), b.pl.UniqueDuration()
	// Durations within a second are the same title.
	if diff := ua - ub; diff > time.Second || diff < -time.Second {
		return ua > ub
	}
	if ca, cb := len(a.pl.Chapters()), len(b.pl.Chapters()); ca != cb {
		return ca > cb
	}
	if len(a.pl.Items) != len(b.pl.Items) {
		return len(a.pl.Items) < len(b.pl.Items)
	}
	return a.name < b.name
}

// cand is a playlist considered as the title.
type cand struct {
	name string
	pl   *bdmv.Playlist
}

// inMuxCandidates finds playlists whose first clip carries an MVC stream in
// its own transport stream, checking the five longest.
func inMuxCandidates(d bdmv.Disc, names []string) []cand {
	var all []cand
	for _, n := range names {
		if pl, err := bdmv.ReadPlaylist(d, n); err == nil && len(pl.Items) > 0 {
			all = append(all, cand{n, pl})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].pl.UniqueDuration() > all[j].pl.UniqueDuration() })
	var out []cand
	for i, c := range all {
		if i == 5 {
			break
		}
		path, _ := bdmv.StreamPath(d, c.pl.Items[0])
		f, _, err := d.Open(path)
		if err != nil {
			continue
		}
		r := m2ts.NewReader(io.LimitReader(f, 4<<20))
		if prog, err := r.ReadProgram(); err == nil {
			for _, s := range prog.Streams {
				if s.Type == m2ts.TypeMVC {
					out = append(out, c)
					break
				}
			}
		}
		_ = f.Close()
	}
	return out
}

func playlistSource(d bdmv.Disc, name string, report Reporter) (*goSource, error) {
	pl, err := bdmv.ReadPlaylist(d, name)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", name, err)
	}
	src := &goSource{
		disc: d, playlist: pl,
		name:            strings.TrimSuffix(name, filepath.Ext(name)),
		chapters:        pl.Chapters(),
		duration:        pl.Duration(),
		langs:           map[uint16]string{},
		baseViewIsRight: pl.BaseViewIsRight,
		knownEye:        true,
	}
	for i, it := range pl.Items {
		for _, s := range it.Streams {
			if s.Lang != "" && src.langs[s.PID] == "" {
				src.langs[s.PID] = s.Lang
			}
		}
		if len(it.Angles) > 0 && i == 0 {
			report.Report("multi-angle title: using the first angle")
		}
		path, ssif := bdmv.StreamPath(d, it)
		c := clipRef{path: path, inTime: int64(it.InTime) * 2, outTime: int64(it.OutTime) * 2}
		c.open = opener(d, path)
		if it.DependentClip != "" && !ssif {
			dp := bdmv.DependentStreamPath(d, it)
			if !d.Exists(dp) {
				return nil, fmt.Errorf("the dependent view clip %s of %s is missing", dp, name)
			}
			c.dependent = &clipRef{path: dp, open: opener(d, dp), inTime: c.inTime, outTime: c.outTime}
		}
		src.clips = append(src.clips, c)
	}
	if len(src.clips) == 0 {
		return nil, fmt.Errorf("%s has no play items", name)
	}
	return src, nil
}

func opener(d bdmv.Disc, path string) func() (io.ReadSeekCloser, int64, error) {
	return func() (io.ReadSeekCloser, int64, error) { return d.Open(path) }
}

// --- probing ---------------------------------------------------------------------

// probeBudget bounds how much of a stream the probe reads: enough for the
// first frames of every track, which on a Blu-ray come within a few
// megabytes of the start.
const probeBudget = 24 << 20

// probeGo lists the tracks of a source the way tsMuxeR lists them: one
// Track per program stream, described from its first frames.
func probeGo(ctx context.Context, src *goSource) ([]Track, error) {
	c := src.clips[0]
	f, size, err := c.open()
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", c.path, err)
	}
	defer func() { _ = f.Close() }()
	r := m2ts.NewReader(io.LimitReader(f, probeBudget))
	prog, err := r.ReadProgram()
	if err != nil {
		return nil, fmt.Errorf("%s: no program table found: %w", c.path, err)
	}
	// Collect the first frames of each stream. An SSIF starts with the
	// dependent view's extent, whose PMT lists only that view, so reading
	// goes on until the other clip's tables have been seen too.
	samples := map[uint16][]byte{}
	sampled := func(s m2ts.ProgramStream) bool {
		switch s.Type {
		case m2ts.TypeAVC, m2ts.TypeMVC, m2ts.TypeLPCM, m2ts.TypeAC3, m2ts.TypeDTS, m2ts.TypeTrueHD,
			m2ts.TypeEAC3, m2ts.TypeDTSHDHR, m2ts.TypeDTSHDMA:
			return len(samples[s.PID]) >= 64<<10
		}
		return true
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		done := r.Packets*192 >= 8<<20
		for _, s := range prog.Streams {
			if !sampled(s) {
				done = false
				break
			}
		}
		if done {
			break
		}
		p, err := r.Next()
		if err != nil {
			break
		}
		if len(samples[p.PID]) < 64<<10 {
			samples[p.PID] = append(samples[p.PID], p.Payload...)
		}
	}
	// The dependent view's clip may be separate: its SPS is there.
	if c.dependent != nil {
		if df, _, err := c.dependent.open(); err == nil {
			dr := m2ts.NewReader(io.LimitReader(df, 4<<20))
			if dp, err := dr.ReadProgram(); err == nil {
				for _, s := range dp.Streams {
					if _, have := prog.Stream(s.PID); !have {
						prog.Streams = append(prog.Streams, s)
					}
				}
				for {
					p, err := dr.Next()
					if err != nil {
						break
					}
					if s, ok := dp.Stream(p.PID); ok && s.Type == m2ts.TypeMVC {
						samples[p.PID] = append(samples[p.PID], p.Payload...)
						break
					}
				}
			}
			_ = df.Close()
		}
	}
	var tracks []Track
	for _, s := range prog.Streams {
		t := Track{ID: int(s.PID), Lang: s.Lang}
		if t.Lang == "" {
			t.Lang = src.langs[s.PID]
		}
		b := samples[s.PID]
		switch s.Type {
		case m2ts.TypeAVC, m2ts.TypeMVC:
			t.Type, t.StreamID = "H.264", "V_MPEG4/ISO/AVC"
			if s.Type == m2ts.TypeMVC {
				t.Type, t.StreamID = "MVC", "V_MPEG4/ISO/MVC"
			}
			t.Info = describeVideoES(b, s.Type == m2ts.TypeMVC)
		case m2ts.TypeAC3, m2ts.TypeEAC3:
			t.StreamID = "A_AC3"
			a, _ := esinfo.ParseAC3(b, s.Type == m2ts.TypeEAC3)
			t.Type, t.Info = a.Codec, a.Describe()
		case m2ts.TypeTrueHD:
			t.StreamID = "A_AC3"
			a, _ := esinfo.ParseTrueHD(b)
			t.Type, t.Info = a.Codec, a.Describe()
		case m2ts.TypeDTS, m2ts.TypeDTSHDHR, m2ts.TypeDTSHDMA:
			t.StreamID = "A_DTS"
			hd := map[byte]string{m2ts.TypeDTSHDHR: "DTS-HD High Resolution", m2ts.TypeDTSHDMA: "DTS-HD Master Audio"}[s.Type]
			a, _ := esinfo.ParseDTS(b, hd)
			t.Type, t.Info = a.Codec, a.Describe()
		case m2ts.TypeLPCM:
			t.StreamID = "A_LPCM"
			a, _ := esinfo.ParseLPCM(b)
			t.Type, t.Info = "LPCM", a.Describe()
		case m2ts.TypePGS:
			t.Type, t.StreamID = "PGS", "S_HDMV/PGS"
			t.Info = fmt.Sprintf("Presentation Graphic Stream #%d", countKind(tracks, KindSubtitle))
		default:
			continue // interactive graphics, text subtitles, secondary streams
		}
		tracks = append(tracks, t)
	}
	if len(tracks) == 0 {
		return nil, fmt.Errorf("%s lists no streams this understands", c.path)
	}
	// Video first, then audio, then subtitles, each in stream order, which is
	// the order the disc lists them and what the output keeps.
	sort.SliceStable(tracks, func(i, j int) bool { return kindOrder(tracks[i]) < kindOrder(tracks[j]) })
	if src.duration == 0 {
		src.duration = fileDuration(c, prog, size)
	}
	_ = size
	return tracks, nil
}

func kindOrder(t Track) int {
	switch t.Kind() {
	case KindBaseView:
		return 0
	case KindDependentView:
		return 1
	case KindAudio:
		return 2
	case KindSubtitle:
		return 3
	}
	return 4
}

func countKind(tracks []Track, k Kind) int {
	n := 0
	for _, t := range tracks {
		if t.Kind() == k {
			n++
		}
	}
	return n
}

// describeVideoES renders the SPS of a video stream like tsMuxeR's listing.
func describeVideoES(b []byte, mvcView bool) string {
	want := byte(7)
	if mvcView {
		want = 15
	}
	for i := 0; i+5 < len(b); i++ {
		if b[i] != 0 || b[i+1] != 0 || b[i+2] != 1 || b[i+3]&0x1f != want {
			continue
		}
		end := len(b)
		for j := i + 4; j+2 < len(b); j++ {
			if b[j] == 0 && b[j+1] == 0 && (b[j+2] == 1 || (j+3 < len(b) && b[j+2] == 0 && b[j+3] == 1)) {
				end = j
				break
			}
		}
		info, err := mvc.ParseSPSInfo(b[i:end])
		if err != nil {
			continue
		}
		prefix := ""
		if mvcView {
			prefix = "H.264/MVC Views: 2 "
		}
		scan := "p"
		if !info.FrameMbsOnly {
			scan = "i"
		}
		fps := ""
		if info.FPSNum > 0 {
			fps = fmt.Sprintf("  Frame rate: %.3f", float64(info.FPSNum)/float64(info.FPSDen))
		}
		return fmt.Sprintf("%sProfile: %s@%d.%d  Resolution: %d:%d%s%s", prefix, info.Profile(),
			info.LevelIdc/10, info.LevelIdc%10, info.Width, info.Height, scan, fps)
	}
	return ""
}

// fileDuration estimates a bare file's length from the first and last video
// timestamps, reading only the file's ends.
func fileDuration(c clipRef, prog *m2ts.Program, size int64) time.Duration {
	var videoPID uint16
	for _, s := range prog.Streams {
		if s.Type == m2ts.TypeAVC {
			videoPID = s.PID
			break
		}
	}
	if videoPID == 0 || size < 8<<20 {
		return 0
	}
	first, last := int64(-1), int64(-1)
	for _, off := range []int64{0, size - 8<<20} {
		f, _, err := c.open()
		if err != nil {
			return 0
		}
		if _, err := f.Seek(off-off%192, io.SeekStart); err != nil {
			_ = f.Close()
			return 0
		}
		r := m2ts.NewReader(io.LimitReader(f, 8<<20))
		r.Select(videoPID)
		for {
			p, err := r.Next()
			if err != nil {
				break
			}
			if p.PTS < 0 {
				continue
			}
			if off == 0 {
				if first < 0 || p.PTS < first {
					first = p.PTS
				}
			} else if p.PTS > last {
				last = p.PTS
			}
		}
		_ = f.Close()
	}
	if first < 0 || last <= first {
		return 0
	}
	return time.Duration(last-first) * time.Second / 90000
}

// --- demuxing ---------------------------------------------------------------------

// esWriter writes one audio or subtitle track as tsMuxeR would.
type esWriter struct {
	track Track
	path  string
	f     *os.File
	w     io.Writer
	n     int64
	lpcm  *wavWriter
	wav   *rawWAV // PCM already in WAV's layout (from Matroska)
	err   error
}

// auPES is a video PES waiting for its other view.
type auPES struct {
	pts, dts int64
	data     []byte
}

// goDemux reads the source clip by clip and is the decoder's access-unit
// source; the other selected tracks are written as a side effect.
type goDemux struct {
	src     *goSource
	sel     Selection
	report  Reporter
	tmp     string
	writers map[uint16]*esWriter
	extras  []extra
	// The output plays each clip from its IN_time to its OUT_time, one after
	// the other: a bare file from its first picture to its end. Pictures,
	// audio and subtitles outside the window are dropped, and subtitle times
	// are rebased onto the joined timeline.
	ins, outs, offsets []int64 // per clip, 90 kHz
	t0Known            bool    // the first clip's IN time is known
	pending            []m2ts.PES
	held               map[uint16]*m2ts.PES // last PES before IN, per PID
	dropped            map[uint16]int64

	clip    int
	r       *m2ts.Reader
	f       io.ReadSeekCloser
	depR    *m2ts.Reader
	depF    io.ReadSeekCloser
	basePID uint16
	depPID  uint16
	eof     bool
	queue   []auPES          // base view, decode order
	deps    map[int64][]byte // dependent view by DTS
	maxDep  int64
	noDep   int
	aus     int64
	packets int64
}

// pairWindow is how many base-view access units may wait for their
// dependent view. An SSIF interleaves the two views in units of several
// megabytes, so the other view of a picture can be seconds away.
const pairWindow = 2048

func newGoDemux(src *goSource, sel Selection, tmp string, report Reporter) *goDemux {
	g := &goDemux{src: src, sel: sel, report: report, tmp: tmp, writers: map[uint16]*esWriter{},
		deps: map[int64][]byte{}, dropped: map[uint16]int64{}, held: map[uint16]*m2ts.PES{}, maxDep: -1}
	g.basePID, g.depPID = uint16(sel.Base.ID), uint16(sel.Dependent.ID) //nolint:gosec // track ids are PIDs
	var acc int64
	for _, c := range src.clips {
		in, out := c.inTime, c.outTime
		if out < 0 {
			out = math.MaxInt64
		}
		g.ins, g.outs, g.offsets = append(g.ins, in), append(g.outs, out), append(g.offsets, acc)
		if in >= 0 && out != math.MaxInt64 {
			acc += out - in
		}
	}
	g.t0Known = g.ins[0] >= 0
	return g
}

// ptsTagShift places the clip index above a 33-bit timestamp, so a frame
// leaving the decoder (which lags its input) can be placed in its clip.
const ptsTagShift = 40

// tag marks a picture's timestamp with the clip it was read from.
func (g *goDemux) tag(pts int64) int64 {
	if pts < 0 {
		return pts
	}
	return int64(g.clip)<<ptsTagShift | pts
}

// KeepFrame reports whether a decoded picture, by its tagged timestamp, is
// inside its clip's window. Pictures the playlist does not play — before
// IN_time or from OUT_time — are decoded (others may reference them) but
// not output.
func (g *goDemux) KeepFrame(tagged int64) bool {
	if tagged < 0 {
		return true
	}
	k := int(tagged >> ptsTagShift)
	pts := tagged & (1<<ptsTagShift - 1)
	if k >= len(g.ins) {
		return true
	}
	if g.ins[k] >= 0 && pts < g.ins[k] {
		return false
	}
	return pts < g.outs[k]
}

// start opens the writers; a failure here is cheap, unlike one at the mux.
func (g *goDemux) start() error {
	if err := os.MkdirAll(g.tmp, 0o750); err != nil { //nolint:gosec // the operator's work directory
		return err
	}
	for _, t := range append(append([]Track(nil), g.sel.Audio...), g.sel.Subtitles...) {
		w, err := createESWriter(g.tmp, g.src.name, t)
		if err != nil {
			return err
		}
		g.writers[uint16(t.ID)] = w //nolint:gosec // track ids are PIDs
	}
	return nil
}

// fileSafe keeps the letters and digits of a string the disc supplied, for
// use in a file name.
func fileSafe(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// openClip starts reading clip i.
func (g *goDemux) openClip(i int) error {
	g.closeClip()
	c := g.src.clips[i]
	f, _, err := c.open()
	if err != nil {
		return fmt.Errorf("opening %s: %w", c.path, err)
	}
	g.f = f
	g.r = m2ts.NewReader(f)
	pids := []uint16{g.basePID, g.depPID}
	for pid := range g.writers {
		pids = append(pids, pid)
	}
	g.r.Select(pids...)
	if c.dependent != nil {
		df, _, err := c.dependent.open()
		if err != nil {
			return fmt.Errorf("opening %s: %w", c.dependent.path, err)
		}
		g.depF = df
		g.depR = m2ts.NewReader(df)
		g.depR.Select(g.depPID)
	}
	g.clip = i
	clear(g.held)
	if i > 0 {
		g.report.Report("reading %s (%d of %d)", c.path, i+1, len(g.src.clips))
	}
	return nil
}

func (g *goDemux) closeClip() {
	if g.f != nil {
		g.packets += g.r.Packets
		_ = g.f.Close()
		g.f, g.r = nil, nil
	}
	if g.depF != nil {
		_ = g.depF.Close()
		g.depF, g.depR = nil, nil
	}
}

// handle routes one PES.
func (g *goDemux) handle(p m2ts.PES) error {
	switch p.PID {
	case g.basePID:
		if !g.t0Known && p.PTS >= 0 {
			// A bare file plays from its first picture.
			g.ins[0], g.t0Known = p.PTS, true
			for _, q := range g.pending {
				if err := g.writeES(q); err != nil {
					return err
				}
			}
			g.pending = nil
		}
		g.queue = append(g.queue, auPES{pts: g.tag(p.PTS), dts: p.DTS, data: p.Payload})
	case g.depPID:
		g.addDep(p)
	default:
		if !g.t0Known {
			g.pending = append(g.pending, p)
			return nil
		}
		return g.writeES(p)
	}
	return nil
}

func (g *goDemux) addDep(p m2ts.PES) {
	if p.DTS > g.maxDep {
		g.maxDep = p.DTS
	}
	if _, dup := g.deps[p.DTS]; !dup {
		g.deps[p.DTS] = p.Payload
	}
}

// writeES windows an audio or subtitle PES to its clip's IN/OUT times and
// writes it to its track.
//
// The cut is at a PES boundary — one audio frame, a few milliseconds for
// TrueHD and 32 for AC-3 — and goes to whichever frame starts nearer IN, so
// the sound is within half a frame of the picture. tsMuxeR keeps everything
// before IN, which on a disc whose audio starts early puts the sound ahead
// of the picture by that much.
func (g *goDemux) writeES(p m2ts.PES) error {
	w := g.writers[p.PID]
	if w == nil || w.err != nil {
		return nil
	}
	in, out := g.ins[g.clip], g.outs[g.clip]
	if p.PTS >= 0 {
		if in >= 0 && p.PTS < in {
			if h := g.held[p.PID]; h != nil {
				g.dropped[p.PID] += int64(len(h.Payload))
			}
			q := p
			g.held[p.PID] = &q
			return nil
		}
		if p.PTS >= out {
			g.dropped[p.PID] += int64(len(p.Payload))
			return nil
		}
		if h := g.held[p.PID]; h != nil {
			delete(g.held, p.PID)
			if in-h.PTS < p.PTS-in {
				if err := g.emitES(w, *h); err != nil {
					return err
				}
			} else {
				g.dropped[p.PID] += int64(len(h.Payload))
			}
		}
	}
	return g.emitES(w, p)
}

// emitES writes one PES payload in the track's file format.
func (g *goDemux) emitES(w *esWriter, p m2ts.PES) error {
	var err error
	switch {
	case w.lpcm != nil:
		err = w.lpcm.write(p.Payload)
	case w.track.StreamID == "S_HDMV/PGS":
		// tsMuxeR's .sup: each segment behind "PG" and its PTS and DTS, on
		// the output's timeline (the first picture at 0).
		base := g.offsets[g.clip] - g.ins[g.clip]
		pts, dts := p.PTS+base, p.DTS+base
		if p.DTS < 0 {
			dts = pts
		}
		err = writeSup(w, pts, dts, p.Payload)
	default:
		_, err = w.w.Write(p.Payload)
		w.n += int64(len(p.Payload))
	}
	if err != nil {
		w.err = err
		return fmt.Errorf("writing %s: %w", filepath.Base(w.path), err)
	}
	return nil
}

// Next returns the next access unit: the base view and, when the stream has
// it, the dependent view for the same picture. io.EOF ends the source.
func (g *goDemux) Next() (base, dep []byte, pts int64, err error) {
	if g.r == nil && !g.eof {
		if err := g.openClip(0); err != nil {
			return nil, nil, 0, err
		}
	}
	for {
		// Emit the head of the queue when its dependent view is here, or
		// cannot come any more.
		if len(g.queue) > 0 {
			h := g.queue[0]
			d, ok := g.deps[h.dts]
			if !ok && h.dts >= 0 && g.depR != nil {
				// A separate dependent clip: read it up to this picture.
				for g.maxDep < h.dts {
					p, err := g.depR.Next()
					if err != nil {
						break
					}
					g.addDep(p)
				}
				d, ok = g.deps[h.dts]
			}
			if ok || g.eof || g.maxDep > h.dts || len(g.queue) > pairWindow {
				g.queue = g.queue[1:]
				if ok {
					delete(g.deps, h.dts)
					// Dependent pictures older than this one will never pair.
					for k := range g.deps {
						if k < h.dts {
							delete(g.deps, k)
						}
					}
				} else if hasSlice(h.data) {
					g.noDep++
				}
				if hasSlice(h.data) {
					g.aus++
				}
				return h.data, d, h.pts, nil
			}
		}
		if g.eof {
			return nil, nil, 0, io.EOF
		}
		p, err := g.r.Next()
		if err != nil {
			// The clip ended (or lost sync, which on a disc means the end
			// of readable data): move to the next clip or finish.
			if g.clip+1 < len(g.src.clips) {
				if err := g.openClip(g.clip + 1); err != nil {
					return nil, nil, 0, err
				}
				continue
			}
			g.closeClip()
			g.eof = true
			continue
		}
		if err := g.handle(p); err != nil {
			return nil, nil, 0, err
		}
	}
}

// hasSlice reports whether an access unit holds a coded picture: a stream
// can end with a PES of only SEI, which is not a picture and has no
// dependent view to pair with.
func hasSlice(au []byte) bool {
	for i := 0; i+3 < len(au); {
		j := bytes.Index(au[i:], []byte{0, 0, 1})
		if j < 0 || i+j+3 >= len(au) {
			return false
		}
		i += j + 3
		if t := au[i] & 0x1f; t == 1 || t == 5 {
			return true
		}
	}
	return false
}

// finish closes the track files and reports; it returns the files to mux.
func (g *goDemux) finish() ([]extra, error) {
	g.closeClip()
	var firstErr error
	for _, t := range append(append([]Track(nil), g.sel.Audio...), g.sel.Subtitles...) {
		w := g.writers[uint16(t.ID)] //nolint:gosec // track ids are PIDs
		if w == nil {
			continue
		}
		if w.lpcm != nil {
			if err := w.lpcm.close(); err != nil && firstErr == nil {
				firstErr = err
			}
			w.n = w.lpcm.n
		}
		if err := w.f.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		if w.err != nil && firstErr == nil {
			firstErr = w.err
		}
		if w.n == 0 {
			g.report.Report("warning: %s track %d demuxed to nothing; it will be missing from the output", t.StreamID, t.ID)
			continue
		}
		g.extras = append(g.extras, extra{path: w.path, track: t})
	}
	if len(g.dropped) > 0 {
		var most int64
		for _, b := range g.dropped {
			most = max(most, b)
		}
		g.report.Report("trimmed the audio and subtitles outside the played section (up to %d KiB per track): a player never plays them", most>>10)
	}
	if g.noDep > 0 {
		g.report.Report("warning: %d of %d pictures had no dependent view", g.noDep, g.aus)
	}
	return g.extras, firstErr
}

// --- LPCM to WAV ---------------------------------------------------------------------

// wavWriter converts Blu-ray LPCM — big-endian samples behind a 4-byte PES
// header, 20-bit ones padded to 24 — into a WAVE_FORMAT_EXTENSIBLE file.
type wavWriter struct {
	f        *os.File
	n        int64
	channels int
	rate     int
	bits     int
	lfe      bool
	started  bool
	buf      []byte
}

func newWAVWriter(f *os.File) *wavWriter { return &wavWriter{f: f} }

func (w *wavWriter) write(payload []byte) error {
	if len(payload) < 4 {
		return nil
	}
	if !w.started {
		a, ok := esinfo.ParseLPCM(payload)
		if !ok {
			return errors.New("unreadable LPCM header")
		}
		w.channels, w.rate, w.bits, w.lfe = a.Channels, a.SampleRate, a.Bits, a.LFE
		if w.bits == 20 {
			w.bits = 24
		}
		if err := w.header(0); err != nil {
			return err
		}
		w.started = true
	}
	data := payload[4:]
	bps := w.bits / 8
	frame := bps * w.channels
	// Blu-ray pads an odd channel count to an even one with a silent
	// channel; the WAV keeps the count the header declares.
	stored := w.channels
	if stored%2 == 1 {
		stored++
	}
	storedFrame := bps * stored
	w.buf = w.buf[:0]
	for i := 0; i+storedFrame <= len(data); i += storedFrame {
		for ch := 0; ch < w.channels; ch++ {
			src := wavChannel(w.channels, w.lfe, ch)
			s := data[i+src*bps : i+src*bps+bps]
			for k := bps - 1; k >= 0; k-- {
				w.buf = append(w.buf, s[k])
			}
		}
	}
	_ = frame
	n, err := w.f.Write(w.buf)
	w.n += int64(n)
	return err
}

// wavChannel maps a WAV channel index to the Blu-ray channel holding it.
// Blu-ray and WAV agree up to 5.1; 7.1 differs in that Blu-ray stores the
// back pair before the side pair.
func wavChannel(channels int, lfe bool, ch int) int {
	if channels == 8 && lfe {
		switch ch {
		case 4:
			return 6
		case 5:
			return 7
		case 6:
			return 4
		case 7:
			return 5
		}
	}
	return ch
}

func (w *wavWriter) header(dataLen uint32) error {
	h := wavHeader(w.channels, w.rate, w.bits, wavMask(w.channels, w.lfe), dataLen)
	if _, err := w.f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err := w.f.Write(h)
	return err
}

// wavHeader is a WAVE_FORMAT_EXTENSIBLE header for dataLen bytes of PCM.
func wavHeader(channels, rate, bits int, mask, dataLen uint32) []byte {
	h := make([]byte, 68)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], 60+dataLen)
	copy(h[8:], "WAVE")
	copy(h[12:], "fmt ")
	binary.LittleEndian.PutUint32(h[16:], 40)
	binary.LittleEndian.PutUint16(h[20:], 0xfffe)                       // extensible
	binary.LittleEndian.PutUint16(h[22:], uint16(channels))             //nolint:gosec // small
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))                 //nolint:gosec // small
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*channels*bits/8)) //nolint:gosec // small
	binary.LittleEndian.PutUint16(h[32:], uint16(channels*bits/8))      //nolint:gosec // small
	binary.LittleEndian.PutUint16(h[34:], uint16(bits))                 //nolint:gosec // small
	binary.LittleEndian.PutUint16(h[36:], 22)
	binary.LittleEndian.PutUint16(h[38:], uint16(bits)) //nolint:gosec // small
	binary.LittleEndian.PutUint32(h[40:], mask)
	// KSDATAFORMAT_SUBTYPE_PCM
	copy(h[44:], []byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x10, 0x00, 0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71})
	copy(h[60:], "data")
	binary.LittleEndian.PutUint32(h[64:], dataLen)
	return h
}

// wavMask is the WAVE channel mask for a Blu-ray layout.
func wavMask(channels int, lfe bool) uint32 {
	const (
		fl, fr, fc, lf, bl, br, sl, sr = 1, 2, 4, 8, 0x10, 0x20, 0x200, 0x400
	)
	switch {
	case channels == 1:
		return fc
	case channels == 2:
		return fl | fr
	case channels == 3:
		return fl | fr | fc
	case channels == 4 && !lfe:
		return fl | fr | bl | br
	case channels == 5:
		return fl | fr | fc | bl | br
	case channels == 6 && lfe:
		return fl | fr | fc | lf | bl | br
	case channels == 7:
		return fl | fr | fc | bl | br | sl | sr
	case channels == 8 && lfe:
		return fl | fr | fc | lf | bl | br | sl | sr
	}
	return 0
}

func (w *wavWriter) close() error {
	if !w.started {
		return nil
	}
	if err := w.header(uint32(w.n)); err != nil { //nolint:gosec // WAV sizes are 32-bit by definition
		return err
	}
	_, err := w.f.Seek(0, io.SeekEnd)
	return err
}

// createESWriter creates the file a track is demuxed to, named and laid out
// as tsMuxeR would.
func createESWriter(tmp, srcName string, t Track) (*esWriter, error) {
	w := &esWriter{track: t}
	ext := "bin"
	switch {
	case t.StreamID == "S_HDMV/PGS":
		ext = "sup"
	case t.Type == "TRUE-HD":
		ext = "thd"
	case strings.HasPrefix(t.Type, "E-AC3"):
		ext = "eac3"
	case t.StreamID == "A_AC3":
		ext = "ac3"
	case t.StreamID == "A_DTS":
		ext = "dts"
	case t.StreamID == "A_LPCM":
		ext = "wav"
	}
	name := fmt.Sprintf("%s.track_%d", fileSafe(srcName), t.ID)
	if l := fileSafe(t.Lang); l != "" {
		name += "_" + l
	}
	w.path = filepath.Join(tmp, name+"."+ext)
	f, err := os.Create(w.path) //nolint:gosec // a file-safe name in the work directory
	if err != nil {
		return nil, fmt.Errorf("creating %s: %w", w.path, err)
	}
	w.f = f
	w.w = f
	if ext == "wav" {
		w.lpcm = newWAVWriter(f)
	}
	return w, nil
}

// writeSup writes a PGS payload to a .sup: each segment behind "PG" and its
// PTS and DTS (90 kHz, on the output's timeline).
func writeSup(w *esWriter, pts, dts int64, seg []byte) error {
	var err error
	for len(seg) >= 3 && err == nil {
		n := 3 + (int(seg[1])<<8 | int(seg[2]))
		n = min(n, len(seg))
		var hdr [10]byte
		hdr[0], hdr[1] = 'P', 'G'
		binary.BigEndian.PutUint32(hdr[2:], uint32(pts)) //nolint:gosec // 33-bit timestamps, as the format stores them
		binary.BigEndian.PutUint32(hdr[6:], uint32(dts)) //nolint:gosec // as above
		if _, err = w.w.Write(hdr[:]); err == nil {
			_, err = w.w.Write(seg[:n])
		}
		w.n += int64(n) + 10
		seg = seg[n:]
	}
	return err
}
