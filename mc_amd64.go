//go:build amd64 && !purego

package mvc

//go:noescape
func mcCopyAsm(dst *byte, ds int, src *byte, ss int, w, h int)

//go:noescape
func mcHAsm(dst *byte, ds int, src *byte, ss int, w, h int)

//go:noescape
func mcVAsm(dst *byte, ds int, src *byte, ss int, w, h int)

//go:noescape
func mcJAsm(dst *byte, ds int, src *byte, ss int, w, h int)

//go:noescape
func mcAvgAsm(dst *byte, ds int, a *byte, as int, b *byte, bs int, w, h int)

//go:noescape
func mcW1Asm(dst *byte, ds int, src *byte, ss int, w, h int, wt, rnd, logWD, off int)

//go:noescape
func mcW2Asm(dst *byte, ds int, a *byte, as int, b *byte, bs int, w, h int, w01, rnd, sh, off int)

//go:noescape
func chromaMCAsm(dst *byte, ds int, src *byte, ss int, w, h int, wab, wcd int)

func xgetbvAsm() (eax, edx uint32)

// useAVX2Asm reports whether the AVX2 assembly kernels can be used: the
// CPU supports AVX2 and the OS saves the YMM state.
var useAVX2Asm = func() bool {
	if max, _, _, _ := cpuidAsm(0, 0); max < 7 {
		return false
	}
	_, _, ecx, _ := cpuidAsm(1, 0)
	if ecx&(1<<27) == 0 || ecx&(1<<28) == 0 { // OSXSAVE, AVX
		return false
	}
	if eax, _ := xgetbvAsm(); eax&6 != 6 {
		return false
	}
	_, ebx, _, _ := cpuidAsm(7, 0)
	return ebx&(1<<5) != 0 // AVX2
}()

func hpelH(out []byte, ds int, src []byte, so, ss, w, h int) {
	if !useAVX2Asm {
		hpelHGeneric(out, ds, src, so, ss, w, h)
		return
	}
	mcHAsm(&out[0], ds, &src[so], ss, w, h)
}

func hpelV(out []byte, ds int, src []byte, so, ss, w, h int) {
	if !useAVX2Asm {
		hpelVGeneric(out, ds, src, so, ss, w, h)
		return
	}
	mcVAsm(&out[0], ds, &src[so], ss, w, h)
}

func hpelJ(out []byte, ds int, tmp []int16, src []byte, so, ss, w, h int) {
	if !useAVX2Asm {
		hpelJGeneric(out, ds, tmp, src, so, ss, w, h)
		return
	}
	mcJAsm(&out[0], ds, &src[so], ss, w, h)
}

func avgInto(dst []byte, ds int, a []byte, as int, b []byte, bs int, w, h int) {
	if !useAVX2Asm {
		avgIntoGeneric(dst, ds, a, as, b, bs, w, h)
		return
	}
	mcAvgAsm(&dst[0], ds, &a[0], as, &b[0], bs, w, h)
}

func storeAvg(dst []byte, ds int, a, b []byte, as, w, h int) {
	if !useAVX2Asm {
		storeAvgGeneric(dst, ds, a, b, as, w, h)
		return
	}
	mcAvgAsm(&dst[0], ds, &a[0], as, &b[0], as, w, h)
}

func storeWeighted1(dst []byte, ds int, a []byte, as, w, h int, wt, of int32, logWD uint) {
	if !useAVX2Asm {
		storeWeighted1Generic(dst, ds, a, as, w, h, wt, of, logWD)
		return
	}
	rnd := 0
	if logWD >= 1 {
		rnd = 1 << (logWD - 1)
	}
	mcW1Asm(&dst[0], ds, &a[0], as, w, h, int(wt), rnd, int(logWD), int(of))
}

func storeWeighted2(dst []byte, ds int, a, b []byte, as, w, h int, w0, w1, o0, o1 int32, logWD uint) {
	if !useAVX2Asm {
		storeWeighted2Generic(dst, ds, a, b, as, w, h, w0, w1, o0, o1, logWD)
		return
	}
	w01 := int(uint16(w0)) | int(uint16(w1))<<16
	mcW2Asm(&dst[0], ds, &a[0], as, &b[0], as, w, h, w01, 1<<logWD, int(logWD+1), int((o0+o1+1)>>1))
}

func copyBlock(dst []byte, ds int, src []byte, ss, w, h int) {
	if !useAVX2Asm {
		copyBlockGeneric(dst, ds, src, ss, w, h)
		return
	}
	mcCopyAsm(&dst[0], ds, &src[0], ss, w, h)
}

func chromaMC(dst []byte, ds int, ref *picture, c int, cx, cy, w, h int) {
	if !useAVX2Asm || w < 4 {
		chromaMCGeneric(dst, ds, ref, c, cx, cy, w, h)
		return
	}
	ix, iy := cx>>3, cy>>3
	fx, fy := cx&7, cy&7
	cw, ch := ref.width/2, ref.height/2
	if ix < -w-1 {
		ix = -w - 1
	} else if ix > cw {
		ix = cw
	}
	if iy < -h-1 {
		iy = -h - 1
	} else if iy > ch {
		iy = ch
	}
	src := ref.planes[c]
	so := ref.origin[c] + iy*ref.stride[c] + ix
	wab := (8-fx)*(8-fy) | fx*(8-fy)<<8
	wcd := (8-fx)*fy | fx*fy<<8
	chromaMCAsm(&dst[0], ds, &src[so], ref.stride[c], w, h, wab, wcd)
}

// chromaMC2 predicts both chroma components with one clamp and setup.
func chromaMC2(cb, cr []byte, ds int, ref *picture, cx, cy, w, h int) {
	if !useAVX2Asm || w < 4 {
		chromaMC2Generic(cb, cr, ds, ref, cx, cy, w, h)
		return
	}
	ix, iy := cx>>3, cy>>3
	fx, fy := cx&7, cy&7
	cw, ch := ref.width/2, ref.height/2
	if ix < -w-1 {
		ix = -w - 1
	} else if ix > cw {
		ix = cw
	}
	if iy < -h-1 {
		iy = -h - 1
	} else if iy > ch {
		iy = ch
	}
	so := ref.origin[1] + iy*ref.stride[1] + ix
	wab := (8-fx)*(8-fy) | fx*(8-fy)<<8
	wcd := (8-fx)*fy | fx*fy<<8
	if ref.stride[1] != ref.stride[2] {
		chromaMCAsm(&cb[0], ds, &ref.planes[1][so], ref.stride[1], w, h, wab, wcd)
		chromaMCAsm(&cr[0], ds, &ref.planes[2][so], ref.stride[2], w, h, wab, wcd)
		return
	}
	chromaMC2Asm(&cb[0], &cr[0], ds, &ref.planes[1][so], &ref.planes[2][so], ref.stride[1], w, h, wab, wcd)
}

//go:noescape
func chromaMC2Asm(cb, cr *byte, ds int, scb, scr *byte, ss int, w, h int, wab, wcd int)
