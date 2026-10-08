// Code generated from the standard's CABAC initialization tables; DO NOT EDIT.

package hevc

// CABAC context offsets, one block of contexts a syntax element.
const (
	ctxSaoMergeFlag                = 0
	ctxSaoTypeIdx                  = 1
	ctxSaoEoClass                  = 2
	ctxSaoBandPosition             = 2
	ctxSaoOffsetAbs                = 2
	ctxSaoOffsetSign               = 2
	ctxEndOfSliceFlag              = 2
	ctxSplitCodingUnitFlag         = 2
	ctxCuTransquantBypassFlag      = 5
	ctxSkipFlag                    = 6
	ctxCuQpDelta                   = 9
	ctxPredModeFlag                = 12
	ctxPartMode                    = 13
	ctxPcmFlag                     = 17
	ctxPrevIntraLumaPredFlag       = 17
	ctxMpmIdx                      = 18
	ctxRemIntraLumaPredMode        = 18
	ctxIntraChromaPredMode         = 18
	ctxMergeFlag                   = 20
	ctxMergeIdx                    = 21
	ctxInterPredIdc                = 22
	ctxRefIdxL0                    = 27
	ctxRefIdxL1                    = 29
	ctxAbsMvdGreater0Flag          = 31
	ctxAbsMvdGreater1Flag          = 33
	ctxAbsMvdMinus2                = 35
	ctxMvdSignFlag                 = 35
	ctxMvpLxFlag                   = 35
	ctxNoResidualDataFlag          = 36
	ctxSplitTransformFlag          = 37
	ctxCbfLuma                     = 40
	ctxCbfCbCr                     = 42
	ctxTransformSkipFlag           = 47
	ctxExplicitRdpcmFlag           = 49
	ctxExplicitRdpcmDirFlag        = 51
	ctxLastSignificantCoeffXPrefix = 53
	ctxLastSignificantCoeffYPrefix = 71
	ctxLastSignificantCoeffXSuffix = 89
	ctxLastSignificantCoeffYSuffix = 89
	ctxSignificantCoeffGroupFlag   = 89
	ctxSignificantCoeffFlag        = 93
	ctxCoeffAbsLevelGreater1Flag   = 137
	ctxCoeffAbsLevelGreater2Flag   = 161
	ctxCoeffAbsLevelRemaining      = 167
	ctxCoeffSignFlag               = 167
	ctxLog2ResScaleAbs             = 167
	ctxResScaleSignFlag            = 175
	ctxCuChromaQpOffsetFlag        = 177
	ctxCuChromaQpOffsetIdx         = 178
	numContexts                    = 179
)

// cabacInit is each context's initValue by initType.
var cabacInit = [3][numContexts]uint8{
	{
		153, 200, 139, 141, 157, 154, 154, 154, 154, 154, 154, 154, 154, 184, 154, 154,
		154, 184, 63, 139, 154, 154, 154, 154, 154, 154, 154, 154, 154, 154, 154, 154,
		154, 154, 154, 154, 154, 153, 138, 138, 111, 141, 94, 138, 182, 154, 154, 139,
		139, 139, 139, 139, 139, 110, 110, 124, 125, 140, 153, 125, 127, 140, 109, 111,
		143, 127, 111, 79, 108, 123, 63, 110, 110, 124, 125, 140, 153, 125, 127, 140,
		109, 111, 143, 127, 111, 79, 108, 123, 63, 91, 171, 134, 141, 111, 111, 125,
		110, 110, 94, 124, 108, 124, 107, 125, 141, 179, 153, 125, 107, 125, 141, 179,
		153, 125, 107, 125, 141, 179, 153, 125, 140, 139, 182, 182, 152, 136, 152, 136,
		153, 136, 139, 111, 136, 139, 111, 141, 111, 140, 92, 137, 138, 140, 152, 138,
		139, 153, 74, 149, 92, 139, 107, 122, 152, 140, 179, 166, 182, 140, 227, 122,
		197, 138, 153, 136, 167, 152, 152, 154, 154, 154, 154, 154, 154, 154, 154, 154,
		154, 154, 154,
	},
	{
		153, 185, 107, 139, 126, 154, 197, 185, 201, 154, 154, 154, 149, 154, 139, 154,
		154, 154, 152, 139, 110, 122, 95, 79, 63, 31, 31, 153, 153, 153, 153, 140,
		198, 140, 198, 168, 79, 124, 138, 94, 153, 111, 149, 107, 167, 154, 154, 139,
		139, 139, 139, 139, 139, 125, 110, 94, 110, 95, 79, 125, 111, 110, 78, 110,
		111, 111, 95, 94, 108, 123, 108, 125, 110, 94, 110, 95, 79, 125, 111, 110,
		78, 110, 111, 111, 95, 94, 108, 123, 108, 121, 140, 61, 154, 155, 154, 139,
		153, 139, 123, 123, 63, 153, 166, 183, 140, 136, 153, 154, 166, 183, 140, 136,
		153, 154, 166, 183, 140, 136, 153, 154, 170, 153, 123, 123, 107, 121, 107, 121,
		167, 151, 183, 140, 151, 183, 140, 140, 140, 154, 196, 196, 167, 154, 152, 167,
		182, 182, 134, 149, 136, 153, 121, 136, 137, 169, 194, 166, 167, 154, 167, 137,
		182, 107, 167, 91, 122, 107, 167, 154, 154, 154, 154, 154, 154, 154, 154, 154,
		154, 154, 154,
	},
	{
		153, 160, 107, 139, 126, 154, 197, 185, 201, 154, 154, 154, 134, 154, 139, 154,
		154, 183, 152, 139, 154, 137, 95, 79, 63, 31, 31, 153, 153, 153, 153, 169,
		198, 169, 198, 168, 79, 224, 167, 122, 153, 111, 149, 92, 167, 154, 154, 139,
		139, 139, 139, 139, 139, 125, 110, 124, 110, 95, 94, 125, 111, 111, 79, 125,
		126, 111, 111, 79, 108, 123, 93, 125, 110, 124, 110, 95, 94, 125, 111, 111,
		79, 125, 126, 111, 111, 79, 108, 123, 93, 121, 140, 61, 154, 170, 154, 139,
		153, 139, 123, 123, 63, 124, 166, 183, 140, 136, 153, 154, 166, 183, 140, 136,
		153, 154, 166, 183, 140, 136, 153, 154, 170, 153, 138, 138, 122, 121, 122, 121,
		167, 151, 183, 140, 151, 183, 140, 140, 140, 154, 196, 167, 167, 154, 152, 167,
		182, 182, 134, 149, 136, 153, 121, 136, 122, 169, 208, 166, 167, 154, 152, 167,
		182, 107, 167, 91, 107, 107, 167, 154, 154, 154, 154, 154, 154, 154, 154, 154,
		154, 154, 154,
	},
}
