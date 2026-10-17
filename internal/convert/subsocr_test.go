package convert

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brunoga/bdtools/internal/mkv"
)

// A PGS track read as text: the fixture's subtitles (drawn as a disc
// draws them), at their times.
func TestOCRTrack(t *testing.T) {
	r := NewRunner(CurrentGOOS, DefaultOptions(), func(string, ...any) {})
	e := extra{path: filepath.Join("..", "..", "testdata", "subs", "ocr.sup"),
		track: Track{ID: 4, StreamID: "S_HDMV/PGS", Type: "PGS", Lang: "eng"}}
	out, ok, err := r.ocrTrack(e, 9, t.TempDir())
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if out.track.StreamID != "S_TEXT/UTF8" || out.track.Lang != "eng" {
		t.Errorf("track %+v", out.track)
	}
	f, err := os.Open(out.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	src := newTextSource(f, out.track, nil, false)
	want := []cue{
		{time.Second, 1500 * time.Millisecond, "Hello there."},
		{3 * time.Second, 2 * time.Second, "Where are you going?\nHome, I think."},
		{6 * time.Second, 1500 * time.Millisecond, "It's 10:45 already!"},
	}
	for i, w := range want {
		fr, err := src.Next()
		if err != nil {
			t.Fatalf("cue %d: %v", i, err)
		}
		if got := (cue{fr.PTS, fr.Duration, string(fr.Data)}); got != w {
			t.Errorf("cue %d: %q at %v for %v, want %q at %v for %v", i, got.text, got.at, got.dur, w.text, w.at, w.dur)
		}
	}
}

// --subs-ocr: a Matroska source's PGS track read, the text beside it in
// Matroska, alone in MP4 (tx3g).
func TestRunnerReadsPGS(t *testing.T) {
	work := t.TempDir()
	src := filepath.Join(work, "src.mkv")
	sup, _ := filepath.Abs(filepath.Join("..", "..", "testdata", "subs", "ocr.sup"))
	if err := runCmd(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=s=320x240:r=24:d=8",
		"-copyts", "-i", sup, "-map", "0:v", "-map", "1:s", "-c:v", "libx264", "-c:s", "copy", "-metadata:s:s:0", "language=eng", src); err != nil {
		t.Skipf("could not build the source: %v", err)
	}
	for _, ext := range []string{".mkv", ".mp4"} {
		out := filepath.Join(t.TempDir(), "out"+ext)
		o := DefaultOptions()
		o.Input, o.Output, o.TempDir = src, out, t.TempDir()
		o.Encoder, o.Codec, o.CRF, o.Decoder, o.SubsOCR = EncoderBuiltin, CodecH265, 30, DecoderCPU, true
		var lines []string
		if err := NewRunner(CurrentGOOS, o, func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }).Run(context.Background()); err != nil {
			t.Fatalf("%v\n%s", err, strings.Join(lines, "\n"))
		}
		ids, _, cues := readCues(t, out)
		want := "S_TEXT/UTF8"
		if ext == ".mp4" {
			want = "S_TEXT/TX3G"
		}
		if len(ids) != 1 || ids[0] != want || len(cues[0]) != 3 {
			t.Fatalf("%s: text tracks %v, cues %v\n%s", ext, ids, cues, strings.Join(lines, "\n"))
		}
		if cues[0][1].text != "Where are you going?\nHome, I think." || cues[0][1].at != 3*time.Second {
			t.Errorf("%s: cue %+v", ext, cues[0][1])
		}
		if ext == ".mkv" {
			f, _ := os.Open(out) //nolint:gosec // test
			rd, err := mkv.NewReader(f)
			if err != nil {
				t.Fatal(err)
			}
			var codecs []string
			for _, tr := range rd.Tracks[1:] {
				codecs = append(codecs, tr.CodecID)
			}
			_ = f.Close()
			if strings.Join(codecs, " ") != "S_HDMV/PGS S_TEXT/UTF8" {
				t.Errorf("tracks %v, want the PGS and its text", codecs)
			}
		}
	}
}
