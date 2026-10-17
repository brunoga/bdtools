package aac

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"testing"
	"time"
)

// signal is seconds of test audio per channel: tones and a little noise.
func signal(channels int, seconds float64) [][]float64 {
	n := int(seconds*48000) / FrameSamples * FrameSamples
	in := make([][]float64, channels)
	seed := uint32(1)
	for c := range channels {
		in[c] = make([]float64, n)
		for i := range n {
			seed = seed*1664525 + 1013904223
			noise := (float64(seed>>8)/(1<<24) - 0.5) * 0.02
			x := float64(i) / 48000
			in[c][i] = 0.3*math.Sin(2*math.Pi*(220*float64(c+1))*x) + 0.15*math.Sin(2*math.Pi*2500*x+float64(c)) + noise
		}
	}
	return in
}

func encode(t *testing.T, e *Encoder, in [][]float64) []byte {
	t.Helper()
	var out []byte
	for f := 0; f+FrameSamples <= len(in[0]); f += FrameSamples {
		pcm := make([][]float64, len(in))
		for c := range in {
			pcm[c] = in[c][f : f+FrameSamples]
		}
		fr, err := e.EncodeFrame(pcm)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, fr...)
	}
	return out
}

// snr decodes an ADTS stream with ffmpeg and gives each channel's SNR
// against in, delayed by Delay, as ffmpeg orders them (its 5.1 is our C
// L R Ls Rs LFE as L R C LFE Ls Rs), and the gain the decoded signal has.
func snr(t *testing.T, ff string, stream []byte, in [][]float64) ([]float64, float64) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), ff, "-v", "error", "-f", "aac", "-i", "-", "-f", "f64le", "-c:a", "pcm_f64le", "-") //nolint:gosec // test
	cmd.Stdin = bytes.NewReader(stream)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffmpeg: %v", err)
	}
	nch := len(in)
	order := []int{0, 1, 2, 3, 4, 5}[:nch]
	if nch == 6 {
		order = []int{1, 2, 0, 5, 3, 4}
	}
	frames := len(out) / 8 / nch
	res := make([]float64, nch)
	var gx, gy float64
	for i, oc := range order {
		if nch == 6 && oc == 5 {
			continue // the LFE: band-limited
		}
		var s, n float64
		for k := 4 * FrameSamples; k < frames && k-Delay < len(in[oc]); k++ {
			v := math.Float64frombits(binary.LittleEndian.Uint64(out[8*(k*nch+i):]))
			x := in[oc][k-Delay]
			s += x * x
			n += (v - x) * (v - x)
			gx += x * x
			gy += v * x
		}
		res[oc] = 10 * math.Log10(s/n)
	}
	return res, gy / gx
}

// ffmpeg decodes the stream, as the input: about the same gain, the same
// sign and alignment.
func TestDecodesWithFFmpeg(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	for _, tc := range []struct {
		channels int
		lfe      bool
		kbps     int
	}{{1, false, 64}, {2, false, 128}, {6, true, 320}} {
		t.Run(fmt.Sprint(tc.channels, "ch-", tc.kbps), func(t *testing.T) {
			e, err := NewEncoder(tc.channels, tc.lfe, 48000, tc.kbps)
			if err != nil {
				t.Fatal(err)
			}
			in := signal(tc.channels, 3)
			stream := encode(t, e, in)
			s, gain := snr(t, ff, stream, in)
			kbps := float64(len(stream)*8) / (float64(len(in[0])) / 48000) / 1000
			t.Logf("SNR %.1f dB by channel %v, gain %.3f, %.0f kb/s", s[0], s, gain, kbps)
			// The noise is shaped under the masking threshold, not spread
			// evenly: coarse bands round up a little.
			if gain < 0.9 || gain > 1.15 {
				t.Errorf("gain %.3f", gain)
			}
			if kbps > float64(tc.kbps)*1.05 {
				t.Errorf("%.0f kb/s, want %d", kbps, tc.kbps)
			}
		})
	}
}

// TestFileAgainstFFmpeg compares this encoder with ffmpeg's on the PCM in
// $BDTOOLS_AAC_PCM (f32le at 48 kHz, $BDTOOLS_AAC_CH channels, 6 in AC-3's
// order L C R Ls Rs LFE) at $BDTOOLS_AAC_KBPS.
func TestFileAgainstFFmpeg(t *testing.T) {
	path := os.Getenv("BDTOOLS_AAC_PCM")
	if path == "" {
		t.Skip("BDTOOLS_AAC_PCM not set")
	}
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	nch, kbps := 2, 128
	fmt.Sscan(os.Getenv("BDTOOLS_AAC_CH"), &nch)
	fmt.Sscan(os.Getenv("BDTOOLS_AAC_KBPS"), &kbps)
	raw, err := os.ReadFile(path) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	n := len(raw) / 4 / nch / FrameSamples * FrameSamples
	file := make([][]float64, nch)
	for c := range nch {
		file[c] = make([]float64, n)
		for i := range n {
			file[c][i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[4*(i*nch+c):])))
		}
	}
	in := file
	ffOrder := []int{0, 1}[:min(nch, 2)]
	layout := map[int]string{1: "mono", 2: "stereo", 6: "5.1"}[nch]
	if nch == 6 {
		in = [][]float64{file[1], file[0], file[2], file[3], file[4], file[5]} // C L R Ls Rs LFE
		ffOrder = []int{0, 2, 1, 5, 3, 4}                                      // ffmpeg's L R C LFE Ls Rs from the file
	}
	e, err := NewEncoder(nch, nch == 6, 48000, kbps)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	ours := encode(t, e, in)
	took := time.Since(start)
	pcm := make([]byte, 0, len(raw))
	for i := range n {
		for _, c := range ffOrder {
			pcm = binary.LittleEndian.AppendUint32(pcm, math.Float32bits(float32(file[c][i])))
		}
	}
	cmd := exec.CommandContext(t.Context(), ff, "-v", "error", "-f", "f32le", "-ar", "48000", "-ch_layout", layout, "-i", "-", //nolint:gosec // test
		"-c:a", "aac", "-b:a", fmt.Sprint(kbps, "k"), "-f", "adts", "-")
	cmd.Stdin = bytes.NewReader(pcm)
	theirs, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if dir := os.Getenv("BDTOOLS_AAC_OUT"); dir != "" {
		_ = os.WriteFile(dir+"/ours.aac", ours, 0o600)
		_ = os.WriteFile(dir+"/ffmpeg.aac", theirs, 0o600)
	}
	so, _ := snr(t, ff, ours, in)
	st, _ := snr(t, ff, theirs, in)
	// The decoded channels, as in.
	decode := func(stream []byte) [][]float64 {
		cmd := exec.CommandContext(t.Context(), ff, "-v", "error", "-f", "aac", "-i", "-", "-f", "f64le", "-c:a", "pcm_f64le", "-") //nolint:gosec // test
		cmd.Stdin = bytes.NewReader(stream)
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		order := []int{0, 1, 2, 3, 4, 5}[:nch]
		if nch == 6 {
			order = []int{1, 2, 0, 5, 3, 4}
		}
		d := make([][]float64, nch)
		frames := len(out) / 8 / nch
		for i, oc := range order {
			d[oc] = make([]float64, 0, frames)
			for k := Delay; k < frames; k++ {
				d[oc] = append(d[oc], math.Float64frombits(binary.LittleEndian.Uint64(out[8*(k*nch+i):])))
			}
		}
		return d
	}
	do, dt := decode(ours), decode(theirs)
	var mo, mt, no, nt, bo, bt float64
	full := 0
	for c := range so {
		if nch == 6 && c == 5 {
			continue
		}
		mo += so[c]
		mt += st[c]
		a, b := nmr(in[c], do[c])
		no += a
		bo += b
		a, b = nmr(in[c], dt[c])
		nt += a
		bt += b
		full++
	}
	f := float64(full)
	dur := float64(n) / 48000
	t.Logf("NMR %.2f dB (%.0f%% frames disturbed), SNR %.2f dB at %.0f kb/s; ffmpeg's NMR %.2f dB (%.0f%%), SNR %.2f dB at %.0f kb/s; %.1f s in %v",
		no/f, 100*bo/f, mo/f, float64(len(ours))*8/dur/1000, nt/f, 100*bt/f, mt/f, float64(len(theirs))*8/dur/1000, dur, took)
}
