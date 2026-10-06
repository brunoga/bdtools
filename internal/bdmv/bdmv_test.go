package bdmv

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fixture(name string) string { return filepath.Join("..", "..", "testdata", "bluray", name) }

func TestPlaylistOfAFolder(t *testing.T) {
	d, err := Open(fixture("folder"))
	if err != nil {
		t.Fatal(err)
	}
	names, err := Playlists(d)
	if err != nil || len(names) != 1 || names[0] != "00000.mpls" {
		t.Fatalf("playlists = %v, %v", names, err)
	}
	pl, err := ReadPlaylist(d, names[0])
	if err != nil {
		t.Fatal(err)
	}
	if !pl.ThreeD() || len(pl.Items) != 1 {
		t.Fatalf("want one 3D item, got %s", pl)
	}
	it := pl.Items[0]
	if it.Clip != "00000" || it.DependentClip != "00001" {
		t.Errorf("clips %q/%q, want 00000 with dependent 00001", it.Clip, it.DependentClip)
	}
	// Nine frames at 23.976.
	if d := pl.Duration(); d < 370*time.Millisecond || d > 380*time.Millisecond {
		t.Errorf("duration %v, want 375 ms", d)
	}
	if pl.BaseViewIsRight {
		t.Error("the fixture's base view is the left eye")
	}
	langs := map[uint16]string{}
	for _, s := range it.Streams {
		langs[s.PID] = s.Lang
	}
	if langs[0x1100] != "eng" || langs[0x1101] != "fra" || langs[0x1102] != "deu" {
		t.Errorf("languages %v", langs)
	}
	// No SSIF in a folder: the views are separate files.
	if p, ssif := StreamPath(d, it); ssif || p != "STREAM/00000.m2ts" {
		t.Errorf("stream %s ssif=%v", p, ssif)
	}
	if p := DependentStreamPath(d, it); p != "STREAM/00001.m2ts" || !d.Exists(p) {
		t.Errorf("dependent stream %s", p)
	}
}

// An image is read in place, and its 3D clip is the interleaved SSIF.
func TestImageIsReadInPlace(t *testing.T) {
	d, err := Open(fixture("disc.iso"))
	if err != nil {
		t.Fatal(err)
	}
	pl, err := ReadPlaylist(d, "00000.mpls")
	if err != nil {
		t.Fatal(err)
	}
	p, ssif := StreamPath(d, pl.Items[0])
	if !ssif || p != "STREAM/SSIF/00000.ssif" {
		t.Fatalf("stream %s ssif=%v, want the SSIF", p, ssif)
	}
	r, size, err := d.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	head := make([]byte, 192)
	if _, err := io.ReadFull(r, head); err != nil || head[4] != 0x47 {
		t.Fatalf("not a transport stream: % x, %v", head[:8], err)
	}
	if size != 399360 {
		t.Errorf("size %d", size)
	}
	// Case does not matter, as on a disc authored elsewhere.
	if !d.Exists("stream/ssif/00000.SSIF") {
		t.Error("lookups must ignore case")
	}
}

func TestClipInfoLanguages(t *testing.T) {
	d, err := Open(fixture("folder"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.ReadFile(ClipInfo(d, "00000"))
	if err != nil {
		t.Fatal(err)
	}
	streams, err := ParseCLPI(b)
	if err != nil {
		t.Fatal(err)
	}
	got := map[uint16]string{}
	for _, s := range streams {
		got[s.PID] = s.Lang
	}
	if got[0x1100] != "eng" || got[0x1101] != "fra" || got[0x1102] != "deu" || len(streams) != 4 {
		t.Errorf("streams %+v", streams)
	}
}

// A loop of one short clip lasts longer than the feature without holding
// more of it.
func TestUniqueDurationIgnoresLoops(t *testing.T) {
	item := func(clip string, secs uint32) PlayItem {
		return PlayItem{Clip: clip, InTime: 45000, OutTime: 45000 + secs*45000}
	}
	loop := &Playlist{}
	for i := 0; i < 100; i++ {
		loop.Items = append(loop.Items, item("00240", 80))
	}
	feature := &Playlist{Items: []PlayItem{item("00272", 5280)}}
	if loop.Duration() <= feature.Duration() {
		t.Fatal("the loop should run longer")
	}
	if loop.UniqueDuration() >= feature.UniqueDuration() {
		t.Errorf("unique: loop %v, feature %v", loop.UniqueDuration(), feature.UniqueDuration())
	}
}

func TestChaptersAcrossItems(t *testing.T) {
	pl := &Playlist{
		Items: []PlayItem{{InTime: 90000, OutTime: 90000 + 45000*60}, {InTime: 0, OutTime: 45000 * 30}},
		Marks: []Mark{{Type: 1, PlayItem: 0, Time: 90000}, {Type: 1, PlayItem: 0, Time: 90000 + 45000*10},
			{Type: 2, PlayItem: 1, Time: 0}, {Type: 1, PlayItem: 1, Time: 45000 * 5}},
	}
	got := pl.Chapters()
	want := []time.Duration{0, 10 * time.Second, 65 * time.Second}
	if len(got) != len(want) {
		t.Fatalf("chapters %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chapter %d at %v, want %v", i, got[i], want[i])
		}
	}
}

func TestNotAPlaylist(t *testing.T) {
	if _, err := ParseMPLS([]byte("HDMV0200")); err == nil {
		t.Error("garbage must not parse")
	}
	b := make([]byte, 64)
	copy(b, "MPLS0200")
	if _, err := ParseMPLS(b); err == nil {
		t.Error("a playlist with no list must not parse")
	}
}

// A dependent view's clip lists its MVC stream in the clip info's 3D
// extension, not in its program.
func TestCLPIDependentView(t *testing.T) {
	d, err := Open(fixture("folder"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.ReadFile("CLIPINF/00001.clpi")
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseCLPI(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(s) != 1 || s[0] != (ClipStream{PID: 0x1012, Coding: CodingMVC, Format: 3, Rate: 1}) {
		t.Errorf("streams %+v", s)
	}
	// The extension's entries are bounds-checked: a table running off the
	// end of the file is no extension.
	ext := int(b[24])<<24 | int(b[25])<<16 | int(b[26])<<8 | int(b[27])
	if _, ok := clpiExtension(b[:ext+20], 2, 5); ok {
		t.Error("a cut extension table was read")
	}
}

func FuzzParseMPLS(f *testing.F) {
	d, err := Open(fixture("folder"))
	if err != nil {
		f.Fatal(err)
	}
	b, err := d.ReadFile("PLAYLIST/00000.mpls")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(b)
	if b, err = d.ReadFile("CLIPINF/00001.clpi"); err != nil {
		f.Fatal(err)
	}
	f.Add(b)
	f.Fuzz(func(t *testing.T, b []byte) {
		if pl, err := ParseMPLS(b); err == nil {
			_ = pl.Duration()
			_ = pl.Chapters()
			_ = pl.String()
		}
		_, _ = ParseCLPI(b)
	})
}

// A disc read from a reader is the disc read from its path: the same
// playlists, durations, stream files and clip streams.
func TestOpenImageMatchesOpen(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "bluray", "disc.iso")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	byPath, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = byPath.Close() }()
	byReader, err := OpenImage(bytes.NewReader(b), int64(len(b)), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = byReader.Close() }()
	describe := func(d Disc) string {
		names, err := Playlists(d)
		if err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		for _, n := range names {
			pl, err := ReadPlaylist(d, n)
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&out, "%s %v 3D=%v\n", n, pl.Duration(), pl.ThreeD())
			for _, it := range pl.Items {
				p, ssif := StreamPath(d, it)
				cb, err := d.ReadFile(ClipInfo(d, it.Clip))
				if err != nil {
					t.Fatal(err)
				}
				cs, err := ParseCLPI(cb)
				if err != nil {
					t.Fatal(err)
				}
				fmt.Fprintf(&out, "  %s ssif=%v %+v\n", p, ssif, cs)
			}
		}
		return out.String()
	}
	a, b2 := describe(byPath), describe(byReader)
	if a != b2 {
		t.Errorf("by path:\n%s\nby reader:\n%s", a, b2)
	}
	if !strings.Contains(a, "ssif=true") {
		t.Errorf("the fixture should read as an SSIF title:\n%s", a)
	}
}

// Closing a disc read from a reader leaves the reader alone; closing one
// opened from a path closes its file.
func TestDiscClose(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "bluray", "disc.iso")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	st, _ := f.Stat()
	d, err := OpenImage(f, st.Size(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ReadAt(make([]byte, 1), 0); err != nil {
		t.Errorf("the caller's reader was closed: %v", err)
	}
	if runtime.GOOS != "linux" {
		return
	}
	fds := func() int {
		e, _ := os.ReadDir("/proc/self/fd")
		return len(e)
	}
	before := fds()
	for range 20 {
		d, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if after := fds(); after > before {
		t.Errorf("%d descriptors left open by 20 opens", after-before)
	}
}

// A truncated or garbled image is an error, never a panic.
func FuzzOpenImage(f *testing.F) {
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "bluray", "disc.iso"))
	if err != nil {
		f.Skip(err)
	}
	f.Add(int64(len(b)), int64(0), byte(0))
	f.Fuzz(func(t *testing.T, size, at int64, v byte) {
		img := append([]byte(nil), b...)
		if at >= 0 && at < int64(len(img)) {
			img[at] = v
		}
		if size <= 0 || size > int64(len(img)) {
			size = int64(len(img))
		}
		d, err := OpenImage(bytes.NewReader(img[:size]), size, "fuzz")
		if err != nil {
			return
		}
		names, _ := Playlists(d)
		for _, n := range names {
			_, _ = ReadPlaylist(d, n)
		}
	})
}

// The STN_table_SS gives each PG stream its offset sequence, after the
// dependent view's entry; a stereoscopic PG stream's own entries are
// skipped, and 255 is none.
func TestSTNTableSSOffsetSequences(t *testing.T) {
	pl := &Playlist{Items: []PlayItem{{Streams: []Stream{
		{Kind: 0, OffsetSequence: -1}, {Kind: 1, OffsetSequence: -1},
		{Kind: 2, OffsetSequence: -1}, {Kind: 2, OffsetSequence: -1}, {Kind: 2, OffsetSequence: -1},
	}}}}
	entry := []byte{9, 1, 0x12, 0x20, 0, 0, 0, 0, 0, 0}
	body := []byte{0x80, 0}                                 // fixed offset during pop-up, reserved
	body = append(body, 9, 2, 0, 0, 0x10, 0x12, 0, 0, 0, 0) // the dependent view, in a sub-path
	body = append(body, 2, 0x20, 0x61, 0, 32)               // attributes; 32 offset sequences
	body = append(body, 0, 0)                               // PG 1: sequence 0
	body = append(body, 7, 0x08)                            // PG 2: sequence 7, stereoscopic
	body = append(body, entry...)
	body = append(body, entry...)
	body = append(body, 0, 3)
	body = append(body, 0xff, 0) // PG 3: none
	b := append([]byte{byte(len(body) >> 8), byte(len(body))}, body...)
	pl.parseSTNSS(&cursor{b: b})
	it := pl.Items[0]
	if it.DependentPID != 0x1012 {
		t.Errorf("dependent PID %#x", it.DependentPID)
	}
	var got []int
	for _, s := range it.Streams {
		got = append(got, s.OffsetSequence)
	}
	if fmt.Sprint(got) != "[-1 -1 0 7 -1]" {
		t.Errorf("offset sequences %v", got)
	}
}
