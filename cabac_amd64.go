//go:build amd64 && !purego

package mvc

import "unsafe"

// cabacResidAsm decodes coded_block_flag (if cbf >= 0) and a residual
// block with maxNum = end+1 coefficients, reading and updating the engine
// state in e. tab is sigLastCtx[cat]. With dst != nil the levels are
// dequantized and stored to dst (see cabacBlockDQ) instead of cb.level.
// It requires BMI2 and LZCNT. The field offsets of cabac (off 0, rng 8,
// buf 16, pos 40) are hard-coded.
//
//go:noescape
func cabacResidAsm(e *cabac, ctx *uint8, tab *[2]uint16, cb *coeffBuf, cbf, end, absBase, gt1Max int,
	dst *int16, scale *int32, pos *uint8, shifts int) bool

//go:noescape
func cabacBlocksAsm(e *cabac, ctx *uint8, tab *[2]uint16, cb *coeffBuf, absBase, gt1Max, end, catBase int,
	desc *uint64, n, cbp int, cbfA, cbfB, cbfCur int, dst *int16, scale0, scale1 *int32, pos *uint8,
	sh0, sh1 int) (cbfOut, nz, ac int)

//go:noescape
func cabacMvdAsm(e *cabac, ctx *uint8, grid *[2]uint8, stride, avail, w, h int) (mx, my int)

//go:noescape
func mvPredFillAsm(refs *int8, mvs *int32, stride, avail, ref, x, y, w, h, mvdx, mvdy, fill int) int

//go:noescape
func directFillAsm(refs0, refs1 *int8, mvs0, mvs1 *int32, stride int, colRefs0, colRefs1 *int8,
	colMvs0, colMvs1 *int32, mask, infer, ref0, ref1, pmv0, pmv1, checkCol int) int

//go:noescape
func cabacIntraModesAsm(e *cabac, ctx *uint8, n int, prev *bool, rem *int8)

func (s *sliceDec) cabacIntraModes(n int) {
	if !useCabacAsm {
		s.cabacIntraModesGo(n)
		return
	}
	cabacIntraModesAsm(&s.cab, &s.ctx[0], n, &s.mb.prevFlag[0], &s.mb.remMode[0])
}

// directFill fills the direct-predicted 8x8 blocks of mask and returns the
// uniformity bits (see directFillAsm).
func (s *sliceDec) directFill(mask uint8, ref [2]int8, pmv [2]int, checkCol bool) int {
	base := s.idx4(0, 0)
	pic, col := s.pic, s.colPic
	infer, cc := 0, 0
	if s.h.sps.direct8x8Inference {
		infer = 1
	}
	if checkCol {
		cc = 1
	}
	return directFillAsm(&pic.refs[0][base], &pic.refs[1][base],
		(*int32)(unsafe.Pointer(&pic.mvs[0][base])), (*int32)(unsafe.Pointer(&pic.mvs[1][base])), s.fc.mbW*4,
		&col.refs[0][base], &col.refs[1][base],
		(*int32)(unsafe.Pointer(&col.mvs[0][base])), (*int32)(unsafe.Pointer(&col.mvs[1][base])),
		int(mask), infer, int(ref[0]), int(ref[1]), pmv[0], pmv[1], cc)
}

// cabacMvd decodes an mvd pair for the partition with top-left 4x4 block
// (x,y) and size (w,h) in 4x4 units, storing absolute values for context
// derivation.
func (s *sliceDec) cabacMvd(l, x, y, w, h int) (int16, int16) {
	if !useCabacAsm {
		return s.cabacMvdGo(l, x, y, w, h)
	}
	avail := 0
	if x > 0 || s.availA {
		avail = 1
	}
	if y > 0 || s.availB {
		avail |= 2
	}
	mx, my := cabacMvdAsm(&s.cab, &s.ctx[0], &s.fc.mvd[l][s.idx4(x, y)], s.fc.mbW*4, avail, w, h)
	return int16(mx), int16(my)
}

// availBits packs the neighbour availability for the assembly routines.
func (s *sliceDec) availBits() int {
	a := 0
	if s.availA {
		a |= 1
	}
	if s.availB {
		a |= 2
	}
	if s.availC {
		a |= 4
	}
	if s.availD {
		a |= 8
	}
	return a
}

// predFill derives the motion vector of a partition from its predictor
// and mvd and fills the partition's refs and mvs.
func (s *sliceDec) predFill(l int, ref int8, x, y, w, h int, mvdx, mvdy int16) {
	if !useCabacAsm {
		s.predFillGo(l, ref, x, y, w, h, mvdx, mvdy)
		return
	}
	base := s.idx4(0, 0)
	mvPredFillAsm(&s.pic.refs[l][base], (*int32)(unsafe.Pointer(&s.pic.mvs[l][base])), s.fc.mbW*4, s.availBits(),
		int(ref), x, y, w, h, int(mvdx), int(mvdy), 1)
}

//go:noescape
func cabacChromaDCAsm(e *cabac, ctx *uint8, cb *coeffBuf, cbfA, cbfB, cbfCur int, coef *int16,
	scale0, scale1, sh0, sh1 int) (cbfOut, nz int)

//go:noescape
func cabacCBPAsm(e *cabac, ctx *uint8, lumaA, lumaB, chromaA, chromaB, chroma int) int

func cpuidAsm(leaf, sub uint32) (eax, ebx, ecx, edx uint32)

func (s *sliceDec) cabacBlocksAsm(a *blockArgs, cbfCur uint32) (cbfOut, nz, ac uint32) {
	maxNum := [6]int{16, 15, 16, 4, 15, 64}[a.cat]
	gt1 := 4
	if a.cat == catChromaDC {
		gt1 = 3
	}
	s1 := a.scale[0]
	if a.scale[1] != nil {
		s1 = a.scale[1]
	}
	o, n, c := cabacBlocksAsm(&s.cab, &s.ctx[0], &sigLastCtx[a.cat&7][0], &s.cb, absBaseTab[a.cat], gt1, maxNum-1,
		85+cbfCatOffset[min(a.cat, 4)], &a.desc[0], len(a.desc), a.cbp, int(a.cbfA), int(a.cbfB), int(cbfCur),
		&a.dst[0], &a.scale[0][0], &s1[0], &a.pos[0], a.shifts[0], a.shifts[1])
	return uint32(o), uint32(n), uint32(c)
}

func (s *sliceDec) cabacCBPWith(lumaA, lumaB, chromaA, chromaB int, chroma bool) uint8 {
	if useCabacAsm {
		c := 0
		if chroma {
			c = 1
		}
		return uint8(cabacCBPAsm(&s.cab, &s.ctx[0], lumaA, lumaB, chromaA, chromaB, c))
	}
	return s.cabacCBPGo(lumaA, lumaB, chromaA, chromaB, chroma)
}

// useCabacAsm reports whether the assembly residual decoder can be used.
var useCabacAsm = func() bool {
	if max, _, _, _ := cpuidAsm(0, 0); max < 7 {
		return false
	}
	_, ebx, _, _ := cpuidAsm(7, 0)
	if ebx&(1<<8) == 0 { // BMI2
		return false
	}
	if ext, _, _, _ := cpuidAsm(0x80000000, 0); ext < 0x80000001 {
		return false
	}
	_, _, ecx, _ := cpuidAsm(0x80000001, 0)
	return ecx&(1<<5) != 0 // LZCNT (ABM)
}()

// cabacBlockAsm runs the assembly decoder for cabacBlock.
func (s *sliceDec) cabacBlockAsm(cbfCtx, cat, maxNum int, cb *coeffBuf, dst *int16, scale *int32, pos *uint8, shifts int) bool {
	gt1 := 4
	if cat == catChromaDC {
		gt1 = 3
	}
	return cabacResidAsm(&s.cab, &s.ctx[0], &sigLastCtx[cat&7][0], cb, cbfCtx, maxNum-1, absBaseTab[cat], gt1,
		dst, scale, pos, shifts)
}

// chromaDCBlocks decodes both chroma DC blocks into mb.coefC and returns
// the updated cbf mask and the nonzero block bits.
func (s *sliceDec) chromaDCBlocks(cbfCur uint32, listCb int) (uint32, uint32) {
	cbfA, cbfB := s.cbfMasks()
	q0, q1 := s.qpc[0], s.qpc[1]
	o, nz := cabacChromaDCAsm(&s.cab, &s.ctx[0], &s.cb, int(cbfA), int(cbfB), int(cbfCur), &s.mb.coefC[0][0],
		int(s.dq4[listCb][q0%6][0]), int(s.dq4[listCb+1][q1%6][0]), q0/6, q1/6)
	return uint32(o), uint32(nz)
}

// pskipFill derives the P_Skip motion vector and fills the macroblock's
// list 0 grid.
func (s *sliceDec) pskipFill() {
	base := s.idx4(0, 0)
	mvPredFillAsm(&s.pic.refs[0][base], (*int32)(unsafe.Pointer(&s.pic.mvs[0][base])), s.fc.mbW*4, s.availBits(),
		0, 0, 0, 4, 4, 0, 0, 3)
}

// spatialPred returns the spatial direct reference index of list l and
// the packed predictor for it (0 when the index is negative).
func (s *sliceDec) spatialPred(l int) (int8, int) {
	base := s.idx4(0, 0)
	r := mvPredFillAsm(&s.pic.refs[l][base], (*int32)(unsafe.Pointer(&s.pic.mvs[l][base])), s.fc.mbW*4, s.availBits(),
		-2, 0, 0, 4, 4, 0, 0, 0)
	return int8(uint8(r)), int(uint32(r >> 8))
}
