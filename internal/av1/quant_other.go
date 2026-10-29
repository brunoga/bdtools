//go:build !amd64 || purego

package av1

func quantDZAVX2(coef *int32, q *int32, inv *uint16, n, den, qs int) int { return 0 }

func rankEstAVX2(coef *int32, inv *uint16, n, dsh, qs int) (d int64, nb, eob int) { return 0, 0, 0 }
