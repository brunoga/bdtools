package av1

// A transform block's coefficients written (coeffs, spec 5.11.39): whether
// any is not zero, the luma's transform type, the end of block, the
// levels from the last back to the first (each by its context of the
// levels already coded around it), then the signs and the large levels'
// remainders from the first.

import "math/bits"

// writeCoeffs writes a transform block's coefficients, updating the
// level and dc contexts as a decoder does.
func (t *tileEnc) writeCoeffs(cd coder, tc *txCoded, update bool) {
	f, b := t.f, &t.b
	plane, txSz := tc.plane, tc.txSz
	x4, y4 := tc.x4, tc.y4
	w4, h4 := int(txWidth[txSz])>>2, int(txHeight[txSz])>>2
	txSzCtx := (int(txSizeSqr[txSz]) + int(txSizeSqrUp[txSz]) + 1) >> 1
	ptype := b2i(plane > 0)
	sub := 0
	if plane > 0 {
		sub = 1
	}
	maxX4, maxY4 := f.miCols>>uint(sub), f.miRows>>uint(sub)
	// all_zero
	bsize := int(subsampledSize[b.size][sub][sub])
	bw, bh := int(blockWidth[bsize]), int(blockHeight[bsize])
	w, h := int(txWidth[txSz]), int(txHeight[txSz])
	var ctx int
	if plane == 0 {
		top, left := 0, 0
		for k := range w4 {
			if x4+k < maxX4 {
				top = max(top, int(t.aboveLevel[plane][x4+k]))
			}
		}
		for k := range h4 {
			if y4+k < maxY4 {
				left = max(left, int(t.leftLevel[plane][y4+k]))
			}
		}
		top, left = min(top, 255), min(left, 255)
		switch {
		case bw == w && bh == h:
			ctx = 0
		case top == 0 && left == 0:
			ctx = 1
		case top == 0 || left == 0:
			ctx = 2 + b2i(max(top, left) > 3)
		case max(top, left) <= 3:
			ctx = 4
		case min(top, left) <= 3:
			ctx = 5
		default:
			ctx = 6
		}
	} else {
		above, left := uint8(0), uint8(0)
		for i := range w4 {
			if x4+i < maxX4 {
				above |= t.aboveLevel[plane][x4+i] | t.aboveDc[plane][x4+i]
			}
		}
		for i := range h4 {
			if y4+i < maxY4 {
				left |= t.leftLevel[plane][y4+i] | t.leftDc[plane][y4+i]
			}
		}
		ctx = 7 + b2i(above != 0) + b2i(left != 0)
		if bw*bh > w*h {
			ctx += 3
		}
	}
	cd.symbol(b2i(tc.eob == 0), t.cdf.txbSkip[txSzCtx][ctx][:])
	culLevel, dcCategory := 0, 0
	if tc.eob > 0 {
		culLevel, dcCategory = t.writeLevels(cd, tc, txSzCtx, ptype)
	}
	if !update {
		return
	}
	for i := range w4 {
		t.aboveLevel[plane][x4+i] = uint8(culLevel) //nolint:gosec // at most 63
		t.aboveDc[plane][x4+i] = uint8(dcCategory)  //nolint:gosec // 0..2
	}
	for i := range h4 {
		t.leftLevel[plane][y4+i] = uint8(culLevel) //nolint:gosec // at most 63
		t.leftDc[plane][y4+i] = uint8(dcCategory)  //nolint:gosec // 0..2
	}
}

// writeTxType is transform_type for an intra luma block.
func (t *tileEnc) writeTxType(cd coder, txSz, txType int) {
	set := t.txSet(txSz)
	if set == TX_SET_DCTONLY || t.f.fp.qIndex == 0 {
		return
	}
	dir := t.b.yMode
	if t.b.filterMode >= 0 {
		dir = int(filterIntraModeToIntraDir[t.b.filterMode])
	}
	sq := txSizeSqr[txSz]
	if set == TX_SET_INTRA_1 {
		for i, tt := range txTypeIntraInvSet1 {
			if int(tt) == txType {
				cd.symbol(i, t.cdf.intraTxSet1[sq][dir][:])
				return
			}
		}
	} else {
		for i, tt := range txTypeIntraInvSet2 {
			if int(tt) == txType {
				cd.symbol(i, t.cdf.intraTxSet2[sq][dir][:])
				return
			}
		}
	}
	panic("av1: a transform type not in its set")
}

// writeEob writes the end of block of a transform's levels: eob_pt and
// its extra bits.
func (t *tileEnc) writeEob(cd coder, txSz, cls, txSzCtx, ptype, eob int) {
	cdf := t.cdf
	eobPt := 1
	if eob >= 2 {
		eobPt = bits.Len(uint(eob-1)) + 1
	}
	ectx := b2i(cls != TX_CLASS_2D)
	switch min(int(txWidthLog2[txSz]), 5) + min(int(txHeightLog2[txSz]), 5) - 4 {
	case 0:
		cd.symbol(eobPt-1, cdf.eobPt16[ptype][ectx][:])
	case 1:
		cd.symbol(eobPt-1, cdf.eobPt32[ptype][ectx][:])
	case 2:
		cd.symbol(eobPt-1, cdf.eobPt64[ptype][ectx][:])
	case 3:
		cd.symbol(eobPt-1, cdf.eobPt128[ptype][ectx][:])
	case 4:
		cd.symbol(eobPt-1, cdf.eobPt256[ptype][ectx][:])
	case 5:
		cd.symbol(eobPt-1, cdf.eobPt512[ptype][:])
	default:
		cd.symbol(eobPt-1, cdf.eobPt1024[ptype][:])
	}
	if eobPt >= 3 {
		rem := eob - (1<<uint(eobPt-2) + 1)
		shift := eobPt - 3
		cd.symbol(rem>>uint(shift)&1, cdf.eobExtra[txSzCtx][ptype][eobPt-3][:])
		for i := 1; i < eobPt-2; i++ {
			cd.boolean(rem>>uint(eobPt-3-i)&1 != 0)
		}
	}
}

// writeLevels writes a block with coefficients, from its transform
// type on, giving its culLevel and dcCategory.
func (t *tileEnc) writeLevels(cd coder, tc *txCoded, txSzCtx, ptype int) (int, int) {
	txSz, txType, eob := tc.txSz, tc.txType, tc.eob
	if tc.plane == 0 {
		if t.b.isInter {
			t.writeInterTxType(cd, txSz, txType)
		} else {
			t.writeTxType(cd, txSz, txType)
		}
	}
	cdf := t.cdf
	quant := t.levels[tc.off:]
	scan := scanFor(txSz, txType)
	cls := txClass(txType)
	t.writeEob(cd, txSz, cls, txSzCtx, ptype, eob)
	// levels, last first
	adj := int(adjustedTxSize[txSz])
	bwl := int(txWidthLog2[adj])
	width, height := 1<<uint(bwl), int(txHeight[adj])
	stride := width + lvPad
	lv := t.lv[:(height+lvPad)*stride]
	clear(lv)
	for c := eob - 1; c >= 0; c-- {
		pos := int(scan[c])
		level := int(abs32(quant[pos]))
		if c == eob-1 {
			var ctx int
			switch {
			case c == 0:
				ctx = 0
			case c <= (height<<uint(bwl))/8:
				ctx = 1
			case c <= (height<<uint(bwl))/4:
				ctx = 2
			default:
				ctx = 3
			}
			cd.symbol(min(level, 3)-1, cdf.coeffBaseEob[txSzCtx][ptype][ctx][:])
		} else {
			cd.symbol(min(level, 3), cdf.coeffBase[txSzCtx][ptype][baseCtx(lv, txSz, cls, bwl, stride, pos)][:])
		}
		if level > NUM_BASE_LEVELS {
			bctx := brCtx(lv, cls, bwl, stride, pos)
			rem := level - (NUM_BASE_LEVELS + 1)
			for range COEFF_BASE_RANGE / (BR_CDF_SIZE - 1) {
				k := min(rem, BR_CDF_SIZE-1)
				cd.symbol(k, cdf.coeffBr[min(txSzCtx, TX_32X32)][ptype][bctx][:])
				rem -= k
				if k < BR_CDF_SIZE-1 {
					break
				}
			}
		}
		lv[lvAt(pos, bwl, stride)] = uint8(min(level, NUM_BASE_LEVELS+COEFF_BASE_RANGE+1)) //nolint:gosec // at most 15
	}
	// signs and remainders, first first
	cul, dcCat := 0, 0
	for c := range eob {
		pos := int(scan[c])
		v := quant[pos]
		if v == 0 {
			continue
		}
		if c == 0 {
			cd.symbol(b2i(v < 0), cdf.dcSign[ptype][t.dcSignCtx(tc)][:])
		} else {
			cd.boolean(v < 0)
		}
		level := int(abs32(v))
		if level > NUM_BASE_LEVELS+COEFF_BASE_RANGE {
			x := uint32(level - (NUM_BASE_LEVELS + COEFF_BASE_RANGE)) //nolint:gosec // positive
			n := bits.Len32(x)
			for range n - 1 {
				cd.boolean(false)
			}
			cd.boolean(true)
			cd.literal(x, n-1)
		}
		if pos == 0 {
			dcCat = 2
			if v < 0 {
				dcCat = 1
			}
		}
		cul += level & 0xFFFFF
	}
	return min(63, cul), dcCat
}

// lvPad is the columns right of and the rows below the levels' buffer
// (their stride width+lvPad) the contexts' neighbours read as 0, as
// libaom's: no neighbour is outside the block.
const lvPad = 4

// lvAt is the coefficient at pos's place in the levels' buffer.
func lvAt(pos, bwl, stride int) int {
	row := pos >> uint(bwl)
	return row*stride + pos - row<<uint(bwl)
}

// baseCtx is get_coeff_base_ctx for a coefficient not the last.
func baseCtx(lv []uint8, txSz, cls, bwl, stride, pos int) int {
	row := pos >> uint(bwl)
	col := pos - row<<uint(bwl)
	p := row*stride + col
	mag := 0
	if cls == TX_CLASS_2D {
		// sigRefDiffOffset's: right, below, below right, 2 right, 2 below
		q := lv[p : p+2*stride+1]
		mag = min(int(q[1]), 3) + min(int(q[stride]), 3) + min(int(q[stride+1]), 3) +
			min(int(q[2]), 3) + min(int(q[2*stride]), 3)
	} else {
		for _, o := range sigRefDiffOffset[cls] {
			mag += min(int(lv[p+int(o[0])*stride+int(o[1])]), 3)
		}
	}
	ctx := min((mag+1)>>1, 4)
	if cls == TX_CLASS_2D {
		if row == 0 && col == 0 {
			return 0
		}
		return ctx + int(coeffBaseCtxOffset[txSz][min(row, 4)][min(col, 4)])
	}
	idx := col
	if cls == TX_CLASS_VERT {
		idx = row
	}
	return ctx + int(coeffBasePosCtxOffset[min(idx, 2)])
}

// brCtx is coeff_br's context.
func brCtx(lv []uint8, cls, bwl, stride, pos int) int {
	row := pos >> uint(bwl)
	col := pos - row<<uint(bwl)
	p := row*stride + col
	mag := 0
	for _, o := range magRefOffsetWithTxClass[cls] {
		mag += min(int(lv[p+int(o[0])*stride+int(o[1])]), COEFF_BASE_RANGE+NUM_BASE_LEVELS+1)
	}
	mag = min((mag+1)>>1, 6)
	switch {
	case pos == 0:
		return mag
	case cls == TX_CLASS_2D:
		if row < 2 && col < 2 {
			return mag + 7
		}
	case cls == TX_CLASS_HORIZ:
		if col == 0 {
			return mag + 7
		}
	default:
		if row == 0 {
			return mag + 7
		}
	}
	return mag + 14
}

// dcSignCtx is dc_sign's context.
func (t *tileEnc) dcSignCtx(tc *txCoded) int {
	f := t.f
	sub := 0
	if tc.plane > 0 {
		sub = 1
	}
	maxX4, maxY4 := f.miCols>>uint(sub), f.miRows>>uint(sub)
	w4, h4 := int(txWidth[tc.txSz])>>2, int(txHeight[tc.txSz])>>2
	s := 0
	for k := range w4 {
		if tc.x4+k < maxX4 {
			switch t.aboveDc[tc.plane][tc.x4+k] {
			case 1:
				s--
			case 2:
				s++
			}
		}
	}
	for k := range h4 {
		if tc.y4+k < maxY4 {
			switch t.leftDc[tc.plane][tc.y4+k] {
			case 1:
				s--
			case 2:
				s++
			}
		}
	}
	switch {
	case s < 0:
		return 1
	case s > 0:
		return 2
	}
	return 0
}
