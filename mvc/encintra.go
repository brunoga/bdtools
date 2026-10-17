package mvc

import "math"

// Intra prediction for the macroblock coder: I_16x16, I_4x4 and I_8x8
// candidates, predicted from the coder's own reconstruction of the
// picture so far (before deblocking, as the decoder predicts), each
// block's mode chosen by its rate-distortion cost.

// Intra candidate kinds.
const (
	intraNone = iota
	intra16x16
	intra4x4
	intra8x8
)

// intraAvailMB is the availability of the macroblock's neighbours for
// intra prediction: inside the slice.
func (c *depCoder) intraAvailMB(mbX, mbY int) (nA, nB, nC, nD bool) {
	_, nA = c.neighbour(mbX-1, mbY)
	_, nB = c.neighbour(mbX, mbY-1)
	_, nC = c.neighbour(mbX+1, mbY-1)
	_, nD = c.neighbour(mbX-1, mbY-1)
	return
}

// recOffsets are the macroblock's offsets in the reconstruction planes.
func (c *depCoder) recOffsets(mbX, mbY int) (yo, co int) {
	r := c.rec
	return r.origin[0] + mbY*16*r.stride[0] + mbX*16, r.origin[1] + mbY*8*r.stride[1] + mbX*8
}

// copyRecLuma copies the macroblock's luma from the reconstruction plane
// into a 16x16 buffer.
func (c *depCoder) copyRecLuma(yo int, dst *[256]byte) {
	st := c.rec.stride[0]
	for y := 0; y < 16; y++ {
		copy(dst[y*16:y*16+16], c.rec.planes[0][yo+y*st:yo+y*st+16])
	}
}

// storeRec writes a macroblock's reconstruction into the planes, for the
// intra prediction of the macroblocks after it.
func (c *depCoder) storeRec(mbX, mbY int, luma *[256]byte, chroma *[2][64]byte) {
	yo, co := c.recOffsets(mbX, mbY)
	st, cst := c.rec.stride[0], c.rec.stride[1]
	for y := 0; y < 16; y++ {
		copy(c.rec.planes[0][yo+y*st:yo+y*st+16], luma[y*16:y*16+16])
	}
	for y := 0; y < 8; y++ {
		copy(c.rec.planes[1][co+y*cst:co+y*cst+8], chroma[0][y*8:y*8+8])
		copy(c.rec.planes[2][co+y*cst:co+y*cst+8], chroma[1][y*8:y*8+8])
	}
}

// intraChroma picks the chroma prediction mode by SAD among those the
// neighbours allow, leaving the prediction in c.predCb / c.predCr.
func (c *depCoder) intraChroma(mbX, mbY int, nA, nB, nD bool) int {
	_, co := c.recOffsets(mbX, mbY)
	cst := c.rec.stride[1]
	bestMode, bestCost := 0, math.MaxInt
	var best [2][64]byte
	for mode := 0; mode < 4; mode++ {
		switch mode {
		case 1:
			if !nA {
				continue
			}
		case 2:
			if !nB {
				continue
			}
		case 3:
			if !nA || !nB || !nD {
				continue
			}
		}
		cost := 0
		var p [2][64]byte
		for comp := 0; comp < 2; comp++ {
			pl := c.rec.planes[1+comp]
			predChroma(pl, co, cst, mode, nB, nA)
			src := c.src.Cb
			if comp == 1 {
				src = c.src.Cr
			}
			for y := 0; y < 8; y++ {
				copy(p[comp][y*8:y*8+8], pl[co+y*cst:co+y*cst+8])
			}
			cost += sadGeneric(src, (mbY*8)*c.src.StrideC+mbX*8, c.src.StrideC, p[comp][:], 0, 8, 8, 8)
		}
		if mode != 0 {
			cost += int(c.lambda * 2)
		}
		if cost < bestCost {
			bestCost, bestMode, best = cost, mode, p
		}
	}
	c.predCb, c.predCr = best[0], best[1]
	return bestMode
}

// intra16Cand builds the I_16x16 candidate: the mode with the lowest SATD
// among those the neighbours allow, with its residual.
func (c *depCoder) intra16Cand(mbX, mbY int, chroma int, nA, nB, nD bool) *cand {
	yo, _ := c.recOffsets(mbX, mbY)
	st := c.rec.stride[0]
	bestMode, bestCost := -1, math.MaxInt
	var best [256]byte
	for mode := 0; mode < 4; mode++ {
		switch mode {
		case 0:
			if !nB {
				continue
			}
		case 1:
			if !nA {
				continue
			}
		case 3:
			if !nA || !nB || !nD {
				continue
			}
		}
		pred16x16(c.rec.planes[0], yo, st, mode, nB, nA, nD)
		var p [256]byte
		c.copyRecLuma(yo, &p)
		cost := c.satd(mbX*16, mbY*16, 16, 16, p[:])
		if cost < bestCost {
			bestCost, bestMode, best = cost, mode, p
		}
	}
	k := c.newCand()
	k.intra, k.imode, k.chroma = intra16x16, bestMode, chroma
	c.pred = best
	c.transformQuantI16(mbX, mbY, &k.res)
	c.chromaQuant(mbX, mbY, &k.res)
	k.sad = float64(bestCost) + c.lambda*6
	k.pred, k.predC[0], k.predC[1] = c.pred, c.predCb, c.predCr
	return k
}

// intraNxNCand builds the I_4x4 (n 16) or I_8x8 (n 4) candidate: each
// block's mode chosen by its rate-distortion cost in coding order, the
// blocks after it predicting from its reconstruction.
func (c *depCoder) intraNxNCand(mbX, mbY, n int, chroma int, nA, nB, nC, nD bool) *cand {
	yo, _ := c.recOffsets(mbX, mbY)
	pl, st := c.rec.planes[0], c.rec.stride[0]
	k := c.newCand()
	k.intra, k.chroma = intra4x4, chroma
	k.res = residual{} // the blocks it leaves uncoded stay empty
	if n == 4 {
		k.intra = intra8x8
		k.res.t8 = true
	}
	g, gst := c.imodes, c.mbW*4
	gi0 := mbY*4*gst + mbX*4
	for i := 0; i < n; i++ {
		var bx, by, size int
		if n == 16 {
			bx, by, size = int(blkX[i]), int(blkY[i]), 4
		} else {
			bx, by, size = (i&1)*2, (i>>1)*2, 8
		}
		gi := gi0 + by*gst + bx
		aA, aB := bx > 0 || nA, by > 0 || nB
		pm := int8(2)
		if aA && aB {
			pm = min(g[gi-1], g[gi-gst])
		}
		var av intraAvail
		av.left, av.top = aA, aB
		if n == 16 {
			switch {
			case bx > 0 && by > 0:
				av.topLeft = true
			case by > 0:
				av.topLeft = nA
			case bx > 0:
				av.topLeft = nB
			default:
				av.topLeft = nD
			}
			if by == 0 {
				if bx < 3 {
					av.topRight = nB
				} else {
					av.topRight = nC
				}
			} else if bx < 3 {
				av.topRight = blkOrder[(by-1)*4+bx+1] < blkOrder[by*4+bx]
			}
		} else {
			switch i {
			case 0:
				av.topLeft, av.topRight = nD, nB
			case 1:
				av.topLeft, av.topRight = nB, nC
			case 2:
				av.topLeft, av.topRight = nA, true
			case 3:
				av.topLeft, av.topRight = true, false
			}
		}
		off := yo + by*4*st + bx*4
		px, py := mbX*16+bx*4, mbY*16+by*4
		bestMode, bestCost := -1, math.MaxFloat64
		var bestPred, bestRec [64]byte
		var bestLv [64]int32
		var bestCBP bool
		// Every mode's prediction, ranked by SATD and the mode's bits;
		// the two best go through the transform and the bit estimate.
		var preds [9][64]byte
		var rank [9]int
		var est [9]float64
		nm := 0
		for mode := 0; mode < 9; mode++ {
			switch mode {
			case 0, 3, 7:
				if !aB {
					continue
				}
			case 1, 8:
				if !aA {
					continue
				}
			case 4, 5, 6:
				if !aA || !aB || !av.topLeft {
					continue
				}
			}
			if n == 16 {
				pred4x4(pl, off, st, mode, av)
			} else {
				pred8x8L(pl, off, st, mode, av)
			}
			pred := &preds[mode]
			for y := 0; y < size; y++ {
				copy(pred[y*size:y*size+size], pl[off+y*st:off+y*st+size])
			}
			bits := 1
			if int8(mode) != pm {
				bits = 4
			}
			var satd int
			if size == 4 {
				satd = satd4x4(c.src.Y, py*c.src.StrideY+px, c.src.StrideY, pred[:])
			} else {
				satd = satdBlock(c.src.Y, py*c.src.StrideY+px, c.src.StrideY, pred[:], 0, size, size, size)
			}
			est[mode] = float64(satd) + c.lambda*float64(bits)
			rank[nm] = mode
			nm++
		}
		for i := 1; i < nm; i++ {
			for j := i; j > 0 && est[rank[j]] < est[rank[j-1]]; j-- {
				rank[j], rank[j-1] = rank[j-1], rank[j]
			}
		}
		for _, mode := range rank[:min(nm, 2)] {
			pred := preds[mode]
			var lv [64]int32
			var rec [64]byte
			coded := c.quantBlock(px, py, size, pred[:], lv[:], rec[:])
			ssd := int64(0)
			for y := 0; y < size; y++ {
				so := (py+y)*c.src.StrideY + px
				for x := 0; x < size; x++ {
					d := int64(c.src.Y[so+x]) - int64(rec[y*size+x])
					ssd += d * d
				}
			}
			bits := 1
			if int8(mode) != pm {
				bits = 4
			}
			if coded {
				bits += levelBits(lv[:size*size])
			}
			cost := float64(ssd) + c.lambda2*float64(bits)
			if cost < bestCost {
				bestCost, bestMode, bestPred, bestRec, bestLv, bestCBP = cost, mode, pred, rec, lv, coded
			}
		}
		// the block's reconstruction, for the blocks after it
		for y := 0; y < size; y++ {
			copy(pl[off+y*st:off+y*st+size], bestRec[y*size:y*size+size])
			copy(c.pred[(by*4+y)*16+bx*4:(by*4+y)*16+bx*4+size], bestPred[y*size:y*size+size])
		}
		if n == 16 {
			g[gi] = int8(bestMode)
			k.imodes[i] = int8(bestMode)
			copy(k.res.luma[by*4+bx][:], bestLv[:16])
			if bestCBP {
				k.res.cbp |= 1 << ((by>>1)*2 + bx>>1)
			}
		} else {
			g[gi], g[gi+1], g[gi+gst], g[gi+gst+1] = int8(bestMode), int8(bestMode), int8(bestMode), int8(bestMode)
			k.imodes[i] = int8(bestMode)
			copy(k.res.luma8[i][:], bestLv[:])
			if bestCBP {
				k.res.cbp |= 1 << i
			}
		}
		k.sad += bestCost
	}
	c.chromaQuant(mbX, mbY, &k.res)
	k.pred, k.predC[0], k.predC[1] = c.pred, c.predCb, c.predCr
	return k
}

// quantBlock codes the residual of an intra size x size luma block against
// its prediction: the levels in scan order and the reconstruction. It
// reports whether any level is nonzero.
func (c *depCoder) quantBlock(px, py, size int, pred []byte, lv []int32, rec []byte) bool {
	qp := c.qp
	coded := false
	if size == 4 {
		var d [16]int32
		for y := 0; y < 4; y++ {
			so := (py+y)*c.src.StrideY + px
			for x := 0; x < 4; x++ {
				d[y*4+x] = int32(c.src.Y[so+x]) - int32(pred[y*4+x])
			}
		}
		fdct4(&d)
		qbits := uint(15 + qp/6)
		f := int32(1<<qbits) / 3
		mf, ls := &quantMF[qp%6], &scale4[qp%6]
		var r [16]int32
		for k := 0; k < 16; k++ {
			p := zigzag4x4[k]
			lv[k] = quantLevel(d[p], mf[p], qbits, f)
			coded = coded || lv[k] != 0
		}
		if coded && c.trellising() {
			t := &c.tc
			t.load(c.e.contexts(), catLuma4x4, 16)
			coded = trellisBlock(t, d[:], zigzag4x4[:], mf[:], qbits, trellisW4[qp][:], c.lambda2*trellisIntra, 0, 0, lv[:16])
		}
		if coded {
			for k, l := range lv[:16] {
				if l != 0 {
					p := zigzag4x4[k]
					r[p] = encDequant4(l, ls[p], qp)
				}
			}
			encIDCT4(&r)
		}
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				rec[y*4+x] = encClip(int32(pred[y*4+x]) + (r[y*4+x]+32)>>6)
			}
		}
		return coded
	}
	var d [64]int32
	for y := 0; y < 8; y++ {
		so := (py+y)*c.src.StrideY + px
		for x := 0; x < 8; x++ {
			d[y*8+x] = int32(c.src.Y[so+x]) - int32(pred[y*8+x])
		}
	}
	fdct8(&d)
	qbits := uint(16 + qp/6)
	f := int32(1<<qbits) / 3
	mf, ls := &quant8MF[qp%6], &scale8[qp%6]
	var r [64]int32
	for k := 0; k < 64; k++ {
		p := zigzag8x8[k]
		lv[k] = quantLevel(d[p], mf[p], qbits, f)
		coded = coded || lv[k] != 0
	}
	if coded && c.trellising() {
		t := &c.tc
		t.load(c.e.contexts(), catLuma8x8, 64)
		coded = trellisBlock(t, d[:], zigzag8x8[:], mf[:], qbits, trellisW8[qp][:], c.lambda2*trellisIntra, 0, 0, lv[:64])
	}
	if coded {
		for k, l := range lv[:64] {
			if l != 0 {
				p := zigzag8x8[k]
				r[p] = encDequant8(l, ls[p], qp)
			}
		}
		encIDCT8(&r)
	}
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			rec[y*8+x] = encClip(int32(pred[y*8+x]) + (r[y*8+x]+32)>>6)
		}
	}
	return coded
}

// dequant4 scales a 4x4 transform level (8.5.12.1).
func encDequant4(l, ls int32, qp int) int32 {
	if qp >= 24 {
		return l * ls << (qp/6 - 4)
	}
	return (l*ls + 1<<(3-qp/6)) >> (4 - qp/6)
}

// dequant8 scales an 8x8 transform level (8.5.13.1).
func encDequant8(l, ls int32, qp int) int32 {
	if qp >= 36 {
		return l * ls << (qp/6 - 6)
	}
	return (l*ls + 1<<(5-qp/6)) >> (6 - qp/6)
}

// transformQuantI16 codes the luma residual of an I_16x16 macroblock
// against c.pred: the 4x4 transforms' DC coefficients in their own
// Hadamard transformed block, the rest as AC blocks (coded all or none).
func (c *depCoder) transformQuantI16(mbX, mbY int, res *residual) {
	*res = residual{i16: true}
	x0, y0 := mbX*16, mbY*16
	qp := c.qp
	qbits := uint(15 + qp/6)
	f := int32(1<<qbits) / 3
	mf := &quantMF[qp%6]
	var dc [16]int32
	ac := false
	for r := 0; r < 16; r++ {
		bx, by := r&3, r>>2
		var d [16]int32
		for j := 0; j < 4; j++ {
			so := (y0+by*4+j)*c.src.StrideY + x0 + bx*4
			po := (by*4+j)*16 + bx*4
			for i := 0; i < 4; i++ {
				d[j*4+i] = int32(c.src.Y[so+i]) - int32(c.pred[po+i])
			}
		}
		fdct4(&d)
		dc[r] = d[0]
		for k := 1; k < 16; k++ {
			p := zigzag4x4[k]
			l := quantLevel(d[p], mf[p], qbits, f)
			res.luma[r][k] = l
			if l != 0 {
				ac = true
				res.nonzer++
			}
		}
	}
	// the DC block: Hadamard transformed and quantised a step coarser
	var t [16]int32
	for i := 0; i < 4; i++ {
		a, b, cc, d := dc[i*4], dc[i*4+1], dc[i*4+2], dc[i*4+3]
		t[i*4], t[i*4+1], t[i*4+2], t[i*4+3] = a+b+cc+d, a+b-cc-d, a-b-cc+d, a-b+cc-d
	}
	var h [16]int32
	for j := 0; j < 4; j++ {
		a, b, cc, d := t[j], t[4+j], t[8+j], t[12+j]
		h[j], h[4+j], h[8+j], h[12+j] = a+b+cc+d, a+b-cc-d, a-b-cc+d, a-b+cc-d
	}
	for k := 0; k < 16; k++ {
		l := quantLevel(h[zigzag4x4[k]], mf[0], qbits+1, 2*f)
		res.dcY[k] = l
		if l != 0 {
			res.nonzer++
		}
	}
	if ac {
		res.cbp = 15
	}
}

// chromaQuant codes the chroma residual against c.predCb / c.predCr into
// res (its chroma cbp bits).
func (c *depCoder) chromaQuant(mbX, mbY int, res *residual) {
	qbc := uint(15 + c.qpc/6)
	fc := int32(1<<qbc) / 6
	mfc := &quantMF[c.qpc%6]
	chroma := 0
	so := mbY*8*c.src.StrideC + mbX*8
	for comp := 0; comp < 2; comp++ {
		src, pred := c.src.Cb, &c.predCb
		if comp == 1 {
			src, pred = c.src.Cr, &c.predCr
		}
		var out [4][16]int32
		var dcs [16]int32
		mask, nz := fdctQuantChroma(src, so, c.src.StrideC, pred, mfc, &c.mfTC, qbc, fc, &out, &dcs)
		for b := 0; b < 4; b++ {
			copy(res.ac[comp][b][:], out[b][1:])
		}
		if c.trellising() && mask != 0 {
			mask, nz = c.trellisChroma(src, so, pred, mask, qbc, res.ac[comp][:])
		}
		if mask != 0 {
			chroma = 2
		}
		res.nonzer += nz
		h := [4]int32{
			dcs[0] + dcs[4] + dcs[8] + dcs[12],
			dcs[0] - dcs[4] + dcs[8] - dcs[12],
			dcs[0] + dcs[4] - dcs[8] - dcs[12],
			dcs[0] - dcs[4] - dcs[8] + dcs[12],
		}
		for i := range h {
			l := quantLevel(h[i], mfc[0], qbc+1, 2*fc)
			res.dc[comp][i] = l
			if l != 0 {
				chroma = max(chroma, 1)
				res.nonzer++
			}
		}
	}
	res.cbp = res.cbp&15 | uint8(chroma)<<4
}

// intraKinds selects the intra candidates tried (bits: I_16x16, I_4x4,
// I_8x8); tests narrow it.
var intraKinds = 7

// intraCands builds the intra candidates of a macroblock, with their
// chroma prediction chosen once: I_16x16, I_4x4 and I_8x8.
func (c *depCoder) intraCands(mbX, mbY int) []*cand {
	nA, nB, nC, nD := c.intraAvailMB(mbX, mbY)
	chroma := c.intraChroma(mbX, mbY, nA, nB, nD)
	cb, cr := c.predCb, c.predCr
	var ks []*cand
	if intraKinds&1 != 0 {
		ks = append(ks, c.intra16Cand(mbX, mbY, chroma, nA, nB, nD))
		c.predCb, c.predCr = cb, cr
	}
	if intraKinds&2 != 0 {
		ks = append(ks, c.intraNxNCand(mbX, mbY, 16, chroma, nA, nB, nC, nD))
		c.predCb, c.predCr = cb, cr
	}
	if intraKinds&4 != 0 {
		ks = append(ks, c.intraNxNCand(mbX, mbY, 4, chroma, nA, nB, nC, nD))
	}
	return ks
}

// writeIType writes an intra mb_type (9.3.2.5): in an I slice its bins
// from ctxIdx 3 with the neighbours' increment, in a P or B slice the
// suffix after the prefix bin at prefixCtx.
func (c *depCoder) writeIType(k *cand, mbX, mbY int, prefixCtx int) {
	e := c.e
	var b2, b3, i4, i5, i6 int
	if c.islice {
		inc := 0
		if a, ok := c.neighbour(mbX-1, mbY); ok && a.intra != intra4x4 && a.intra != intra8x8 {
			inc++
		}
		if b, ok := c.neighbour(mbX, mbY-1); ok && b.intra != intra4x4 && b.intra != intra8x8 {
			inc++
		}
		if k.intra != intra16x16 {
			e.decision(3+inc, 0)
			return
		}
		e.decision(3+inc, 1)
		b2, b3, i4, i5, i6 = 3+3, 3+4, 3+5, 3+6, 3+7
	} else {
		if k.intra != intra16x16 {
			e.decision(prefixCtx, 0)
			return
		}
		e.decision(prefixCtx, 1)
		b2, b3, i4, i5, i6 = prefixCtx+1, prefixCtx+2, prefixCtx+2, prefixCtx+3, prefixCtx+3
	}
	e.terminate(0) // not I_PCM
	ac := 0
	if k.res.cbp&15 != 0 {
		ac = 1
	}
	e.decision(b2, ac)
	chroma := int(k.res.cbp >> 4)
	if chroma == 0 {
		e.decision(b3, 0)
	} else {
		e.decision(b3, 1)
		e.decision(i4, chroma-1)
	}
	e.decision(i5, k.imode>>1)
	e.decision(i6, k.imode&1)
}

// writeIntraMB writes an intra macroblock after its mb_type: the
// transform size and prediction modes of I_NxN, the chroma mode, the
// coded block pattern of I_NxN, mb_qp_delta and the residual.
func (c *depCoder) writeIntraMB(mbX, mbY int, k *cand) {
	e := c.e
	cur := &c.cur[mbY*c.mbW+mbX]
	a, okA := c.neighbour(mbX-1, mbY)
	b, okB := c.neighbour(mbX, mbY-1)
	if k.intra != intra16x16 {
		inc := 0
		if okA && a.t8 {
			inc++
		}
		if okB && b.t8 {
			inc++
		}
		t8 := 0
		if k.intra == intra8x8 {
			t8 = 1
		}
		e.decision(399+inc, t8)
		cur.t8 = k.intra == intra8x8
		// the prediction modes, against the ones the neighbours predict
		n, g, gst := 16, c.imodes, c.mbW*4
		if k.intra == intra8x8 {
			n = 4
		}
		gi0 := mbY*4*gst + mbX*4
		for i := 0; i < n; i++ {
			var bx, by int
			if n == 16 {
				bx, by = int(blkX[i]), int(blkY[i])
			} else {
				bx, by = (i&1)*2, (i>>1)*2
			}
			gi := gi0 + by*gst + bx
			pm := int8(2)
			if (bx > 0 || okA) && (by > 0 || okB) {
				pm = min(g[gi-1], g[gi-gst])
			}
			mode := k.imodes[i]
			if mode == pm {
				e.decision(68, 1)
			} else {
				e.decision(68, 0)
				rem := int(mode)
				if mode > pm {
					rem--
				}
				e.decision(69, rem&1)
				e.decision(69, rem>>1&1)
				e.decision(69, rem>>2&1)
			}
			// as the blocks after it predict from it
			if n == 16 {
				g[gi] = mode
			} else {
				g[gi], g[gi+1], g[gi+gst], g[gi+gst+1] = mode, mode, mode, mode
			}
		}
	}
	// intra_chroma_pred_mode
	inc := 0
	if okA && a.chroma != 0 {
		inc++
	}
	if okB && b.chroma != 0 {
		inc++
	}
	switch k.chroma {
	case 0:
		e.decision(64+inc, 0)
	case 1:
		e.decision(64+inc, 1)
		e.decision(67, 0)
	case 2:
		e.decision(64+inc, 1)
		e.decision(67, 1)
		e.decision(67, 0)
	default:
		e.decision(64+inc, 1)
		e.decision(67, 1)
		e.decision(67, 1)
	}
	cur.chroma = uint8(k.chroma)
	c.writeResidual(mbX, mbY, &k.res)
}

// intraEstimate is a quick estimate of what intra prediction would cost
// the macroblock, in the motion search's terms (SATD and lambda times
// bits): the best I_16x16 mode's.
func (c *depCoder) intraEstimate(mbX, mbY int) float64 {
	nA, nB, _, nD := c.intraAvailMB(mbX, mbY)
	yo, _ := c.recOffsets(mbX, mbY)
	st := c.rec.stride[0]
	best := math.MaxInt
	for mode := 0; mode < 4; mode++ {
		switch {
		case mode == 0 && !nB, mode == 1 && !nA, mode == 3 && (!nA || !nB || !nD):
			continue
		}
		pred16x16(c.rec.planes[0], yo, st, mode, nB, nA, nD)
		var p [256]byte
		c.copyRecLuma(yo, &p)
		best = min(best, c.satd(mbX*16, mbY*16, 16, 16, p[:]))
	}
	return float64(best) + c.lambda*6
}

// satd4x4 is satdGeneric for one 4x4 block, unrolled: the intra 4x4 mode
// ranking calls it nine times a block.
func satd4x4(src []byte, so, ss int, pred []byte) int {
	s0, s1, s2, s3 := src[so:so+4], src[so+ss:so+ss+4], src[so+2*ss:so+2*ss+4], src[so+3*ss:so+3*ss+4]
	p := pred[:16]
	// rows: differences, then the horizontal transform
	row := func(s []byte, q []byte) (int32, int32, int32, int32) {
		d0 := int32(s[0]) - int32(q[0])
		d1 := int32(s[1]) - int32(q[1])
		d2 := int32(s[2]) - int32(q[2])
		d3 := int32(s[3]) - int32(q[3])
		a0, a1, a2, a3 := d0+d1, d0-d1, d2+d3, d2-d3
		return a0 + a2, a1 + a3, a0 - a2, a1 - a3
	}
	r00, r01, r02, r03 := row(s0, p[0:4])
	r10, r11, r12, r13 := row(s1, p[4:8])
	r20, r21, r22, r23 := row(s2, p[8:12])
	r30, r31, r32, r33 := row(s3, p[12:16])
	abs := func(v int32) int32 {
		m := v >> 31
		return (v ^ m) - m
	}
	col := func(a, b, c, d int32) int32 {
		a0, a1, a2, a3 := a+b, a-b, c+d, c-d
		return abs(a0+a2) + abs(a1+a3) + abs(a0-a2) + abs(a1-a3)
	}
	sum := col(r00, r10, r20, r30) + col(r01, r11, r21, r31) + col(r02, r12, r22, r32) + col(r03, r13, r23, r33)
	return int(sum) / 2
}
