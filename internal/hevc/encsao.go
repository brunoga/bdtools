package hevc

import "math"

// The encoder's SAO (7.3.8.3, 8.7.3): per CTB and component, band offset,
// one of the four edge offset classes, or none, or the left or upper
// CTB's parameters, whichever costs least. The statistics are taken on
// the reconstruction before the deblocking (which runs later, row by
// row), leaving out the CTB's last column and row, whose neighbours are
// not coded yet.

// saoStats is a component's statistics in a CTB: for each edge class and
// category, and each band, the sum of the source minus the
// reconstruction and the count.
type saoStats struct {
	eoSum, eoCnt [4][4]int64 // class, offset index (categories 1, 2, 3, 4)
	boSum, boCnt [32]int64
}

// gather takes the statistics of the block at (x0, y0) of w x h samples
// (component samples) of the plane rec against src, leaving out its last
// column and row; the edge classes take the samples whose neighbours are
// in the picture.
func (st *saoStats) gather(rec []uint16, rstride int, src []uint16, sstride int, x0, y0, w, h, pw, ph, depth int) {
	*st = saoStats{}
	bandShift := depth - 5
	xe, ye := min(x0+w-1, pw), min(y0+h-1, ph)
	for y := y0; y < ye; y++ {
		r := rec[y*rstride+x0 : y*rstride+xe]
		o := src[y*sstride+x0 : y*sstride+xe]
		for x, v := range r {
			b := int(v) >> bandShift
			st.boSum[b] += int64(o[x]) - int64(v)
			st.boCnt[b]++
		}
	}
	// The edge classes: the samples with both neighbours in the picture.
	xs, ys := max(x0, 1), max(y0, 1)
	xe, ye = min(xe, pw-1), min(ye, ph-1)
	if xe <= xs || ye <= ys {
		return
	}
	// Over the widest multiple of 16 columns: the kernel's width, the same
	// statistics on every platform.
	ew := (xe - xs) &^ 15
	if ew == 0 {
		return
	}
	offs := [4][2]int{
		{-1, 1}, {-rstride, rstride}, {-rstride - 1, rstride + 1}, {-rstride + 1, rstride - 1},
	}
	for c := range 4 {
		saoEO(rec, ys*rstride+xs, rstride, src, ys*sstride+xs, sstride, ew, ye-ys, offs[c][0], offs[c][1], &st.eoSum[c], &st.eoCnt[c])
	}
}

// saoEOGo is saoEO's statistics, a sample at a time.
func saoEOGo(rec []uint16, r0, rs int, src []uint16, s0, ss, w, h, off0, off1 int, sum, cnt *[4]int64) {
	// s = sign(v-a) + sign(v-b), -2..2 -> offset index 0, 1, -, 2, 3.
	idx := [5]int{0, 1, -1, 2, 3}
	for y := range h {
		for x := range w {
			o := r0 + y*rs + x
			v := int(rec[o])
			k := idx[2+sign(v-int(rec[o+off0]))+sign(v-int(rec[o+off1]))]
			if k < 0 {
				continue
			}
			sum[k] += int64(src[s0+y*ss+x]) - int64(v)
			cnt[k]++
		}
	}
}

// saoGain is the squared error change of offset o on n samples whose
// error sums to s.
func saoGain(o, n, s int64) int64 { return n*o*o - 2*o*s }

// bestOffset is the offset minimising the error of n samples summing s,
// within [lo, hi].
func bestOffset(n, s int64, lo, hi int) int {
	if n == 0 {
		return 0
	}
	o := int(math.Round(float64(s) / float64(n)))
	return min(max(o, lo), hi)
}

// unaryBits is the bits of a truncated unary code of v up to cMax.
func unaryBits(v, cMax int) float64 {
	if v >= cMax {
		return float64(cMax)
	}
	return float64(v + 1)
}

// saoChoice is a component's SAO and its cost.
type saoChoice struct {
	p    saoParams
	dist int64   // squared error change
	bits float64 // the offsets' bits, the type's apart
}

// choose finds the component's best band and edge parameters; edge
// offsets are positive for the first two categories and negative for the
// last two, as the decoder takes them.
func (st *saoStats) choose(depth int) (band saoChoice, edge [4]saoChoice) {
	cMax := 1<<(min(depth, 10)-5) - 1
	shift := depth - min(depth, 10)
	for c := range 4 {
		ch := saoChoice{p: saoParams{typ: 2, class: uint8(c)}} //nolint:gosec // 0..3
		for i := range 4 {
			n, s := st.eoCnt[c][i], st.eoSum[c][i]
			var o int
			if i < 2 {
				o = bestOffset(n, s>>shift, 0, cMax)
			} else {
				o = bestOffset(n, s>>shift, -cMax, 0)
			}
			ch.p.offset[i] = int16(o << shift) //nolint:gosec // small
			ch.dist += saoGain(int64(o<<shift), n, s)
			ch.bits += unaryBits(iabs(o), cMax)
		}
		ch.bits += 2 // the class
		edge[c] = ch
	}
	// Band: the four consecutive bands gaining most.
	var gain [32]int64
	var offs [32]int
	for b := range 32 {
		offs[b] = bestOffset(st.boCnt[b], st.boSum[b]>>shift, -cMax, cMax)
		gain[b] = saoGain(int64(offs[b]<<shift), st.boCnt[b], st.boSum[b])
	}
	band.dist = math.MaxInt64
	for start := range 29 {
		var d int64
		for k := range 4 {
			d += gain[start+k]
		}
		if d < band.dist {
			band.dist = d
			band.p = saoParams{typ: 1, band: uint8(start)} //nolint:gosec // 0..28
			for k := range 4 {
				band.p.offset[k] = int16(offs[start+k] << shift) //nolint:gosec // small
			}
		}
	}
	band.bits = 5
	for k := range 4 {
		o := int(band.p.offset[k]) >> shift
		band.bits += unaryBits(iabs(o), cMax)
		if o != 0 {
			band.bits++ // the sign
		}
	}
	return band, edge
}

// distOf is the squared error change of params p on the statistics.
func (st *saoStats) distOf(p *saoParams, depth int) int64 {
	switch p.typ {
	case 1:
		var d int64
		for k := range 4 {
			b := (int(p.band) + k) & 31
			d += saoGain(int64(p.offset[k]), st.boCnt[b], st.boSum[b])
		}
		return d
	case 2:
		var d int64
		for i := range 4 {
			d += saoGain(int64(p.offset[i]), st.eoCnt[p.class][i], st.eoSum[p.class][i])
		}
		return d
	}
	return 0
}

// decideSAO chooses the CTB's SAO from its reconstruction (before the
// deblocking) and records it in the block state the frame filter reads.
func (c *picCoder) decideSAO(ctbX, ctbY int) {
	s, ps, pic, src := c.s, c.ps, c.pic, c.src
	addr := ctbY*s.ctbW + ctbX
	var st [3]saoStats
	size := s.ctbSize
	st[0].gather(pic.y, pic.strideY, src.Y, src.StrideY, ctbX*size, ctbY*size, size, size, s.width, s.height, s.bitDepth)
	cs := size / 2
	st[1].gather(pic.cb, pic.strideC, src.Cb, src.StrideC, ctbX*cs, ctbY*cs, cs, cs, s.width/2, s.height/2, s.bitDepthC)
	st[2].gather(pic.cr, pic.strideC, src.Cr, src.StrideC, ctbX*cs, ctbY*cs, cs, cs, s.width/2, s.height/2, s.bitDepthC)
	lam := c.lambda
	// Luma alone.
	bandY, edgeY := st[0].choose(s.bitDepth)
	bestY := saoChoice{}
	costY := lam * 1 // sao_type_idx_luma 0
	for _, ch := range append([]saoChoice{bandY}, edgeY[:]...) {
		if cost := float64(ch.dist) + lam*(ch.bits+2); cost < costY {
			bestY, costY = ch, cost
		}
	}
	// Chroma: one type (and class) for both.
	bandCb, edgeCb := st[1].choose(s.bitDepthC)
	bandCr, edgeCr := st[2].choose(s.bitDepthC)
	best := [2]saoChoice{}
	costC := lam * 1
	if cost := float64(bandCb.dist+bandCr.dist) + lam*(bandCb.bits+bandCr.bits+2); cost < costC {
		best, costC = [2]saoChoice{bandCb, bandCr}, cost
	}
	for cl := range 4 {
		cost := float64(edgeCb[cl].dist+edgeCr[cl].dist) + lam*(edgeCb[cl].bits+edgeCr[cl].bits-2+2)
		if cost < costC {
			best, costC = [2]saoChoice{edgeCb[cl], edgeCr[cl]}, cost
			best[1].p.class = best[0].p.class
		}
	}
	params := [3]saoParams{bestY.p, best[0].p, best[1].p}
	cost := costY + costC + lam*2 // the merge flags
	// The left and upper CTBs' parameters, merged.
	try := func(n [3]saoParams) {
		d := float64(0)
		for ci := range 3 {
			depth := s.bitDepth
			if ci > 0 {
				depth = s.bitDepthC
			}
			d += float64(st[ci].distOf(&n[ci], depth))
		}
		if mc := d + lam*1; mc < cost {
			params, cost = n, mc
		}
	}
	if ctbX > 0 {
		try(ps.sao[addr-1])
	}
	if ctbY > 0 {
		try(ps.sao[addr-s.ctbW])
	}
	ps.sao[addr] = params
}

// writeSAO writes the CTB's SAO syntax (7.3.8.3): merged with the left or
// upper CTB when its parameters are theirs.
func (c *picCoder) writeSAO(bc binCoder, ctbX, ctbY int) {
	s, ps := c.s, c.ps
	addr := ctbY*s.ctbW + ctbX
	p := &ps.sao[addr]
	if ctbX > 0 {
		left := *p == ps.sao[addr-1]
		bc.decision(ctxSaoMergeFlag, boolInt(left))
		if left {
			return
		}
	}
	if ctbY > 0 {
		up := *p == ps.sao[addr-s.ctbW]
		bc.decision(ctxSaoMergeFlag, boolInt(up))
		if up {
			return
		}
	}
	for ci := range 3 {
		sp := &p[ci]
		depth := s.bitDepth
		if ci > 0 {
			depth = s.bitDepthC
		}
		cMax := 1<<(min(depth, 10)-5) - 1
		shift := depth - min(depth, 10)
		if ci < 2 {
			bc.decision(ctxSaoTypeIdx, boolInt(sp.typ != 0))
			if sp.typ != 0 {
				bc.bypass(boolInt(sp.typ == 2))
			}
		}
		if sp.typ == 0 {
			continue
		}
		var abs [4]int
		for i := range 4 {
			abs[i] = iabs(int(sp.offset[i]) >> shift)
			for k := range min(abs[i], cMax) {
				_ = k
				bc.bypass(1)
			}
			if abs[i] < cMax {
				bc.bypass(0)
			}
		}
		if sp.typ == 1 {
			for i := range 4 {
				if abs[i] != 0 {
					bc.bypass(boolInt(sp.offset[i] < 0))
				}
			}
			bc.bypassBits(uint32(sp.band), 5)
			continue
		}
		if ci < 2 {
			bc.bypassBits(uint32(sp.class), 2)
		}
	}
}
