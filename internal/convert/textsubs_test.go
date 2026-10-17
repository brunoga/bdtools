package convert

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brunoga/bdtools/internal/mkv"
	"github.com/brunoga/bdtools/internal/mp4"
)

func TestPlainText(t *testing.T) {
	for _, c := range []struct {
		id   string
		data []byte
		want string
	}{
		{"S_TEXT/UTF8", []byte("<i>Hello</i>,\r\n<font color=\"red\">world</font>"), "Hello,\nworld"},
		{"S_TEXT/ASS", []byte(`3,0,Default,,0,0,0,,{\an8\i1}Up here,\Nwith a comma`), "Up here,\nwith a comma"},
		{"S_TEXT/SSA", []byte(`3,Marked=0,Default,,0,0,0,,Old {\b1}style`), "Old style"},
		{"S_TEXT/WEBVTT", []byte("<v Bob>Hi</v> there"), "Hi there"},
		{"S_TEXT/TX3G", append([]byte{0, 5}, append([]byte("Hello"), 0, 0, 0, 12, 's', 't', 'y', 'l', 0, 0, 0, 0)...), "Hello"},
		{"S_TEXT/TX3G", []byte{0, 0}, ""},
		{"S_TEXT/WVTT", []byte("\x00\x00\x00\x15vttc\x00\x00\x00\x0dpaylHello"), "Hello"},
	} {
		if got := plainText(c.id, c.data); got != c.want {
			t.Errorf("%s %q: %q, want %q", c.id, c.data, got, c.want)
		}
	}
}

// subtitled makes a 3-second Matroska or MP4 source with text subtitle
// tracks (ffmpeg's codec names).
func subtitled(t *testing.T, dir, name string, subs ...string) string {
	t.Helper()
	srt := filepath.Join(dir, "in.srt")
	if err := os.WriteFile(srt, []byte("1\n00:00:00,500 --> 00:00:01,250\n<i>First</i> line\n\n2\n00:00:02,000 --> 00:00:02,750\nSecond\nline\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, name)
	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=s=320x240:r=24:d=3"}
	for range subs {
		args = append(args, "-i", srt)
	}
	args = append(args, "-map", "0:v")
	for i, c := range subs {
		args = append(args, "-map", fmt.Sprintf("%d:s", i+1), fmt.Sprintf("-c:s:%d", i), c, fmt.Sprintf("-metadata:s:s:%d", i), "language=eng")
	}
	args = append(args, "-c:v", "libx264", src)
	if err := runCmd(t, "ffmpeg", args...); err != nil {
		t.Skipf("could not build the source: %v", err)
	}
	return src
}

// convertTo converts src with the encoder here, giving the output's path.
func convertTo(t *testing.T, src, ext string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "out"+ext)
	o := DefaultOptions()
	o.Input, o.Output, o.TempDir = src, out, t.TempDir()
	o.Encoder, o.Codec, o.CRF, o.Decoder = EncoderBuiltin, CodecH265, 30, DecoderCPU
	var lines []string
	if err := NewRunner(CurrentGOOS, o, func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }).Run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, strings.Join(lines, "\n"))
	}
	return out
}

type cue struct {
	at, dur time.Duration
	text    string
}

var wantCues = []cue{{500 * time.Millisecond, 750 * time.Millisecond, "First line"}, {2 * time.Second, 750 * time.Millisecond, "Second\nline"}}

// readCues reads a file's subtitle tracks: their codec IDs, private data
// and cues as plain text.
func readCues(t *testing.T, path string) (ids []string, private [][]byte, cues [][]cue) {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var rd frameReader
	var tracks []mkv.ReadTrack
	if strings.HasSuffix(path, ".mp4") {
		r, err := mp4.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		rd, tracks = r, r.Tracks
	} else {
		r, err := mkv.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		rd, tracks = r, r.Tracks
	}
	index := map[uint64]int{}
	for _, tr := range tracks {
		if _, text := textStreamIDs[tr.CodecID]; tr.Type == mkv.TypeSubtitle && text {
			index[tr.Number] = len(ids)
			ids = append(ids, tr.CodecID)
			private = append(private, tr.CodecPrivate)
			cues = append(cues, nil)
		}
	}
	for {
		p, err := rd.Next()
		if err != nil {
			break
		}
		if i, ok := index[p.Track]; ok {
			if text := plainText(textStreamIDs[ids[i]].id, p.Data); text != "" {
				cues[i] = append(cues[i], cue{p.Time, p.Duration, text})
			}
		}
	}
	return ids, private, cues
}

func sameCues(t *testing.T, what string, got []cue) {
	t.Helper()
	if len(got) != len(wantCues) {
		t.Fatalf("%s: %d cues %v, want %v", what, len(got), got, wantCues)
	}
	for i, c := range got {
		w := wantCues[i]
		if c.text != w.text || (c.at-w.at).Abs() > 2*time.Millisecond || (c.dur-w.dur).Abs() > 2*time.Millisecond {
			t.Errorf("%s: cue %d %v, want %v", what, i, c, w)
		}
	}
}

// SRT and ASS tracks from Matroska: kept as they are in Matroska, made
// tx3g in MP4 (which ffmpeg reads back as the same text).
func TestRunnerCarriesTextSubtitles(t *testing.T) {
	work := t.TempDir()
	src := subtitled(t, work, "src.mkv", "srt", "ass")
	ids, private, cues := readCues(t, convertTo(t, src, ".mkv"))
	if len(ids) != 2 || ids[0] != "S_TEXT/UTF8" || ids[1] != "S_TEXT/ASS" || !strings.Contains(string(private[1]), "[Events]") {
		t.Fatalf("tracks %v", ids)
	}
	sameCues(t, "SRT in Matroska", cues[0])
	sameCues(t, "ASS in Matroska", cues[1])

	mp4Out := convertTo(t, src, ".mp4")
	ids, _, cues = readCues(t, mp4Out)
	if len(ids) != 2 || ids[0] != "S_TEXT/TX3G" || ids[1] != "S_TEXT/TX3G" {
		t.Fatalf("MP4 tracks %v", ids)
	}
	sameCues(t, "SRT in MP4", cues[0])
	sameCues(t, "ASS in MP4", cues[1])
	if ff, err := LookPath("ffmpeg"); err == nil {
		out, err := exec.CommandContext(t.Context(), ff, "-v", "error", "-i", mp4Out, "-map", "0:s:0", "-f", "srt", "-").Output() //nolint:gosec // test
		if err != nil {
			t.Fatalf("ffmpeg reading the tx3g track: %v", err)
		}
		want := "1\n00:00:00,500 --> 00:00:01,250\nFirst line\n\n2\n00:00:02,000 --> 00:00:02,750\nSecond\nline\n\n"
		// ffmpeg states the sample entry's font as markup: the text is
		// what matters.
		if got := markupTag.ReplaceAllString(strings.ReplaceAll(string(out), "\r\n", "\n"), ""); got != want {
			t.Errorf("ffmpeg reads\n%s\nwant\n%s", got, want)
		}
	}
}

// MP4's timed text (ffmpeg's mov_text) into Matroska: SRT.
func TestRunnerCarriesMP4TimedText(t *testing.T) {
	src := subtitled(t, t.TempDir(), "src.mp4", "mov_text")
	ids, _, cues := readCues(t, convertTo(t, src, ".mkv"))
	if len(ids) != 1 || ids[0] != "S_TEXT/UTF8" {
		t.Fatalf("tracks %v", ids)
	}
	sameCues(t, "tx3g in Matroska", cues[0])
}

const (
	srtFile = "\ufeff1\r\n00:00:00,500 --> 00:00:01,250\r\n<i>First</i> line\r\n\r\n2\r\n00:00:02,000 --> 00:00:02,750\r\nSecond\r\nline\r\n"
	vttFile = "WEBVTT\n\nNOTE a note\n\nintro\n00:00.500 --> 00:01.250 align:start\nFirst line\n\n00:00:02.000 --> 00:00:02.750\nSecond\nline\n"
	assFile = "[Script Info]\nScriptType: v4.00+\n\n[V4+ Styles]\nFormat: Name, Fontname, Fontsize\nStyle: Default,Arial,20\n\n[Events]\n" +
		"Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
		"Dialogue: 0,0:00:00.50,0:00:01.25,Default,,0,0,0,,{\\i1}First{\\i0} line\n" +
		"Dialogue: 0,0:00:02.00,0:00:02.75,Default,,0,0,0,,Second\\Nline\n"
)

func TestParseSubtitleFiles(t *testing.T) {
	for _, c := range []struct {
		id    string
		cues  func() []textCue
		first string
	}{
		{"S_TEXT/UTF8", func() []textCue {
			return parseSRT(strings.ReplaceAll(strings.TrimPrefix(srtFile, "\ufeff"), "\r\n", "\n"))
		}, "<i>First</i> line"},
		{"S_TEXT/WEBVTT", func() []textCue { return parseVTT(vttFile) }, "First line"},
		{"S_TEXT/ASS", func() []textCue { _, c, _ := parseASS(assFile); return c }, `0,0,Default,,0,0,0,,{\i1}First{\i0} line`},
	} {
		cues := c.cues()
		if len(cues) != 2 {
			t.Fatalf("%s: %d cues", c.id, len(cues))
		}
		if cues[0].data != c.first {
			t.Errorf("%s: first cue %q, want %q", c.id, cues[0].data, c.first)
		}
		var got []cue
		for _, k := range cues {
			got = append(got, cue{k.at, k.dur, plainText(c.id, []byte(k.data))})
		}
		sameCues(t, c.id, got)
	}
	if f := ParseSubsFile("en:C:/films/x.srt"); f.Lang != "en" || f.Path != "C:/films/x.srt" {
		t.Errorf("ParseSubsFile: %+v", f)
	}
	if f := ParseSubsFile("C:/films/x.srt"); f.Lang != "" || f.Path != "C:/films/x.srt" {
		t.Errorf("ParseSubsFile: %+v", f)
	}
}

// --subs-file: SRT, WebVTT and ASS files added as tracks, their
// languages from their names or as given.
func TestRunnerAddsSubtitleFiles(t *testing.T) {
	work := t.TempDir()
	src := filepath.Join(work, "src.mkv")
	if err := runCmd(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=s=320x240:r=24:d=3",
		"-c:v", "libx264", src); err != nil {
		t.Skipf("could not build the source: %v", err)
	}
	files := map[string]string{"film.en.srt": srtFile, "film.fr.forced.vtt": vttFile, "film.ass": assFile}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(work, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, ext := range []string{".mkv", ".mp4"} {
		out := filepath.Join(t.TempDir(), "out"+ext)
		o := DefaultOptions()
		o.Input, o.Output, o.TempDir = src, out, t.TempDir()
		o.Encoder, o.Codec, o.CRF, o.Decoder = EncoderBuiltin, CodecH265, 30, DecoderCPU
		o.SubsFiles = []SubsFile{{Path: filepath.Join(work, "film.en.srt")}, {Path: filepath.Join(work, "film.fr.forced.vtt")},
			{Path: filepath.Join(work, "film.ass"), Lang: "de"}}
		var lines []string
		if err := NewRunner(CurrentGOOS, o, func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }).Run(context.Background()); err != nil {
			t.Fatalf("%v\n%s", err, strings.Join(lines, "\n"))
		}
		ids, _, cues := readCues(t, out)
		want := []string{"S_TEXT/UTF8", "S_TEXT/WEBVTT", "S_TEXT/ASS"}
		if ext == ".mp4" {
			want = []string{"S_TEXT/TX3G", "S_TEXT/TX3G", "S_TEXT/TX3G"}
		}
		if strings.Join(ids, " ") != strings.Join(want, " ") {
			t.Fatalf("%s: tracks %v, want %v", ext, ids, want)
		}
		for i := range cues {
			sameCues(t, fmt.Sprintf("%s track %d", ext, i), cues[i])
		}
		f, _ := os.Open(out) //nolint:gosec // test
		var langs []string
		var forced []bool
		if ext == ".mkv" {
			rd, err := mkv.NewReader(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, tr := range rd.Tracks[1:] {
				langs, forced = append(langs, tr.Language), append(forced, tr.Forced)
			}
			if strings.Join(langs, " ") != "eng fra deu" || !forced[1] || forced[0] {
				t.Errorf("languages %v, forced %v", langs, forced)
			}
		}
		_ = f.Close()
	}
}
