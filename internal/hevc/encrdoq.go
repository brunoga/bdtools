package hevc

import "math"

// Rate-distortion optimised quantisation, a simplified form of the
// reference software's: each coefficient's level is the rounded one, one
// less, or zero, whichever costs least in distortion plus bits (the bits
// from the coder's context states as the CTU began), and the block then
// ends at the last position that pays for itself.

// binBits is the cost in bits of coding bin with context state s.
func binBits(s uint8, bin int) float64 {
	if bin != int(s&1) {
		return cabacEntropy[s|1]
	}
	return cabacEntropy[s&^1]
}

// remBits is the bits of coeff_abs_level_remaining v at Rice parameter 0.
func remBits(v int) float64 {
	if v < 4 {
		return float64(v + 1)
	}
	prefix := 4
	for v >= ((1 << (prefix - 2)) + 2) {
		prefix++
	}
	return float64(prefix + 1 + prefix - 3)
}

// lastBits is the bits of coding a last position coordinate p of a block
// of 1 << log2 (its prefix, truncated unary, and its suffix).
func lastBits(p, log2 int) float64 {
	prefix, _, n := lastPosCode(p)
	b := float64(min(prefix+1, log2<<1-1))
	if prefix > 3 {
		b += float64(n)
	}
	return b
}

// lastBitsTab is lastBits by block size (4 to 32) and coordinate.
var lastBitsTab = func() (t [4][32]float64) {
	for log2 := 2; log2 <= 5; log2++ {
		for p := range 1 << log2 {
			t[log2-2][p] = lastBits(p, log2)
		}
	}
	return t
}()

// scanRaster holds, by block size (4 to 32) and scan, the raster index of
// each position in coding order: sub-block by sub-block.
var scanRaster = func() (t [4][3][]int32) {
	for log2 := 2; log2 <= 5; log2++ {
		n := 1 << log2
		for scanIdx := range 3 {
			sbScan := subBlockScan(log2, scanIdx)
			posScan := scanOrder[1][scanIdx]
			r := make([]int32, 0, n*n)
			for sb := range sbScan {
				for k := range 16 {
					x := int(sbScan[sb][0])<<2 + int(posScan[k][0])
					y := int(sbScan[sb][1])<<2 + int(posScan[k][1])
					r = append(r, int32(y*n+x)) //nolint:gosec // within the block
				}
			}
			t[log2-2][scanIdx] = r
		}
	}
	return t
}()

// rdoqScratch is rdoq's working memory.
type rdoqScratch struct {
	pos, maxL, level, lastLevel [32 * 32]int32
	absC                        [32 * 32]int64
	cost, costLast, dist0       [32 * 32]float64
	after                       [32*32 + 1]float64
}

// rdoq quantises coef (n x n, row major, its scan scanIdx, component ci)
// in place, returning the levels not zero.
func (c *picCoder) rdoq(q *quantizer, coef []int32, log2, ci, scanIdx int) int {
	n := 1 << log2
	ctx := &c.enc.ctx
	depth := c.s.bitDepth
	if ci > 0 {
		depth = c.s.bitDepthC
	}
	ts := 15 - depth - log2
	// The squared error of a level, in samples: the coefficient's error in
	// level steps, scaled back through the quantiser and the transform.
	errScale := 1 / (float64(q.scale) * float64(q.scale) * math.Ldexp(1, 2*ts))
	lambda := c.lambda
	one := int64(1) << q.qBits
	half := one >> 1
	sbScan := subBlockScan(log2, scanIdx)
	posScan := scanOrder[1][scanIdx]
	sbW := 1 << (log2 - 2)
	sc := &c.rq
	nn := n * n
	// All below half a step: nothing to decide.
	limit := half/q.scale + 1
	anyLevel := false
	for _, v := range coef[:nn] {
		if a := int64(v); a >= limit || -a >= limit {
			anyLevel = true
			break
		}
	}
	if !anyLevel {
		clear(coef[:nn])
		return 0
	}
	pos, absC, maxL := sc.pos[:nn], sc.absC[:nn], sc.maxL[:nn]
	copy(pos, scanRaster[log2-2][scanIdx])
	last := -1
	var csbf [8][8]bool
	for i, p := range pos {
		a := int64(iabs32(coef[p])) * q.scale
		absC[i] = a
		maxL[i] = 0
		if l := int32((a + half) >> q.qBits); l > 0 { //nolint:gosec // a level
			maxL[i] = min(l, 32767)
			last = i
			sb := i >> 4
			csbf[sbScan[sb][0]][sbScan[sb][1]] = true
		}
	}
	if last < 0 {
		clear(coef[:n*n])
		return 0
	}
	dist := func(i int, l int32) float64 {
		e := float64(absC[i] - int64(l)*one)
		return e * e * errScale
	}
	// Each position's best level, and the costs the last position search
	// needs.
	level, lastLevel := sc.level[:last+1], sc.lastLevel[:last+1]
	cost, costLast, dist0 := sc.cost[:last+1], sc.costLast[:last+1], sc.dist0[:last+1]
	for i := 0; i <= last; i++ {
		sb, k := i>>4, i&15
		xS, yS := int(sbScan[sb][0]), int(sbScan[sb][1])
		prevCsbf := 0
		if xS < sbW-1 && csbf[xS+1][yS] {
			prevCsbf |= 1
		}
		if yS < sbW-1 && csbf[xS][yS+1] {
			prevCsbf |= 2
		}
		sigCtx := ctxSignificantCoeffFlag + sigCtxInc(log2, ci, scanIdx, xS, yS, int(posScan[k][0]), int(posScan[k][1]), prevCsbf)
		ctxSet := 0
		if sb > 0 && ci == 0 {
			ctxSet = 2
		}
		g1 := ctxCoeffAbsLevelGreater1Flag + ctxSet*4 + 1
		g2 := ctxCoeffAbsLevelGreater2Flag + ctxSet
		if ci > 0 {
			g1 += 16
			g2 += 4
		}
		levelBits := func(l int32) float64 {
			b := 1.0 // the sign
			switch l {
			case 1:
				b += binBits(ctx[g1], 0)
			case 2:
				b += binBits(ctx[g1], 1) + binBits(ctx[g2], 0)
			default:
				b += binBits(ctx[g1], 1) + binBits(ctx[g2], 1) + remBits(int(l)-3)
			}
			return b
		}
		d0 := dist(i, 0)
		dist0[i] = d0
		sig0, sig1 := binBits(ctx[sigCtx], 0), binBits(ctx[sigCtx], 1)
		best, bestCost, bestLast, bestLastL := int32(0), d0+lambda*sig0, math.Inf(1), int32(0)
		for _, l := range [2]int32{maxL[i], maxL[i] - 1} {
			if l <= 0 {
				continue
			}
			d := dist(i, l) + lambda*levelBits(l)
			if d < bestLast {
				bestLast, bestLastL = d, l
			}
			if cc := d + lambda*sig1; cc < bestCost {
				best, bestCost = l, cc
			}
		}
		level[i], cost[i], costLast[i], lastLevel[i] = best, bestCost, bestLast, bestLastL
	}
	// The last position: coding up to i, its level not zero (its
	// significance not coded), the rest zero and uncoded.
	bestEnd, bestTotal := -1, math.Inf(1)
	before := 0.0
	lb := &lastBitsTab[log2-2]
	after := sc.after[:last+2]
	after[last+1] = 0
	for i := last; i >= 0; i-- {
		after[i] = after[i+1] + dist0[i]
	}
	for i := 0; i <= last; i++ {
		if !math.IsInf(costLast[i], 1) {
			x := int(pos[i]) % n
			y := int(pos[i]) / n
			if scanIdx == 2 {
				x, y = y, x
			}
			t := before + costLast[i] + after[i+1] + lambda*(lb[x&31]+lb[y&31])
			if t < bestTotal {
				bestEnd, bestTotal = i, t
			}
		}
		before += cost[i]
	}
	clear(coef[:n*n])
	if bestEnd < 0 || bestTotal >= after[0] {
		return 0 // nothing pays for its bits
	}
	nz := 0
	for i := 0; i <= bestEnd; i++ {
		l := level[i]
		if i == bestEnd {
			l = lastLevel[i] // the last coefficient is never zero
		}
		if l == 0 {
			continue
		}
		p := pos[i]
		if c := c.orig[p]; c < 0 {
			coef[p] = -l
		} else {
			coef[p] = l
		}
		nz++
	}
	return nz
}
