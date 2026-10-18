//go:build !amd64 || purego

package av1

func filterRow(out []int32, row []uint16, k *[8]int16, lo, hi int, add int32, sh uint) {
	filterRowGo(out, row, k, lo, hi, add, sh)
}

func filterCol(out, mid []int32, w int, k *[8]int16, lo, hi int, add int32, sh uint) {
	filterColGo(out, mid, w, k, lo, hi, add, sh)
}
