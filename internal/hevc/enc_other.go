//go:build !amd64 || purego

package hevc

func sadBlock(a []uint16, as int, b []uint16, bs, w, h int) int {
	return sadBlockGo(a, as, b, bs, w, h)
}

func satd8(a []uint16, as int, b []uint16, bs int) int { return satd8Go(a, as, b, bs) }

func satd16x8(a []uint16, as int, b []uint16, bs int) int {
	return satd8Go(a, as, b, bs) + satd8Go(a[8:], as, b[8:], bs)
}

func forwardTransformFast([]int16, []int32, int, int, *transformScratch) bool { return false }

func inverseTransformFast([]int32, int, int) bool { return false }

func ssdBlock(a []uint16, as int, b []uint16, bs, w, h int) int64 {
	return ssdBlockGo(a, as, b, bs, w, h)
}

func avgBlock(dst []uint16, ds int, a []uint16, as int, b []uint16, bs, w, h int) {
	avgBlockGo(dst, ds, a, as, b, bs, w, h)
}

func saoEO(rec []uint16, r0, rs int, src []uint16, s0, ss, w, h, off0, off1 int, sum, cnt *[4]int64) {
	saoEOGo(rec, r0, rs, src, s0, ss, w, h, off0, off1, sum, cnt)
}
