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
