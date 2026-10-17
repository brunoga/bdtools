package av1

// A tile's symbols recorded as the tile is decided, and coded at the end
// of the frame (packed), when what is decided after the tiles (the CDEF
// strengths) is known. A record names its CDF by where it is in the
// tile's cdfs, so the pack codes it with its own CDFs, which adapt as a
// decoder's do; the decisions' copy adapts alike as the record is made.

import "unsafe"

// Record kinds.
const (
	opSymbol    = iota // a symbol with a CDF of the cdfs
	opBool             // an equiprobable bit
	opSplitHorz        // split_or_horz, from a partition CDF
	opSplitVert        // split_or_vert
	opCdef             // a 64x64's cdef_idx, its bits (n) filled in at the pack
)

type symOp struct {
	kind uint8
	n    uint8  // the CDF's symbols; a split's block size; cdef_idx's bits
	s    uint16 // the symbol; for opCdef, which 64x64 (an index)
	off  uint32 // the CDF's place in cdfs, in uint16s
}

// recorder records symbols, adapting the CDFs it is given.
type recorder struct {
	base *cdfs
	ops  []symOp
}

func (r *recorder) offset(cdf []uint16) uint32 {
	return uint32((uintptr(unsafe.Pointer(&cdf[0])) - uintptr(unsafe.Pointer(r.base))) / 2) //nolint:gosec // within cdfs
}

func (r *recorder) symbol(s int, cdf []uint16) {
	r.ops = append(r.ops, symOp{kind: opSymbol, n: uint8(len(cdf) - 1), s: uint16(s), off: r.offset(cdf)}) //nolint:gosec // small
	adaptCDF(cdf, s)
}

// encode is only used for the split CDFs, which are made from the
// partition's: splitCDF records them.
func (r *recorder) encode(int, []uint16, int) { panic("av1: recorder.encode") }

func (r *recorder) boolean(b bool) {
	r.ops = append(r.ops, symOp{kind: opBool, s: uint16(b2i(b))})
}

func (r *recorder) literal(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		r.boolean(v>>uint(i)&1 != 0)
	}
}

// split records split_or_horz (horz) or split_or_vert.
func (r *recorder) split(split bool, partCDF []uint16, bSize int, horz bool) {
	k := uint8(opSplitVert)
	if horz {
		k = opSplitHorz
	}
	r.ops = append(r.ops, symOp{kind: k, n: uint8(bSize), s: uint16(b2i(split)), off: r.offset(partCDF)}) //nolint:gosec // small
}

// cdef records a 64x64's cdef_idx, to be filled in.
func (r *recorder) cdef(index int) {
	r.ops = append(r.ops, symOp{kind: opCdef, s: uint16(index)}) //nolint:gosec // an index
}

// pack codes a tile's record from the frame's CDFs (start, which it does
// not change), giving the bytes and the CDFs as the tile ends.
func pack(ops []symOp, start *cdfs, adapt bool, cdefIdx []int8, cdefBits int) ([]byte, *cdfs) {
	cc := *start
	c := &cc
	base := unsafe.Pointer(c)
	at := func(off uint32, n int) []uint16 {
		return unsafe.Slice((*uint16)(unsafe.Add(base, uintptr(off)*2)), n)
	}
	w := newSymbolWriter(adapt)
	for _, op := range ops {
		switch op.kind {
		case opSymbol:
			w.symbol(int(op.s), at(op.off, int(op.n)+1))
		case opBool:
			w.boolean(op.s != 0)
		case opSplitHorz:
			w.encode(int(op.s), splitOrHorz(at(op.off, partitionCDFLen(int(op.n))), int(op.n)), 2)
		case opSplitVert:
			w.encode(int(op.s), splitOrVert(at(op.off, partitionCDFLen(int(op.n))), int(op.n)), 2)
		case opCdef:
			w.literal(uint32(cdefIdx[op.s]), cdefBits) //nolint:gosec // an index
		}
	}
	return w.finish(), c
}

// partitionCDFLen is a partition CDF's length by block size.
func partitionCDFLen(bSize int) int {
	switch miWidthLog2[bSize] {
	case 1:
		return 5
	case 5:
		return 9
	}
	return 11
}
