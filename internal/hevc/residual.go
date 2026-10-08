package hevc

// transformUnit parses a transform unit (7.3.8.10) and reconstructs its
// blocks: intra prediction first where the CU is intra, then the residual.
func (sd *sliceDec) transformUnit(x0, y0, xBase, yBase, log2Size, depth, blkIdx int, cbfLuma bool, cbfC [2]bool) error {
	s, p, c := sd.s, sd.p, &sd.c
	ps := sd.ps
	size := 1 << log2Size
	// The TU's edges, for the deblocking.
	for y := y0; y < y0+size && y < s.height; y += 4 {
		ps.tuEdgeV[sd.blk4(x0, y)] |= 1
	}
	for x := x0; x < x0+size && x < s.width; x += 4 {
		ps.tuEdgeH[sd.blk4(x, y0)] |= 1
	}
	if cbfLuma {
		ps.markCbf(sd, x0, y0, size, true)
	}
	if (cbfLuma || cbfC[0] || cbfC[1]) && p.cuQPDeltaEnabled && !sd.cuQPDeltaCoded {
		abs := 0
		for abs < 5 && c.decision(ctxCuQpDelta+min(abs, 1)) == 1 {
			abs++
		}
		if abs == 5 {
			k := 0
			for k < 32 && c.bypass() == 1 {
				abs += 1 << k
				k++
			}
			if k == 32 {
				return errStream
			}
			abs += c.bypassBits(k)
		}
		if abs > 0 && c.bypass() == 1 {
			abs = -abs
		}
		sd.cuQPDeltaCoded = true
		sd.cuQPDeltaVal = abs
		off := s.qpBdOffset
		sd.qpY = ((sd.qgPred+abs+52+2*off)%(52+off) - off)
	}
	intra := sd.predMode == modeIntra
	if intra {
		mode := int(ps.intraMode[sd.blk4(x0, y0)])
		sd.intraPredict(x0, y0, log2Size, 0, mode)
	}
	if cbfLuma {
		if err := sd.residual(x0, y0, log2Size, 0); err != nil {
			return err
		}
	}
	if log2Size > 2 {
		for ci := 1; ci <= 2; ci++ {
			if intra {
				sd.intraPredict(x0/2, y0/2, log2Size-1, ci, sd.intraModeC)
			}
			if cbfC[ci-1] {
				if err := sd.residual(x0/2, y0/2, log2Size-1, ci); err != nil {
					return err
				}
			}
		}
	} else if blkIdx == 3 {
		for ci := 1; ci <= 2; ci++ {
			if intra {
				sd.intraPredict(xBase/2, yBase/2, 2, ci, sd.intraModeC)
			}
			if cbfC[ci-1] {
				if err := sd.residual(xBase/2, yBase/2, 2, ci); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// scanOrder gives a scan's positions for 2x2 (sub-blocks of 8x8), 4x4 and
// 8x8 (sub-blocks of 32x32) arrays: [log2 size - 1][scanIdx].
var scanOrder = [3][3][][2]uint8{
	{diagScan2x2[:], horizScan2x2, vertScan2x2},
	{diagScan4x4, horizScan4x4, vertScan4x4},
	{diagScan8x8, makeHorizScan(8), makeVertScan(8)},
}

// residual parses a transform block's coefficients (7.3.8.11) and adds its
// residual to the picture's component ci at (x0, y0) (in the
// component's samples).
func (sd *sliceDec) residual(x0, y0, log2Size, ci int) error {
	p, c := sd.p, &sd.c
	size := 1 << log2Size
	transformSkip := false
	if p.transformSkip && !sd.bypass && log2Size <= 2 {
		transformSkip = c.decision(ctxTransformSkipFlag+boolInt(ci > 0)) == 1
	}
	// The last significant coefficient.
	maxPrefix := log2Size<<1 - 1
	var ctxOff, ctxShift int
	if ci == 0 {
		ctxOff, ctxShift = 3*(log2Size-2)+((log2Size-1)>>2), (log2Size+1)>>2
	} else {
		ctxOff, ctxShift = 15, log2Size-2
	}
	lastX, lastY := 0, 0
	for lastX < maxPrefix && c.decision(ctxLastSignificantCoeffXPrefix+ctxOff+lastX>>ctxShift) == 1 {
		lastX++
	}
	for lastY < maxPrefix && c.decision(ctxLastSignificantCoeffYPrefix+ctxOff+lastY>>ctxShift) == 1 {
		lastY++
	}
	if lastX > 3 {
		n := lastX>>1 - 1
		lastX = (1<<n)*(2+lastX&1) + c.bypassBits(n)
	}
	if lastY > 3 {
		n := lastY>>1 - 1
		lastY = (1<<n)*(2+lastY&1) + c.bypassBits(n)
	}
	// The scan (7.4.9.11).
	scanIdx := 0
	if sd.predMode == modeIntra && (log2Size == 2 || log2Size == 3 && ci == 0) {
		mode := sd.intraModeC
		if ci == 0 {
			mode = int(sd.ps.intraMode[sd.blk4(x0, y0)])
		}
		switch {
		case mode >= 6 && mode <= 14:
			scanIdx = 2
		case mode >= 22 && mode <= 30:
			scanIdx = 1
		}
	}
	if scanIdx == 2 {
		lastX, lastY = lastY, lastX
	}
	if lastX >= size || lastY >= size {
		return errStream
	}
	log2Sb := log2Size - 2
	sbScan := scanOrder[0][scanIdx]
	if log2Sb == 0 {
		sbScan = [][2]uint8{{0, 0}}
	} else if log2Sb == 2 {
		sbScan = scanOrder[1][scanIdx]
	} else if log2Sb == 3 {
		sbScan = scanOrder[2][scanIdx]
	}
	posScan := scanOrder[1][scanIdx]
	// Find the last sub-block and position.
	lastSb := (1 << (2 * log2Sb)) - 1
	lastPos := 16
	for {
		if lastPos == 0 {
			lastPos = 16
			lastSb--
			if lastSb < 0 {
				return errStream
			}
		}
		lastPos--
		xS, yS := int(sbScan[lastSb][0]), int(sbScan[lastSb][1])
		xC, yC := xS<<2+int(posScan[lastPos][0]), yS<<2+int(posScan[lastPos][1])
		if xC == lastX && yC == lastY {
			break
		}
	}
	coeffs := sd.coeffs[:size*size]
	clear(coeffs)
	var csbf [8][8]bool
	greater1Ctx := 1
	firstSb := true
	sbW := 1 << log2Sb
	for i := lastSb; i >= 0; i-- {
		xS, yS := int(sbScan[i][0]), int(sbScan[i][1])
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
			csbf[xS][yS] = c.decision(ctxSignificantCoeffGroupFlag+ctx) == 1
			inferDC = true
		} else {
			csbf[xS][yS] = true
		}
		var sig [16]bool
		nSig := 0
		start := 15
		if i == lastSb {
			start = lastPos - 1
			sig[lastPos] = true
			nSig = 1
		}
		prevCsbf := 0
		if xS < sbW-1 && csbf[xS+1][yS] {
			prevCsbf |= 1
		}
		if yS < sbW-1 && csbf[xS][yS+1] {
			prevCsbf |= 2
		}
		if csbf[xS][yS] {
			for n := start; n >= 0; n-- {
				xP, yP := int(posScan[n][0]), int(posScan[n][1])
				if n == 0 && inferDC {
					sig[0] = true
					nSig++
					break
				}
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
				if c.decision(ctxSignificantCoeffFlag+sigCtx) == 1 {
					sig[n] = true
					nSig++
					inferDC = false
				}
			}
		}
		if nSig == 0 {
			continue
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
		var g2 [16]bool
		numG1 := 0
		lastG1Pos := -1
		firstSigPos, lastSigPos := 16, -1
		for n := 15; n >= 0; n-- {
			if !sig[n] {
				continue
			}
			if numG1 < 8 {
				inc := ctxSet*4 + min(3, greater1Ctx)
				if ci > 0 {
					inc += 16
				}
				g1[n] = c.decision(ctxCoeffAbsLevelGreater1Flag+inc) == 1
				numG1++
				if g1[n] {
					greater1Ctx = 0
					if lastG1Pos == -1 {
						lastG1Pos = n
					}
				} else if greater1Ctx > 0 && greater1Ctx < 3 {
					greater1Ctx++
				}
			}
			if lastSigPos == -1 {
				lastSigPos = n
			}
			firstSigPos = n
		}
		signHidden := !sd.bypass && lastSigPos-firstSigPos > 3
		if lastG1Pos != -1 {
			inc := ctxSet
			if ci > 0 {
				inc += 4
			}
			g2[lastG1Pos&15] = c.decision(ctxCoeffAbsLevelGreater2Flag+inc) == 1
		}
		var neg [16]bool
		for n := 15; n >= 0; n-- {
			if sig[n] && (!p.signDataHiding || !signHidden || n != firstSigPos) {
				neg[n] = c.bypass() == 1
			}
		}
		numSig := 0
		sumAbs := 0
		rice := 0
		for n := 15; n >= 0; n-- {
			if !sig[n] {
				continue
			}
			base := 1 + boolInt(g1[n]) + boolInt(g2[n])
			threshold := 1
			if numSig < 8 {
				threshold = 2
				if n == lastG1Pos {
					threshold = 3
				}
			}
			level := base
			if base == threshold {
				rem, err := sd.absLevelRemaining(rice)
				if err != nil {
					return err
				}
				level += rem
				if level > 3<<rice {
					rice = min(rice+1, 4)
				}
			}
			sumAbs += level
			if neg[n] {
				level = -level
			}
			if p.signDataHiding && signHidden && n == firstSigPos && sumAbs&1 == 1 {
				level = -level
			}
			xC, yC := xS<<2+int(posScan[n][0]), yS<<2+int(posScan[n][1])
			coeffs[yC*size+xC] = int32(level)
			numSig++
		}
	}
	sd.reconstruct(x0, y0, log2Size, ci, transformSkip)
	return nil
}

// absLevelRemaining reads coeff_abs_level_remaining (9.3.3.11).
func (sd *sliceDec) absLevelRemaining(rice int) (int, error) {
	c := &sd.c
	prefix := 0
	for prefix < 32 && c.bypass() == 1 {
		prefix++
	}
	if prefix == 32 {
		return 0, errStream
	}
	if prefix <= 3 {
		return prefix<<rice + c.bypassBits(rice), nil
	}
	n := prefix - 3 + rice
	if n > 31 {
		return 0, errStream
	}
	return ((1<<(prefix-3))+3-1)<<rice + c.bypassBits(n), nil
}

// reconstruct scales the block's coefficients, transforms them and adds
// the residual to the prediction in the picture.
func (sd *sliceDec) reconstruct(x0, y0, log2Size, ci int, transformSkip bool) {
	s, p, h := sd.s, sd.p, sd.h
	size := 1 << log2Size
	coeffs := sd.coeffs[:size*size]
	depth := s.bitDepth
	if ci > 0 {
		depth = s.bitDepthC
	}
	if !sd.bypass {
		// Scaling (8.6.2, 8.6.3).
		qp := sd.qpY + s.qpBdOffset
		if ci > 0 {
			off := p.cbQPOffset + h.cbQPOffset
			if ci == 2 {
				off = p.crQPOffset + h.crQPOffset
			}
			qpi := min(max(sd.qpY+off, -s.qpBdOffsetC), 57)
			qp = chromaQP(qpi) + s.qpBdOffsetC
		}
		bdShift := depth + log2Size - 5
		scale := levelScale[qp%6] << (qp / 6)
		var m []uint8
		var dc uint8
		if s.scalingListEnabled && (!transformSkip || log2Size <= 2) {
			sl := &s.scaling
			if p.scalingListPresent {
				sl = &p.scaling
			}
			matrix := ci
			if sd.predMode != modeIntra {
				matrix += 3
			}
			m = sl.lists[log2Size-2][matrix][:]
			if log2Size >= 4 {
				dc = sl.dc[log2Size-4][matrix]
			}
		}
		add := int64(1) << (bdShift - 1)
		for y := range size {
			for x := range size {
				i := y*size + x
				v := coeffs[i]
				if v == 0 {
					continue
				}
				f := int64(16)
				if m != nil {
					switch {
					case log2Size >= 4 && x == 0 && y == 0:
						f = int64(dc)
					case log2Size == 2:
						f = int64(m[y*4+x])
					default:
						sh := log2Size - 3
						f = int64(m[(y>>sh)*8+x>>sh])
					}
				}
				r := (int64(v)*f*int64(scale) + add) >> bdShift
				coeffs[i] = int32(min(max(r, -32768), 32767))
			}
		}
		if transformSkip {
			// 8.6.4.2: r = d << 7, then the second stage's rounding.
			bd := 20 - depth
			for i, v := range coeffs {
				coeffs[i] = (v<<7 + 1<<(bd-1)) >> bd
			}
		} else {
			dst := sd.predMode == modeIntra && ci == 0 && log2Size == 2
			inverseTransform(coeffs, log2Size, depth, dst)
		}
	}
	pl, stride := sd.pic.y, sd.pic.strideY
	if ci == 1 {
		pl, stride = sd.pic.cb, sd.pic.strideC
	} else if ci == 2 {
		pl, stride = sd.pic.cr, sd.pic.strideC
	}
	maxV := int32(1)<<depth - 1
	for y := range size {
		row := pl[(y0+y)*stride+x0:]
		res := coeffs[y*size : y*size+size]
		for x := range size {
			row[x] = uint16(min(max(int32(row[x])+res[x], 0), maxV))
		}
	}
}

// inverseTransform turns scaled coefficients into the residual (8.6.4.2):
// columns, clipped to 16 bits, then rows.
func inverseTransform(c []int32, log2Size, depth int, dst bool) {
	n := 1 << log2Size
	var tmp [32 * 32]int32
	var col, out [32]int32
	// Columns.
	for x := range n {
		last := -1
		for y := range n {
			col[y] = c[y*n+x]
			if col[y] != 0 {
				last = y
			}
		}
		if last < 0 {
			for y := range n {
				tmp[y*n+x] = 0
			}
			continue
		}
		transform1D(col[:n], out[:n], last+1, dst)
		for y := range n {
			tmp[y*n+x] = min(max((out[y]+64)>>7, -32768), 32767)
		}
	}
	// Rows.
	bd := 20 - depth
	add := int32(1) << (bd - 1)
	for y := range n {
		row := tmp[y*n : y*n+n]
		last := -1
		for k := range n {
			if row[k] != 0 {
				last = k
			}
		}
		o := c[y*n : y*n+n]
		if last < 0 {
			for j := range o {
				o[j] = add >> bd
			}
			continue
		}
		transform1D(row, out[:n], last+1, dst)
		for j := range n {
			o[j] = (out[j] + add) >> bd
		}
	}
}

// transform1D is the one-dimensional inverse transform of in (its first
// nz coefficients may be non-zero) into out: a partial butterfly, its
// even and odd halves summed separately (the same sums as the matrix
// product).
func transform1D(in, out []int32, nz int, dst bool) {
	n := len(in)
	if dst {
		for j := range 4 {
			var sum int32
			for k := range nz {
				sum += int32(dstMatrix[k][j]) * in[k]
			}
			out[j] = sum
		}
		return
	}
	if n == 4 {
		e0 := 64*in[0] + 64*in[2]
		e1 := 64*in[0] - 64*in[2]
		o0 := 83*in[1] + 36*in[3]
		o1 := 36*in[1] - 83*in[3]
		out[0], out[1], out[2], out[3] = e0+o0, e1+o1, e1-o1, e0-o0
		return
	}
	half := n / 2
	step := 32 / n
	// Odd part: the odd coefficients' contribution to the first half.
	var odd [16]int32
	for j := range half {
		var sum int32
		for k := 1; k < nz; k += 2 {
			sum += int32(transMatrix[k*step][j]) * in[k]
		}
		odd[j] = sum
	}
	// Even part: the half-size transform of the even coefficients.
	var even, evenIn [16]int32
	for k := range half {
		evenIn[k] = in[2*k]
	}
	transform1D(evenIn[:half], even[:half], (nz+1)/2, false)
	for j := range half {
		out[j] = even[j] + odd[j]
		out[n-1-j] = even[j] - odd[j]
	}
}
