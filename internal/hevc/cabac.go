package hevc

import mbits "math/bits"

// cabac is the arithmetic decoding engine (9.3.4.3): the 9-bit offset
// window over the slice data, and its range.
type cabac struct {
	data []byte
	pos  int // bits read
	rng  uint32
	off  uint32
	ctx  [numContexts]uint8 // pStateIdx<<1 | valMps
}

var rangeTabLps = [64][4]uint8{
	{128, 176, 208, 240}, {128, 167, 197, 227}, {128, 158, 187, 216}, {123, 150, 178, 205},
	{116, 142, 169, 195}, {111, 135, 160, 185}, {105, 128, 152, 175}, {100, 122, 144, 166},
	{95, 116, 137, 158}, {90, 110, 130, 150}, {85, 104, 123, 142}, {81, 99, 117, 135},
	{77, 94, 111, 128}, {73, 89, 105, 122}, {69, 85, 100, 116}, {66, 80, 95, 110},
	{62, 76, 90, 104}, {59, 72, 86, 99}, {56, 69, 81, 94}, {53, 65, 77, 89},
	{51, 62, 73, 85}, {48, 59, 69, 80}, {46, 56, 66, 76}, {43, 53, 63, 72},
	{41, 50, 59, 69}, {39, 48, 56, 65}, {37, 45, 54, 62}, {35, 43, 51, 59},
	{33, 41, 48, 56}, {32, 39, 46, 53}, {30, 37, 43, 50}, {29, 35, 41, 48},
	{27, 33, 39, 45}, {26, 31, 37, 43}, {24, 30, 35, 41}, {23, 28, 33, 39},
	{22, 27, 32, 37}, {21, 26, 30, 35}, {20, 24, 29, 33}, {19, 23, 27, 31},
	{18, 22, 26, 30}, {17, 21, 25, 28}, {16, 20, 23, 27}, {15, 19, 22, 25},
	{14, 18, 21, 24}, {14, 17, 20, 23}, {13, 16, 19, 22}, {12, 15, 18, 21},
	{12, 14, 17, 20}, {11, 14, 16, 19}, {11, 13, 15, 18}, {10, 12, 15, 17},
	{10, 12, 14, 16}, {9, 11, 13, 15}, {9, 11, 12, 14}, {8, 10, 12, 14},
	{8, 9, 11, 13}, {7, 9, 11, 12}, {7, 9, 10, 12}, {7, 8, 10, 11},
	{6, 8, 9, 11}, {6, 7, 9, 10}, {6, 7, 8, 9}, {2, 2, 2, 2},
}

var transIdxLps = [64]uint8{
	0, 0, 1, 2, 2, 4, 4, 5, 6, 7, 8, 9, 9, 11, 11, 12,
	13, 13, 15, 15, 16, 16, 18, 18, 19, 19, 21, 21, 22, 22, 23, 24,
	24, 25, 26, 26, 27, 27, 28, 29, 29, 30, 30, 30, 31, 32, 32, 33,
	33, 33, 34, 34, 35, 35, 35, 36, 36, 36, 37, 37, 37, 38, 38, 63,
}

// readBits reads n (at most 25) bits of the slice data, zeros past its
// end.
func (c *cabac) readBits(n int) uint32 {
	if n == 0 {
		return 0
	}
	at := c.pos >> 3
	var v uint32
	if at+4 <= len(c.data) {
		b := c.data[at : at+4]
		v = uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	} else {
		for i := range 4 {
			v <<= 8
			if at+i < len(c.data) {
				v |= uint32(c.data[at+i])
			}
		}
	}
	v = v << uint(c.pos&7) >> uint(32-n)
	c.pos += n
	return v
}

// init starts the engine at byte offset at of data (9.3.2.5).
func (c *cabac) init(data []byte, at int) {
	c.data = data
	c.pos = at * 8
	c.rng = 510
	c.off = c.readBits(9)
}

// alignedNext is the byte where data resumes after a terminating bin of
// 1: the last bit the engine read is the stop bit ending the arithmetic
// code, and zeros follow it to the byte's end.
func (c *cabac) alignedNext() int { return (c.pos + 7) >> 3 }

// initContexts sets every context from its initValue for the slice's QP
// (9.3.2.2).
func (c *cabac) initContexts(initType, qp int) {
	qp = min(max(qp, 0), 51)
	for i := range numContexts {
		v := int(cabacInit[initType][i])
		m := (v>>4)*5 - 45
		n := (v&15)<<3 - 16
		pre := min(max(((m*qp)>>4)+n, 1), 126)
		if pre <= 63 {
			c.ctx[i] = uint8((63 - pre) << 1)
		} else {
			c.ctx[i] = uint8((pre-64)<<1 | 1)
		}
	}
}

// decision decodes a bin with context ctx.
func (c *cabac) decision(ctx int) int {
	s := c.ctx[ctx]
	state := s >> 1
	mps := int(s & 1)
	lps := uint32(rangeTabLps[state][(c.rng>>6)&3])
	c.rng -= lps
	bin := mps
	if c.off >= c.rng {
		bin = 1 - mps
		c.off -= c.rng
		c.rng = lps
		if state == 0 {
			mps = 1 - mps
		}
		c.ctx[ctx] = transIdxLps[state]<<1 | uint8(mps)
	} else if state < 62 {
		c.ctx[ctx] = (state+1)<<1 | uint8(mps)
	}
	if c.rng < 256 {
		n := mbits.LeadingZeros32(c.rng) - 23
		c.rng <<= uint(n)
		c.off = c.off<<uint(n) | c.readBits(n)
	}
	return bin
}

// bypass decodes an equiprobable bin.
func (c *cabac) bypass() int {
	c.off = c.off<<1 | c.readBits(1)
	if c.off >= c.rng {
		c.off -= c.rng
		return 1
	}
	return 0
}

// bypassBits decodes n bypass bins as a number, first most significant.
func (c *cabac) bypassBits(n int) int {
	v := 0
	for range n {
		v = v<<1 | c.bypass()
	}
	return v
}

// terminate decodes a bin terminating the slice, a substream or a PCM
// block's coded part.
func (c *cabac) terminate() int {
	c.rng -= 2
	if c.off >= c.rng {
		return 1
	}
	if c.rng < 256 {
		c.rng <<= 1
		c.off = c.off<<1 | c.readBits(1)
	}
	return 0
}
