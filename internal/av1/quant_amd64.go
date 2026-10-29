//go:build amd64 && !purego

package av1

//go:noescape
func quantDZAVX2(coef *int32, q *int32, inv *uint16, n, den, qs int) int

//go:noescape
func rankEstAVX2(coef *int32, inv *uint16, n, dsh, qs int) (d int64, nb, eob int)
