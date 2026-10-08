package hevc

import "slices"

// sliceDec decodes a slice segment's data.
type sliceDec struct {
	d   *Decoder
	h   *sliceHeader
	p   *pps
	s   *sps
	pic *picture
	ps  *picState
	c   cabac

	sliceIdx int

	ctbAddrRS, ctbAddrTS int

	// The quantization group.
	qpY              int
	qpYPrev          int
	cuQPDeltaCoded   bool
	cuQPDeltaVal     int
	qgX, qgY         int
	qgPred           int  // qPY_PRED of the quantization group
	lastCUQpY        int  // the QP of the last CU decoded
	firstQGInTask    bool // the next QG starts a slice, tile or CTB row (WPP)
	log2MinCuQPDelta int

	// The coding unit.
	cuX, cuY, cuLog2 int
	predMode         int
	partMode         int
	bypass           bool
	pcm              bool
	intraModeC       int
	intraModes       [4]int
	mergeFlag        bool
	maxTrafoDepth    int
	intraSplit       bool

	coeffs [32 * 32]int32
	pred   predScratch

	substreams []int // where each substream after the first starts
	substream  int

	data   []byte // the slice segment data
	endTS  int    // the tile scan address past the segment's last CTB
	refIdx int16  // the picture's refPOC entry of the slice
	// The slice's reference lists.
	refList [2][]*picture
	refIsLT [2][]bool
	chain   *sliceChain
}

// sliceChain is what a slice's dependent segments continue from.
type sliceChain struct {
	lastQPY int
	dsCtx   [numContexts]uint8
	wppCtx  [numContexts]uint8 // with tiles: the row above's, in the tile
}

func (d *Decoder) decodeSlice(h *sliceHeader, n *nalUnit, r *bits) error {
	p := d.ppss[h.ppsID]
	if p.sps != d.sps {
		return errStream // a PPS of another SPS within a picture
	}
	ps := &d.pic
	ps.slices = append(ps.slices, h)
	sd := &sliceDec{d: d, h: h, p: p, s: d.sps, pic: d.cur, ps: ps, sliceIdx: len(ps.slices) - 1}
	if h.typ != sliceI {
		if !d.buildRefLists(h) {
			return errStream
		}
		for l := range 2 {
			sd.refList[l] = slices.Clone(ps.refList[l])
			sd.refIsLT[l] = slices.Clone(ps.refIsLT[l])
		}
	}
	d.recordRefs(sd)
	sd.refIdx = int16(len(d.cur.refPOC) - 1)
	sd.log2MinCuQPDelta = sd.s.log2Ctb - p.diffCuQPDeltaDepth
	// Each substream's start in the slice data, from the entry points.
	start := n.escaped(h.dataOffset)
	sd.substreams = sd.substreams[:0]
	for _, o := range h.entryOffsets {
		start += o
		sd.substreams = append(sd.substreams, n.unescaped(start)-h.dataOffset)
	}
	sd.data = n.rbsp[h.dataOffset:]
	d.pending = append(d.pending, sd)
	return nil
}

// recordRefs keeps the slice's reference POCs on the picture, for the
// pictures that will take motion from it.
func (d *Decoder) recordRefs(sd *sliceDec) {
	var pocs [2][16]int32
	var lts [2][16]bool
	for l := range 2 {
		for i, p := range d.pic.refList[l] {
			if sd.h.typ == sliceI || i >= sd.h.numRefIdx[l] {
				break
			}
			pocs[l][i] = int32(p.poc)
			lts[l][i] = d.pic.refIsLT[l][i]
		}
	}
	d.cur.refPOC = append(d.cur.refPOC, pocs)
	d.cur.refLT = append(d.cur.refLT, lts)
}

func (sd *sliceDec) initType() int {
	switch {
	case sd.h.typ == sliceI:
		return 0
	case sd.h.typ == sliceP && sd.h.cabacInit, sd.h.typ == sliceB && !sd.h.cabacInit:
		return 2
	}
	return 1
}

// decode parses the slice segment data and reconstructs its CTUs.
func (sd *sliceDec) decode(data []byte) error {
	if sd.wppParallel(data) {
		return sd.decodeWPP(data)
	}
	h, p, s, ps := sd.h, sd.p, sd.s, sd.ps
	sd.ctbAddrRS = h.segmentAddr
	sd.ctbAddrTS = p.rsToTS[sd.ctbAddrRS]
	if h.dependent && sd.ctbAddrRS > 0 && sd.ctbAddrTS > 0 {
		// A dependent segment continues the slice's QP prediction.
		sd.qpY = sd.chain.lastQPY
		sd.lastCUQpY = sd.chain.lastQPY
	} else {
		sd.qpY = h.sliceQP
	}
	sd.c.init(data, 0)
	sd.firstQGInTask = !h.dependent
	nCtb := s.ctbW * s.ctbH
	first := true
	for {
		if sd.ctbAddrTS >= sd.endTS {
			return errStream
		}
		sd.ctbAddrRS = p.tsToRS[sd.ctbAddrTS]
		ctbX, ctbY := sd.ctbAddrRS%s.ctbW, sd.ctbAddrRS/s.ctbW
		tileStart := sd.ctbAddrTS == 0 || p.tileID[sd.ctbAddrTS] != p.tileID[sd.ctbAddrTS-1]
		rowStart := p.entropySync && (ctbX == 0 || p.tileID[sd.ctbAddrTS] != p.tileID[p.rsToTS[sd.ctbAddrRS-1]])
		// Context initialization (9.3.1).
		if tileStart || rowStart {
			sd.firstQGInTask = true
		}
		switch {
		case tileStart:
			sd.c.initContexts(sd.initType(), h.sliceQP)
		case rowStart:
			x0, y0 := ctbX<<s.log2Ctb, ctbY<<s.log2Ctb
			if sd.available(x0, y0, x0+s.ctbSize, y0-s.ctbSize) {
				if p.tiles {
					sd.c.ctx = sd.chain.wppCtx
				} else {
					sd.c.ctx = ps.wppRowCtx[ctbY-1]
				}
			} else {
				sd.c.initContexts(sd.initType(), h.sliceQP)
			}
		case first && h.dependent:
			sd.c.ctx = sd.chain.dsCtx
		case first:
			sd.c.initContexts(sd.initType(), h.sliceQP)
		}
		first = false
		if err := sd.ctu(ctbX, ctbY); err != nil {
			return err
		}
		ps.ctbDecoded[sd.ctbAddrRS] = true
		// WPP storage after a tile row's second CTB (or its only one).
		if p.entropySync {
			col := ctbX
			for i := 0; i < len(p.colBd)-1; i++ {
				if ctbX >= p.colBd[i] && ctbX < p.colBd[i+1] {
					col = ctbX - p.colBd[i]
				}
			}
			if col == 1 || (col == 0 && p.tileID[sd.ctbAddrTS] != p.tileID[p.rsToTS[min(sd.ctbAddrRS+1, nCtb-1)]]) {
				if p.tiles {
					sd.chain.wppCtx = sd.c.ctx
				} else {
					ps.wppRowCtx[ctbY] = sd.c.ctx
				}
			}
		}
		end := sd.c.terminate() == 1
		sd.ctbAddrTS++
		if end {
			if p.dependentSlices {
				sd.chain.dsCtx = sd.c.ctx
			}
			sd.chain.lastQPY = sd.lastCUQpY
			return nil
		}
		if sd.ctbAddrTS >= sd.endTS {
			return errStream
		}
		next := p.tsToRS[sd.ctbAddrTS]
		if p.tiles && p.tileID[sd.ctbAddrTS] != p.tileID[sd.ctbAddrTS-1] ||
			p.entropySync && (next%s.ctbW == 0 || p.tileID[sd.ctbAddrTS] != p.tileID[p.rsToTS[next-1]]) {
			if sd.c.terminate() != 1 { // end_of_subset_one_bit
				return errStream
			}
			next := sd.c.alignedNext()
			if sd.substream < len(sd.substreams) {
				next = sd.substreams[sd.substream]
			}
			sd.substream++
			sd.c.init(sd.c.data, next)
		}
	}
}

// available reports whether the block at (xN, yN) is available for
// prediction at (xCurr, yCurr) (6.4.1): in the picture, decoded before,
// in the same slice and tile.
func (sd *sliceDec) available(xCurr, yCurr, xN, yN int) bool {
	s := sd.s
	if xN < 0 || yN < 0 || xN >= s.width || yN >= s.height {
		return false
	}
	p := sd.p
	zn := p.minTbZs[(yN>>s.log2MinTb)*s.minTbW+xN>>s.log2MinTb]
	zc := p.minTbZs[(yCurr>>s.log2MinTb)*s.minTbW+xCurr>>s.log2MinTb]
	if zn > zc {
		return false
	}
	ctbN := (yN>>s.log2Ctb)*s.ctbW + xN>>s.log2Ctb
	ctbC := (yCurr>>s.log2Ctb)*s.ctbW + xCurr>>s.log2Ctb
	// The slice first: another slice's CTBs may be being decoded.
	if sd.ps.slices[sd.ps.ctbSlice[ctbN]].sliceAddr != sd.h.sliceAddr {
		return false
	}
	if !sd.ps.ctbDecoded[ctbN] && ctbN != ctbC {
		return false
	}
	return p.tileID[p.rsToTS[ctbN]] == p.tileID[p.rsToTS[ctbC]]
}

// blk4 is the index of the 4x4 block at luma (x, y).
func (sd *sliceDec) blk4(x, y int) int { return (y>>2)*sd.ps.w4 + x>>2 }

func (sd *sliceDec) ctu(ctbX, ctbY int) error {
	s := sd.s
	x0, y0 := ctbX<<s.log2Ctb, ctbY<<s.log2Ctb
	if sd.h.saoLuma || sd.h.saoChroma {
		sd.saoSyntax(ctbX, ctbY)
	}
	return sd.codingQuadtree(x0, y0, s.log2Ctb, 0)
}

func (sd *sliceDec) saoSyntax(rx, ry int) {
	s, p, h, c := sd.s, sd.p, sd.h, &sd.c
	addr := sd.ctbAddrRS
	params := &sd.ps.sao[addr]
	if rx > 0 {
		leftInSlice := addr > h.sliceAddr
		leftInTile := p.tileID[sd.ctbAddrTS] == p.tileID[p.rsToTS[addr-1]]
		if leftInSlice && leftInTile && c.decision(ctxSaoMergeFlag) == 1 {
			*params = sd.ps.sao[addr-1]
			return
		}
	}
	if ry > 0 {
		up := addr - s.ctbW
		upInSlice := up >= h.sliceAddr
		upInTile := p.tileID[sd.ctbAddrTS] == p.tileID[p.rsToTS[up]]
		if upInSlice && upInTile && c.decision(ctxSaoMergeFlag) == 1 {
			*params = sd.ps.sao[up]
			return
		}
	}
	for ci := range 3 {
		if ci == 0 && !h.saoLuma || ci > 0 && !h.saoChroma {
			params[ci] = saoParams{}
			continue
		}
		sp := &params[ci]
		if ci == 2 {
			sp.typ = params[1].typ
			sp.class = params[1].class
		} else {
			sp.typ = 0
			if c.decision(ctxSaoTypeIdx) == 1 {
				sp.typ = 1
				if c.bypass() == 1 {
					sp.typ = 2
				}
			}
		}
		if sp.typ == 0 {
			continue
		}
		depth := s.bitDepth
		if ci > 0 {
			depth = s.bitDepthC
		}
		cMax := 1<<(min(depth, 10)-5) - 1
		var abs [4]int
		for i := range 4 {
			for abs[i] < cMax && c.bypass() == 1 {
				abs[i]++
			}
		}
		shift := depth - min(depth, 10)
		if sp.typ == 1 {
			for i := range 4 {
				if abs[i] != 0 && c.bypass() == 1 {
					abs[i] = -abs[i]
				}
			}
			sp.band = uint8(c.bypassBits(5))
			for i := range 4 {
				sp.offset[i] = int16(abs[i] << shift)
			}
		} else {
			if ci == 0 {
				sp.class = uint8(c.bypassBits(2))
			}
			if ci == 1 {
				sp.class = uint8(c.bypassBits(2))
			}
			sp.offset = [4]int16{int16(abs[0] << shift), int16(abs[1] << shift), int16(-abs[2] << shift), int16(-abs[3] << shift)}
		}
	}
}

func (sd *sliceDec) codingQuadtree(x0, y0, log2Cb, depth int) error {
	s, p := sd.s, sd.p
	size := 1 << log2Cb
	var split bool
	if x0+size <= s.width && y0+size <= s.height && log2Cb > s.log2MinCb {
		inc := 0
		if sd.available(x0, y0, x0-1, y0) && int(sd.ps.ctDepth[(y0>>s.log2MinCb)*s.minCbW+(x0-1)>>s.log2MinCb]) > depth {
			inc++
		}
		if sd.available(x0, y0, x0, y0-1) && int(sd.ps.ctDepth[((y0-1)>>s.log2MinCb)*s.minCbW+x0>>s.log2MinCb]) > depth {
			inc++
		}
		split = sd.c.decision(ctxSplitCodingUnitFlag+inc) == 1
	} else {
		split = log2Cb > s.log2MinCb
	}
	if p.cuQPDeltaEnabled && log2Cb >= sd.log2MinCuQPDelta {
		sd.cuQPDeltaCoded = false
		sd.cuQPDeltaVal = 0
		sd.startQG(x0, y0)
	}
	if split {
		half := size >> 1
		for i := range 4 {
			x, y := x0+(i&1)*half, y0+(i>>1)*half
			if x < s.width && y < s.height {
				if err := sd.codingQuadtree(x, y, log2Cb-1, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return sd.codingUnit(x0, y0, log2Cb, depth)
}

// startQG starts a quantization group at (x, y): its QP prediction
// (8.6.1).
func (sd *sliceDec) startQG(x, y int) {
	s := sd.s
	sd.qgX, sd.qgY = x, y
	if sd.firstQGInTask {
		sd.qpYPrev = sd.h.sliceQP
	} else {
		sd.qpYPrev = sd.lastCUQpY
	}
	predA := sd.qpYPrev
	ctbAddr := (y>>s.log2Ctb)*s.ctbW + x>>s.log2Ctb
	if sd.available(x, y, x-1, y) && ((y>>s.log2Ctb)*s.ctbW+(x-1)>>s.log2Ctb) == ctbAddr {
		predA = int(sd.ps.qpY[sd.blk4(x-1, y)])
	}
	predB := sd.qpYPrev
	if sd.available(x, y, x, y-1) && (((y-1)>>s.log2Ctb)*s.ctbW+x>>s.log2Ctb) == ctbAddr {
		predB = int(sd.ps.qpY[sd.blk4(x, y-1)])
	}
	sd.qgPred = (predA + predB + 1) >> 1
	sd.qpY = sd.qgPred // until a delta is coded
}

func (sd *sliceDec) codingUnit(x0, y0, log2Cb, depth int) error {
	s, p, h, c := sd.s, sd.p, sd.h, &sd.c
	ps := sd.ps
	size := 1 << log2Cb
	sd.cuX, sd.cuY, sd.cuLog2 = x0, y0, log2Cb
	sd.bypass = false
	sd.pcm = false
	sd.partMode = part2Nx2N
	sd.intraSplit = false
	sd.mergeFlag = false
	if !p.cuQPDeltaEnabled {
		sd.qpY = h.sliceQP
	}
	if p.transquantBypass {
		sd.bypass = c.decision(ctxCuTransquantBypassFlag) == 1
	}
	skip := false
	if h.typ != sliceI {
		inc := 0
		if sd.available(x0, y0, x0-1, y0) && ps.predMode[sd.blk4(x0-1, y0)] == modeSkip {
			inc++
		}
		if sd.available(x0, y0, x0, y0-1) && ps.predMode[sd.blk4(x0, y0-1)] == modeSkip {
			inc++
		}
		skip = c.decision(ctxSkipFlag+inc) == 1
	}
	// Record the CU's depth for split flags' contexts.
	minCb := 1 << s.log2MinCb
	for y := y0; y < y0+size && y < s.height; y += minCb {
		for x := x0; x < x0+size && x < s.width; x += minCb {
			ps.ctDepth[(y>>s.log2MinCb)*s.minCbW+x>>s.log2MinCb] = uint8(depth)
		}
	}
	if skip {
		sd.predMode = modeSkip
		sd.setPredMode(x0, y0, size, size, modeSkip)
		if err := sd.predictionUnit(x0, y0, size, size, 0, true); err != nil {
			return err
		}
		sd.finishCU(x0, y0, log2Cb)
		return nil
	}
	sd.predMode = modeInter
	if h.typ != sliceI {
		if c.decision(ctxPredModeFlag) == 1 {
			sd.predMode = modeIntra
		}
	} else {
		sd.predMode = modeIntra
	}
	if sd.predMode != modeIntra || log2Cb == s.log2MinCb {
		sd.partMode = sd.partModeSyntax(log2Cb)
		sd.intraSplit = sd.predMode == modeIntra && sd.partMode == partNxN
	}
	sd.setPredMode(x0, y0, size, size, sd.predMode)
	if sd.predMode == modeIntra {
		if sd.partMode == part2Nx2N && s.pcm && log2Cb >= s.log2MinPCM && log2Cb <= s.log2MaxPCM {
			sd.pcm = c.terminate() == 1
		}
		if sd.pcm {
			if err := sd.pcmSample(x0, y0, log2Cb); err != nil {
				return err
			}
			// A PCM CU's neighbours predict from DC (8.4.2).
			for y := y0; y < y0+size; y += 4 {
				for x := x0; x < x0+size; x += 4 {
					ps.intraMode[sd.blk4(x, y)] = 1
				}
			}
			sd.finishCU(x0, y0, log2Cb)
			return nil
		}
		sd.intraModeSyntax(x0, y0, log2Cb)
	} else {
		half, quarter := size/2, size/4
		var err error
		switch sd.partMode {
		case part2Nx2N:
			err = sd.predictionUnit(x0, y0, size, size, 0, false)
		case part2NxN:
			if err = sd.predictionUnit(x0, y0, size, half, 0, false); err == nil {
				err = sd.predictionUnit(x0, y0+half, size, half, 1, false)
			}
		case partNx2N:
			if err = sd.predictionUnit(x0, y0, half, size, 0, false); err == nil {
				err = sd.predictionUnit(x0+half, y0, half, size, 1, false)
			}
		case part2NxnU:
			if err = sd.predictionUnit(x0, y0, size, quarter, 0, false); err == nil {
				err = sd.predictionUnit(x0, y0+quarter, size, size*3/4, 1, false)
			}
		case part2NxnD:
			if err = sd.predictionUnit(x0, y0, size, size*3/4, 0, false); err == nil {
				err = sd.predictionUnit(x0, y0+size*3/4, size, quarter, 1, false)
			}
		case partnLx2N:
			if err = sd.predictionUnit(x0, y0, quarter, size, 0, false); err == nil {
				err = sd.predictionUnit(x0+quarter, y0, size*3/4, size, 1, false)
			}
		case partnRx2N:
			if err = sd.predictionUnit(x0, y0, size*3/4, size, 0, false); err == nil {
				err = sd.predictionUnit(x0+size*3/4, y0, quarter, size, 1, false)
			}
		case partNxN:
			for i := range 4 {
				if err = sd.predictionUnit(x0+(i&1)*half, y0+(i>>1)*half, half, half, i, false); err != nil {
					break
				}
			}
		}
		if err != nil {
			return err
		}
	}
	rootCbf := true
	if sd.predMode != modeIntra && (sd.partMode != part2Nx2N || !sd.mergeFlag) {
		rootCbf = c.decision(ctxNoResidualDataFlag) == 1
	}
	if rootCbf {
		if sd.predMode == modeIntra {
			sd.maxTrafoDepth = s.maxTrDepthIntra
			if sd.intraSplit {
				sd.maxTrafoDepth++
			}
		} else {
			sd.maxTrafoDepth = s.maxTrDepthInter
		}
		if err := sd.transformTree(x0, y0, x0, y0, log2Cb, 0, 0, [2]bool{true, true}); err != nil {
			return err
		}
	} else if sd.predMode == modeIntra {
		// Not reachable: intra CUs always code the tree.
		return errStream
	}
	sd.finishCU(x0, y0, log2Cb)
	return nil
}

// finishCU records the CU's QP, filter exemption and the edge on its
// sides for the deblocking.
func (sd *sliceDec) finishCU(x0, y0, log2Cb int) {
	s, ps := sd.s, sd.ps
	size := 1 << log2Cb
	sd.lastCUQpY = sd.qpY
	sd.firstQGInTask = false
	noFilter := sd.bypass || sd.pcm && s.pcmLoopFilterDisabled
	for y := y0; y < y0+size && y < s.height; y += 4 {
		for x := x0; x < x0+size && x < s.width; x += 4 {
			i := sd.blk4(x, y)
			ps.qpY[i] = int8(sd.qpY)
			ps.noFilter[i] = noFilter
		}
	}
	for y := y0; y < y0+size && y < s.height; y += 4 {
		ps.tuEdgeV[sd.blk4(x0, y)] |= 3
	}
	for x := x0; x < x0+size && x < s.width; x += 4 {
		ps.tuEdgeH[sd.blk4(x, y0)] |= 3
	}
}

func (sd *sliceDec) setPredMode(x0, y0, w, h, mode int) {
	s, ps := sd.s, sd.ps
	for y := y0; y < y0+h && y < s.height; y += 4 {
		for x := x0; x < x0+w && x < s.width; x += 4 {
			ps.predMode[sd.blk4(x, y)] = uint8(mode)
		}
	}
}

func (sd *sliceDec) partModeSyntax(log2Cb int) int {
	s, c := sd.s, &sd.c
	if c.decision(ctxPartMode) == 1 {
		return part2Nx2N
	}
	if log2Cb == s.log2MinCb {
		if sd.predMode == modeIntra {
			return partNxN
		}
		if c.decision(ctxPartMode+1) == 1 {
			return part2NxN
		}
		if log2Cb == 3 {
			return partNx2N
		}
		if c.decision(ctxPartMode+2) == 1 {
			return partNx2N
		}
		return partNxN
	}
	if !s.amp {
		if c.decision(ctxPartMode+1) == 1 {
			return part2NxN
		}
		return partNx2N
	}
	if c.decision(ctxPartMode+1) == 1 {
		if c.decision(ctxPartMode+3) == 1 {
			return part2NxN
		}
		if c.bypass() == 1 {
			return part2NxnD
		}
		return part2NxnU
	}
	if c.decision(ctxPartMode+3) == 1 {
		return partNx2N
	}
	if c.bypass() == 1 {
		return partnRx2N
	}
	return partnLx2N
}

// intraModeSyntax reads the CU's intra prediction modes and derives them
// (8.4.2, 8.4.3).
func (sd *sliceDec) intraModeSyntax(x0, y0, log2Cb int) {
	s, c, ps := sd.s, &sd.c, sd.ps
	n := 1
	pb := 1 << log2Cb
	if sd.partMode == partNxN {
		n, pb = 4, pb/2
	}
	var prevFlag [4]bool
	for i := range n {
		prevFlag[i] = c.decision(ctxPrevIntraLumaPredFlag) == 1
	}
	for i := range n {
		x, y := x0+(i&1)*pb, y0+(i>>1)*pb
		mpmIdx, rem := 0, 0
		if prevFlag[i] {
			for mpmIdx < 2 && c.bypass() == 1 {
				mpmIdx++
			}
		} else {
			rem = c.bypassBits(5)
		}
		// Candidates from the left and above (8.4.2).
		candA, candB := 1, 1
		if sd.available(x, y, x-1, y) {
			if i := sd.blk4(x-1, y); ps.predMode[i] == modeIntra {
				candA = int(ps.intraMode[i])
			}
		}
		if y-1 >= (y>>s.log2Ctb)<<s.log2Ctb && sd.available(x, y, x, y-1) {
			if i := sd.blk4(x, y-1); ps.predMode[i] == modeIntra {
				candB = int(ps.intraMode[i])
			}
		}
		var cand [3]int
		if candA == candB {
			if candA < 2 {
				cand = [3]int{0, 1, 26}
			} else {
				cand = [3]int{candA, 2 + (candA+29)%32, 2 + (candA-2+1)%32}
			}
		} else {
			cand[0], cand[1] = candA, candB
			switch {
			case candA != 0 && candB != 0:
				cand[2] = 0
			case candA != 1 && candB != 1:
				cand[2] = 1
			default:
				cand[2] = 26
			}
		}
		mode := 0
		if prevFlag[i] {
			mode = cand[mpmIdx]
		} else {
			if cand[0] > cand[1] {
				cand[0], cand[1] = cand[1], cand[0]
			}
			if cand[0] > cand[2] {
				cand[0], cand[2] = cand[2], cand[0]
			}
			if cand[1] > cand[2] {
				cand[1], cand[2] = cand[2], cand[1]
			}
			mode = rem
			for j := range 3 {
				if mode >= cand[j] {
					mode++
				}
			}
		}
		sd.intraModes[i] = mode
		for yy := y; yy < y+pb; yy += 4 {
			for xx := x; xx < x+pb; xx += 4 {
				if xx < s.width && yy < s.height {
					ps.intraMode[sd.blk4(xx, yy)] = uint8(mode)
				}
			}
		}
	}
	if n == 1 {
		sd.intraModes[1], sd.intraModes[2], sd.intraModes[3] = sd.intraModes[0], sd.intraModes[0], sd.intraModes[0]
	}
	// intra_chroma_pred_mode, for 4:2:0 once (8.4.3), from the first PB.
	chroma := 4
	if c.decision(ctxIntraChromaPredMode) == 1 {
		chroma = c.bypassBits(2)
	}
	luma := sd.intraModes[0]
	switch chroma {
	case 4:
		sd.intraModeC = luma
	default:
		m := [4]int{0, 26, 10, 1}[chroma]
		if m == luma {
			m = 34
		}
		sd.intraModeC = m
	}
}

// pcmSample reads a PCM CU's samples (7.3.8.7).
func (sd *sliceDec) pcmSample(x0, y0, log2Cb int) error {
	s, c := sd.s, &sd.c
	r := &bits{b: c.data, pos: c.alignedNext() * 8}
	size := 1 << log2Cb
	pic := sd.pic
	for y := range size {
		row := pic.y[(y0+y)*pic.strideY+x0:]
		for x := range size {
			row[x] = uint16(r.u(s.pcmBits) << (s.bitDepth - s.pcmBits))
		}
	}
	for _, pl := range [2][]uint16{pic.cb, pic.cr} {
		for y := range size / 2 {
			row := pl[(y0/2+y)*pic.strideC+x0/2:]
			for x := range size / 2 {
				row[x] = uint16(r.u(s.pcmBitsC) << (s.bitDepthC - s.pcmBitsC))
			}
		}
	}
	if r.left() < 0 {
		return errShort
	}
	c.init(c.data, r.pos>>3)
	sd.ps.markCbf(sd, x0, y0, size, false)
	return nil
}

func (ps *picState) markCbf(sd *sliceDec, x0, y0, size int, v bool) {
	for y := y0; y < y0+size && y < sd.s.height; y += 4 {
		for x := x0; x < x0+size && x < sd.s.width; x += 4 {
			ps.cbfLuma[sd.blk4(x, y)] = v
		}
	}
}

// transformTree parses a transform tree node (7.3.8.8), reconstructing its
// transform units; cbfC are the parent's chroma cbfs.
func (sd *sliceDec) transformTree(x0, y0, xBase, yBase, log2Size, depth, blkIdx int, parentCbf [2]bool) error {
	s, c := sd.s, &sd.c
	var split bool
	if log2Size <= s.log2MaxTb && log2Size > s.log2MinTb && depth < sd.maxTrafoDepth && (!sd.intraSplit || depth != 0) {
		split = c.decision(ctxSplitTransformFlag+5-log2Size) == 1
	} else {
		interSplit := s.maxTrDepthInter == 0 && sd.predMode == modeInter && sd.partMode != part2Nx2N && depth == 0
		split = log2Size > s.log2MaxTb || sd.intraSplit && depth == 0 || interSplit
	}
	cbf := [2]bool{false, false}
	if log2Size > 2 {
		for ci := range 2 {
			if depth == 0 || parentCbf[ci] {
				cbf[ci] = c.decision(ctxCbfCbCr+depth) == 1
			}
		}
	} else {
		cbf = parentCbf // 4:2:0 4x4 luma: chroma is coded with the 4th block, from the parent's flags
		if depth == 0 {
			cbf = [2]bool{false, false}
		}
	}
	if split {
		half := 1 << (log2Size - 1)
		for i := range 4 {
			if err := sd.transformTree(x0+(i&1)*half, y0+(i>>1)*half, x0, y0, log2Size-1, depth+1, i, cbf); err != nil {
				return err
			}
		}
		return nil
	}
	cbfLuma := true
	if sd.predMode == modeIntra || depth != 0 || cbf[0] || cbf[1] {
		cbfLuma = c.decision(ctxCbfLuma+boolInt(depth == 0)) == 1
	}
	return sd.transformUnit(x0, y0, xBase, yBase, log2Size, depth, blkIdx, cbfLuma, cbf)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
