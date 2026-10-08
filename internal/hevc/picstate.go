package hevc

// Prediction modes.
const (
	modeInter = 0
	modeIntra = 1
	modeSkip  = 2
)

// Partition modes.
const (
	part2Nx2N = iota
	part2NxN
	partNx2N
	partNxN
	part2NxnU
	part2NxnD
	partnLx2N
	partnRx2N
)

// saoParams is a CTB's SAO for one component.
type saoParams struct {
	typ    uint8 // 0 none, 1 band, 2 edge
	class  uint8 // edge offset class
	band   uint8 // band position
	offset [4]int16
}

// picState is what decoding a picture keeps: its reference sets and lists,
// and per-block information for prediction, the loop filters and the
// pictures that refer to it.
type picState struct {
	stCurrBefore, stCurrAfter, ltCurr []*picture
	refList                           [2][]*picture
	refIsLT                           [2][]bool

	// Per 4x4 block (luma samples), picture wide.
	w4, h4    int
	predMode  []uint8 // modeInter, modeIntra, modeSkip
	intraMode []uint8 // luma intra prediction mode
	qpY       []int8
	noFilter  []bool  // PCM with its loop filter off, or transquant bypass
	tuEdgeV   []uint8 // a transform edge on the block's left: 1, prediction edge: 2
	tuEdgeH   []uint8 // on its top
	cbfLuma   []bool  // the transform block covering it has luma coefficients
	ctDepth   []uint8 // per minimum CB
	// Per CTB.
	sao        [][3]saoParams
	ctbSlice   []int // index into slices
	ctbDecoded []bool

	slices []*sliceHeader

	// WPP and dependent slice storage of the context variables.
	wppRowCtx [][numContexts]uint8 // per CTB row, without tiles
}

func (d *Decoder) beginPicture() {
	s := d.sps
	ps := &d.pic
	w4 := (s.ctbW << s.log2Ctb) >> 2
	h4 := (s.ctbH << s.log2Ctb) >> 2
	if ps.w4 != w4 || ps.h4 != h4 {
		ps.w4, ps.h4 = w4, h4
		n := w4 * h4
		ps.predMode = make([]uint8, n)
		ps.intraMode = make([]uint8, n)
		ps.qpY = make([]int8, n)
		ps.noFilter = make([]bool, n)
		ps.tuEdgeV = make([]uint8, n)
		ps.tuEdgeH = make([]uint8, n)
		ps.cbfLuma = make([]bool, n)
	}
	if len(ps.ctDepth) != s.minCbW*s.minCbH {
		ps.ctDepth = make([]uint8, s.minCbW*s.minCbH)
	}
	nCtb := s.ctbW * s.ctbH
	if len(ps.sao) != nCtb {
		ps.sao = make([][3]saoParams, nCtb)
		ps.ctbSlice = make([]int, nCtb)
		ps.ctbDecoded = make([]bool, nCtb)
		ps.wppRowCtx = make([][numContexts]uint8, s.ctbH)
	}
	clear(ps.ctbDecoded)
	clear(ps.tuEdgeV)
	clear(ps.tuEdgeH)
	clear(ps.noFilter)
	clear(ps.cbfLuma)
	clear(ps.sao)
	ps.slices = ps.slices[:0]
	clear(d.cur.mvf)
}

func (d *Decoder) endPicture() {
	d.loopFilter()
}
