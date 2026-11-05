//go:build amd64 && !purego

package av1

//go:noescape
func residAVX2(src *uint16, ss int, pred *uint16, out *int32, w, h int)

//go:noescape
func reconAddAVX2(rec *uint16, rs int, res *int32, w, h, maxV int)

// residual is out (w wide) = src (stride ss) - pred (w wide), w x h.
func residual(src []uint16, ss int, pred []uint16, out []int32, w, h int) {
	if !useAVX2 || w&3 != 0 {
		residualGo(src, ss, pred, out, w, h)
		return
	}
	_, _, _ = src[(h-1)*ss+w-1], pred[w*h-1], out[w*h-1]
	residAVX2(&src[0], ss, &pred[0], &out[0], w, h)
}

// reconAdd is rec (stride rs) += res (w wide), clamped to 0..maxV.
func reconAdd(rec []uint16, rs int, res []int32, w, h int, maxV int32) {
	if !useAVX2 || w&3 != 0 {
		reconAddGo(rec, rs, res, w, h, maxV)
		return
	}
	_, _ = rec[(h-1)*rs+w-1], res[w*h-1]
	reconAddAVX2(&rec[0], rs, &res[0], w, h, int(maxV))
}
