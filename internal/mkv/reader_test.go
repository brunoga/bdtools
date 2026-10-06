package mkv

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// What the muxer writes, the reader reads back: tracks, chapters and every
// frame with its time.
func TestReaderRoundTrip(t *testing.T) {
	mk := func() []Source {
		v, err := NewVideoSource(fixture(t, "mkv", "bframes.264"), H264, 24000, 1001, 1)
		if err != nil {
			t.Fatal(err)
		}
		a, err := NewAudioSource(fixture(t, "bluray", "src", "a.ac3"), AC3, false, "fra")
		if err != nil {
			t.Fatal(err)
		}
		th, err := NewAudioSource(fixture(t, "bluray", "src", "e.thd"), TrueHD, false, "eng")
		if err != nil {
			t.Fatal(err)
		}
		th.SetName("Commentary")
		return []Source{v, a, th}
	}
	path := filepath.Join(t.TempDir(), "x.mkv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	chs := []Chapter{{Start: 0, Name: "One"}, {Start: 500 * time.Millisecond, Name: "Two"}}
	if err := Mux(f, mk(), Options{Chapters: chs}); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	in, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	r, err := NewReader(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Tracks) != 3 {
		t.Fatalf("%d tracks", len(r.Tracks))
	}
	want := []struct {
		codec, lang, name string
		typ               TrackType
	}{{"V_MPEG4/ISO/AVC", "und", "", TypeVideo}, {"A_AC3", "fra", "", TypeAudio}, {"A_TRUEHD", "eng", "Commentary", TypeAudio}}
	for i, w := range want {
		tr := r.Tracks[i]
		if tr.CodecID != w.codec || tr.Language != w.lang || tr.Name != w.name || tr.Type != w.typ {
			t.Errorf("track %d: %+v", i, tr)
		}
	}
	if v := r.Tracks[0]; v.Width != 1280 || v.Height != 480 || v.StereoMode != 1 || len(v.CodecPrivate) < 20 {
		t.Errorf("video: %+v", v)
	}
	if len(r.Chapters) != 2 || r.Chapters[1] != 500*time.Millisecond {
		t.Errorf("chapters %v", r.Chapters)
	}
	if r.Duration <= 0 {
		t.Errorf("duration %v", r.Duration)
	}

	// The same frames, in each track's order, at the times the muxer
	// wrote (rounded to the file's millisecond ticks).
	got := map[uint64][]Packet{}
	for {
		p, err := r.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		got[p.Track] = append(got[p.Track], p)
	}
	for i, s := range mk() {
		var n int
		for ; ; n++ {
			fr, err := s.Next()
			if err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			ps := got[uint64(i+1)]
			if n >= len(ps) {
				t.Fatalf("track %d: only %d frames read back", i+1, len(ps))
			}
			p := ps[n]
			if !bytes.Equal(p.Data, fr.Data) || p.Time != time.Duration(ticks(fr.PTS))*time.Millisecond {
				t.Fatalf("track %d frame %d: %v %d bytes, want %v %d bytes", i+1, n, p.Time, len(p.Data), fr.PTS, len(fr.Data))
			}
			if i == 0 && p.Keyframe != fr.Keyframe {
				t.Fatalf("frame %d: keyframe %v", n, p.Keyframe)
			}
		}
		if n != len(got[uint64(i+1)]) {
			t.Errorf("track %d: %d frames read, %d written", i+1, len(got[uint64(i+1)]), n)
		}
	}
}

func TestUnlace(t *testing.T) {
	a, b, c := bytes.Repeat([]byte{1}, 300), bytes.Repeat([]byte{2}, 5), bytes.Repeat([]byte{3}, 70)
	all := append(append(append([]byte(nil), a...), b...), c...)
	for name, tc := range map[string]struct {
		lacing byte
		head   []byte
		want   [][]byte
	}{
		"xiph": {1, []byte{2, 255, 45, 5}, [][]byte{a, b, c}},
		// 300 as a 2-byte vint, then 5-300 = -295 as a signed 2-byte vint.
		"ebml":  {3, []byte{2, 0x41, 0x2c, 0x5e, 0xd8}, [][]byte{a, b, c}},
		"fixed": {2, []byte{2}, nil},
	} {
		data := append(append([]byte(nil), tc.head...), all...)
		if name == "fixed" {
			data = append([]byte{2}, bytes.Repeat([]byte{9}, 375)...)
			tc.want = [][]byte{bytes.Repeat([]byte{9}, 125), bytes.Repeat([]byte{9}, 125), bytes.Repeat([]byte{9}, 125)}
		}
		frames, err := unlace(data, tc.lacing)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(frames) != len(tc.want) {
			t.Fatalf("%s: %d frames", name, len(frames))
		}
		for i := range frames {
			if !bytes.Equal(frames[i], tc.want[i]) {
				t.Errorf("%s: frame %d is %d bytes, want %d", name, i, len(frames[i]), len(tc.want[i]))
			}
		}
	}
	if _, err := unlace([]byte{2, 255}, 1); err == nil {
		t.Error("a truncated lace must fail")
	}
}

// Files mkvmerge wrote — laced, header-stripped, with BlockGroups — read
// back to exactly what mkvextract takes out of them.
func TestReaderMatchesMkvextract(t *testing.T) {
	mkvmerge, err1 := exec.LookPath("mkvmerge")
	mkvextract, err2 := exec.LookPath("mkvextract")
	if err1 != nil || err2 != nil {
		t.Skip("no mkvmerge/mkvextract")
	}
	dir := t.TempDir()
	src := filepath.Join("..", "..", "testdata", "bluray", "src")
	for name, args := range map[string][]string{
		"plain":  {filepath.Join(src, "a.ac3"), filepath.Join(src, "d.dts")},
		"zlib":   {"--compression", "0:zlib", filepath.Join(src, "a.ac3")},
		"truehd": {filepath.Join(src, "e.thd"), filepath.Join(src, "a.ac3")},
	} {
		out := filepath.Join(dir, name+".mkv")
		if b, err := exec.CommandContext(t.Context(), mkvmerge, append([]string{"-q", "-o", out}, args...)...).CombinedOutput(); err != nil { //nolint:gosec // test
			if errors.As(err, new(*exec.ExitError)) && bytes.Contains(b, []byte("Warning")) {
				// mkvmerge exits 1 on warnings.
			} else {
				t.Fatalf("%s: mkvmerge: %v\n%s", name, err, b)
			}
		}
		f, err := os.Open(out) //nolint:gosec // test
		if err != nil {
			t.Fatal(err)
		}
		r, err := NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		got := map[uint64]*bytes.Buffer{}
		for {
			p, err := r.Next()
			if err == io.EOF {
				break
			} else if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if got[p.Track] == nil {
				got[p.Track] = &bytes.Buffer{}
			}
			got[p.Track].Write(p.Data)
		}
		_ = f.Close()
		for _, tr := range r.Tracks {
			raw := filepath.Join(dir, name+"_raw")
			// mkvextract numbers tracks from 0.
			if b, err := exec.CommandContext(t.Context(), mkvextract, out, "tracks", itoa(int64(tr.Number-1))+":"+raw).CombinedOutput(); err != nil { //nolint:gosec // test
				t.Fatalf("mkvextract: %v\n%s", err, b)
			}
			want, _ := os.ReadFile(raw) //nolint:gosec // test
			if got[tr.Number] == nil || !bytes.Equal(got[tr.Number].Bytes(), want) {
				t.Errorf("%s: track %d (%s): %d bytes read, mkvextract gives %d", name, tr.Number, tr.CodecID, got[tr.Number].Len(), len(want))
			}
		}
	}
}

func TestReaderRejectsOtherFiles(t *testing.T) {
	if _, err := NewReader(bytes.NewReader([]byte("not a matroska file at all"))); err == nil {
		t.Error("garbage must not read")
	}
	if _, err := NewReader(bytes.NewReader(nil)); err == nil {
		t.Error("an empty file must not read")
	}
}

func FuzzReader(f *testing.F) {
	f.Add([]byte{0x1A, 0x45, 0xDF, 0xA3, 0x84, 0x42, 0x82, 0x81, 'x'})
	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := NewReader(bytes.NewReader(b))
		if err != nil {
			return
		}
		for i := 0; i < 10000; i++ {
			if _, err := r.Next(); err != nil {
				return
			}
		}
	})
}

// Header stripping puts the stripped bytes back in front of every frame,
// laced ones included.
func TestReaderHeaderStripping(t *testing.T) {
	r := &Reader{scale: 1000000, byNumber: map[uint64]*ReadTrack{}}
	tr := &ReadTrack{Number: 1, comprAlgo: 3, comprSettings: []byte{0x0b, 0x77}}
	r.byNumber[1] = tr
	// Track 1, timestamp +5, Xiph lacing, two frames of 2 and 1 bytes.
	if err := r.block([]byte{0x81, 0, 5, 0x82, 1, 2, 'a', 'b', 'c'}, true); err != nil {
		t.Fatal(err)
	}
	if len(r.pending) != 2 || string(r.pending[0].Data) != "\x0b\x77ab" || string(r.pending[1].Data) != "\x0b\x77c" ||
		r.pending[0].Time != 5*time.Millisecond {
		t.Errorf("%+v", r.pending)
	}
}
