package dts

// bitReader reads bits most significant first; reading past the end
// gives zeros and a negative left().
type bitReader struct {
	b   []byte
	pos int
}

func (r *bitReader) left() int { return len(r.b)*8 - r.pos }

func (r *bitReader) bit() int {
	i := r.pos >> 3
	r.pos++
	if i >= len(r.b) {
		return 0
	}
	return int(r.b[i]>>(7-uint(r.pos-1)&7)) & 1 //nolint:gosec // a bit position
}

// u reads n bits (at most 32).
func (r *bitReader) u(n int) int {
	v := 0
	for range n {
		v = v<<1 | r.bit()
	}
	return v
}

// s reads n bits as two's complement.
func (r *bitReader) s(n int) int {
	v := r.u(n)
	if n > 0 && v&(1<<(n-1)) != 0 {
		v -= 1 << n
	}
	return v
}

func (r *bitReader) flag() bool { return r.bit() != 0 }

func (r *bitReader) skip(n int) { r.pos += n }

// vlc is a Huffman code book: a binary tree, each leaf a symbol.
type vlc struct {
	// node k's children are next[2k] and next[2k+1]: a node index, or
	// -1-symbol for a leaf (0 for none).
	next   []int32
	offset int
}

// newVLC builds a code book from (symbol, length) pairs in code order:
// each code the one after the last at its length (FFmpeg's
// ff_vlc_init_from_lengths), symbols plus offset.
func newVLC(pairs []uint8, n, offset int) *vlc {
	v := &vlc{next: []int32{0, 0}, offset: offset}
	code := uint64(0)
	for i := range n {
		sym, ln := int(pairs[2*i]), int(pairs[2*i+1])
		c := code >> (32 - ln)
		node := 0
		for b := ln - 1; b >= 0; b-- {
			bit := int(c>>b) & 1
			idx := 2*node + bit
			if b == 0 {
				v.next[idx] = int32(-1 - sym) //nolint:gosec // a small symbol
				break
			}
			if v.next[idx] <= 0 {
				v.next = append(v.next, 0, 0)
				v.next[idx] = int32(len(v.next)/2 - 1) //nolint:gosec // a node index
			}
			node = int(v.next[idx])
		}
		code += 1 << (32 - ln)
	}
	return v
}

// read decodes a symbol (0 on an invalid code).
func (v *vlc) read(r *bitReader) int {
	node := 0
	for range 32 {
		n := v.next[2*node+r.bit()]
		if n < 0 {
			return int(-1-n) + v.offset
		}
		if n == 0 {
			return 0
		}
		node = int(n)
	}
	return 0
}

// books are the core's code books.
var books = func() (b struct {
	quant    [10][7]*vlc
	bitalloc [5]*vlc
	scale    [5]*vlc
	tmode    [4]*vlc
}) {
	p := vlcPairs[:]
	take := func(n, offset int) *vlc {
		v := newVLC(p, n, offset)
		p = p[2*n:]
		return v
	}
	for i := range 10 {
		for j := range int(quantIndexGroupSize[i]) {
			b.quant[i][j] = take(bitallocSizes[i], bitallocOffsets[i])
		}
	}
	for i := range 5 {
		b.bitalloc[i] = take(12, 1)
	}
	for i := range 5 {
		b.scale[i] = take(129, -64)
	}
	for i := range 4 {
		b.tmode[i] = take(4, 0)
	}
	return b
}()
