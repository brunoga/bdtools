//go:build !amd64 || purego

package mvc

func pred4x4(pl []byte, off, stride, mode int, a intraAvail) {
	pred4x4Generic(pl, off, stride, mode, a)
}
func pred8x8L(pl []byte, off, stride, mode int, a intraAvail) {
	pred8x8LGeneric(pl, off, stride, mode, a)
}
func predPlane(pl []byte, off, stride, w, h int) { predPlaneGeneric(pl, off, stride, w, h) }
func predChroma(pl []byte, off, stride, mode int, haveTop, haveLeft bool) {
	predChromaGeneric(pl, off, stride, mode, haveTop, haveLeft)
}
