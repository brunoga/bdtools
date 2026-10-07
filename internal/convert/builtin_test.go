package convert

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brunoga/mvc"
	"github.com/brunoga/mvc/internal/bdmv"
	"github.com/brunoga/mvc/internal/hwenc"
	"github.com/brunoga/mvc/m2ts"
)

// The built-in demuxer is tested on a synthetic Blu-ray (testdata/bluray):
// the same MVC pair as the other fixtures, with AC-3, LPCM and E-AC-3
// tracks, as a folder (two clip files) and as a UDF image (an SSIF).

func bluray(name string) string {
	p, _ := filepath.Abs(filepath.Join("..", "..", "testdata", "bluray", name))
	return p
}

var bothForms = []string{"folder", "disc.iso"}

func TestBuiltinListsTheDisc(t *testing.T) {
	for _, form := range bothForms {
		src, err := resolveGo(bluray(form), "", nil)
		if err != nil {
			t.Fatal(err)
		}
		tracks, err := probeGo(context.Background(), src)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, tr := range tracks {
			got = append(got, tr.StreamID+"/"+tr.Type+"/"+tr.Lang)
		}
		want := []string{"V_MPEG4/ISO/AVC/H.264/", "V_MPEG4/ISO/MVC/MVC/", "A_AC3/AC3/eng", "A_LPCM/LPCM/fra",
			"A_AC3/E-AC3 (DD+)/deu"}
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s: tracks\n %v\nwant\n %v", form, got, want)
		}
		if _, err := SelectTracks(tracks); err != nil {
			t.Errorf("%s: %v", form, err)
		}
	}
}

// decodeBuiltin decodes a source through the built-in demuxer into
// side-by-side Y4M, writing the selected other tracks to dir.
func decodeBuiltin(t *testing.T, input, dir string, filter func(*Selection)) ([]byte, []extra) {
	t.Helper()
	src, err := resolveGo(input, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	tracks, err := probeGo(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	sel, err := SelectTracks(tracks)
	if err != nil {
		t.Fatal(err)
	}
	if filter != nil {
		filter(&sel)
	}
	g := newGoDemux(src, sel, dir, nil)
	if err := g.start(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	y4m := mvc.NewY4MWriter(&out, mvc.LayoutSideBySide)
	st, err := mvc.NewDecoder(mvc.Options{}).DecodeStream(mvc.Source{Format: mvc.FormatAccessUnits, AccessUnits: g.Next},
		mvc.DecodeOptions{}, func(sf *mvc.StereoFrame) error {
			if !g.KeepFrame(sf.Base.PTS) {
				return nil
			}
			return y4m.Write(sf)
		})
	if err != nil {
		t.Fatal(err)
	}
	if err := y4m.Flush(); err != nil {
		t.Fatal(err)
	}
	if st.Errors != 0 || st.DependentFrames != st.Frames {
		t.Errorf("%d errors, %d of %d frames with a dependent view", st.Errors, st.DependentFrames, st.Frames)
	}
	extras, err := g.finish()
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes(), extras
}

// Reading the disc in place gives exactly the pictures of the combined
// stream the clips were made from, whether the views are two files or an
// interleaved SSIF.
func TestBuiltinDecodeMatchesTheCombinedStream(t *testing.T) {
	dir := fixtureDir(t)
	want := decodeSource(t, mvc.Source{Format: mvc.FormatAnnexB, R: bytes.NewReader(readFixture(t, dir, "mvc_combined.264"))}, false)
	for _, form := range bothForms {
		got, _ := decodeBuiltin(t, bluray(form), t.TempDir(), func(s *Selection) { s.Audio, s.Subtitles = nil, nil })
		if !bytes.Equal(got, want) {
			t.Errorf("%s: %d bytes of Y4M differ from the combined stream's %d", form, len(got), len(want))
		}
	}
}

// The audio is written as tsMuxeR writes it — the PES payloads, untouched —
// but only for the stretch the playlist plays: a player never plays what is
// past OUT_time (here the 1 s tone outlasts the 9-frame video).
func TestBuiltinAudioIsTheSourceCutToThePlaylist(t *testing.T) {
	for _, form := range bothForms {
		_, extras := decodeBuiltin(t, bluray(form), t.TempDir(), nil)
		if len(extras) != 3 {
			t.Fatalf("%s: %d tracks written, want 3", form, len(extras))
		}
		for _, e := range extras {
			got, err := os.ReadFile(e.path)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case strings.HasSuffix(e.path, ".ac3"), strings.HasSuffix(e.path, ".eac3"):
				srcName := map[bool]string{true: "a.ac3", false: "c.eac3"}[strings.HasSuffix(e.path, ".ac3")]
				src, _ := os.ReadFile(bluray("src/" + srcName))
				if len(got) == 0 || !bytes.HasPrefix(src, got) {
					t.Errorf("%s %s: not a prefix of the source (%d of %d bytes)", form, filepath.Base(e.path), len(got), len(src))
				}
				// 0.375 s of video: the cut is within one frame of it.
				if len(got) >= len(src)/2 {
					t.Errorf("%s %s: %d of %d bytes kept; the audio past OUT_time should be cut", form, filepath.Base(e.path), len(got), len(src))
				}
			case strings.HasSuffix(e.path, ".wav"):
				src, _ := os.ReadFile(bluray("src/b.wav"))
				samples := src[bytes.Index(src, []byte("data"))+8:]
				if string(got[:4]) != "RIFF" || binary.LittleEndian.Uint16(got[22:]) != 2 ||
					binary.LittleEndian.Uint32(got[24:]) != 48000 || binary.LittleEndian.Uint16(got[34:]) != 16 {
					t.Errorf("%s: WAV header % x", form, got[:44])
				}
				data := got[68:]
				if int(binary.LittleEndian.Uint32(got[64:])) != len(data) || len(data) == 0 ||
					!bytes.HasPrefix(samples, data) {
					t.Errorf("%s: WAV samples are not the source's (%d bytes)", form, len(data))
				}
			default:
				t.Errorf("unexpected track file %s", e.path)
			}
		}
	}
}

// The remux copies packets untouched and rewrites only the tables: the
// result decodes to exactly the same pictures, lists only the kept tracks
// (base view, dependent view, then the rest), and says their languages.
func TestBuiltinRemux(t *testing.T) {
	dir := fixtureDir(t)
	want := decodeSource(t, mvc.Source{Format: mvc.FormatAnnexB, R: bytes.NewReader(readFixture(t, dir, "mvc_combined.264"))}, false)
	for _, form := range bothForms {
		out := filepath.Join(t.TempDir(), "remux.m2ts")
		o := DefaultOptions()
		o.Input, o.Output, o.Remux = bluray(form), out, true
		o.Audio = TrackFilter{Langs: []string{"deu"}}
		r := NewRunner(CurrentGOOS, o, nil)
		r.tool = func(string) (string, error) { return "", os.ErrNotExist } // needs no tools
		if err := r.Run(context.Background()); err != nil {
			t.Fatalf("%s: %v", form, err)
		}
		f, err := os.Open(out)
		if err != nil {
			t.Fatal(err)
		}
		rd := m2ts.NewReader(f)
		prog, err := rd.ReadProgram()
		if err != nil {
			t.Fatal(err)
		}
		var pids []uint16
		for _, s := range prog.Streams {
			pids = append(pids, s.PID)
		}
		if len(pids) != 3 || pids[0] != 0x1011 || pids[1] != 0x1012 || pids[2] != 0x1102 || prog.Streams[2].Lang != "deu" {
			t.Errorf("%s: program %+v", form, prog.Streams)
		}
		seen := map[uint16]bool{}
		for {
			p, err := rd.Next()
			if err != nil {
				break
			}
			seen[p.PID] = true
		}
		_ = f.Close()
		if seen[0x1100] || seen[0x1101] || !seen[0x1102] {
			t.Errorf("%s: PIDs carried %v", form, seen)
		}
		in, _ := os.Open(out)
		got := decodeSource(t, mvc.Source{Format: mvc.FormatM2TS, R: in}, false)
		_ = in.Close()
		if !bytes.Equal(got, want) {
			t.Errorf("%s: the remux decodes differently (%d vs %d bytes)", form, len(got), len(want))
		}
	}
}

// Pictures outside a clip's IN/OUT window are decoded but not output, and a
// frame is placed in its clip by the tag on its timestamp.
func TestKeepFrameWindow(t *testing.T) {
	g := &goDemux{ins: []int64{1000, 50}, outs: []int64{2000, 400}}
	for _, c := range []struct {
		clip int
		pts  int64
		keep bool
	}{{0, 999, false}, {0, 1000, true}, {0, 1999, true}, {0, 2000, false}, {1, 50, true}, {1, 1000, false}, {1, 49, false}} {
		g.clip = c.clip
		if got := g.KeepFrame(g.tag(c.pts)); got != c.keep {
			t.Errorf("clip %d pts %d: keep %v, want %v", c.clip, c.pts, got, c.keep)
		}
	}
	if !g.KeepFrame(-1) {
		t.Error("a picture without a timestamp is kept")
	}
}

func TestBetterTitlePrefersContentThenChapters(t *testing.T) {
	item := func(clip string, secs uint32) bdmv.PlayItem {
		return bdmv.PlayItem{Clip: clip, InTime: 45000, OutTime: 45000 + secs*45000}
	}
	loop := cand{"00020.mpls", &bdmv.Playlist{}}
	for i := 0; i < 152; i++ {
		loop.pl.Items = append(loop.pl.Items, item("00240", 80))
	}
	bare := cand{"00001.mpls", &bdmv.Playlist{Items: []bdmv.PlayItem{item("00272", 5283)}}}
	withChapters := cand{"00800.mpls", &bdmv.Playlist{Items: []bdmv.PlayItem{item("00272", 5283)},
		Marks: []bdmv.Mark{{Type: 1, Time: 45000}, {Type: 1, Time: 45000 * 600}}}}
	if betterTitle(loop, bare) || !betterTitle(bare, loop) {
		t.Error("a 152-item loop of one clip must lose to the feature")
	}
	if !betterTitle(withChapters, bare) || betterTitle(bare, withChapters) {
		t.Error("between equal features, the one with chapters wins")
	}
}

// Blu-ray LPCM is big-endian, pads an odd channel count, and stores 7.1's
// back pair before its side pair; the WAV is little-endian and in WAVE
// order.
func TestWAVWriterReordersAndSwapsBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.wav")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := newWAVWriter(f)
	// 7.1 (assignment 11), 48 kHz, 16 bits: one frame of eight samples
	// numbered by their position on the disc.
	payload := []byte{0, 0, 11<<4 | 1, 1 << 6}
	for ch := 0; ch < 8; ch++ {
		payload = append(payload, 0x10, byte(ch))
	}
	if err := w.write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.close(); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	b, _ := os.ReadFile(path)
	if binary.LittleEndian.Uint16(b[22:]) != 8 || binary.LittleEndian.Uint32(b[40:]) != 0x63f {
		t.Errorf("header: %d channels, mask %#x", binary.LittleEndian.Uint16(b[22:]), binary.LittleEndian.Uint32(b[40:]))
	}
	data := b[68:]
	var order []byte
	for i := 0; i+1 < len(data); i += 2 {
		if data[i+1] != 0x10 {
			t.Fatalf("sample %d not byte-swapped: % x", i/2, data[i:i+2])
		}
		order = append(order, data[i])
	}
	if !bytes.Equal(order, []byte{0, 1, 2, 3, 6, 7, 4, 5}) {
		t.Errorf("channel order %v", order)
	}
}

// A PES of presentation graphics can hold several segments; each gets its
// own "PG" header, on the output's timeline.
func TestPGSSegmentsGetHeaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.sup")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	g := &goDemux{ins: []int64{90000}, outs: []int64{1 << 40}, offsets: []int64{0}}
	w := &esWriter{track: Track{StreamID: "S_HDMV/PGS"}, f: f, w: f}
	seg := func(typ byte, body ...byte) []byte { return append([]byte{typ, 0, byte(len(body))}, body...) }
	payload := append(seg(0x16, 1, 2, 3), seg(0x80)...)
	if err := g.emitES(w, m2ts.PES{PTS: 90000 + 4500, DTS: -1, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	b, _ := os.ReadFile(path)
	want := []byte{'P', 'G', 0, 0, 0x11, 0x94, 0, 0, 0x11, 0x94, 0x16, 0, 3, 1, 2, 3,
		'P', 'G', 0, 0, 0x11, 0x94, 0, 0, 0x11, 0x94, 0x80, 0, 0}
	if !bytes.Equal(b, want) {
		t.Errorf("got  % x\nwant % x", b, want)
	}
}

// A segment longer than its PES packet holds (a full-screen graphic's
// object data) continues in the next packet: it is written whole, once,
// with the times of the packet it began in, and the segments after it are
// not misread.
func TestPGSSegmentSplitAcrossPackets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.sup")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	g := &goDemux{ins: []int64{0}, outs: []int64{1 << 40}, offsets: []int64{0}}
	w := &esWriter{track: Track{StreamID: "S_HDMV/PGS"}, f: f, w: f}
	big := bytes.Repeat([]byte{7}, 65519)
	ods := append([]byte{0x15, 0xff, 0xef}, big...)
	end := []byte{0x80, 0, 0}
	for _, p := range []m2ts.PES{
		{PTS: 900, DTS: -1, Payload: ods[:65522-20]},                // cut 20 bytes short
		{PTS: -1, DTS: -1, Payload: append(ods[65522-20:], end...)}, // the rest, then the end
		{PTS: 1800, DTS: -1, Payload: end[:1]},                      // a header cut short
		{PTS: -1, DTS: -1, Payload: end[1:]},
	} {
		if err := g.emitES(w, p); err != nil {
			t.Fatal(err)
		}
	}
	_ = f.Close()
	b, _ := os.ReadFile(path) //nolint:gosec // test
	var types []byte
	var pts []uint32
	for len(b) >= 13 {
		if b[0] != 'P' || b[1] != 'G' {
			t.Fatalf("not a segment header: % x", b[:13])
		}
		n := int(b[11])<<8 | int(b[12])
		types, pts = append(types, b[10]), append(pts, binary.BigEndian.Uint32(b[2:]))
		if b[10] == 0x15 && !bytes.Equal(b[13:13+n], big) {
			t.Error("the object data changed")
		}
		b = b[13+n:]
	}
	if fmt.Sprintf("% x %v", types, pts) != "15 80 80 [900 900 1800]" || len(b) != 0 {
		t.Errorf("segments % x at %v, %d bytes over", types, pts, len(b))
	}
}

// The built-in plan names no demuxing tool and reads the input in place.
func TestBuiltinPlan(t *testing.T) {
	o := DefaultOptions()
	o.Input, o.Output, o.TempDir = "/in/disc.iso", "/out/a.mkv", "/tmp/w"
	o.Encoder = EncoderSoftware
	p, err := BuildPlan("linux", o)
	if err != nil {
		t.Fatal(err)
	}
	s := p.String()
	if strings.Contains(s, "tsMuxeR") || !strings.Contains(s, "built in") || !strings.Contains(s, "/in/disc.iso") {
		t.Errorf("plan:\n%s", s)
	}
	if names := RequiredFor("linux", o); len(names) != 1 || names[0].Name != "x264" {
		t.Errorf("only the encoder is needed, got %v", names)
	}
	if s := mustPlan(t, o).String(); !strings.Contains(s, "write /out/a.mkv") {
		t.Errorf("the mux is built in:\n%s", s)
	}
	o.Remux, o.Output = true, "/out/a.m2ts"
	if len(RequiredFor("linux", o)) != 0 {
		t.Error("a remux needs no tools")
	}
}

func mustPlan(t *testing.T, o Options) *Plan {
	t.Helper()
	p, err := BuildPlan("linux", o)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// With the GPU's library working in process, auto picks it before trying
// ffmpeg, and the conversion then needs no encoder program.
func TestNativeGPUIsPreferred(t *testing.T) {
	origNative, origLook := ProbeNative, LookPath
	t.Cleanup(func() { ProbeNative, LookPath = origNative, origLook })
	ProbeNative = func(e Encoder, c Codec, _ int, _ string) bool { return e == EncoderVAAPI && c == CodecH265 }
	LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if got := DefaultEncoder(context.Background(), "linux", CodecH265, 8, ""); got != EncoderVAAPI {
		t.Errorf("auto = %s, want vaapi", got)
	}
	if got := DefaultEncoder(context.Background(), "linux", CodecH264, 8, ""); got != EncoderSoftware {
		t.Errorf("auto for h264 = %s, want software", got)
	}
	o := DefaultOptions()
	o.Input, o.Output, o.Encoder, o.Codec = "/in/a.iso", "/out/a.mkv", EncoderVAAPI, CodecH265
	ResolveGPU(&o)
	if !o.NativeGPU || len(RequiredFor("linux", o)) != 0 {
		t.Errorf("native %v, required %v", o.NativeGPU, RequiredFor("linux", o))
	}
	if s := mustPlan(t, o).String(); strings.Contains(s, "ffmpeg") || !strings.Contains(s, "vaapi h265 qp") {
		t.Errorf("plan:\n%s", s)
	}
	o.GPUAPI = GPUFFmpeg
	ResolveGPU(&o)
	if o.NativeGPU {
		t.Error("--gpu-api ffmpeg must not run the encoder in process")
	}
}

// Half-SBS squeezes each view to half width; a flat view stays flat and a
// left/right split lands in the right halves.
func TestDrawSBS(t *testing.T) {
	mk := func(v byte) *mvc.Frame {
		f := &mvc.Frame{Width: 8, Height: 4, StrideY: 8, StrideC: 4}
		f.Y, f.Cb, f.Cr = bytes.Repeat([]byte{v}, 32), bytes.Repeat([]byte{v + 1}, 8), bytes.Repeat([]byte{v + 2}, 8)
		return f
	}
	sf := &mvc.StereoFrame{Base: mk(10), Dependent: mk(200)}
	for _, half := range []bool{false, true} {
		w := 16
		if half {
			w = 8
		}
		p := &hwenc.Picture{Y: make([]byte, 32*4), UV: make([]byte, 32*2), Pitch: 32}
		drawSBS(p, sf, false, half)
		for y := 0; y < 4; y++ {
			for x := 0; x < w; x++ {
				want := byte(10)
				if x >= w/2 {
					want = 200
				}
				if p.Y[y*32+x] != want {
					t.Fatalf("half=%v: Y(%d,%d) = %d, want %d", half, x, y, p.Y[y*32+x], want)
				}
			}
		}
		for x := 0; x < w; x += 2 {
			wantU, wantV := byte(11), byte(12)
			if x >= w/2 {
				wantU, wantV = 201, 202
			}
			if p.UV[x] != wantU || p.UV[x+1] != wantV {
				t.Fatalf("half=%v: UV at %d = %d,%d", half, x, p.UV[x], p.UV[x+1])
			}
		}
		drawSBS(p, sf, true, half)
		if p.Y[0] != 200 || p.Y[w-1] != 10 {
			t.Errorf("swap: %d .. %d", p.Y[0], p.Y[w-1])
		}
	}
}

// VideoToolbox's quality runs the other way from --crf: a lower --crf must
// ask ffmpeg for a higher -q:v.
func TestVideoToolboxQualityFollowsCRF(t *testing.T) {
	o := DefaultOptions()
	o.Input, o.Output, o.Encoder, o.GPUAPI = "/in/a.iso", "/out/a.mkv", EncoderVideoToolbox, GPUFFmpeg
	q := func(crf int) string {
		o.CRF = crf
		p, err := BuildPlan("darwin", o)
		if err != nil {
			t.Fatal(err)
		}
		s := p.String()
		i := strings.Index(s, "-q:v ")
		if i < 0 {
			t.Fatalf("no -q:v in:\n%s", s)
		}
		return strings.Fields(s[i:])[1]
	}
	if got := q(18); got != "65" {
		t.Errorf("--crf 18: -q:v %s, want 65", got)
	}
	if got := q(0); got != "100" {
		t.Errorf("--crf 0: -q:v %s, want 100", got)
	}
}

// With the source's length known the progress line says how far along the
// conversion is and how long it has left; without it, just the count.
func TestProgressLine(t *testing.T) {
	var lines []string
	r := NewRunner(CurrentGOOS, DefaultOptions(), func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) })
	r.length = 2*time.Hour + 14*time.Minute + 32*time.Second
	p := r.newProgress("encoded")
	p.begin(24000, 1001, 0)
	if len(lines) != 1 || lines[0] != "about 193534 frames to encode (2h14m32s at 23.976 fps)" {
		t.Errorf("announced %q", lines)
	}
	at := p.started.Add(1000 * time.Second)
	if got, want := p.line(97100, at), "97100 of 193534 frames encoded (50.2%), 97.1 fps, 16m33s left"; got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	// Past the estimate (the length was a little short): no percentage.
	if got := p.line(200000, p.started.Add(2000*time.Second)); got != "200000 frames encoded (100.0 fps)" {
		t.Errorf("past the end: %q", got)
	}
	// Resumed, the count and the time left are for what is left.
	lines = nil
	p = r.newProgress("encoded")
	p.begin(24000, 1001, 93534)
	if len(lines) != 1 || lines[0] != "about 100000 of 193534 frames left to encode (2h14m32s at 23.976 fps)" {
		t.Errorf("resumed: announced %q", lines)
	}
	if got, want := p.line(50000, p.started.Add(500*time.Second)), "50000 of 100000 frames encoded (50.0%), 100.0 fps, 8m20s left"; got != want {
		t.Errorf("resumed: got %q, want %q", got, want)
	}
	r.length = 0
	q := r.newProgress("decoded")
	q.begin(24000, 1001, 0)
	if got := q.line(50, q.started.Add(time.Second)); got != "50 frames decoded (50.0 fps)" {
		t.Errorf("no length: %q", got)
	}
	// Reports come every progressEvery, not every frame.
	lines = nil
	clock := q.started
	q.now = func() time.Time { return clock }
	for i := 1; i <= 100; i++ {
		clock = clock.Add(time.Second)
		q.frame(i)
	}
	if len(lines) != 3 {
		t.Errorf("%d reports over 100 s", len(lines))
	}
}

// Sync points are kept sparse: the first, one a second, and every jump.
func TestSyncPointSampling(t *testing.T) {
	w := &esWriter{}
	at := time.Duration(0)
	for i := 0; i < 3000; i++ { // 3 s of 1 ms payloads
		w.mark(at, nil)
		w.n += 100
		at += time.Millisecond
	}
	w.mark(at+500*time.Millisecond, nil) // a gap
	w.n += 100
	w.mark(at+501*time.Millisecond, []byte{0x0b, 0x77}) // the first AC-3 frame
	w.n += 100
	w.mark(at+502*time.Millisecond, []byte{0x0b, 0x77}) // and the next: nothing
	var got []time.Duration
	for _, s := range w.sync {
		got = append(got, s.At)
	}
	want := []time.Duration{0, time.Second, 2 * time.Second, 3500 * time.Millisecond, 3501 * time.Millisecond}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("sync points at %v, want %v", got, want)
	}
	if w.sync[3].Offset != 300000 {
		t.Errorf("the gap's point is at offset %d", w.sync[3].Offset)
	}
}

// The first kept picture's place on the output's timeline becomes the
// video's delay: a disc whose picture starts after its sound keeps that gap.
func TestFirstPictureDelay(t *testing.T) {
	g := &goDemux{ins: []int64{552600, 900000}, outs: []int64{1000000000, 1000000000}, offsets: []int64{0, 3600 * 90000}}
	r := NewRunner(CurrentGOOS, DefaultOptions(), nil)
	r.timeline = g.timeline
	r.noteFirstPicture(639450) // 0.965 s after IN, in clip 0
	if r.videoDelay != 965*time.Millisecond {
		t.Errorf("delay %v", r.videoDelay)
	}
	r.videoDelay = 0
	r.noteFirstPicture(552600) // at IN
	if r.videoDelay != 0 {
		t.Errorf("a picture at IN delays by %v", r.videoDelay)
	}
	if got := g.timeline(int64(1)<<ptsTagShift | 900000 + 90000); got != time.Hour+time.Second {
		t.Errorf("second clip: %v", got)
	}
}

// The name says what the file is: layout, resolution, codec and quality,
// encoder, and the main audio track.
func TestDetailTags(t *testing.T) {
	o := DefaultOptions()
	o.Codec, o.CRF, o.Encoder = CodecH265, 20, EncoderNVENC
	thd := Track{Type: "TRUE-HD", StreamID: "A_AC3", Info: "AC3 core + TRUE-HD + ATMOS. Sample Rate: 48KHz Channels: 7.1"}
	got := DetailTags(o, []Track{thd}, 1080)
	if got != "3D FSBS 1080p HEVC QP20 NVENC TrueHD-Atmos 7.1" {
		t.Errorf("tags %q", got)
	}
	o.Codec, o.CRF, o.Encoder, o.Layout = CodecH264, 18, EncoderSoftware, LayoutHalfSBS
	ma := Track{Type: "DTS-HD Master Audio", StreamID: "A_DTS", Info: "Sample Rate: 48KHz Channels: 5.1"}
	if got := DetailTags(o, []Track{ma}, 1080); got != "3D HSBS 1080p H264 CRF18 x264 DTS-HD-MA 5.1" {
		t.Errorf("tags %q", got)
	}
	o.Codec, o.CRF, o.Encoder, o.Layout = CodecAV1, 20, EncoderSoftware, LayoutFullSBS
	if got := DetailTags(o, nil, 1080); got != "3D FSBS 1080p AV1 CRF20 SVT-AV1" {
		t.Errorf("AV1 tags %q", got)
	}
	for in, want := range map[string]string{
		"/out/Moana (2016).mkv":         "/out/Moana (2016) 3D FSBS 1080p.mkv",
		"/out/Moana (2016) 3D FSBS.mkv": "/out/Moana (2016) 3D FSBS 1080p.mkv",
	} {
		if got := WithDetails(in, "3D FSBS 1080p"); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}

// --playlist picks a disc's title by name, with or without the extension.
func TestPlaylistOption(t *testing.T) {
	for _, form := range bothForms {
		for _, name := range []string{"00000", "00000.mpls", "00000.MPLS"} {
			src, err := resolveGo(bluray(form), name, nil)
			if err != nil || src.playlist == nil {
				t.Fatalf("%s %s: %v", form, name, err)
			}
		}
		if _, err := resolveGo(bluray(form), "09999", nil); err == nil {
			t.Errorf("%s: a missing playlist must fail", form)
		}
	}
}
