//go:build amd64 && !purego

package av1

//go:noescape
func blendAVX2(dst *uint16, dstStride int, intra, mask *uint16, w, h int)

// blendBlock is interIntraBlend's blend: dst (stride dstStride) weighed
// by 64-mask, intra (stride w) by mask, w by h.
func blendBlock(dst []uint16, dstStride int, intra, mask []uint16, w, h int) {
	if !useAVX2 || w&7 != 0 {
		blendBlockGo(dst, dstStride, intra, mask, w, h)
		return
	}
	_, _, _ = dst[(h-1)*dstStride+w-1], intra[w*h-1], mask[w*h-1]
	blendAVX2(&dst[0], dstStride, &intra[0], &mask[0], w, h)
}
