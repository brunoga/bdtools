//go:build (linux || windows) && (amd64 || arm64)

package hwenc

// NVENC API 12.0 (nv-codec-headers n12.0.16.1): versions, struct sizes and
// field offsets, printed by gcc with offsetof from nvEncodeAPI.h.
// TestNVENCLayout re-derives them when a C compiler and the header are at
// hand. The structs have the same layout on 64-bit Linux and Windows.
const (
	nvencAPIVersion           = 0xc
	nvFunctionListVer         = 0x7002000c
	nvOpenSessionExParamsVer  = 0x7001000c
	nvInitializeParamsVer     = 0xf005000c
	nvConfigVer               = 0xf008000c
	nvPresetConfigVer         = 0xf004000c
	nvCreateInputBufferVer    = 0x7001000c
	nvCreateBitstreamBufVer   = 0x7001000c
	nvLockInputBufferVer      = 0x7001000c
	nvLockBitstreamVer        = 0x7002000c
	nvPicParamsVer            = 0xf006000c
	nvRCParamsVer             = 0x7001000c
	nvSizeFunctionList        = 2552
	nvFnGetPresetConfigEx     = 320
	nvFnInitializeEncoder     = 96
	nvFnCreateInputBuffer     = 104
	nvFnDestroyInputBuffer    = 112
	nvFnCreateBitstreamBuffer = 120
	nvFnDestroyBitstreamBuf   = 128
	nvFnEncodePicture         = 136
	nvFnLockBitstream         = 144
	nvFnUnlockBitstream       = 152
	nvFnLockInputBuffer       = 160
	nvFnUnlockInputBuffer     = 168
	nvFnDestroyEncoder        = 224
	nvFnOpenSessionEx         = 240
	nvFnGetLastErrorString    = 304

	nvSizeOpenSessionExParams = 1552
	nvOSDeviceType            = 4
	nvOSDevice                = 8
	nvOSAPIVersion            = 24

	nvSizePresetConfig = 5128
	nvPCPresetCfg      = 8

	nvSizeConfig            = 3584
	nvCfgGOPLength          = 20
	nvCfgFrameIntervalP     = 24
	nvCfgRCParams           = 40
	nvCfgCodecConfig        = 168
	nvRCRateControlMode     = 4
	nvRCConstQP             = 8
	nvH264IDRPeriod         = 8
	nvHEVCIDRPeriod         = 20
	nvSizeInitializeParams  = 1808
	nvIPEncodeGUID          = 4
	nvIPPresetGUID          = 20
	nvIPEncodeWidth         = 36
	nvIPEncodeHeight        = 40
	nvIPDarWidth            = 44
	nvIPDarHeight           = 48
	nvIPFrameRateNum        = 52
	nvIPFrameRateDen        = 56
	nvIPEnablePTD           = 64
	nvIPEncodeConfig        = 88
	nvIPMaxEncodeWidth      = 96
	nvIPMaxEncodeHeight     = 100
	nvIPTuningInfo          = 136
	nvSizeCreateInputBuffer = 776
	nvCIBWidth              = 4
	nvCIBHeight             = 8
	nvCIBBufferFmt          = 16
	nvCIBInputBuffer        = 24
	nvSizeCreateBitstream   = 776
	nvCBBBitstreamBuffer    = 16
	nvSizeLockInputBuffer   = 1544
	nvLIBInputBuffer        = 8
	nvLIBDataPtr            = 16
	nvLIBPitch              = 24
	nvSizeLockBitstream     = 1544
	nvLBOutputBitstream     = 8
	nvLBSize                = 36
	nvLBDataPtr             = 56
	nvSizePicParams         = 3360
	nvPPInputWidth          = 4
	nvPPInputHeight         = 8
	nvPPInputPitch          = 12
	nvPPEncodePicFlags      = 16
	nvPPInputTimeStamp      = 24
	nvPPInputBuffer         = 40
	nvPPOutputBitstream     = 48
	nvPPBufferFmt           = 64
	nvPPPictureStruct       = 68

	nvDeviceTypeCUDA    = 1
	nvBufferFormatNV12  = 1
	nvRCConstQPMode     = 0
	nvPicStructFrame    = 1
	nvPicFlagEOS        = 0x8
	nvTuningHighQuality = 1
	nvErrNeedMoreInput  = 17
	nvSuccess           = 0
)

var (
	nvCodecH264GUID = guid(0x6bc82762, 0x4e63, 0x4ca4, [8]byte{0xaa, 0x85, 0x1e, 0x50, 0xf3, 0x21, 0xf6, 0xbf})
	nvCodecHEVCGUID = guid(0x790cdc88, 0x4522, 0x4d7b, [8]byte{0x94, 0x25, 0xbd, 0xa9, 0x97, 0x5f, 0x76, 0x03})
	nvPresetP4GUID  = guid(0x90a7b826, 0xdf06, 0x4862, [8]byte{0xb9, 0xd2, 0xcd, 0x6d, 0x73, 0xa0, 0x86, 0x81})
)
