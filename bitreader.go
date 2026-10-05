package mvc

import (
	"encoding/binary"
	"math/bits"
)

// bitReader reads bits MSB-first from an RBSP (emulation prevention bytes
// already removed). Reads past the end return zero bits; callers detect
// overruns with overrun().
type bitReader struct {
	buf   []byte
	pos   int    // next byte to load into cache
	cache uint64 // left-aligned bits
	n     uint   // valid bits in cache
	stop  int    // bit position of rbsp_stop_one_bit, -1 if unknown
}

func (br *bitReader) init(b []byte) {
	br.buf = b
	br.pos = 0
	br.cache = 0
	br.n = 0
	br.stop = -1
	br.refill()
}

func (br *bitReader) refill() {
	if br.pos+8 <= len(br.buf) {
		v := binary.BigEndian.Uint64(br.buf[br.pos:])
		// load as many whole bytes as fit
		take := (64 - br.n) >> 3
		br.cache |= v >> br.n &^ (^uint64(0) >> (br.n + take*8))
		br.pos += int(take)
		br.n += take * 8
		return
	}
	for br.n <= 56 {
		var b byte
		if br.pos < len(br.buf) {
			b = br.buf[br.pos]
		}
		br.pos++
		br.cache |= uint64(b) << (56 - br.n)
		br.n += 8
	}
}

// u reads n bits (n <= 32).
func (br *bitReader) u(n uint) uint32 {
	if n == 0 {
		return 0
	}
	if br.n < n {
		br.refill()
	}
	v := uint32(br.cache >> (64 - n))
	br.cache <<= n
	br.n -= n
	return v
}

func (br *bitReader) u1() uint32 {
	if br.n < 1 {
		br.refill()
	}
	v := uint32(br.cache >> 63)
	br.cache <<= 1
	br.n--
	return v
}

func (br *bitReader) flag() bool { return br.u1() != 0 }

func (br *bitReader) peek(n uint) uint32 {
	if br.n < n {
		br.refill()
	}
	return uint32(br.cache >> (64 - n))
}

func (br *bitReader) skip(n uint) {
	for n > 32 {
		br.u(32)
		n -= 32
	}
	if br.n < n {
		br.refill()
	}
	br.cache <<= n
	br.n -= n
}

// ue reads an unsigned Exp-Golomb code (up to 32 bits of value).
func (br *bitReader) ue() uint32 {
	if br.n < 32 {
		br.refill()
	}
	lz := uint(bits.LeadingZeros64(br.cache | 1))
	if lz < 16 && 2*lz+1 <= br.n {
		v := br.cache >> (64 - (2*lz + 1))
		br.cache <<= 2*lz + 1
		br.n -= 2*lz + 1
		return uint32(v) - 1
	}
	// slow path for long codes
	zeros := uint(0)
	for br.u1() == 0 {
		zeros++
		if zeros > 31 {
			return 0xffffffff
		}
	}
	if zeros == 0 {
		return 0
	}
	return uint32((uint64(1)<<zeros)-1) + br.u(zeros)
}

func (br *bitReader) se() int32 {
	k := br.ue()
	if k&1 != 0 {
		return int32((k + 1) >> 1)
	}
	return -int32(k >> 1)
}

// te reads a truncated Exp-Golomb code with range max.
func (br *bitReader) te(max int) uint32 {
	if max > 1 {
		return br.ue()
	}
	return br.u1() ^ 1
}

// bitPos returns the number of bits consumed.
func (br *bitReader) bitPos() int { return br.pos*8 - int(br.n) }

func (br *bitReader) alignByte() {
	br.skip(uint((8 - br.bitPos()&7) & 7))
}

// overrun reports whether more bits were consumed than available.
func (br *bitReader) overrun() bool { return br.bitPos() > len(br.buf)*8 }

// moreRBSPData implements more_rbsp_data().
func (br *bitReader) moreRBSPData() bool {
	if br.stop < 0 {
		// find last 1 bit (rbsp_stop_one_bit)
		last := len(br.buf) - 1
		for last >= 0 && br.buf[last] == 0 {
			last--
		}
		if last < 0 {
			br.stop = 0
		} else {
			br.stop = last*8 + 7 - bits.TrailingZeros8(br.buf[last])
		}
	}
	return br.bitPos() < br.stop
}

// unescapeRBSP removes emulation prevention bytes from a NAL payload,
// appending to dst.
func unescapeRBSP(dst, src []byte) []byte {
	dst = dst[:0]
	zeros := 0
	start := 0
	for i := 0; i < len(src); i++ {
		b := src[i]
		if zeros >= 2 && b == 3 {
			dst = append(dst, src[start:i]...)
			start = i + 1
			zeros = 0
			continue
		}
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return append(dst, src[start:]...)
}
