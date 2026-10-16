package hevc

// The residual coding writer (7.3.8.11), the mirror of the decoder's
// residual: the same contexts in the same order, from the levels.

// scanIdxFor is the scan of a transform block (7.4.9.11): intra 4x4 and
// 8x8 luma (4x4 chroma) blocks scan along their prediction direction.
func scanIdxFor(intra bool, mode, log2Size, ci int) int {
	if !intra || log2Size != 2 && (log2Size != 3 || ci != 0) {
		return 0
	}
	switch {
	case mode >= 6 && mode <= 14:
		return 2
	case mode >= 22 && mode <= 30:
		return 1
	}
	return 0
}

// lastPosCode splits a last significant coefficient's position into its
// prefix (a truncated unary code) and suffix (fixed length) parts.
func lastPosCode(pos int) (prefix int, suffix uint32, suffixBits int) {
	if pos < 4 {
		return pos, 0, 0
	}
	n := 0
	for 1<<(n+1) <= pos {
		n++
	}
	// Groups 2k and 2k+1 cover [2^k, 3*2^(k-1)) and [3*2^(k-1), 2^(k+1)).
	prefix = 2 * n
	base := 1 << n
	if pos >= base+base>>1 {
		prefix++
		base += base >> 1
	}
	return prefix, uint32(pos - base), n - 1 //nolint:gosec // a small offset
}

// sigCtxInc is the context increment of a significant_coeff_flag
// (9.3.4.2.5) at scan position (xP, yP) of sub-block (xS, yS).
func sigCtxInc(log2Size, ci, scanIdx, xS, yS, xP, yP, prevCsbf int) int {
	xC, yC := xS<<2+xP, yS<<2+yP
	var sigCtx int
	switch {
	case log2Size == 2:
		sigCtx = ctxIdxMap[yC<<2+xC]
	case xC+yC == 0:
		sigCtx = 0
	default:
		switch prevCsbf {
		case 0:
			switch {
			case xP+yP == 0:
				sigCtx = 2
			case xP+yP < 3:
				sigCtx = 1
			}
		case 1:
			sigCtx = max(2-yP, 0)
		case 2:
			sigCtx = max(2-xP, 0)
		default:
			sigCtx = 2
		}
		if ci == 0 {
			if xS+yS > 0 {
				sigCtx += 3
			}
			if log2Size == 3 {
				if scanIdx == 0 {
					sigCtx += 9
				} else {
					sigCtx += 15
				}
			} else {
				sigCtx += 21
			}
		} else {
			if log2Size == 3 {
				sigCtx += 9
			} else {
				sigCtx += 12
			}
		}
	}
	if ci > 0 {
		sigCtx += 27
	}
	return sigCtx
}

// subBlockScan is the scan of the sub-blocks of a transform block.
func subBlockScan(log2Size, scanIdx int) [][2]uint8 {
	switch log2Size - 2 {
	case 0:
		return [][2]uint8{{0, 0}}
	case 1:
		return scanOrder[0][scanIdx]
	case 2:
		return scanOrder[1][scanIdx]
	}
	return scanOrder[2][scanIdx]
}

// writeResidual codes the levels of a transform block (size 1 << log2Size
// of component ci, row major, at least one non-zero) with scan scanIdx.
// Sign hiding and transform skip are off in the streams written.
func writeResidual(bc binCoder, coeffs []int32, log2Size, ci, scanIdx int) {
	size := 1 << log2Size
	sbScan := subBlockScan(log2Size, scanIdx)
	posScan := scanOrder[1][scanIdx]
	at := func(sb, n int) int32 {
		xC, yC := int(sbScan[sb][0])<<2+int(posScan[n][0]), int(sbScan[sb][1])<<2+int(posScan[n][1])
		return coeffs[yC*size+xC]
	}
	// The last significant coefficient in scan order.
	lastSb, lastPos := -1, -1
	for i := len(sbScan) - 1; i >= 0 && lastSb < 0; i-- {
		for n := 15; n >= 0; n-- {
			if at(i, n) != 0 {
				lastSb, lastPos = i, n
				break
			}
		}
	}
	if lastSb < 0 {
		panic("hevc: writeResidual of an empty block")
	}
	lastX := int(sbScan[lastSb][0])<<2 + int(posScan[lastPos][0])
	lastY := int(sbScan[lastSb][1])<<2 + int(posScan[lastPos][1])
	if scanIdx == 2 {
		lastX, lastY = lastY, lastX // coded transposed for the vertical scan
	}
	maxPrefix := log2Size<<1 - 1
	var ctxOff, ctxShift int
	if ci == 0 {
		ctxOff, ctxShift = 3*(log2Size-2)+((log2Size-1)>>2), (log2Size+1)>>2
	} else {
		ctxOff, ctxShift = 15, log2Size-2
	}
	px, sx, nx := lastPosCode(lastX)
	py, sy, ny := lastPosCode(lastY)
	for i := range px {
		bc.decision(ctxLastSignificantCoeffXPrefix+ctxOff+i>>ctxShift, 1)
	}
	if px < maxPrefix {
		bc.decision(ctxLastSignificantCoeffXPrefix+ctxOff+px>>ctxShift, 0)
	}
	for i := range py {
		bc.decision(ctxLastSignificantCoeffYPrefix+ctxOff+i>>ctxShift, 1)
	}
	if py < maxPrefix {
		bc.decision(ctxLastSignificantCoeffYPrefix+ctxOff+py>>ctxShift, 0)
	}
	if px > 3 {
		bc.bypassBits(sx, nx)
	}
	if py > 3 {
		bc.bypassBits(sy, ny)
	}
	var csbf [8][8]bool
	greater1Ctx := 1
	firstSb := true
	sbW := 1 << (log2Size - 2)
	for i := lastSb; i >= 0; i-- {
		xS, yS := int(sbScan[i][0]), int(sbScan[i][1])
		var lv [16]int32
		nz := false
		for n := range 16 {
			lv[n] = at(i, n)
			if lv[n] != 0 {
				nz = true
			}
		}
		inferDC := false
		if i < lastSb && i > 0 {
			ctx := 0
			if xS < sbW-1 && csbf[xS+1][yS] {
				ctx = 1
			}
			if yS < sbW-1 && csbf[xS][yS+1] {
				ctx = 1
			}
			if ci > 0 {
				ctx += 2
			}
			bc.decision(ctxSignificantCoeffGroupFlag+ctx, boolInt(nz))
			csbf[xS][yS] = nz
			inferDC = true
		} else {
			csbf[xS][yS] = true
		}
		if !csbf[xS][yS] {
			continue
		}
		prevCsbf := 0
		if xS < sbW-1 && csbf[xS+1][yS] {
			prevCsbf |= 1
		}
		if yS < sbW-1 && csbf[xS][yS+1] {
			prevCsbf |= 2
		}
		start := 15
		sigFound := false
		if i == lastSb {
			start = lastPos - 1
			sigFound = true
		}
		for n := start; n >= 0; n-- {
			if n == 0 && inferDC && !sigFound {
				break // the decoder infers the DC significant
			}
			xP, yP := int(posScan[n][0]), int(posScan[n][1])
			sig := lv[n] != 0
			bc.decision(ctxSignificantCoeffFlag+sigCtxInc(log2Size, ci, scanIdx, xS, yS, xP, yP, prevCsbf), boolInt(sig))
			if sig {
				sigFound = true
			}
		}
		// Levels.
		ctxSet := 0
		if i > 0 && ci == 0 {
			ctxSet = 2
		}
		if !firstSb && greater1Ctx == 0 {
			ctxSet++
		}
		firstSb = false
		greater1Ctx = 1
		var g1 [16]bool
		numG1 := 0
		lastG1Pos := -1
		for n := 15; n >= 0; n-- {
			if lv[n] == 0 {
				continue
			}
			if numG1 < 8 {
				inc := ctxSet*4 + min(3, greater1Ctx)
				if ci > 0 {
					inc += 16
				}
				g := iabs32(lv[n]) > 1
				bc.decision(ctxCoeffAbsLevelGreater1Flag+inc, boolInt(g))
				numG1++
				g1[n] = g
				if g {
					greater1Ctx = 0
					if lastG1Pos == -1 {
						lastG1Pos = n
					}
				} else if greater1Ctx > 0 && greater1Ctx < 3 {
					greater1Ctx++
				}
			}
		}
		g2 := false
		if lastG1Pos != -1 {
			inc := ctxSet
			if ci > 0 {
				inc += 4
			}
			g2 = iabs32(lv[lastG1Pos]) > 2 //nolint:gosec // an index below 16
			bc.decision(ctxCoeffAbsLevelGreater2Flag+inc, boolInt(g2))
		}
		for n := 15; n >= 0; n-- {
			if lv[n] != 0 {
				bc.bypass(boolInt(lv[n] < 0))
			}
		}
		numSig := 0
		rice := 0
		for n := 15; n >= 0; n-- {
			if lv[n] == 0 {
				continue
			}
			abs := int(iabs32(lv[n]))
			base := 1 + boolInt(g1[n]) + boolInt(n == lastG1Pos && g2)
			threshold := 1
			if numSig < 8 {
				threshold = 2
				if n == lastG1Pos {
					threshold = 3
				}
			}
			if base == threshold {
				writeAbsLevelRemaining(bc, abs-base, rice)
				if abs > 3<<rice {
					rice = min(rice+1, 4)
				}
			}
			numSig++
		}
	}
}

// writeAbsLevelRemaining codes coeff_abs_level_remaining (9.3.3.11): a
// unary prefix and a suffix of rice bits, then Exp-Golomb beyond the
// fourth group.
func writeAbsLevelRemaining(bc binCoder, v, rice int) {
	if v < 4<<rice {
		prefix := v >> rice
		for range prefix {
			bc.bypass(1)
		}
		bc.bypass(0)
		bc.bypassBits(uint32(v)&(1<<rice-1), rice) //nolint:gosec // rice bits
		return
	}
	prefix := 4
	for v >= ((1<<(prefix-2))+2)<<rice {
		prefix++
	}
	suffix := v - ((1<<(prefix-3))+2)<<rice
	for range prefix {
		bc.bypass(1)
	}
	bc.bypass(0)
	bc.bypassBits(uint32(suffix), prefix-3+rice) //nolint:gosec // positive
}
