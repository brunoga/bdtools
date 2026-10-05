//go:build !amd64 || purego

package mvc

const useCabacAsm = false

func (s *sliceDec) cabacBlockAsm(cbfCtx, cat, maxNum int, cb *coeffBuf, dst *int16, scale *int32, pos *uint8, shifts int) bool {
	panic("unreachable")
}

func (s *sliceDec) cabacBlocksAsm(a *blockArgs, cbfCur uint32) (cbfOut, nz, ac uint32) {
	panic("unreachable")
}

func (s *sliceDec) cabacMvd(l, x, y, w, h int) (int16, int16) {
	return s.cabacMvdGo(l, x, y, w, h)
}

func (s *sliceDec) predFill(l int, ref int8, x, y, w, h int, mvdx, mvdy int16) {
	s.predFillGo(l, ref, x, y, w, h, mvdx, mvdy)
}

func (s *sliceDec) cabacCBPWith(lumaA, lumaB, chromaA, chromaB int, chroma bool) uint8 {
	return s.cabacCBPGo(lumaA, lumaB, chromaA, chromaB, chroma)
}

func (s *sliceDec) chromaDCBlocks(cbfCur uint32, listCb int) (uint32, uint32) {
	panic("unreachable")
}

func (s *sliceDec) cabacIntraModes(n int) { s.cabacIntraModesGo(n) }

func (s *sliceDec) directFill(mask uint8, ref [2]int8, pmv [2]int, checkCol bool) int {
	panic("unreachable")
}

func (s *sliceDec) pskipFill() {
	m := s.pskipMV()
	s.fillMotion(0, 0, 0, 4, 4, 0, m)
}

func (s *sliceDec) spatialPred(l int) (int8, int) {
	rA, _, _ := s.nb(l, -1, 0)
	rB, _, _ := s.nb(l, 0, -1)
	rC, _, _ := s.nbC(l, 0, 0, 4)
	ref := minPositive(rA, minPositive(rB, rC))
	if ref < 0 {
		return ref, 0
	}
	return ref, packMV(s.mvPred(l, ref, 0, 0, 4, 4))
}
