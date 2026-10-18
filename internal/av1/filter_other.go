//go:build !amd64 || purego

package av1

func filterRow(out []int32, row []uint16, k *[8]int16, lo, hi int, add int32, sh uint) {
	filterRowGo(out, row, k, lo, hi, add, sh)
}

func filterCol(out, mid []int32, w int, k *[8]int16, lo, hi int, add int32, sh uint) {
	filterColGo(out, mid, w, k, lo, hi, add, sh)
}

func filterColClip(out []uint16, mid []int32, w int, k *[8]int16, lo, hi int, add int32, sh uint, maxV int32, tmp []int32) {
	filterColClipGo(out, mid, w, k, lo, hi, add, sh, maxV, tmp)
}

func widen(out []int32, src []uint16, sh uint) { widenGo(out, src, sh) }

func compAvg(out []uint16, p0, p1 []int32, w0, w1 int32, sh uint, maxV int32) {
	compAvgGo(out, p0, p1, w0, w1, sh, maxV)
}
