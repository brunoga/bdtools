package nn

// The matrix products, row major, accumulating into c.

// gemmNT adds a [m, k] times b [n, k] transposed to c [m, n].
func gemmNT(c, a, b []float32, m, k, n int) {
	parallel((m+31)/32, func(blk int) {
		for i := blk * 32; i < min(blk*32+32, m); i++ {
			ai := a[i*k : (i+1)*k]
			ci := c[i*n : (i+1)*n]
			for j := range n {
				ci[j] += dot(ai, b[j*k:(j+1)*k])
			}
		}
	})
}

// gemmNN adds a [m, k] times b [k, n] to c [m, n].
func gemmNN(c, a, b []float32, m, k, n int) {
	parallel(m, func(i int) {
		ci := c[i*n : (i+1)*n]
		for p, av := range a[i*k : (i+1)*k] {
			if av != 0 {
				axpy(ci, b[p*n:(p+1)*n], av)
			}
		}
	})
}

func dot(a, b []float32) float32 {
	b = b[:len(a)]
	var s0, s1, s2, s3 float32
	i := 0
	for ; i+4 <= len(a); i += 4 {
		s0 += a[i] * b[i]
		s1 += a[i+1] * b[i+1]
		s2 += a[i+2] * b[i+2]
		s3 += a[i+3] * b[i+3]
	}
	for ; i < len(a); i++ {
		s0 += a[i] * b[i]
	}
	return (s0 + s1) + (s2 + s3)
}

func axpy(y, x []float32, a float32) {
	x = x[:len(y)]
	for i, v := range x {
		y[i] += a * v
	}
}
