package hevc

// Hardware decoding: with an Accel, the decoder parses the stream, keeps
// the picture order, the reference sets and lists and the output as it
// always does, and hands each picture's slices to the accelerator (VAAPI)
// in place of decoding them itself. Pictures live in the accelerator's
// surfaces.

// Accel decodes pictures for the decoder.
type Accel interface {
	// NewSurface gives a surface for pictures of the size and depth (the
	// coded size, in luma samples). A change of size or depth comes at an
	// IRAP picture that empties the buffer: the surfaces of the old one
	// have all been released.
	NewSurface(width, height, bitDepth int) (int, error)
	// Release gives a surface back.
	Release(surface int)
	// DecodePicture decodes a picture from its slice segments, in order.
	DecodePicture(p *AccelPicture, slices []AccelSlice) error
	// Output gives out the picture in surface, its information in p (no
	// planes), in display order.
	Output(surface int, p *Picture) error
}

// AccelRef is a reference picture of the one being decoded.
type AccelRef struct {
	Surface  int
	POC      int
	LongTerm bool
	// RPS says which list of the picture's reference picture set it is
	// in: RPSStCurrBefore, RPSStCurrAfter, RPSLtCurr, or 0 (kept only for
	// later pictures).
	RPS int
}

// The reference picture set lists.
const (
	RPSStCurrBefore = 1 + iota
	RPSStCurrAfter
	RPSLtCurr
)

// ScalingLists are the scaling factors in raster order: ScalingList of
// the standard by size; 32x32's matrices 0 and 1 are its matrixId 0 and 3.
type ScalingLists struct {
	L4      [6][16]uint8
	L8, L16 [6][64]uint8
	L32     [2][64]uint8
	DC16    [6]uint8
	DC32    [2]uint8
}

// AccelPicture is a picture's parameters, the standard's syntax elements
// and their derived values, named as there.
type AccelPicture struct {
	Surface int
	POC     int
	// Refs are the references the buffer holds; the slices' lists index
	// them.
	Refs      []AccelRef
	IRAP, IDR bool

	// From the SPS.
	Width, Height                    int // in luma samples
	ChromaFormat                     int
	SeparateColourPlane              bool
	BitDepth, BitDepthC              int
	Log2MaxPOCLsb                    int
	MaxDecPicBuffering               int
	Log2MinCb, Log2Ctb               int
	Log2MinTb, Log2MaxTb             int
	MaxTrDepthInter, MaxTrDepthIntra int
	PCM                              bool
	PCMBits, PCMBitsC                int
	Log2MinPCM, Log2MaxPCM           int
	PCMLoopFilterDisabled            bool
	AMP, SAO                         bool
	TemporalMVP                      bool
	StrongIntraSmoothing             bool
	LongTermRefsPresent              bool
	NumShortTermRPS                  int
	NumLongTermRefsSPS               int
	// Scaling is nil without scaling lists.
	Scaling *ScalingLists

	// From the PPS.
	DependentSlices         bool
	OutputFlagPresent       bool
	NumExtraSliceHeaderBits int
	SignDataHiding          bool
	CabacInitPresent        bool
	NumRefIdxDefault        [2]int
	InitQP                  int
	ConstrainedIntraPred    bool
	TransformSkip           bool
	CuQPDelta               bool
	DiffCuQPDeltaDepth      int
	CbQPOffset, CrQPOffset  int
	SliceChromaQPOffsets    bool
	WeightedPred            bool
	WeightedBipred          bool
	TransquantBypass        bool
	Tiles                   bool
	EntropySync             bool
	ColumnWidths            []int // in CTBs, with tiles
	RowHeights              []int
	LoopFilterAcrossTiles   bool
	LoopFilterAcrossSlices  bool
	DeblockingOverride      bool
	DeblockingDisabled      bool
	BetaOffsetDiv2          int
	TcOffsetDiv2            int
	ListsModification       bool
	Log2ParMrgLevel         int
	SliceHeaderExtension    bool

	// StRPSBits is the size of the first slice header's own
	// short_term_ref_pic_set(), 0 when it takes one of the SPS's.
	StRPSBits int
}

// AccelWeight is a reference's explicit weights, offsets at 8 bits.
type AccelWeight struct {
	LumaWeight, LumaOffset     int
	ChromaWeight, ChromaOffset [2]int
}

// AccelSlice is a slice segment: its NAL unit and its header.
type AccelSlice struct {
	// Data is the NAL unit, header and emulation prevention bytes
	// included; slice_data() starts at DataOffset.
	Data       []byte
	DataOffset int

	SegmentAddr        int
	Dependent          bool
	Type               int // 0 B, 1 P, 2 I
	ColourPlane        int
	SAOLuma, SAOChroma bool
	MvdL1Zero          bool
	CabacInit          bool
	TemporalMVP        bool
	DeblockingDisabled bool
	ColFromL0          bool
	LoopFilterAcross   bool
	ColRefIdx          int
	NumRefIdx          [2]int
	// RefList are the lists, as indexes into the picture's Refs.
	RefList                      [2][]int
	QPDelta                      int
	CbQPOffset, CrQPOffset       int
	BetaOffsetDiv2, TcOffsetDiv2 int
	MaxMergeCand                 int
	// With explicit weighted prediction.
	Log2WeightDenom, Log2WeightDenomC int
	Weights                           [2][]AccelWeight
}

// accelError is the accelerator failing: decoding stops.
type accelError struct{ error }

func (e accelError) Unwrap() error { return e.error }

// SetAccel makes the decoder decode with a, before the first picture.
func (d *Decoder) SetAccel(a Accel) { d.accel = a }

// accelState is a picture's on its way to the accelerator.
type accelState struct {
	pic    *AccelPicture
	refs   []*picture // by index in pic.Refs
	slices []AccelSlice
	data   []byte // the slices' NAL units, one after the other
	ends   []int  // where each slice's ends in data
}

// accelSlice takes a slice segment for the accelerator.
func (d *Decoder) accelSlice(h *sliceHeader, n *nalUnit) error {
	a := &d.acc
	if a.pic == nil {
		a.pic = d.accelPicture(h, n)
		a.refs = a.refs[:0]
		for _, p := range d.dpb {
			if p.ref == unused {
				continue
			}
			a.refs = append(a.refs, p)
		}
		ps := &d.pic
		for _, p := range a.refs {
			r := AccelRef{Surface: p.surface, POC: p.poc, LongTerm: p.ref == longTerm}
			switch {
			case contains(ps.stCurrBefore, p):
				r.RPS = RPSStCurrBefore
			case contains(ps.stCurrAfter, p):
				r.RPS = RPSStCurrAfter
			case contains(ps.ltCurr, p):
				r.RPS = RPSLtCurr
			}
			a.pic.Refs = append(a.pic.Refs, r)
		}
	}
	if h.typ != sliceI && !d.buildRefLists(h) {
		return errStream
	}
	s, p := d.sps, d.ppss[h.ppsID]
	sl := AccelSlice{
		DataOffset:  2 + n.escaped(h.dataOffset),
		SegmentAddr: h.segmentAddr, Dependent: h.dependent, Type: h.typ, ColourPlane: h.colourPlane,
		SAOLuma: h.saoLuma, SAOChroma: h.saoChroma, MvdL1Zero: h.mvdL1Zero, CabacInit: h.cabacInit,
		TemporalMVP: h.temporalMVP, DeblockingDisabled: h.deblockingDisabled, ColFromL0: h.colFromL0,
		LoopFilterAcross: h.loopFilterAcross, ColRefIdx: h.colRefIdx, NumRefIdx: h.numRefIdx,
		QPDelta: h.qpDelta, CbQPOffset: h.cbQPOffset, CrQPOffset: h.crQPOffset,
		BetaOffsetDiv2: h.betaOffset / 2, TcOffsetDiv2: h.tcOffset / 2, MaxMergeCand: h.maxMergeCand,
	}
	if h.typ == sliceI {
		sl.NumRefIdx = [2]int{}
	}
	for l := range 2 {
		if l >= numLists(h.typ) {
			break
		}
		for _, rp := range d.pic.refList[l] {
			i := -1
			for j, c := range a.refs {
				if c == rp {
					i = j
					break
				}
			}
			if i < 0 {
				return errStream
			}
			sl.RefList[l] = append(sl.RefList[l], i)
		}
	}
	if h.typ == sliceP && p.weightedPred || h.typ == sliceB && p.weightedBipred {
		sl.Log2WeightDenom, sl.Log2WeightDenomC = h.log2WeightDenom, h.log2WeightDenomC
		for l := range numLists(h.typ) {
			for i := range h.numRefIdx[l] {
				w := &h.weights[l][i]
				sl.Weights[l] = append(sl.Weights[l], AccelWeight{
					LumaWeight: w.lumaWeight, LumaOffset: w.lumaOffset >> (s.bitDepth - 8),
					ChromaWeight: w.chromaWeight,
					ChromaOffset: [2]int{w.chromaOffset[0] >> (s.bitDepthC - 8), w.chromaOffset[1] >> (s.bitDepthC - 8)},
				})
			}
		}
	}
	a.data = append(a.data, n.raw...)
	a.ends = append(a.ends, len(a.data))
	a.slices = append(a.slices, sl)
	return nil
}

// numLists is how many reference lists a slice type has.
func numLists(typ int) int {
	switch typ {
	case sliceB:
		return 2
	case sliceP:
		return 1
	}
	return 0
}

func contains(list []*picture, p *picture) bool {
	for _, c := range list {
		if c == p {
			return true
		}
	}
	return false
}

// accelPicture is the picture's parameters, from its first slice.
func (d *Decoder) accelPicture(h *sliceHeader, n *nalUnit) *AccelPicture {
	s, p := d.sps, d.ppss[h.ppsID]
	a := &AccelPicture{
		Surface: d.cur.surface, POC: d.cur.poc,
		IRAP: isIRAP(n.typ), IDR: n.typ == nalIDRWRADL || n.typ == nalIDRNLP,

		Width: s.width, Height: s.height, ChromaFormat: s.chromaFormat, SeparateColourPlane: s.sepColour,
		BitDepth: s.bitDepth, BitDepthC: s.bitDepthC, Log2MaxPOCLsb: s.log2MaxPOCLsb, MaxDecPicBuffering: s.maxDecPicBuf,
		Log2MinCb: s.log2MinCb, Log2Ctb: s.log2Ctb, Log2MinTb: s.log2MinTb, Log2MaxTb: s.log2MaxTb,
		MaxTrDepthInter: s.maxTrDepthInter, MaxTrDepthIntra: s.maxTrDepthIntra,
		PCM: s.pcm, PCMBits: s.pcmBits, PCMBitsC: s.pcmBitsC, Log2MinPCM: s.log2MinPCM, Log2MaxPCM: s.log2MaxPCM,
		PCMLoopFilterDisabled: s.pcmLoopFilterDisabled, AMP: s.amp, SAO: s.sao, TemporalMVP: s.temporalMVP,
		StrongIntraSmoothing: s.strongIntraSmoothing, LongTermRefsPresent: s.longTermRefsPresent,
		NumShortTermRPS: len(s.stRPS), NumLongTermRefsSPS: len(s.ltPOCLsb),

		DependentSlices: p.dependentSlices, OutputFlagPresent: p.outputFlagPresent,
		NumExtraSliceHeaderBits: p.numExtraSliceHeaderBits, SignDataHiding: p.signDataHiding,
		CabacInitPresent: p.cabacInitPresent, NumRefIdxDefault: p.numRefIdxDefault, InitQP: p.initQP,
		ConstrainedIntraPred: p.constrainedIntraPred, TransformSkip: p.transformSkip, CuQPDelta: p.cuQPDeltaEnabled,
		DiffCuQPDeltaDepth: p.diffCuQPDeltaDepth, CbQPOffset: p.cbQPOffset, CrQPOffset: p.crQPOffset,
		SliceChromaQPOffsets: p.sliceChromaQPOffsets, WeightedPred: p.weightedPred, WeightedBipred: p.weightedBipred,
		TransquantBypass: p.transquantBypass, Tiles: p.tiles, EntropySync: p.entropySync,
		LoopFilterAcrossTiles: p.loopFilterAcrossTiles, LoopFilterAcrossSlices: p.loopFilterAcrossSlices,
		DeblockingOverride: p.deblockingOverride, DeblockingDisabled: p.deblockingDisabled,
		BetaOffsetDiv2: p.betaOffset / 2, TcOffsetDiv2: p.tcOffset / 2, ListsModification: p.listsModification,
		Log2ParMrgLevel: p.log2ParMrgLevel, SliceHeaderExtension: p.sliceHeaderExtension,
	}
	if !h.shortTermRefSPS {
		a.StRPSBits = h.stRPSBits
	}
	if p.tiles {
		for i := range len(p.colBd) - 1 {
			a.ColumnWidths = append(a.ColumnWidths, p.colBd[i+1]-p.colBd[i])
		}
		for i := range len(p.rowBd) - 1 {
			a.RowHeights = append(a.RowHeights, p.rowBd[i+1]-p.rowBd[i])
		}
	}
	if s.scalingListEnabled {
		sl := &s.scaling
		if p.scalingListPresent {
			sl = &p.scaling
		}
		l := &ScalingLists{}
		for m := range 6 {
			copy(l.L4[m][:], sl.lists[0][m][:16])
			l.L8[m] = sl.lists[1][m]
			l.L16[m] = sl.lists[2][m]
			l.DC16[m] = sl.dc[0][m]
		}
		for m := range 2 {
			l.L32[m] = sl.lists[3][3*m]
			l.DC32[m] = sl.dc[1][3*m]
		}
		a.Scaling = l
	}
	return a
}

// accelDecode sends the current picture to the accelerator.
func (d *Decoder) accelDecode() error {
	a := &d.acc
	defer func() {
		a.pic = nil
		a.slices = a.slices[:0]
		a.data = a.data[:0]
		a.ends = a.ends[:0]
	}()
	if a.pic == nil {
		return nil // no slice decoded
	}
	start := 0
	for i := range a.slices {
		a.slices[i].Data = a.data[start:a.ends[i]]
		start = a.ends[i]
	}
	return d.accel.DecodePicture(a.pic, a.slices)
}
