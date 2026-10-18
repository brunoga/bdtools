//go:build amd64 && !purego

package av1

//go:noescape
func filterRowAVX2(out *int32, row *uint16, n int, k *int32, taps int, add int32, sh uint64)

//go:noescape
func filterColAVX2(out *int32, mid *int32, w int, n int, k *int32, taps int, add int32, sh uint64)

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
