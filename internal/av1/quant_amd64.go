//go:build amd64 && !purego

package av1

//go:noescape
func quantDZAVX2(coef *int32, q *int32, inv *uint16, n, den, qs int) int
