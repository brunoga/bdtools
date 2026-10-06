package convert

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brunoga/mvc"
	"github.com/brunoga/mvc/internal/mkv"
)

// withSegments makes segments n pictures long for a test.
func withSegments(t *testing.T, n int) {
	t.Helper()
	orig := segmentFrames
	segmentFrames = n
	t.Cleanup(func() { segmentFrames = orig })
}

// fakeSink records the pictures it gets and writes their timestamps, and
// fails at the failAt-th picture it is given (never with 0).
type fakeSink struct {
	pts    []int64
	starts int
	failAt int
	path   string
	f      *os.File
}

func (s *fakeSink) start(path string, _ *mvc.StereoFrame, _, _ int) error {
	f, err := os.Create(path) //nolint:gosec // test
	s.path, s.f = path, f
	s.starts++
	return err
}

func (s *fakeSink) put(sf *mvc.StereoFrame) error {
	if s.failAt > 0 && len(s.pts)+1 == s.failAt {
		return errors.New("the encoder fell over")
	}
	s.pts = append(s.pts, sf.Base.PTS)
	_, err := fmt.Fprintf(s.f, "%d\n", sf.Base.PTS)
	return err
}

func (s *fakeSink) finish() error { return s.f.Close() }

func (s *fakeSink) abort() error {
	_ = s.f.Close()
	return os.Remove(s.path)
}

// encodeFixture runs decodeAndEncode over the Blu-ray fixture into work
// with sink, as runBuiltin would.
func encodeFixture(t *testing.T, o Options, work *work, sink encoderSink) ([]string, []string, error) {
	t.Helper()
	src, err := resolveGo(bluray("disc.iso"), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer src.close()
	tracks, err := probeGo(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	sel, err := SelectTracks(tracks)
	if err != nil {
		t.Fatal(err)
	}
	sel.Audio, sel.Subtitles = nil, nil
	g := newGoDemux(src, sel, t.TempDir(), nil)
	if err := g.start(); err != nil {
		t.Fatal(err)
	}
	var lines []string
	r := NewRunner(CurrentGOOS, o, func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) })
	r.work, r.sink = work, sink
	segs, encErr := r.decodeAndEncode(context.Background(), mvc.Source{Format: mvc.FormatAccessUnits, AccessUnits: g.Next}, g.KeepFrame)
	_, _ = g.finish()
	return segs, lines, encErr
}

// An interrupted encode keeps the segments it finished; the next run
// encodes exactly the pictures they do not hold, and the segments together
// are every picture once, in order.
func TestResumeEncodesWhatIsLeft(t *testing.T) {
	withSegments(t, 2)
	o := opts("linux", func(o *Options) { o.Input = bluray("disc.iso") })
	dir := t.TempDir()

	w, err := openWork(dir)
	if err != nil {
		t.Fatal(err)
	}
	first := &fakeSink{failAt: 5}
	if _, _, err := encodeFixture(t, o, w, first); err == nil || !strings.Contains(err.Error(), "fell over") {
		t.Fatalf("the first run should fail at the fifth picture: %v", err)
	}
	if got := w.m.done(); got != 4 || len(w.m.Segments) != 2 {
		t.Fatalf("after the failure: %d frames in %d segments, want 4 in 2", got, len(w.m.Segments))
	}
	if _, err := os.Stat(w.segmentPath(2, ".264")); !os.IsNotExist(err) {
		t.Errorf("the segment cut short is still there: %v", err)
	}

	// Reopened, as the next run does.
	w, err = openWork(dir)
	if err != nil {
		t.Fatal(err)
	}
	second := &fakeSink{}
	segs, lines, err := encodeFixture(t, o, w, second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "resuming: 4 frames") {
		t.Errorf("no word of resuming:\n%s", strings.Join(lines, "\n"))
	}
	if len(second.pts) != 5 || second.starts != 3 {
		t.Errorf("the second run encoded %d pictures in %d segments, want 5 in 3", len(second.pts), second.starts)
	}
	var all []string
	for _, s := range segs {
		b, err := os.ReadFile(s) //nolint:gosec // test
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, strings.Fields(string(b))...)
	}
	var want []string
	for _, p := range append(first.pts, second.pts...) {
		want = append(want, fmt.Sprint(p))
	}
	if strings.Join(all, " ") != strings.Join(want, " ") || len(all) != 9 {
		t.Errorf("the segments hold %v, want the 9 pictures %v", all, want)
	}

	// Other settings, and it starts over.
	w, err = openWork(dir)
	if err != nil {
		t.Fatal(err)
	}
	o.CRF++
	third := &fakeSink{}
	_, lines, err = encodeFixture(t, o, w, third)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.pts) != 9 || !strings.Contains(strings.Join(lines, "\n"), "starting over") {
		t.Errorf("with another CRF: %d pictures encoded, lines:\n%s", len(third.pts), strings.Join(lines, "\n"))
	}
}

// What openWork keeps: the manifest's whole segments, nothing else.
func TestOpenWorkKeepsOnlyWholeSegments(t *testing.T) {
	dir := t.TempDir()
	w, err := openWork(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.begin("key", nil); err != nil {
		t.Fatal(err)
	}
	for i, n := range []int{2, 2} {
		if err := os.WriteFile(w.segmentPath(i, ".265"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := w.add(segment{File: filepath.Base(w.segmentPath(i, ".265")), Frames: n}); err != nil {
			t.Fatal(err)
		}
	}
	// A third listed but empty (a crash mid-write), a fourth listed after
	// it, and leftovers of the run.
	for _, f := range []string{"video-0002.265", "a.thd", "segments.json.new"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(w.segmentPath(3, ".265"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	w.m.Segments = append(w.m.Segments, segment{"video-0002.265", 2}, segment{"video-0003.265", 2})
	if err := w.save(); err != nil {
		t.Fatal(err)
	}

	w, err = openWork(dir)
	if err != nil {
		t.Fatal(err)
	}
	if w.m.done() != 4 || w.m.Key != "key" {
		t.Errorf("reopened: %+v", w.m)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, " ") != "segments.json video-0000.265 video-0001.265" {
		t.Errorf("left %v", names)
	}
	// A garbled manifest is no manifest.
	if err := os.WriteFile(filepath.Join(dir, manifestName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if w, err = openWork(dir); err != nil || w.m.done() != 0 {
		t.Errorf("garbled manifest: %+v %v", w.m, err)
	}
}

// IVF segments read as one IVF stream: the first keeps its file header,
// the others lose theirs.
func TestOpenSegmentsJoinsIVF(t *testing.T) {
	dir := t.TempDir()
	ivf := func(name string, frames ...string) string {
		h := make([]byte, 32)
		copy(h, "DKIF")
		h[6] = 32
		copy(h[8:], "AV01")
		b := append([]byte(nil), h...)
		for _, f := range frames {
			b = append(b, byte(len(f)), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
			b = append(b, f...)
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a, b := ivf("a.ivf", "one", "two"), ivf("b.ivf", "three")
	raw := filepath.Join(dir, "raw.265")
	if err := os.WriteFile(raw, []byte{0, 0, 0, 1, 0x40}, 0o600); err != nil {
		t.Fatal(err)
	}
	r, done, err := openSegments([]string{a, b, raw})
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(a)           //nolint:gosec // test
	second, _ := os.ReadFile(b)         //nolint:gosec // test
	want = append(want, second[32:]...) // header dropped
	want = append(want, 0, 0, 0, 1, 0x40)
	if !bytes.Equal(got, want) {
		t.Errorf("got\n% x\nwant\n% x", got, want)
	}
}

// The end-to-end resume: a conversion that stops partway, run again,
// finishes with exactly the video an uninterrupted one makes, and leaves no
// work directory.
//
// Skips without x264.
func TestRunnerResumesAConversion(t *testing.T) {
	bin, err := LookPath("x264")
	if err != nil {
		t.Skip("x264 not installed")
	}
	withSegments(t, 2)
	run := func(out string, sink func(*Runner) encoderSink) error {
		o := DefaultOptions()
		o.Input, o.Output = bluray("disc.iso"), out
		o.Encoder, o.CRF, o.Preset = EncoderSoftware, 25, "ultrafast"
		o.Subs = TrackFilter{Langs: []string{"none"}}
		r := NewRunner(CurrentGOOS, o, nil)
		if sink != nil {
			r.sink = sink(r)
		}
		return r.Run(context.Background())
	}
	dir := t.TempDir()
	whole := filepath.Join(dir, "whole.mkv")
	if err := run(whole, nil); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "resumed.mkv")
	err = run(out, func(r *Runner) encoderSink {
		return &failingSink{encoderSink: &programSink{r: r, ctx: context.Background(), bin: bin}, failAt: 5}
	})
	if err == nil {
		t.Fatal("the first run should fail")
	}
	work := workDirName(dir, out)
	if b, err := os.ReadFile(filepath.Join(work, manifestName)); err != nil || !bytes.Contains(b, []byte("video-0001")) {
		t.Fatalf("the failed run kept no segments: %v %s", err, b)
	}
	if err := run(out, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Errorf("the work directory is still there: %v", err)
	}
	a, b := videoPackets(t, whole), videoPackets(t, out)
	if len(a) != 9 || len(b) != len(a) {
		t.Fatalf("%d video frames uninterrupted, %d resumed; want 9", len(a), len(b))
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			t.Errorf("frame %d differs", i)
		}
	}
}

// failingSink fails at the failAt-th picture.
type failingSink struct {
	encoderSink
	n, failAt int
}

func (s *failingSink) put(sf *mvc.StereoFrame) error {
	if s.n++; s.n == s.failAt {
		return errors.New("stopped")
	}
	return s.encoderSink.put(sf)
}

// videoPackets reads a Matroska file's video frames.
func videoPackets(t *testing.T, path string) [][]byte {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	r, err := mkv.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	for {
		p, err := r.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		if p.Track == r.Tracks[0].Number {
			out = append(out, p.Data)
		}
	}
}
