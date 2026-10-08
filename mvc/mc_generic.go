//go:build !amd64 || purego

package mvc

func hpelH(out []byte, ds int, src []byte, so, ss, w, h int) {
	hpelHGeneric(out, ds, src, so, ss, w, h)
}
func hpelV(out []byte, ds int, src []byte, so, ss, w, h int) {
	hpelVGeneric(out, ds, src, so, ss, w, h)
}
func hpelJ(out []byte, ds int, tmp []int16, src []byte, so, ss, w, h int) {
	hpelJGeneric(out, ds, tmp, src, so, ss, w, h)
}
func avgInto(dst []byte, ds int, a []byte, as int, b []byte, bs int, w, h int) {
	avgIntoGeneric(dst, ds, a, as, b, bs, w, h)
}
func storeAvg(dst []byte, ds int, a, b []byte, as, w, h int) {
	storeAvgGeneric(dst, ds, a, b, as, w, h)
}
func storeWeighted1(dst []byte, ds int, a []byte, as, w, h int, wt, of int32, logWD uint) {
	storeWeighted1Generic(dst, ds, a, as, w, h, wt, of, logWD)
}
func storeWeighted2(dst []byte, ds int, a, b []byte, as, w, h int, w0, w1, o0, o1 int32, logWD uint) {
	storeWeighted2Generic(dst, ds, a, b, as, w, h, w0, w1, o0, o1, logWD)
}
func chromaMC(dst []byte, ds int, ref *picture, c int, cx, cy, w, h int) {
	chromaMCGeneric(dst, ds, ref, c, cx, cy, w, h)
}
func copyBlock(dst []byte, ds int, src []byte, ss, w, h int) {
	copyBlockGeneric(dst, ds, src, ss, w, h)
}
func chromaMC2(cb, cr []byte, ds int, ref *picture, cx, cy, w, h int) {
	chromaMC2Generic(cb, cr, ds, ref, cx, cy, w, h)
}
