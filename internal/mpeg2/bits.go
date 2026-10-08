// Package mpeg2 decodes MPEG-2 video (ISO/IEC 13818-2, Main Profile: 4:2:0,
// frame and field pictures, every prediction mode) in Go: a fallback where
// no GPU decodes a Blu-ray's or DVD's MPEG-2.
package mpeg2

import "errors"

var errShort = errors.New("mpeg2: the stream ends inside a syntax element")

// bits reads a byte slice's bits, most significant first.
type bits struct {
	b   []byte
	pos int // in bits
}

func (r *bits) left() int { return len(r.b)*8 - r.pos }

// peek gives the next n (at most 32) bits without consuming them, zeros past
// the end.
func (r *bits) peek(n int) uint32 {
	var v uint64
	byteAt := r.pos >> 3
	for i := range 5 {
		v <<= 8
		if byteAt+i < len(r.b) {
			v |= uint64(r.b[byteAt+i])
		}
	}
	v <<= uint(r.pos&7) + 24
	return uint32(v >> (64 - n))
}

func (r *bits) skip(n int) { r.pos += n }

func (r *bits) u(n int) int {
	if n == 0 {
		return 0
	}
	v := r.peek(n)
	r.pos += n
	return int(v)
}

func (r *bits) flag() bool { return r.u(1) != 0 }

// vlc is a variable length code table: the value and length of each code,
// looked up by the next maxLen bits.
type vlc struct {
	maxLen int
	value  []int16
	length []uint8 // 0: no code
}

// newVLC makes a table from codes (each code's bits and length) and the
// values they stand for.
func newVLC(codes [][2]int, values []int) *vlc {
	maxLen := 0
	for _, c := range codes {
		maxLen = max(maxLen, c[1])
	}
	t := &vlc{maxLen: maxLen, value: make([]int16, 1<<maxLen), length: make([]uint8, 1<<maxLen)}
	for i, c := range codes {
		code, n := c[0], c[1]
		first := code << (maxLen - n)
		for j := range 1 << (maxLen - n) {
			t.value[first+j] = int16(values[i])
			t.length[first+j] = uint8(n)
		}
	}
	return t
}

// read decodes a code: its value, and false when the bits are no code.
func (t *vlc) read(r *bits) (int, bool) {
	i := r.peek(t.maxLen)
	n := t.length[i]
	if n == 0 {
		return 0, false
	}
	r.pos += int(n)
	return int(t.value[i]), true
}
