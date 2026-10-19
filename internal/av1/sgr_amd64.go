//go:build amd64 && !purego

package av1

//go:noescape
func sgrEvenAVX2(out, u, aUp, aDn, bUp, bDn *int32, n int)

//go:noescape
func sgrOddAVX2(out, u, a, b *int32, n int)

//go:noescape
func sgrCrossAVX2(out, u, a, b *int32, bw, n int)

// sgrRows is sgrFilter's row i of a pass to the first of out's samples
// not done (a multiple of 8) with AVX2, k being A[i][0]'s index.
func sgrRows(out, u, a, b []int32, i, k, bw, pass int) int {
	n := len(out) &^ 7
	if !useAVX2 || n == 0 {
		return 0
	}
	switch {
	case pass == 0 && i&1 == 0:
		_, _ = a[k+bw+n], b[k+bw+n]
		sgrEvenAVX2(&out[0], &u[0], &a[k-bw], &a[k+bw], &b[k-bw], &b[k+bw], n)
	case pass == 0:
		_, _ = a[k+n], b[k+n]
		sgrOddAVX2(&out[0], &u[0], &a[k], &b[k], n)
	default:
		_, _ = a[k+bw+n], b[k+bw+n]
		sgrCrossAVX2(&out[0], &u[0], &a[k], &b[k], bw, n)
	}
	return n
}
