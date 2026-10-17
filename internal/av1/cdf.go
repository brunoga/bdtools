package av1

// The CDFs a frame's tiles code with: each tile starts from the defaults
// (the coefficients' chosen by the frame's q index, as init_coeff_cdfs
// does) and adapts its own copy. Only the intra frame's are here.

import "math"

type cdfs struct {
	yMode           [INTRA_MODE_CONTEXTS][INTRA_MODE_CONTEXTS][INTRA_MODES + 1]uint16
	uvCfl           [INTRA_MODES][UV_INTRA_MODES_CFL_ALLOWED + 1]uint16
	uvNoCfl         [INTRA_MODES][UV_INTRA_MODES_CFL_NOT_ALLOWED + 1]uint16
	angleDelta      [DIRECTIONAL_MODES][2*MAX_ANGLE_DELTA + 2]uint16
	partW8          [PARTITION_CONTEXTS][5]uint16
	partW16         [PARTITION_CONTEXTS][11]uint16
	partW32         [PARTITION_CONTEXTS][11]uint16
	partW64         [PARTITION_CONTEXTS][11]uint16
	partW128        [PARTITION_CONTEXTS][9]uint16
	tx8             [TX_SIZE_CONTEXTS][MAX_TX_DEPTH + 1]uint16
	tx16            [TX_SIZE_CONTEXTS][MAX_TX_DEPTH + 2]uint16
	tx32            [TX_SIZE_CONTEXTS][MAX_TX_DEPTH + 2]uint16
	tx64            [TX_SIZE_CONTEXTS][MAX_TX_DEPTH + 2]uint16
	skip            [SKIP_CONTEXTS][3]uint16
	filterIntra     [BLOCK_SIZES][3]uint16
	filterIntraMode [6]uint16
	intraTxSet1     [2][INTRA_MODES][8]uint16
	intraTxSet2     [3][INTRA_MODES][6]uint16
	cflSign         [CFL_JOINT_SIGNS + 1]uint16
	cflAlpha        [CFL_ALPHA_CONTEXTS][CFL_ALPHABET_SIZE + 1]uint16

	txbSkip      [TX_SIZES][TXB_SKIP_CONTEXTS][3]uint16
	eobPt16      [PLANE_TYPES][2][6]uint16
	eobPt32      [PLANE_TYPES][2][7]uint16
	eobPt64      [PLANE_TYPES][2][8]uint16
	eobPt128     [PLANE_TYPES][2][9]uint16
	eobPt256     [PLANE_TYPES][2][10]uint16
	eobPt512     [PLANE_TYPES][11]uint16
	eobPt1024    [PLANE_TYPES][12]uint16
	eobExtra     [TX_SIZES][PLANE_TYPES][EOB_COEF_CONTEXTS][3]uint16
	dcSign       [PLANE_TYPES][DC_SIGN_CONTEXTS][3]uint16
	coeffBaseEob [TX_SIZES][PLANE_TYPES][SIG_COEF_CONTEXTS_EOB][4]uint16
	coeffBase    [TX_SIZES][PLANE_TYPES][SIG_COEF_CONTEXTS][5]uint16
	coeffBr      [TX_SIZES][PLANE_TYPES][LEVEL_CONTEXTS][BR_CDF_SIZE + 1]uint16
}

// coeffCDFIndex is init_coeff_cdfs' choice of defaults by q index.
func coeffCDFIndex(qIndex int) int {
	switch {
	case qIndex <= 20:
		return 0
	case qIndex <= 60:
		return 1
	case qIndex <= 120:
		return 2
	}
	return 3
}

// newCDFs gives a frame's starting CDFs.
func newCDFs(qIndex int) *cdfs {
	q := coeffCDFIndex(qIndex)
	return &cdfs{
		yMode: defaultIntraFrameYModeCdf, uvCfl: defaultUvModeCflAllowedCdf, uvNoCfl: defaultUvModeCflNotAllowedCdf,
		angleDelta: defaultAngleDeltaCdf,
		partW8:     defaultPartitionW8Cdf, partW16: defaultPartitionW16Cdf, partW32: defaultPartitionW32Cdf,
		partW64: defaultPartitionW64Cdf, partW128: defaultPartitionW128Cdf,
		tx8: defaultTx8x8Cdf, tx16: defaultTx16x16Cdf, tx32: defaultTx32x32Cdf, tx64: defaultTx64x64Cdf,
		skip: defaultSkipCdf, filterIntra: defaultFilterIntraCdf, filterIntraMode: defaultFilterIntraModeCdf,
		intraTxSet1: defaultIntraTxTypeSet1Cdf, intraTxSet2: defaultIntraTxTypeSet2Cdf,
		cflSign: defaultCflSignCdf, cflAlpha: defaultCflAlphaCdf,

		txbSkip: defaultTxbSkipCdf[q], eobPt16: defaultEobPt16Cdf[q], eobPt32: defaultEobPt32Cdf[q],
		eobPt64: defaultEobPt64Cdf[q], eobPt128: defaultEobPt128Cdf[q], eobPt256: defaultEobPt256Cdf[q],
		eobPt512: defaultEobPt512Cdf[q], eobPt1024: defaultEobPt1024Cdf[q], eobExtra: defaultEobExtraCdf[q],
		dcSign: defaultDcSignCdf[q], coeffBaseEob: defaultCoeffBaseEobCdf[q], coeffBase: defaultCoeffBaseCdf[q],
		coeffBr: defaultCoeffBrCdf[q],
	}
}

// Rates are in 1/costScale bits.
const costScale = 256

// probCost is the rate of a symbol of probability p/32768.
var probCost = func() []int32 {
	t := make([]int32, 32769)
	t[0] = 30 * costScale
	for p := 1; p <= 32768; p++ {
		t[p] = int32(math.Round(-math.Log2(float64(p)/32768) * costScale))
	}
	return t
}()

// symbolCost is the rate of coding s with a CDF (spec form).
func symbolCost(s int, cdf []uint16) int32 {
	lo := uint16(0)
	if s > 0 {
		lo = cdf[s-1]
	}
	return probCost[cdf[s]-lo]
}
