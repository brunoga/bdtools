package convert

import (
	"bufio"
	"fmt"
	"os"

	"github.com/brunoga/bdtools/internal/ac3"
	"github.com/brunoga/bdtools/internal/dts"
)

// dtsToAC3 codes the DTS track at path (DTS, or DTS-HD: its core) as
// AC-3 with the decoder and encoder here, into out. It returns the AC-3
// rate in kb/s and the coded audio's delay in samples.
func dtsToAC3(path, out string) (kbps, delay int, err error) {
	b, err := os.ReadFile(path) //nolint:gosec // our work directory
	if err != nil {
		return 0, 0, err
	}
	var d dts.Decoder
	// The first core says the layout.
	var first *dts.Frame
	for i := 0; i+16 <= len(b); i++ {
		if n := dts.FrameSize(b[i:]); n > 0 && i+n <= len(b) {
			if first, err = (&dts.Decoder{}).Decode(b[i : i+n]); err != nil {
				return 0, 0, err
			}
			break
		}
	}
	if first == nil {
		return 0, 0, fmt.Errorf("no DTS core frame")
	}
	if first.SampleRate != 48000 {
		return 0, 0, fmt.Errorf("DTS at %d Hz", first.SampleRate)
	}
	// The AC-3 channels: those of L, C, R, Ls, Rs the core has, then the
	// LFE (a 6.1 core's Cs folded into the surrounds).
	var spk []int
	for _, s := range []int{dts.SpeakerL, dts.SpeakerC, dts.SpeakerR, dts.SpeakerLs, dts.SpeakerRs} {
		if first.Samples[s] != nil {
			spk = append(spk, s)
		}
	}
	if len(spk) == 1 && first.Samples[dts.SpeakerC] != nil {
		spk = []int{dts.SpeakerC}
	}
	lfe := first.Samples[dts.SpeakerLFE] != nil && len(spk) >= 2
	kbps = 640
	if len(spk) <= 2 {
		kbps = 320
	}
	nch := len(spk)
	if lfe {
		nch++
	}
	enc, err := ac3.NewEncoder(nch, lfe, kbps)
	if err != nil {
		return 0, 0, err
	}
	of, err := os.Create(out) //nolint:gosec // our work directory
	if err != nil {
		return 0, 0, err
	}
	w := bufio.NewWriterSize(of, 1<<20)
	defer func() {
		if ferr := w.Flush(); err == nil {
			err = ferr
		}
		if cerr := of.Close(); err == nil {
			err = cerr
		}
	}()
	buf := make([][]float64, nch)
	put := func(flush bool) error {
		for len(buf[0]) >= ac3.FrameSamples || flush && len(buf[0]) > 0 {
			frame := make([][]float64, nch)
			for c := range frame {
				n := min(len(buf[c]), ac3.FrameSamples)
				frame[c] = append(append([]float64(nil), buf[c][:n]...), make([]float64, ac3.FrameSamples-n)...)
				buf[c] = buf[c][n:]
			}
			fb, err := enc.EncodeFrame(frame)
			if err != nil {
				return err
			}
			if _, err := w.Write(fb); err != nil {
				return err
			}
		}
		return nil
	}
	const scale = 1.0 / (1 << 23)
	for i := 0; i+16 <= len(b); {
		n := dts.FrameSize(b[i:])
		if n == 0 || i+n > len(b) {
			i++ // the extension substream after a core, or garbage: to the next core
			continue
		}
		f, err := d.Decode(b[i : i+n])
		if err != nil {
			return 0, 0, fmt.Errorf("DTS at byte %d: %w", i, err)
		}
		i += n
		cs := f.Samples[dts.SpeakerCs]
		for c, s := range spk {
			src := f.Samples[s]
			for k, v := range src {
				x := float64(v) * scale
				if cs != nil && (s == dts.SpeakerLs || s == dts.SpeakerRs) {
					x += 0.7071 * float64(cs[k]) * scale
				}
				buf[c] = append(buf[c], x)
			}
		}
		if lfe {
			for _, v := range f.Samples[dts.SpeakerLFE] {
				buf[nch-1] = append(buf[nch-1], float64(v)*scale)
			}
		}
		if err := put(false); err != nil {
			return 0, 0, err
		}
	}
	for c := range buf {
		buf[c] = append(buf[c], make([]float64, ac3.Delay)...)
	}
	if err := put(true); err != nil {
		return 0, 0, err
	}
	return kbps, ac3.Delay, nil
}
