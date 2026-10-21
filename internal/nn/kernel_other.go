//go:build !amd64 || purego

package nn

func kernel(k int, a, b, c []float32, ldc int) { kernelGo(k, a, b, c, ldc) }
