package hevc

// Slice types.
const (
	sliceB = 0
	sliceP = 1
	sliceI = 2
)

// predWeight is a reference's explicit weighted prediction: luma's and
// each chroma component's weight and offset, and the shifts.
type predWeight struct {
	lumaWeight, lumaOffset     int
	chromaWeight, chromaOffset [2]int
}

type sliceHeader struct {
	firstSliceInPic    bool
	noOutputOfPrior    bool
	ppsID              int
	dependent          bool
	segmentAddr        int // slice_segment_address, CTB raster
	sliceAddr          int // SliceAddrRs: the independent slice's address
	typ                int
	picOutput          bool
	colourPlane        int
	pocLsb             int
	shortTermRefSPS    bool
	stRPS              stRPS
	stRPSBits          int
	numLongTerm        int
	ltPOC              []int // PocLsbLt, or the full POC when ltMSBPresent
	ltUsed             []bool
	ltMSBPresent       []bool
	temporalMVP        bool
	saoLuma, saoChroma bool
	numRefIdx          [2]int
	refPicListMod      [2]bool
	listEntry          [2][16]int
	mvdL1Zero          bool
	cabacInit          bool
	colFromL0          bool
	colRefIdx          int
	log2WeightDenom    int
	log2WeightDenomC   int
	weights            [2][16]predWeight
	maxMergeCand       int
	qpDelta            int
	cbQPOffset         int
	crQPOffset         int
	deblockingDisabled bool
	betaOffset         int
	tcOffset           int
	loopFilterAcross   bool
	entryPoints        int
	entryOffsets       []int // substreams' sizes, in payload bytes
	dataOffset         int   // the slice data's first byte in the RBSP
	sliceQP            int
	poc                int
}

// parseSliceHeader reads a slice segment header; a dependent segment takes
// what it does not carry from the independent one before (prev).
func (d *Decoder) parseSliceHeader(r *bits, nal *nalUnit, prev *sliceHeader) (*sliceHeader, error) {
	h := &sliceHeader{}
	h.firstSliceInPic = r.flag()
	if nal.typ >= nalBLAWLP && nal.typ <= nalRsvIRAP23 {
		h.noOutputOfPrior = r.flag()
	}
	h.ppsID = r.ue()
	if h.ppsID > 63 || d.ppss[h.ppsID] == nil {
		return nil, errStream
	}
	p := d.ppss[h.ppsID]
	s := d.spss[p.spsID]
	if s == nil {
		return nil, errStream
	}
	if err := p.activate(s, d.lastPPS); err != nil {
		return nil, err
	}
	d.lastPPS = p
	if !h.firstSliceInPic {
		if p.dependentSlices {
			h.dependent = r.flag()
		}
		n := s.ctbW * s.ctbH
		bitsN := 0
		for 1<<bitsN < n {
			bitsN++
		}
		h.segmentAddr = r.u(bitsN)
		if h.segmentAddr >= n {
			return nil, errStream
		}
	}
	if h.dependent {
		if prev == nil {
			return nil, errStream
		}
		// The independent slice's header, with this segment's address.
		addr := h.segmentAddr
		dep := *prev
		h = &dep
		h.dependent = true
		h.firstSliceInPic = false
		h.segmentAddr = addr
	} else {
		h.sliceAddr = h.segmentAddr
		r.skip(p.numExtraSliceHeaderBits)
		h.typ = r.ue()
		if h.typ > 2 {
			return nil, errStream
		}
		h.picOutput = true
		if p.outputFlagPresent {
			h.picOutput = r.flag()
		}
		if s.sepColour {
			h.colourPlane = r.u(2)
		}
		idr := nal.typ == nalIDRWRADL || nal.typ == nalIDRNLP
		if !idr {
			h.pocLsb = r.u(s.log2MaxPOCLsb)
			h.poc = d.computePOC(h, nal, s)
			h.shortTermRefSPS = r.flag()
			if !h.shortTermRefSPS {
				start := r.pos
				var err error
				if h.stRPS, err = parseSTRPS(r, s.stRPS, len(s.stRPS), true); err != nil {
					return nil, err
				}
				h.stRPSBits = r.pos - start
			} else {
				idx := 0
				if len(s.stRPS) > 1 {
					n := 0
					for 1<<n < len(s.stRPS) {
						n++
					}
					idx = r.u(n)
				}
				if idx >= len(s.stRPS) {
					return nil, errStream
				}
				h.stRPS = s.stRPS[idx]
			}
			if s.longTermRefsPresent {
				numSPS := 0
				if len(s.ltPOCLsb) > 0 {
					numSPS = r.ue()
				}
				numPics := r.ue()
				if numSPS > len(s.ltPOCLsb) || numSPS+numPics > 32 {
					return nil, errStream
				}
				h.numLongTerm = numSPS + numPics
				deltaMSB := 0
				for i := range h.numLongTerm {
					var lsb int
					var used bool
					if i < numSPS {
						idx := 0
						if len(s.ltPOCLsb) > 1 {
							n := 0
							for 1<<n < len(s.ltPOCLsb) {
								n++
							}
							idx = r.u(n)
						}
						if idx >= len(s.ltPOCLsb) {
							return nil, errStream
						}
						lsb, used = s.ltPOCLsb[idx], s.ltUsed[idx]
					} else {
						lsb = r.u(s.log2MaxPOCLsb)
						used = r.flag()
					}
					msbPresent := r.flag()
					delta := 0
					if msbPresent {
						delta = r.ue()
					}
					// 7-52: DeltaPocMsbCycleLt accumulates within each group.
					if i == 0 || i == numSPS {
						deltaMSB = delta
					} else {
						deltaMSB += delta
					}
					if msbPresent {
						// 8-5: the full POC.
						maxLsb := 1 << s.log2MaxPOCLsb
						lsb += h.poc - deltaMSB*maxLsb - (h.poc & (maxLsb - 1))
					}
					h.ltPOC = append(h.ltPOC, lsb)
					h.ltUsed = append(h.ltUsed, used)
					h.ltMSBPresent = append(h.ltMSBPresent, msbPresent)
				}
			}
			if s.temporalMVP {
				h.temporalMVP = r.flag()
			}
		}
		if s.sao {
			h.saoLuma = r.flag()
			h.saoChroma = r.flag()
		}
		if h.typ != sliceI {
			h.numRefIdx = p.numRefIdxDefault
			if h.typ != sliceB {
				h.numRefIdx[1] = 0
			}
			if r.flag() { // num_ref_idx_active_override_flag
				h.numRefIdx[0] = r.ue() + 1
				if h.typ == sliceB {
					h.numRefIdx[1] = r.ue() + 1
				}
			}
			if h.numRefIdx[0] > 15 || h.numRefIdx[1] > 15 {
				return nil, errStream
			}
			numPicTotal := 0
			for i := range h.stRPS.num() {
				if h.stRPS.used[i] {
					numPicTotal++
				}
			}
			for _, u := range h.ltUsed {
				if u {
					numPicTotal++
				}
			}
			if p.listsModification && numPicTotal > 1 {
				n := 0
				for 1<<n < numPicTotal {
					n++
				}
				for l := range 2 {
					if l == 1 && h.typ != sliceB {
						break
					}
					h.refPicListMod[l] = r.flag()
					if h.refPicListMod[l] {
						for i := range h.numRefIdx[l] {
							h.listEntry[l][i] = r.u(n)
						}
					}
				}
			}
			if h.typ == sliceB {
				h.mvdL1Zero = r.flag()
			}
			if p.cabacInitPresent {
				h.cabacInit = r.flag()
			}
			if h.temporalMVP {
				h.colFromL0 = true
				if h.typ == sliceB {
					h.colFromL0 = r.flag()
				}
				l := 1
				if h.colFromL0 {
					l = 0
				}
				if h.numRefIdx[l] > 1 {
					h.colRefIdx = r.ue()
					if h.colRefIdx >= h.numRefIdx[l] {
						return nil, errStream
					}
				}
			}
			if p.weightedPred && h.typ == sliceP || p.weightedBipred && h.typ == sliceB {
				if err := parsePredWeights(r, h, s); err != nil {
					return nil, err
				}
			}
			h.maxMergeCand = 5 - r.ue()
			if h.maxMergeCand < 1 || h.maxMergeCand > 5 {
				return nil, errStream
			}
		}
		h.qpDelta = r.se()
		if p.sliceChromaQPOffsets {
			h.cbQPOffset = r.se()
			h.crQPOffset = r.se()
		}
		override := false
		if p.deblockingOverride {
			override = r.flag()
		}
		h.deblockingDisabled = p.deblockingDisabled
		h.betaOffset, h.tcOffset = p.betaOffset, p.tcOffset
		if override {
			h.deblockingDisabled = r.flag()
			if !h.deblockingDisabled {
				h.betaOffset = r.se() * 2
				h.tcOffset = r.se() * 2
			}
		}
		h.loopFilterAcross = p.loopFilterAcrossSlices
		if p.loopFilterAcrossSlices && (h.saoLuma || h.saoChroma || !h.deblockingDisabled) {
			h.loopFilterAcross = r.flag()
		}
		h.sliceQP = p.initQP + h.qpDelta
		if h.sliceQP < -s.qpBdOffset || h.sliceQP > 51 {
			return nil, errStream
		}
	}
	if p.tiles || p.entropySync {
		h.entryPoints = r.ue()
		if h.entryPoints > 0 {
			bitsN := r.ue() + 1
			if bitsN > 32 {
				return nil, errStream
			}
			h.entryOffsets = make([]int, h.entryPoints)
			for i := range h.entryPoints {
				h.entryOffsets[i] = r.u(bitsN) + 1
			}
		}
	}
	if p.sliceHeaderExtension {
		n := r.ue()
		r.skip(8 * n)
	}
	// byte_alignment(): a one, then zeros.
	if r.u(1) != 1 {
		return nil, errStream
	}
	r.byteAlign()
	if r.left() < 0 {
		return nil, errShort
	}
	h.dataOffset = r.pos >> 3
	return h, nil
}

func parsePredWeights(r *bits, h *sliceHeader, s *sps) error {
	h.log2WeightDenom = r.ue()
	if h.log2WeightDenom > 7 {
		return errStream
	}
	h.log2WeightDenomC = h.log2WeightDenom + r.se()
	if h.log2WeightDenomC < 0 || h.log2WeightDenomC > 7 {
		return errStream
	}
	lists := 1
	if h.typ == sliceB {
		lists = 2
	}
	for l := range lists {
		n := h.numRefIdx[l]
		var lumaFlag, chromaFlag [16]bool
		// Pictures of another layer or the same POC cannot be told apart
		// here; every entry carries its flags (single layer, no
		// pps_curr_pic_ref).
		for i := range n {
			lumaFlag[i] = r.flag()
		}
		for i := range n {
			chromaFlag[i] = r.flag()
		}
		for i := range n {
			w := &h.weights[l][i]
			w.lumaWeight = 1 << h.log2WeightDenom
			w.chromaWeight = [2]int{1 << h.log2WeightDenomC, 1 << h.log2WeightDenomC}
			if lumaFlag[i] {
				w.lumaWeight += r.se()
				w.lumaOffset = r.se() << (s.bitDepth - 8)
			}
			if chromaFlag[i] {
				for j := range 2 {
					dw := r.se()
					w.chromaWeight[j] += dw
					dOff := r.se()
					// 7-56, with wpOffsetHalfRangeC 128 (no high precision
					// offsets), scaled to the bit depth (8-252).
					const half = 128
					off := (half - ((half * w.chromaWeight[j]) >> h.log2WeightDenomC)) + dOff
					w.chromaOffset[j] = min(max(off, -half), half-1) << (s.bitDepthC - 8)
				}
			}
		}
	}
	return nil
}
