package convert

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/brunoga/bdtools/internal/mkv"
)

// Text subtitles: SRT, ASS and SSA, WebVTT (Matroska's) and 3GPP timed
// text (MP4's tx3g), and WebVTT in MP4. A Matroska output carries them as
// they are (MP4's as SRT); an MP4 output carries every one as tx3g, its
// text plain: styling, positions and karaoke go.

// textStreamIDs are the stream IDs text subtitles are listed under, by
// the source's codec ID, with their human names.
var textStreamIDs = map[string]struct{ id, name string }{
	"S_TEXT/UTF8":        {"S_TEXT/UTF8", "SRT"},
	"S_TEXT/ASS":         {"S_TEXT/ASS", "ASS"},
	"S_TEXT/SSA":         {"S_TEXT/SSA", "SSA"},
	"S_TEXT/WEBVTT":      {"S_TEXT/WEBVTT", "WebVTT"},
	"D_WEBVTT/SUBTITLES": {"S_TEXT/WEBVTT", "WebVTT"},
	"S_TEXT/TX3G":        {"S_TEXT/TX3G", "tx3g"},
	"S_TEXT/WVTT":        {"S_TEXT/WVTT", "WebVTT"},
}

// textDefaultDuration is how long a cue shows whose source does not say.
const textDefaultDuration = 4 * time.Second

// isText reports whether a stream ID is a text subtitle's.
func isText(streamID string) bool {
	_, ok := textStreamIDs[streamID]
	return ok && streamID != "D_WEBVTT/SUBTITLES"
}

// writeCue records a cue in a text track's work file: its time from the
// output's start, how long it shows, and its data as the source has it.
func writeCue(w io.Writer, at, dur time.Duration, data []byte) error {
	var hdr [20]byte
	binary.BigEndian.PutUint64(hdr[:], uint64(at))   //nolint:gosec // a time
	binary.BigEndian.PutUint64(hdr[8:], uint64(dur)) //nolint:gosec // a duration
	binary.BigEndian.PutUint32(hdr[16:], uint32(len(data)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

// textSource reads a text track's work file back as an output track.
type textSource struct {
	r     *bufio.Reader
	in    string // the source's stream ID
	track mkv.Track
	plain bool // the text alone, for tx3g or SRT out
}

// newTextSource opens a text track for an output: as the source has it
// (Matroska out), or plain (MP4 out, or a codec Matroska does not take).
func newTextSource(r io.Reader, t Track, private []byte, mp4Out bool) *textSource {
	s := &textSource{r: bufio.NewReader(r), in: t.StreamID, track: mkv.Track{Type: mkv.TypeSubtitle,
		CodecID: t.StreamID, CodecPrivate: private, Language: strings.TrimSpace(t.Lang), Name: t.Name, Forced: t.Forced}}
	switch {
	case mp4Out:
		s.plain, s.track.CodecID, s.track.CodecPrivate = true, "S_TEXT/TX3G", nil
	case t.StreamID == "S_TEXT/TX3G" || t.StreamID == "S_TEXT/WVTT":
		s.plain, s.track.CodecID, s.track.CodecPrivate = true, "S_TEXT/UTF8", nil
	}
	return s
}

func (s *textSource) Track() mkv.Track { return s.track }

func (s *textSource) Next() (mkv.Frame, error) {
	for {
		var hdr [20]byte
		if _, err := io.ReadFull(s.r, hdr[:]); err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				err = io.EOF
			}
			return mkv.Frame{}, err
		}
		at := time.Duration(binary.BigEndian.Uint64(hdr[:]))   //nolint:gosec // written above
		dur := time.Duration(binary.BigEndian.Uint64(hdr[8:])) //nolint:gosec // written above
		data := make([]byte, binary.BigEndian.Uint32(hdr[16:]))
		if _, err := io.ReadFull(s.r, data); err != nil {
			return mkv.Frame{}, io.EOF
		}
		if s.plain {
			text := plainText(s.in, data)
			if text == "" {
				continue // a gap, or nothing to show
			}
			data = []byte(text)
		}
		if dur <= 0 || at < 0 {
			continue
		}
		return mkv.Frame{PTS: at, Order: at, Keyframe: true, Data: data, Duration: dur}, nil
	}
}

var (
	assOverride = regexp.MustCompile(`\{[^}]*\}`)
	markupTag   = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)
)

// plainText is a cue's text without styling: lines apart by newlines.
func plainText(streamID string, data []byte) string {
	var s string
	switch streamID {
	case "S_TEXT/ASS", "S_TEXT/SSA":
		// Matroska's ASS block: ReadOrder, Layer, Style, Name, MarginL,
		// MarginR, MarginV, Effect, Text (SSA has Marked for Layer).
		parts := strings.SplitN(string(data), ",", 9)
		if len(parts) < 9 {
			return ""
		}
		s = assOverride.ReplaceAllString(parts[8], "")
		s = strings.NewReplacer(`\N`, "\n", `\n`, "\n", `\h`, " ").Replace(s)
	case "S_TEXT/TX3G":
		// A length, the text, then style boxes.
		if len(data) < 2 {
			return ""
		}
		n := int(binary.BigEndian.Uint16(data))
		if n > len(data)-2 {
			return ""
		}
		s = string(data[2 : 2+n])
	case "S_TEXT/WVTT":
		s = wvttText(data)
	default:
		s = markupTag.ReplaceAllString(string(data), "")
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimSpace(s)
}

// wvttText is the cue text of an MP4 WebVTT sample: its vttc boxes'
// payl boxes, a line each.
func wvttText(b []byte) string {
	var lines []string
	var walk func(b []byte)
	walk = func(b []byte) {
		for len(b) >= 8 {
			size := int(binary.BigEndian.Uint32(b))
			if size < 8 || size > len(b) {
				return
			}
			typ, body := string(b[4:8]), b[8:size]
			switch typ {
			case "vttc":
				walk(body)
			case "payl":
				lines = append(lines, markupTag.ReplaceAllString(string(body), ""))
			}
			b = b[size:]
		}
	}
	walk(b)
	return strings.Join(lines, "\n")
}

// describeText is a text track's listing line.
func describeText(streamID string, n int) string {
	return fmt.Sprintf("%s text subtitles #%d", textStreamIDs[streamID].name, n)
}

// A subtitle file added to the output (--subs-file): SRT, ASS or SSA, or
// WebVTT, read into a text track's cues.

// SubsFile is a subtitle file to add, with its language ("" to go by its
// name: film.en.srt, film.eng.forced.srt).
type SubsFile struct {
	Path, Lang string
}

// ParseSubsFile reads --subs-file: PATH, or LANG:PATH.
func ParseSubsFile(s string) SubsFile {
	if lang, path, ok := strings.Cut(s, ":"); ok && len(lang) >= 2 && len(lang) <= 3 && isLetters(lang) {
		return SubsFile{Path: path, Lang: lang}
	}
	return SubsFile{Path: s}
}

func isLetters(s string) bool {
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

// iso6392 is a language's ISO 639-2 code, from what an operator or a file
// name says ("en", "eng", "pt-br").
func iso6392(lang string) string {
	lang = strings.ToLower(lang)
	if codes, ok := langAliases[lang]; ok && len(codes) > 0 {
		return codes[0]
	}
	if len(lang) == 3 {
		return lang
	}
	return ""
}

// subsFileTrack reads a subtitle file into a cues file in dir, giving the
// track it is.
func subsFileTrack(f SubsFile, id int, dir string) (extra, error) {
	b, err := os.ReadFile(f.Path)
	if err != nil {
		return extra{}, err
	}
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")) // a byte order mark
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	base := strings.ToLower(filepath.Base(f.Path))
	t := Track{ID: id, Lang: iso6392(f.Lang)}
	parts := strings.Split(strings.TrimSuffix(base, filepath.Ext(base)), ".")
	for _, p := range parts[1:] {
		switch {
		case p == "forced":
			t.Forced = true
		case t.Lang == "" && len(p) >= 2 && len(p) <= 5:
			t.Lang = iso6392(p)
		}
	}
	var cues []textCue
	switch filepath.Ext(base) {
	case ".srt":
		t.StreamID, cues, err = "S_TEXT/UTF8", parseSRT(text), nil
	case ".vtt":
		t.StreamID, cues, err = "S_TEXT/WEBVTT", parseVTT(text), nil
	case ".ass", ".ssa":
		t.StreamID = "S_TEXT/ASS"
		if filepath.Ext(base) == ".ssa" {
			t.StreamID = "S_TEXT/SSA"
		}
		t.private, cues, err = parseASS(text)
	default:
		return extra{}, fmt.Errorf("%s: want a .srt, .ass, .ssa or .vtt file", f.Path)
	}
	if err != nil {
		return extra{}, fmt.Errorf("%s: %w", f.Path, err)
	}
	if len(cues) == 0 {
		return extra{}, fmt.Errorf("%s: no subtitles in it", f.Path)
	}
	t.Type = textStreamIDs[t.StreamID].name
	t.Info = fmt.Sprintf("%s text subtitles from %s", t.Type, filepath.Base(f.Path))
	path := filepath.Join(dir, fmt.Sprintf("subs_file_%d.cues", id))
	out, err := os.Create(path) //nolint:gosec // the work directory
	if err != nil {
		return extra{}, err
	}
	w := bufio.NewWriter(out)
	for _, c := range cues {
		if err := writeCue(w, c.at, c.dur, []byte(c.data)); err != nil {
			_ = out.Close()
			return extra{}, err
		}
	}
	if err := w.Flush(); err != nil {
		_ = out.Close()
		return extra{}, err
	}
	return extra{path: path, track: t}, out.Close()
}

// textCue is a cue read from a subtitle file, its data as a Matroska
// block of the track's codec holds it.
type textCue struct {
	at, dur time.Duration
	data    string
}

var cueTime = regexp.MustCompile(`^\s*(?:(\d+):)?(\d{1,2}):(\d{2})[,.](\d{1,3})\s*-->\s*(?:(\d+):)?(\d{1,2}):(\d{2})[,.](\d{1,3})`)

// cueTimes reads a "start --> end" line (SRT's commas or WebVTT's
// points, the hours optional).
func cueTimes(line string) (at, end time.Duration, ok bool) {
	m := cueTime.FindStringSubmatch(line)
	if m == nil {
		return 0, 0, false
	}
	t := func(h, mi, s, ms string) time.Duration {
		n := func(v string) time.Duration { d, _ := strconv.Atoi(v); return time.Duration(d) }
		frac := (ms + "00")[:3]
		return n(h)*time.Hour + n(mi)*time.Minute + n(s)*time.Second + n(frac)*time.Millisecond
	}
	return t(m[1], m[2], m[3], m[4]), t(m[5], m[6], m[7], m[8]), true
}

// parseSRT reads SRT's numbered cues.
func parseSRT(text string) []textCue {
	var cues []textCue
	for _, block := range strings.Split(text, "\n\n") {
		lines := strings.Split(strings.Trim(block, "\n"), "\n")
		for i, l := range lines {
			if at, end, ok := cueTimes(l); ok {
				if body := strings.Join(lines[i+1:], "\n"); strings.TrimSpace(body) != "" && end > at {
					cues = append(cues, textCue{at, end - at, body})
				}
				break
			}
		}
	}
	return cues
}

// parseVTT reads WebVTT's cues, leaving out its notes, styles and regions.
func parseVTT(text string) []textCue {
	var cues []textCue
	for _, block := range strings.Split(text, "\n\n") {
		lines := strings.Split(strings.Trim(block, "\n"), "\n")
		for i, l := range lines {
			if i > 1 {
				break // the timing is the first line, or the second after an id
			}
			if at, end, ok := cueTimes(l); ok {
				if body := strings.Join(lines[i+1:], "\n"); strings.TrimSpace(body) != "" && end > at {
					cues = append(cues, textCue{at, end - at, body})
				}
				break
			}
		}
	}
	return cues
}

// parseASS reads an ASS (or SSA) script: the header Matroska keeps as
// the track's private data (everything but the dialogue), and each
// Dialogue line as Matroska's block, ReadOrder first.
func parseASS(text string) ([]byte, []textCue, error) {
	var header strings.Builder
	var format []string
	var cues []textCue
	inEvents := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inEvents = strings.EqualFold(trimmed, "[Events]")
		}
		if inEvents && strings.HasPrefix(trimmed, "Format:") {
			for _, f := range strings.Split(strings.TrimPrefix(trimmed, "Format:"), ",") {
				format = append(format, strings.ToLower(strings.TrimSpace(f)))
			}
		}
		if !inEvents || !strings.HasPrefix(trimmed, "Dialogue:") {
			header.WriteString(line + "\n")
			continue
		}
		fields := strings.SplitN(strings.TrimSpace(strings.TrimPrefix(trimmed, "Dialogue:")), ",", len(format))
		if len(format) == 0 || len(fields) < len(format) {
			continue
		}
		get := func(name string) string {
			for i, f := range format {
				if f == name {
					return strings.TrimSpace(fields[i])
				}
			}
			return ""
		}
		at, ok1 := assTime(get("start"))
		end, ok2 := assTime(get("end"))
		if !ok1 || !ok2 || end <= at {
			continue
		}
		layer := get("layer")
		if layer == "" {
			layer = get("marked")
		}
		data := strings.Join([]string{strconv.Itoa(len(cues)), layer, get("style"), get("name"),
			get("marginl"), get("marginr"), get("marginv"), get("effect"), fields[len(format)-1]}, ",")
		cues = append(cues, textCue{at, end - at, data})
	}
	if format == nil {
		return nil, nil, errors.New("no [Events] format line")
	}
	return []byte(header.String()), cues, nil
}

// assTime reads ASS's H:MM:SS.cc.
func assTime(s string) (time.Duration, bool) {
	var h, m, sec, cs int
	if _, err := fmt.Sscanf(s, "%d:%d:%d.%d", &h, &m, &sec, &cs); err != nil {
		return 0, false
	}
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(sec)*time.Second +
		time.Duration(cs)*10*time.Millisecond, true
}
