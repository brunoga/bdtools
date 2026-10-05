package bdmv

import (
	"io"
	"path/filepath"
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
	f.Fuzz(func(t *testing.T, b []byte) {
		if pl, err := ParseMPLS(b); err == nil {
			_ = pl.Duration()
			_ = pl.Chapters()
			_ = pl.String()
		}
		_, _ = ParseCLPI(b)
	})
}
