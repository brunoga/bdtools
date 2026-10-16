package ac3

import (
	"errors"
	"fmt"
	"math"
)

// FrameSamples is how many samples per channel a frame codes.
const FrameSamples = 1536

// Delay is how many samples the coded audio lags its input: the first
// frame's first block overlaps silence before the input.
const Delay = 256

// Encoder codes PCM as AC-3 frames.
type Encoder struct {
	fbw     int  // full-bandwidth channels
	lfe     bool // and a low-frequency effects channel
	acmod   int
	rateIdx int // into bitrates
	size    int // bytes a frame
	bw      int // chbwcod
	end     int // the full-bandwidth channels' coded bins
	window  [512]float64
	prev    [6][256]float64 // each channel's last 256 input samples
	frames  int
	// remat is each block's rematrixing flags (stereo): the band's left
	// and right coded as their sum and difference, halved.
	remat [6][4]bool
}

// acmods by full-bandwidth channel count: 1/0, 2/0, 3/0, 2/2, 3/2.
var acmods = [6]int{0, 1, 2, 3, 6, 7}

// NewEncoder makes an encoder for channels (1 to 6; with lfe, 4 or 6, the
// last of them the LFE channel), at 48 kHz and kbps kb/s.
//
// Channels are given in AC-3's order: L, C, R, Ls, Rs (as many as there
// are: L, R for stereo; L, C, R; L, R, Ls, Rs), then LFE.
func NewEncoder(channels int, lfe bool, kbps int) (*Encoder, error) {
	fbw := channels
	if lfe {
		fbw--
	}
	if fbw < 1 || fbw > 5 || lfe && fbw < 2 {
		return nil, fmt.Errorf("ac3: %d channels (lfe %v): want 1 to 5 full-bandwidth ones", channels, lfe)
	}
	e := &Encoder{fbw: fbw, lfe: lfe, acmod: acmods[fbw], rateIdx: -1}
	if fbw == 3 && !lfe {
		e.acmod = 3 // 3/0
	}
	if fbw == 4 {
		e.acmod = 6 // 2/2
	}
	for i, r := range bitrates {
		if r == kbps {
			e.rateIdx = i
		}
	}
	if e.rateIdx < 0 {
		return nil, fmt.Errorf("ac3: %d kb/s is not an AC-3 rate", kbps)
	}
	e.size = kbps * 1000 * FrameSamples / 48000 / 8
	e.bw = bandwidthCode[fbw-1][e.rateIdx]
	if e.bw == 0 && e.rateIdx < 6 {
		return nil, fmt.Errorf("ac3: %d kb/s is too little for %d channels", kbps, channels)
	}
	e.end = 73 + 3*e.bw
	e.initTransform()
	return e, nil
}

// FrameSize is the bytes of each frame.
func (e *Encoder) FrameSize() int { return e.size }

// initTransform makes the Kaiser-Bessel-derived window (alpha 5).
func (e *Encoder) initTransform() {
	const n, alpha = 256, 5.0
	var k [n + 1]float64
	sum := 0.0
	for i := 0; i <= n; i++ {
		x := 2*float64(i)/n - 1
		k[i] = bessel0(math.Pi * alpha * math.Sqrt(1-x*x))
		sum += k[i]
	}
	acc := 0.0
	for i := range n {
		acc += k[i]
		w := math.Sqrt(acc / sum)
		e.window[i] = w
		e.window[511-i] = w
	}
}

func bessel0(x float64) float64 {
	sum, term := 1.0, 1.0
	for k := 1; k < 50; k++ {
		term *= (x / 2) * (x / 2) / float64(k*k)
		sum += term
		if term < 1e-12*sum {
			break
		}
	}
	return sum
}

// block is one channel's audio block: its coefficients, exponents and
// bit allocation.
type block struct {
	coef     [256]float64
	exp      [256]int // as coded (the decoder's)
	raw      [256]int // the coefficients' own
	expStrat int
	bap      [256]uint8
	psd      [256]int
	bndPSD   [50]int
	mask     [50]int
	curves   bool // psd and mask computed
}

// EncodeFrame codes FrameSamples samples of each channel (in [-1, 1], AC-3
// order) as one frame.
func (e *Encoder) EncodeFrame(pcm [][]float64) ([]byte, error) {
	nch := e.fbw + boolInt(e.lfe)
	if len(pcm) != nch {
		return nil, errors.New("ac3: wrong channel count")
	}
	var blks [6][6]block // [block][channel]
	for ch := range nch {
		if len(pcm[ch]) < FrameSamples {
			return nil, errors.New("ac3: a short frame")
		}
		for b := range 6 {
			var in [512]float64
			copy(in[:256], e.prev[ch][:])
			copy(in[256:], pcm[ch][b*256:(b+1)*256])
			copy(e.prev[ch][:], in[256:])
			for i := range in {
				in[i] *= e.window[i]
			}
			var out [256]float64
			mdct(&in, &out, -2.0/512)
			bl := &blks[b][ch]
			n := e.end
			if e.lfe && ch == nch-1 {
				n = 7
			}
			copy(bl.coef[:n], out[:n])
		}
	}
	if e.acmod == 2 {
		e.rematrix(&blks)
	}
	// Exponents: each coefficient's, then the strategies and the coded ones.
	for ch := range nch {
		n := e.end
		lfe := e.lfe && ch == nch-1
		if lfe {
			n = 7
		}
		for b := range 6 {
			bl := &blks[b][ch]
			for k := range n {
				bl.raw[k] = exponent(bl.coef[k])
			}
		}
		e.exponents(&blks, ch, n, lfe)
	}
	// Bit allocation: the bits the frame has left for mantissas, the
	// largest SNR offset whose mantissas fit, then a finer step for the
	// channels the bits left over still pay for.
	fixed := e.fixedBits(&blks)
	avail := e.size*8 - fixed
	all := func(v int) [6]int {
		var s [6]int
		for i := range s {
			s[i] = v
		}
		return s
	}
	cache := map[[2]int][6]bapCount{}
	lo, hi := 0, 63*16+15
	best := -1
	for lo <= hi {
		mid := (lo + hi) / 2
		if e.mantissaBits(&blks, cache, all(mid)) <= avail {
			best, lo = mid, mid+1
		} else {
			hi = mid - 1
		}
	}
	if best < 0 {
		return nil, errors.New("ac3: the frame cannot hold its exponents")
	}
	// One coarse offset for the frame: each channel's fine one may rise
	// while it stays within the coarse step and the bits last.
	snr := all(best)
	for more := true; more; {
		more = false
		for ch := range nch {
			if snr[ch]&15 == 15 {
				continue
			}
			try := snr
			try[ch]++
			if e.mantissaBits(&blks, cache, try) <= avail {
				snr, more = try, true
			}
		}
	}
	e.allocate(&blks, snr)
	frame := e.write(&blks, snr)
	e.frames++
	return frame, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// exponent is a coefficient's: the shift that brings it into [0.5, 1),
// 24 for (near) zero.
func exponent(c float64) int {
	a := math.Abs(c)
	if a < 1.0/(1<<24) {
		return 24
	}
	_, e := math.Frexp(a) // a = f * 2^e, f in [0.5, 1)
	return min(max(-e, 0), 24)
}

// exponents chooses a channel's exponent strategies over the frame
// (reuse while the exponents stay near, coarser the more often they are
// sent) and codes them: each block's exps are what the decoder rebuilds.
func (e *Encoder) exponents(blks *[6][6]block, ch, n int, lfe bool) {
	var expStrat [6]int
	expStrat[0] = expD15
	if lfe {
		for b := 1; b < 6; b++ {
			expStrat[b] = expReuse
		}
	} else {
		for b := 1; b < 6; b++ {
			diff := 0
			for k := range n {
				d := blks[b][ch].raw[k] - blks[b-1][ch].raw[k]
				if d < 0 {
					d = -d
				}
				diff += d
			}
			expStrat[b] = expReuse
			if diff > 500*n/256 {
				expStrat[b] = expD15
			}
		}
		for b := 0; b < 6; {
			b1 := b + 1
			for b1 < 6 && expStrat[b1] == expReuse {
				b1++
			}
			expStrat[b] = [6]int{expD45, expD25, expD25, expD15, expD15, expD15}[b1-b-1]
			b = b1
		}
	}
	for b := 0; b < 6; {
		b1 := b + 1
		for b1 < 6 && expStrat[b1] == expReuse {
			b1++
		}
		// The run's exponents: each bin's smallest, so every block's
		// coefficients fit.
		var ex [256]int
		for k := range n {
			ex[k] = 24
			for r := b; r < b1; r++ {
				ex[k] = min(ex[k], blks[r][ch].raw[k])
			}
		}
		codeExponents(ex[:n], expStrat[b])
		for r := b; r < b1; r++ {
			blks[r][ch].expStrat = expReuse
			copy(blks[r][ch].exp[:n], ex[:n])
		}
		blks[b][ch].expStrat = expStrat[b]
		b = b1
	}
}

// groupSize is the bins an exponent covers by strategy.
var groupSize = [4]int{0, 1, 2, 4}

// groups is how many groups of three exponent differences a channel of n
// coded bins sends with strategy s.
func groups(n, s int) int {
	switch s {
	case expD15:
		return (n - 1) / 3
	case expD25:
		return (n - 1 + 3) / 6
	}
	return (n - 1 + 9) / 12
}

// codeExponents turns exponents into what the strategy can send: the DC
// one at most 15, one per group of bins (the group's smallest), each
// within 2 of the one before; ex then holds what the decoder rebuilds.
func codeExponents(ex []int, s int) {
	n := len(ex)
	gs := groupSize[s]
	units := groups(n, s) * 3
	u := make([]int, units+1)
	u[0] = min(ex[0], 15)
	for i := 1; i <= units; i++ {
		v := 24
		for k := 1 + (i-1)*gs; k < 1+i*gs && k < n; k++ {
			v = min(v, ex[k])
		}
		u[i] = v
	}
	for i := 1; i <= units; i++ {
		u[i] = min(u[i], u[i-1]+2)
	}
	for i := units - 1; i >= 0; i-- {
		u[i] = min(u[i], u[i+1]+2)
	}
	ex[0] = u[0]
	for i := 1; i <= units; i++ {
		for k := 1 + (i-1)*gs; k < 1+i*gs && k < n; k++ {
			ex[k] = u[i]
		}
	}
}

// unitsOf gives the coded exponent units of a channel's exponents (DC
// first), as codeExponents left them.
func unitsOf(ex []int, s int) []int {
	n := len(ex)
	gs := groupSize[s]
	units := groups(n, s) * 3
	u := make([]int, units+1)
	u[0] = ex[0]
	for i := 1; i <= units; i++ {
		k := 1 + (i-1)*gs
		if k < n {
			u[i] = ex[k]
		} else {
			u[i] = u[i-1]
		}
	}
	return u
}

// psdMask computes a channel block's bit allocation curves (A/52 7.2):
// the power spectral density and the masking curve.
func psdMask(bl *block, n int, lfe bool) {
	for k := range n {
		bl.psd[k] = 3072 - bl.exp[k]<<7
	}
	k := 0
	band := 0
	for k < n {
		v := bl.psd[k]
		k++
		bend := min(bandStart[band+1], n)
		for ; k < bend; k++ {
			mx := max(v, bl.psd[k])
			adr := min(mx-((v+bl.psd[k]+1)>>1), 255)
			v = mx + int(logAdd[adr])
		}
		bl.bndPSD[band] = v
		band++
	}
	var excite [50]int
	bandEnd := min(binBand[n-1]+1, len(excite))
	lowcomp := calcLowcomp1(0, bl.bndPSD[0], bl.bndPSD[1], 384)
	excite[0] = bl.bndPSD[0] - fastGain - lowcomp
	lowcomp = calcLowcomp1(lowcomp, bl.bndPSD[1], bl.bndPSD[2], 384)
	excite[1] = bl.bndPSD[1] - fastGain - lowcomp
	begin := 7
	var fastleak, slowleak int
	for b := 2; b < 7; b++ {
		if !lfe || b != 6 {
			lowcomp = calcLowcomp1(lowcomp, bl.bndPSD[b], bl.bndPSD[b+1], 384)
		}
		fastleak = bl.bndPSD[b] - fastGain
		slowleak = bl.bndPSD[b] - slowGain
		excite[b] = fastleak - lowcomp
		if (!lfe || b != 6) && bl.bndPSD[b] <= bl.bndPSD[b+1] {
			begin = b + 1
			break
		}
	}
	end1 := min(bandEnd, 22)
	for b := begin; b < end1; b++ {
		if !lfe || b != 6 {
			lowcomp = calcLowcomp(lowcomp, bl.bndPSD[b], bl.bndPSD[b+1], b)
		}
		fastleak = max(fastleak-fastDecay, bl.bndPSD[b]-fastGain)
		slowleak = max(slowleak-slowDecay, bl.bndPSD[b]-slowGain)
		excite[b] = max(fastleak-lowcomp, slowleak)
	}
	for b := 22; b < bandEnd; b++ {
		fastleak = max(fastleak-fastDecay, bl.bndPSD[b]-fastGain)
		slowleak = max(slowleak-slowDecay, bl.bndPSD[b]-slowGain)
		excite[b] = max(fastleak, slowleak)
	}
	for b := range excite[:bandEnd] {
		if t := dbPerBit - bl.bndPSD[b]; t > 0 {
			excite[b] += t >> 2
		}
		bl.mask[b] = max(hearing[b], excite[b])
	}
}

func calcLowcomp1(a, b0, b1, c int) int {
	switch {
	case b0+256 == b1:
		return c
	case b0 > b1:
		return max(a-64, 0)
	}
	return a
}

func calcLowcomp(a, b0, b1, bin int) int {
	switch {
	case bin < 7:
		return calcLowcomp1(a, b0, b1, 384)
	case bin < 20:
		return calcLowcomp1(a, b0, b1, 320)
	}
	return max(a-128, 0)
}

// bap computes a channel block's bit allocation pointers at the SNR
// offset (the coarse and fine offsets as one number, 16 fine steps a
// coarse one).
func bap(bl *block, n, snr int) {
	off := (snr - 15*16) << 2
	for band := 0; bandStart[band] < n; band++ {
		m := bl.mask[band] - off - floorVal
		if m < 0 {
			m = 0
		}
		m &= 0x1fe0
		m += floorVal
		for k := bandStart[band]; k < min(bandStart[band+1], n); k++ {
			a := (bl.psd[k] - m) >> 5
			bl.bap[k] = bapTab[min(max(a, 0), 63)]
		}
	}
}

// coded is the coded bins of channel ch.
func (e *Encoder) coded(ch int) int {
	if e.lfe && ch == e.fbw {
		return 7
	}
	return e.end
}

// allocate computes every block's bit allocation at snr.
func (e *Encoder) allocate(blks *[6][6]block, snr [6]int) {
	nch := e.fbw + boolInt(e.lfe)
	for b := range 6 {
		for ch := range nch {
			n := e.coded(ch)
			bl := &blks[b][ch]
			if !bl.curves {
				psdMask(bl, n, e.lfe && ch == e.fbw)
				bl.curves = true
			}
			bap(bl, n, snr[ch])
		}
	}
}

// bapCount is a channel block's mantissas at an SNR offset: how many at
// baps 1, 2 and 4 (grouped across the block's channels) and the bits of
// the rest.
type bapCount struct{ n1, n2, n4, bits int }

// channelCounts gives channel ch's counts in each block at snr, computed
// once per frame for each offset tried.
func (e *Encoder) channelCounts(blks *[6][6]block, cache map[[2]int][6]bapCount, ch, snr int) [6]bapCount {
	if c, ok := cache[[2]int{ch, snr}]; ok {
		return c
	}
	var c [6]bapCount
	n := e.coded(ch)
	for b := range 6 {
		bl := &blks[b][ch]
		if !bl.curves {
			psdMask(bl, n, e.lfe && ch == e.fbw)
			bl.curves = true
		}
		bap(bl, n, snr)
		cnt := &c[b]
		for k := range n {
			switch v := bl.bap[k]; v {
			case 0:
			case 1:
				cnt.n1++
			case 2:
				cnt.n2++
			case 4:
				cnt.n4++
			case 3:
				cnt.bits += 3
			case 5:
				cnt.bits += 4
			case 14:
				cnt.bits += 14
			case 15:
				cnt.bits += 16
			default:
				cnt.bits += int(v) - 1
			}
		}
	}
	cache[[2]int{ch, snr}] = c
	return c
}

// mantissaBits counts the frame's mantissa bits at snr, grouped as they
// are written: three to a group at baps 1 and 2, two at 4, within a block.
func (e *Encoder) mantissaBits(blks *[6][6]block, cache map[[2]int][6]bapCount, snr [6]int) int {
	nch := e.fbw + boolInt(e.lfe)
	var sum [6]bapCount
	for ch := range nch {
		c := e.channelCounts(blks, cache, ch, snr[ch])
		for b := range 6 {
			sum[b].n1 += c[b].n1
			sum[b].n2 += c[b].n2
			sum[b].n4 += c[b].n4
			sum[b].bits += c[b].bits
		}
	}
	bits := 0
	for _, s := range sum {
		bits += s.bits + (s.n1+2)/3*5 + (s.n2+2)/3*7 + (s.n4+1)/2*7
	}
	return bits
}

// fixedBits counts the frame's bits other than its mantissas: the header,
// each block's side information and exponents, the CRCs.
func (e *Encoder) fixedBits(blks *[6][6]block) int {
	w := &bitWriter{}
	e.writeHeader(w)
	for b := range 6 {
		e.writeBlockSide(w, blks, b, [6]int{})
	}
	return w.n + 16 // and crc2
}

// rematrix decides each block's rematrixing (stereo, A/52 7.5): a band is
// coded as the sum and difference of its channels, halved, when the
// smaller of those has less energy than the smaller channel (the decoder
// takes left as their sum, right as their difference).
func (e *Encoder) rematrix(blks *[6][6]block) {
	for b := range 6 {
		l, r := &blks[b][0].coef, &blks[b][1].coef
		for band := range 4 {
			var el, er, em, es float64
			hi := min(rematrixBands[band+1], e.end)
			for k := rematrixBands[band]; k < hi; k++ {
				m, s := (l[k]+r[k])/2, (l[k]-r[k])/2
				el += l[k] * l[k]
				er += r[k] * r[k]
				em += m * m
				es += s * s
			}
			e.remat[b][band] = min(em, es) < min(el, er)
			if !e.remat[b][band] {
				continue
			}
			for k := rematrixBands[band]; k < hi; k++ {
				l[k], r[k] = (l[k]+r[k])/2, (l[k]-r[k])/2
			}
		}
	}
}
