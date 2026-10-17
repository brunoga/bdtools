//go:build amd64 && !purego

package ocr

func cpuidAsm(leaf, sub uint32) (eax, ebx, ecx, edx uint32)
func xgetbvAsm() (eax, edx uint32)

// useAVX2 reports whether the AVX2 and FMA kernels can be used: the CPU
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

//go:noescape
func axpyAVX2(y, x *float32, n int, a float32)

//go:noescape
func dotAVX2(a, b *float32, n int) float32

func axpy(y, x []float32, a float32) {
	x = x[:len(y)]
	n := len(y) &^ 15
	if useAVX2 && n > 0 {
		axpyAVX2(&y[0], &x[0], n, a)
	} else {
		n = 0
	}
	for i := n; i < len(y); i++ {
		y[i] += a * x[i]
	}
}

func dot(a, b []float32) float32 {
	b = b[:len(a)]
	n := len(a) &^ 15
	var s float32
	if useAVX2 && n > 0 {
		s = dotAVX2(&a[0], &b[0], n)
	} else {
		n = 0
	}
	for i := n; i < len(a); i++ {
		s += a[i] * b[i]
	}
	return s
}
