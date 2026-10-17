// Package aac encodes AAC-LC (ISO/IEC 14496-3): PCM in, ADTS frames out,
// mono to 5.1 at 44.1 or 48 kHz. Long blocks, a sine window, mid/side
// stereo, a rate loop with a bit reservoir over band thresholds that
// follow the signal's energy.
package aac

import (
	"errors"
	"fmt"
	"math"
)

// FrameSamples is a frame's samples per channel.
const FrameSamples = 1024

// Delay is how many samples the decoded audio lags the input.
const Delay = 1024

// Encoder codes PCM as AAC-LC.
type Encoder struct {
	rate, rateIndex int
	channels        int
	lfe             bool
	kbps            int
	maxSfb          int // the bands coded, by the bandwidth
	elements        []element
	window          [2048]float64
	mdct            *mdctN
	prev            [][]float64 // each channel's last frame
	reservoir       int         // bits saved for later frames
	// prevMask is each channel's masking threshold of the frame before,
	// by band (a threshold may fall only so fast, as a masker's effect
	// lingers).
	prevMask [][49]float64
	// lastNoise is the noise scale the frame before took.
	lastNoise float64
}

// element is a syntactic element: its type and the input channels it
// codes.
type element struct {
	typ int // idSCE, idCPE, idLFE
	ch  []int
}

const (
	idSCE = 0
	idCPE = 1
	idLFE = 3
	idEND = 7
)

// NewEncoder makes an encoder for channels at rate (44100 or 48000) and
// kbps kb/s. Channels go in AAC's order: C (mono), L R (stereo), C L R,
// C L R S, C L R Ls Rs, C L R Ls Rs LFE (lfe set, 6 channels).
func NewEncoder(channels int, lfe bool, rate, kbps int) (*Encoder, error) {
	e := &Encoder{rate: rate, channels: channels, lfe: lfe, kbps: kbps}
	switch rate {
	case 48000:
		e.rateIndex = 3
	case 44100:
		e.rateIndex = 4
	default:
		return nil, fmt.Errorf("aac: %d Hz: want 44100 or 48000", rate)
	}
	switch {
	case channels == 1:
		e.elements = []element{{idSCE, []int{0}}}
	case channels == 2:
		e.elements = []element{{idCPE, []int{0, 1}}}
	case channels == 3:
		e.elements = []element{{idSCE, []int{0}}, {idCPE, []int{1, 2}}}
	case channels == 5:
		e.elements = []element{{idSCE, []int{0}}, {idCPE, []int{1, 2}}, {idCPE, []int{3, 4}}}
	case channels == 6 && lfe:
		e.elements = []element{{idSCE, []int{0}}, {idCPE, []int{1, 2}}, {idCPE, []int{3, 4}}, {idLFE, []int{5}}}
	default:
		return nil, fmt.Errorf("aac: %d channels (lfe %v)", channels, lfe)
	}
	if kbps < 16 || kbps > 128*channels {
		return nil, fmt.Errorf("aac: %d kb/s for %d channels", kbps, channels)
	}
	// The bandwidth, by the rate each full-bandwidth channel gets.
	full := channels
	if lfe {
		full--
	}
	per := kbps / full
	cutoff := 20000.0
	switch {
	case per < 40:
		cutoff = 12000
	case per < 56:
		cutoff = 15000
	case per < 72:
		cutoff = 16500
	case per < 96:
		cutoff = 18000
	}
	bin := int(cutoff * 2048 / float64(rate))
	e.maxSfb = 49
	for b := range 49 {
		if int(swbOffset1024_48[b]) >= bin {
			e.maxSfb = b
			break
		}
	}
	for i := range 2048 {
		e.window[i] = math.Sin(math.Pi / 2048 * (float64(i) + 0.5))
	}
	e.mdct = newMDCT(1024)
	e.prevMask = make([][49]float64, channels)
	e.prev = make([][]float64, channels)
	for i := range e.prev {
		e.prev[i] = make([]float64, FrameSamples)
	}
	return e, nil
}

// Config is the stream's AudioSpecificConfig.
func (e *Encoder) Config() []byte {
	cfg := e.channels
	if e.channels == 6 {
		cfg = 6
	}
	return []byte{byte(2<<3 | e.rateIndex>>1), byte(e.rateIndex&1<<7 | cfg<<3)}
}

// spectrum is a channel's frame: its coefficients and, once coded, its
// bands' scale factors, quantized values and code books.
type spectrum struct {
	coef  [1024]float64
	c34   [1024]float64 // |coef|^(3/4)
	sf    [49]int
	book  [49]int
	quant [1024]int
	nsfb  int
	mask  [49]float64 // the noise each band masks
}

// EncodeFrame codes FrameSamples samples per channel (in [-1, 1]) as one
// ADTS frame.
func (e *Encoder) EncodeFrame(pcm [][]float64) ([]byte, error) {
	if len(pcm) != e.channels {
		return nil, errors.New("aac: wrong channel count")
	}
	specs := make([]*spectrum, e.channels)
	var in [2048]float64
	for ch := range e.channels {
		if len(pcm[ch]) < FrameSamples {
			return nil, errors.New("aac: a short frame")
		}
		copy(in[:1024], e.prev[ch])
		copy(in[1024:], pcm[ch][:FrameSamples])
		copy(e.prev[ch], pcm[ch][:FrameSamples])
		for i := range in {
			in[i] *= e.window[i]
		}
		s := &spectrum{}
		copy(s.coef[:], e.mdct.transform(in[:]))
		for k := range s.coef {
			s.coef[k] *= coefScale
		}
		specs[ch] = s
	}
	// Mid/side for each pair, band by band.
	ms := make([][49]bool, len(e.elements))
	for i, el := range e.elements {
		if el.typ == idCPE {
			ms[i] = e.midSide(specs[el.ch[0]], specs[el.ch[1]])
		}
	}
	for ch, s := range specs {
		for k, v := range s.coef {
			s.c34[k] = math.Pow(math.Abs(v), 0.75)
		}
		e.mask(s, ch)
	}
	// The rate loop: the noise allowed, scaled until the frame fits.
	avg := e.kbps * 1000 * FrameSamples / e.rate
	budget := avg + min(e.reservoir, 6144*e.channels-avg)
	// log2 of the noise scale: near the frame before's.
	lo, hi := e.lastNoise-8, e.lastNoise+8
	var best []byte
	for range 9 {
		mid := (lo + hi) / 2
		for ch, s := range specs {
			e.quantize(s, ch, math.Exp2(mid))
		}
		frame := e.write(specs, ms)
		if 8*len(frame) <= budget {
			best, hi = frame, mid
			e.lastNoise = mid
		} else {
			lo = mid
		}
	}
	if best == nil {
		for ch, s := range specs {
			e.quantize(s, ch, math.Exp2(hi))
		}
		best = e.write(specs, ms)
	}
	e.reservoir += avg - 8*len(best)
	e.reservoir = min(max(e.reservoir, 0), 6144*e.channels)
	return best, nil
}

// coefScale brings the transform's coefficients to the decoder's scale
// (measured: a decoder gives back the input at it).
const coefScale = 65536

// midSide codes a band of a pair as mid and side where that leaves less
// to code, in place.
func (e *Encoder) midSide(l, r *spectrum) (used [49]bool) {
	for b := range e.maxSfb {
		var el, er, em, es float64
		for k := swbOffset1024_48[b]; k < swbOffset1024_48[b+1]; k++ {
			m, s := (l.coef[k]+r.coef[k])/2, (l.coef[k]-r.coef[k])/2
			el += l.coef[k] * l.coef[k]
			er += r.coef[k] * r.coef[k]
			em += m * m
			es += s * s
		}
		if min(em, es) < 0.5*min(el, er) {
			used[b] = true
			for k := swbOffset1024_48[b]; k < swbOffset1024_48[b+1]; k++ {
				l.coef[k], r.coef[k] = (l.coef[k]+r.coef[k])/2, (l.coef[k]-r.coef[k])/2
			}
		}
	}
	return used
}

// quantize chooses each band's scale factor, the largest whose noise stays
// within the band's allowance (noise times its share), and quantizes.
func (e *Encoder) quantize(s *spectrum, ch int, noise float64) {
	nsfb := e.maxSfb
	if e.lfe && ch == e.channels-1 {
		nsfb = 3 // under 300 Hz
	}
	s.nsfb = nsfb
	prevSF := -1
	for b := range nsfb {
		lo, hi := int(swbOffset1024_48[b]), int(swbOffset1024_48[b+1])
		var energy, peak float64
		for _, v := range s.coef[lo:hi] {
			energy += v * v
			peak = max(peak, math.Abs(v))
		}
		allowed := noise * s.mask[b]
		if peak == 0 || energy <= allowed {
			s.book[b] = 0
			clear(s.quant[lo:hi])
			continue
		}
		// The scale factor: coarse to fine, the largest within allowance;
		// at least the one keeping every value under 8192.
		sfMin := min(max(int(math.Ceil(100+4*math.Log2(peak/math.Pow(8191, 4.0/3)))), 0), 255)
		sf := sfMin
		for step := 64; step > 0; step >>= 1 {
			if t := sf + step; t <= 255 && distortion(s.coef[lo:hi], s.c34[lo:hi], t) <= allowed {
				sf = t
			}
		}
		if prevSF >= 0 {
			sf = min(max(sf, prevSF-60), prevSF+60)
		}
		s.sf[b] = sf
		zero := true
		for k := lo; k < hi; k++ {
			s.quant[k] = quant(s.coef[k], s.c34[k], sf)
			if s.quant[k] != 0 {
				zero = false
			}
		}
		if zero {
			s.book[b] = 0
			continue
		}
		s.book[b] = 1 // chosen when sectioned
		prevSF = sf
	}
	// The scale factors of zero bands are not sent: the differences
	// between the coded ones must stay within 60 (clamped above), and the
	// first is the global gain.
}

// The psychoacoustic model: a band's masking threshold is the energy of
// the bands around it spread over the Bark scale (upward more than
// downward), some way below them, and never under the absolute threshold
// of hearing.
const (
	spreadUp   = 15.0 // dB per Bark the masking falls toward higher bands
	spreadDown = 27.0 // and toward lower ones
	maskOffset = 26.0 // dB the threshold sits below the spread energy
)

// barkOf is a band's centre on the Bark scale.
var barkOf = func() (z [49]float64) {
	for b := range 49 {
		f := (float64(swbOffset1024_48[b]) + float64(swbOffset1024_48[b+1])) / 2 * 48000 / 2048
		z[b] = 13*math.Atan(0.00076*f) + 3.5*math.Atan((f/7500)*(f/7500))
	}
	return z
}()

// athEnergy is the absolute threshold of hearing in the band, as energy in
// the coefficient domain (a full-scale tone is about 2^31 there).
func athEnergy(b, rate int) float64 {
	lo, hi := float64(swbOffset1024_48[b]), float64(swbOffset1024_48[b+1])
	f := (lo + hi) / 2 * float64(rate) / 2048 / 1000 // kHz
	f = max(f, 0.03)
	db := 3.64*math.Pow(f, -0.8) - 6.5*math.Exp(-0.6*(f-3.3)*(f-3.3)) + 1e-3*math.Pow(f, 4)
	// 96 dB SPL is full scale; the threshold's power, spread over the band.
	full := float64(coefScale) * float64(coefScale) * 1024 / 4
	return full * math.Pow(10, (db-96)/10) * (hi - lo) / 4
}

// mask computes the frame's masking threshold for each band.
func (e *Encoder) mask(s *spectrum, ch int) {
	var energy [49]float64
	for b := range e.maxSfb {
		for k := swbOffset1024_48[b]; k < swbOffset1024_48[b+1]; k++ {
			energy[b] += s.coef[k] * s.coef[k]
		}
	}
	for b := range e.maxSfb {
		var spread float64
		for j := range e.maxSfb {
			dz := barkOf[b] - barkOf[j]
			att := spreadUp * dz
			if dz < 0 {
				att = -spreadDown * dz
			}
			if att < 60 {
				spread += energy[j] * math.Pow(10, -att/10)
			}
		}
		t := spread * math.Pow(10, -maskOffset/10)
		// What masked the frame before still masks some.
		t = max(t, 0.25*e.prevMask[ch][b])
		s.mask[b] = max(t, athEnergy(b, e.rate))
	}
	for b := range e.maxSfb {
		e.prevMask[ch][b] = s.mask[b]
	}
}

// quantGain and dequantGain are 2^(-3/16 (sf-100)) and 2^(1/4 (sf-100));
// pow43 is q^(4/3), by q.
var quantGain, dequantGain = func() (q, d [256]float64) {
	for sf := range 256 {
		q[sf] = math.Exp2(-0.1875 * float64(sf-100))
		d[sf] = math.Exp2(0.25 * float64(sf-100))
	}
	return q, d
}()

var pow43 = func() (t [8192]float64) {
	for i := range t {
		t[i] = math.Pow(float64(i), 4.0/3)
	}
	return t
}()

// quant is a coefficient's quantized value at scale factor sf, from v and
// |v|^(3/4).
func quant(v, v34 float64, sf int) int {
	q := min(int(v34*quantGain[sf]+0.4054), 8191)
	if v < 0 {
		return -q
	}
	return q
}

// distortion is the squared error of a band quantized at sf.
func distortion(c, c34 []float64, sf int) float64 {
	var d float64
	g, dg := quantGain[sf], dequantGain[sf]
	for k, v := range c {
		q := min(int(c34[k]*g+0.4054), 8191)
		e := math.Abs(v) - pow43[q]*dg
		d += e * e
	}
	return d
}
