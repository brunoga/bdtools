//go:build !amd64 || purego

package av1

const useAVX2 = false

func fwdColAVX2(kind, log2 int, src *int32, srcStride int, dst *int32, dstStride int, cospi *int32, cosBit, up, down int) bool {
	return false
}

func fwdRowAVX2(kind, log2 int, flip bool, src *int32, srcStride int, dst *int32, dstStride int, cospi *int32, cosBit, up, down int) bool {
	return false
}

func invColAVX2(kind, log2 int, src *int32, srcStride int, dst *int32, dstStride int, lo, hi, rnd, shift, clo, chi, rect int) bool {
	return false
}

func invRowAVX2(kind, log2 int, src *int32, srcStride int, dst *int32, dstStride int, lo, hi, rnd, shift, clo, chi, rect int) bool {
	return false
}

func invAVX2(kind, log2 int) bool { return false }

func blendBlock(dst []uint16, dstStride int, intra, mask []uint16, w, h int) {
	blendBlockGo(dst, dstStride, intra, mask, w, h)
}

func sse(a []uint16, as int, b []uint16, bs int, w, h int) int64 { return sseGo(a, as, b, bs, w, h) }
