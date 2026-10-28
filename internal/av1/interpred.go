package av1

// Inter prediction (spec 7.11.3), exactly as a decoder predicts, for what
// the encoder codes: translation only (no warps), references the
// size of the frame, the 8-tap subpel filters (their 4-tap variants for
// blocks 4 wide or high) in each direction, reference samples fetched
// clamped to the reference picture's real extent. The rounding variables
// are kept for compound prediction too, and the block process can give
// the unclipped preds[] compound blending needs, though only single
// reference prediction is wired.

// interRounding is the rounding variables derivation process (7.11.3.2):
// InterRound0, InterRound1 and InterPostRound.
type interRounding struct{ round0, round1, post int }

// interRoundingFor gives the rounding variables of a bit depth for single
// or compound prediction.
func interRoundingFor(bitDepth int, isCompound bool) interRounding {
	r := interRounding{round0: 3, round1: 11}
	if isCompound {
		r.round1 = 7
	}
	if bitDepth == 12 {
		r.round0 += 2
		if !isCompound {
			r.round1 -= 2
		}
	}
	r.post = 2*FILTER_BITS - (r.round0 + r.round1)
	return r
}

// scaleMv is the motion vector scaling process (7.11.3.3): where a block
// at (x, y) of a plane is in the reference (refW by refH luma samples) for
// a frame frameW by frameH, in 1/1024 samples, and the steps.
func scaleMv(plane, x, y int, m mv, refW, refH, frameW, frameH int) (startX, startY, stepX, stepY int) {
	xScale := ((int64(refW) << REF_SCALE_SHIFT) + int64(frameW/2)) / int64(frameW)
	yScale := ((int64(refH) << REF_SCALE_SHIFT) + int64(frameH/2)) / int64(frameH)
	sub := uint(min(plane, 1))
	const halfSample = 1 << (SUBPEL_BITS - 1)
	origX := int64(x)<<SUBPEL_BITS + int64((2*m.col)>>sub) + halfSample
	origY := int64(y)<<SUBPEL_BITS + int64((2*m.row)>>sub) + halfSample
	baseX := origX*xScale - halfSample<<REF_SCALE_SHIFT
	baseY := origY*yScale - halfSample<<REF_SCALE_SHIFT
	const off = (1 << (SCALE_SUBPEL_BITS - SUBPEL_BITS)) / 2
	const sh = REF_SCALE_SHIFT + SUBPEL_BITS - SCALE_SUBPEL_BITS
	startX = int(round2Signed64(baseX, sh) + off)
	startY = int(round2Signed64(baseY, sh) + off)
	stepX = int(round2Signed64(xScale, REF_SCALE_SHIFT-SCALE_SUBPEL_BITS))
	stepY = int(round2Signed64(yScale, REF_SCALE_SHIFT-SCALE_SUBPEL_BITS))
	return
}

// interScratch is the block inter prediction's working memory: the
// clamped reference samples of a block reaching outside the reference
// and the intermediate array.
type interScratch struct {
	edge [(128 + 7) * (128 + 7)]uint16
	mid  [(128 + 7) * 128]int32
	// mid16 is filterBlock's
	mid16 [(128 + 7) * 128]int16
	// comp holds a compound block's two predictions (preds[])
	comp [2][]int32
	// obmc is an overlapped prediction's obmcPred
	obmc [64 * 64]uint16
}

// subpelTaps is, by filter and position, the range of a Subpel_Filters
// kernel's nonzero taps (the others add nothing).
var subpelTaps = func() (t [6][16][2]uint8) {
	for f := range subpelFilters {
		for p := range subpelFilters[f] {
			k := &subpelFilters[f][p]
			lo, hi := 0, 8
			for k[lo] == 0 {
				lo++
			}
			for k[hi-1] == 0 {
				hi--
			}
			t[f][p] = [2]uint8{uint8(lo), uint8(hi)} //nolint:gosec // 0..8
		}
	}
	return
}()

// filterFor is the filter a block of size n (its width for the horizontal
// filter, its height for the vertical) uses: the 4-tap variants at 4 and
// below.
func filterFor(f, n int) int {
	if n <= 4 {
		switch f {
		case EIGHTTAP, EIGHTTAP_SHARP:
			return 4
		case EIGHTTAP_SMOOTH:
			return 5
		}
	}
	return f
}

// blockInterPred is the block inter prediction process (7.11.3.4) for an
// unscaled reference (steps of 1 << SCALE_SUBPEL_BITS): the w by h block
// of plane at (startX, startY) (in 1/1024 samples) of ref, filtered by
// filt (InterpFilter: [0] the vertical filter, [1] the horizontal),
// rounded by rnd. With dst16 given, the prediction is clipped into it
// (single prediction, where InterPostRound is 0); otherwise preds[] goes
// unclipped into dst32.
func (s *interScratch) blockInterPred(ref *refFrameState, plane, startX, startY, w, h int, filt [2]int, rnd interRounding, bitDepth int, dst16 []uint16, dst32 []int32, dstStride int) {
	sub := min(plane, 1)
	lastX := ((ref.width + sub) >> sub) - 1
	lastY := ((ref.height + sub) >> sub) - 1
	fracX, fracY := (startX>>6)&SUBPEL_MASK, (startY>>6)&SUBPEL_MASK
	x0, y0 := (startX>>10)-3, (startY>>10)-3
	// The samples the filters read: (w + 7) by (h + 7) from (x0, y0),
	// straight from the reference when they are all inside it.
	src, ss := ref.rec[plane], ref.stride[plane]
	off := y0*ss + x0
	if x0 < 0 || y0 < 0 || x0+w+6 > lastX || y0+h+6 > lastY {
		ss = w + 7
		// the columns inside the reference copied, those left and right
		// of it its first and last
		in0, in1 := min(max(-x0, 0), ss), min(max(lastX+1-x0, 0), ss)
		for i := range h + 7 {
			row := src[min(max(y0+i, 0), lastY)*ref.stride[plane]:]
			e := s.edge[i*ss : i*ss+ss]
			for j := range in0 {
				e[j] = row[0]
			}
			if in1 > in0 {
				copy(e[in0:in1], row[x0+in0:x0+in1])
			}
			for j := max(in1, in0); j < ss; j++ {
				e[j] = row[lastX]
			}
		}
		src, off = s.edge[:], 0
	}

	fx, fy := filterFor(filt[1], w), filterFor(filt[0], h)
	maxV := int32(1)<<uint(bitDepth) - 1
	if fracX == 0 && fracY == 0 {
		// Both kernels are the identity: the samples, shifted by
		// InterPostRound.
		for i := range h {
			row := src[off+(i+3)*ss+3 : off+(i+3)*ss+3+w]
			if dst16 != nil {
				copy(dst16[i*dstStride:i*dstStride+w], row)
				continue
			}
			widen(dst32[i*dstStride:i*dstStride+w], row, uint(rnd.post))
		}
		return
	}

	// The horizontal filter, into the intermediate rows the vertical
	// filter's nonzero taps read.
	ty := subpelTaps[fy][fracY]
	r0, r1 := int(ty[0]), h-1+int(ty[1])
	if filterBlock(s, src, off, ss, w, h, r0, r1, &subpelFilters[fx][fracX], &subpelFilters[fy][fracY], rnd, maxV, dst16, dst32, dstStride) {
		return
	}
	mid := s.mid[:]
	kx := &subpelFilters[fx][fracX]
	tx := subpelTaps[fx][fracX]
	sh0 := uint(rnd.round0)
	add0 := int32(1) << (sh0 - 1)
	for r := r0; r < r1; r++ {
		row := src[off+r*ss : off+r*ss+w+7]
		out := mid[r*w : r*w+w]
		if fracX == 0 {
			widen(out, row[3:], FILTER_BITS-sh0)
			continue
		}
		filterRow(out, row, kx, int(tx[0]), int(tx[1]), add0, sh0)
	}

	// The vertical filter.
	ky := &subpelFilters[fy][fracY]
	sh1 := uint(rnd.round1)
	add1 := int32(1) << (sh1 - 1)
	lo, hi := int(ty[0]), int(ty[1])
	var acc [128]int32
	for r := range h {
		if dst16 == nil {
			filterCol(dst32[r*dstStride:r*dstStride+w], mid[(r+lo)*w:], w, ky, lo, hi, add1, sh1)
			continue
		}
		filterColClip(dst16[r*dstStride:r*dstStride+w], mid[(r+lo)*w:], w, ky, lo, hi, add1, sh1, maxV, acc[:w])
	}
}

// filterColClipGo is filterColGo with its outputs clipped to 0..maxV,
// as samples (tmp holds the unclipped ones).
func filterColClipGo(out []uint16, mid []int32, w int, k *[8]int16, lo, hi int, add int32, sh uint, maxV int32, tmp []int32) {
	filterColGo(tmp, mid, w, k, lo, hi, add, sh)
	for c, v := range tmp {
		out[c] = uint16(clip(v, maxV)) //nolint:gosec // clipped
	}
}

// widenGo sets out[i] to src[i] << sh.
func widenGo(out []int32, src []uint16, sh uint) {
	src = src[:len(out)]
	for i, v := range src {
		out[i] = int32(v) << sh
	}
}

// filterColGo is filterCol in Go. filterCol is one row of the vertical
// filter: out[c] is the kernel's
// taps lo..hi-1 over column c of the rows (w apart) from mid's, rounded
// by sh.
func filterColGo(out, mid []int32, w int, k *[8]int16, lo, hi int, add int32, sh uint) {
	switch hi - lo {
	case 6:
		k0, k1, k2, k3, k4, k5 := int32(k[lo]), int32(k[lo+1]), int32(k[lo+2]), int32(k[lo+3]), int32(k[lo+4]), int32(k[lo+5])
		m0, m1, m2 := mid[:w], mid[w:2*w], mid[2*w:3*w]
		m3, m4, m5 := mid[3*w:4*w], mid[4*w:5*w], mid[5*w:6*w]
		for c := range out {
			out[c] = (k0*m0[c] + k1*m1[c] + k2*m2[c] + k3*m3[c] + k4*m4[c] + k5*m5[c] + add) >> sh
		}
	case 8:
		k0, k1, k2, k3 := int32(k[0]), int32(k[1]), int32(k[2]), int32(k[3])
		k4, k5, k6, k7 := int32(k[4]), int32(k[5]), int32(k[6]), int32(k[7])
		m0, m1, m2, m3 := mid[:w], mid[w:2*w], mid[2*w:3*w], mid[3*w:4*w]
		m4, m5, m6, m7 := mid[4*w:5*w], mid[5*w:6*w], mid[6*w:7*w], mid[7*w:8*w]
		for c := range out {
			out[c] = (k0*m0[c] + k1*m1[c] + k2*m2[c] + k3*m3[c] + k4*m4[c] + k5*m5[c] + k6*m6[c] + k7*m7[c] + add) >> sh
		}
	case 4:
		k0, k1, k2, k3 := int32(k[lo]), int32(k[lo+1]), int32(k[lo+2]), int32(k[lo+3])
		m0, m1, m2, m3 := mid[:w], mid[w:2*w], mid[2*w:3*w], mid[3*w:4*w]
		for c := range out {
			out[c] = (k0*m0[c] + k1*m1[c] + k2*m2[c] + k3*m3[c] + add) >> sh
		}
	default:
		for c := range out {
			var sum int32
			for t := lo; t < hi; t++ {
				sum += int32(k[t]) * mid[(t-lo)*w+c]
			}
			out[c] = (sum + add) >> sh
		}
	}
}

// filterRowGo is filterRow in Go. filterRow is one row of the horizontal
// filter: out[c] is the kernel's
// taps lo..hi-1 over row[c+lo..], rounded by sh.
func filterRowGo(out []int32, row []uint16, k *[8]int16, lo, hi int, add int32, sh uint) {
	switch {
	case lo == 1 && hi == 7:
		k1, k2, k3, k4, k5, k6 := int32(k[1]), int32(k[2]), int32(k[3]), int32(k[4]), int32(k[5]), int32(k[6])
		for c := range out {
			p := row[c+1 : c+7]
			sum := k1*int32(p[0]) + k2*int32(p[1]) + k3*int32(p[2]) + k4*int32(p[3]) + k5*int32(p[4]) + k6*int32(p[5])
			out[c] = (sum + add) >> sh
		}
	case lo == 0 && hi == 8:
		k0, k1, k2, k3, k4, k5, k6, k7 := int32(k[0]), int32(k[1]), int32(k[2]), int32(k[3]), int32(k[4]), int32(k[5]), int32(k[6]), int32(k[7])
		for c := range out {
			p := row[c : c+8]
			sum := k0*int32(p[0]) + k1*int32(p[1]) + k2*int32(p[2]) + k3*int32(p[3]) + k4*int32(p[4]) + k5*int32(p[5]) + k6*int32(p[6]) + k7*int32(p[7])
			out[c] = (sum + add) >> sh
		}
	case lo == 2 && hi == 6:
		k2, k3, k4, k5 := int32(k[2]), int32(k[3]), int32(k[4]), int32(k[5])
		for c := range out {
			p := row[c+2 : c+6]
			sum := k2*int32(p[0]) + k3*int32(p[1]) + k4*int32(p[2]) + k5*int32(p[3])
			out[c] = (sum + add) >> sh
		}
	default:
		for c := range out {
			var sum int32
			for t := lo; t < hi; t++ {
				sum += int32(k[t]) * int32(row[c+t])
			}
			out[c] = (sum + add) >> sh
		}
	}
}

// predictInter is the inter prediction process (7.11.3.1) of a single
// reference block: the w by h block at (x, y) of a plane predicted from
// ref (the size of the frame) by m, filtered by filt, into dst.
func predictInter(ref *refFrameState, plane, x, y, w, h int, m mv, filt [2]int, bitDepth int, dst []uint16, dstStride int, s *interScratch) {
	startX, startY, _, _ := scaleMv(plane, x, y, m, ref.width, ref.height, ref.width, ref.height)
	s.blockInterPred(ref, plane, startX, startY, w, h, filt, interRoundingFor(bitDepth, false), bitDepth, dst, nil, dstStride)
}

// predictCompound is a compound block's prediction (COMPOUND_AVERAGE):
// each reference's at the compound rounding, averaged.
func predictCompound(refs *[TOTAL_REFS_PER_FRAME]*refFrameState, plane, x, y, w, h int, ref [2]int, m [2]mv, filt [2]int, bitDepth int, wt [2]int32, dst []uint16, dstStride int, s *interScratch, mk *compMask, b *blockState) {
	rnd := interRoundingFor(bitDepth, true)
	for k := range 2 {
		if cap(s.comp[k]) < 128*128 {
			s.comp[k] = make([]int32, 128*128)
		}
		r := refs[ref[k]]
		startX, startY, _, _ := scaleMv(plane, x, y, m[k], r.width, r.height, r.width, r.height)
		s.blockInterPred(r, plane, startX, startY, w, h, filt, rnd, bitDepth, nil, s.comp[k], w)
	}
	sh := uint(1 + rnd.post)
	maxV := int32(1)<<uint(bitDepth) - 1
	p0, p1 := s.comp[0], s.comp[1]
	if mk != nil { // COMPOUND_WEDGE, COMPOUND_DIFFWTD
		if plane == 0 {
			mk.setMask(b.size, p0, p1, w, h, bitDepth, rnd, &b.maskBuf)
		}
		mk.blend(plane, p0, p1, w, h, bitDepth, rnd, dst, dstStride)
		return
	}
	w0, w1 := int32(1), int32(1)
	if wt[0] != 0 { // COMPOUND_DISTANCE
		sh = uint(4 + rnd.post)
		w0, w1 = wt[0], wt[1]
	}
	for i := range h {
		compAvg(dst[i*dstStride:i*dstStride+w], p0[i*w:i*w+w], p1[i*w:i*w+w], w0, w1, sh, maxV)
	}
}

// compAvgGo is the compound average, or with weights w0 and w1 the
// distance weighted compound, of a row: out[i] = (w0*p0[i] + w1*p1[i] +
// 1<<(sh-1)) >> sh, clipped to 0..maxV.
func compAvgGo(out []uint16, p0, p1 []int32, w0, w1 int32, sh uint, maxV int32) {
	p0, p1 = p0[:len(out)], p1[:len(out)]
	for j := range out {
		v := (w0*p0[j] + w1*p1[j] + 1<<(sh-1)) >> sh
		out[j] = uint16(clip(v, maxV)) //nolint:gosec // clipped
	}
}

// predictInterBlock is compute_prediction's inter part for a plane of
// block b: its prediction into dst (stride dstStride, from the block's
// top left sample of the plane, the chroma one of the 4x4 chroma block
// when b is narrower or shorter than 8), from the references by
// RefFrames (refs[LAST_FRAME..ALTREF_FRAME]), filtered by filt. The
// block's own RefFrames and Mvs must be stored. A chroma block covering
// several luma blocks that are all inter is predicted in parts, each by
// its luma block's motion (the spec's candRow and candCol); with one of
// them intra, it is predicted whole by the block's.
func (f *frameEnc) predictInterBlock(refs *[TOTAL_REFS_PER_FRAME]*refFrameState, b *blockState, plane int, filt [2]int, dst []uint16, dstStride int, s *interScratch) {
	sub := min(plane, 1)
	if b.warp && b.warpValid {
		// the block warp process (useWarp 1): luma, and chroma 8 or
		// more both ways
		w, h := int(blockWidth[b.size])>>sub, int(blockHeight[b.size])>>sub
		if w >= 8 && h >= 8 {
			if cap(s.comp[0]) < 128*128 {
				s.comp[0] = make([]int32, 128*128)
			}
			pred := s.comp[0][:w*h]
			blockWarp(refs[b.refFrame], plane, (b.c*MI_SIZE)>>sub, (b.r*MI_SIZE)>>sub, w, h, &b.warpParams, interRoundingFor(f.bitDepth, false), pred)
			maxV := int32(1)<<uint(f.bitDepth) - 1
			for i := range h {
				row := dst[i*dstStride : i*dstStride+w]
				for j := range row {
					row[j] = uint16(clip(pred[i*w+j], maxV)) //nolint:gosec // clipped
				}
			}
			return
		}
	}
	if plane == 0 {
		// the block's own motion (what setModes stores, so a luma block
		// is never in parts): read from the block, not its mode info
		if f.fp.interpFilter == SWITCHABLE {
			filt = [2]int{b.interp, b.interp}
		}
		w, h := int(blockWidth[b.size]), int(blockHeight[b.size])
		if b.refFrame2 > INTRA_FRAME && !b.ii.on {
			ref := [2]int{b.refFrame, b.refFrame2}
			var wt [2]int32 // COMPOUND_AVERAGE
			if b.compDist {
				wt = f.fp.distWeights(ref)
			}
			var mk *compMask
			if b.masked {
				mk = &b.mask
			}
			predictCompound(refs, 0, b.c*MI_SIZE, b.r*MI_SIZE, w, h, ref, [2]mv{b.mv, b.mv2}, filt, f.bitDepth, wt, dst, dstStride, s, mk, b)
			return
		}
		predictInter(refs[b.refFrame], 0, b.c*MI_SIZE, b.r*MI_SIZE, w, h, b.mv, filt, f.bitDepth, dst, dstStride, s)
		return
	}
	planeSz := int(subsampledSize[b.size][1][1])
	num4x4W, num4x4H := int(num4x4BlocksWide[planeSz]), int(num4x4BlocksHigh[planeSz])
	baseX, baseY := (b.c>>sub)*MI_SIZE, (b.r>>sub)*MI_SIZE
	candRow, candCol := (b.r>>sub)<<sub, (b.c>>sub)<<sub
	predW, predH := int(blockWidth[b.size])>>sub, int(blockHeight[b.size])>>sub
	someUseIntra := false
	for r := range num4x4H << sub {
		for c := range num4x4W << sub {
			// outside the frame is the block's own (decode_block stores
			// the whole block): not intra
			if candRow+r >= f.miRows || candCol+c >= f.miCols {
				continue
			}
			if f.refFrames[0][f.mi(candRow+r, candCol+c)] == INTRA_FRAME {
				someUseIntra = true
			}
		}
	}
	if someUseIntra {
		predW, predH = num4x4W*4, num4x4H*4
		candRow, candCol = b.r, b.c
	}
	r := 0
	for y := 0; y < num4x4H*4; y += predH {
		c := 0
		for x := 0; x < num4x4W*4; x += predW {
			i := f.mi(candRow+r, candCol+c)
			filt := filt
			if f.fp.interpFilter == SWITCHABLE {
				filt = [2]int{int(f.interp[i]), int(f.interp[i])}
			}
			if f.refFrames[1][i] > INTRA_FRAME {
				ref := [2]int{int(f.refFrames[0][i]), int(f.refFrames[1][i])}
				var wt [2]int32 // COMPOUND_AVERAGE
				if f.compIdx[i] == 0 {
					wt = f.fp.distWeights(ref)
				}
				var mk *compMask
				if b.masked {
					mk = &b.mask
				}
				predictCompound(refs, plane, baseX+x, baseY+y, predW, predH, ref,
					[2]mv{f.mvs[0][i], f.mvs[1][i]}, filt, f.bitDepth, wt, dst[y*dstStride+x:], dstStride, s, mk, b)
				c++
				continue
			}
			predictInter(refs[f.refFrames[0][i]], plane, baseX+x, baseY+y, predW, predH, f.mvs[0][i], filt, f.bitDepth, dst[y*dstStride+x:], dstStride, s)
			c++
		}
		r++
	}
}

// obmcMask is get_obmc_mask.
func obmcMask(n int) []uint8 {
	switch n {
	case 2:
		return obmcMask2[:]
	case 4:
		return obmcMask4[:]
	case 8:
		return obmcMask8[:]
	case 16:
		return obmcMask16[:]
	}
	return obmcMask32[:]
}

// overlapPredict is the overlapped motion compensation process
// (7.11.3.9) for a plane of block b, whose prediction (the plane's whole
// block: an OBMC block is 8 by 8 or larger) is in dst (stride
// dstStride): strips along its top and left edges predicted by the
// overlappable above and left neighbours' first reference and motion
// vector, with their interpolation filter, blended in by the masks. The
// neighbours' RefFrames, Mvs, InterpFilters and MiSizes must be stored.
func (f *frameEnc) overlapPredict(refs *[TOTAL_REFS_PER_FRAME]*refFrameState, b *blockState, plane int, filt [2]int, dst []uint16, dstStride int, s *interScratch) {
	sub := uint(min(plane, 1))
	planeSz := b.size
	if plane > 0 {
		planeSz = int(subsampledSize[b.size][1][1])
	}
	w, h := int(blockWidth[b.size])>>sub, int(blockHeight[b.size])>>sub
	baseX, baseY := (b.c>>sub)*MI_SIZE, (b.r>>sub)*MI_SIZE
	// overlap predicts the strip at (predX, predY) of a neighbour's (mi
	// i) motion and blends it in (pass 0 from above, 1 from the left).
	// The rows (columns) whose mask is 64 keep the block's prediction, so
	// they are not predicted; the filters are those of the whole strip
	// (4 or less predicted only when the strip is 4 or less).
	overlap := func(i, predX, predY, predW, predH, pass int) {
		mask := obmcMask(predH)
		pw, ph := predW, predH-predH/4
		if pass == 1 {
			mask = obmcMask(predW)
			pw, ph = predW-predW/4, predH
		}
		fl := filt
		if f.fp.interpFilter == SWITCHABLE {
			fl = [2]int{int(f.interp[i]), int(f.interp[i])}
		}
		op := s.obmc[:pw*ph]
		predictInter(refs[f.refFrames[0][i]], plane, predX, predY, pw, ph, f.mvs[0][i], fl, f.bitDepth, op, pw, s)
		d := dst[(predY-baseY)*dstStride+predX-baseX:]
		for y := range ph {
			row, o := d[y*dstStride:y*dstStride+pw], op[y*pw:(y+1)*pw]
			if pass == 0 {
				m := uint32(mask[y])
				for x := range row {
					row[x] = uint16((m*uint32(row[x]) + (64-m)*uint32(o[x]) + 32) >> 6) //nolint:gosec // a blend of samples
				}
				continue
			}
			for x := range row {
				m := uint32(mask[x])
				row[x] = uint16((m*uint32(row[x]) + (64-m)*uint32(o[x]) + 32) >> 6) //nolint:gosec // a blend of samples
			}
		}
	}
	if b.availU && planeSz >= BLOCK_8X8 {
		n, limit := 0, min(4, int(miWidthLog2[b.size]))
		for x4 := b.c; n < limit && x4 < min(f.miCols, b.c+b.bw4); {
			i := f.mi(b.r-1, x4|1)
			step4 := min(max(int(num4x4BlocksWide[f.miSize[i]]), 2), 16)
			if f.refFrames[0][i] > INTRA_FRAME {
				n++
				overlap(i, (x4*4)>>sub, baseY, min(w, (step4*MI_SIZE)>>sub), min(h>>1, 32>>sub), 0)
			}
			x4 += step4
		}
	}
	if b.availL {
		n, limit := 0, min(4, int(miHeightLog2[b.size]))
		for y4 := b.r; n < limit && y4 < min(f.miRows, b.r+b.bh4); {
			i := f.mi(y4|1, b.c-1)
			step4 := min(max(int(num4x4BlocksHigh[f.miSize[i]]), 2), 16)
			if f.refFrames[0][i] > INTRA_FRAME {
				n++
				overlap(i, baseX, (y4*4)>>sub, min(w>>1, 32>>sub), min(h, (step4*MI_SIZE)>>sub), 1)
			}
			y4 += step4
		}
	}
}
