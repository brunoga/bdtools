//go:build amd64 && !purego

package av1

func cpuidAsm(leaf, sub uint32) (eax, ebx, ecx, edx uint32)
func xgetbvAsm() (eax, edx uint32)

//go:noescape
func sadAVX2(a *uint16, as int, b *uint16, bs int, w, h int) int

//go:noescape
func satd8AVX2(a *uint16, as int, b *uint16, bs int) int

//go:noescape
func satd16x8AVX2(a *uint16, as int, b *uint16, bs int) uint64

// satd16x8 is satd8 of the two 8x8 blocks of a 16x8 region, summed.
func satd16x8(a []uint16, as int, b []uint16, bs int) int {
	if !useAVX2 {
		return satd8Go(a, as, b, bs) + satd8Go(a[8:], as, b[8:], bs)
	}
	_ = a[7*as+15]
	_ = b[7*bs+15]
	v := satd16x8AVX2(&a[0], as, &b[0], bs)
	return int((v&0xffffffff+1)>>1) + int((v>>32+1)>>1)
}

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

// sadBlock is the sum of absolute differences of two w x h blocks.
func sadBlock(a []uint16, as int, b []uint16, bs, w, h int) int {
	if !useAVX2 || w&7 != 0 || h == 0 {
		return sadBlockGo(a, as, b, bs, w, h)
	}
	_ = a[(h-1)*as+w-1]
	_ = b[(h-1)*bs+w-1]
	return sadAVX2(&a[0], as, &b[0], bs, w, h)
}

// satd8 is the sum of the absolute Hadamard transformed differences of
// two 8x8 blocks, halved twice.
func satd8(a []uint16, as int, b []uint16, bs int) int {
	if !useAVX2 {
		return satd8Go(a, as, b, bs)
	}
	_ = a[7*as+7]
	_ = b[7*bs+7]
	return (satd8AVX2(&a[0], as, &b[0], bs) + 2) >> 2
}
