package convert

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"

	"github.com/brunoga/bdtools/internal/ac3"
	"github.com/brunoga/bdtools/internal/mkv"
)

// pcmToAC3 codes the PCM track at path (WAV, the demuxer's channel order)
// as AC-3 with the encoder here, into out: 7.1 and 7.0 folded to 5.1 and
// 5.0, 96 and 192 kHz brought to 48. It returns the AC-3 rate in kb/s and
// the coded audio's delay, in 48 kHz samples (the encoder's, and the
// resampling filter's).
func pcmToAC3(path, out string) (kbps, delay int, err error) {
	f, err := os.Open(path) //nolint:gosec // our work directory
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = f.Close() }()
	src, err := mkv.NewAudioSource(f, mkv.WAV, false, "")
	if err != nil {
		return 0, 0, err
	}
	t := src.Track()
	rate, nch, bits := int(t.SampleRate), t.Channels, t.BitDepth
	factor := rate / 48000
	if rate%48000 != 0 || factor != 1 && factor != 2 && factor != 4 {
		return 0, 0, fmt.Errorf("PCM at %d Hz", rate)
	}
	if bits != 16 && bits != 24 && bits != 32 {
		return 0, 0, fmt.Errorf("%d-bit PCM", bits)
	}
	// The AC-3 channels, each a mix of the source's (its index and weight).
	type tap struct {
		ch int
		w  float64
	}
	var mix [][]tap
	lfe := false
	const back = 0.7071
	switch nch {
	case 1, 2:
		for c := range nch {
			mix = append(mix, []tap{{c, 1}})
		}
	case 3: // FL FR FC: L C R
		mix = [][]tap{{{0, 1}}, {{2, 1}}, {{1, 1}}}
	case 4: // FL FR BL BR: L R Ls Rs
		mix = [][]tap{{{0, 1}}, {{1, 1}}, {{2, 1}}, {{3, 1}}}
	case 5: // FL FR FC BL BR: L C R Ls Rs
		mix = [][]tap{{{0, 1}}, {{2, 1}}, {{1, 1}}, {{3, 1}}, {{4, 1}}}
	case 6: // FL FR FC LFE BL BR
		mix = [][]tap{{{0, 1}}, {{2, 1}}, {{1, 1}}, {{4, 1}}, {{5, 1}}, {{3, 1}}}
		lfe = true
	case 7: // FL FR FC BL BR SL SR
		mix = [][]tap{{{0, 1}}, {{2, 1}}, {{1, 1}}, {{5, 1}, {3, back}}, {{6, 1}, {4, back}}}
	case 8: // FL FR FC LFE BL BR SL SR
		mix = [][]tap{{{0, 1}}, {{2, 1}}, {{1, 1}}, {{6, 1}, {4, back}}, {{7, 1}, {5, back}}, {{3, 1}}}
		lfe = true
	default:
		return 0, 0, fmt.Errorf("%d PCM channels", nch)
	}
	kbps = 640
	if len(mix) <= 2 {
		kbps = 320
	}
	enc, err := ac3.NewEncoder(len(mix), lfe, kbps)
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
	dec := make([]*decimator, len(mix))
	for i := range dec {
		dec[i] = newDecimator(factor)
	}
	buf := make([][]float64, len(mix))
	put := func(flush bool) error {
		for len(buf[0]) >= ac3.FrameSamples || flush && len(buf[0]) > 0 {
			frame := make([][]float64, len(mix))
			for c := range frame {
				n := min(len(buf[c]), ac3.FrameSamples)
				frame[c] = append(append([]float64(nil), buf[c][:n]...), make([]float64, ac3.FrameSamples-n)...)
				buf[c] = buf[c][n:]
			}
			b, err := enc.EncodeFrame(frame)
			if err != nil {
				return err
			}
			if _, err := w.Write(b); err != nil {
				return err
			}
		}
		return nil
	}
	scale := 1 / math.Exp2(float64(bits-1))
	bps := bits / 8
	for {
		fr, err := src.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, 0, err
		}
		n := len(fr.Data) / (bps * nch)
		sample := func(i, c int) float64 {
			p := fr.Data[(i*nch+c)*bps:]
			var v int32
			switch bps {
			case 2:
				v = int32(int16(binary.LittleEndian.Uint16(p)))
			case 3:
				v = int32(uint32(p[0])<<8|uint32(p[1])<<16|uint32(p[2])<<24) >> 8
			default:
				v = int32(binary.LittleEndian.Uint32(p))
			}
			return float64(v) * scale
		}
		for i := range n {
			for oc, taps := range mix {
				var v float64
				for _, tp := range taps {
					v += tp.w * sample(i, tp.ch)
				}
				if o, ok := dec[oc].push(v); ok {
					buf[oc] = append(buf[oc], o)
				}
			}
		}
		if err := put(false); err != nil {
			return 0, 0, err
		}
	}
	// The encoder's delay: a frame more, of silence, once the last
	// samples are in.
	for c := range buf {
		buf[c] = append(buf[c], make([]float64, ac3.Delay)...)
	}
	if err := put(true); err != nil {
		return 0, 0, err
	}
	delay = ac3.Delay
	if factor > 1 {
		delay += dec[0].delay()
	}
	return kbps, delay, nil
}

// delay is the filter's, in output samples.
func (d *decimator) delay() int { return (len(d.taps) - 1) / 2 / d.factor }

// decimator brings a signal down by an integer factor through a low-pass
// filter (a windowed sinc cutting at 20 kHz out of 24); factor 1 passes
// it through.
type decimator struct {
	factor int
	taps   []float64
	hist   []float64
	n      int
}

func newDecimator(factor int) *decimator {
	d := &decimator{factor: factor}
	if factor == 1 {
		return d
	}
	const half = 32
	n := half * factor * 2
	fc := 20000.0 / (48000.0 * float64(factor)) // of the input rate
	d.taps = make([]float64, n+1)
	sum := 0.0
	for i := range d.taps {
		x := float64(i - n/2)
		s := 2 * fc
		if x != 0 {
			s = math.Sin(2*math.Pi*fc*x) / (math.Pi * x)
		}
		w := 0.42 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n)) + 0.08*math.Cos(4*math.Pi*float64(i)/float64(n)) // Blackman
		d.taps[i] = s * w
		sum += d.taps[i]
	}
	for i := range d.taps {
		d.taps[i] /= sum
	}
	d.hist = make([]float64, len(d.taps))
	return d
}

// push takes an input sample, giving an output one every factor of them.
func (d *decimator) push(v float64) (float64, bool) {
	if d.factor == 1 {
		return v, true
	}
	copy(d.hist, d.hist[1:])
	d.hist[len(d.hist)-1] = v
	d.n++
	if d.n%d.factor != 0 {
		return 0, false
	}
	s := 0.0
	for i, t := range d.taps {
		s += t * d.hist[i]
	}
	return s, true
}
