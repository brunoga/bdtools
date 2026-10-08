//go:build !amd64 || purego

package mvc

func filterLuma(pl []byte, off, step, along int, bs *[4]uint8, alpha, beta int32, indexA int) {
	filterLumaGeneric(pl, off, step, along, bs, alpha, beta, indexA)
}

func filterChroma2(cb, cr []byte, off, step, along int, bs *[4]uint8, alpha, beta [2]int32, indexA [2]int) {
	filterChroma2Generic(cb, cr, off, step, along, bs, alpha, beta, indexA)
}
