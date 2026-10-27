//go:build amd64 && !purego

package av1

//go:noescape
func sseAVX2(a *uint16, as int, b *uint16, bs int, w, h int) int64

// sse is the sum of squared differences of a w by h block of a (stride
// as) and of b (stride bs); 0 when w or h is not positive (a block past
// the picture's edge).
func sse(a []uint16, as int, b []uint16, bs int, w, h int) int64 {
	if w <= 0 || h <= 0 {
		return 0 // past the picture's edge
	}
	if !useAVX2 || w&7 != 0 {
		return sseGo(a, as, b, bs, w, h)
	}
	_, _ = a[(h-1)*as+w-1], b[(h-1)*bs+w-1]
	return sseAVX2(&a[0], as, &b[0], bs, w, h)
}
