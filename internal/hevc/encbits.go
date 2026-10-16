package hevc

import "math"

// The writing side of the bitstream: an RBSP bit writer, NAL unit wrapping
// with emulation prevention, and the CABAC arithmetic encoder (9.3.4.3's
// inverse: the encoding engine of 9.3.5 of H.264, which HEVC shares).

// bitWriter writes an RBSP most significant bit first.
type bitWriter struct {
	b    []byte
	cur  uint64
	nbit uint
}

// u writes the n (at most 32) low bits of v.
func (w *bitWriter) u(n uint, v uint32) {
	if n == 0 {
		return
	}
	w.cur = w.cur<<n | uint64(v)&(1<<n-1)
	w.nbit += n
	for w.nbit >= 8 {
		w.nbit -= 8
		w.b = append(w.b, byte(w.cur>>w.nbit))
	}
}

func (w *bitWriter) flag(b bool) {
	if b {
		w.u(1, 1)
	} else {
		w.u(1, 0)
	}
}

// ue writes an unsigned Exp-Golomb code (9.2).
func (w *bitWriter) ue(v uint32) {
	x := uint64(v) + 1
	n := uint(0)
	for t := x; t > 1; t >>= 1 {
		n++
	}
	w.u(n, 0)
	if n+1 > 32 {
		w.u(n+1-32, uint32(x>>32))
		w.u(32, uint32(x))
		return
	}
	w.u(n+1, uint32(x))
}

// se writes a signed Exp-Golomb code.
func (w *bitWriter) se(v int32) {
	if v > 0 {
		w.ue(uint32(2*v - 1))
	} else {
		w.ue(uint32(-2 * v))
	}
}

// alignZeros pads to the byte boundary with zero bits.
func (w *bitWriter) alignZeros() {
	for w.nbit != 0 {
		w.u(1, 0)
	}
}

// trailing writes rbsp_trailing_bits and returns the RBSP.
func (w *bitWriter) trailing() []byte {
	w.u(1, 1)
	w.alignZeros()
	return w.b
}

// bytes returns the RBSP written so far, which must end on a byte boundary.
func (w *bitWriter) bytes() []byte { return w.b }

// escapeNAL appends the RBSP to dst with emulation prevention bytes
// inserted (7.4.2), continuing a run of zeros of the given length from
// what precedes it; it returns the new run length.
func escapeNAL(dst []byte, rbsp []byte, zeros int) ([]byte, int) {
	for _, c := range rbsp {
		if zeros >= 2 && c <= 3 {
			dst = append(dst, 3)
			zeros = 0
		}
		dst = append(dst, c)
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return dst, zeros
}

// writeNAL appends a NAL unit with a four byte start code: its two header
// bytes for type typ (layer 0, temporal id 0), then the RBSP escaped.
func writeNAL(dst []byte, typ int, rbsp []byte) []byte {
	return writeLayerNAL(dst, typ, 0, rbsp)
}

// writeLayerNAL writes a NAL unit of layer nuh_layer_id layer.
func writeLayerNAL(dst []byte, typ, layer int, rbsp []byte) []byte {
	dst = append(dst, 0, 0, 0, 1, byte(typ<<1|layer>>5), byte(layer&31)<<3|1)
	dst, _ = escapeNAL(dst, rbsp, 0)
	// An RBSP ending in a zero byte (cabac_zero_word) is followed by 0x03.
	if len(rbsp) > 0 && rbsp[len(rbsp)-1] == 0 {
		dst = append(dst, 3)
	}
	return dst
}

// binCoder codes CABAC bins: the arithmetic coder, or an estimate of what
// they would cost.
type binCoder interface {
	decision(ctxIdx int, bin int)
	bypass(bin int)
	bypassBits(v uint32, n int)
	terminate(bin int)
	// contexts is the coder's context states, for an estimate to start
	// from.
	contexts() *[numContexts]uint8
}

// cabacEncoder is the arithmetic encoding engine. Context states use the
// decoder's layout: pStateIdx<<1 | valMps.
type cabacEncoder struct {
	w           *bitWriter
	low         uint32
	rng         uint32
	outstanding int
	first       bool
	ctx         [numContexts]uint8
}

func (e *cabacEncoder) contexts() *[numContexts]uint8 { return &e.ctx }

// Next context state after coding the more or the less probable symbol,
// by state (pStateIdx<<1 | valMps).
var cabacNextMPS, cabacNextLPS [128]uint8

// cabacEntropy is the cost in bits of a bin by context state: index
// pStateIdx<<1 | 1 for the less probable symbol, | 0 for the more.
var cabacEntropy [128]float64

func init() {
	for s := range 128 {
		state, mps := uint8(s>>1), uint8(s&1)
		cabacNextMPS[s] = min(state+1, 62)<<1 | mps
		next := mps
		if state == 0 {
			next = 1 - mps
		}
		cabacNextLPS[s] = transIdxLps[state]<<1 | next
	}
	// The LPS probability of state p is 0.5 * alpha^p, alpha =
	// (0.01875/0.5)^(1/63).
	alpha := math.Pow(0.01875/0.5, 1.0/63)
	for p := range 64 {
		lps := 0.5 * math.Pow(alpha, float64(p))
		cabacEntropy[p<<1|1] = -math.Log2(lps)
		cabacEntropy[p<<1] = -math.Log2(1 - lps)
	}
}

// start initializes the engine and the context variables for slice data
// written to w, which must be byte aligned.
func (e *cabacEncoder) start(w *bitWriter, initType, qp int) {
	e.w = w
	e.low = 0
	e.rng = 510
	e.outstanding = 0
	e.first = true
	var c cabac
	c.initContexts(initType, qp)
	e.ctx = c.ctx
}

// resume starts the engine with the given context states (a WPP row
// taking the row above's).
func (e *cabacEncoder) resume(w *bitWriter, ctx *[numContexts]uint8) {
	e.w = w
	e.low = 0
	e.rng = 510
	e.outstanding = 0
	e.first = true
	e.ctx = *ctx
}

func (e *cabacEncoder) putBit(b uint32) {
	if e.first {
		e.first = false
	} else {
		e.w.u(1, b)
	}
	for ; e.outstanding > 0; e.outstanding-- {
		e.w.u(1, 1-b)
	}
}

func (e *cabacEncoder) renorm() {
	for e.rng < 256 {
		switch {
		case e.low < 256:
			e.putBit(0)
		case e.low >= 512:
			e.low -= 512
			e.putBit(1)
		default:
			e.low -= 256
			e.outstanding++
		}
		e.rng <<= 1
		e.low <<= 1
	}
}

// decision encodes bin with context ctxIdx.
func (e *cabacEncoder) decision(ctxIdx int, bin int) {
	s := e.ctx[ctxIdx]
	state, mps := s>>1, int(s&1)
	lps := uint32(rangeTabLps[state][(e.rng>>6)&3])
	e.rng -= lps
	if bin != mps {
		e.low += e.rng
		e.rng = lps
		e.ctx[ctxIdx] = cabacNextLPS[s]
	} else {
		e.ctx[ctxIdx] = cabacNextMPS[s]
	}
	e.renorm()
}

// bypass encodes an equiprobable bin.
func (e *cabacEncoder) bypass(bin int) {
	e.low <<= 1
	if bin != 0 {
		e.low += e.rng
	}
	switch {
	case e.low >= 1024:
		e.putBit(1)
		e.low -= 1024
	case e.low < 512:
		e.putBit(0)
	default:
		e.low -= 512
		e.outstanding++
	}
}

// bypassBits encodes the n low bits of v as bypass bins, most significant
// first.
func (e *cabacEncoder) bypassBits(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		e.bypass(int(v>>uint(i)) & 1)
	}
}

// terminate encodes end_of_slice_segment_flag, end_of_subset_one_bit or
// pcm_flag. A bin of 1 flushes the engine; its last written bit is the
// stop bit the decoder aligns from.
func (e *cabacEncoder) terminate(bin int) {
	e.rng -= 2
	if bin != 0 {
		e.low += e.rng
		e.rng = 2
		e.renorm()
		e.putBit(e.low >> 9 & 1)
		e.w.u(2, e.low>>7&3|1)
		return
	}
	e.renorm()
}

// cabacCost estimates the bits bins would take in the arithmetic coder,
// following the context states as it would, without coding anything:
// a decision costs the information of its bin at its context's
// probability, a bypass bin one bit.
type cabacCost struct {
	ctx  [numContexts]uint8
	bits float64
}

func (k *cabacCost) contexts() *[numContexts]uint8 { return &k.ctx }

func (k *cabacCost) decision(ctxIdx int, bin int) {
	s := k.ctx[ctxIdx]
	if bin != int(s&1) {
		k.bits += cabacEntropy[s|1]
		k.ctx[ctxIdx] = cabacNextLPS[s]
	} else {
		k.bits += cabacEntropy[s&^1]
		k.ctx[ctxIdx] = cabacNextMPS[s]
	}
}

func (k *cabacCost) bypass(int) { k.bits++ }

func (k *cabacCost) bypassBits(_ uint32, n int) { k.bits += float64(n) }

func (k *cabacCost) terminate(int) {}
