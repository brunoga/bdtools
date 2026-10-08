//go:build amd64 && !purego

package mvc

import "unsafe"

//go:noescape
func beginMBAsm(refs0, refs1 *int8, mvs0, mvs1 *mv, mvd0, mvd1 *[2]uint8, i4 *int8, stride int, mvd bool)

// clearMBGrids resets the per-block grids of the current macroblock.
func (s *sliceDec) clearMBGrids(base, st int) {
	pic := s.pic
	beginMBAsm(&pic.refs[0][base], &pic.refs[1][base], &pic.mvs[0][base], &pic.mvs[1][base],
		&s.fc.mvd[0][base], &s.fc.mvd[1][base], &s.fc.i4[base], st, s.cabacOn)
}

//go:noescape
func fillMotionMBAsm(refs *int8, mvs *int32, stride, ref, mv int)

//go:noescape
func fillIDsAsm(out0, out1 *int32, stride, id0, id1 int)

// fillMotionMB fills the whole macroblock of list l with one reference
// and motion vector.
func (s *sliceDec) fillMotionMB(l int, ref int8, m mv) {
	base := s.idx4(0, 0)
	fillMotionMBAsm(&s.pic.refs[l][base], (*int32)(unsafe.Pointer(&s.pic.mvs[l][base])), s.fc.mbW*4, int(ref),
		int(uint16(m.x))|int(m.y)<<16)
}

// fillIDs records one reference id per list for the whole macroblock.
func (s *sliceDec) fillIDs(base int, id0, id1 int32) {
	fillIDsAsm(&s.fc.refIDs[0][base], &s.fc.refIDs[1][base], s.fc.mbW*4, int(id0), int(id1))
}
