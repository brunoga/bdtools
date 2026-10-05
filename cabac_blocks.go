package mvc

// Multi-block residual decoding. A block descriptor packs, for one
// residual block of a macroblock, where its coded_block_flag context
// conditions come from, which cbp bit gates it and where its coefficients
// go; the decoders walk a static descriptor table per block category.
//
//	bits 0-4   cbp bit gating the block
//	bits 5-9   bit set in the macroblock's cbf mask when coded
//	bits 10-14 bit of the left condition (in cur or the left mask)
//	bits 15-19 bit of the top condition
//	bit 20     left condition comes from the current macroblock's mask
//	bit 21     top condition comes from the current macroblock's mask
//	bit 22     no coded_block_flag (8x8 blocks)
//	bit 23     chroma component (selects the second scale table)
//	bits 24-39 destination offset in int16 units
//	bits 40-44 bit set in the nz/ac results when coded
func mkDesc(cbpBit, curBit, aBit int, aCur bool, bBit int, bCur bool, noCbf bool, comp, dstOff, nzBit int) uint64 {
	d := uint64(cbpBit) | uint64(curBit)<<5 | uint64(aBit)<<10 | uint64(bBit)<<15 |
		uint64(comp)<<23 | uint64(dstOff)<<24 | uint64(nzBit)<<40
	if aCur {
		d |= 1 << 20
	}
	if bCur {
		d |= 1 << 21
	}
	if noCbf {
		d |= 1 << 22
	}
	return d
}

var (
	lumaBlkDesc  [16]uint64 // luma 4x4 blocks in decoding order (cat 1 and 2)
	luma8x8Desc  [4]uint64  // luma 8x8 blocks (cat 5)
	chromaACDesc [8]uint64  // chroma AC blocks, Cb then Cr (cat 4)
)

func init() {
	for blk := 0; blk < 16; blk++ {
		bx, by := int(blkX[blk]), int(blkY[blk])
		r := by*4 + bx
		aBit, aCur := r+3, false
		if bx > 0 {
			aBit, aCur = r-1, true
		}
		bBit, bCur := r+12, false
		if by > 0 {
			bBit, bCur = r-4, true
		}
		// (blkOff16 is filled by another init function, so compute the
		// offset here: 4 rows of 16 words per block row, 4 words per block)
		lumaBlkDesc[blk] = mkDesc(blk>>2, r, aBit, aCur, bBit, bCur, false, 0, by*64+bx*4, r)
	}
	for b8 := 0; b8 < 4; b8++ {
		luma8x8Desc[b8] = mkDesc(b8, 0, 0, false, 0, false, true, 0, b8>>1*128+b8&1*8, b8)
	}
	for c := 0; c < 2; c++ {
		for b := 0; b < 4; b++ {
			bx, by := b&1, b>>1
			bit := 16 + c*4 + b
			aBit, aCur := bit+1, false
			if bx > 0 {
				aBit, aCur = bit-1, true
			}
			bBit, bCur := bit+2, false
			if by > 0 {
				bBit, bCur = bit-2, true
			}
			chromaACDesc[c*4+b] = mkDesc(0, bit, aBit, aCur, bBit, bCur, false, c, c*64+by*32+bx*4, c*4+b)
		}
	}
}

// blockArgs are the parameters shared by the blocks of one category.
type blockArgs struct {
	cat        int
	desc       []uint64
	cbp        int
	cbfA, cbfB uint32 // neighbour cbf masks (all ones: unavailable and intra)
	dst        []int16
	scale      [2][]int32 // per chroma component (luma uses [0])
	pos        []uint8
	shifts     [2]int
}

// cabacBlocks decodes the blocks of a and returns the updated cbf mask of
// the macroblock and the masks of coded blocks and of blocks with AC
// coefficients.
func (s *sliceDec) cabacBlocks(a *blockArgs, cbfCur uint32) (cbfOut, nz, ac uint32) {
	if useCabacAsm {
		return s.cabacBlocksAsm(a, cbfCur)
	}
	return s.cabacBlocksGo(a, cbfCur)
}

func (s *sliceDec) cabacBlocksGo(a *blockArgs, cbfCur uint32) (cbfOut, nz, ac uint32) {
	maxNum := [6]int{16, 15, 16, 4, 15, 64}[a.cat]
	catBase := 85 + cbfCatOffset[min(a.cat, 4)]
	cb := &s.cb
	for _, d := range a.desc {
		if a.cbp>>(d&31)&1 == 0 {
			continue
		}
		cbfCtx := -1
		if d>>22&1 == 0 {
			var ca, cbv uint32
			if d>>20&1 != 0 {
				ca = cbfCur >> (d >> 10 & 31) & 1
			} else {
				ca = a.cbfA >> (d >> 10 & 31) & 1
			}
			if d>>21&1 != 0 {
				cbv = cbfCur >> (d >> 15 & 31) & 1
			} else {
				cbv = a.cbfB >> (d >> 15 & 31) & 1
			}
			cbfCtx = catBase + int(ca) + 2*int(cbv)
		}
		comp := int(d >> 23 & 1)
		dst := a.dst[d>>24&0xffff:]
		if !s.cabacBlockDQ(cbfCtx, a.cat, maxNum, cb, dst, a.scale[comp], a.pos, a.shifts[comp]) {
			continue
		}
		if d>>22&1 == 0 {
			cbfCur |= 1 << (d >> 5 & 31)
		}
		bit := uint32(1) << (d >> 40 & 31)
		nz |= bit
		if cb.n != 1 || cb.idx[0] != 0 {
			ac |= bit
		}
	}
	return cbfCur, nz, ac
}
