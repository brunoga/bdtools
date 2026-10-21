//go:build !amd64 || purego

package nn

// useAVX2 is false where there is no AVX2 kernel.
var useAVX2 = false

func kernel(k int, a, b, c []float32, ldc int) { kernelGo(k, a, b, c, ldc) }
