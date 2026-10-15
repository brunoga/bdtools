package mvc

import "math"

// The writing side of the bitstream: an RBSP bit writer, NAL unit wrapping
// with emulation prevention, and the CABAC arithmetic encoder (9.3.4.2).

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

// ue writes an unsigned Exp-Golomb code (9.1).
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

// se writes a signed Exp-Golomb code (9.1.1).
func (w *bitWriter) se(v int32) {
	if v > 0 {
		w.ue(uint32(2*v - 1))
	} else {
		w.ue(uint32(-2 * v))
	}
}

// alignOnes pads to the byte boundary with one bits
// (cabac_alignment_one_bit).
func (w *bitWriter) alignOnes() {
	for w.nbit != 0 {
		w.u(1, 1)
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

// appendNAL appends a NAL unit with a four byte start code: its header bytes,
// then the RBSP with emulation prevention bytes inserted (7.4.1).
func appendNAL(dst []byte, header []byte, rbsp []byte) []byte {
	dst = append(dst, 0, 0, 0, 1)
	dst = append(dst, header...)
	zeros := 0
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
	// An RBSP ending in a zero byte (cabac_zero_word) is followed by 0x03.
	if len(rbsp) > 0 && rbsp[len(rbsp)-1] == 0 {
		dst = append(dst, 3)
	}
	return dst
}

// mvcNALHeader returns the four byte header of a prefix (14) or coded slice
// extension (20) NAL unit: nal_unit_header_mvc_extension (H.7.3.1.1) with
// svc_extension_flag 0.
func mvcNALHeader(typ, refIdc int, idr bool, viewID int, anchor, interView bool) []byte {
	var v uint32
	if !idr {
		v |= 1 << 22 // non_idr_flag
	}
	// priority_id 0
	v |= uint32(viewID&1023) << 6
	// temporal_id 0
	if anchor {
		v |= 1 << 2
	}
	if interView {
		v |= 1 << 1
	}
	v |= 1 // reserved_one_bit
	return []byte{byte(refIdc<<5 | typ), byte(v >> 16), byte(v >> 8), byte(v)}
}

// cabacEncoder is the arithmetic encoding engine (9.3.4.2). Context states
// use the decoder's layout: pStateIdx<<1 | valMPS.
type cabacEncoder struct {
	w           *bitWriter
	low         uint32
	rng         uint32
	outstanding int
	first       bool
	ctx         [512]uint8
}

// binCoder codes CABAC bins: the arithmetic coder, or an estimate of what
// they would cost.
type binCoder interface {
	decision(ctxIdx int, bin int)
	bypass(bin int)
	terminate(bin int)
	// contexts is the coder's context states, for an estimate to start
	// from.
	contexts() *[512]uint8
}

func (e *cabacEncoder) contexts() *[512]uint8 { return &e.ctx }
func (k *cabacCost) contexts() *[512]uint8    { return &k.ctx }

// cabacCost estimates the bits bins would take in the arithmetic coder,
// following the context states as it would, without coding anything:
// a decision costs the information of its bin at its context's
// probability, a bypass bin one bit.
type cabacCost struct {
	ctx  [512]uint8
	bits float64
}

// cabacEntropy is the cost in bits of a bin by context state: index
// pStateIdx<<1 | 1 for the less probable symbol, | 0 for the more.
var cabacEntropy [128]float64

func init() {
	// The LPS probability of state p is 0.5 * alpha^p, alpha =
	// (0.01875/0.5)^(1/63) (9.3.3.2.1.1).
	alpha := math.Pow(0.01875/0.5, 1.0/63)
	for p := 0; p < 64; p++ {
		lps := 0.5 * math.Pow(alpha, float64(p))
		cabacEntropy[p<<1|1] = -math.Log2(lps)
		cabacEntropy[p<<1] = -math.Log2(1 - lps)
	}
}

// reset starts an estimate from the coder's context states.
func (k *cabacCost) reset(from binCoder) {
	k.ctx = *from.contexts()
	k.bits = 0
}

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

func (k *cabacCost) terminate(int) {}

// start initializes the engine (9.3.4.1) and the context variables for
// slice data written to w, which must be byte aligned.
func (e *cabacEncoder) start(w *bitWriter, initSet, qp int) {
	e.w = w
	e.low = 0
	e.rng = 510
	e.outstanding = 0
	e.first = true
	initContexts(&e.ctx, initSet, qp)
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

// decision encodes bin with context ctxIdx (EncodeDecision, 9.3.4.2).
func (e *cabacEncoder) decision(ctxIdx int, bin int) {
	s := e.ctx[ctxIdx]
	p, mps := int(s>>1), int(s&1)
	lps := uint32(cabacLPS[p*4+int(e.rng>>6&3)])
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

// bypass encodes an equiprobable bin (EncodeBypass, 9.3.4.4).
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

// terminate encodes end_of_slice_flag or another terminating bin
// (EncodeTerminate, 9.3.4.5). A bin of 1 flushes the engine; its last
// written bit is the rbsp_stop_one_bit.
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
