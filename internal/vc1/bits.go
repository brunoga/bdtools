// Package vc1 decodes SMPTE 421M (VC-1) Advanced Profile video in Go:
// progressive, interlaced frame and interlaced field pictures, 4:2:0 8-bit,
// as Blu-ray and HD DVD carry it. It is a fallback where no GPU decodes a
// disc's VC-1.
package vc1

import "errors"

var (
	errShort  = errors.New("vc1: the stream ends inside a syntax element")
	errStream = errors.New("vc1: an invalid stream")
)

// bits reads a byte slice's bits, most significant first.
type bits struct {
	b   []byte
	pos int // in bits
}

func (r *bits) left() int { return len(r.b)*8 - r.pos }

// peek gives the next n (at most 32) bits without consuming them, zeros past
// the end.
func (r *bits) peek(n int) uint32 {
	byteAt := r.pos >> 3
	var v uint64
	if byteAt+8 <= len(r.b) {
		b := r.b[byteAt : byteAt+8]
		v = uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | uint64(b[3])<<32 |
			uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7])
	} else {
		for i := range 8 {
			v <<= 8
			if byteAt+i < len(r.b) {
				v |= uint64(r.b[byteAt+i])
			}
		}
	}
	v <<= uint(r.pos & 7)
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

// unary reads ones (stop 0) or zeros (stop 1) up to a stopping bit or max
// of them, giving their count.
func (r *bits) unary(stop, max int) int {
	n := 0
	for n < max && r.u(1) != stop {
		n++
	}
	return n
}

// v012 reads the code 0, 10, 11 as 0, 1, 2.
func (r *bits) v012() int {
	if r.u(1) == 0 {
		return 0
	}
	return 1 + r.u(1)
}

// vlc is a variable length code table, looked up a level of bits at a
// time: an entry is a value and its code's length, or (length 0) the
// sub-table for codes longer than the level.
type vlc struct {
	bits  int
	value []int32
	len   []uint8
	sub   []*vlc
}

const vlcLevel = 9

// newVLC makes a table from codes and their lengths: code i stands for
// value i (lengths of 0 have no code).
func newVLC(codes []uint32, lens []uint8) *vlc {
	return buildVLC(codes, lens, nil, 0)
}

func buildVLC(codes []uint32, lens []uint8, values []int32, consumed int) *vlc {
	maxLen := 0
	for _, n := range lens {
		maxLen = max(maxLen, int(n)-consumed)
	}
	level := min(maxLen, vlcLevel)
	t := &vlc{bits: level, value: make([]int32, 1<<level), len: make([]uint8, 1<<level), sub: make([]*vlc, 1<<level)}
	type group struct {
		codes  []uint32
		lens   []uint8
		values []int32
	}
	long := map[uint32]*group{}
	for i, n := range lens {
		rest := int(n) - consumed
		if n == 0 || rest <= 0 {
			continue
		}
		code := codes[i] & (1<<rest - 1) // the bits not consumed yet
		val := int32(i)
		if values != nil {
			val = values[i]
		}
		if rest <= level {
			first := code << (level - rest)
			for j := range uint32(1) << (level - rest) {
				t.value[first+j] = val
				t.len[first+j] = uint8(rest)
			}
			continue
		}
		prefix := code >> (rest - level)
		g := long[prefix]
		if g == nil {
			g = &group{}
			long[prefix] = g
		}
		g.codes = append(g.codes, code)
		g.lens = append(g.lens, n)
		g.values = append(g.values, val)
	}
	for prefix, g := range long {
		t.sub[prefix] = buildVLC(g.codes, g.lens, g.values, consumed+level)
	}
	return t
}

// read decodes a code: its value, and false when the bits are no code.
func (t *vlc) read(r *bits) (int, bool) {
	for {
		i := r.peek(t.bits)
		if n := t.len[i]; n != 0 {
			r.pos += int(n)
			return int(t.value[i]), true
		}
		if t.sub[i] == nil {
			return 0, false
		}
		r.pos += t.bits
		t = t.sub[i]
	}
}
