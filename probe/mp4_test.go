package probe

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brunoga/bdtools/internal/mkv"
	"github.com/brunoga/bdtools/internal/mp4"
)

// muxStereo writes the repository's MV-HEVC test stream (640x368, 24 frames
// at 24000/1001) through the MP4 muxer, which is the file a conversion to
// Vision Pro produces. The muxer puts the movie box last.
func muxStereo(t *testing.T, o mp4.Options) []byte {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "internal", "mkv", "testdata", "mvhevc.hevc"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	v, err := mkv.NewVideoSource(f, mkv.HEVC, 24000, 1001, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "stereo.mp4")
	of, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	o.FPSNum, o.FPSDen = 24000, 1001
	err = mp4.Mux(of, []mkv.Source{v}, o)
	_ = of.Close()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMP4ReadsMVHEVC(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    mp4.Options
	}{
		{"spatial MP4", mp4.Options{Spatial: &mp4.Spatial{FOV: 90, Baseline: 63}}},
		{"spatial QuickTime", mp4.Options{QuickTime: true, Spatial: &mp4.Spatial{FOV: 71.6, Baseline: 63.76, HeroRight: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := muxStereo(t, tc.o)
			res, err := MP4(bytes.NewReader(b), int64(len(b)), "stereo")
			if err != nil {
				t.Fatal(err)
			}
			if res.Kind != KindMP4 || !res.Is3D || res.Layout != LayoutMVHEVC {
				t.Fatalf("kind %v, 3D %v, layout %v; want an MP4 carrying MV-HEVC", res.Kind, res.Is3D, res.Layout)
			}
			if len(res.Video) != 1 {
				t.Fatalf("%d video tracks, want 1", len(res.Video))
			}
			v := res.Video[0]
			if v.Codec != "MV-HEVC" || v.Width != 640 || v.Height != 368 || v.BitDepth != 8 {
				t.Errorf("video %+v, want MV-HEVC 640x368 8-bit", v)
			}
			// 24 frames at 24000/1001 is 1.001 s.
			if d := res.Duration; d < 990*time.Millisecond || d > 1010*time.Millisecond {
				t.Errorf("duration %v, want about 1.001s", d)
			}
		})
	}
}

// sampleReader serves only the first and last piece of a file, as a
// torrent sample does, and refuses everything between.
type sampleReader struct {
	b     []byte
	piece int64
}


func (s sampleReader) ReadAt(p []byte, off int64) (int, error) {
	end := off + int64(len(p))
	size := int64(len(s.b))
	inFirst := end <= s.piece
	inLast := off >= size-s.piece
	if !inFirst && !inLast {
		return 0, fmt.Errorf("bytes %d-%d: %w", off, end, errNotFetched)
	}
	return copy(p, s.b[off:min(end, size)]), nil
}

// TestMP4FromTheEndsOfAFile is the claim the probe rests on: the movie box
// sits in the first or the last piece, so the two pieces a probe already
// fetches for a disc image are enough for an MP4 too. The media data box
// between them is skipped by its size and never read.
func TestMP4FromTheEndsOfAFile(t *testing.T) {
	b := muxStereo(t, mp4.Options{Spatial: &mp4.Spatial{FOV: 90, Baseline: 63}})
	piece := int64(len(b)) / 4
	res, err := MP4(sampleReader{b, piece}, int64(len(b)), "stereo")
	if err != nil {
		t.Fatalf("first and last quarter: %v", err)
	}
	if res.Layout != LayoutMVHEVC {
		t.Errorf("layout %v, want MV-HEVC", res.Layout)
	}

	// Without the last piece the probe says which bytes it wanted.
	_, err = MP4(firstPiece{b, piece}, int64(len(b)), "stereo")
	var md *MissingDataError
	if !errors.As(err, &md) {
		t.Fatalf("got %v, want a *MissingDataError", err)
	}
	if md.Offset < int64(len(b))-piece {
		t.Errorf("missing data at %d, but the movie box is in the last piece (from %d)", md.Offset, int64(len(b))-piece)
	}
}

// firstPiece serves the first piece alone.
type firstPiece struct {
	b     []byte
	piece int64
}

func (f firstPiece) ReadAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > f.piece {
		return 0, fmt.Errorf("bytes %d-%d: %w", off, off+int64(len(p)), errNotFetched)
	}
	return copy(p, f.b[off:]), nil
}

// --- hand-built files, for what the muxer does not write ---------------------

func bx(typ string, body ...[]byte) []byte {
	b := bytes.Join(body, nil)
	out := binary.BigEndian.AppendUint32(nil, uint32(8+len(b))) //nolint:gosec // test sizes are small
	return append(append(out, typ...), b...)
}

func full(typ string, v byte, body ...[]byte) []byte {
	return bx(typ, append([]byte{v, 0, 0, 0}, bytes.Join(body, nil)...))
}

func u16(v int) []byte { return binary.BigEndian.AppendUint16(nil, uint16(v)) } //nolint:gosec // test
func u32(v int) []byte { return binary.BigEndian.AppendUint32(nil, uint32(v)) } //nolint:gosec // test

// visual is a visual sample entry of the given size with child boxes.
func visual(typ string, w, h int, ext ...[]byte) []byte {
	fixed := bytes.Join([][]byte{
		make([]byte, 6), u16(1), make([]byte, 16), u16(w), u16(h),
		u32(0x00480000), u32(0x00480000), u32(0), u16(1), make([]byte, 32), u16(0x18), u16(0xffff),
	}, nil)
	return bx(typ, append(fixed, bytes.Join(ext, nil)...))
}

func sound(typ string, channels, rate int) []byte {
	return bx(typ, make([]byte, 6), u16(1), make([]byte, 8), u16(channels), u16(16), make([]byte, 4), u32(rate<<16))
}

// lang packs an ISO 639-2/T code as a media header stores it.
func lang(s string) int {
	return int(s[0]-0x60)<<10 | int(s[1]-0x60)<<5 | int(s[2]-0x60)
}

func trak(handler string, language int, entry []byte) []byte {
	mdhd := full("mdhd", 0, u32(0), u32(0), u32(1000), u32(5000), u16(language), u16(0))
	hdlr := full("hdlr", 0, u32(0), []byte(handler), make([]byte, 12), []byte{0})
	stsd := full("stsd", 0, u32(1), entry)
	return bx("trak", bx("mdia", mdhd, hdlr, bx("minf", bx("stbl", stsd))))
}

// fastStart is a file with its movie box BEFORE the media data, as a
// streaming-ready MP4 is written.
func fastStart(traks ...[]byte) []byte {
	mvhd := full("mvhd", 0, u32(0), u32(0), u32(600), u32(600*7200), make([]byte, 80))
	moov := bx("moov", append([][]byte{mvhd}, traks...)...)
	return bytes.Join([][]byte{bx("ftyp", []byte("isom"), u32(0), []byte("isom")), moov, bx("mdat", make([]byte, 4096))}, nil)
}

func probeBytes(t *testing.T, b []byte) *Result {
	t.Helper()
	res, err := MP4(bytes.NewReader(b), int64(len(b)), "built")
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestMP4TwoDimensional(t *testing.T) {
	hvcC := make([]byte, 23)
	hvcC[17] = 0x02 // 10-bit
	b := fastStart(
		trak("vide", lang("und"), visual("hvc1", 3840, 2160, bx("hvcC", hvcC))),
		trak("soun", lang("eng"), sound("ec-3", 6, 48000)),
		trak("soun", lang("por"), sound("mp4a", 2, 48000)),
		trak("sbtl", lang("eng"), bx("tx3g", make([]byte, 8))),
	)
	res := probeBytes(t, b)
	if res.Is3D || res.Layout != Layout2D {
		t.Errorf("3D %v, layout %v; a single-layer HEVC track is 2D", res.Is3D, res.Layout)
	}
	if res.Duration != 2*time.Hour {
		t.Errorf("duration %v, want 2h from the movie header", res.Duration)
	}
	if v := res.Video[0]; v.Codec != "HEVC" || v.Width != 3840 || v.Height != 2160 || v.BitDepth != 10 {
		t.Errorf("video %+v", v)
	}
	want := []AudioTrack{
		{Codec: "E-AC3 (DD+)", Language: "eng", Channels: 6, SampleRate: 48000},
		{Codec: "AAC", Language: "por", Channels: 2, SampleRate: 48000},
	}
	if fmt.Sprint(res.Audio) != fmt.Sprint(want) {
		t.Errorf("audio %+v, want %+v", res.Audio, want)
	}
	if len(res.Subtitle) != 1 || res.Subtitle[0].Language != "eng" {
		t.Errorf("subtitles %+v", res.Subtitle)
	}
}

// A layered track whose Apple eyes box names only one eye is not a stereo
// pair, and a frame-packed track says so in its spherical video box.
func TestMP4StereoSignalling(t *testing.T) {
	stri := func(flags byte) []byte {
		return bx("vexu", bx("eyes", full("stri", 0, []byte{flags})))
	}
	for _, tc := range []struct {
		name  string
		ext   [][]byte
		want  Layout
		codec string
	}{
		{"layered without Apple's box", [][]byte{bx("hvcC", make([]byte, 23)), bx("lhvC", make([]byte, 8))}, LayoutMVHEVC, "MV-HEVC"},
		{"layered, both eyes", [][]byte{bx("hvcC", make([]byte, 23)), bx("lhvC", make([]byte, 8)), stri(0x03)}, LayoutMVHEVC, "MV-HEVC"},
		{"layered, left eye only", [][]byte{bx("hvcC", make([]byte, 23)), bx("lhvC", make([]byte, 8)), stri(0x01)}, Layout2D, "HEVC"},
		{"side by side", [][]byte{bx("hvcC", make([]byte, 23)), full("st3d", 0, []byte{2})}, LayoutSideBySide, "HEVC"},
		{"top bottom", [][]byte{bx("hvcC", make([]byte, 23)), full("st3d", 0, []byte{1})}, LayoutTopBottom, "HEVC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := probeBytes(t, fastStart(trak("vide", 0, visual("hvc1", 1920, 1080, tc.ext...))))
			if res.Layout != tc.want || res.Is3D != (tc.want != Layout2D) {
				t.Errorf("layout %v (3D %v), want %v", res.Layout, res.Is3D, tc.want)
			}
			if res.Video[0].Codec != tc.codec {
				t.Errorf("codec %q, want %q", res.Video[0].Codec, tc.codec)
			}
		})
	}
}

func TestMP4RefusesOtherFiles(t *testing.T) {
	for name, b := range map[string][]byte{
		"Matroska":     {0x1a, 0x45, 0xdf, 0xa3, 0, 0, 0, 0, 0, 0, 0, 0},
		"no movie box": bytes.Join([][]byte{bx("ftyp", []byte("isom")), bx("mdat", make([]byte, 64))}, nil),
		"box too big":  append(u32(1<<20), []byte("ftyp")...),
	} {
		if _, err := MP4(bytes.NewReader(b), int64(len(b)), name); err == nil {
			t.Errorf("%s: probed without error", name)
		}
	}
	// Truncated and corrupted movie boxes fail or answer; they never panic.
	b := fastStart(trak("vide", 0, visual("hvc1", 1920, 1080, bx("hvcC", make([]byte, 23)))))
	for i := 8; i < len(b); i += 7 {
		c := append([]byte(nil), b...)
		c[i] ^= 0xff
		_, _ = MP4(bytes.NewReader(c), int64(len(c)), "corrupt")
	}
}

func TestPathProbesMP4(t *testing.T) {
	b := muxStereo(t, mp4.Options{QuickTime: true, Spatial: &mp4.Spatial{FOV: 90, Baseline: 63}})
	p := filepath.Join(t.TempDir(), "Some Film AVP.mov")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Path(p)
	if err != nil {
		t.Fatal(err)
	}
	if res.Layout != LayoutMVHEVC || !strings.HasSuffix(res.Source, ".mov") {
		t.Errorf("layout %v source %q", res.Layout, res.Source)
	}
}
