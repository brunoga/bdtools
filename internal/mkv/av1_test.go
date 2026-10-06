package mkv

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The AV1 source makes one block per temporal unit, temporal delimiters
// removed, shown one frame duration apart, with keyframes where the encoder
// put them and the sequence header in an av1C.
func TestAV1Source(t *testing.T) {
	v, err := NewVideoSource(fixture(t, "mkv", "av1.obu"), AV1, 24000, 1001, 1)
	if err != nil {
		t.Fatal(err)
	}
	tr := v.Track()
	if tr.CodecID != "V_AV1" || tr.Width != 1280 || tr.Height != 480 || tr.StereoMode != 1 {
		t.Errorf("track %+v", tr)
	}
	priv := tr.CodecPrivate
	if len(priv) < 8 || priv[0] != 0x81 || priv[1]>>5 != 0 || priv[2]&0x0c != 0x0c || priv[4]>>3&0xf != obuSequenceHeader {
		t.Errorf("av1C % x", priv[:min(8, len(priv))])
	}
	var keys []int
	n := 0
	for ; ; n++ {
		f, err := v.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if want := time.Duration(n) * time.Second * 1001 / 24000; f.PTS-want > time.Microsecond || want-f.PTS > time.Microsecond || f.Order != f.PTS {
			t.Fatalf("frame %d at %v/%v, want %v", n, f.PTS, f.Order, want)
		}
		if f.Keyframe {
			keys = append(keys, n)
		}
		r := bufio.NewReader(bytes.NewReader(f.Data))
		for {
			typ, _, _, err := readOBU(r)
			if err == io.EOF {
				break
			} else if err != nil {
				t.Fatalf("frame %d: %v", n, err)
			}
			if typ == obuTemporalDelimiter {
				t.Fatalf("frame %d keeps its temporal delimiter", n)
			}
		}
	}
	if n != 27 || len(keys) != 3 || keys[0] != 0 || keys[1] != 12 || keys[2] != 24 {
		t.Errorf("%d frames, keyframes at %v", n, keys)
	}
}

// A muxed AV1 file is one players and tools read: ffprobe decodes all 27
// frames and mkvmerge identifies an AV1 track.
func TestAV1Muxes(t *testing.T) {
	v, err := NewVideoSource(fixture(t, "mkv", "av1.obu"), AV1, 24000, 1001, 1)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "av1.mkv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Mux(f, []Source{v}, Options{}); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	in, _ := os.Open(path)
	r, err := NewReader(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Tracks[0].CodecID != "V_AV1" || r.Tracks[0].StereoMode != 1 {
		t.Errorf("track %+v", r.Tracks[0])
	}
	_ = in.Close()

	if ffprobe, err := exec.LookPath("ffprobe"); err == nil {
		out, err := exec.CommandContext(t.Context(), ffprobe, "-v", "error", "-count_frames", "-select_streams", "v",
			"-show_entries", "stream=codec_name,width,height,nb_read_frames", "-of", "csv=p=0", path).CombinedOutput() //nolint:gosec // test
		if err != nil || strings.Trim(strings.TrimSpace(string(out)), ",") != "av1,1280,480,27" {
			t.Errorf("ffprobe: %v %q", err, out)
		}
	}
	if mkvmerge, err := exec.LookPath("mkvmerge"); err == nil {
		out, _ := exec.CommandContext(t.Context(), mkvmerge, "-J", path).CombinedOutput() //nolint:gosec // test
		if !bytes.Contains(out, []byte(`"codec": "AV1"`)) {
			t.Errorf("mkvmerge does not see AV1:\n%s", out)
		}
	}
}

func TestAV1SequenceHeaderFields(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "mkv", "av1.obu"))
	if err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(bytes.NewReader(b))
	for {
		typ, _, payload, err := readOBU(r)
		if err != nil {
			t.Fatal("no sequence header:", err)
		}
		if typ != obuSequenceHeader {
			continue
		}
		s, err := parseAV1Seq(payload)
		if err != nil {
			t.Fatal(err)
		}
		if s.profile != 0 || s.width != 1280 || s.height != 480 || s.highBitDepth || s.mono || !s.subX || !s.subY || s.reduced {
			t.Errorf("sequence header %+v", s)
		}
		return
	}
}

func FuzzAV1Source(f *testing.F) {
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "mkv", "av1.obu"))
	if err == nil {
		f.Add(b[:min(len(b), 2048)])
	}
	f.Add([]byte{0x12, 0x00, 0x0a, 0x0b, 0x00, 0x00, 0x00, 0x24, 0xc4, 0xff, 0xdf, 0x00, 0x68, 0x02})
	f.Fuzz(func(t *testing.T, b []byte) {
		v, err := NewVideoSource(bytes.NewReader(b), AV1, 24000, 1001, 1)
		if err != nil {
			return
		}
		for i := 0; i < 1000; i++ {
			if _, err := v.Next(); err != nil {
				return
			}
		}
	})
}
