package av1

// Inter blocks' syntax: the reference frame, the motion vector difference,
// the variable transform size and the inter transform types.

import "math/bits"

// countRefs is count_refs: how many of the above and left blocks' two
// references are ref.
func (t *tileEnc) countRefs(ref int) int {
	f, b := t.f, &t.b
	n := 0
	if b.availU {
		i := f.mi(b.r-1, b.c)
		n += b2i(int(f.refFrames[0][i]) == ref) + b2i(int(f.refFrames[1][i]) == ref)
	}
	if b.availL {
		i := f.mi(b.r, b.c-1)
		n += b2i(int(f.refFrames[0][i]) == ref) + b2i(int(f.refFrames[1][i]) == ref)
	}
	return n
}

func refCountCtx(c0, c1 int) int {
	switch {
	case c0 < c1:
		return 0
	case c0 == c1:
		return 1
	}
	return 2
}

// writeRefFrame is read_ref_frames for a single reference (no compound).
func (t *tileEnc) writeRefFrame(cd coder, ref int) {
	cnt := func(refs ...int) int {
		n := 0
		for _, r := range refs {
			n += t.countRefs(r)
		}
		return n
	}
	sr := &t.cdf.singleRef
	fwd := cnt(LAST_FRAME, LAST2_FRAME, LAST3_FRAME, GOLDEN_FRAME)
	bwd := cnt(BWDREF_FRAME, ALTREF2_FRAME, ALTREF_FRAME)
	isBwd := ref >= BWDREF_FRAME
	cd.symbol(b2i(isBwd), sr[refCountCtx(fwd, bwd)][0][:])
	if isBwd {
		// single_ref_p2: comp_bwdref's context; p6: comp_bwdref_p1's
		brf, arf2, arf := cnt(BWDREF_FRAME), cnt(ALTREF2_FRAME), cnt(ALTREF_FRAME)
		cd.symbol(b2i(ref == ALTREF_FRAME), sr[refCountCtx(brf+arf2, arf)][1][:])
		if ref != ALTREF_FRAME {
			cd.symbol(b2i(ref == ALTREF2_FRAME), sr[refCountCtx(brf, arf2)][5][:])
		}
		return
	}
	l12, l3g := cnt(LAST_FRAME, LAST2_FRAME), cnt(LAST3_FRAME, GOLDEN_FRAME)
	high := ref == LAST3_FRAME || ref == GOLDEN_FRAME
	cd.symbol(b2i(high), sr[refCountCtx(l12, l3g)][2][:])
	if high {
		cd.symbol(b2i(ref == GOLDEN_FRAME), sr[refCountCtx(cnt(LAST3_FRAME), cnt(GOLDEN_FRAME))][4][:])
	} else {
		cd.symbol(b2i(ref == LAST2_FRAME), sr[refCountCtx(cnt(LAST_FRAME), cnt(LAST2_FRAME))][3][:])
	}
}

// writeMvDiff is read_mv: a motion vector's difference from its
// prediction (1/8 units; with high precision off both are even).
func (t *tileEnc) writeMvDiff(cd coder, d mv) {
	ctx := 0 // MvCtx: no intrabc
	joint := MV_JOINT_ZERO
	switch {
	case d.row != 0 && d.col != 0:
		joint = MV_JOINT_HNZVNZ
	case d.row != 0:
		joint = MV_JOINT_HZVNZ
	case d.col != 0:
		joint = MV_JOINT_HNZVZ
	}
	cd.symbol(joint, t.cdf.mvJoint[ctx][:])
	if d.row != 0 {
		t.writeMvComponent(cd, ctx, 0, d.row)
	}
	if d.col != 0 {
		t.writeMvComponent(cd, ctx, 1, d.col)
	}
}

// mvClass splits a component's magnitude less one into its class and
// its offset within it.
func mvClass(z int) (class, offset int) {
	if z < CLASS0_SIZE<<3 {
		return 0, z
	}
	class = bits.Len(uint(z)) - 4
	return class, z - CLASS0_SIZE<<uint(class+2)
}

// writeMvComponent is read_mv_component.
func (t *tileEnc) writeMvComponent(cd coder, ctx, comp int, v int32) {
	c := t.cdf
	hp := t.f.fp.highPrecisionMV
	cd.symbol(b2i(v < 0), c.mvSign[ctx][comp][:])
	z := int(abs32(v)) - 1
	class, off := mvClass(z)
	cd.symbol(class, c.mvClass[ctx][comp][:])
	if class == 0 {
		bit := off >> 3
		cd.symbol(bit, c.mvClass0Bit[ctx][comp][:])
		cd.symbol(off>>1&3, c.mvClass0Fr[ctx][comp][bit][:])
		if hp {
			cd.symbol(off&1, c.mvClass0Hp[ctx][comp][:])
		}
		return
	}
	d := off >> 3
	for i := range class {
		cd.symbol(d>>uint(i)&1, c.mvBit[ctx][comp][i][:])
	}
	cd.symbol(off>>1&3, c.mvFr[ctx][comp][:])
	if hp {
		cd.symbol(off&1, c.mvHp[ctx][comp][:])
	}
}

// aboveTxWidth is get_above_tx_width at (row, col).
func (t *tileEnc) aboveTxWidth(row, col int) int {
	f, b := t.f, &t.b
	if row == b.r {
		if !b.availU {
			return 64
		}
		if i := f.mi(row-1, col); f.skip[i] != 0 && f.isInter[i] != 0 {
			return int(blockWidth[f.miSize[i]])
		}
	}
	return int(txWidth[f.txSize[f.mi(row-1, col)]])
}

// leftTxHeight is get_left_tx_height at (row, col).
func (t *tileEnc) leftTxHeight(row, col int) int {
	f, b := t.f, &t.b
	if col == b.c {
		if !b.availL {
			return 64
		}
		if i := f.mi(row, col-1); f.skip[i] != 0 && f.isInter[i] != 0 {
			return int(blockHeight[f.miSize[i]])
		}
	}
	return int(txHeight[f.txSize[f.mi(row, col-1)]])
}

// writeTxfmSplit is txfm_split at (row, col) for a transform of txSz.
func (t *tileEnc) writeTxfmSplit(cd coder, row, col, txSz int, split bool) {
	b := &t.b
	above := b2i(t.aboveTxWidth(row, col) < int(txWidth[txSz]))
	left := b2i(t.leftTxHeight(row, col) < int(txHeight[txSz]))
	size := min(64, max(int(blockWidth[b.size]), int(blockHeight[b.size])))
	maxTx := squareTx(size)
	ctx := b2i(int(txSizeSqrUp[txSz]) != maxTx)*3 + (TX_SIZES-1-maxTx)*6 + above + left
	cd.symbol(b2i(split), t.cdf.txfmSplit[ctx][:])
}

// squareTx is the square transform size of a side.
func squareTx(side int) int {
	switch side {
	case 4:
		return TX_4X4
	case 8:
		return TX_8X8
	case 16:
		return TX_16X16
	case 32:
		return TX_32X32
	}
	return TX_64X64
}

// interTxSet is get_tx_set for an inter block.
func (t *tileEnc) interTxSet(txSz int) int {
	sq, up := int(txSizeSqr[txSz]), int(txSizeSqrUp[txSz])
	switch {
	case up > TX_32X32:
		return TX_SET_DCTONLY
	case t.f.fp.reducedTx || up == TX_32X32:
		return TX_SET_INTER_3
	case sq == TX_16X16:
		return TX_SET_INTER_2
	}
	return TX_SET_INTER_1
}

// writeInterTxType is transform_type for an inter luma block.
func (t *tileEnc) writeInterTxType(cd coder, txSz, tt int) {
	set := t.interTxSet(txSz)
	if set == TX_SET_DCTONLY || t.f.fp.qIndex == 0 {
		return
	}
	sq := txSizeSqr[txSz]
	find := func(inv []txType) int {
		for i, v := range inv {
			if int(v) == tt {
				return i
			}
		}
		panic("av1: a transform type not in its set")
	}
	switch set {
	case TX_SET_INTER_1:
		cd.symbol(find(txTypeInterInvSet1[:]), t.cdf.interTxSet1[sq][:])
	case TX_SET_INTER_2:
		cd.symbol(find(txTypeInterInvSet2[:]), t.cdf.interTxSet2[:])
	default:
		cd.symbol(find(txTypeInterInvSet3[:]), t.cdf.interTxSet3[sq][:])
	}
}

// interTxTypes are the transform types an inter luma block may try.
func (t *tileEnc) interTxTypes(txSz int) []int {
	switch t.interTxSet(txSz) {
	case TX_SET_DCTONLY:
		return []int{DCT_DCT}
	case TX_SET_INTER_3:
		return []int{DCT_DCT, IDTX}
	case TX_SET_INTER_2:
		return []int{DCT_DCT, ADST_DCT, DCT_ADST, ADST_ADST, IDTX, V_DCT, H_DCT}
	}
	return []int{DCT_DCT, ADST_DCT, DCT_ADST, ADST_ADST, IDTX, V_DCT, H_DCT, FLIPADST_DCT, DCT_FLIPADST}
}
