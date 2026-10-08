//go:build !amd64 || purego

package hevc

const useAVX2 = false

func hFilter(dst []int16, dstride, w, h int, src []uint16, sstride int, c *tapPairs, pairs int, shift uint) {
	hFilterGo(dst, dstride, w, h, src, sstride, c, pairs, shift)
}

func vFilter(dst []int16, dstride, w, h int, src []uint16, sstride int, c *tapPairs, pairs int, shift uint) {
	vFilterGo(dst, dstride, w, h, src, sstride, c, pairs, shift)
}

func vFilter16(dst []int16, dstride, w, h int, src []int16, sstride int, c *tapPairs, pairs int) {
	vFilterGo(dst, dstride, w, h, src, sstride, c, pairs, 6)
}

func copyBlock(dst []int16, dstride, w, h int, src []uint16, sstride int, shift uint) {
	copyBlockGo(dst, dstride, w, h, src, sstride, shift)
}

func putBlock(dst []uint16, dstride int, a, b []int16, abstride, w, h int, w0, w1, add int, shift uint, o, maxV int) {
	putBlockGo(dst, dstride, a, b, abstride, w, h, w0, w1, add, shift, o, maxV)
}

func packBytes(dst []byte, src []uint16)                 { packBytesGo(dst, src) }
func packWords(dst []byte, src []uint16, shift uint)     { packWordsGo(dst, src, shift) }
func weaveBytes(dst []byte, cb, cr []uint16)             { weaveBytesGo(dst, cb, cr) }
func weaveWords(dst []byte, cb, cr []uint16, shift uint) { weaveWordsGo(dst, cb, cr, shift) }
