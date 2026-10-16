package ac3

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The frame's two CRCs check: each part's CRC, with its CRC in it, is
// zero.
func TestCRCs(t *testing.T) {
	e, err := NewEncoder(2, false, 192)
	if err != nil {
		t.Fatal(err)
	}
	pcm := tone(2, FrameSamples, 1000)
	f, err := e.EncodeFrame(pcm)
	if err != nil {
		t.Fatal(err)
	}
	fs58 := ((len(f) >> 2) + (len(f) >> 4)) << 1
	if c := crc16(f[2:fs58]); c != 0 {
		t.Errorf("crc1 region sums to %#x", c)
	}
	if c := crc16(f[fs58:]); c != 0 {
		t.Errorf("crc2 region sums to %#x", c)
	}
}

// tone is n samples per channel of a sine at hz, a different one for each
// channel.
func tone(ch, n int, hz float64) [][]float64 {
	pcm := make([][]float64, ch)
	for c := range ch {
		pcm[c] = make([]float64, n)
		for i := range n {
			pcm[c][i] = 0.5 * math.Sin(2*math.Pi*hz*float64(c+1)*float64(i)/48000)
		}
	}
	return pcm
}

// encodeFile codes seconds of a test signal and returns the stream and the
// input (interleaved, as s16le), channel by channel.
func encodeFile(t *testing.T, channels int, lfe bool, kbps int, seconds float64) ([]byte, [][]float64) {
	t.Helper()
	e, err := NewEncoder(channels, lfe, kbps)
	if err != nil {
		t.Fatal(err)
	}
	n := int(seconds*48000) / FrameSamples * FrameSamples
	in := make([][]float64, channels)
	seed := uint32(1)
	for c := range channels {
		in[c] = make([]float64, n)
		for i := range n {
			seed = seed*1664525 + 1013904223
			noise := (float64(seed>>8)/(1<<24) - 0.5) * 0.05
			x := float64(i) / 48000
			in[c][i] = 0.3*math.Sin(2*math.Pi*(220*float64(c+1))*x) + 0.2*math.Sin(2*math.Pi*3150*x+float64(c)) + noise
		}
	}
	var out []byte
	for f := 0; f < n; f += FrameSamples {
		pcm := make([][]float64, channels)
		for c := range channels {
			pcm[c] = in[c][f : f+FrameSamples]
		}
		fr, err := e.EncodeFrame(pcm)
		if err != nil {
			t.Fatal(err)
		}
		if len(fr) != e.FrameSize() {
			t.Fatalf("frame of %d bytes, want %d", len(fr), e.FrameSize())
		}
		out = append(out, fr...)
	}
	return out, in
}

// decodeSNR decodes an AC-3 stream with ffmpeg and gives each of our
// channels' SNR against in (delayed by Delay), the LFE left out.
func decodeSNR(t *testing.T, ff string, stream []byte, in [][]float64, lfe bool) []float64 {
	t.Helper()
	path := filepath.Join(t.TempDir(), "x.ac3")
	if err := os.WriteFile(path, stream, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(t.Context(), ff, "-v", "error", "-err_detect", "crccheck+bitstream+explode", //nolint:gosec // test
		"-f", "ac3", "-i", path, "-f", "f32le", "-c:a", "pcm_f32le", "-").Output()
	if err != nil {
		t.Fatalf("ffmpeg: %v", err)
	}
	nch := len(in)
	frames := len(out) / 4 / nch
	// ffmpeg's channel order for 5.1 is L R C LFE Ls Rs; ours L C R Ls Rs LFE.
	order := []int{0, 1, 2, 3, 4, 5}[:nch]
	if nch == 6 {
		order = []int{0, 2, 1, 5, 3, 4}
	}
	snrs := make([]float64, nch)
	for i, oc := range order {
		if lfe && oc == nch-1 {
			continue
		}
		var sig, noise float64
		for k := 4 * FrameSamples; k < frames && k-Delay < len(in[oc]); k++ {
			v := float64(math.Float32frombits(binary.LittleEndian.Uint32(out[4*(k*nch+i):])))
			x := in[oc][k-Delay]
			sig += x * x
			noise += (v - x) * (v - x)
		}
		snrs[oc] = 10 * math.Log10(sig/noise)
	}
	return snrs
}

// ffmpegAC3 codes in with ffmpeg's AC-3 encoder at kbps.
func ffmpegAC3(t *testing.T, ff string, in [][]float64, kbps int) []byte {
	t.Helper()
	nch := len(in)
	order := []int{0, 1, 2, 3, 4, 5}[:nch]
	layout := map[int]string{1: "mono", 2: "stereo", 6: "5.1"}[nch]
	if nch == 6 {
		order = []int{0, 2, 1, 5, 3, 4}
	}
	raw := make([]byte, 0, 4*nch*len(in[0]))
	for k := range in[0] {
		for _, oc := range order {
			raw = binary.LittleEndian.AppendUint32(raw, math.Float32bits(float32(in[oc][k])))
		}
	}
	cmd := exec.CommandContext(t.Context(), ff, "-v", "error", "-f", "f32le", "-ar", "48000", "-ch_layout", layout, "-i", "-", //nolint:gosec // test
		"-c:a", "ac3", "-b:a", fmt.Sprint(kbps, "k"), "-f", "ac3", "-")
	cmd.Stdin = bytes.NewReader(raw)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffmpeg's encoder: %v", err)
	}
	return out
}

// ffmpeg decodes the streams without complaint (CRCs checked) to the
// input, about as close as ffmpeg's own AC-3 encoder at the same rate.
func TestAgainstFFmpeg(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	for _, tc := range []struct {
		name     string
		channels int
		lfe      bool
		kbps     int
	}{
		{"mono-96", 1, false, 96},
		{"stereo-192", 2, false, 192},
		{"5.1-448", 6, true, 448},
		{"5.1-640", 6, true, 640},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream, in := encodeFile(t, tc.channels, tc.lfe, tc.kbps, 3)
			ours := decodeSNR(t, ff, stream, in, tc.lfe)
			theirs := decodeSNR(t, ff, ffmpegAC3(t, ff, in, tc.kbps), in, tc.lfe)
			for c := range ours {
				if tc.lfe && c == tc.channels-1 {
					continue
				}
				t.Logf("channel %d: SNR %.1f dB, ffmpeg's %.1f dB", c, ours[c], theirs[c])
				if ours[c] < theirs[c]-1 {
					t.Errorf("channel %d: %.1f dB, more than 1 dB under ffmpeg's %.1f", c, ours[c], theirs[c])
				}
			}
		})
	}
}

// TestFileAgainstFFmpeg compares this encoder with ffmpeg's on the PCM in
// $BDTOOLS_AC3_PCM (f32le at 48 kHz, $BDTOOLS_AC3_CH channels in AC-3
// order, the LFE last for 6) at $BDTOOLS_AC3_KBPS.
func TestFileAgainstFFmpeg(t *testing.T) {
	path := os.Getenv("BDTOOLS_AC3_PCM")
	if path == "" {
		t.Skip("BDTOOLS_AC3_PCM not set")
	}
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	nch, kbps := 6, 640
	fmt.Sscan(os.Getenv("BDTOOLS_AC3_CH"), &nch)
	fmt.Sscan(os.Getenv("BDTOOLS_AC3_KBPS"), &kbps)
	raw, err := os.ReadFile(path) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	n := len(raw) / 4 / nch / FrameSamples * FrameSamples
	in := make([][]float64, nch)
	for c := range nch {
		in[c] = make([]float64, n)
		for i := range n {
			in[c][i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[4*(i*nch+c):])))
		}
	}
	lfe := nch == 6
	e, err := NewEncoder(nch, lfe, kbps)
	if err != nil {
		t.Fatal(err)
	}
	var stream []byte
	for f := 0; f < n; f += FrameSamples {
		pcm := make([][]float64, nch)
		for c := range nch {
			pcm[c] = in[c][f : f+FrameSamples]
		}
		fr, err := e.EncodeFrame(pcm)
		if err != nil {
			t.Fatal(err)
		}
		stream = append(stream, fr...)
	}
	ours := decodeSNR(t, ff, stream, in, lfe)
	theirs := decodeSNR(t, ff, ffmpegAC3(t, ff, in, kbps), in, lfe)
	var so, st float64
	for c := range ours {
		if lfe && c == nch-1 {
			continue
		}
		t.Logf("channel %d: SNR %.2f dB, ffmpeg's %.2f dB", c, ours[c], theirs[c])
		so += ours[c]
		st += theirs[c]
	}
	t.Logf("mean: %.2f dB, ffmpeg's %.2f dB", so/float64(len(ours)-boolInt(lfe)), st/float64(len(ours)-boolInt(lfe)))
}
