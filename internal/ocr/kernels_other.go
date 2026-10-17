//go:build !amd64 || purego

package ocr

func axpy(y, x []float32, a float32) {
	x = x[:len(y)]
	for i, v := range x {
		y[i] += a * v
	}
}

func dot(a, b []float32) float32 {
	b = b[:len(a)]
	var s float32
	for i, v := range a {
		s += v * b[i]
	}
	return s
}
