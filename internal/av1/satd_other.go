//go:build !amd64 || purego

package av1

func sadBlock(a []uint16, as int, b []uint16, bs, w, h int) int {
	return sadBlockGo(a, as, b, bs, w, h)
}

func satd8(a []uint16, as int, b []uint16, bs int) int { return satd8Go(a, as, b, bs) }

func satd16x8(a []uint16, as int, b []uint16, bs int) int {
	return satd8Go(a, as, b, bs) + satd8Go(a[8:], as, b[8:], bs)
}
