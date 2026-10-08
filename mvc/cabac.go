package mvc

import (
	"encoding/binary"
	"math/bits"
)

// cabac is the arithmetic decoding engine (9.3.3.2). Range and offset are
// kept scaled up within 64 bits so that renormalization only happens about
// every 48 consumed bits: the spec's 9-bit codIRange is rng >> e and
// codIOffset is off >> e, where e = 55 - clz(rng).
type cabac struct {
	off   uint64
	rng   uint64
	buf   []byte
	pos   int // next byte to load
	start int
}

var cabacLPS = [64 * 4]uint8{
	128, 176, 208, 240, 128, 167, 197, 227, 128, 158, 187, 216, 123, 150, 178, 205,
	116, 142, 169, 195, 111, 135, 160, 185, 105, 128, 152, 175, 100, 122, 144, 166,
	95, 116, 137, 158, 90, 110, 130, 150, 85, 104, 123, 142, 81, 99, 117, 135,
	77, 94, 111, 128, 73, 89, 105, 122, 69, 85, 100, 116, 66, 80, 95, 110,
	62, 76, 90, 104, 59, 72, 86, 99, 56, 69, 81, 94, 53, 65, 77, 89,
	51, 62, 73, 85, 48, 59, 69, 80, 46, 56, 66, 76, 43, 53, 63, 72,
	41, 50, 59, 69, 39, 48, 56, 65, 37, 45, 54, 62, 35, 43, 51, 59,
	33, 41, 48, 56, 32, 39, 46, 53, 30, 37, 43, 50, 29, 35, 41, 48,
	27, 33, 39, 45, 26, 31, 37, 43, 24, 30, 35, 41, 23, 28, 33, 39,
	22, 27, 32, 37, 21, 26, 30, 35, 20, 24, 29, 33, 19, 23, 27, 31,
	18, 22, 26, 30, 17, 21, 25, 28, 16, 20, 23, 27, 15, 19, 22, 25,
	14, 18, 21, 24, 14, 17, 20, 23, 13, 16, 19, 22, 12, 15, 18, 21,
	12, 14, 17, 20, 11, 14, 16, 19, 11, 13, 15, 18, 10, 12, 15, 17,
	10, 12, 14, 16, 9, 11, 13, 15, 9, 11, 12, 14, 8, 10, 12, 14,
	8, 9, 11, 13, 7, 9, 11, 12, 7, 9, 10, 12, 7, 8, 10, 11,
	6, 8, 9, 11, 6, 7, 9, 10, 6, 7, 8, 9, 2, 2, 2, 2,
}

var cabacTransLPS = [64]uint8{
	0, 0, 1, 2, 2, 4, 4, 5, 6, 7, 8, 9, 9, 11, 11, 12,
	13, 13, 15, 15, 16, 16, 18, 18, 19, 19, 21, 21, 22, 22, 23, 24,
	24, 25, 26, 26, 27, 27, 28, 29, 29, 30, 30, 30, 31, 32, 32, 33,
	33, 33, 34, 34, 35, 35, 35, 36, 36, 36, 37, 37, 37, 38, 38, 63,
}

// cabacLPS4[pStateIdx] packs rangeTabLPS for the four qCodIRangeIdx values
// in its bytes, so the table load depends only on the context state and
// stays off the range's dependency chain.
var cabacLPS4 [64]uint32

// Context states are stored as pStateIdx<<1 | valMPS.
var cabacNextMPS, cabacNextLPS [128]uint8

// cabacTrans[hit<<7|state] is the next state after an MPS (hit=0) or
// LPS (hit=1) decision.
var cabacTrans [256]uint8

// cabacLPSByState is cabacLPS indexed by state>>1<<2 | q, i.e. state&^1<<1|q.
func init() {
	for s := 0; s < 128; s++ {
		p, m := s>>1, s&1
		np := p + 1
		if np > 62 {
			np = 62
		}
		if p == 63 {
			np = 63
		}
		cabacNextMPS[s] = uint8(np<<1 | m)
		lp := int(cabacTransLPS[p])
		lm := m
		if p == 0 {
			lm ^= 1
		}
		cabacNextLPS[s] = uint8(lp<<1 | lm)
		if m == 0 {
			for q := 0; q < 4; q++ {
				cabacLPS4[p] |= uint32(cabacLPS[p*4+q]) << (8 * q)
			}
		}
		cabacTrans[s] = cabacNextMPS[s]
		cabacTrans[128|s] = cabacNextLPS[s]
	}
}

// initContexts initializes context variables (9.3.1.1).
func initContexts(ctx *[512]uint8, initSet int, qp int) {
	tab := &cabacInit[initSet]
	if qp < 0 {
		qp = 0
	}
	for i := range tab {
		m, n := int(tab[i][0]), int(tab[i][1])
		pre := ((m * qp) >> 4) + n
		if pre < 1 {
			pre = 1
		} else if pre > 126 {
			pre = 126
		}
		if pre <= 63 {
			ctx[i] = uint8((63 - pre) << 1)
		} else {
			ctx[i] = uint8((pre-64)<<1 | 1)
		}
	}
	ctx[276] = 63 << 1 // end_of_slice: pStateIdx 63, unused (terminate)
}

func (c *cabac) init(buf []byte, pos int) {
	c.buf = buf
	c.pos = pos
	c.start = pos
	c.off = 0
	for i := 0; i < 8; i++ {
		c.off = c.off<<8 | uint64(c.byteAt(c.pos))
		c.pos++
	}
	c.rng = 510 << 55
}

func (c *cabac) byteAt(i int) byte {
	if i < len(c.buf) {
		return c.buf[i]
	}
	return 0
}

// cabacPad is the number of zero bytes callers append to CABAC slice data,
// so that refills are single unconditional loads.
const cabacPad = 32

// cabacRefill shifts 48 new bits into engine state held in registers. It
// contains no calls, so hot loops keep their state in registers. buf must
// carry cabacPad bytes of padding; reads past the end (corrupt data) are
// clamped to the last 8 bytes.
func cabacRefill(buf []byte, pos int, rng, off uint64) (int, uint64, uint64) {
	p := min(pos, len(buf)-8)
	v := binary.BigEndian.Uint64(buf[p:]) >> 16
	return pos + 6, off<<48 | v, rng << 48
}

// refill shifts 48 new bits into the offset.
func (c *cabac) refill() {
	c.pos, c.off, c.rng = cabacRefill(c.buf, c.pos, c.rng, c.off)
}

// decision decodes one context-coded bin (DecodeDecision).
func (c *cabac) decision(ctx *uint8) uint32 {
	st := *ctx
	rng, off := c.rng, c.off
	shift := uint(bits.LeadingZeros64(rng)) & 63
	lps := uint64(uint8(cabacLPS4[uint(st>>1)&63]>>((rng<<shift>>58)&24))) << ((55 - shift) & 63)
	rng -= lps
	noff := off - rng
	var hit uint
	if off >= rng {
		hit = 1
	}
	if hit != 0 {
		off = noff
	}
	if hit != 0 {
		rng = lps
	}
	*ctx = cabacTrans[(hit<<7|uint(st))&255]
	c.off = off
	c.rng = rng
	if rng < 256 {
		c.refill()
	}
	return uint32((uint(st) ^ hit) & 1)
}

// bypass decodes one equiprobable bin (DecodeBypass).
func (c *cabac) bypass() uint32 {
	if c.rng < 512 {
		c.refill()
	}
	c.rng >>= 1
	if c.off >= c.rng {
		c.off -= c.rng
		return 1
	}
	return 0
}

// bypassBits decodes n bypass bins as an unsigned integer.
func (c *cabac) bypassBits(n int) uint32 {
	var v uint32
	for ; n > 0; n-- {
		v = v<<1 | c.bypass()
	}
	return v
}

// terminate decodes a bin with DecodeTerminate.
func (c *cabac) terminate() uint32 {
	shift := uint(bits.LeadingZeros64(c.rng))
	two := uint64(2) << (55 - shift)
	c.rng -= two
	if c.off >= c.rng {
		// Arithmetic decoding finished; remember the bit position for PCM.
		c.rng += two // restore so bitPos sees the same scale
		return 1
	}
	if c.rng < 256 {
		c.refill()
	}
	return 0
}

// bitPos returns the number of bits consumed by the spec decoder, relative
// to the start of the CABAC data.
func (c *cabac) bitPos() int {
	e := 55 - bits.LeadingZeros64(c.rng)
	return (c.pos-c.start)*8 - e
}
