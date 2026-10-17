// Package dts decodes DTS Coherent Acoustics core audio (ETSI TS 102 114):
// the core of a DTS or DTS-HD stream (its extension substreams left
// aside), up to 5.1 at 48 kHz, in fixed point, as the reference decoder
// does it.
package dts

import (
	"errors"
	"fmt"
	"math/bits"
)

// Speakers, as Decoder.Decode gives the channels: the core's primary
// channels in this order (those present), then the LFE.
const (
	SpeakerC = iota
	SpeakerL
	SpeakerR
	SpeakerLs
	SpeakerRs
	SpeakerLFE
	SpeakerCs
)

var errStream = errors.New("dts: an invalid core frame")

const (
	subbands       = 32
	subbandSamples = 8
	adpcmCoeffs    = 4
	lfeHistory     = 8
	maxChannels    = 7
)

// The primary channels' speakers by audio mode.
var primarySpeakers = [10][5]int{
	{SpeakerC, -1, -1, -1, -1},
	{SpeakerL, SpeakerR, -1, -1, -1},
	{SpeakerL, SpeakerR, -1, -1, -1},
	{SpeakerL, SpeakerR, -1, -1, -1},
	{SpeakerL, SpeakerR, -1, -1, -1},
	{SpeakerC, SpeakerL, SpeakerR, -1, -1},
	{SpeakerL, SpeakerR, SpeakerCs, -1, -1},
	{SpeakerC, SpeakerL, SpeakerR, SpeakerCs, -1},
	{SpeakerL, SpeakerR, SpeakerLs, SpeakerRs, -1},
	{SpeakerC, SpeakerL, SpeakerR, SpeakerLs, SpeakerRs},
}

var blockCodeBits = [7]int{7, 10, 12, 13, 15, 17, 19}

// Decoder decodes core frames one after another, keeping the prediction
// and filter history between them.
type Decoder struct {
	// Header.
	crcPresent, predHistory, filterPerfect bool
	npcmblocks, frameSize, audioMode       int
	sampleRate, bitRate                    int
	drcPresent, syncSSF                    bool
	lfePresent                             int
	sumdiffFront, sumdiffSurround          bool
	// Coding header.
	nsubframes, nchannels int
	nsubbands             [maxChannels]int
	vqStart               [maxChannels]int
	jointIndex            [maxChannels]int
	tmodeSel              [maxChannels]int
	scaleSel              [maxChannels]int
	bitallocSel           [maxChannels]int
	quantSel              [maxChannels][10]int
	scaleAdj              [maxChannels][10]int32
	// Subframe.
	nssf      int
	predMode  [maxChannels][subbands]bool
	predIndex [maxChannels][subbands]int
	bitalloc  [maxChannels][subbands]int
	tmode     [maxChannels][subbands]int
	scales    [maxChannels][subbands][2]int32
	jointSel  [maxChannels]int
	jointScl  [maxChannels][subbands]int32
	// Samples: per channel and subband, the ADPCM history then the frame's.
	sub   [maxChannels][subbands][]int32
	lfe   []int32
	synth [maxChannels]synthState
}

// synthState is a channel's synthesis filter history.
type synthState struct {
	hist1  [1024]int32
	offset int
	hist2  [32]int32
}

// Frame is a decoded frame: each speaker's 24-bit samples (nil for those
// absent), at SampleRate.
type Frame struct {
	SampleRate int
	Samples    [7][]int32
}

// FrameSize reads a core frame's size from its first bytes (at least 10),
// 0 if they are no core frame header.
func FrameSize(b []byte) int {
	if len(b) < 10 || b[0] != 0x7f || b[1] != 0xfe || b[2] != 0x80 || b[3] != 0x01 {
		return 0
	}
	r := &bitReader{b: b, pos: 32 + 1 + 5 + 1 + 7}
	return r.u(14) + 1
}

// Decode decodes one core frame (from its sync word; anything after its
// size, the extension substreams, is left aside).
func (d *Decoder) Decode(b []byte) (*Frame, error) {
	r := &bitReader{b: b}
	if err := d.header(r); err != nil {
		return nil, err
	}
	if d.frameSize > len(b) {
		return nil, errStream
	}
	r.b = b[:d.frameSize]
	d.alloc()
	if err := d.codingHeader(r); err != nil {
		return nil, err
	}
	subPos, lfePos := 0, lfeHistory
	for range d.nsubframes {
		if err := d.subframeHeader(r); err != nil {
			return nil, err
		}
		if err := d.subframeAudio(r, &subPos, &lfePos); err != nil {
			return nil, err
		}
		if r.left() < 0 {
			return nil, errStream
		}
	}
	for ch := range d.nchannels {
		n := d.nsubbands[ch]
		if j := d.jointIndex[ch]; j > 0 {
			n = max(n, d.nsubbands[j-1])
		}
		for band := range subbands {
			s := d.sub[ch][band]
			if band < n {
				copy(s[:adpcmCoeffs], s[d.npcmblocks:d.npcmblocks+adpcmCoeffs])
			} else {
				clear(s)
			}
		}
	}
	return d.filter()
}

func (d *Decoder) header(r *bitReader) error {
	if r.u(32) != 0x7ffe8001 {
		return errStream
	}
	r.skip(1) // normal frame
	if r.u(5)+1 != 32 {
		return errors.New("dts: deficit samples")
	}
	d.crcPresent = r.flag()
	d.npcmblocks = r.u(7) + 1
	if d.npcmblocks&(subbandSamples-1) != 0 || d.npcmblocks < 8 {
		return errStream
	}
	d.frameSize = r.u(14) + 1
	if d.frameSize < 96 {
		return errStream
	}
	d.audioMode = r.u(6)
	if d.audioMode >= 10 {
		return fmt.Errorf("dts: audio mode %d", d.audioMode)
	}
	d.sampleRate = int(sampleRates[r.u(4)])
	if d.sampleRate == 0 {
		return errStream
	}
	d.bitRate = int(bitRates[r.u(5)])
	if r.flag() {
		return errStream
	}
	d.drcPresent = r.flag()
	r.skip(1 + 1 + 1 + 3 + 1) // time stamp, aux data, HDCD, extension type and flag
	d.syncSSF = r.flag()
	d.lfePresent = r.u(2)
	if d.lfePresent == 3 {
		return errStream
	}
	if d.lfePresent == 1 {
		return errors.New("dts: an LFE of 128 times interpolation")
	}
	d.predHistory = r.flag()
	if d.crcPresent {
		r.skip(16)
	}
	d.filterPerfect = r.flag()
	r.skip(4 + 2) // encoder revision, copy history
	pcmr := r.u(3)
	if pcmr == 4 || pcmr == 7 {
		return errStream
	}
	d.sumdiffFront = r.flag()
	d.sumdiffSurround = r.flag()
	r.skip(4) // dialog normalization
	return nil
}

// alloc sizes the sample buffers for the frame, keeping their history.
func (d *Decoder) alloc() {
	n := adpcmCoeffs + d.npcmblocks
	for ch := range maxChannels {
		for band := range subbands {
			if cap(d.sub[ch][band]) < n {
				s := make([]int32, n)
				copy(s, d.sub[ch][band])
				d.sub[ch][band] = s
			}
			d.sub[ch][band] = d.sub[ch][band][:n]
			if !d.predHistory {
				clear(d.sub[ch][band][:adpcmCoeffs])
			}
		}
	}
	nl := lfeHistory + d.npcmblocks/2
	if cap(d.lfe) < nl {
		l := make([]int32, nl)
		copy(l, d.lfe)
		d.lfe = l
	}
	d.lfe = d.lfe[:nl]
}

func (d *Decoder) codingHeader(r *bitReader) error {
	d.nsubframes = r.u(4) + 1
	d.nchannels = r.u(3) + 1
	if d.nchannels != int(channelCount[d.audioMode]) || d.nchannels > 5 {
		return errStream
	}
	for ch := range d.nchannels {
		d.nsubbands[ch] = r.u(5) + 2
		if d.nsubbands[ch] > subbands {
			return errStream
		}
	}
	for ch := range d.nchannels {
		d.vqStart[ch] = r.u(5) + 1
	}
	for ch := range d.nchannels {
		d.jointIndex[ch] = r.u(3)
		if d.jointIndex[ch] > d.nchannels {
			return errStream
		}
	}
	for ch := range d.nchannels {
		d.tmodeSel[ch] = r.u(2)
	}
	for ch := range d.nchannels {
		if d.scaleSel[ch] = r.u(3); d.scaleSel[ch] == 7 {
			return errStream
		}
	}
	for ch := range d.nchannels {
		if d.bitallocSel[ch] = r.u(3); d.bitallocSel[ch] == 7 {
			return errStream
		}
	}
	for n := range 10 {
		for ch := range d.nchannels {
			d.quantSel[ch][n] = r.u(int(quantIndexSelBits[n]))
		}
	}
	for n := range 10 {
		for ch := range d.nchannels {
			if d.quantSel[ch][n] < int(quantIndexGroupSize[n]) {
				d.scaleAdj[ch][n] = scaleFactorAdj[r.u(2)]
			}
		}
	}
	if d.crcPresent {
		r.skip(16)
	}
	return nil
}

func (d *Decoder) scale(r *bitReader, idx *int, sel int) (int32, error) {
	table := scaleFactorQuant6[:]
	if sel > 5 {
		table = scaleFactorQuant7[:]
	}
	if sel < 5 {
		*idx += books.scale[sel].read(r)
	} else {
		*idx = r.u(sel + 1)
	}
	if *idx < 0 || *idx >= len(table) {
		return 0, errStream
	}
	return table[*idx], nil
}

func (d *Decoder) subframeHeader(r *bitReader) error {
	d.nssf = r.u(2) + 1
	r.skip(3) // partial subsubframe
	for ch := range d.nchannels {
		for band := range d.nsubbands[ch] {
			d.predMode[ch][band] = r.flag()
		}
	}
	for ch := range d.nchannels {
		for band := range d.nsubbands[ch] {
			if d.predMode[ch][band] {
				d.predIndex[ch][band] = r.u(12)
			}
		}
	}
	for ch := range d.nchannels {
		sel := d.bitallocSel[ch]
		for band := range d.vqStart[ch] {
			var a int
			if sel < 5 {
				a = books.bitalloc[sel].read(r)
			} else {
				a = r.u(sel - 1)
			}
			if a < 0 || a > 26 {
				return errStream
			}
			d.bitalloc[ch][band] = a
		}
	}
	for ch := range d.nchannels {
		d.tmode[ch] = [subbands]int{}
		if d.nssf > 1 {
			for band := range d.vqStart[ch] {
				if d.bitalloc[ch][band] != 0 {
					d.tmode[ch][band] = books.tmode[d.tmodeSel[ch]].read(r)
				}
			}
		}
	}
	for ch := range d.nchannels {
		sel := d.scaleSel[ch]
		idx := 0
		for band := range d.vqStart[ch] {
			if d.bitalloc[ch][band] == 0 {
				d.scales[ch][band][0] = 0
				continue
			}
			v, err := d.scale(r, &idx, sel)
			if err != nil {
				return err
			}
			d.scales[ch][band][0] = v
			if d.tmode[ch][band] != 0 {
				if v, err = d.scale(r, &idx, sel); err != nil {
					return err
				}
				d.scales[ch][band][1] = v
			}
		}
		for band := d.vqStart[ch]; band < d.nsubbands[ch]; band++ {
			v, err := d.scale(r, &idx, sel)
			if err != nil {
				return err
			}
			d.scales[ch][band][0] = v
		}
	}
	for ch := range d.nchannels {
		if d.jointIndex[ch] != 0 {
			if d.jointSel[ch] = r.u(3); d.jointSel[ch] == 7 {
				return errStream
			}
		}
	}
	for ch := range d.nchannels {
		src := d.jointIndex[ch] - 1
		if src < 0 {
			continue
		}
		sel := d.jointSel[ch]
		for band := d.nsubbands[ch]; band < d.nsubbands[src]; band++ {
			var idx int
			if sel < 5 {
				idx = books.scale[sel].read(r)
			} else {
				idx = r.u(sel + 1)
			}
			idx += 64
			if idx < 0 || idx >= len(jointScaleFactors) {
				return errStream
			}
			d.jointScl[ch][band] = jointScaleFactors[idx]
		}
	}
	if d.drcPresent {
		r.skip(8)
	}
	if d.crcPresent {
		r.skip(16)
	}
	return nil
}

// extract reads a subband's 8 quantized samples; huffman says whether
// the scale factor is adjusted.
func (d *Decoder) extract(r *bitReader, audio *[subbandSamples]int32, abits, ch int) (huffman bool, err error) {
	if abits == 0 {
		*audio = [subbandSamples]int32{}
		return false, nil
	}
	if abits <= 10 {
		sel := d.quantSel[ch][abits-1]
		if sel < int(quantIndexGroupSize[abits-1]) {
			b := books.quant[abits-1][sel]
			for i := range audio {
				audio[i] = int32(b.read(r)) //nolint:gosec // a small level
			}
			return true, nil
		}
		if abits <= 7 {
			n := blockCodeBits[abits-1]
			c1, c2 := r.u(n), r.u(n)
			levels := int(quantLevels[abits])
			off := (levels - 1) / 2
			for i := range 4 {
				audio[i] = int32(c1%levels - off) //nolint:gosec // a small level
				c1 /= levels
			}
			for i := 4; i < 8; i++ {
				audio[i] = int32(c2%levels - off) //nolint:gosec // a small level
				c2 /= levels
			}
			if c1|c2 != 0 {
				return false, errStream
			}
			return false, nil
		}
	}
	for i := range audio {
		audio[i] = int32(r.s(abits - 3)) //nolint:gosec // abits-3 bits
	}
	return false, nil
}

func (d *Decoder) subframeAudio(r *bitReader, subPos, lfePos *int) error {
	nsamples := d.nssf * subbandSamples
	if *subPos+nsamples > d.npcmblocks {
		return errStream
	}
	for ch := range d.nchannels {
		for band := d.vqStart[ch]; band < d.nsubbands[ch]; band++ {
			vq := &highFreqVQ[r.u(10)]
			scale := d.scales[ch][band][0]
			s := d.sub[ch][band][adpcmCoeffs+*subPos:]
			for j := range nsamples {
				s[j] = clip23((int32(vq[j])*scale + 8) >> 4)
			}
		}
	}
	if d.lfePresent != 0 {
		n := 2 * d.lfePresent * d.nssf // 4 a subsubframe at 64 times interpolation
		var audio [16]int32
		for i := range n {
			audio[i] = int32(r.s(8)) //nolint:gosec // 8 bits
		}
		idx := r.u(8)
		if idx >= len(scaleFactorQuant7) {
			return errStream
		}
		scale := mul23(4697620, scaleFactorQuant7[idx])
		for i := range n {
			d.lfe[*lfePos] = clip23(audio[i] * scale >> 4)
			*lfePos++
		}
	}
	ofs := *subPos
	var audio [subbandSamples]int32
	for ssf := range d.nssf {
		for ch := range d.nchannels {
			for band := range d.vqStart[ch] {
				abits := d.bitalloc[ch][band]
				huff, err := d.extract(r, &audio, abits, ch)
				if err != nil {
					return err
				}
				step := lossyQuant[abits]
				if d.bitRate == 3 {
					step = losslessQuant[abits]
				}
				scale := d.scales[ch][band][0]
				if t := d.tmode[ch][band]; t != 0 && ssf >= t {
					scale = d.scales[ch][band][1]
				}
				if huff {
					scale = clip23(int32(int64(d.scaleAdj[ch][abits-1]) * int64(scale) >> 22))
				}
				dequantize(d.sub[ch][band][adpcmCoeffs+ofs:], &audio, step, scale)
			}
		}
		if (ssf == d.nssf-1 || d.syncSSF) && r.u(16) != 0xffff {
			return errors.New("dts: a DSYNC check failed")
		}
		ofs += subbandSamples
	}
	// Inverse ADPCM.
	for ch := range d.nchannels {
		for band := range d.nsubbands[ch] {
			if !d.predMode[ch][band] {
				continue
			}
			c := &adpcmVB[d.predIndex[ch][band]]
			s := d.sub[ch][band]
			for j := adpcmCoeffs + *subPos; j < adpcmCoeffs+*subPos+nsamples; j++ {
				var p int64
				for i := range adpcmCoeffs {
					p += int64(s[j-1-i]) * int64(c[i])
				}
				s[j] = clip23(s[j] + clip23(norm(p, 13)))
			}
		}
	}
	// Joint intensity.
	for ch := range d.nchannels {
		src := d.jointIndex[ch] - 1
		if src < 0 {
			continue
		}
		for band := d.nsubbands[ch]; band < d.nsubbands[src]; band++ {
			scale := d.jointScl[ch][band]
			dst, from := d.sub[ch][band][adpcmCoeffs+*subPos:], d.sub[src][band][adpcmCoeffs+*subPos:]
			for j := range nsamples {
				dst[j] = clip23(mul(from[j], scale, 17))
			}
		}
	}
	*subPos = ofs
	return nil
}

func dequantize(out []int32, in *[subbandSamples]int32, step, scale int32) {
	ss := int64(step) * int64(scale)
	shift := 0
	if ss > 1<<23 {
		shift = bits.Len64(uint64(ss >> 23)) // av_log2(x) + 1
		ss >>= shift
	}
	for n, v := range in {
		out[n] = clip23(norm(int64(v)*ss, 22-shift))
	}
}

// filter reconstructs the frame's PCM: each channel through the 32-band
// synthesis filter, the LFE through its interpolation, the sum and
// difference pairs undone.
func (d *Decoder) filter() (*Frame, error) {
	n := d.npcmblocks * 32
	f := &Frame{SampleRate: d.sampleRate}
	coeff := &fir32Nonperfect
	if d.filterPerfect {
		coeff = &fir32Perfect
	}
	for ch := range d.nchannels {
		spk := primarySpeakers[d.audioMode][ch]
		if spk < 0 {
			return nil, errStream
		}
		out := make([]int32, n)
		st := &d.synth[ch]
		var in [32]int32
		for j := range d.npcmblocks {
			for i := range in {
				in[i] = d.sub[ch][i][adpcmCoeffs+j]
			}
			synthFilter(st, coeff, out[j*32:j*32+32], &in)
		}
		f.Samples[spk] = out
	}
	if d.lfePresent != 0 {
		out := make([]int32, n)
		nl := d.npcmblocks / 2
		for i := range nl {
			for j := range 32 {
				var a, b int64
				for k := range 8 {
					v := int64(d.lfe[lfeHistory+i-k])
					a += int64(lfeFir64[j*8+k]) * v
					b += int64(lfeFir64[255-j*8-k]) * v
				}
				out[i*64+j] = clip23(norm(a, 23))
				out[i*64+32+j] = clip23(norm(b, 23))
			}
		}
		copy(d.lfe[:lfeHistory], d.lfe[nl:nl+lfeHistory])
		f.Samples[SpeakerLFE] = out
	}
	butterfly := func(a, b []int32) {
		for i := range a {
			a[i], b[i] = a[i]+b[i], a[i]-b[i]
		}
	}
	if (d.sumdiffFront && d.audioMode > 0) || d.audioMode == 3 {
		butterfly(f.Samples[SpeakerL], f.Samples[SpeakerR])
	}
	if d.sumdiffSurround && d.audioMode >= 8 {
		butterfly(f.Samples[SpeakerLs], f.Samples[SpeakerRs])
	}
	for _, s := range f.Samples {
		for i := range s {
			s[i] = clip23(s[i])
		}
	}
	return f, nil
}

// synthFilter is the 32-band QMF synthesis of one sample of each band.
func synthFilter(st *synthState, window *[512]int32, out []int32, in *[32]int32) {
	buf := st.hist1[st.offset:]
	imdctHalf32(buf[:32], in)
	off := st.offset
	for i := range 16 {
		a := int64(st.hist2[i]) << 21
		b := int64(st.hist2[i+16]) << 21
		var c, dd int64
		j := 0
		for ; j < 512-off; j += 64 {
			a += int64(window[i+j]) * int64(buf[i+j])
			b += int64(window[i+j+16]) * int64(buf[15-i+j])
			c += int64(window[i+j+32]) * int64(buf[16+i+j])
			dd += int64(window[i+j+48]) * int64(buf[31-i+j])
		}
		for ; j < 512; j += 64 {
			w := st.hist1[:]
			a += int64(window[i+j]) * int64(w[off+i+j-512])
			b += int64(window[i+j+16]) * int64(w[off+15-i+j-512])
			c += int64(window[i+j+32]) * int64(w[off+16+i+j-512])
			dd += int64(window[i+j+48]) * int64(w[off+31-i+j-512])
		}
		out[i] = clip23(norm(a, 21))
		out[i+16] = clip23(norm(b, 21))
		st.hist2[i] = norm(c, 21)
		st.hist2[i+16] = norm(dd, 21)
	}
	st.offset = (st.offset - 32) & 511
}

func norm(a int64, n int) int32 {
	if n > 0 {
		return int32((a + 1<<(n-1)) >> n) //nolint:gosec // as the reference truncates
	}
	return int32(a) //nolint:gosec // as above
}

func mul(a, b int32, n int) int32 { return norm(int64(a)*int64(b), n) }

func mul23(a, b int32) int32 { return mul(a, b, 23) }

func clip23(a int32) int32 { return min(max(a, -(1<<23)), 1<<23-1) }
