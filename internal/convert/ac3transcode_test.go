package convert

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/brunoga/bdtools/internal/mkv"
)

// A PCM track coded as AC-3 for an MP4 keeps its timeline: its late start
// and its gap move to the AC-3 frames that hold those samples (less the
// encoder's delay), and the AC-3 decodes.
func TestPCMToAC3KeepsTheTimeline(t *testing.T) {
	if _, err := LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "track.wav")
	const rate, secs = 48000, 2
	data := make([]byte, rate*secs*4)
	for i := range rate * secs {
		v := int16(8000 * math.Sin(2*math.Pi*440*float64(i)/rate))
		binary.LittleEndian.PutUint16(data[4*i:], uint16(v))
		binary.LittleEndian.PutUint16(data[4*i+2:], uint16(v))
	}
	hdr := []byte("RIFF\x00\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x02\x00")
	hdr = binary.LittleEndian.AppendUint32(hdr, rate)
	hdr = binary.LittleEndian.AppendUint32(hdr, rate*4)
	hdr = append(hdr, 4, 0, 16, 0)
	hdr = append(hdr, "data"...)
	hdr = binary.LittleEndian.AppendUint32(hdr, uint32(len(data)))
	binary.LittleEndian.PutUint32(hdr[4:], uint32(len(hdr)-8+len(data)))
	if err := os.WriteFile(path, append(hdr, data...), 0o600); err != nil {
		t.Fatal(err)
	}
	head := int64(len(hdr))
	e := extra{path: path, track: Track{ID: 4352, Type: "LPCM", StreamID: "A_LPCM"},
		sync: []mkv.SyncPoint{{Offset: head, At: 500 * time.Millisecond}, {Offset: head + rate*4, At: 1700 * time.Millisecond}}}
	r := NewRunner(CurrentGOOS, DefaultOptions(), nil)
	out, sync, err := r.toAC3(context.Background(), e, mkv.WAV)
	if err != nil {
		t.Fatal(err)
	}
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	if len(sync) != 2 {
		t.Fatalf("sync points %v, want 2", sync)
	}
	// The first frame starts 256 samples before the source; the gap's frame
	// is the first whose first sample is past it (frame 32: sample 48896; stereo frames are 1280 bytes at 320 kb/s).
	want := []struct {
		off int64
		at  float64
	}{{0, 500 - 256.0/48}, {32 * 1280, 1700 + 896.0/48}}
	for i, w := range want {
		if sync[i].Offset != w.off || math.Abs(ms(sync[i].At)-w.at) > 0.05 {
			t.Errorf("sync point %d: %d at %.3f ms, want %d at %.3f ms", i, sync[i].Offset, ms(sync[i].At), w.off, w.at)
		}
	}
	f, err := os.Open(out) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	a, err := mkv.NewAudioSource(f, mkv.AC3, false, "eng")
	if err != nil {
		t.Fatal(err)
	}
	a.SetSyncPoints(sync)
	if tr := a.Track(); tr.CodecID != "A_AC3" || tr.Channels != 2 || tr.SampleRate != 48000 {
		t.Errorf("track %+v, want AC-3 stereo at 48 kHz", tr)
	}
	n := 0
	for {
		if _, err := a.Next(); err != nil {
			break
		}
		n++
	}
	if n < 62 || a.Delay() == 0 || len(a.Gaps()) != 1 {
		t.Errorf("%d frames, delay %v, gaps %v", n, a.Delay(), a.Gaps())
	}
}

// A DTS track (5.1, as discs have it) codes as 5.1 AC-3 at 640 kb/s, from
// where the source starts.
func TestDTSToAC3(t *testing.T) {
	ff, err := LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	path := filepath.Join(t.TempDir(), "track.dts")
	cmd := exec.CommandContext(t.Context(), ff, "-v", "error", "-f", "lavfi", "-i", "sine=f=440:r=48000:d=2", //nolint:gosec // test
		"-af", "pan=5.1|c0=c0|c1=c0|c2=c0|c3=c0|c4=c0|c5=c0", "-c:a", "dca", "-strict", "-2", "-f", "dts", path)
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("making DTS: %v %s", err, msg)
	}
	e := extra{path: path, track: Track{ID: 4353, Type: "DTS", StreamID: "A_DTS"},
		sync: []mkv.SyncPoint{{Offset: 0, At: 300 * time.Millisecond}}}
	r := NewRunner(CurrentGOOS, DefaultOptions(), nil)
	out, sync, err := r.toAC3(t.Context(), e, mkv.DTS)
	if err != nil {
		t.Fatal(err)
	}
	if len(sync) != 1 || sync[0].Offset != 0 || (sync[0].At-(300*time.Millisecond-5333*time.Microsecond)).Abs() > 100*time.Microsecond {
		t.Errorf("sync points %v, want one at 294.667 ms", sync)
	}
	f, err := os.Open(out) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	a, err := mkv.NewAudioSource(f, mkv.AC3, false, "eng")
	if err != nil {
		t.Fatal(err)
	}
	if tr := a.Track(); tr.Channels != 6 {
		t.Errorf("%d channels, want 6", tr.Channels)
	}
}
