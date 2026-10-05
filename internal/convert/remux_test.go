package convert

import (
	"context"
	"strings"
	"testing"
)

func remuxSelection() Selection {
	return Selection{
		Base:      Track{ID: 4113, StreamID: "V_MPEG4/ISO/AVC", Type: "H.264"},
		Dependent: Track{ID: 4114, StreamID: "V_MPEG4/ISO/MVC", Type: "MVC"},
		Audio:     []Track{{ID: 4352, StreamID: "A_TRUEHD", Type: "TRUE-HD", Lang: "eng"}},
		Subtitles: []Track{{ID: 4608, StreamID: "S_HDMV/PGS", Type: "PGS", Lang: "eng"}},
	}
}

// The remux meta is the demux meta without --demux: the same track references,
// so the disc's own video passes through rather than being decoded.
func TestRemuxMetaKeepsTheDiscsStreams(t *testing.T) {
	got := RemuxMeta("/w/00001.mpls", remuxSelection())
	for _, want := range []string{
		`V_MPEG4/ISO/AVC, "/w/00001.mpls", track=4113`,
		`V_MPEG4/ISO/MVC, "/w/00001.mpls", track=4114`,
		`A_TRUEHD, "/w/00001.mpls", track=4352, lang=eng`,
		`S_HDMV/PGS, "/w/00001.mpls", track=4608, lang=eng`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("meta missing %q, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "--demux") {
		t.Error("a remux meta must not ask for a demux")
	}
}

// insertSEI and contSPS rebuild picture timing and repeat parameter sets so an
// extracted elementary stream can stand alone. A remux leaves the stream in a
// container that carries them, and asking for them would mean rewriting video
// that is meant to pass through untouched.
func TestRemuxMetaDoesNotRewriteTheVideo(t *testing.T) {
	got := RemuxMeta("/w/src.mpls", remuxSelection())
	for _, unwanted := range []string{"insertSEI", "contSPS"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("remux meta asks for %q, which rewrites the stream", unwanted)
		}
	}
	// The demux path still needs them, so this must not have removed them there.
	if d := DemuxMeta("/w/src.mpls", remuxSelection()); !strings.Contains(d, "insertSEI") {
		t.Error("the demux meta lost insertSEI, which an extracted stream needs")
	}
}

// A player strict about Blu-ray stream structure can refuse a stream muxed
// without the conventions a disc uses.
func TestRemuxMetaUsesBluRayMuxingConventions(t *testing.T) {
	got := RemuxMeta("/w/src.mpls", remuxSelection())
	for _, want := range []string{"--no-pcr-on-video-pid", "--new-audio-pes", "--vbr", "--vbv-len=500"} {
		if !strings.Contains(got, want) {
			t.Errorf("meta missing %q", want)
		}
	}
}

// Only the tracks that survived the filters are written, which is where the
// space saving comes from.
func TestRemuxMetaWritesOnlyTheSelectedTracks(t *testing.T) {
	sel := remuxSelection()
	got := RemuxMeta("/w/src.mpls", sel)
	if n := strings.Count(got, "track="); n != 4 {
		t.Errorf("meta references %d tracks, want 4 (both views, one audio, one subtitle):\n%s", n, got)
	}
}

// A remux is one step: nothing is decoded, stacked or encoded.
func TestRemuxPlanIsASingleStep(t *testing.T) {
	o := opts("linux", func(o *Options) {
		o.Remux = true
		o.Output = "/media/out/Film.m2ts"
	})
	p, err := BuildPlan("linux", o)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(p.Steps) != 1 || p.Steps[0].Name != "remux" {
		t.Fatalf("plan has %d steps (%v), want one remux", len(p.Steps), p.Steps)
	}
	joined := strings.Join(p.Steps[0].Argv, " ")
	if !strings.Contains(joined, "tsMuxeR") || !strings.Contains(joined, "/media/out/Film.m2ts") {
		t.Errorf("remux step = %q", joined)
	}
	for _, s := range p.Steps {
		if s.Name == "decode" || s.Name == "encode" {
			t.Errorf("a remux must not %s", s.Name)
		}
	}
}

// MVC has no home in Matroska that players agree on, so a .mkv output would be
// a file nothing could play. Saying so beats producing one.
func TestRemuxRefusesAnMkvOutput(t *testing.T) {
	_, err := BuildPlan("linux", opts("linux", func(o *Options) {
		o.Remux = true
		o.Output = "/media/out/Film.mkv"
	}))
	if err == nil {
		t.Fatal("a remux to .mkv must be refused")
	}
	if !strings.Contains(err.Error(), ".m2ts") {
		t.Errorf("the error should name the format to use, got: %v", err)
	}
}

func TestRemuxAcceptsTsAndM2ts(t *testing.T) {
	for _, ext := range []string{".m2ts", ".ts"} {
		_, err := BuildPlan("linux", opts("linux", func(o *Options) {
			o.Remux = true
			o.Output = "/media/out/Film" + ext
		}))
		if err != nil {
			t.Errorf("%s output refused: %v", ext, err)
		}
	}
}

// Settings that describe the decode-and-encode path cannot apply to a copy of
// the disc's video. Refusing them beats ignoring them, which would hand back a
// file that quietly is not what was asked for.
func TestRemuxRefusesSettingsItCannotHonour(t *testing.T) {
	for _, c := range []struct {
		name string
		mut  func(*Options)
		says string
	}{
		{"half-SBS", func(o *Options) { o.Layout = LayoutHalfSBS }, "layout"},
		{"eye swap", func(o *Options) { o.SwapLR = true }, "swap"},
	} {
		_, err := BuildPlan("linux", opts("linux", func(o *Options) {
			o.Remux = true
			o.Output = "/media/out/Film.m2ts"
			c.mut(o)
		}))
		if err == nil {
			t.Errorf("%s should be refused with --remux", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: error should explain itself, got: %v", c.name, err)
		}
	}
}

// --- keep-fallback ---

// The core is dropped by default because the disc never listed it. Asked for,
// it is kept — and tagged, because an unidentified track beside the one a
// player was told about is the defect this path exists to avoid, whether it is
// there by accident or on purpose.
func TestKeepFallbackKeepsAndTagsTheCore(t *testing.T) {
	path := "/w/00001.track_4352_eng.ac3+thd"
	stubIdentify(t, map[string]string{path: truehdWithCore})

	r := &Runner{Opts: Options{KeepFallback: true}}
	argv, dropped := r.extraArgs(context.Background(), "mkvmerge", []extra{
		{path: path, track: Track{ID: 4352, StreamID: "A_TRUEHD", Type: "TRUE-HD", Lang: "eng"}},
	})
	joined := strings.Join(argv, " ")
	if strings.Contains(joined, "--audio-tracks") {
		t.Errorf("the core was still filtered out: %q", joined)
	}
	// Both the primary and the core carry the language.
	for _, want := range []string{"--language 0:eng", "--language 1:eng"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q: %q", want, joined)
		}
	}
	if len(dropped) != 0 {
		t.Errorf("reported dropping %+v while keeping the fallback", dropped)
	}
}

// Without the flag the behaviour is unchanged: the core goes.
func TestDefaultStillDropsTheCore(t *testing.T) {
	path := "/w/a.ac3+thd"
	stubIdentify(t, map[string]string{path: truehdWithCore})
	r := &Runner{Opts: Options{}}
	argv, dropped := r.extraArgs(context.Background(), "mkvmerge", []extra{
		{path: path, track: Track{Type: "TRUE-HD", Lang: "eng"}},
	})
	if !strings.Contains(strings.Join(argv, " "), "--audio-tracks 0") {
		t.Errorf("argv = %v, want the core filtered out by default", argv)
	}
	if len(dropped) != 1 {
		t.Errorf("dropped = %+v, want the core reported", dropped)
	}
}

// An untagged track with the flag set needs no language options at all.
func TestKeepFallbackWithNoLanguage(t *testing.T) {
	path := "/w/a.ac3+thd"
	stubIdentify(t, map[string]string{path: truehdWithCore})
	r := &Runner{Opts: Options{KeepFallback: true}}
	argv, _ := r.extraArgs(context.Background(), "mkvmerge", []extra{
		{path: path, track: Track{Type: "TRUE-HD"}},
	})
	if strings.Join(argv, " ") != path {
		t.Errorf("argv = %v, want just the path", argv)
	}
}
