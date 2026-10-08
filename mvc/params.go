package mvc

import (
	"errors"
	"sync/atomic"
)

// maxFrameMbs is the largest accepted picture size in macroblocks.
var maxFrameMbs = 139264

var (
	errUnsupported = errors.New("mvc: unsupported bitstream feature")
	errInvalid     = errors.New("mvc: invalid bitstream")
)

// NAL unit types used by the decoder.
const (
	nalSlice        = 1
	nalSliceIDR     = 5
	nalSEI          = 6
	nalSPS          = 7
	nalPPS          = 8
	nalAUD          = 9
	nalEndSeq       = 10
	nalEndStream    = 11
	nalPrefix       = 14
	nalSubsetSPS    = 15
	nalSliceExt     = 20
	nalDepDelimiter = 24 // Blu-ray dependent view delimiter
	maxRefsPerList  = 32
	maxSPS          = 32
	maxPPS          = 256
	maxViews        = 2
)

type sps struct {
	valid                   bool
	profileIdc              int
	constraintFlags         int
	levelIdc                int
	id                      int
	chromaFormatIdc         int
	bitDepthLuma            int
	bitDepthChroma          int
	transformBypass         bool
	scalingMatrixPresent    bool
	scaling4x4              [6][16]uint8 // zigzag order
	scaling8x8              [6][64]uint8
	log2MaxFrameNum         uint
	pocType                 int
	log2MaxPocLsb           uint
	deltaPicOrderAlwaysZero bool
	offsetForNonRefPic      int32
	offsetForTopToBottom    int32
	offsetForRefFrame       []int32
	maxNumRefFrames         int
	gapsAllowed             bool
	widthMbs                int
	heightMapUnits          int
	frameMbsOnly            bool
	mbaff                   bool
	direct8x8Inference      bool
	cropLeft, cropRight     int // in luma samples
	cropTop, cropBottom     int
	// VUI
	maxDecFrameBuffering int    // -1 if absent
	numReorderFrames     int    // -1 if absent
	numUnitsInTick       uint32 // timing_info: 0 if absent
	timeScale            uint32
	// MVC extension (subset SPS)
	mvc mvcExt
}

type mvcExt struct {
	present       bool
	numViews      int
	viewID        []int
	anchorRefs    [2][][]int // [list][viewIdx] -> view ids
	nonAnchorRefs [2][][]int
}

func (s *sps) heightMbs() int {
	if s.frameMbsOnly {
		return s.heightMapUnits
	}
	return s.heightMapUnits * 2
}

func (s *sps) viewIndex(viewID int) int {
	if !s.mvc.present {
		return 0
	}
	for i, v := range s.mvc.viewID {
		if v == viewID {
			return i
		}
	}
	return -1
}

type pps struct {
	valid                  bool
	id                     int
	spsID                  int
	cabac                  bool
	bottomFieldPicOrder    bool
	numSliceGroups         int
	numRefIdxActive        [2]int
	weightedPred           bool
	weightedBipredIdc      int
	picInitQP              int
	picInitQS              int
	chromaQPOffset         [2]int
	deblockingControl      bool
	constrainedIntraPred   bool
	redundantPicCntPresent bool
	transform8x8           bool
	scalingMatrixPresent   bool
	scalingListPresent     [12]bool
	scalingUseDefault      [12]bool
	scaling4x4             [6][16]uint8
	scaling8x8             [6][64]uint8

	// derived dequantization tables for a particular SPS
	dq atomic.Pointer[dqTables]
}

// dqTables holds LevelScale tables for a PPS/SPS combination.
type dqTables struct {
	sps *sps
	dq4 [6][6][16]int32 // [list][qp%6][raster pos]
	dq8 [2][6][64]int32
	// the same tables in zigzag scan order, for fused dequantization
	dq4z [6][6][16]int32
	dq8z [2][6][64]int32
}

// dequant returns the dequantization tables of p for SPS s.
func (p *pps) dequant(s *sps) *dqTables {
	if t := p.dq.Load(); t != nil && t.sps == s {
		return t
	}
	t := p.computeDequant(s)
	p.dq.Store(t)
	return t
}

var zigzag4x4 = [16]uint8{0, 1, 4, 8, 5, 2, 3, 6, 9, 12, 13, 10, 7, 11, 14, 15}

var zigzag8x8 = [64]uint8{
	0, 1, 8, 16, 9, 2, 3, 10, 17, 24, 32, 25, 18, 11, 4, 5,
	12, 19, 26, 33, 40, 48, 41, 34, 27, 20, 13, 6, 7, 14, 21, 28,
	35, 42, 49, 56, 57, 50, 43, 36, 29, 22, 15, 23, 30, 37, 44, 51,
	58, 59, 52, 45, 38, 31, 39, 46, 53, 60, 61, 54, 47, 55, 62, 63,
}

var default4x4Intra = [16]uint8{6, 13, 13, 20, 20, 20, 28, 28, 28, 28, 32, 32, 32, 37, 37, 42}
var default4x4Inter = [16]uint8{10, 14, 14, 20, 20, 20, 24, 24, 24, 24, 27, 27, 27, 30, 30, 34}
var default8x8Intra = [64]uint8{
	6, 10, 10, 13, 11, 13, 16, 16, 16, 16, 18, 18, 18, 18, 18, 23,
	23, 23, 23, 23, 23, 25, 25, 25, 25, 25, 25, 25, 27, 27, 27, 27,
	27, 27, 27, 27, 29, 29, 29, 29, 29, 29, 29, 31, 31, 31, 31, 31,
	31, 33, 33, 33, 33, 33, 36, 36, 36, 36, 38, 38, 38, 40, 40, 42,
}
var default8x8Inter = [64]uint8{
	9, 13, 13, 15, 13, 15, 17, 17, 17, 17, 19, 19, 19, 19, 19, 21,
	21, 21, 21, 21, 21, 22, 22, 22, 22, 22, 22, 22, 24, 24, 24, 24,
	24, 24, 24, 24, 25, 25, 25, 25, 25, 25, 25, 27, 27, 27, 27, 27,
	27, 28, 28, 28, 28, 28, 30, 30, 30, 30, 32, 32, 32, 33, 33, 35,
}

func flat16() (l [16]uint8) {
	for i := range l {
		l[i] = 16
	}
	return
}

func flat64() (l [64]uint8) {
	for i := range l {
		l[i] = 16
	}
	return
}

// parseScalingList reads a scaling list; returns useDefault.
func parseScalingList(br *bitReader, list []uint8) bool {
	last, next := 8, 8
	for j := range list {
		if next != 0 {
			delta := int(br.se())
			next = (last + delta + 256) & 255
			if j == 0 && next == 0 {
				return true
			}
		}
		if next != 0 {
			list[j] = uint8(next)
		} else {
			list[j] = uint8(last)
		}
		last = int(list[j])
	}
	return false
}

// parseScalingMatrix parses n scaling lists, applying fall-back rules.
// fallback4/fallback8 give the lists used for the first list of each kind
// when absent (rule A: defaults, rule B: the SPS lists).
func parseScalingMatrix(br *bitReader, n int, l4 *[6][16]uint8, l8 *[6][64]uint8,
	fb4 *[6][16]uint8, fb8 *[6][64]uint8) {
	for i := 0; i < n; i++ {
		present := br.flag()
		if i < 6 {
			if present {
				if parseScalingList(br, l4[i][:]) {
					if i < 3 {
						l4[i] = default4x4Intra
					} else {
						l4[i] = default4x4Inter
					}
				}
				continue
			}
			switch i {
			case 0, 3:
				l4[i] = fb4[i]
			default:
				l4[i] = l4[i-1]
			}
		} else {
			k := i - 6
			if present {
				if parseScalingList(br, l8[k][:]) {
					if k%2 == 0 {
						l8[k] = default8x8Intra
					} else {
						l8[k] = default8x8Inter
					}
				}
				continue
			}
			if k < 2 {
				l8[k] = fb8[k]
			} else {
				l8[k] = l8[k-2]
			}
		}
	}
	// lists beyond n (8x8 lists for transform_8x8_mode_flag == 0)
	for i := n; i < 8; i++ {
		k := i - 6
		if k >= 0 && k < 2 {
			l8[k] = fb8[k]
		}
	}
}

func defaultFallbackA() (fb4 [6][16]uint8, fb8 [6][64]uint8) {
	fb4[0] = default4x4Intra
	fb4[3] = default4x4Inter
	fb8[0] = default8x8Intra
	fb8[1] = default8x8Inter
	return
}

func parseSPSData(br *bitReader, s *sps) error {
	s.profileIdc = int(br.u(8))
	s.constraintFlags = int(br.u(8))
	s.levelIdc = int(br.u(8))
	id := br.ue()
	if id >= maxSPS {
		return errInvalid
	}
	s.id = int(id)
	s.chromaFormatIdc = 1
	s.bitDepthLuma = 8
	s.bitDepthChroma = 8
	for i := range s.scaling4x4 {
		s.scaling4x4[i] = flat16()
		s.scaling8x8[i] = flat64()
	}
	switch s.profileIdc {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		s.chromaFormatIdc = int(br.ue())
		if s.chromaFormatIdc > 3 {
			return errInvalid
		}
		if s.chromaFormatIdc == 3 {
			br.u1() // separate_colour_plane_flag
		}
		s.bitDepthLuma = int(br.ue()) + 8
		s.bitDepthChroma = int(br.ue()) + 8
		s.transformBypass = br.flag()
		s.scalingMatrixPresent = br.flag()
		if s.scalingMatrixPresent {
			n := 8
			if s.chromaFormatIdc == 3 {
				n = 12
			}
			fb4, fb8 := defaultFallbackA()
			if n > 8 {
				return errUnsupported
			}
			parseScalingMatrix(br, n, &s.scaling4x4, &s.scaling8x8, &fb4, &fb8)
		}
	}
	s.log2MaxFrameNum = uint(br.ue()) + 4
	if s.log2MaxFrameNum > 16 {
		return errInvalid
	}
	s.pocType = int(br.ue())
	switch s.pocType {
	case 0:
		s.log2MaxPocLsb = uint(br.ue()) + 4
		if s.log2MaxPocLsb > 16 {
			return errInvalid
		}
	case 1:
		s.deltaPicOrderAlwaysZero = br.flag()
		s.offsetForNonRefPic = br.se()
		s.offsetForTopToBottom = br.se()
		n := br.ue()
		if n > 255 {
			return errInvalid
		}
		s.offsetForRefFrame = make([]int32, n)
		for i := range s.offsetForRefFrame {
			s.offsetForRefFrame[i] = br.se()
		}
	case 2:
	default:
		return errInvalid
	}
	s.maxNumRefFrames = int(br.ue())
	if s.maxNumRefFrames > 16 {
		return errInvalid
	}
	s.gapsAllowed = br.flag()
	s.widthMbs = int(br.ue()) + 1
	s.heightMapUnits = int(br.ue()) + 1
	if s.widthMbs > 1024 || s.heightMapUnits > 1024 {
		return errInvalid
	}
	s.frameMbsOnly = br.flag()
	if !s.frameMbsOnly {
		s.mbaff = br.flag()
	}
	s.direct8x8Inference = br.flag()
	// MaxFS of level 6.2 bounds memory use on malformed input
	if s.widthMbs*s.heightMbs() > maxFrameMbs {
		return errUnsupported
	}
	if br.flag() {
		cl, cr, ct, cb := int(br.ue()), int(br.ue()), int(br.ue()), int(br.ue())
		cx, cy := 1, 1
		if s.chromaFormatIdc == 1 || s.chromaFormatIdc == 2 {
			cx = 2
		}
		if s.chromaFormatIdc == 1 {
			cy = 2
		}
		if !s.frameMbsOnly {
			cy *= 2
		}
		s.cropLeft, s.cropRight = cl*cx, cr*cx
		s.cropTop, s.cropBottom = ct*cy, cb*cy
		if s.cropLeft+s.cropRight >= s.widthMbs*16 || s.cropTop+s.cropBottom >= s.heightMbs()*16 {
			s.cropLeft, s.cropRight, s.cropTop, s.cropBottom = 0, 0, 0, 0
		}
	}
	s.maxDecFrameBuffering = -1
	s.numReorderFrames = -1
	if br.flag() {
		parseVUI(br, s)
	}
	if br.overrun() {
		return errInvalid
	}
	return nil
}

func skipHRD(br *bitReader) {
	cnt := br.ue() + 1
	br.u(8)
	for i := uint32(0); i < cnt && i < 32; i++ {
		br.ue()
		br.ue()
		br.u1()
	}
	br.u(20)
}

func parseVUI(br *bitReader, s *sps) {
	if br.flag() { // aspect_ratio_info_present
		if br.u(8) == 255 {
			br.u(16)
			br.u(16)
		}
	}
	if br.flag() { // overscan_info_present
		br.u1()
	}
	if br.flag() { // video_signal_type_present
		br.u(4)
		if br.flag() {
			br.u(24)
		}
	}
	if br.flag() { // chroma_loc_info_present
		br.ue()
		br.ue()
	}
	if br.flag() { // timing_info_present
		s.numUnitsInTick = br.u(32)
		s.timeScale = br.u(32)
		br.u1() // fixed_frame_rate_flag
	}
	nal := br.flag()
	if nal {
		skipHRD(br)
	}
	vcl := br.flag()
	if vcl {
		skipHRD(br)
	}
	if nal || vcl {
		br.u1() // low_delay_hrd_flag
	}
	br.u1()        // pic_struct_present_flag
	if br.flag() { // bitstream_restriction_flag
		br.u1()
		br.ue()
		br.ue()
		br.ue()
		br.ue()
		s.numReorderFrames = int(br.ue())
		s.maxDecFrameBuffering = int(br.ue())
		if br.overrun() || s.numReorderFrames > 16 || s.maxDecFrameBuffering > 16 {
			s.numReorderFrames = -1
			s.maxDecFrameBuffering = -1
		}
	}
}

func parseSPS(rbsp []byte) (*sps, error) {
	var br bitReader
	br.init(rbsp)
	s := &sps{}
	if err := parseSPSData(&br, s); err != nil {
		return nil, err
	}
	s.valid = true
	return s, nil
}

func parseSubsetSPS(rbsp []byte) (*sps, error) {
	var br bitReader
	br.init(rbsp)
	s := &sps{}
	if err := parseSPSData(&br, s); err != nil {
		return nil, err
	}
	if s.profileIdc != 118 && s.profileIdc != 128 && s.profileIdc != 134 {
		return nil, errUnsupported
	}
	br.u1() // bit_equal_to_one
	m := &s.mvc
	m.present = true
	m.numViews = int(br.ue()) + 1
	if m.numViews > 1024 {
		return nil, errInvalid
	}
	m.viewID = make([]int, m.numViews)
	for i := range m.viewID {
		m.viewID[i] = int(br.ue())
	}
	for l := 0; l < 2; l++ {
		m.anchorRefs[l] = make([][]int, m.numViews)
		m.nonAnchorRefs[l] = make([][]int, m.numViews)
	}
	for i := 1; i < m.numViews; i++ {
		for l := 0; l < 2; l++ {
			n := br.ue()
			if n > 15 {
				return nil, errInvalid
			}
			m.anchorRefs[l][i] = make([]int, n)
			for j := range m.anchorRefs[l][i] {
				m.anchorRefs[l][i][j] = int(br.ue())
			}
		}
	}
	for i := 1; i < m.numViews; i++ {
		for l := 0; l < 2; l++ {
			n := br.ue()
			if n > 15 {
				return nil, errInvalid
			}
			m.nonAnchorRefs[l][i] = make([]int, n)
			for j := range m.nonAnchorRefs[l][i] {
				m.nonAnchorRefs[l][i][j] = int(br.ue())
			}
		}
	}
	// level values / operation points and MVC VUI are not needed for decoding.
	if br.overrun() {
		return nil, errInvalid
	}
	s.valid = true
	return s, nil
}

func parsePPS(rbsp []byte) (*pps, error) {
	var br bitReader
	br.init(rbsp)
	p := &pps{}
	id := br.ue()
	sid := br.ue()
	if id >= maxPPS || sid >= maxSPS {
		return nil, errInvalid
	}
	p.id, p.spsID = int(id), int(sid)
	p.cabac = br.flag()
	p.bottomFieldPicOrder = br.flag()
	p.numSliceGroups = int(br.ue()) + 1
	if p.numSliceGroups > 1 {
		return nil, errUnsupported // FMO
	}
	p.numRefIdxActive[0] = int(br.ue()) + 1
	p.numRefIdxActive[1] = int(br.ue()) + 1
	if p.numRefIdxActive[0] > 32 || p.numRefIdxActive[1] > 32 {
		return nil, errInvalid
	}
	p.weightedPred = br.flag()
	p.weightedBipredIdc = int(br.u(2))
	p.picInitQP = 26 + int(br.se())
	p.picInitQS = 26 + int(br.se())
	p.chromaQPOffset[0] = int(br.se())
	p.chromaQPOffset[1] = p.chromaQPOffset[0]
	p.deblockingControl = br.flag()
	p.constrainedIntraPred = br.flag()
	p.redundantPicCntPresent = br.flag()
	if br.moreRBSPData() {
		p.transform8x8 = br.flag()
		p.scalingMatrixPresent = br.flag()
		if p.scalingMatrixPresent {
			n := 6
			if p.transform8x8 {
				n = 8
			}
			// Fall-back lists are resolved at activation time against the
			// SPS; record which lists are present here.
			for i := 0; i < n; i++ {
				p.scalingListPresent[i] = br.flag()
				if p.scalingListPresent[i] {
					if i < 6 {
						p.scalingUseDefault[i] = parseScalingList(&br, p.scaling4x4[i][:])
					} else {
						p.scalingUseDefault[i] = parseScalingList(&br, p.scaling8x8[i-6][:])
					}
				}
			}
		}
		p.chromaQPOffset[1] = int(br.se())
	}
	if br.overrun() {
		return nil, errInvalid
	}
	p.valid = true
	return p, nil
}

var normAdjust4 = [6][3]int32{{10, 16, 13}, {11, 18, 14}, {13, 20, 16}, {14, 23, 18}, {16, 25, 20}, {18, 29, 23}}
var normAdjust8 = [6][6]int32{
	{20, 18, 32, 19, 25, 24}, {22, 19, 35, 21, 28, 26}, {26, 23, 42, 24, 33, 31},
	{28, 25, 45, 26, 35, 33}, {32, 28, 51, 30, 40, 38}, {36, 32, 58, 34, 46, 43},
}

// computeDequant resolves the effective scaling lists for pps under s and
// builds the LevelScale tables.
func (p *pps) computeDequant(s *sps) *dqTables {
	t := &dqTables{sps: s}
	var l4 [6][16]uint8
	var l8 [6][64]uint8
	if !p.scalingMatrixPresent {
		l4, l8 = s.scaling4x4, s.scaling8x8
	} else {
		var fb4 [6][16]uint8
		var fb8 [6][64]uint8
		if s.scalingMatrixPresent {
			fb4, fb8 = s.scaling4x4, s.scaling8x8 // rule B
		} else {
			fb4, fb8 = defaultFallbackA() // rule A
		}
		for i := 0; i < 8; i++ {
			if i < 6 { //nolint:gosec // G602: i < 6 indexes the six 4x4 lists
				switch {
				case p.scalingListPresent[i] && p.scalingUseDefault[i]:
					if i < 3 {
						l4[i] = default4x4Intra
					} else {
						l4[i] = default4x4Inter
					}
				case p.scalingListPresent[i]:
					l4[i] = p.scaling4x4[i]
				case i == 0 || i == 3:
					l4[i] = fb4[i]
				default:
					l4[i] = l4[i-1]
				}
			} else {
				k := i - 6
				switch {
				case p.scalingListPresent[i] && p.scalingUseDefault[i]:
					if k == 0 {
						l8[k] = default8x8Intra
					} else {
						l8[k] = default8x8Inter
					}
				case p.scalingListPresent[i]:
					l8[k] = p.scaling8x8[k]
				default:
					l8[k] = fb8[k]
				}
			}
		}
	}
	for list := 0; list < 6; list++ {
		for m := 0; m < 6; m++ {
			for k := 0; k < 16; k++ {
				pos := zigzag4x4[k]
				i, j := pos>>2, pos&3
				var v int32
				switch {
				case i&1 == 0 && j&1 == 0:
					v = normAdjust4[m][0]
				case i&1 == 1 && j&1 == 1:
					v = normAdjust4[m][1]
				default:
					v = normAdjust4[m][2]
				}
				t.dq4[list][m][pos] = int32(l4[list][k]) * v
				t.dq4z[list][m][k] = t.dq4[list][m][pos]
			}
		}
	}
	for list := 0; list < 2; list++ {
		for m := 0; m < 6; m++ {
			for k := 0; k < 64; k++ {
				pos := zigzag8x8[k]
				i, j := int(pos>>3), int(pos&7)
				var v int32
				switch {
				case i%4 == 0 && j%4 == 0:
					v = normAdjust8[m][0]
				case i%2 == 1 && j%2 == 1:
					v = normAdjust8[m][1]
				case i%4 == 2 && j%4 == 2:
					v = normAdjust8[m][2]
				case (i%4 == 0 && j%2 == 1) || (i%2 == 1 && j%4 == 0):
					v = normAdjust8[m][3]
				case (i%4 == 0 && j%4 == 2) || (i%4 == 2 && j%4 == 0):
					v = normAdjust8[m][4]
				default:
					v = normAdjust8[m][5]
				}
				t.dq8[list][m][pos] = int32(l8[list][k]) * v
				t.dq8z[list][m][k] = t.dq8[list][m][pos]
			}
		}
	}
	return t
}
