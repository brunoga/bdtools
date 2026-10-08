package mvc

// Slice types (slice_type % 5).
const (
	sliceP  = 0
	sliceB  = 1
	sliceI  = 2
	sliceSP = 3
	sliceSI = 4
)

type nalHeader struct {
	refIdc     int
	typ        int
	idr        bool
	hasMVC     bool // header carries nal_unit_header_mvc_extension
	viewID     int
	anchor     bool
	interView  bool
	temporalID int
	priorityID int
}

// parseNALHeader parses the NAL header (1 or 4 bytes) and returns the size
// of the header in bytes.
func parseNALHeader(b []byte) (h nalHeader, n int, err error) {
	if len(b) < 1 || b[0]&0x80 != 0 {
		return h, 0, errInvalid
	}
	h.refIdc = int(b[0]>>5) & 3
	h.typ = int(b[0] & 31)
	h.idr = h.typ == nalSliceIDR
	h.interView = true
	n = 1
	if h.typ == nalPrefix || h.typ == nalSliceExt {
		if len(b) < 4 {
			return h, 0, errInvalid
		}
		if b[1]&0x80 != 0 {
			return h, 4, errUnsupported // SVC
		}
		v := uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
		h.hasMVC = true
		h.idr = (v>>22)&1 == 0 // non_idr_flag
		h.priorityID = int(v>>16) & 63
		h.viewID = int(v>>6) & 1023
		h.temporalID = int(v>>3) & 7
		h.anchor = (v>>2)&1 != 0
		h.interView = (v>>1)&1 != 0
		n = 4
	}
	return h, n, nil
}

type refPicMod struct {
	idc int
	val uint32
}

type predWeight struct {
	lumaLog2   int
	chromaLog2 int
	// [list][refIdx] weights/offsets; [0]=Y,[1]=Cb,[2]=Cr
	weight     [2][maxRefsPerList][3]int16
	offset     [2][maxRefsPerList][3]int16
	lumaFlag   [2][maxRefsPerList]bool
	chromaFlag [2][maxRefsPerList]bool
	// identity[l][i] is set when the weights of the reference are the
	// defaults (2^logWD, offset 0), which the weighting formulas reduce to
	// the unweighted prediction for.
	identity [2][maxRefsPerList]bool
}

type mmcoOp struct {
	op               int
	diffPicNums      uint32
	longTermPicNum   uint32
	longTermFrameIdx uint32
	maxLongTermIdxP1 uint32
}

type sliceHeader struct {
	nal                 nalHeader
	firstMb             int
	sliceType           int
	sliceTypeRaw        int
	ppsID               int
	frameNum            int
	idrPicID            int
	pocLsb              int
	deltaPocBottom      int32
	deltaPoc            [2]int32
	redundantPicCnt     int
	directSpatial       bool
	numRefIdxActive     [2]int
	refPicMods          [2][]refPicMod
	refPicModFlag       [2]bool
	pw                  predWeight
	hasWeights          bool // explicit weighted prediction in use
	noOutputOfPriorPics bool
	longTermReference   bool
	adaptiveMarking     bool
	mmco                []mmcoOp
	cabacInitIdc        int
	qpDelta             int
	disableDeblock      int
	alphaOffset         int // FilterOffsetA
	betaOffset          int // FilterOffsetB
	hasMMCO5            bool

	sps *sps
	pps *pps
}

func parseSliceHeader(br *bitReader, h *sliceHeader, nal nalHeader, spsTab *[maxSPS]*sps,
	subsetTab *[maxSPS]*sps, ppsTab *[maxPPS]*pps) error {
	h.nal = nal
	h.firstMb = int(br.ue())
	st := br.ue()
	if st > 9 {
		return errInvalid
	}
	h.sliceTypeRaw = int(st)
	h.sliceType = int(st % 5)
	pid := br.ue()
	if pid >= maxPPS || ppsTab[pid] == nil {
		return errInvalid
	}
	p := ppsTab[pid]
	h.ppsID = int(pid)
	var s *sps
	if nal.typ == nalSliceExt {
		s = subsetTab[p.spsID]
	} else {
		s = spsTab[p.spsID]
	}
	if s == nil {
		return errInvalid
	}
	h.sps, h.pps = s, p
	if h.sliceType == sliceSP || h.sliceType == sliceSI {
		return errUnsupported
	}
	if s.chromaFormatIdc == 3 || s.chromaFormatIdc == 2 || s.bitDepthLuma != 8 || s.bitDepthChroma != 8 || s.transformBypass {
		return errUnsupported
	}
	h.frameNum = int(br.u(s.log2MaxFrameNum))
	if !s.frameMbsOnly {
		if br.flag() { // field_pic_flag
			return errUnsupported
		}
		if s.mbaff {
			return errUnsupported
		}
	}
	if nal.idr {
		h.idrPicID = int(br.ue())
	}
	h.deltaPocBottom = 0
	h.deltaPoc = [2]int32{}
	if s.pocType == 0 {
		h.pocLsb = int(br.u(s.log2MaxPocLsb))
		if p.bottomFieldPicOrder {
			h.deltaPocBottom = br.se()
		}
	} else if s.pocType == 1 && !s.deltaPicOrderAlwaysZero {
		h.deltaPoc[0] = br.se()
		if p.bottomFieldPicOrder {
			h.deltaPoc[1] = br.se()
		}
	}
	h.redundantPicCnt = 0
	if p.redundantPicCntPresent {
		h.redundantPicCnt = int(br.ue())
	}
	if h.sliceType == sliceB {
		h.directSpatial = br.flag()
	}
	h.numRefIdxActive = p.numRefIdxActive
	if h.sliceType == sliceP || h.sliceType == sliceB {
		if br.flag() {
			h.numRefIdxActive[0] = int(br.ue()) + 1
			if h.sliceType == sliceB {
				h.numRefIdxActive[1] = int(br.ue()) + 1
			}
		}
		if h.numRefIdxActive[0] > 32 || h.numRefIdxActive[1] > 32 {
			return errInvalid
		}
	}
	if h.sliceType != sliceB {
		h.numRefIdxActive[1] = 0
	}
	if h.sliceType == sliceI {
		h.numRefIdxActive[0] = 0
	}
	// ref_pic_list_modification / ref_pic_list_mvc_modification
	for l := 0; l < 2; l++ {
		h.refPicMods[l] = h.refPicMods[l][:0]
		h.refPicModFlag[l] = false
		if (l == 0 && h.sliceType != sliceI) || (l == 1 && h.sliceType == sliceB) {
			if br.flag() {
				h.refPicModFlag[l] = true
				for {
					idc := int(br.ue())
					if idc == 3 {
						break
					}
					if idc > 5 || (idc > 3 && nal.typ != nalSliceExt) || len(h.refPicMods[l]) > 64 {
						return errInvalid
					}
					h.refPicMods[l] = append(h.refPicMods[l], refPicMod{idc, br.ue()})
				}
			}
		}
	}
	h.hasWeights = false
	if (p.weightedPred && h.sliceType == sliceP) || (p.weightedBipredIdc == 1 && h.sliceType == sliceB) {
		h.hasWeights = true
		parsePredWeight(br, h, s.chromaFormatIdc != 0)
	}
	h.adaptiveMarking = false
	h.mmco = h.mmco[:0]
	h.hasMMCO5 = false
	h.noOutputOfPriorPics = false
	h.longTermReference = false
	if nal.refIdc != 0 {
		if nal.idr {
			h.noOutputOfPriorPics = br.flag()
			h.longTermReference = br.flag()
		} else {
			h.adaptiveMarking = br.flag()
			if h.adaptiveMarking {
				for {
					op := int(br.ue())
					if op == 0 {
						break
					}
					if op > 6 || len(h.mmco) > 66 {
						return errInvalid
					}
					m := mmcoOp{op: op}
					if op == 1 || op == 3 {
						m.diffPicNums = br.ue()
					}
					if op == 2 {
						m.longTermPicNum = br.ue()
					}
					if op == 3 || op == 6 {
						m.longTermFrameIdx = br.ue()
					}
					if op == 4 {
						m.maxLongTermIdxP1 = br.ue()
					}
					if op == 5 {
						h.hasMMCO5 = true
					}
					h.mmco = append(h.mmco, m)
				}
			}
		}
	}
	h.cabacInitIdc = 0
	if p.cabac && h.sliceType != sliceI {
		h.cabacInitIdc = int(br.ue())
		if h.cabacInitIdc > 2 {
			return errInvalid
		}
	}
	h.qpDelta = int(br.se())
	if p.picInitQP+h.qpDelta < 0 || p.picInitQP+h.qpDelta > 51 {
		return errInvalid
	}
	h.disableDeblock = 0
	h.alphaOffset, h.betaOffset = 0, 0
	if p.deblockingControl {
		h.disableDeblock = int(br.ue())
		if h.disableDeblock > 2 {
			return errInvalid
		}
		if h.disableDeblock != 1 {
			h.alphaOffset = int(br.se()) * 2
			h.betaOffset = int(br.se()) * 2
			if h.alphaOffset < -12 || h.alphaOffset > 12 || h.betaOffset < -12 || h.betaOffset > 12 {
				return errInvalid
			}
		}
	}
	if br.overrun() {
		return errInvalid
	}
	return nil
}

func parsePredWeight(br *bitReader, h *sliceHeader, chroma bool) {
	pw := &h.pw
	pw.lumaLog2 = int(br.ue() & 7)
	if chroma {
		pw.chromaLog2 = int(br.ue() & 7)
	}
	nl := 1
	if h.sliceType == sliceB {
		nl = 2
	}
	for l := 0; l < nl; l++ {
		for i := 0; i < h.numRefIdxActive[l]; i++ {
			pw.weight[l][i] = [3]int16{int16(1 << pw.lumaLog2), int16(1 << pw.chromaLog2), int16(1 << pw.chromaLog2)}
			pw.offset[l][i] = [3]int16{}
			pw.lumaFlag[l][i] = br.flag()
			if pw.lumaFlag[l][i] {
				pw.weight[l][i][0] = int16(br.se())
				pw.offset[l][i][0] = int16(br.se())
			}
			pw.chromaFlag[l][i] = false
			if chroma {
				pw.chromaFlag[l][i] = br.flag()
				if pw.chromaFlag[l][i] {
					for c := 1; c < 3; c++ {
						pw.weight[l][i][c] = int16(br.se())
						pw.offset[l][i][c] = int16(br.se())
					}
				}
			}
			pw.identity[l][i] = pw.weight[l][i] == [3]int16{int16(1 << pw.lumaLog2), int16(1 << pw.chromaLog2), int16(1 << pw.chromaLog2)} &&
				pw.offset[l][i] == [3]int16{}
		}
	}
}
