//go:build amd64 && !purego

package av1

//go:noescape
func filterRowAVX2(out *int32, row *uint16, n int, k *int32, taps int, add int32, sh uint64)

//go:noescape
func filterColAVX2(out *int32, mid *int32, w int, n int, k *int32, taps int, add int32, sh uint64)

//go:noescape
func filterColClipAVX2(out *uint16, mid *int32, w int, n int, k *int32, taps int, add int32, sh uint64, maxV int32)

//go:noescape
func widenAVX2(out *int32, src *uint16, n int, sh uint64)

//go:noescape
func compAvgAVX2(out *uint16, p0, p1 *int32, n int, w0, w1, add int32, sh uint64, maxV int32)

// filterRow is filterRowGo, 8 outputs at a time with AVX2.
func filterRow(out []int32, row []uint16, k *[8]int16, lo, hi int, add int32, sh uint) {
	n := len(out)
	if !useAVX2 || n == 0 || n&7 != 0 || hi-1+n > len(row) {
		filterRowGo(out, row, k, lo, hi, add, sh)
		return
	}
	var kk [8]int32
	for t := lo; t < hi; t++ {
		kk[t-lo] = int32(k[t])
	}
	filterRowAVX2(&out[0], &row[lo], n, &kk[0], hi-lo, add, uint64(sh))
}

// filterCol is filterColGo, 8 outputs at a time with AVX2.
func filterCol(out, mid []int32, w int, k *[8]int16, lo, hi int, add int32, sh uint) {
	n := len(out)
	if !useAVX2 || n == 0 || n&7 != 0 || (hi-lo-1)*w+n > len(mid) {
		filterColGo(out, mid, w, k, lo, hi, add, sh)
		return
	}
	var kk [8]int32
	for t := lo; t < hi; t++ {
		kk[t-lo] = int32(k[t])
	}
	filterColAVX2(&out[0], &mid[0], w, n, &kk[0], hi-lo, add, uint64(sh))
}

// filterColClip is filterColClipGo, 8 outputs at a time with AVX2.
func filterColClip(out []uint16, mid []int32, w int, k *[8]int16, lo, hi int, add int32, sh uint, maxV int32, tmp []int32) {
	n := len(out)
	if !useAVX2 || n == 0 || n&7 != 0 || (hi-lo-1)*w+n > len(mid) {
		filterColClipGo(out, mid, w, k, lo, hi, add, sh, maxV, tmp)
		return
	}
	var kk [8]int32
	for t := lo; t < hi; t++ {
		kk[t-lo] = int32(k[t])
	}
	filterColClipAVX2(&out[0], &mid[0], w, n, &kk[0], hi-lo, add, uint64(sh), maxV)
}

// widen is widenGo, 8 at a time with AVX2.
func widen(out []int32, src []uint16, sh uint) {
	n := 0
	if useAVX2 {
		n = len(out) &^ 7
	}
	if n > 0 {
		widenAVX2(&out[0], &src[0], n, uint64(sh))
	}
	widenGo(out[n:], src[n:len(out)], sh)
}

// compAvg is compAvgGo, 8 at a time with AVX2.
func compAvg(out []uint16, p0, p1 []int32, w0, w1 int32, sh uint, maxV int32) {
	n := 0
	if useAVX2 {
		n = len(out) &^ 7
	}
	if n > 0 {
		compAvgAVX2(&out[0], &p0[0], &p1[0], n, w0, w1, 1<<(sh-1), uint64(sh), maxV)
	}
	compAvgGo(out[n:], p0[n:len(out)], p1[n:len(out)], w0, w1, sh, maxV)
}

//go:noescape
func filterHAVX2(mid *int16, src *uint16, ss, w, rows int, k *int16, add int32, sh uint64)

//go:noescape
func filterVClipAVX2(dst *uint16, ds int, mid *int16, w, h int, k *int16, add int32, sh uint64, maxV int32)

//go:noescape
func filterV32AVX2(dst *int32, ds int, mid *int16, w, h int, k *int16, add int32, sh uint64)

// filterBlock is blockInterPred's two filter passes over a block w wide
// (a multiple of 8) at once with AVX2, the intermediate values words:
// false when it cannot (no AVX2, w not a multiple of 8). src at off is
// the block's samples from 3 left and above (ss apart); the horizontal
// filter's rows are r0..r1-1 (those the vertical filter's nonzero taps
// read).
func filterBlock(s *interScratch, src []uint16, off, ss, w, h, r0, r1 int, kx, ky *[8]int16, rnd interRounding, maxV int32, dst16 []uint16, dst32 []int32, dstStride int) bool {
	if !useAVX2 || w&7 != 0 {
		return false
	}
	filterH16(s.mid16[r0*w:], src, off+r0*ss, ss, w, r1-r0, kx, uint(rnd.round0))
	sh1 := uint(rnd.round1)
	if dst16 != nil {
		filterVClip16(dst16, dstStride, s.mid16[:], w, h, ky, sh1, maxV)
	} else {
		_ = dst32[(h-1)*dstStride+w-1]
		filterV32AVX2(&dst32[0], dstStride, &s.mid16[0], w, h, &ky[0], 1<<(sh1-1), uint64(sh1))
	}
	return true
}

// filterH16 is the horizontal filter of rows rows of w samples (w a
// multiple of 8, with AVX2) into mid as words: each row's w+7 samples
// from src at off, ss apart; the kernel's 8 taps, rounded by sh.
func filterH16(mid []int16, src []uint16, off, ss, w, rows int, k *[8]int16, sh uint) {
	_, _ = src[off+(rows-1)*ss+w+6], mid[rows*w-1]
	filterHAVX2(&mid[0], &src[off], ss, w, rows, &k[0], 1<<(sh-1), uint64(sh))
}

// filterVClip16 is the vertical filter of a w by h block (w a multiple of
// 8, with AVX2) from mid's words (w apart, h+7 rows) into dst (ds
// apart), clipped to 0..maxV: the kernel's 8 taps, rounded by sh.
func filterVClip16(dst []uint16, ds int, mid []int16, w, h int, k *[8]int16, sh uint, maxV int32) {
	_, _ = dst[(h-1)*ds+w-1], mid[(h+6)*w+w-1]
	filterVClipAVX2(&dst[0], ds, &mid[0], w, h, &k[0], 1<<(sh-1), uint64(sh), maxV)
}
