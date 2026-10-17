package av1

// The CDFs a frame's tiles code with: each tile starts from the defaults
// (the coefficients' chosen by the frame's q index, as init_coeff_cdfs
// does) and adapts its own copy. Only the intra frame's are here.

import (
	"math"
	"reflect"
	"unsafe"
)

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

	// inter frames'
	yModeInter   [BLOCK_SIZE_GROUPS][INTRA_MODES + 1]uint16
	isInter      [IS_INTER_CONTEXTS][3]uint16
	newMv        [NEW_MV_CONTEXTS][3]uint16
	zeroMv       [ZERO_MV_CONTEXTS][3]uint16
	refMv        [REF_MV_CONTEXTS][3]uint16
	drlMode      [DRL_MODE_CONTEXTS][3]uint16
	singleRef    [REF_CONTEXTS][SINGLE_REFS - 1][3]uint16
	txfmSplit    [TXFM_PARTITION_CONTEXTS][3]uint16
	skipMode     [SKIP_MODE_CONTEXTS][3]uint16
	deltaQ       [DELTA_Q_SMALL + 2]uint16
	compMode     [COMP_INTER_CONTEXTS][3]uint16
	compRefType  [COMP_REF_TYPE_CONTEXTS][3]uint16
	compRef      [REF_CONTEXTS][FWD_REFS - 1][3]uint16
	compBwdRef   [REF_CONTEXTS][BWD_REFS - 1][3]uint16
	compoundMode [COMPOUND_MODE_CONTEXTS][COMPOUND_MODES + 1]uint16
	interTxSet1  [2][17]uint16
	interTxSet2  [13]uint16
	interTxSet3  [4][3]uint16
	mvJoint      [MV_CONTEXTS][MV_JOINTS + 1]uint16
	mvClass      [MV_CONTEXTS][2][MV_CLASSES + 1]uint16
	mvClass0Bit  [MV_CONTEXTS][2][3]uint16
	mvFr         [MV_CONTEXTS][2][MV_JOINTS + 1]uint16
	mvClass0Fr   [MV_CONTEXTS][2][CLASS0_SIZE][MV_JOINTS + 1]uint16
	mvClass0Hp   [MV_CONTEXTS][2][3]uint16
	mvSign       [MV_CONTEXTS][2][3]uint16
	mvBit        [MV_CONTEXTS][2][MV_OFFSET_BITS][3]uint16
	mvHp         [MV_CONTEXTS][2][3]uint16

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

	// loop restoration's
	useWiener       [3]uint16
	useSgrproj      [3]uint16
	restorationType [RESTORE_SWITCHABLE + 1]uint16
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
	c := &cdfs{
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

		yModeInter: defaultYModeCdf, isInter: defaultIsInterCdf, newMv: defaultNewMvCdf, zeroMv: defaultZeroMvCdf,
		refMv: defaultRefMvCdf, drlMode: defaultDrlModeCdf, singleRef: defaultSingleRefCdf, txfmSplit: defaultTxfmSplitCdf,
		skipMode: defaultSkipModeCdf, deltaQ: defaultDeltaQCdf, compMode: defaultCompModeCdf, compRefType: defaultCompRefTypeCdf, compRef: defaultCompRefCdf,
		compBwdRef: defaultCompBwdRefCdf, compoundMode: defaultCompoundModeCdf,
		interTxSet1: defaultInterTxTypeSet1Cdf, interTxSet2: defaultInterTxTypeSet2Cdf, interTxSet3: defaultInterTxTypeSet3Cdf,

		useWiener: defaultUseWienerCdf, useSgrproj: defaultUseSgrprojCdf, restorationType: defaultRestorationTypeCdf,
	}
	for i := range MV_CONTEXTS {
		c.mvJoint[i] = defaultMvJointCdf
		c.mvClass[i] = defaultMvClassCdf
		c.mvFr[i] = defaultMvFrCdf
		c.mvClass0Fr[i] = defaultMvClass0FrCdf
		for comp := range 2 {
			c.mvClass0Bit[i][comp] = defaultMvClass0BitCdf
			c.mvClass0Hp[i][comp] = defaultMvClass0HpCdf
			c.mvSign[i][comp] = defaultMvSignCdf
			c.mvBit[i][comp] = defaultMvBitCdf
			c.mvHp[i][comp] = defaultMvHpCdf
		}
	}
	return c
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

// cdfCounts are where, in cdfs seen as uint16s, each CDF's count is: the
// last entry of each innermost array.
var cdfCounts = func() []int {
	var out []int
	typ := reflect.TypeFor[cdfs]()
	for i := range typ.NumField() {
		fld := typ.Field(i)
		t, total := fld.Type, 1
		for t.Kind() == reflect.Array {
			total *= t.Len()
			if t.Elem().Kind() != reflect.Array {
				break
			}
			t = t.Elem()
		}
		row := t.Len()
		base := int(fld.Offset / 2)
		for r := range total / row {
			out = append(out, base+r*row+row-1)
		}
	}
	return out
}()

// loaded is a copy of saved frame CDFs as load_cdfs gives them: every
// count 0.
func (c *cdfs) loaded() *cdfs {
	d := *c
	flat := unsafe.Slice((*uint16)(unsafe.Pointer(&d)), unsafe.Sizeof(d)/2)
	for _, i := range cdfCounts {
		flat[i] = 0
	}
	return &d
}
