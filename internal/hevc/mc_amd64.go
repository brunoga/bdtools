//go:build amd64 && !purego

package hevc

import "unsafe"

func cpuidAsm(leaf, sub uint32) (eax, ebx, ecx, edx uint32)
func xgetbvAsm() (eax, edx uint32)

// useAVX2 reports whether the AVX2 kernels can be used: the CPU has AVX2
// and the OS saves the YMM state.
var useAVX2 = func() bool {
	if top, _, _, _ := cpuidAsm(0, 0); top < 7 {
		return false
	}
	_, _, ecx, _ := cpuidAsm(1, 0)
	if ecx&(1<<27) == 0 || ecx&(1<<28) == 0 { // OSXSAVE, AVX
		return false
	}
	if eax, _ := xgetbvAsm(); eax&6 != 6 {
		return false
	}
	_, ebx, _, _ := cpuidAsm(7, 0)
	return ebx&(1<<5) != 0
}()

//go:noescape
func hFilterAVX2(dst *int16, dstride int, src *uint16, sstride int, w4, h int, c *tapPairs, pairs int, shift int)

//go:noescape
func vFilterAVX2(dst *int16, dstride int, src unsafe.Pointer, sstride int, w4, h int, c *tapPairs, pairs int, shift int)

//go:noescape
func copyAVX2(dst *int16, dstride int, src *uint16, sstride int, w4, h int, shift int)

//go:noescape
func putAVX2(dst *uint16, dstride int, a, b *int16, abstride, w4, h, wts, add, shift, o, maxV int)

// The kernels take the columns in fours; the Go versions any left over
// (chroma blocks 2 or 6 wide). The slices are checked to hold the block
// first, as the kernels index them unchecked.

func hFilter(dst []int16, dstride, w, h int, src []uint16, sstride int, c *tapPairs, pairs int, shift uint) {
	w4 := w &^ 3
	if !useAVX2 || w4 == 0 || h == 0 {
		hFilterGo(dst, dstride, w, h, src, sstride, c, pairs, shift)
		return
	}
	_ = dst[(h-1)*dstride+w-1]
	_ = src[(h-1)*sstride+w+2*pairs-2]
	hFilterAVX2(&dst[0], dstride, &src[0], sstride, w4, h, c, pairs, int(shift))
	if w4 < w {
		hFilterGo(dst[w4:], dstride, w-w4, h, src[w4:], sstride, c, pairs, shift)
	}
}

func vFilter(dst []int16, dstride, w, h int, src []uint16, sstride int, c *tapPairs, pairs int, shift uint) {
	w4 := w &^ 3
	if !useAVX2 || w4 == 0 || h == 0 {
		vFilterGo(dst, dstride, w, h, src, sstride, c, pairs, shift)
		return
	}
	_ = dst[(h-1)*dstride+w-1]
	_ = src[(h+2*pairs-2)*sstride+w-1]
	vFilterAVX2(&dst[0], dstride, unsafe.Pointer(&src[0]), sstride, w4, h, c, pairs, int(shift))
	if w4 < w {
		vFilterGo(dst[w4:], dstride, w-w4, h, src[w4:], sstride, c, pairs, shift)
	}
}

// vFilter16 is vFilter from a first pass, its shift 6.
func vFilter16(dst []int16, dstride, w, h int, src []int16, sstride int, c *tapPairs, pairs int) {
	w4 := w &^ 3
	if !useAVX2 || w4 == 0 || h == 0 {
		vFilterGo(dst, dstride, w, h, src, sstride, c, pairs, 6)
		return
	}
	_ = dst[(h-1)*dstride+w-1]
	_ = src[(h+2*pairs-2)*sstride+w-1]
	vFilterAVX2(&dst[0], dstride, unsafe.Pointer(&src[0]), sstride, w4, h, c, pairs, 6)
	if w4 < w {
		vFilterGo(dst[w4:], dstride, w-w4, h, src[w4:], sstride, c, pairs, 6)
	}
}

func copyBlock(dst []int16, dstride, w, h int, src []uint16, sstride int, shift uint) {
	w4 := w &^ 3
	if !useAVX2 || w4 == 0 || h == 0 {
		copyBlockGo(dst, dstride, w, h, src, sstride, shift)
		return
	}
	_ = dst[(h-1)*dstride+w-1]
	_ = src[(h-1)*sstride+w-1]
	copyAVX2(&dst[0], dstride, &src[0], sstride, w4, h, int(shift))
	if w4 < w {
		copyBlockGo(dst[w4:], dstride, w-w4, h, src[w4:], sstride, shift)
	}
}

func putBlock(dst []uint16, dstride int, a, b []int16, abstride, w, h int, w0, w1, add int, shift uint, o, maxV int) {
	w4 := w &^ 3
	if !useAVX2 || w4 == 0 || h == 0 {
		putBlockGo(dst, dstride, a, b, abstride, w, h, w0, w1, add, shift, o, maxV)
		return
	}
	_ = dst[(h-1)*dstride+w-1]
	_ = a[(h-1)*abstride+w-1]
	_ = b[(h-1)*abstride+w-1]
	wts := int(uint32(uint16(int16(w0))) | uint32(uint16(int16(w1)))<<16)
	putAVX2(&dst[0], dstride, &a[0], &b[0], abstride, w4, h, wts, add, int(shift), o, maxV)
	if w4 < w {
		putBlockGo(dst[w4:], dstride, a[w4:], b[w4:], abstride, w-w4, h, w0, w1, add, shift, o, maxV)
	}
}

//go:noescape
func packBytesAVX2(dst *byte, src *uint16, n int)

//go:noescape
func packWordsAVX2(dst *byte, src *uint16, n int, shift int)

//go:noescape
func weaveBytesAVX2(dst *byte, cb, cr *uint16, n int)

//go:noescape
func weaveWordsAVX2(dst *byte, cb, cr *uint16, n int, shift int)

// The packing kernels take the samples in sixteens, the Go versions the
// rest.

func packBytes(dst []byte, src []uint16) {
	n := len(src) &^ 15
	if !useAVX2 || n == 0 {
		packBytesGo(dst, src)
		return
	}
	_ = dst[len(src)-1]
	packBytesAVX2(&dst[0], &src[0], n)
	packBytesGo(dst[n:], src[n:])
}

func packWords(dst []byte, src []uint16, shift uint) {
	n := len(src) &^ 15
	if !useAVX2 || n == 0 {
		packWordsGo(dst, src, shift)
		return
	}
	_ = dst[2*len(src)-1]
	packWordsAVX2(&dst[0], &src[0], n, int(shift))
	packWordsGo(dst[2*n:], src[n:], shift)
}

func weaveBytes(dst []byte, cb, cr []uint16) {
	n := len(cb) &^ 15
	if !useAVX2 || n == 0 {
		weaveBytesGo(dst, cb, cr)
		return
	}
	_ = dst[2*len(cb)-1]
	_ = cr[len(cb)-1]
	weaveBytesAVX2(&dst[0], &cb[0], &cr[0], n)
	weaveBytesGo(dst[2*n:], cb[n:], cr[n:])
}

func weaveWords(dst []byte, cb, cr []uint16, shift uint) {
	n := len(cb) &^ 15
	if !useAVX2 || n == 0 {
		weaveWordsGo(dst, cb, cr, shift)
		return
	}
	_ = dst[4*len(cb)-1]
	_ = cr[len(cb)-1]
	weaveWordsAVX2(&dst[0], &cb[0], &cr[0], n, int(shift))
	weaveWordsGo(dst[4*n:], cb[n:], cr[n:], shift)
}
