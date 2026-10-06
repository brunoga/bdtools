// Package probe identifies a video source from the little of it that a
// pre-download sample contains: a disc image's BDMV metadata, or a Matroska
// file's header. It reports what the container states and never guesses.
//
// A Blu-ray image keeps its UDF directory at the front and its BDMV
// metadata (playlists, clip info) at the end, so the first and last pieces
// of a torrent are enough; a Matroska file states its tracks in its first
// kilobytes. Reads outside what the caller has are ordinary errors: a
// partial source never panics, and never yields a plausible wrong answer.
//
// Nothing here decodes video: the package depends on the container readers
// alone, not on the MVC decoder or its assembly.
package probe

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brunoga/mvc/internal/bdmv"
	"github.com/brunoga/mvc/internal/esinfo"
	"github.com/brunoga/mvc/internal/mkv"
	"github.com/brunoga/mvc/m2ts"
)

// Kind is what sort of source was probed.
type Kind int

const (
	KindUnknown  Kind = iota
	KindDisc          // UDF/BDMV image or directory
	KindMatroska      // Matroska file
)

// Layout is how a source carries its views.
type Layout int

const (
	Layout2D         Layout = iota // the source states it is not stereoscopic
	LayoutMVC                      // dependent view: a Blu-ray 3D disc, or MVC in MKV
	LayoutSideBySide               // both eyes side by side in each frame
	LayoutTopBottom                // both eyes one above the other
	LayoutOther                    // stereoscopic, but not a layout named here
)

// Result is what a probe found. Fields the source does not state are zero.
// New fields may be added; existing ones keep their meaning.
type Result struct {
	Kind     Kind
	Source   string        // label for messages
	Duration time.Duration // zero when the container does not say

	// Is3D is explicit: false means the source states it is 2D, which is as
	// useful an answer as true. Layout is Layout2D exactly when Is3D is
	// false.
	Is3D   bool
	Layout Layout
	// BaseViewRight is the disc's mvc_base_view_R_flag. Discs only.
	BaseViewRight bool

	Video    []VideoTrack
	Audio    []AudioTrack
	Subtitle []SubtitleTrack
}

// VideoTrack is a video stream.
type VideoTrack struct {
	Codec         string // "H.264", "MVC", "HEVC", … or the raw CodecID
	Width, Height int    // zero when unstated
	BitDepth      int    // zero when unstated
}

// AudioTrack is an audio stream.
type AudioTrack struct {
	Codec       string // "TRUE-HD", "DTS", "E-AC3 (DD+)", … or the raw CodecID
	Language    string // ISO 639-2, empty when unstated
	Channels    int    // zero when unstated
	SampleRate  float64
	BitrateKbps int // zero when the stream does not state it
}

// SubtitleTrack is a subtitle stream.
type SubtitleTrack struct {
	Codec    string
	Language string
}

// Image probes a UDF/BDMV disc image. size is the image's full length. The
// caller owns r and Image does not close it.
//
// When the probe fails because a read of r failed — the sample lacks a
// piece the directory or the metadata is in — the error is a
// *MissingDataError naming the bytes that were wanted, so the caller can
// fetch them and try again.
func Image(r io.ReaderAt, size int64, label string) (*Result, error) {
	rr := &watchReader{r: r}
	res, err := probeImage(rr, size, label)
	if err != nil && rr.failed != nil {
		return nil, &MissingDataError{Offset: rr.off, Length: rr.n, Err: rr.failed, Probe: err}
	}
	return res, err
}

func probeImage(r io.ReaderAt, size int64, label string) (*Result, error) {
	d, err := bdmv.OpenImage(r, size, label)
	if err != nil {
		return nil, fmt.Errorf("probe: %w", err)
	}
	defer func() { _ = d.Close() }()
	return probeDisc(d, label)
}

// MissingDataError says a probe failed for want of bytes the caller's
// reader could not supply.
type MissingDataError struct {
	// Offset and Length are the first read that failed.
	Offset, Length int64
	// Err is what the reader returned; Probe is the error it led to.
	Err, Probe error
}

func (e *MissingDataError) Error() string {
	return fmt.Sprintf("%v (bytes %d-%d unavailable: %v)", e.Probe, e.Offset, e.Offset+e.Length, e.Err)
}

// Unwrap gives both the reader's error and the probe's.
func (e *MissingDataError) Unwrap() []error { return []error{e.Err, e.Probe} }

// watchReader remembers the first read of the caller's reader that failed.
type watchReader struct {
	r      io.ReaderAt
	off, n int64
	failed error
}

func (w *watchReader) ReadAt(p []byte, off int64) (int, error) {
	n, err := w.r.ReadAt(p, off)
	if err != nil && !errors.Is(err, io.EOF) && w.failed == nil {
		w.off, w.n, w.failed = off, int64(len(p)), err
	}
	return n, err
}

// Matroska probes a Matroska file, reading only as far as its header (and,
// within a small budget, its first frames), so a prefix of the file is
// enough.
func Matroska(r io.Reader, label string) (res *Result, err error) {
	defer func() {
		if p := recover(); p != nil {
			res, err = nil, fmt.Errorf("probe: %s: malformed Matroska (%v)", label, p)
		}
	}()
	return probeMatroska(r, label)
}

// Path probes whatever is at path, choosing by extension and by whether it
// is a directory: a BDMV folder or .iso image, or a .mkv/.mk3d file.
func Path(path string) (*Result, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("probe: %w", err)
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch {
	case st.IsDir() || ext == ".iso":
		d, err := bdmv.Open(path)
		if err != nil {
			return nil, fmt.Errorf("probe: %w", err)
		}
		defer func() { _ = d.Close() }()
		return probeDisc(d, path)
	case ext == ".mkv" || ext == ".mk3d" || ext == ".webm":
		f, err := os.Open(path) //nolint:gosec // the caller's path is the point
		if err != nil {
			return nil, fmt.Errorf("probe: %w", err)
		}
		defer func() { _ = f.Close() }()
		return Matroska(f, path)
	}
	return nil, fmt.Errorf("probe: %s is neither a disc image, a BDMV folder nor a Matroska file", path)
}

// --- discs ---------------------------------------------------------------------

// probeDisc reads the disc's playlists and the feature's clip info. Every
// metadata read must succeed: a missing playlist or clip info is an error,
// so a partial image cannot pass a lesser title off as the feature. The
// stream sample that adds per-track detail is optional.
func probeDisc(d bdmv.Disc, label string) (res *Result, err error) {
	defer func() {
		if p := recover(); p != nil {
			res, err = nil, fmt.Errorf("probe: %s: malformed disc (%v)", label, p)
		}
	}()
	names, err := bdmv.Playlists(d)
	if err != nil {
		return nil, fmt.Errorf("probe: %s: %w", label, err)
	}
	var best *bdmv.Playlist
	var bestName string
	for _, n := range names {
		pl, err := bdmv.ReadPlaylist(d, n)
		if err != nil {
			return nil, fmt.Errorf("probe: %s: %s: %w", label, n, err)
		}
		if len(pl.Items) == 0 {
			continue
		}
		if best == nil || longer(pl, best) {
			best, bestName = pl, n
		}
	}
	if best == nil {
		return nil, fmt.Errorf("probe: %s: no playlist plays anything", label)
	}
	res = &Result{Kind: KindDisc, Source: label, Duration: best.Duration(), BaseViewRight: best.BaseViewIsRight}
	if best.ThreeD() {
		res.Is3D, res.Layout = true, LayoutMVC
	} else {
		res.BaseViewRight = false
	}

	item := best.Items[0]
	b, err := d.ReadFile(bdmv.ClipInfo(d, item.Clip))
	if err != nil {
		return nil, fmt.Errorf("probe: %s: clip info of %s (%s): %w", label, item.Clip, bestName, err)
	}
	streams, err := bdmv.ParseCLPI(b)
	if err != nil {
		return nil, fmt.Errorf("probe: %s: clip info of %s: %w", label, item.Clip, err)
	}
	audioPIDs := map[uint16]int{}
	for _, s := range streams {
		switch name, kind := codingName(s.Coding); kind {
		case kindVideo:
			w, h := videoSize(s.Format)
			res.Video = append(res.Video, VideoTrack{Codec: name, Width: w, Height: h})
		case kindAudio:
			audioPIDs[s.PID] = len(res.Audio)
			res.Audio = append(res.Audio, AudioTrack{Codec: name, Language: s.Lang,
				Channels: presentationChannels(s.Format), SampleRate: sampleRate(s.Rate)})
		case kindSubtitle:
			res.Subtitle = append(res.Subtitle, SubtitleTrack{Codec: name, Language: s.Lang})
		}
	}
	if item.DependentClip != "" {
		// The playlist names the dependent view's MVC stream; a disc
		// describes it in its clip info's 3D extension, which is not read
		// here, so its size is left unstated.
		res.Video = append(res.Video, VideoTrack{Codec: "MVC"})
	}
	sampleAudio(d, item, streams, audioPIDs, res)
	return res, nil
}

// longer ranks playlists for being the feature: the most content, each
// stretch of a clip counted once (a menu loop or a decoy playlist repeats
// clips to run longer than the film), then the longest, then the fewest
// items.
func longer(a, b *bdmv.Playlist) bool {
	if ua, ub := a.UniqueDuration(), b.UniqueDuration(); ua != ub {
		return ua > ub
	}
	if da, db := a.Duration(), b.Duration(); da != db {
		return da > db
	}
	return len(a.Items) < len(b.Items)
}

// streamSample bounds how much of the feature's stream is read for audio
// headers: the start of the stream, which a torrent's first piece holds.
const streamSample = 12 << 20

// sampleAudio fills channel counts and bitrates from the audio frames at
// the start of the feature's stream, when that much of it can be read. A
// read that fails — a sample without those bytes — leaves the clip info's
// values as they are.
func sampleAudio(d bdmv.Disc, item bdmv.PlayItem, streams []bdmv.ClipStream, pids map[uint16]int, res *Result) {
	if len(pids) == 0 {
		return
	}
	path, _ := bdmv.StreamPath(d, item)
	f, _, err := d.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	coding := map[uint16]byte{}
	var want []uint16
	for _, s := range streams {
		if _, ok := pids[s.PID]; ok {
			coding[s.PID] = s.Coding
			want = append(want, s.PID)
		}
	}
	r := m2ts.NewReader(io.LimitReader(f, streamSample))
	r.Select(want...)
	samples := map[uint16][]byte{}
	for {
		p, err := r.Next()
		if err != nil {
			break
		}
		if len(samples[p.PID]) < 64<<10 {
			samples[p.PID] = append(samples[p.PID], p.Payload...)
		}
	}
	for pid, b := range samples {
		a, ok := parseAudio(coding[pid], b)
		if !ok {
			continue
		}
		t := &res.Audio[pids[pid]]
		if a.Channels > 0 {
			t.Channels = a.Channels
		}
		if a.SampleRate > 0 {
			t.SampleRate = float64(a.SampleRate)
		}
		t.BitrateKbps = a.Bitrate
		if a.Codec != "" && coding[pid] != bdmv.CodingLPCM {
			t.Codec = a.Codec
		}
	}
}

// parseAudio reads an audio stream's first frames by its coding type.
func parseAudio(coding byte, b []byte) (esinfo.Audio, bool) {
	switch coding {
	case bdmv.CodingAC3:
		return esinfo.ParseAC3(b, false)
	case bdmv.CodingEAC3, bdmv.CodingEAC3Sec:
		return esinfo.ParseAC3(b, true)
	case bdmv.CodingTrueHD:
		return esinfo.ParseTrueHD(b)
	case bdmv.CodingDTS:
		return esinfo.ParseDTS(b, "")
	case bdmv.CodingDTSHDHR:
		return esinfo.ParseDTS(b, "DTS-HD High Resolution")
	case bdmv.CodingDTSHDMA:
		return esinfo.ParseDTS(b, "DTS-HD Master Audio")
	case bdmv.CodingLPCM:
		return esinfo.ParseLPCM(b)
	}
	return esinfo.Audio{}, false
}

const (
	kindOther = iota
	kindVideo
	kindAudio
	kindSubtitle
)

// codingName names a Blu-ray stream coding type, the way the rest of
// mvctools does.
func codingName(c byte) (string, int) {
	switch c {
	case 0x01:
		return "MPEG-1", kindVideo
	case 0x02:
		return "MPEG-2", kindVideo
	case bdmv.CodingAVC:
		return "H.264", kindVideo
	case bdmv.CodingMVC:
		return "MVC", kindVideo
	case bdmv.CodingHEVC:
		return "HEVC", kindVideo
	case 0xea:
		return "VC-1", kindVideo
	case 0x03, 0x04:
		return "MPEG audio", kindAudio
	case bdmv.CodingLPCM:
		return "LPCM", kindAudio
	case bdmv.CodingAC3:
		return "AC3", kindAudio
	case bdmv.CodingDTS:
		return "DTS", kindAudio
	case bdmv.CodingTrueHD:
		return "TRUE-HD", kindAudio
	case bdmv.CodingEAC3, bdmv.CodingEAC3Sec:
		return "E-AC3 (DD+)", kindAudio
	case bdmv.CodingDTSHDHR:
		return "DTS-HD High Resolution", kindAudio
	case bdmv.CodingDTSHDMA:
		return "DTS-HD Master Audio", kindAudio
	case bdmv.CodingDTSSec:
		return "DTS-HD", kindAudio
	case bdmv.CodingPGS:
		return "PGS", kindSubtitle
	case bdmv.CodingText:
		return "Text", kindSubtitle
	}
	return fmt.Sprintf("0x%02x", c), kindOther // interactive graphics (menus) and the unknown
}

// videoSize is the frame size a clip info's video_format states.
func videoSize(format byte) (int, int) {
	switch format {
	case 1, 3:
		return 720, 480
	case 2, 7:
		return 720, 576
	case 4, 6:
		return 1920, 1080
	case 5:
		return 1280, 720
	case 8:
		return 3840, 2160
	}
	return 0, 0
}

// presentationChannels is the channel count an audio_presentation_type
// states: mono and stereo say; "multi-channel" does not say how many.
func presentationChannels(t byte) int {
	switch t {
	case 1:
		return 1
	case 3:
		return 2
	}
	return 0
}

// sampleRate is the rate a sampling_frequency code states.
func sampleRate(code byte) float64 {
	switch code {
	case 1:
		return 48000
	case 4, 14:
		return 96000
	case 5, 12:
		return 192000
	}
	return 0
}

// --- Matroska -----------------------------------------------------------------------

// mkvSample bounds how much past the header is read for frames to describe
// tracks with (a prefix ends sooner, which is fine).
const mkvSample = 4 << 20

func probeMatroska(r io.Reader, label string) (*Result, error) {
	cr := &countReader{r: r}
	m, err := mkv.NewReader(cr)
	if err != nil {
		return nil, fmt.Errorf("probe: %s: %w", label, err)
	}
	res := &Result{Kind: KindMatroska, Source: label, Duration: m.Duration}
	stereo := 0
	mvcTrack := false
	audio := map[uint64]int{}
	video := map[uint64]bool{}
	for _, t := range m.Tracks {
		switch t.Type {
		case mkv.TypeVideo:
			res.Video = append(res.Video, VideoTrack{Codec: videoCodec(t.CodecID), Width: t.Width, Height: t.Height})
			if stereo == 0 {
				stereo = t.StereoMode
			}
			if t.CodecID == "V_MPEG4/ISO/MVC" {
				mvcTrack = true
			}
			video[t.Number] = t.CodecID == "V_MPEG4/ISO/AVC"
		case mkv.TypeAudio:
			audio[t.Number] = len(res.Audio)
			res.Audio = append(res.Audio, AudioTrack{Codec: audioCodec(t.CodecID), Language: t.Language,
				Channels: t.Channels, SampleRate: t.SampleRate})
		case mkv.TypeSubtitle:
			res.Subtitle = append(res.Subtitle, SubtitleTrack{Codec: subtitleCodec(t.CodecID), Language: t.Language})
		}
	}
	res.Layout = stereoLayout(stereo)
	if mvcTrack {
		res.Layout = LayoutMVC
	}
	sampleMatroska(m, cr, video, audio, res)
	res.Is3D = res.Layout != Layout2D
	return res, nil
}

// stereoLayout maps a Matroska StereoMode: 0 mono; 1 and 11 side by side
// (left and right eye first); 2, 3 and 4 top-bottom; 12 and 13 both eyes in
// one block, which is how an MVC stream travels in Matroska; anything else
// stereoscopic is "other".
func stereoLayout(mode int) Layout {
	switch mode {
	case 0:
		return Layout2D
	case 1, 11:
		return LayoutSideBySide
	case 2, 3, 4:
		return LayoutTopBottom
	case 12, 13:
		return LayoutMVC
	}
	return LayoutOther
}

// sampleMatroska reads the first frames, within a budget, for what the
// header does not say: whether an AVC track carries an MVC dependent view
// (its blocks hold MVC NAL units, which is the stream stating it), and the
// audio's codec flavour and bitrate.
func sampleMatroska(m *mkv.Reader, cr *countReader, video map[uint64]bool, audio map[uint64]int, res *Result) {
	samples := map[uint64][]byte{}
	start := cr.n
	for cr.n-start < mkvSample {
		p, err := m.Next()
		if err != nil {
			break
		}
		if isAVC, ok := video[p.Track]; ok {
			if isAVC && hasMVC(p.Data) {
				res.Layout = LayoutMVC
			}
			continue
		}
		if _, ok := audio[p.Track]; ok && len(samples[p.Track]) < 64<<10 {
			samples[p.Track] = append(samples[p.Track], p.Data...)
		}
	}
	for _, t := range m.Tracks {
		i, ok := audio[t.Number]
		if !ok || len(samples[t.Number]) == 0 {
			continue
		}
		var a esinfo.Audio
		switch t.CodecID {
		case "A_AC3":
			a, ok = esinfo.ParseAC3(samples[t.Number], false)
		case "A_EAC3":
			a, ok = esinfo.ParseAC3(samples[t.Number], true)
		case "A_TRUEHD":
			a, ok = esinfo.ParseTrueHD(samples[t.Number])
		case "A_DTS":
			a, ok = esinfo.ParseDTS(samples[t.Number], dtsKind(samples[t.Number]))
		default:
			ok = false
		}
		if !ok {
			continue
		}
		at := &res.Audio[i]
		if a.Codec != "" {
			at.Codec = a.Codec
		}
		if a.Channels > 0 {
			at.Channels = a.Channels
		}
		at.BitrateKbps = a.Bitrate
	}
}

// dtsKind says which DTS-HD a stream's frames carry, by their extension
// substreams: a lossless one makes it Master Audio.
func dtsKind(b []byte) string {
	if !bytes.Contains(b, []byte{0x64, 0x58, 0x20, 0x25}) {
		return ""
	}
	if bytes.Contains(b, []byte{0x41, 0xa2, 0x95, 0x47}) {
		return "DTS-HD Master Audio"
	}
	return "DTS-HD High Resolution"
}

// hasMVC reports whether a length-prefixed AVC block carries MVC NAL units
// (subset SPS, or slice extensions of the dependent view).
func hasMVC(b []byte) bool {
	for len(b) >= 5 {
		n := int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3])
		if n <= 0 || n > len(b)-4 {
			return false
		}
		if t := b[4] & 0x1f; t == 15 || t == 20 {
			return true
		}
		b = b[4+n:]
	}
	return false
}

func videoCodec(id string) string {
	switch id {
	case "V_MPEG4/ISO/AVC":
		return "H.264"
	case "V_MPEG4/ISO/MVC":
		return "MVC"
	case "V_MPEGH/ISO/HEVC":
		return "HEVC"
	case "V_AV1":
		return "AV1"
	case "V_MPEG2":
		return "MPEG-2"
	case "V_VP9":
		return "VP9"
	}
	return id
}

func audioCodec(id string) string {
	switch {
	case id == "A_TRUEHD":
		return "TRUE-HD"
	case id == "A_AC3":
		return "AC3"
	case id == "A_EAC3":
		return "E-AC3 (DD+)"
	case id == "A_DTS":
		return "DTS"
	case strings.HasPrefix(id, "A_PCM"):
		return "LPCM"
	case strings.HasPrefix(id, "A_AAC"):
		return "AAC"
	case id == "A_FLAC":
		return "FLAC"
	case id == "A_OPUS":
		return "Opus"
	}
	return id
}

func subtitleCodec(id string) string {
	switch id {
	case "S_HDMV/PGS":
		return "PGS"
	case "S_TEXT/UTF8":
		return "SRT"
	case "S_TEXT/ASS", "S_TEXT/SSA":
		return "ASS"
	case "S_VOBSUB":
		return "VobSub"
	}
	return id
}

// countReader counts the bytes read through it, to bound the frame sample.
type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n += int64(n)
	return n, err
}
