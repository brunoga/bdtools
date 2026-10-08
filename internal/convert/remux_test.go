package convert

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brunoga/bdtools/internal/mkv"
)

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
	if !p.Steps[0].Builtin || !strings.Contains(joined, "/media/out/Film.m2ts") {
		t.Errorf("remux step = %q", joined)
	}
	for _, s := range p.Steps {
		if s.Name == "decode" || s.Name == "encode" {
			t.Errorf("a remux must not %s", s.Name)
		}
	}
}

// MVC has no home in Matroska that players agree on, so a 3D source's remux
// into a .mkv is refused, saying what to do instead; with --2d its base view
// remuxes into one.
func TestRemuxIntoMatroskaIs2D(t *testing.T) {
	run := func(twoD bool) error {
		o := DefaultOptions()
		o.Input, o.Output, o.Remux, o.TwoD = bluray("disc.iso"), filepath.Join(t.TempDir(), "Film.mkv"), true, twoD
		return NewRunner(CurrentGOOS, o, nil).Run(t.Context())
	}
	err := run(false)
	if err == nil || !strings.Contains(err.Error(), ".m2ts") || !strings.Contains(err.Error(), "--2d") {
		t.Errorf("a 3D remux into .mkv: %v", err)
	}
	if err := run(true); err != nil {
		t.Errorf("--2d --remux into .mkv: %v", err)
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

// trueHDWithCore interleaves the AC-3 fixture's frames into the TrueHD
// fixture, one every 40 access units, as a Blu-ray's TrueHD track carries
// its AC-3 core.
func trueHDWithCore(t *testing.T) []byte {
	t.Helper()
	ac3, err := os.ReadFile(bluray(filepath.Join("src", "a.ac3")))
	if err != nil {
		t.Fatal(err)
	}
	thd, err := os.ReadFile(bluray(filepath.Join("src", "e.thd")))
	if err != nil {
		t.Fatal(err)
	}
	var frames [][]byte
	for b := ac3; len(b) >= 6; {
		// frmsizecod table for 48 kHz: 2-byte words per frame.
		code := int(b[4] & 0x3f)
		n := 2 * []int{64, 64, 80, 80, 96, 96, 112, 112, 128, 128, 160, 160, 192, 192, 224, 224, 256, 256,
			320, 320, 384, 384, 448, 448, 512, 512, 640, 640, 768, 768, 896, 896, 1024, 1024, 1152, 1152,
			1280, 1280}[min(code, 37)]
		if n > len(b) {
			break
		}
		frames, b = append(frames, b[:n]), b[n:]
	}
	var out []byte
	for i, b := 0, thd; len(b) >= 4; i++ {
		n := int(binary.BigEndian.Uint16(b)&0x0fff) * 2
		if n == 0 || n > len(b) {
			break
		}
		if i%40 == 0 && i/40 < len(frames) {
			out = append(out, frames[i/40]...)
		}
		out, b = append(out, b[:n]...), b[n:]
	}
	return out
}

// The AC-3 core inside a TrueHD track is dropped by default; --keep-fallback
// keeps it as its own track, with the language of the track it came from.
func TestKeepFallbackKeepsTheCore(t *testing.T) {
	for _, keep := range []bool{false, true} {
		tmp := t.TempDir()
		thd := filepath.Join(tmp, "a.track_4352_eng.thd")
		if err := os.WriteFile(thd, trueHDWithCore(t), 0o600); err != nil {
			t.Fatal(err)
		}
		video := filepath.Join(tmp, "v.264")
		if err := os.WriteFile(video, readFixture(t, fixtureDir(t), "mvc_base.264"), 0o600); err != nil {
			t.Fatal(err)
		}
		o := DefaultOptions()
		o.Output, o.KeepFallback = filepath.Join(tmp, "out.mkv"), keep
		r := NewRunner(CurrentGOOS, o, nil)
		err := r.muxBuiltin(context.Background(), []string{video}, []extra{
			{path: thd, track: Track{ID: 4352, StreamID: "A_AC3", Type: "TRUE-HD", Lang: "eng"}},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(o.Output)
		if err != nil {
			t.Fatal(err)
		}
		rd, err := mkv.NewReader(f)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		var audio []string
		for _, tr := range rd.Tracks {
			if tr.Type == mkv.TypeAudio {
				audio = append(audio, tr.CodecID+":"+tr.Language)
			}
		}
		want := "A_TRUEHD:eng"
		if keep {
			want += " A_AC3:eng"
		}
		if got := strings.Join(audio, " "); got != want {
			t.Errorf("keep %v: audio tracks %q, want %q", keep, got, want)
		}
	}
}
