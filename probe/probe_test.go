package probe

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brunoga/bdtools/internal/mkv"
)

func testdata(parts ...string) string {
	return filepath.Join(append([]string{"..", "testdata"}, parts...)...)
}

func readFixture(t *testing.T, parts ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(testdata(parts...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func asJSON(t *testing.T, r *Result) string {
	t.Helper()
	c := *r
	c.Source = ""
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The fixture is a Blu-ray 3D in miniature: an MVC title with three audio
// tracks.
func TestDiscImage(t *testing.T) {
	b := readFixture(t, "bluray", "disc.iso")
	r, err := Image(bytes.NewReader(b), int64(len(b)), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != KindDisc || !r.Is3D || r.Layout != LayoutMVC || r.Duration < 300*time.Millisecond {
		t.Errorf("result %+v", r)
	}
	// The MVC view's size comes from the dependent clip's 3D extension; the
	// depth from what Blu-ray allows these codings.
	if len(r.Video) != 2 || r.Video[0] != (VideoTrack{"H.264", 720, 480, 8}) || r.Video[1] != (VideoTrack{"MVC", 720, 480, 8}) {
		t.Errorf("video %+v", r.Video)
	}
	var langs []string
	for _, a := range r.Audio {
		langs = append(langs, a.Codec+"/"+a.Language)
	}
	if strings.Join(langs, " ") != "AC3/eng LPCM/fra E-AC3 (DD+)/deu" {
		t.Errorf("audio %v", langs)
	}
	if r.Audio[0].Channels != 6 || r.Audio[0].BitrateKbps != 384 {
		t.Errorf("AC-3 track %+v: the stream sample did not fill it", r.Audio[0])
	}
	// The same disc as a folder and by path gives the same answer.
	for _, p := range []string{testdata("bluray", "folder"), testdata("bluray", "disc.iso")} {
		pr, err := Path(p)
		if err != nil {
			t.Fatal(err)
		}
		if asJSON(t, pr) != asJSON(t, r) {
			t.Errorf("%s:\n%s\nwant\n%s", p, asJSON(t, pr), asJSON(t, r))
		}
	}
}

// rangeReader serves only some byte ranges of an image, as a torrent with
// a few pieces does; every other read fails. It records what was read.
type rangeReader struct {
	b       []byte
	allowed [][2]int64 // [start, end)
	mu      sync.Mutex
	reads   [][2]int64
}

var errNotFetched = errors.New("not fetched")

func (r *rangeReader) ReadAt(p []byte, off int64) (int, error) {
	end := off + int64(len(p))
	r.mu.Lock()
	r.reads = append(r.reads, [2]int64{off, end})
	r.mu.Unlock()
	if r.allowed != nil {
		ok := false
		for _, a := range r.allowed {
			if off >= a[0] && end <= a[1] {
				ok = true
				break
			}
		}
		if !ok {
			return 0, errNotFetched
		}
	}
	if off >= int64(len(r.b)) {
		return 0, io.EOF
	}
	n := copy(p, r.b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// merged returns the reads as sorted, merged ranges.
func merged(rs [][2]int64) [][2]int64 {
	sort.Slice(rs, func(i, j int) bool { return rs[i][0] < rs[j][0] })
	var out [][2]int64
	for _, r := range rs {
		if n := len(out); n > 0 && r[0] <= out[n-1][1] {
			out[n-1][1] = max(out[n-1][1], r[1])
			continue
		}
		out = append(out, r)
	}
	return out
}

// The whole feature rests on this: an image of which only the directory and
// the BDMV metadata are present still probes, and one missing the
// metadata fails rather than answering wrongly.
func TestPartialImage(t *testing.T) {
	b := readFixture(t, "bluray", "disc.iso")
	full, err := Image(bytes.NewReader(b), int64(len(b)), "fixture")
	if err != nil {
		t.Fatal(err)
	}

	// Learn which bytes a probe reads.
	rec := &rangeReader{b: b}
	if _, err := Image(rec, int64(len(b)), "fixture"); err != nil {
		t.Fatal(err)
	}
	all := merged(rec.reads)

	// Exactly those bytes: the same answer.
	r, err := Image(&rangeReader{b: b, allowed: all}, int64(len(b)), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if asJSON(t, r) != asJSON(t, full) {
		t.Errorf("with only the bytes read:\n%s\nwant\n%s", asJSON(t, r), asJSON(t, full))
	}

	// Only the directory and the metadata — the stream reads (large ones)
	// left out: the title and its tracks still come out, only the details
	// the stream sample adds are missing.
	var meta [][2]int64
	for _, rg := range merged(append([][2]int64(nil), rec.reads...)) {
		if rg[1]-rg[0] < 64<<10 {
			meta = append(meta, rg)
		}
	}
	r, err = Image(&rangeReader{b: b, allowed: meta}, int64(len(b)), "fixture")
	if err != nil {
		t.Fatalf("metadata alone: %v", err)
	}
	if !r.Is3D || r.Layout != LayoutMVC || r.Duration != full.Duration || len(r.Audio) != len(full.Audio) || r.Audio[0].BitrateKbps != 0 {
		t.Errorf("metadata alone: %+v", r)
	}

	// Without the playlist's bytes — whether they fail to read or read as
	// the zeros of a sparse file — there is no answer, not a wrong one.
	at := int64(bytes.Index(b, []byte("MPLS0")))
	if at < 0 {
		t.Fatal("no playlist in the fixture")
	}
	var noPlaylist [][2]int64
	for _, rg := range all {
		if at >= rg[0] && at < rg[1] {
			continue
		}
		noPlaylist = append(noPlaylist, rg)
	}
	r, err = Image(&rangeReader{b: b, allowed: noPlaylist}, int64(len(b)), "fixture")
	var missing *MissingDataError
	switch {
	case err == nil:
		t.Errorf("no playlist bytes, yet a result: %+v", r)
	case !errors.As(err, &missing) || !errors.Is(err, errNotFetched):
		t.Errorf("the error does not say what is missing: %v", err)
	case at < missing.Offset || at >= missing.Offset+missing.Length:
		t.Errorf("missing bytes %d-%d do not hold the playlist at %d", missing.Offset, missing.Offset+missing.Length, at)
	}
	zeroed := append([]byte(nil), b...)
	clear(zeroed[at : at+2048])
	if r, err := Image(bytes.NewReader(zeroed), int64(len(zeroed)), "fixture"); err == nil {
		t.Errorf("a zeroed playlist, yet a result: %+v", r)
	}

	// Without the directory at the front: an error too.
	if r, err := Image(&rangeReader{b: b, allowed: [][2]int64{{int64(len(b)) - 4096, int64(len(b))}}}, int64(len(b)), "fixture"); err == nil {
		t.Errorf("only the image's tail, yet a result: %+v", r)
	}
	// And a reader that has nothing at all.
	if _, err := Image(&rangeReader{b: b, allowed: [][2]int64{}}, int64(len(b)), "fixture"); err == nil {
		t.Error("nothing fetched, yet a result")
	}
}

// A disc without a dependent view says so: 2D, not an error.
func TestDisc2D(t *testing.T) {
	dir := t.TempDir()
	src := testdata("bluray", "folder")
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(dir, rel), 0o750)
		}
		b, err := os.ReadFile(p) //nolint:gosec // test fixture
		if err != nil {
			return err
		}
		if strings.EqualFold(filepath.Ext(p), ".mpls") {
			// No extension data, so no MVC sub-path: no dependent view.
			clear(b[16:20]) // ExtensionData_start_address
		}
		return os.WriteFile(filepath.Join(dir, rel), b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Path(dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.Is3D || r.Layout != Layout2D || r.BaseViewRight || len(r.Video) != 1 || len(r.Audio) != 3 {
		t.Errorf("2D disc: %+v", r)
	}
}

// matroska muxes a short file with a stereo mode and two audio tracks.
func matroska(t *testing.T, stereo int) []byte {
	t.Helper()
	v, err := mkv.NewVideoSource(bytes.NewReader(readFixture(t, "mkv", "bframes.264")), mkv.H264, 24000, 1001, stereo)
	if err != nil {
		t.Fatal(err)
	}
	a, err := mkv.NewAudioSource(bytes.NewReader(readFixture(t, "bluray", "src", "a.ac3")), mkv.AC3, false, "eng")
	if err != nil {
		t.Fatal(err)
	}
	th, err := mkv.NewAudioSource(bytes.NewReader(readFixture(t, "bluray", "src", "e.thd")), mkv.TrueHD, false, "fra")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "x.mkv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := mkv.Mux(f, []mkv.Source{v, a, th}, mkv.Options{}); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	b, err := os.ReadFile(path) //nolint:gosec // test file
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// header strips what only the frames say, so a prefix can be compared with
// the whole file.
func header(r *Result) Result {
	c := *r
	c.Source = ""
	c.Audio = append([]AudioTrack(nil), r.Audio...)
	for i := range c.Audio {
		c.Audio[i].BitrateKbps = 0
	}
	return c
}

// A prefix of a Matroska file, cut anywhere after its Tracks element, probes
// as the whole file does; cut before them, it fails.
func TestTruncatedMatroska(t *testing.T) {
	b := matroska(t, 1)
	whole, err := Matroska(bytes.NewReader(b), "whole")
	if err != nil {
		t.Fatal(err)
	}
	if whole.Kind != KindMatroska || !whole.Is3D || whole.Layout != LayoutSideBySide || whole.Duration < time.Second ||
		len(whole.Video) != 1 || whole.Video[0].Width != 1280 || len(whole.Audio) != 2 ||
		whole.Audio[0].Codec != "AC3" || whole.Audio[0].Language != "eng" || whole.Audio[1].Codec != "TRUE-HD" {
		t.Fatalf("whole file: %+v", whole)
	}
	want, _ := json.Marshal(header(whole))
	tracks := bytes.Index(b, []byte{0x16, 0x54, 0xAE, 0x6B})
	if tracks < 0 {
		t.Fatal("no Tracks element")
	}
	for _, n := range []int{32 << 10, 4096, len(b) / 2} {
		r, err := Matroska(bytes.NewReader(b[:min(n, len(b))]), "prefix")
		if err != nil {
			t.Fatalf("%d-byte prefix: %v", n, err)
		}
		if got, _ := json.Marshal(header(r)); !bytes.Equal(got, want) {
			t.Errorf("%d-byte prefix:\n%s\nwant\n%s", n, got, want)
		}
	}
	if r, err := Matroska(bytes.NewReader(b[:tracks+8]), "cut"); err == nil {
		t.Errorf("cut inside Tracks, yet a result: %+v", r)
	}
	if r, err := Matroska(bytes.NewReader(b[:tracks]), "cut"); err == nil {
		t.Errorf("cut before Tracks, yet a result: %+v", r)
	}
}

// A Matroska file with no stereo mode is 2D, stated as such.
func TestMatroska2D(t *testing.T) {
	r, err := Matroska(bytes.NewReader(matroska(t, 0)), "flat")
	if err != nil {
		t.Fatal(err)
	}
	if r.Is3D || r.Layout != Layout2D || len(r.Video) != 1 {
		t.Errorf("2D file: %+v", r)
	}
}

func TestStereoModes(t *testing.T) {
	for mode, want := range map[int]Layout{0: Layout2D, 1: LayoutSideBySide, 11: LayoutSideBySide,
		2: LayoutTopBottom, 3: LayoutTopBottom, 4: LayoutTopBottom, 12: LayoutMVC, 13: LayoutMVC, 5: LayoutOther, 14: LayoutOther} {
		if got := stereoLayout(mode); got != want {
			t.Errorf("StereoMode %d: %v, want %v", mode, got, want)
		}
	}
}

func TestPathRefusesOtherFiles(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.mp4")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Path(p); err == nil {
		t.Error("an MP4 must not probe")
	}
}

// Partial and malformed input is the normal case: never a panic.
func FuzzMatroska(f *testing.F) {
	f.Add([]byte{0x1A, 0x45, 0xDF, 0xA3, 0x84, 0x42, 0x82, 0x81, 'x'})
	f.Add([]byte("not a matroska file"))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = Matroska(bytes.NewReader(b), "fuzz")
	})
}

// Probing a disc by path leaves no descriptor open.
func TestPathClosesTheImage(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("no /proc/self/fd")
	}
	fds := func() int {
		e, _ := os.ReadDir("/proc/self/fd")
		return len(e)
	}
	before := fds()
	for range 20 {
		if _, err := Path(testdata("bluray", "disc.iso")); err != nil {
			t.Fatal(err)
		}
	}
	if after := fds(); after > before {
		t.Errorf("%d descriptors left open by 20 probes", after-before)
	}
}

// A large element after the tracks — a cover image attachment — does not
// need to be in the sample: a prefix that ends inside it still probes.
func TestMatroskaPrefixEndsInsideAnAttachment(t *testing.T) {
	b := matroska(t, 1)
	whole, err := Matroska(bytes.NewReader(b), "whole")
	if err != nil {
		t.Fatal(err)
	}
	// The Tracks element: its ID followed by a size that fits the file (the
	// seek head names the ID first, followed by other bytes).
	end := -1
	for i := 0; i+12 < len(b) && end < 0; i++ {
		if !bytes.HasPrefix(b[i:], []byte{0x16, 0x54, 0xAE, 0x6B}) {
			continue
		}
		first := b[i+4]
		l := 1
		for first&(0x80>>(l-1)) == 0 && l < 8 {
			l++
		}
		n := int(first) & (0xff >> l)
		for _, c := range b[i+5 : i+4+l] {
			n = n<<8 | int(c)
		}
		if e := i + 4 + l + n; n > 16 && e < len(b) && b[e] != 0 {
			end = e
		}
	}
	if end < 0 {
		t.Fatal("no Tracks element")
	}
	attachment := append([]byte{0xEC, 0x01, 0, 0, 0, 0, 0x10, 0, 0}, make([]byte, 1<<20)...) // a 1 MiB Void
	big := append(append(append([]byte(nil), b[:end]...), attachment...), b[end:]...)
	r, err := Matroska(bytes.NewReader(big[:end+64<<10]), "prefix")
	if err != nil {
		t.Fatalf("prefix ending inside the attachment: %v", err)
	}
	if got, want := header(r), header(whole); asJSON(t, &got) != asJSON(t, &want) {
		t.Errorf("got\n%s\nwant\n%s", asJSON(t, &got), asJSON(t, &want))
	}
}

// A Matroska video track's depth comes from its Colour element or, failing
// that, from the codec's configuration record.
func TestMatroskaBitDepth(t *testing.T) {
	record := func(c mkv.Codec, file string) []byte {
		v, err := mkv.NewVideoSource(bytes.NewReader(readFixture(t, "mkv", file)), c, 24000, 1001, 0)
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join(t.TempDir(), "x.mkv"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		if err := mkv.Mux(f, []mkv.Source{v}, mkv.Options{}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		m, err := mkv.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		return m.Tracks[0].CodecPrivate
	}
	avc, hevc, av1 := record(mkv.H264, "bframes.264"), record(mkv.HEVC, "bframes.265"), record(mkv.AV1, "av1.obu")
	with := func(b []byte, i int, v byte) []byte {
		b = append([]byte(nil), b...)
		b[i] = v
		return b
	}
	// The High profile record ends with chroma format, luma and chroma depth
	// and the SPS extension count; make it High 10 with a luma depth of 10.
	if avc[1] != 100 || avc[len(avc)-3] != 0xf8 {
		t.Fatalf("expected a High profile avcC with its depth fields, got % x", avc)
	}
	high10 := with(with(avc, 1, 110), len(avc)-3, 0xfa)
	for _, c := range []struct {
		name string
		t    mkv.ReadTrack
		want int
	}{
		{"avcC", mkv.ReadTrack{CodecID: "V_MPEG4/ISO/AVC", CodecPrivate: avc}, 8},
		{"avcC High 10", mkv.ReadTrack{CodecID: "V_MPEG4/ISO/AVC", CodecPrivate: high10}, 10},
		{"avcC High 10, cut", mkv.ReadTrack{CodecID: "V_MPEG4/ISO/AVC", CodecPrivate: high10[:len(avc)-4]}, 0},
		{"hvcC", mkv.ReadTrack{CodecID: "V_MPEGH/ISO/HEVC", CodecPrivate: hevc}, 8},
		{"hvcC Main 10", mkv.ReadTrack{CodecID: "V_MPEGH/ISO/HEVC", CodecPrivate: with(hevc, 17, 0xfa)}, 10},
		{"av1C", mkv.ReadTrack{CodecID: "V_AV1", CodecPrivate: av1}, 8},
		{"av1C 10-bit", mkv.ReadTrack{CodecID: "V_AV1", CodecPrivate: with(av1, 2, av1[2]|0x40)}, 10},
		{"av1C 12-bit", mkv.ReadTrack{CodecID: "V_AV1", CodecPrivate: with(av1, 2, av1[2]|0x60)}, 12},
		{"Colour", mkv.ReadTrack{CodecID: "V_MPEGH/ISO/HEVC", CodecPrivate: hevc, BitsPerChannel: 10}, 10},
		{"nothing stated", mkv.ReadTrack{CodecID: "V_VP9"}, 0},
		{"short record", mkv.ReadTrack{CodecID: "V_MPEGH/ISO/HEVC", CodecPrivate: hevc[:10]}, 0},
	} {
		if got := mkvBitDepth(c.t); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
}
