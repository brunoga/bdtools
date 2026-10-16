package mp4

import (
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/brunoga/bdtools/internal/mkv"
)

// ffprobeTimes is ffprobe's presentation times of a stream's packets.
func ffprobeTimes(t *testing.T, path, stream string) []float64 {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "ffprobe", "-v", "error", "-select_streams", stream, //nolint:gosec // test
		"-show_entries", "packet=pts_time", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	var ts []float64
	for _, l := range strings.Fields(string(out)) {
		v, err := strconv.ParseFloat(strings.TrimSuffix(l, ","), 64)
		if err == nil {
			ts = append(ts, v)
		}
	}
	return ts
}

// MP4 and QuickTime files ffmpeg writes read back as ffprobe reads them:
// the tracks, every sample, the presentation times (B-frames, and an
// audio track starting late through its edit list), the keyframes.
func TestReaderMatchesFFprobe(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	for _, tc := range []struct {
		name, ext string
		vcodec    []string
		acodec    []string
		videoID   string
		audioID   string
	}{
		{"h264-ac3", ".mp4", []string{"-c:v", "libx264", "-bf", "3"}, []string{"-c:a", "ac3"}, "V_MPEG4/ISO/AVC", "A_AC3"},
		{"hevc-pcm", ".mov", []string{"-c:v", "libx265", "-tag:v", "hvc1", "-x265-params", "log-level=error"}, []string{"-c:a", "pcm_s16be"}, "V_MPEGH/ISO/HEVC", "A_PCM/INT/LIT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+tc.ext)
			args := []string{"-v", "error", "-f", "lavfi", "-i", "testsrc2=s=320x240:r=24:d=2", "-itsoffset", "0.5", "-f", "lavfi", "-i", "sine=f=440:r=48000:d=2",
				"-map", "0:v", "-map", "1:a"}
			args = append(append(append(args, tc.vcodec...), tc.acodec...), path)
			if out, err := exec.CommandContext(t.Context(), "ffmpeg", args...).CombinedOutput(); err != nil { //nolint:gosec // test
				t.Skipf("making the file: %v %s", err, out)
			}
			f, err := os.Open(path) //nolint:gosec // test
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			r, err := NewReader(f)
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Tracks) != 2 || r.Tracks[0].CodecID != tc.videoID || r.Tracks[1].CodecID != tc.audioID {
				t.Fatalf("tracks %+v", r.Tracks)
			}
			if v := r.Tracks[0]; v.Width != 320 || v.Height != 240 || len(v.CodecPrivate) == 0 {
				t.Errorf("video %dx%d, %d bytes of configuration", v.Width, v.Height, len(v.CodecPrivate))
			}
			got := map[uint64][]time.Duration{}
			keys := 0
			for {
				p, err := r.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				got[p.Track] = append(got[p.Track], p.Time)
				if p.Track == r.Tracks[0].Number && p.Keyframe {
					keys++
				}
			}
			vt := ffprobeTimes(t, path, "v:0")
			if len(got[r.Tracks[0].Number]) != len(vt) {
				t.Fatalf("%d video samples, ffprobe %d", len(got[r.Tracks[0].Number]), len(vt))
			}
			for i, v := range vt {
				if d := got[r.Tracks[0].Number][i].Seconds() - v; math.Abs(d) > 0.002 {
					t.Fatalf("video sample %d at %v, ffprobe %.4f", i, got[r.Tracks[0].Number][i], v)
				}
			}
			if keys == 0 {
				t.Error("no keyframe")
			}
			at := ffprobeTimes(t, path, "a:0")
			a := got[r.Tracks[1].Number]
			if len(a) == 0 || len(at) == 0 || math.Abs(a[0].Seconds()-at[0]) > 0.03 || a[0] < 400*time.Millisecond {
				t.Errorf("audio starts at %v, ffprobe %v (of %d packets)", a, at[:min(len(at), 1)], len(at))
			}
		})
	}
}

// The MV-HEVC files this package writes read back with their second
// layer's parameter sets.
func TestReaderMVHEVC(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.mp4")
	writeStereoTestFile(t, path)
	f, err := os.Open(path) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	r, err := NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	v := r.Tracks[0]
	if v.CodecID != "V_MPEGH/ISO/HEVC" || len(r.LayerParams[v.Number]) == 0 {
		t.Fatalf("track %+v, layer parameters %d bytes", v, len(r.LayerParams[v.Number]))
	}
	n := 0
	for {
		if _, err := r.Next(); err != nil {
			break
		}
		n++
	}
	if n == 0 {
		t.Error("no samples")
	}
	_ = mkv.TypeVideo
}

// writeStereoTestFile muxes the MV-HEVC test stream into an MP4.
func writeStereoTestFile(t *testing.T, path string) {
	t.Helper()
	f, err := os.Open("../mkv/testdata/mvhevc.hevc")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	v, err := mkv.NewVideoSource(f, mkv.HEVC, 24000, 1001, 0)
	if err != nil {
		t.Fatal(err)
	}
	of, err := os.Create(path) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	err = Mux(of, []mkv.Source{v}, Options{FPSNum: 24000, FPSDen: 1001, Spatial: &Spatial{FOV: 90}})
	if cerr := of.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
}
