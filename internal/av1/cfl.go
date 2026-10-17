package av1

// Chroma from luma (spec 7.11.5): a chroma block predicted as DC plus the
// block's reconstructed luma, subsampled, less its mean, scaled by an
// alpha (in 1/8ths, -16..16 but not 0) of each chroma plane's.

// cflAllowed reports whether the block may use CFL (uv_mode's CDF with it).
func (t *tileEnc) cflAllowed() bool {
	b := &t.b
	return !t.f.lossless && max(blockWidth[b.size], blockHeight[b.size]) <= 32
}

// cflLuma fills t.cflL with the luma under a chroma transform block (3
// fractional bits) and gives its average.
func (t *tileEnc) cflLuma(startX, startY, txSz int) int32 {
	f, b := t.f, &t.b
	w, h := int(txWidth[txSz]), int(txHeight[txSz])
	rec, st := f.rec[0], f.stride[0]
	var sum int32
	for i := range h {
		ly := min((startY+i)<<1, b.maxLumaH-2)
		for j := range w {
			lx := min((startX+j)<<1, b.maxLumaW-2)
			v := (int32(rec[ly*st+lx]) + int32(rec[ly*st+lx+1]) + int32(rec[(ly+1)*st+lx]) + int32(rec[(ly+1)*st+lx+1])) << 1
			t.cflL[i*w+j] = v
			sum += v
		}
	}
	n := uint(txWidthLog2[txSz] + txHeightLog2[txSz])
	return (sum + 1<<(n-1)) >> n
}

// cflApply adds the scaled luma to a DC prediction.
func (t *tileEnc) cflApply(pred []uint16, w, h int, avg int32, alpha int) {
	maxV := int32(1)<<uint(t.f.bitDepth) - 1
	for i := range w * h {
		v := int32(pred[i]) + round2Signed(int32(alpha)*(t.cflL[i]-avg), 6)
		pred[i] = uint16(clip(v, maxV)) //nolint:gosec // clipped
	}
}

// cflAlphas picks each chroma plane's alpha: the one whose prediction is
// nearest the source (a block that may use CFL has one transform block a
// plane).
func (t *tileEnc) cflAlphas() (int, int) {
	f, b := t.f, &t.b
	txSz := t.planeTxSize(1)
	w, h := int(txWidth[txSz]), int(txHeight[txSz])
	x0, y0 := (b.c>>1)*4, (b.r>>1)*4
	avg := t.cflLuma(x0, y0, txSz)
	var alphas [2]int
	saved := b.uvMode
	b.uvMode = DC_PRED
	for p := 1; p <= 2; p++ {
		ib := t.intraBlockFor(p, x0, y0, txSz)
		t.edges.predictIntra(&ib, f.rec[p], f.stride[p], t.pred[:])
		src, st := f.src[p], f.stride[p]
		maxV := int32(1)<<uint(f.bitDepth) - 1
		best, bestSSE := 0, int64(-1)
		for a := -16; a <= 16; a++ {
			var sse int64
			for i := range h {
				for j := range w {
					v := clip(int32(t.pred[i*w+j])+round2Signed(int32(a)*(t.cflL[i*w+j]-avg), 6), maxV)
					d := int64(src[(y0+i)*st+x0+j]) - int64(v)
					sse += d * d
				}
			}
			if bestSSE < 0 || sse < bestSSE {
				best, bestSSE = a, sse
			}
		}
		alphas[p-1] = best
	}
	b.uvMode = saved
	return alphas[0], alphas[1]
}

// writeCflAlphas is read_cfl_alphas.
func (t *tileEnc) writeCflAlphas(cd coder) {
	b := &t.b
	sign := func(a int) int {
		switch {
		case a < 0:
			return CFL_SIGN_NEG
		case a > 0:
			return CFL_SIGN_POS
		}
		return CFL_SIGN_ZERO
	}
	su, sv := sign(b.cflU), sign(b.cflV)
	cd.symbol(su*3+sv-1, t.cdf.cflSign[:])
	if su != CFL_SIGN_ZERO {
		cd.symbol(abs(b.cflU)-1, t.cdf.cflAlpha[(su-1)*3+sv][:])
	}
	if sv != CFL_SIGN_ZERO {
		cd.symbol(abs(b.cflV)-1, t.cdf.cflAlpha[(sv-1)*3+su][:])
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
