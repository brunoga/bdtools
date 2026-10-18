package av1

import (
	"math"
	"sync/atomic"
)

// Masked compound (comp_group_idx 1): a compound block's two predictions
// blended by a mask, either a wedge (a soft edge across the block, one of
// sixteen with a sign: the wedge mask process) or the predictions'
// difference (the difference weight mask process). The mask is the luma
// block's, subsampled for chroma (the mask blend process).

// dbgNoMasked turns masked compound (enable_masked_compound) off;
// dbgForceMasked codes compound blocks masked where they can be (tests).
var dbgNoMasked, dbgForceMasked = false, false

// maskedBlocks counts the blocks coded masked, by difference and by
// wedge (for tests).
var maskedBlocks [2]atomic.Int64

// maskedCands is how many of the best compound candidates are also
// estimated masked.
var maskedCands = 2

// compMask is a masked compound block's mask.
type compMask struct {
	wedge     bool // COMPOUND_WEDGE, else COMPOUND_DIFFWTD
	idx, sign int  // wedge_index, wedge_sign
	maskType  int  // mask_type
	// m is the luma mask, w wide, as the luma prediction set it
	m []uint8
	w int
}

// wedgeMasks is WedgeMasks[bsize][flipSign][wedge] (Block_Width wide),
// initialise_wedge_mask_table's.
var wedgeMasks = func() (wm [BLOCK_SIZES][2][WEDGE_TYPES][]uint8) {
	const n = MASK_MASTER_SIZE
	var master [6][n][n]uint8
	for j := range n {
		shift := n / 4
		for i := 0; i < n; i += 2 {
			master[WEDGE_OBLIQUE63][i][j] = wedgeMasterObliqueEven[min(max(j-shift, 0), n-1)]
			shift--
			master[WEDGE_OBLIQUE63][i+1][j] = wedgeMasterObliqueOdd[min(max(j-shift, 0), n-1)]
			master[WEDGE_VERTICAL][i][j] = wedgeMasterVertical[j]
			master[WEDGE_VERTICAL][i+1][j] = wedgeMasterVertical[j]
		}
	}
	for i := range n {
		for j := range n {
			msk := master[WEDGE_OBLIQUE63][i][j]
			master[WEDGE_OBLIQUE27][j][i] = msk
			master[WEDGE_OBLIQUE117][i][n-1-j] = 64 - msk
			master[WEDGE_OBLIQUE153][n-1-j][i] = 64 - msk
			master[WEDGE_HORIZONTAL][j][i] = master[WEDGE_VERTICAL][i][j]
		}
	}
	for bs := BLOCK_8X8; bs < BLOCK_SIZES; bs++ {
		if wedgeBits[bs] == 0 {
			continue
		}
		w, h := int(blockWidth[bs]), int(blockHeight[bs])
		w4, h4 := num4x4BlocksWide[bs], num4x4BlocksHigh[bs]
		shape := 2
		if h4 > w4 {
			shape = 0
		} else if h4 < w4 {
			shape = 1
		}
		for wedge := range WEDGE_TYPES {
			cb := wedgeCodebook[shape][wedge]
			dir := int(cb[0])
			xoff := n/2 - ((int(cb[1]) * w) >> 3)
			yoff := n/2 - ((int(cb[2]) * h) >> 3)
			sum := 0
			for i := range w {
				sum += int(master[dir][yoff][xoff+i])
			}
			for i := 1; i < h; i++ {
				sum += int(master[dir][yoff+i][xoff])
			}
			avg := (sum + (w+h-1)/2) / (w + h - 1)
			flip := b2i(avg < 32)
			a, b := make([]uint8, w*h), make([]uint8, w*h)
			for i := range h {
				for j := range w {
					v := master[dir][yoff+i][xoff+j]
					a[i*w+j], b[i*w+j] = v, 64-v
				}
			}
			wm[bs][flip][wedge], wm[bs][1-flip][wedge] = a, b
		}
	}
	return wm
}()

// setMask sets mk's luma mask from the luma predictions p0 and p1 (w by
// h) of a block of size bs, at the compound rounding rnd.
func (mk *compMask) setMask(bs int, p0, p1 []int32, w, h, bitDepth int, rnd interRounding, buf *[]uint8) {
	mk.w = w
	if mk.wedge {
		mk.m = wedgeMasks[bs][mk.sign][mk.idx]
		return
	}
	if cap(*buf) < w*h {
		*buf = make([]uint8, 128*128)
	}
	m := (*buf)[:w*h]
	sh := (bitDepth - 8) + rnd.post
	for i := range m {
		d := p0[i] - p1[i]
		if d < 0 {
			d = -d
		}
		if sh > 0 {
			d = (d + 1<<(sh-1)) >> sh
		}
		v := min(38+d/16, 64)
		if mk.maskType != 0 {
			v = 64 - v
		}
		m[i] = uint8(v) //nolint:gosec // 0..64
	}
	mk.m = m
}

// blend is the mask blend process for a plane's w by h predictions p0
// and p1 (the mask the luma's, subsampled for chroma).
func (mk *compMask) blend(plane int, p0, p1 []int32, w, h, bitDepth int, rnd interRounding, dst []uint16, dstStride int) {
	sh := uint(6 + rnd.post)
	maxV := int32(1)<<uint(bitDepth) - 1
	m, mw := mk.m, mk.w
	for i := range h {
		row := dst[i*dstStride : i*dstStride+w]
		for j := range row {
			var k int32
			if plane == 0 {
				k = int32(m[i*mw+j])
			} else {
				a, b := m[2*i*mw+2*j:], m[(2*i+1)*mw+2*j:]
				k = (int32(a[0]) + int32(a[1]) + int32(b[0]) + int32(b[1]) + 2) >> 2
			}
			v := (k*p0[i*w+j] + (64-k)*p1[i*w+j] + 1<<(sh-1)) >> sh
			row[j] = uint16(clip(v, maxV)) //nolint:gosec // clipped
		}
	}
}

// writeCompoundType writes a compound block's read_compound_type syntax:
// comp_group_idx, then compound_idx, or compound_type and the mask's.
func (t *tileEnc) writeCompoundType(cd coder) {
	f, b := t.f, &t.b
	if b.skipMode {
		return
	}
	if f.seq.maskedCompound {
		cd.symbol(b2i(b.masked), t.cdf.compGroupIdx[t.compGroupCtx()][:])
	}
	if !b.masked {
		t.writeCompoundIdx(cd)
		return
	}
	if wedgeBits[b.size] > 0 {
		cd.symbol(b2i(!b.mask.wedge), t.cdf.compoundType[b.size][:]) // COMPOUND_WEDGE 0, COMPOUND_DIFFWTD 1
	}
	if b.mask.wedge {
		cd.symbol(b.mask.idx, t.cdf.wedgeIndex[b.size][:])
		cd.literal(uint32(b.mask.sign), 1) //nolint:gosec // a bit
	} else {
		cd.literal(uint32(b.mask.maskType), 1) //nolint:gosec // a bit
	}
}

// compGroupCtx is comp_group_idx's context.
func (t *tileEnc) compGroupCtx() int {
	f, b := t.f, &t.b
	ctx := 0
	for _, n := range [2]struct {
		avail bool
		r, c  int
	}{{b.availU, b.r - 1, b.c}, {b.availL, b.r, b.c - 1}} {
		if !n.avail {
			continue
		}
		i := f.mi(n.r, n.c)
		if f.refFrames[1][i] > INTRA_FRAME {
			ctx += int(f.compGroup[i])
		} else if f.refFrames[0][i] == ALTREF_FRAME {
			ctx += 3
		}
	}
	return min(ctx, 5)
}

// maskedVariants estimates a compound candidate masked: every wedge
// (where the size has them) and both difference masks, each by its luma
// SATD and rate. It gives the best wedge and the best difference variant.
func (t *tileEnc) maskedVariants(cd interCand) []interCand {
	f, b := t.f, &t.b
	w, h := int(blockWidth[b.size]), int(blockHeight[b.size])
	b.refFrame, b.refFrame2, b.yMode, b.refMvIdx, b.mv, b.mv2 = cd.ref, cd.ref2, cd.mode, cd.idx, cd.m, cd.m2
	b.interp, b.obmc, b.warp, b.compDist, b.masked = cd.interp, false, false, false, true
	t.setModes()
	rnd := interRoundingFor(f.bitDepth, true)
	if cap(t.maskP[0]) < w*h {
		t.maskP[0], t.maskP[1] = make([]int32, 128*128), make([]int32, 128*128)
	}
	p0, p1 := t.maskP[0][:w*h], t.maskP[1][:w*h]
	filt := [2]int{cd.interp, cd.interp}
	for k, p := range [2][]int32{p0, p1} {
		r := f.refs[[2]int{cd.ref, cd.ref2}[k]]
		m := [2]mv{cd.m, cd.m2}[k]
		sx, sy, _, _ := scaleMv(0, b.c*4, b.r*4, m, r.width, r.height, r.width, r.height)
		t.is.blockInterPred(r, 0, sx, sy, w, h, filt, rnd, f.bitDepth, nil, p, w)
	}
	sl := math.Sqrt(t.lambda)
	pred := t.interPred[0][:]
	try := func(mk compMask) float64 {
		b.mask = mk
		b.mask.setMask(b.size, p0, p1, w, h, f.bitDepth, rnd, &b.maskBuf)
		b.mask.blend(0, p0, p1, w, h, f.bitDepth, rnd, pred, w)
		satd := t.satd(0, b.c*4, b.r*4, w, h, pred)
		t.cost.n = 0
		t.writeInterModes(&t.cost)
		return float64(satd) + sl*float64(t.cost.n)/costScale
	}
	var out []interCand
	if wedgeBits[b.size] > 0 {
		best, bestE := compMask{}, math.Inf(1)
		for idx := range WEDGE_TYPES {
			for sign := range 2 {
				mk := compMask{wedge: true, idx: idx, sign: sign}
				if e := try(mk); e < bestE {
					best, bestE = mk, e
				}
			}
		}
		c := cd
		c.masked, c.mask, c.est = true, best, bestE
		out = append(out, c)
	}
	best, bestE := compMask{}, math.Inf(1)
	for mt := range 2 {
		mk := compMask{maskType: mt}
		if e := try(mk); e < bestE {
			best, bestE = mk, e
		}
	}
	c := cd
	c.masked, c.mask, c.est = true, best, bestE
	out = append(out, c)
	b.masked, b.mask = false, compMask{}
	return out
}
