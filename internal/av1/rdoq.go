package av1

// Quantization by rate and distortion: each coefficient rounded to the
// nearest level, then, from the last coded back to the first (the order
// they are coded in, so the contexts are the coded ones), the level or
// the one below kept, whichever costs less with its rate in the contexts
// so far. The distortion is measured on the coefficients: a coefficient's
// squared error is 1/64 of the pixels' (times dqDenom squared).

// quantize quantizes t.coef (the spec's layout, min(32, w) wide) into
// levels appended to t.levels, giving the end of block (in scan order).
func (t *tileEnc) quantize(plane, txSz, txType int) int {
	f := t.f
	tw, th := min(32, int(txWidth[txSz])), min(32, int(txHeight[txSz]))
	n := tw * th
	off := len(t.levels)
	t.levels = append(t.levels, make([]int32, n)...)
	q := t.levels[off:]
	den := int64(dqDenom(txSz))
	dcQ, acQ := int64(t.dcQ[plane]), int64(t.acQ[plane])
	inv := invScanFor(txSz, txType)[:n]
	eob := 0
	for i := range n {
		qs := acQ
		if i == 0 {
			qs = dcQ
		}
		c := int64(t.coef[i])
		neg := c < 0
		if neg {
			c = -c
		}
		v := c*den + qs/2
		if v < qs {
			continue // 0 (q is zeroed): most are, and no division
		}
		l := min(v/qs, 1<<18)
		if neg {
			l = -l
		}
		q[i] = int32(l) //nolint:gosec // clamped
		eob = max(eob, int(inv[i])+1)
	}
	scan := scanFor(txSz, txType)[:n]
	if eob == 0 || f.lossless || dbgNoRDOQ {
		return eob
	}
	// the greedy pass
	cdf := t.cdf
	txSzCtx := (int(txSizeSqr[txSz]) + int(txSizeSqrUp[txSz]) + 1) >> 1
	ptype := b2i(plane > 0)
	cls := txClass(txType)
	adj := int(adjustedTxSize[txSz])
	bwl := int(txWidthLog2[adj])
	width, height := 1<<uint(bwl), int(txHeight[adj])
	lv := t.lv[:width*height]
	clear(lv)
	scale := float64(den*den) / 64
	lambda := t.lambda
	brCDF := &cdf.coeffBr[min(txSzCtx, TX_32X32)][ptype]
	for c := eob - 1; c >= 0; c-- {
		pos := int(scan[c])
		l0 := int64(abs32(q[pos]))
		if l0 == 0 {
			continue
		}
		qs := acQ
		if pos == 0 {
			qs = dcQ
		}
		coef := float64(abs32(t.coef[pos]))
		var baseCDF []uint16
		last := c == eob-1
		if last {
			ctx := 3
			switch {
			case c == 0:
				ctx = 0
			case c <= (height<<uint(bwl))/8:
				ctx = 1
			case c <= (height<<uint(bwl))/4:
				ctx = 2
			}
			baseCDF = cdf.coeffBaseEob[txSzCtx][ptype][ctx][:]
		} else {
			baseCDF = cdf.coeffBase[txSzCtx][ptype][baseCtx(lv, txSz, cls, bwl, width, height, pos)][:]
		}
		bctx := -1
		bestL, bestJ := l0, 0.0
		for k, l := range [2]int64{l0, l0 - 1} {
			if l < 0 || (last && l == 0) {
				break
			}
			dq := float64(((l * qs) & 0xFFFFFF) / den)
			d := (coef - dq) * (coef - dq) * scale
			var r int32
			if last {
				r = symbolCost(int(min(l, 3))-1, baseCDF)
			} else {
				r = symbolCost(int(min(l, 3)), baseCDF)
			}
			if l > 0 {
				r += costScale // the sign
			}
			if l > NUM_BASE_LEVELS {
				if bctx < 0 {
					bctx = brCtx(lv, cls, bwl, width, height, pos)
				}
				rem := int(l) - (NUM_BASE_LEVELS + 1)
				for range COEFF_BASE_RANGE / (BR_CDF_SIZE - 1) {
					s := min(rem, BR_CDF_SIZE-1)
					r += symbolCost(s, brCDF[bctx][:])
					rem -= s
					if s < BR_CDF_SIZE-1 {
						break
					}
				}
				if l > NUM_BASE_LEVELS+COEFF_BASE_RANGE {
					x := l - (NUM_BASE_LEVELS + COEFF_BASE_RANGE)
					nb := 0
					for x>>uint(nb) > 1 {
						nb++
					}
					r += int32(2*nb+1) * costScale
				}
			}
			j := d + lambda*float64(r)/costScale
			if k == 0 || j < bestJ {
				bestL, bestJ = l, j
			}
		}
		if q[pos] < 0 {
			q[pos] = -int32(bestL) //nolint:gosec // a level
		} else {
			q[pos] = int32(bestL) //nolint:gosec // a level
		}
		lv[pos] = uint8(min(bestL, NUM_BASE_LEVELS+COEFF_BASE_RANGE+1)) //nolint:gosec // at most 15
	}
	// RDOQ only lowers levels: the end of block can only move back
	for eob > 0 && q[scan[eob-1]] == 0 {
		eob--
	}
	return eob
}
