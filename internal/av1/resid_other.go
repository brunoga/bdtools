//go:build !amd64 || purego

package av1

func residual(src []uint16, ss int, pred []uint16, out []int32, w, h int) {
	residualGo(src, ss, pred, out, w, h)
}

func reconAdd(rec []uint16, rs int, res []int32, w, h int, maxV int32) {
	reconAddGo(rec, rs, res, w, h, maxV)
}
