//go:build amd64 && !purego

package mvc

//go:noescape
func predNxNAsm(p *byte, stride, n, mode, avail int)

//go:noescape
func predPlaneAsm(p *byte, stride, w, h int)

func availBitsIntra(a intraAvail) int {
	v := 0
	if a.left {
		v |= 1
	}
	if a.top {
		v |= 2
	}
	if a.topRight {
		v |= 4
	}
	if a.topLeft {
		v |= 8
	}
	return v
}

func pred4x4(pl []byte, off, stride, mode int, a intraAvail) {
	if !useAVX2Asm {
		pred4x4Generic(pl, off, stride, mode, a)
		return
	}
	predNxNAsm(&pl[off], stride, 4, mode, availBitsIntra(a))
}

func pred8x8L(pl []byte, off, stride, mode int, a intraAvail) {
	if !useAVX2Asm {
		pred8x8LGeneric(pl, off, stride, mode, a)
		return
	}
	predNxNAsm(&pl[off], stride, 8, mode, availBitsIntra(a))
}

func predPlane(pl []byte, off, stride, w, h int) {
	if !useAVX2Asm {
		predPlaneGeneric(pl, off, stride, w, h)
		return
	}
	predPlaneAsm(&pl[off], stride, w, h)
}

//go:noescape
func predChromaAsm(p *byte, stride, mode, avail int)

func predChroma(pl []byte, off, stride, mode int, haveTop, haveLeft bool) {
	if !useAVX2Asm || mode == 3 {
		predChromaGeneric(pl, off, stride, mode, haveTop, haveLeft)
		return
	}
	avail := 0
	if haveLeft {
		avail = 1
	}
	if haveTop {
		avail |= 2
	}
	predChromaAsm(&pl[off], stride, mode, avail)
}
