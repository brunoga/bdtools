//go:build amd64 && !purego

package nn

func cpuidAsm(leaf, sub uint32) (eax, ebx, ecx, edx uint32)
func xgetbvAsm() (eax, edx uint32)

//go:noescape
func kernel6x16AVX2(k int, a, b, c *float32, ldc int)

// useAVX2 reports whether the AVX2 and FMA kernel can be used: the CPU
// has them and the OS saves the YMM state.
var useAVX2 = func() bool {
	if top, _, _, _ := cpuidAsm(0, 0); top < 7 {
		return false
	}
	_, _, ecx, _ := cpuidAsm(1, 0)
	if ecx&(1<<12) == 0 || ecx&(1<<27) == 0 || ecx&(1<<28) == 0 { // FMA, OSXSAVE, AVX
		return false
	}
	if eax, _ := xgetbvAsm(); eax&6 != 6 {
		return false
	}
	_, ebx, _, _ := cpuidAsm(7, 0)
	return ebx&(1<<5) != 0 // AVX2
}()

// kernel is the 6x16 tile kernel: c[r*ldc+j] += sum over p < k of
// a[p*6+r] b[p*16+j].
func kernel(k int, a, b, c []float32, ldc int) {
	if useAVX2 {
		if k > 0 {
			_, _, _ = a[6*k-1], b[16*k-1], c[5*ldc+15]
			kernel6x16AVX2(k, &a[0], &b[0], &c[0], ldc)
		}
		return
	}
	kernelGo(k, a, b, c, ldc)
}
