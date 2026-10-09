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

// refState is the current picture's reference picture sets and a slice's
// reference lists.
type refState struct {
	stCurrBefore, stCurrAfter, ltCurr []*picture
	refList                           [2][]*picture
	refIsLT                           [2][]bool
}

// picState is what decoding a picture keeps: per-block information for
// prediction, the loop filters and the pictures that refer to it.
type picState struct {
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
