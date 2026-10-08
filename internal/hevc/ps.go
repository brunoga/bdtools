package hevc

import "slices"

// stRPS is a short-term reference picture set: the POC deltas of the
// pictures before (negative, nearest first) and after, and which the
// current picture uses.
type stRPS struct {
	numNeg   int
	numPos   int
	deltaPOC [32]int
	used     [32]bool
}

func (s *stRPS) num() int { return s.numNeg + s.numPos }

type scalingList struct {
	// lists[sizeID][matrixID]: 4x4 (16 entries), 8x8 and up (64, the 8x8
	// grid upsampled for 16x16 and 32x32); dc for 16x16 and 32x32.
	lists [4][6][64]uint8
	dc    [2][6]uint8
}

type sps struct {
	id            int
	profile       int // general_profile_idc
	chromaFormat  int
	sepColour     bool
	width, height int
	confWin       [4]int // left, right, top, bottom, in luma samples
	bitDepth      int
	bitDepthC     int
	log2MaxPOCLsb int
	maxDecPicBuf  int // of the highest sub-layer
	maxReorder    int
	maxLatency    int // SpsMaxLatencyPictures, 0 when not limited

	log2MinCb       int
	log2Ctb         int
	log2MinTb       int
	log2MaxTb       int
	maxTrDepthInter int
	maxTrDepthIntra int

	scalingListEnabled bool
	scaling            scalingList

	amp                   bool
	sao                   bool
	pcm                   bool
	pcmBits               int
	pcmBitsC              int
	log2MinPCM            int
	log2MaxPCM            int
	pcmLoopFilterDisabled bool

	stRPS []stRPS

	longTermRefsPresent bool
	ltPOCLsb            []int
	ltUsed              []bool

	temporalMVP          bool
	strongIntraSmoothing bool

	// VUI
	primaries, transfer, matrix int
	fullRange                   bool
	timeScale, unitsInTick      int

	// Derived.
	ctbSize        int
	ctbW, ctbH     int // picture size in CTBs
	minCbW, minCbH int
	minTbW, minTbH int
	qpBdOffset     int
	qpBdOffsetC    int
}

type pps struct {
	id, spsID int

	dependentSlices         bool
	outputFlagPresent       bool
	numExtraSliceHeaderBits int
	signDataHiding          bool
	cabacInitPresent        bool
	numRefIdxDefault        [2]int
	initQP                  int
	constrainedIntraPred    bool
	transformSkip           bool
	cuQPDeltaEnabled        bool
	diffCuQPDeltaDepth      int
	cbQPOffset, crQPOffset  int
	sliceChromaQPOffsets    bool
	weightedPred            bool
	weightedBipred          bool
	transquantBypass        bool
	tiles                   bool
	entropySync             bool
	colWidths, rowHeights   []int // in CTBs
	loopFilterAcrossTiles   bool
	loopFilterAcrossSlices  bool
	deblockingOverride      bool
	deblockingDisabled      bool
	betaOffset, tcOffset    int // already doubled
	scalingListPresent      bool
	scaling                 scalingList
	listsModification       bool
	log2ParMrgLevel         int
	sliceHeaderExtension    bool

	// Derived, for the SPS it was activated with.
	sps     *sps
	colBd   []int // tile column boundaries, in CTBs
	rowBd   []int
	rsToTS  []int // CTB raster to tile scan
	tsToRS  []int
	tileID  []int // by tile scan address
	minTbZs []int // z-scan order of each minimum transform block, raster
}

var defaultScalingIntra = [64]uint8{
	16, 16, 16, 16, 17, 18, 21, 24, 16, 16, 16, 16, 17, 19, 22, 25,
	16, 16, 17, 18, 20, 22, 25, 29, 16, 16, 18, 21, 24, 27, 31, 36,
	17, 17, 20, 24, 30, 35, 41, 47, 18, 19, 22, 27, 35, 44, 54, 65,
	21, 22, 25, 31, 41, 54, 70, 88, 24, 25, 29, 36, 47, 65, 88, 115,
}

var defaultScalingInter = [64]uint8{
	16, 16, 16, 16, 17, 18, 20, 24, 16, 16, 16, 17, 18, 20, 24, 25,
	16, 16, 17, 18, 20, 24, 25, 28, 16, 17, 18, 20, 24, 25, 28, 33,
	17, 18, 20, 24, 25, 28, 33, 41, 18, 20, 24, 25, 28, 33, 41, 54,
	20, 24, 25, 28, 33, 41, 54, 71, 24, 25, 28, 33, 41, 54, 71, 91,
}

// The defaults (Table 7-6) and the lists are kept in raster order.
func defaultScaling() scalingList {
	var s scalingList
	for m := range 6 {
		for i := range 16 {
			s.lists[0][m][i] = 16
		}
	}
	for size := 1; size < 4; size++ {
		for m := range 6 {
			src := &defaultScalingIntra
			if m >= 3 {
				src = &defaultScalingInter
			}
			s.lists[size][m] = *src
		}
	}
	for i := range 2 {
		for m := range 6 {
			s.dc[i][m] = 16
		}
	}
	return s
}

func parseScalingList(r *bits, s *scalingList) error {
	for size := range 4 {
		step := 1
		if size == 3 {
			step = 3
		}
		for m := 0; m < 6; m += step {
			n := min(64, 1<<(4+size*2))
			if !r.flag() { // scaling_list_pred_mode_flag
				delta := r.ue()
				if size == 3 {
					delta *= 3
				}
				if delta > m {
					return errStream
				}
				if delta == 0 {
					// The default list.
					d := defaultScaling()
					s.lists[size][m] = d.lists[size][m]
					if size > 1 {
						s.dc[size-2][m] = 16
					}
				} else {
					s.lists[size][m] = s.lists[size][m-delta]
					if size > 1 {
						s.dc[size-2][m] = s.dc[size-2][m-delta]
					}
				}
				continue
			}
			next := 8
			if size > 1 {
				dc := r.se() + 8
				if dc < 1 || dc > 255 {
					return errStream
				}
				next = dc
				s.dc[size-2][m] = uint8(dc)
			}
			for i := range n {
				delta := r.se()
				next = (next + delta + 256) % 256
				var x, y int
				if size == 0 {
					x, y = int(diagScan4x4[i][0]), int(diagScan4x4[i][1])
					s.lists[size][m][y*4+x] = uint8(next)
				} else {
					x, y = int(diagScan8x8[i][0]), int(diagScan8x8[i][1])
					s.lists[size][m][y*8+x] = uint8(next)
				}
			}
		}
	}
	// 32x32 chroma lists (only for 4:4:4) take the 16x16 ones.
	for _, m := range []int{1, 2, 4, 5} {
		s.lists[3][m] = s.lists[2][m]
		s.dc[1][m] = s.dc[0][m]
	}
	return nil
}

// parseProfileTierLevel reads profile_tier_level(), giving
// general_profile_idc.
func parseProfileTierLevel(r *bits, maxSubLayersMinus1 int) int {
	r.skip(2 + 1)
	profile := r.u(5)
	r.skip(32 + 4 + 43 + 1 + 8)
	var profilePresent, levelPresent [8]bool
	for i := range maxSubLayersMinus1 {
		profilePresent[i] = r.flag()
		levelPresent[i] = r.flag()
	}
	if maxSubLayersMinus1 > 0 {
		r.skip(2 * (8 - maxSubLayersMinus1))
	}
	for i := range maxSubLayersMinus1 {
		if profilePresent[i] {
			r.skip(2 + 1 + 5 + 32 + 4 + 43 + 1)
		}
		if levelPresent[i] {
			r.skip(8)
		}
	}
	return profile
}

// parseSTRPS reads st_ref_pic_set(idx): from the SPS's sets (inSlice
// false, idx their number so far) or a slice header (idx the SPS's count).
func parseSTRPS(r *bits, sets []stRPS, idx int, inSlice bool) (stRPS, error) {
	var s stRPS
	predict := false
	if idx != 0 {
		predict = r.flag()
	}
	if predict {
		deltaIdx := 1
		if inSlice {
			deltaIdx = r.ue() + 1
		}
		if deltaIdx > idx {
			return s, errStream
		}
		ref := &sets[idx-deltaIdx]
		sign := r.u(1)
		abs := r.ue() + 1
		deltaRPS := (1 - 2*sign) * abs
		var used, useDelta [33]bool
		for j := 0; j <= ref.num(); j++ {
			used[j] = r.flag()
			useDelta[j] = true
			if !used[j] {
				useDelta[j] = r.flag()
			}
		}
		// 7-61, 7-62.
		i := 0
		for j := ref.numPos - 1; j >= 0; j-- {
			d := ref.deltaPOC[ref.numNeg+j] + deltaRPS
			if d < 0 && useDelta[ref.numNeg+j] {
				s.deltaPOC[i], s.used[i] = d, used[ref.numNeg+j]
				i++
			}
		}
		if deltaRPS < 0 && useDelta[ref.num()] {
			s.deltaPOC[i], s.used[i] = deltaRPS, used[ref.num()]
			i++
		}
		for j := 0; j < ref.numNeg; j++ {
			d := ref.deltaPOC[j] + deltaRPS
			if d < 0 && useDelta[j] {
				s.deltaPOC[i], s.used[i] = d, used[j]
				i++
			}
		}
		s.numNeg = i
		for j := ref.numNeg - 1; j >= 0; j-- {
			d := ref.deltaPOC[j] + deltaRPS
			if d > 0 && useDelta[j] {
				if i >= 32 {
					return s, errStream
				}
				s.deltaPOC[i], s.used[i] = d, used[j]
				i++
			}
		}
		if deltaRPS > 0 && useDelta[ref.num()] {
			if i >= 32 {
				return s, errStream
			}
			s.deltaPOC[i], s.used[i] = deltaRPS, used[ref.num()]
			i++
		}
		for j := 0; j < ref.numPos; j++ {
			d := ref.deltaPOC[ref.numNeg+j] + deltaRPS
			if d > 0 && useDelta[ref.numNeg+j] {
				if i >= 32 {
					return s, errStream
				}
				s.deltaPOC[i], s.used[i] = d, used[ref.numNeg+j]
				i++
			}
		}
		s.numPos = i - s.numNeg
		return s, nil
	}
	s.numNeg = r.ue()
	s.numPos = r.ue()
	if s.numNeg > 16 || s.numPos > 16 {
		return s, errStream
	}
	poc := 0
	for i := range s.numNeg {
		poc -= r.ue() + 1
		s.deltaPOC[i] = poc
		s.used[i] = r.flag()
	}
	poc = 0
	for i := range s.numPos {
		poc += r.ue() + 1
		s.deltaPOC[s.numNeg+i] = poc
		s.used[s.numNeg+i] = r.flag()
	}
	return s, nil
}

func (d *Decoder) parseSPS(r *bits) error {
	s := &sps{primaries: 2, transfer: 2, matrix: 2}
	r.skip(4) // sps_video_parameter_set_id
	maxSub := r.u(3)
	r.skip(1)
	s.profile = parseProfileTierLevel(r, maxSub)
	s.id = r.ue()
	if s.id > 15 {
		return errStream
	}
	s.chromaFormat = r.ue()
	if s.chromaFormat == 3 {
		s.sepColour = r.flag()
	}
	if s.chromaFormat != 1 {
		return unsupported("a chroma format other than 4:2:0")
	}
	s.width = r.ue()
	s.height = r.ue()
	if r.flag() {
		for i := range 4 {
			s.confWin[i] = r.ue() * 2 // SubWidthC, SubHeightC: 2 for 4:2:0
		}
	}
	s.bitDepth = r.ue() + 8
	s.bitDepthC = r.ue() + 8
	if s.bitDepth > 12 || s.bitDepthC > 12 {
		return unsupported("a bit depth above 12")
	}
	s.log2MaxPOCLsb = r.ue() + 4
	if s.log2MaxPOCLsb > 16 {
		return errStream
	}
	subOrdering := r.flag()
	first := maxSub
	if subOrdering {
		first = 0
	}
	for i := first; i <= maxSub; i++ {
		s.maxDecPicBuf = r.ue() + 1
		s.maxReorder = r.ue()
		lat := r.ue()
		s.maxLatency = 0
		if lat != 0 {
			s.maxLatency = s.maxReorder + lat - 1
		}
	}
	s.log2MinCb = r.ue() + 3
	s.log2Ctb = s.log2MinCb + r.ue()
	s.log2MinTb = r.ue() + 2
	s.log2MaxTb = s.log2MinTb + r.ue()
	s.maxTrDepthInter = r.ue()
	s.maxTrDepthIntra = r.ue()
	if s.log2Ctb > 6 || s.log2Ctb < 4 || s.log2MaxTb > 5 || s.log2MaxTb > s.log2Ctb || s.log2MinTb >= s.log2MinCb {
		return errStream
	}
	s.scalingListEnabled = r.flag()
	if s.scalingListEnabled {
		s.scaling = defaultScaling()
		if r.flag() {
			if err := parseScalingList(r, &s.scaling); err != nil {
				return err
			}
		}
	}
	s.amp = r.flag()
	s.sao = r.flag()
	s.pcm = r.flag()
	if s.pcm {
		s.pcmBits = r.u(4) + 1
		s.pcmBitsC = r.u(4) + 1
		s.log2MinPCM = r.ue() + 3
		s.log2MaxPCM = s.log2MinPCM + r.ue()
		s.pcmLoopFilterDisabled = r.flag()
	}
	n := r.ue()
	if n > 64 {
		return errStream
	}
	s.stRPS = make([]stRPS, n)
	for i := range n {
		var err error
		if s.stRPS[i], err = parseSTRPS(r, s.stRPS, i, false); err != nil {
			return err
		}
	}
	s.longTermRefsPresent = r.flag()
	if s.longTermRefsPresent {
		n := r.ue()
		if n > 32 {
			return errStream
		}
		for range n {
			s.ltPOCLsb = append(s.ltPOCLsb, r.u(s.log2MaxPOCLsb))
			s.ltUsed = append(s.ltUsed, r.flag())
		}
	}
	s.temporalMVP = r.flag()
	s.strongIntraSmoothing = r.flag()
	if r.flag() {
		parseVUI(r, s, maxSub)
	}
	// The extensions, read for the range extensions' profiles only (as
	// ffmpeg: version 1 streams can carry extension data meant to be
	// skipped).
	if r.flag() && s.profile >= 4 { // sps_extension_present_flag
		rangeExt := r.flag()
		r.skip(1 + 1 + 1 + 4) // multilayer, 3D, SCC, 4 bits
		if rangeExt {
			for range 9 {
				if r.flag() {
					return unsupported("the format range extensions")
				}
			}
		}
	}
	if r.left() < 0 {
		return errShort
	}
	s.ctbSize = 1 << s.log2Ctb
	s.ctbW = (s.width + s.ctbSize - 1) >> s.log2Ctb
	s.ctbH = (s.height + s.ctbSize - 1) >> s.log2Ctb
	s.minCbW = s.width >> s.log2MinCb
	s.minCbH = s.height >> s.log2MinCb
	s.minTbW = s.ctbW << (s.log2Ctb - s.log2MinTb)
	s.minTbH = s.ctbH << (s.log2Ctb - s.log2MinTb)
	s.qpBdOffset = 6 * (s.bitDepth - 8)
	s.qpBdOffsetC = 6 * (s.bitDepthC - 8)
	if s.width == 0 || s.height == 0 || s.width%(1<<s.log2MinCb) != 0 || s.height%(1<<s.log2MinCb) != 0 {
		return errStream
	}
	if s.width > 16888 || s.height > 16888 {
		return unsupported("a picture over 16888 samples wide or high")
	}
	d.spss[s.id] = s
	return nil
}

func parseHRD(r *bits, common bool, maxSub int) {
	nal, vcl, subPic := false, false, false
	if common {
		nal = r.flag()
		vcl = r.flag()
		if nal || vcl {
			subPic = r.flag()
			if subPic {
				r.skip(8 + 5 + 1 + 5)
			}
			r.skip(4 + 4)
			if subPic {
				r.skip(4)
			}
			r.skip(5 + 5 + 5)
		}
	}
	for range maxSub + 1 {
		fixedGeneral := r.flag()
		fixedWithin := true
		if !fixedGeneral {
			fixedWithin = r.flag()
		}
		lowDelay := false
		if fixedWithin {
			r.ue()
		} else {
			lowDelay = r.flag()
		}
		cpbCnt := 1
		if !lowDelay {
			cpbCnt = r.ue() + 1
		}
		for _, present := range []bool{nal, vcl} {
			if !present {
				continue
			}
			for range cpbCnt {
				r.ue()
				r.ue()
				if subPic {
					r.ue()
					r.ue()
				}
				r.skip(1)
			}
		}
	}
}

func parseVUI(r *bits, s *sps, maxSub int) {
	if r.flag() { // aspect_ratio_info_present_flag
		if r.u(8) == 255 {
			r.skip(32)
		}
	}
	if r.flag() { // overscan_info_present_flag
		r.skip(1)
	}
	if r.flag() { // video_signal_type_present_flag
		r.skip(3)
		s.fullRange = r.flag()
		if r.flag() {
			s.primaries, s.transfer, s.matrix = r.u(8), r.u(8), r.u(8)
		}
	}
	if r.flag() { // chroma_loc_info_present_flag
		r.ue()
		r.ue()
	}
	r.skip(3)     // neutral_chroma, field_seq, frame_field_info
	if r.flag() { // default_display_window_flag
		r.ue()
		r.ue()
		r.ue()
		r.ue()
	}
	if r.flag() { // vui_timing_info_present_flag
		s.unitsInTick = r.u(32)
		s.timeScale = r.u(32)
		if r.flag() {
			r.ue()
		}
		if r.flag() {
			parseHRD(r, true, maxSub)
		}
	}
	if r.flag() { // bitstream_restriction_flag
		r.skip(3)
		r.ue()
		r.ue()
		r.ue()
		r.ue()
		r.ue()
	}
}

func (d *Decoder) parsePPS(r *bits) error {
	p := &pps{}
	p.id = r.ue()
	if p.id > 63 {
		return errStream
	}
	p.spsID = r.ue()
	if p.spsID > 15 {
		return errStream
	}
	p.dependentSlices = r.flag()
	p.outputFlagPresent = r.flag()
	p.numExtraSliceHeaderBits = r.u(3)
	p.signDataHiding = r.flag()
	p.cabacInitPresent = r.flag()
	p.numRefIdxDefault[0] = r.ue() + 1
	p.numRefIdxDefault[1] = r.ue() + 1
	p.initQP = 26 + r.se()
	p.constrainedIntraPred = r.flag()
	p.transformSkip = r.flag()
	p.cuQPDeltaEnabled = r.flag()
	if p.cuQPDeltaEnabled {
		p.diffCuQPDeltaDepth = r.ue()
	}
	p.cbQPOffset = r.se()
	p.crQPOffset = r.se()
	p.sliceChromaQPOffsets = r.flag()
	p.weightedPred = r.flag()
	p.weightedBipred = r.flag()
	p.transquantBypass = r.flag()
	p.tiles = r.flag()
	p.entropySync = r.flag()
	uniform := true
	if p.tiles {
		cols := r.ue() + 1
		rows := r.ue() + 1
		if cols > 20 || rows > 22 {
			return errStream
		}
		uniform = r.flag()
		p.colWidths = make([]int, cols)
		p.rowHeights = make([]int, rows)
		if !uniform {
			for i := range cols - 1 {
				p.colWidths[i] = r.ue() + 1
			}
			for i := range rows - 1 {
				p.rowHeights[i] = r.ue() + 1
			}
		}
		p.loopFilterAcrossTiles = r.flag()
	}
	p.loopFilterAcrossSlices = r.flag()
	if r.flag() { // deblocking_filter_control_present_flag
		p.deblockingOverride = r.flag()
		p.deblockingDisabled = r.flag()
		if !p.deblockingDisabled {
			p.betaOffset = r.se() * 2
			p.tcOffset = r.se() * 2
		}
	}
	p.scalingListPresent = r.flag()
	if p.scalingListPresent {
		p.scaling = defaultScaling()
		if err := parseScalingList(r, &p.scaling); err != nil {
			return err
		}
	}
	p.listsModification = r.flag()
	p.log2ParMrgLevel = r.ue() + 2
	p.sliceHeaderExtension = r.flag()
	if r.flag() && d.spss[p.spsID] != nil && d.spss[p.spsID].profile >= 4 { // pps_extension_present_flag
		rangeExt := r.flag()
		r.skip(1 + 1 + 1 + 4)
		if rangeExt {
			if p.transformSkip {
				r.ue() // log2_max_transform_skip_block_size_minus2
			}
			crossComponent, qpOffsetList := r.flag(), r.flag()
			if crossComponent || qpOffsetList {
				return unsupported("the format range extensions")
			}
			saoScaleLuma, saoScaleChroma := r.ue(), r.ue()
			if saoScaleLuma != 0 || saoScaleChroma != 0 {
				return unsupported("the format range extensions")
			}
		}
	}
	if r.left() < 0 {
		return errShort
	}
	if !uniform {
		p.colWidths = append(p.colWidths[:len(p.colWidths)-1], -1)
		p.rowHeights = append(p.rowHeights[:len(p.rowHeights)-1], -1)
	}
	d.ppss[p.id] = p
	return nil
}

// activate derives a PPS's tile layout and scan conversions for its SPS.
func (p *pps) activate(s *sps, prev *pps) error {
	if p.sps == s {
		return nil
	}
	// Discs send their parameter sets again and again, the same: the
	// tables of the PPS before serve.
	if prev != nil && prev.sps != nil && sameLayout(prev, p, s) {
		p.colBd, p.rowBd = prev.colBd, prev.rowBd
		p.rsToTS, p.tsToRS, p.tileID, p.minTbZs = prev.rsToTS, prev.tsToRS, prev.tileID, prev.minTbZs
		p.sps = s
		return nil
	}
	cols, rows := 1, 1
	if p.tiles {
		cols, rows = len(p.colWidths), len(p.rowHeights)
	}
	p.colBd = make([]int, cols+1)
	p.rowBd = make([]int, rows+1)
	widths := make([]int, cols)
	heights := make([]int, rows)
	switch {
	case !p.tiles:
		widths[0], heights[0] = s.ctbW, s.ctbH
	case p.colWidths[cols-1] == -1:
		sum := 0
		for i := range cols - 1 {
			widths[i] = p.colWidths[i]
			sum += widths[i]
		}
		widths[cols-1] = s.ctbW - sum
		sum = 0
		for i := range rows - 1 {
			heights[i] = p.rowHeights[i]
			sum += heights[i]
		}
		heights[rows-1] = s.ctbH - sum
	default: // uniform
		for i := range cols {
			widths[i] = ((i+1)*s.ctbW)/cols - (i*s.ctbW)/cols
		}
		for i := range rows {
			heights[i] = ((i+1)*s.ctbH)/rows - (i*s.ctbH)/rows
		}
	}
	for i := range cols {
		if widths[i] <= 0 {
			return errStream
		}
		p.colBd[i+1] = p.colBd[i] + widths[i]
	}
	for i := range rows {
		if heights[i] <= 0 {
			return errStream
		}
		p.rowBd[i+1] = p.rowBd[i] + heights[i]
	}
	if p.colBd[cols] != s.ctbW || p.rowBd[rows] != s.ctbH {
		return errStream
	}
	n := s.ctbW * s.ctbH
	p.rsToTS = make([]int, n)
	p.tsToRS = make([]int, n)
	p.tileID = make([]int, n)
	for rs := range n {
		tbX, tbY := rs%s.ctbW, rs/s.ctbW
		tileX, tileY := 0, 0
		for i := range cols {
			if tbX >= p.colBd[i] {
				tileX = i
			}
		}
		for j := range rows {
			if tbY >= p.rowBd[j] {
				tileY = j
			}
		}
		v := 0
		for i := range tileX {
			v += heights[tileY] * widths[i]
		}
		for j := range tileY {
			v += s.ctbW * heights[j]
		}
		v += (tbY-p.rowBd[tileY])*widths[tileX] + tbX - p.colBd[tileX]
		p.rsToTS[rs] = v
		p.tsToRS[v] = rs
	}
	tid := 0
	for j := range rows {
		for i := range cols {
			for y := p.rowBd[j]; y < p.rowBd[j+1]; y++ {
				for x := p.colBd[i]; x < p.colBd[i+1]; x++ {
					p.tileID[p.rsToTS[y*s.ctbW+x]] = tid
				}
			}
			tid++
		}
	}
	// 6-10: z-scan order of the minimum transform blocks.
	shift := s.log2Ctb - s.log2MinTb
	p.minTbZs = make([]int, s.minTbW*s.minTbH)
	for y := range s.minTbH {
		for x := range s.minTbW {
			tbX := (x << s.log2MinTb) >> s.log2Ctb
			tbY := (y << s.log2MinTb) >> s.log2Ctb
			v := p.rsToTS[tbY*s.ctbW+tbX] << (shift * 2)
			for i := range shift {
				m := 1 << i
				if m&x != 0 {
					v += m * m
				}
				if m&y != 0 {
					v += 2 * m * m
				}
			}
			p.minTbZs[y*s.minTbW+x] = v
		}
	}
	p.sps = s
	return nil
}

// sameLayout reports whether p on s has the CTB and tile layout prev has
// on its SPS.
func sameLayout(prev, p *pps, s *sps) bool {
	ps := prev.sps
	return ps.ctbW == s.ctbW && ps.ctbH == s.ctbH && ps.log2Ctb == s.log2Ctb && ps.log2MinTb == s.log2MinTb &&
		ps.minTbW == s.minTbW && ps.minTbH == s.minTbH && prev.tiles == p.tiles &&
		slices.Equal(prev.colWidths, p.colWidths) && slices.Equal(prev.rowHeights, p.rowHeights)
}
