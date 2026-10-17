package av1

// The symbol coder: AV1's multi-symbol arithmetic coder (spec 8.2), as an
// encoder. A symbol of N values is coded with a CDF of N entries in Q15
// (the spec's form: cdf[i] the probability, times 32768, that the symbol
// is at most i; cdf[N-1] 32768) and one more, a count the adaptation's
// rate follows; the CDF adapts after each symbol as the decoder's does.
// The arithmetic is the inverse of the spec's decoder: the interval kept
// is the one its decoding loop lands in, measured down from the top of the
// range (the decoder's value is inverted). Output goes through a buffer
// of 16-bit words that carries propagate through once, at the end.

import "math/bits"

const (
	ecProbShift = 6
	ecMinProb   = 4
)

// symbolWriter codes symbols into bytes.
type symbolWriter struct {
	low      uint32
	rng      uint32
	cnt      int
	precarry []uint16
	adapt    bool // the CDFs adapt (disable_cdf_update 0)
}

func newSymbolWriter(adapt bool) *symbolWriter {
	return &symbolWriter{rng: 0x8000, cnt: -9, adapt: adapt}
}

// normalize scales the range back to 16 bits, moving the low end's settled
// bits out.
func (w *symbolWriter) normalize(low, rng uint32) {
	d := 16 - (32 - bits.LeadingZeros32(rng))
	c := w.cnt
	s := c + d
	if s >= 0 {
		c += 16
		m := uint32(1)<<uint(c) - 1
		if s >= 8 {
			w.precarry = append(w.precarry, uint16(low>>uint(c)))
			low &= m
			c -= 8
			m >>= 8
		}
		w.precarry = append(w.precarry, uint16(low>>uint(c)))
		s = c + d - 24
		low &= m
	}
	w.low = low << uint(d)
	w.rng = rng << uint(d)
	w.cnt = s
}

// encode codes symbol s of an n-valued alphabet whose CDF (spec form) is
// cdf, without adapting it.
func (w *symbolWriter) encode(s int, cdf []uint16, n int) {
	l, r := w.low, w.rng
	fh := 32768 - uint32(cdf[s]) // the decoder's f for s
	nn := uint32(n - 1)
	if s > 0 {
		fl := 32768 - uint32(cdf[s-1])
		u := (r>>8)*(fl>>ecProbShift)>>(7-ecProbShift) + ecMinProb*(nn-uint32(s-1))
		v := (r>>8)*(fh>>ecProbShift)>>(7-ecProbShift) + ecMinProb*(nn-uint32(s))
		l += r - u
		r = u - v
	} else {
		r -= (r>>8)*(fh>>ecProbShift)>>(7-ecProbShift) + ecMinProb*(nn-uint32(s))
	}
	w.normalize(l, r)
}

// symbol codes s with cdf (n+1 entries: n values and the count), adapting
// it.
func (w *symbolWriter) symbol(s int, cdf []uint16) {
	n := len(cdf) - 1
	w.encode(s, cdf, n)
	if w.adapt {
		adaptCDF(cdf, s)
	}
}

// adaptCDF moves a CDF toward the symbol seen (spec 8.2.6).
func adaptCDF(cdf []uint16, s int) {
	n := len(cdf) - 1
	cnt := cdf[n]
	rate := 3 + b2u(cnt > 15) + b2u(cnt > 31) + min(uint(bits.Len(uint(n))-1), 2)
	var tmp uint16
	for i := range n - 1 {
		if i == s {
			tmp = 1 << 15
		}
		if tmp < cdf[i] {
			cdf[i] -= (cdf[i] - tmp) >> rate
		} else {
			cdf[i] += (tmp - cdf[i]) >> rate
		}
	}
	if cnt < 32 {
		cdf[n]++
	}
}

func b2u(b bool) uint {
	if b {
		return 1
	}
	return 0
}

// boolean codes an equiprobable bit (read_bool).
func (w *symbolWriter) boolean(b bool) {
	cdf := [3]uint16{1 << 14, 1 << 15, 0}
	w.encode(int(b2u(b)), cdf[:], 2)
}

// literal codes n bits, most significant first (read_literal).
func (w *symbolWriter) literal(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		w.boolean(v>>uint(i)&1 != 0)
	}
}

// bitCount is about how many bits have been coded: for rate estimates.
func (w *symbolWriter) bitCount() int {
	return 8*len(w.precarry) + w.cnt + 10
}

// finish ends the coding (with the trailing one bit the spec's exit
// process wants) and gives the bytes.
func (w *symbolWriter) finish() []byte {
	l, c := w.low, w.cnt
	const m = 0x3FFF
	e := (l+m)&^m | (m + 1)
	s := c + 10
	if s > 0 {
		n := uint32(1)<<uint(c+16) - 1
		for s > 0 {
			w.precarry = append(w.precarry, uint16(e>>uint(c+16)))
			e &= n
			s -= 8
			c -= 8
			n >>= 8
		}
	}
	out := make([]byte, len(w.precarry))
	var carry uint32
	for i := len(w.precarry) - 1; i >= 0; i-- {
		carry += uint32(w.precarry[i])
		out[i] = byte(carry)
		carry >>= 8
	}
	return out
}
