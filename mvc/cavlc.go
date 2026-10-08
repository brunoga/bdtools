package mvc

import "math/bits"

// vlcEntry is a decoded VLC symbol: value and code length (0 = invalid).
type vlcEntry struct {
	val uint8
	len uint8
}

// vlcTable decodes prefix codes of up to 16 bits using the number of
// leading zeros and the following 4 bits.
type vlcTable struct {
	t [17][16]vlcEntry
}

func (t *vlcTable) add(code uint32, length int, val int) {
	// leading zeros of the code
	lz := 0
	for lz < length && code>>(length-1-lz)&1 == 0 {
		lz++
	}
	if lz == length {
		// all zero code: matches any input with >= lz leading zeros
		for z := lz; z <= 16; z++ {
			for s := 0; s < 16; s++ {
				t.t[z][s] = vlcEntry{uint8(val), uint8(length)}
			}
		}
		return
	}
	rem := length - lz - 1 // bits after the leading one
	if rem > 4 {
		panic("vlc suffix too long")
	}
	suffix := int(code) & (1<<rem - 1)
	for s := 0; s < 16; s++ {
		if s>>(4-rem) == suffix {
			t.t[lz][s] = vlcEntry{uint8(val), uint8(length)}
		}
	}
}

func (br *bitReader) vlc(t *vlcTable) (int, bool) {
	v := br.peek(24)
	lz := bits.LeadingZeros32(v<<8 | 0x80)
	if lz > 16 {
		lz = 16
	}
	s := (v << 8 << uint(lz+1)) >> 28
	e := t.t[lz][s]
	if e.len == 0 {
		return 0, false
	}
	br.skip(uint(e.len))
	return int(e.val), true
}

var coeffTokenLen = [3][4 * 17]uint8{
	{
		1, 0, 0, 0,
		6, 2, 0, 0, 8, 6, 3, 0, 9, 8, 7, 5, 10, 9, 8, 6,
		11, 10, 9, 7, 13, 11, 10, 8, 13, 13, 11, 9, 13, 13, 13, 10,
		14, 14, 13, 11, 14, 14, 14, 13, 15, 15, 14, 14, 15, 15, 15, 14,
		16, 15, 15, 15, 16, 16, 16, 15, 16, 16, 16, 16, 16, 16, 16, 16,
	},
	{
		2, 0, 0, 0,
		6, 2, 0, 0, 6, 5, 3, 0, 7, 6, 6, 4, 8, 6, 6, 4,
		8, 7, 7, 5, 9, 8, 8, 6, 11, 9, 9, 6, 11, 11, 11, 7,
		12, 11, 11, 9, 12, 12, 12, 11, 12, 12, 12, 11, 13, 13, 13, 12,
		13, 13, 13, 13, 13, 14, 13, 13, 14, 14, 14, 13, 14, 14, 14, 14,
	},
	{
		4, 0, 0, 0,
		6, 4, 0, 0, 6, 5, 4, 0, 6, 5, 5, 4, 7, 5, 5, 4,
		7, 5, 5, 4, 7, 6, 6, 4, 7, 6, 6, 4, 8, 7, 7, 5,
		8, 8, 7, 6, 9, 8, 8, 7, 9, 9, 8, 8, 9, 9, 9, 8,
		10, 9, 9, 9, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10,
	},
}

var coeffTokenBits = [3][4 * 17]uint8{
	{
		1, 0, 0, 0,
		5, 1, 0, 0, 7, 4, 1, 0, 7, 6, 5, 3, 7, 6, 5, 3,
		7, 6, 5, 4, 15, 6, 5, 4, 11, 14, 5, 4, 8, 10, 13, 4,
		15, 14, 9, 4, 11, 10, 13, 12, 15, 14, 9, 12, 11, 10, 13, 8,
		15, 1, 9, 12, 11, 14, 13, 8, 7, 10, 9, 12, 4, 6, 5, 8,
	},
	{
		3, 0, 0, 0,
		11, 2, 0, 0, 7, 7, 3, 0, 7, 10, 9, 5, 7, 6, 5, 4,
		4, 6, 5, 6, 7, 6, 5, 8, 15, 6, 5, 4, 11, 14, 13, 4,
		15, 10, 9, 4, 11, 14, 13, 12, 8, 10, 9, 8, 15, 14, 13, 12,
		11, 10, 9, 12, 7, 11, 6, 8, 9, 8, 10, 1, 7, 6, 5, 4,
	},
	{
		15, 0, 0, 0,
		15, 14, 0, 0, 11, 15, 13, 0, 8, 12, 14, 12, 15, 10, 11, 11,
		11, 8, 9, 10, 9, 14, 13, 9, 8, 10, 9, 8, 15, 14, 13, 13,
		11, 14, 10, 12, 15, 10, 13, 12, 11, 14, 9, 12, 8, 10, 13, 8,
		13, 7, 9, 12, 9, 12, 11, 10, 5, 8, 7, 6, 1, 4, 3, 2,
	},
}

var chromaDCTokenLen = [4 * 5]uint8{2, 0, 0, 0, 6, 1, 0, 0, 6, 6, 3, 0, 6, 7, 7, 6, 6, 8, 8, 7}
var chromaDCTokenBits = [4 * 5]uint8{1, 0, 0, 0, 7, 1, 0, 0, 4, 6, 1, 0, 3, 3, 2, 5, 2, 3, 2, 0}

var totalZerosLen = [15][16]uint8{
	{1, 3, 3, 4, 4, 5, 5, 6, 6, 7, 7, 8, 8, 9, 9, 9},
	{3, 3, 3, 3, 3, 4, 4, 4, 4, 5, 5, 6, 6, 6, 6},
	{4, 3, 3, 3, 4, 4, 3, 3, 4, 5, 5, 6, 5, 6},
	{5, 3, 4, 4, 3, 3, 3, 4, 3, 4, 5, 5, 5},
	{4, 4, 4, 3, 3, 3, 3, 3, 4, 5, 4, 5},
	{6, 5, 3, 3, 3, 3, 3, 3, 4, 3, 6},
	{6, 5, 3, 3, 3, 2, 3, 4, 3, 6},
	{6, 4, 5, 3, 2, 2, 3, 3, 6},
	{6, 6, 4, 2, 2, 3, 2, 5},
	{5, 5, 3, 2, 2, 2, 4},
	{4, 4, 3, 3, 1, 3},
	{4, 4, 2, 1, 3},
	{3, 3, 1, 2},
	{2, 2, 1},
	{1, 1},
}

var totalZerosBits = [15][16]uint8{
	{1, 3, 2, 3, 2, 3, 2, 3, 2, 3, 2, 3, 2, 3, 2, 1},
	{7, 6, 5, 4, 3, 5, 4, 3, 2, 3, 2, 3, 2, 1, 0},
	{5, 7, 6, 5, 4, 3, 4, 3, 2, 3, 2, 1, 1, 0},
	{3, 7, 5, 4, 6, 5, 4, 3, 3, 2, 2, 1, 0},
	{5, 4, 3, 7, 6, 5, 4, 3, 2, 1, 1, 0},
	{1, 1, 7, 6, 5, 4, 3, 2, 1, 1, 0},
	{1, 1, 5, 4, 3, 3, 2, 1, 1, 0},
	{1, 1, 1, 3, 3, 2, 2, 1, 0},
	{1, 0, 1, 3, 2, 1, 1, 1},
	{1, 0, 1, 3, 2, 1, 1},
	{0, 1, 1, 2, 1, 3},
	{0, 1, 1, 1, 1},
	{0, 1, 1, 1},
	{0, 1, 1},
	{0, 1},
}

var chromaDCTotalZerosLen = [3][4]uint8{{1, 2, 3, 3}, {1, 2, 2}, {1, 1}}
var chromaDCTotalZerosBits = [3][4]uint8{{1, 1, 1, 0}, {1, 1, 0}, {1, 0}}

var runBeforeLen = [7][16]uint8{
	{1, 1},
	{1, 2, 2},
	{2, 2, 2, 2},
	{2, 2, 2, 3, 3},
	{2, 2, 3, 3, 3, 3},
	{2, 3, 3, 3, 3, 3, 3},
	{3, 3, 3, 3, 3, 3, 3, 4, 5, 6, 7, 8, 9, 10, 11},
}
var runBeforeBits = [7][16]uint8{
	{1, 0},
	{1, 1, 0},
	{3, 2, 1, 0},
	{3, 2, 1, 1, 0},
	{3, 2, 3, 2, 1, 0},
	{3, 0, 1, 3, 2, 5, 4},
	{7, 6, 5, 4, 3, 2, 1, 1, 1, 1, 1, 1, 1, 1, 1},
}

var (
	coeffTokenVLC [3]vlcTable // nC 0-1, 2-3, 4-7
	chromaDCVLC   vlcTable
	totalZerosVLC [15]vlcTable
	chromaDCTZVLC [3]vlcTable
	runBeforeVLC  [7]vlcTable
)

func init() {
	for t := 0; t < 3; t++ {
		for i := 0; i < 4*17; i++ {
			if l := coeffTokenLen[t][i]; l != 0 {
				coeffTokenVLC[t].add(uint32(coeffTokenBits[t][i]), int(l), i)
			}
		}
	}
	for i := 0; i < 4*5; i++ {
		if l := chromaDCTokenLen[i]; l != 0 {
			chromaDCVLC.add(uint32(chromaDCTokenBits[i]), int(l), i)
		}
	}
	for t := 0; t < 15; t++ {
		for i := 0; i < 16; i++ {
			if l := totalZerosLen[t][i]; l != 0 {
				totalZerosVLC[t].add(uint32(totalZerosBits[t][i]), int(l), i)
			}
		}
	}
	for t := 0; t < 3; t++ {
		for i := 0; i < 4; i++ {
			if l := chromaDCTotalZerosLen[t][i]; l != 0 {
				chromaDCTZVLC[t].add(uint32(chromaDCTotalZerosBits[t][i]), int(l), i)
			}
		}
	}
	for t := 0; t < 7; t++ {
		for i := 0; i < 16; i++ {
			if l := runBeforeLen[t][i]; l != 0 {
				runBeforeVLC[t].add(uint32(runBeforeBits[t][i]), int(l), i)
			}
		}
	}
}

// cavlcResidual parses residual_block_cavlc for nC (-1 for chroma DC) and
// maxNumCoeff coefficients, filling cb with scan indices relative to the
// start of the block. Returns TotalCoeff or -1 on error.
func (s *sliceDec) cavlcResidual(nC int, maxNum int, cb *coeffBuf) int {
	br := &s.br
	var token int
	var ok bool
	switch {
	case nC < 0:
		token, ok = br.vlc(&chromaDCVLC)
	case nC < 2:
		token, ok = br.vlc(&coeffTokenVLC[0])
	case nC < 4:
		token, ok = br.vlc(&coeffTokenVLC[1])
	case nC < 8:
		token, ok = br.vlc(&coeffTokenVLC[2])
	default:
		v := int(br.u(6))
		if v == 3 {
			token = 0
		} else {
			token = (v>>2+1)*4 + v&3
		}
		ok = true
	}
	if !ok {
		return -1
	}
	total, t1s := token>>2, token&3
	cb.n = total
	if total == 0 {
		return 0
	}
	if total > maxNum {
		return -1
	}
	var level [16]int32
	suffixLength := uint(0)
	if total > 10 && t1s < 3 {
		suffixLength = 1
	}
	for i := 0; i < total; i++ {
		if i < t1s {
			level[i] = 1 - 2*int32(br.u1())
			continue
		}
		// level_prefix
		prefix := 0
		for br.u1() == 0 {
			prefix++
			if prefix > 32 {
				return -1
			}
		}
		levelCode := int32(min(15, prefix)) << suffixLength
		if suffixLength > 0 || prefix >= 14 {
			size := suffixLength
			if prefix == 14 && suffixLength == 0 {
				size = 4
			}
			if prefix >= 15 {
				size = uint(prefix - 3)
			}
			if size > 0 {
				levelCode += int32(br.u(size))
			}
		}
		if prefix >= 15 && suffixLength == 0 {
			levelCode += 15
		}
		if prefix >= 16 {
			levelCode += (1 << (prefix - 3)) - 4096
		}
		if i == t1s && t1s < 3 {
			levelCode += 2
		}
		if levelCode&1 == 0 {
			level[i] = (levelCode + 2) >> 1
		} else {
			level[i] = (-levelCode - 1) >> 1
		}
		if suffixLength == 0 {
			suffixLength = 1
		}
		a := level[i]
		if a < 0 {
			a = -a
		}
		if a > 3<<(suffixLength-1) && suffixLength < 6 {
			suffixLength++
		}
	}
	zerosLeft := 0
	if total < maxNum {
		var tz int
		if nC < 0 {
			tz, ok = br.vlc(&chromaDCTZVLC[total-1])
		} else {
			tz, ok = br.vlc(&totalZerosVLC[total-1])
		}
		if !ok {
			return -1
		}
		zerosLeft = tz
	}
	if zerosLeft+total > maxNum {
		return -1
	}
	// runs: coefficients in reverse scan order
	pos := total + zerosLeft - 1
	for i := 0; i < total; i++ {
		cb.idx[total-1-i] = uint8(pos)
		cb.level[total-1-i] = level[i]
		if i == total-1 {
			break
		}
		run := 0
		if zerosLeft > 0 {
			t := zerosLeft - 1
			if t > 6 {
				t = 6
			}
			run, ok = br.vlc(&runBeforeVLC[t])
			if !ok || run > zerosLeft {
				return -1
			}
			zerosLeft -= run
		}
		pos -= run + 1
	}
	return total
}
