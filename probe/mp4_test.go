package probe

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/brunoga/bdtools/internal/mkv"
	"github.com/brunoga/bdtools/internal/mp4"
)

// mvhevcFile is an MV-HEVC MP4 (the test stream, muxed with Apple's stereo
// boxes), its movie box at the end as this program writes it.
func mvhevcFile(t *testing.T, stereoMode int) []byte {
	t.Helper()
	f, err := os.Open("../internal/mkv/testdata/mvhevc.hevc")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	v, err := mkv.NewVideoSource(f, mkv.HEVC, 24000, 1001, stereoMode)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "s.mp4")
	of, err := os.Create(path) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	o := mp4.Options{FPSNum: 24000, FPSDen: 1001}
	if stereoMode == 0 {
		o.Spatial = &mp4.Spatial{FOV: 90}
	}
	err = mp4.Mux(of, []mkv.Source{v}, o)
	if cerr := of.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Spatial video is MV-HEVC, read from the movie box at the file's end with
// the first and last pieces alone; without the last, the probe names the
// bytes it wants.
func TestMP4SpatialVideo(t *testing.T) {
	b := mvhevcFile(t, 0)
	size := int64(len(b))
	const piece = 16 << 10
	r := &rangeReader{b: b, allowed: [][2]int64{{0, piece}, {size - piece, size}}}
	res, err := MP4(r, size, "spatial.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindMP4 || !res.Is3D || res.Layout != LayoutMVHEVC || len(res.Video) != 1 ||
		res.Video[0].Codec != "HEVC" || res.Video[0].BitDepth != 8 || res.Duration == 0 {
		t.Errorf("result %+v", res)
	}
	_, err = MP4(&rangeReader{b: b, allowed: [][2]int64{{0, piece}}}, size, "spatial.mp4")
	var missing *MissingDataError
	if !errors.As(err, &missing) || missing.Offset < piece {
		t.Errorf("without the end: %v", err)
	}
}

// A frame holding both eyes is told by its st3d box.
func TestMP4FramePacked(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	es := filepath.Join(t.TempDir(), "ou.hevc")
	if out, err := exec.CommandContext(t.Context(), ff, "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=320x480:r=24:d=1", //nolint:gosec // test
		"-c:v", "libx265", "-x265-params", "log-level=error", "-f", "hevc", es).CombinedOutput(); err != nil {
		t.Skipf("making the stream: %v %s", err, out)
	}
	f, err := os.Open(es) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	v, err := mkv.NewVideoSource(f, mkv.HEVC, 24, 1, 3) // over-under
	if err != nil {
		t.Fatal(err)
	}
	var buf seekBuffer
	if err := mp4.Mux(&buf, []mkv.Source{v}, mp4.Options{FPSNum: 24, FPSDen: 1}); err != nil {
		t.Fatal(err)
	}
	res, err := MP4(bytes.NewReader(buf.b), int64(len(buf.b)), "ou.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Is3D || res.Layout != LayoutTopBottom {
		t.Errorf("result %+v", res)
	}
}

// seekBuffer is an in-memory io.WriteSeeker.
type seekBuffer struct {
	b   []byte
	pos int64
}

func (s *seekBuffer) Write(p []byte) (int, error) {
	if need := s.pos + int64(len(p)); need > int64(len(s.b)) {
		s.b = append(s.b, make([]byte, need-int64(len(s.b)))...)
	}
	copy(s.b[s.pos:], p)
	s.pos += int64(len(p))
	return len(p), nil
}

func (s *seekBuffer) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case 0:
		s.pos = off
	case 1:
		s.pos += off
	default:
		s.pos = int64(len(s.b)) + off
	}
	return s.pos, nil
}

// A plain 2D file with audio, as ffmpeg writes it (movie box first, with
// -movflags faststart).
func TestMP42D(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	path := filepath.Join(t.TempDir(), "flat.mp4")
	out, err := exec.CommandContext(t.Context(), ff, "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=320x240:r=24:d=1", //nolint:gosec // test
		"-f", "lavfi", "-i", "sine=r=48000:d=1", "-c:v", "libx264", "-c:a", "ac3", "-metadata:s:a", "language=eng",
		"-movflags", "faststart", path).CombinedOutput()
	if err != nil {
		t.Skipf("making the file: %v %s", err, out)
	}
	res, err := Path(path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Is3D || res.Layout != Layout2D || len(res.Video) != 1 || res.Video[0].Codec != "H.264" || res.Video[0].Width != 320 ||
		len(res.Audio) != 1 || res.Audio[0].Codec != "AC3" || res.Audio[0].Language != "eng" {
		t.Errorf("result %+v", res)
	}
}

// Partial and malformed input never panics.
func FuzzMP4(f *testing.F) {
	f.Add([]byte("\x00\x00\x00\x08ftyp"))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = MP4(bytes.NewReader(b), int64(len(b)), "fuzz")
	})
}

// Truncated files fail cleanly.
func TestTruncatedMP4(t *testing.T) {
	b := mvhevcFile(t, 0)
	for _, n := range []int{0, 7, 8, 100, len(b) / 2, len(b) - 1} {
		if _, err := MP4(bytes.NewReader(b[:n]), int64(n), "cut"); err == nil {
			t.Errorf("%d bytes probed", n)
		}
	}
}
