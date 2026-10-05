//go:build amd64 && !purego

package mvc

import "unsafe"

// deblockArgs is the argument block of deblockMBAsm (offsets hard-coded
// in deblock_ctl_amd64.s).
type deblockArgs struct {
	cur, left, top *mbInfo // left/top nil when their edge is not filtered
	sp             *sliceParams
	y, cb, cr      *byte // macroblock top-left in each plane
	stY, stC       int
	refs0, refs1   *int32 // reference id grids at block (0,0) of the MB
	mvs0, mvs1     *int32 // motion vector grids
	st4            int    // grid stride in blocks
	chroma         int
}

//go:noescape
func deblockMBAsm(a *deblockArgs)

// deblockMBFast runs the assembly deblocking control; it reports false
// when the Go path must be used.
func (fc *frameCtx) deblockMBFast(mbX, mbY int, cur *mbInfo, sp *sliceParams, filterLeft, filterTop bool) bool {
	if !useAVX2Asm {
		return false
	}
	pic := fc.pic
	addr := mbY*fc.mbW + mbX
	a := &fc.dba
	a.cur = cur
	a.left, a.top = nil, nil
	if filterLeft {
		a.left = &fc.mbs[addr-1]
	}
	if filterTop {
		a.top = &fc.mbs[addr-fc.mbW]
	}
	a.sp = sp
	a.y = &pic.planes[0][pic.origin[0]+mbY*16*pic.stride[0]+mbX*16]
	co := pic.origin[1] + mbY*8*pic.stride[1] + mbX*8
	a.cb, a.cr = &pic.planes[1][co], &pic.planes[2][co]
	a.stY, a.stC = pic.stride[0], pic.stride[1]
	base4 := mbY*4*fc.mbW*4 + mbX*4
	a.refs0, a.refs1 = &fc.refIDs[0][base4], &fc.refIDs[1][base4]
	a.mvs0 = (*int32)(unsafe.Pointer(&pic.mvs[0][base4]))
	a.mvs1 = (*int32)(unsafe.Pointer(&pic.mvs[1][base4]))
	a.st4 = fc.mbW * 4
	a.chroma = 0
	if fc.sps.chromaFormatIdc != 0 {
		a.chroma = 1
	}
	deblockMBAsm(a)
	return true
}
