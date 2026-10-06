package convert

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brunoga/mvc"
	"github.com/brunoga/mvc/internal/mkv"
)

// frameSource is an mkv.Source over prepared frames.
type frameSource struct {
	track  mkv.Track
	frames []mkv.Frame
}

func (s *frameSource) Track() mkv.Track { return s.track }

func (s *frameSource) Next() (mkv.Frame, error) {
	if len(s.frames) == 0 {
		return mkv.Frame{}, io.EOF
	}
	f := s.frames[0]
	s.frames = s.frames[1:]
	return f, nil
}

// nals splits Annex B into NAL units.
func nals(b []byte) [][]byte {
	var out [][]byte
	start := -1
	for i := 0; i+2 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			if start >= 0 {
				end := i
				if end > start && b[end-1] == 0 {
					end--
				}
				out = append(out, b[start:end])
			}
			start = i + 3
			i += 2
		}
	}
	if start >= 0 && start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

// mvcMatroska writes an MKV the way a Blu-ray 3D remux is made: the MVC
// stream's access units whole (both views) in one AVC track, the AC-3
// fixture as a named audio track, and a forced PGS track.
func mvcMatroska(t *testing.T) string {
	t.Helper()
	ar := mvc.NewAUReader(bytes.NewReader(readFixture(t, fixtureDir(t), "mvc_combined.264")))
	var video []mkv.Frame
	var sps, pps []byte
	for {
		au, err := ar.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		var data []byte
		key := false
		for _, n := range nals(au) {
			switch n[0] & 0x1f {
			case 7:
				if sps == nil {
					sps = n
				}
			case 8:
				if pps == nil {
					pps = n
				}
			case 5:
				key = true
			}
			data = binary.BigEndian.AppendUint32(data, uint32(len(n))) //nolint:gosec // small
			data = append(data, n...)
		}
		if !hasSlice(au) && len(video) > 0 {
			// The dependent view's unit: it joins its base view's block.
			video[len(video)-1].Data = append(video[len(video)-1].Data, data...)
			continue
		}
		at := time.Duration(len(video)) * time.Second * 1001 / 24000
		video = append(video, mkv.Frame{PTS: at, Order: at, Keyframe: key, Data: data})
	}
	avcC := []byte{1, sps[1], sps[2], sps[3], 0xff, 0xe1}
	avcC = binary.BigEndian.AppendUint16(avcC, uint16(len(sps))) //nolint:gosec // small
	avcC = append(avcC, sps...)
	avcC = append(avcC, 1)
	avcC = binary.BigEndian.AppendUint16(avcC, uint16(len(pps))) //nolint:gosec // small
	avcC = append(avcC, pps...)

	af, err := os.Open(bluray(filepath.Join("src", "a.ac3")))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = af.Close() }()
	audio, err := mkv.NewAudioSource(af, mkv.AC3, false, "eng")
	if err != nil {
		t.Fatal(err)
	}
	audio.SetName("Surround")
	// A presentation composition segment and an end segment.
	pgs := []byte{0x16, 0, 3, 1, 2, 3, 0x80, 0, 0}
	subs := &frameSource{track: mkv.Track{Type: mkv.TypeSubtitle, CodecID: "S_HDMV/PGS", Language: "eng", Name: "Signs", Forced: true},
		frames: []mkv.Frame{{PTS: 100 * time.Millisecond, Order: 100 * time.Millisecond, Keyframe: true, Data: pgs}}}

	path := filepath.Join(t.TempDir(), "film 3D.mkv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	src := &frameSource{track: mkv.Track{Type: mkv.TypeVideo, CodecID: "V_MPEG4/ISO/AVC", CodecPrivate: avcC,
		Width: 640, Height: 480, StereoMode: 13, Default: true}, frames: video}
	if err := mkv.Mux(f, []mkv.Source{src, audio, subs}, mkv.Options{
		Chapters: []mkv.Chapter{{Start: 0, Name: "One"}, {Start: 200 * time.Millisecond, Name: "Two"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// A Matroska remux of a 3D disc lists its one video track as both views,
// with the audio and subtitles described, named and flagged.
func TestMatroskaProbe(t *testing.T) {
	src, tracks, err := probeMatroska(t.Context(), mvcMatroska(t))
	if err != nil {
		t.Fatal(err)
	}
	sel, err := SelectTracks(tracks)
	if err != nil {
		t.Fatal(err)
	}
	if sel.Base.ID != 1 || sel.Dependent.ID != 1 || len(sel.Audio) != 1 || len(sel.Subtitles) != 1 {
		t.Fatalf("selection %+v", sel)
	}
	a := sel.Audio[0]
	if a.Type != "AC3" || a.Lang != "eng" || a.Name != "Surround" || channelCount(a) == 0 {
		t.Errorf("audio %+v", a)
	}
	if s := sel.Subtitles[0]; !s.Forced || s.Name != "Signs" {
		t.Errorf("subtitles %+v", s)
	}
	if len(src.chapters) != 2 || src.chapters[1] != 200*time.Millisecond {
		t.Errorf("chapters %v", src.chapters)
	}
	if sel.Dependent.Info == "" {
		t.Error("the dependent view is not described")
	}
}

// The pictures decode exactly as the stream the file was made from, the
// audio comes out as the AC-3 that went in, and the subtitles as a .sup.
func TestMatroskaDemux(t *testing.T) {
	path := mvcMatroska(t)
	src, tracks, err := probeMatroska(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	sel, err := SelectTracks(tracks)
	if err != nil {
		t.Fatal(err)
	}
	g := newMkvDemux(src, sel, t.TempDir(), nil)
	if err := g.start(); err != nil {
		t.Fatal(err)
	}
	got := decodeSource(t, mvc.Source{Format: mvc.FormatAccessUnits, AccessUnits: g.Next}, false)
	extras, err := g.finish()
	if err != nil {
		t.Fatal(err)
	}
	want := decodeSource(t, mvc.Source{Format: mvc.FormatAnnexB, R: bytes.NewReader(readFixture(t, fixtureDir(t), "mvc_combined.264"))}, false)
	if !bytes.Equal(got, want) {
		t.Errorf("%d bytes of Y4M differ from the combined stream's %d", len(got), len(want))
	}
	if g.noDep != 0 || g.aus == 0 {
		t.Errorf("%d of %d pictures without a dependent view", g.noDep, g.aus)
	}
	if len(extras) != 2 {
		t.Fatalf("extras %+v", extras)
	}
	ac3, _ := os.ReadFile(bluray(filepath.Join("src", "a.ac3")))
	gotAC3, _ := os.ReadFile(extras[0].path)
	if !bytes.Equal(gotAC3, ac3) {
		t.Errorf("the AC-3 track is %d bytes, the source %d", len(gotAC3), len(ac3))
	}
	sup, _ := os.ReadFile(extras[1].path)
	// "PG", PTS and DTS at 100 ms (9000 ticks), then each segment.
	wantSup := []byte{'P', 'G', 0, 0, 0x23, 0x28, 0, 0, 0x23, 0x28, 0x16, 0, 3, 1, 2, 3,
		'P', 'G', 0, 0, 0x23, 0x28, 0, 0, 0x23, 0x28, 0x80, 0, 0}
	if !bytes.Equal(sup, wantSup) {
		t.Errorf(".sup % x", sup)
	}
}

// The whole conversion of a Matroska source, mux included, keeps the
// track names and the forced flag.
func TestMatroskaMuxKeepsNames(t *testing.T) {
	path := mvcMatroska(t)
	src, tracks, err := probeMatroska(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	sel, _ := SelectTracks(tracks)
	tmp := t.TempDir()
	g := newMkvDemux(src, sel, tmp, nil)
	if err := g.start(); err != nil {
		t.Fatal(err)
	}
	for {
		if _, _, _, err := g.Next(); err != nil {
			break
		}
	}
	extras, err := g.finish()
	if err != nil {
		t.Fatal(err)
	}
	// The video does not matter here: the source's own stream will do.
	video := filepath.Join(tmp, "v.264")
	if err := os.WriteFile(video, readFixture(t, fixtureDir(t), "mvc_base.264"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := DefaultOptions()
	o.Output = filepath.Join(tmp, "out.mkv")
	r := NewRunner(CurrentGOOS, o, nil)
	if err := r.muxBuiltin(t.Context(), video, extras, src.chapters); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(o.Output)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	rd, err := mkv.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(rd.Tracks) != 3 || rd.Tracks[1].Name != "Surround" || rd.Tracks[2].Name != "Signs" || !rd.Tracks[2].Forced || rd.Tracks[1].Forced {
		t.Errorf("tracks %+v", rd.Tracks)
	}
}

// --remux takes discs: a Matroska file is already one.
func TestMatroskaRemuxIsRefused(t *testing.T) {
	o := DefaultOptions()
	o.Input, o.Output, o.Remux = mvcMatroska(t), filepath.Join(t.TempDir(), "x.m2ts"), true
	r := NewRunner(CurrentGOOS, o, nil)
	r.tool = func(string) (string, error) { return "", os.ErrNotExist }
	if err := r.Run(t.Context()); err == nil {
		t.Error("remuxing a Matroska source must fail")
	}
}

// --keep-fallback on a remux keeps the lossy track that stands in for the
// TrueHD core, in the same language, and nothing for DTS-HD, whose core is
// inside it.
func TestMatroskaFallbackTrack(t *testing.T) {
	thd := Track{ID: 2, Type: "TRUE-HD", StreamID: "A_AC3", Lang: "eng", Info: "AC3 core + TRUE-HD + ATMOS. Channels: 7.1"}
	compat := Track{ID: 3, Type: "E-AC3 (DD+)", StreamID: "A_AC3", Lang: "eng", Info: "EAC3 Bitrate: 1024Kbps Channels: 5.1"}
	stereo := Track{ID: 4, Type: "AC3", StreamID: "A_AC3", Lang: "eng", Info: "Bitrate: 192Kbps Channels: 2.0"}
	french := Track{ID: 5, Type: "AC3", StreamID: "A_AC3", Lang: "fre", Info: "Bitrate: 640Kbps Channels: 5.1"}
	ma := Track{ID: 6, Type: "DTS-HD Master Audio", StreamID: "A_DTS", Lang: "spa", Info: "Channels: 7.1"}
	all := []Track{thd, compat, stereo, french, ma}
	got := withFallbacks([]Track{thd}, all, nil)
	if len(got) != 2 || got[1].ID != 3 {
		t.Errorf("TrueHD fallback: %+v", got)
	}
	if got := withFallbacks([]Track{ma}, all, nil); len(got) != 1 {
		t.Errorf("DTS-HD needs none: %+v", got)
	}
	if got := withFallbacks([]Track{compat}, all, nil); len(got) != 1 {
		t.Errorf("a lossy track needs none: %+v", got)
	}
}
