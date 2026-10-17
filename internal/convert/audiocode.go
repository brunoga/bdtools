package convert

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"

	"github.com/brunoga/bdtools/internal/aac"
	"github.com/brunoga/bdtools/internal/ac3"
	"github.com/brunoga/bdtools/internal/dts"
	"github.com/brunoga/bdtools/internal/mkv"
)

// Audio a container cannot take as it is, coded again here: PCM and DTS
// (its core) decoded, then coded as AC-3 or AAC.

// The speakers of a decoded stream, in the order streams give them.
const (
	spkL = iota
	spkC
	spkR
	spkLs
	spkRs
	spkLFE
)

// pcmStream is decoded audio at 48 kHz: chunks of samples (in [-1, 1])
// for each of its speakers, in the order above.
type pcmStream struct {
	speakers []int
	next     func() ([][]float64, error) // io.EOF at the end
	delay    int                         // samples the stream lags its source
	close    func()
}

// openPCM decodes a track file of format WAV or DTS.
func openPCM(path string, format mkv.AudioFormat) (*pcmStream, error) {
	switch format {
	case mkv.WAV:
		return wavStream(path)
	case mkv.DTS:
		return dtsStream(path)
	}
	return nil, fmt.Errorf("no decoder here for it")
}

// wavStream reads a WAV track (the demuxer's channel order): 7.1 and 7.0
// folded to 5.1 and 5.0, 96 and 192 kHz brought to 48.
func wavStream(path string) (*pcmStream, error) {
	f, err := os.Open(path) //nolint:gosec // our work directory
	if err != nil {
		return nil, err
	}
	src, err := mkv.NewAudioSource(f, mkv.WAV, false, "")
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	t := src.Track()
	rate, nch, bits := t.SampleRate, t.Channels, t.BitDepth
	factor := rate / 48000
	if rate%48000 != 0 || factor != 1 && factor != 2 && factor != 4 {
		_ = f.Close()
		return nil, fmt.Errorf("PCM at %d Hz", rate)
	}
	if bits != 16 && bits != 24 && bits != 32 {
		_ = f.Close()
		return nil, fmt.Errorf("%d-bit PCM", bits)
	}
	// Each speaker out: a mix of the source's channels (index, weight).
	type tap struct {
		ch int
		w  float64
	}
	const back = 0.7071
	var spk []int
	var mix [][]tap
	switch nch {
	case 1:
		spk, mix = []int{spkC}, [][]tap{{{0, 1}}}
	case 2:
		spk, mix = []int{spkL, spkR}, [][]tap{{{0, 1}}, {{1, 1}}}
	case 3: // FL FR FC
		spk, mix = []int{spkL, spkC, spkR}, [][]tap{{{0, 1}}, {{2, 1}}, {{1, 1}}}
	case 4: // FL FR BL BR
		spk, mix = []int{spkL, spkR, spkLs, spkRs}, [][]tap{{{0, 1}}, {{1, 1}}, {{2, 1}}, {{3, 1}}}
	case 5: // FL FR FC BL BR
		spk, mix = []int{spkL, spkC, spkR, spkLs, spkRs}, [][]tap{{{0, 1}}, {{2, 1}}, {{1, 1}}, {{3, 1}}, {{4, 1}}}
	case 6: // FL FR FC LFE BL BR
		spk, mix = []int{spkL, spkC, spkR, spkLs, spkRs, spkLFE}, [][]tap{{{0, 1}}, {{2, 1}}, {{1, 1}}, {{4, 1}}, {{5, 1}}, {{3, 1}}}
	case 7: // FL FR FC BL BR SL SR
		spk, mix = []int{spkL, spkC, spkR, spkLs, spkRs}, [][]tap{{{0, 1}}, {{2, 1}}, {{1, 1}}, {{5, 1}, {3, back}}, {{6, 1}, {4, back}}}
	case 8: // FL FR FC LFE BL BR SL SR
		spk, mix = []int{spkL, spkC, spkR, spkLs, spkRs, spkLFE}, [][]tap{{{0, 1}}, {{2, 1}}, {{1, 1}}, {{6, 1}, {4, back}}, {{7, 1}, {5, back}}, {{3, 1}}}
	default:
		_ = f.Close()
		return nil, fmt.Errorf("%d PCM channels", nch)
	}
	dec := make([]*decimator, len(mix))
	for i := range dec {
		dec[i] = newDecimator(factor)
	}
	scale := 1 / math.Exp2(float64(bits-1))
	bps := bits / 8
	s := &pcmStream{speakers: spk, close: func() { _ = f.Close() }}
	if factor > 1 {
		s.delay = dec[0].delay()
	}
	s.next = func() ([][]float64, error) {
		fr, err := src.Next()
		if err != nil {
			return nil, err
		}
		n := len(fr.Data) / (bps * nch)
		out := make([][]float64, len(mix))
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
					out[oc] = append(out[oc], o)
				}
			}
		}
		return out, nil
	}
	return s, nil
}

// dtsStream decodes a DTS track's cores (a 6.1 core's centre surround
// folded into the surrounds).
func dtsStream(path string) (*pcmStream, error) {
	b, err := os.ReadFile(path) //nolint:gosec // our work directory
	if err != nil {
		return nil, err
	}
	var first *dts.Frame
	for i := 0; i+16 <= len(b); i++ {
		if n := dts.FrameSize(b[i:]); n > 0 && i+n <= len(b) {
			if first, err = (&dts.Decoder{}).Decode(b[i : i+n]); err != nil {
				return nil, err
			}
			break
		}
	}
	if first == nil {
		return nil, errors.New("no DTS core frame")
	}
	if first.SampleRate != 48000 {
		return nil, fmt.Errorf("DTS at %d Hz", first.SampleRate)
	}
	from := map[int]int{spkL: dts.SpeakerL, spkC: dts.SpeakerC, spkR: dts.SpeakerR, spkLs: dts.SpeakerLs, spkRs: dts.SpeakerRs, spkLFE: dts.SpeakerLFE}
	var spk []int
	for _, s := range []int{spkL, spkC, spkR, spkLs, spkRs} {
		if first.Samples[from[s]] != nil {
			spk = append(spk, s)
		}
	}
	if first.Samples[dts.SpeakerLFE] != nil && len(spk) >= 2 {
		spk = append(spk, spkLFE)
	}
	var d dts.Decoder
	i := 0
	const scale = 1.0 / (1 << 23)
	s := &pcmStream{speakers: spk, close: func() {}}
	s.next = func() ([][]float64, error) {
		for i+16 <= len(b) {
			n := dts.FrameSize(b[i:])
			if n == 0 || i+n > len(b) {
				i++ // an extension substream, or garbage: to the next core
				continue
			}
			f, err := d.Decode(b[i : i+n])
			if err != nil {
				return nil, fmt.Errorf("DTS at byte %d: %w", i, err)
			}
			i += n
			cs := f.Samples[dts.SpeakerCs]
			out := make([][]float64, len(spk))
			for c, sp := range spk {
				src := f.Samples[from[sp]]
				out[c] = make([]float64, len(src))
				for k, v := range src {
					x := float64(v) * scale
					if cs != nil && (sp == spkLs || sp == spkRs) {
						x += 0.7071 * float64(cs[k]) * scale
					}
					out[c][k] = x
				}
			}
			return out, nil
		}
		return nil, io.EOF
	}
	return s, nil
}

// codedAudio is a track coded here: the file, where its frames start, the
// samples a frame holds, and the coded audio's delay behind its source.
type codedAudio struct {
	path     string
	offsets  []int64
	frameLen int
	delay    int
	kbps     int
	format   mkv.AudioFormat
}

// codeAudio codes a decoded stream as codec ("ac3" or "aac") into out.
func codeAudio(s *pcmStream, codec, out string) (*codedAudio, error) {
	var lfe bool
	var full []int
	for _, sp := range s.speakers {
		if sp == spkLFE {
			lfe = true
		} else {
			full = append(full, sp)
		}
	}
	// The encoder's channel order, as indexes into the stream's speakers.
	idx := func(order []int) []int {
		var out []int
		for _, want := range order {
			for i, sp := range s.speakers {
				if sp == want {
					out = append(out, i)
				}
			}
		}
		return out
	}
	c := &codedAudio{path: out}
	var encode func([][]float64) ([]byte, error)
	var order []int
	switch codec {
	case "aac":
		// AAC's layouts: C, L R, C L R, C L R Ls Rs, and that with the LFE.
		if len(full) == 4 {
			return nil, errors.New("AAC has no 2/2 layout")
		}
		order = idx([]int{spkC, spkL, spkR, spkLs, spkRs, spkLFE})
		c.kbps = 96 * len(full)
		if lfe {
			c.kbps += 32
		}
		e, err := aac.NewEncoder(len(order), lfe, 48000, c.kbps)
		if err != nil {
			return nil, err
		}
		encode, c.frameLen, c.delay, c.format = e.EncodeFrame, aac.FrameSamples, aac.Delay+s.delay, mkv.AAC
	default:
		order = idx([]int{spkL, spkC, spkR, spkLs, spkRs, spkLFE})
		c.kbps = 640
		if len(full) <= 2 {
			c.kbps = 320
		}
		e, err := ac3.NewEncoder(len(order), lfe, c.kbps)
		if err != nil {
			return nil, err
		}
		encode, c.frameLen, c.delay, c.format = e.EncodeFrame, ac3.FrameSamples, ac3.Delay+s.delay, mkv.AC3
	}
	of, err := os.Create(out) //nolint:gosec // our work directory
	if err != nil {
		return nil, err
	}
	w := bufio.NewWriterSize(of, 1<<20)
	var pos int64
	buf := make([][]float64, len(order))
	put := func(flush bool) error {
		for len(buf[0]) >= c.frameLen || flush && len(buf[0]) > 0 {
			frame := make([][]float64, len(order))
			for i := range frame {
				n := min(len(buf[i]), c.frameLen)
				frame[i] = append(append([]float64(nil), buf[i][:n]...), make([]float64, c.frameLen-n)...)
				buf[i] = buf[i][n:]
			}
			b, err := encode(frame)
			if err != nil {
				return err
			}
			c.offsets = append(c.offsets, pos)
			pos += int64(len(b))
			if _, err := w.Write(b); err != nil {
				return err
			}
		}
		return nil
	}
	err = func() error {
		for {
			chunk, err := s.next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			for i, si := range order {
				buf[i] = append(buf[i], chunk[si]...)
			}
			if err := put(false); err != nil {
				return err
			}
		}
		// The encoder's delay: what is left, then silence for it.
		for i := range buf {
			buf[i] = append(buf[i], make([]float64, c.delay)...)
		}
		return put(true)
	}()
	if ferr := w.Flush(); err == nil {
		err = ferr
	}
	if cerr := of.Close(); err == nil {
		err = cerr
	}
	return c, err
}

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

// delay is the filter's, in output samples.
func (d *decimator) delay() int { return (len(d.taps) - 1) / 2 / d.factor }
