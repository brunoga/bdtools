//go:build linux && amd64

package hwenc

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The NVENC and VAAPI layouts are numbers gcc printed from the C headers.
// These tests print them again and compare, when a C compiler and the
// headers are at hand: point MVC_NVENC_HEADERS at nv-codec-headers' include
// directory (n12.0.16.1) and MVC_LIBVA_HEADERS at libva's include
// directories (a list), or install them system-wide.

// cRun compiles and runs a C program, skipping without a compiler.
func cRun(t *testing.T, src string, includes []string) string {
	t.Helper()
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler")
	}
	dir := t.TempDir()
	c := filepath.Join(dir, "layout.c")
	if err := os.WriteFile(c, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "layout")
	args := []string{"-o", bin, c}
	for _, i := range includes {
		args = append(args, "-I", i)
	}
	if out, err := exec.CommandContext(t.Context(), cc, args...).CombinedOutput(); err != nil { //nolint:gosec // test
		t.Fatalf("compiling: %v\n%s", err, out)
	}
	out, err := exec.CommandContext(t.Context(), bin).Output() //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// headerDirs returns the directories that hold a header, from an
// environment variable or the system's include directory.
func headerDirs(t *testing.T, env, header string, system ...string) []string {
	t.Helper()
	dirs := filepath.SplitList(os.Getenv(env))
	if len(dirs) == 0 {
		dirs = system
	}
	for _, d := range dirs {
		if _, err := os.Stat(filepath.Join(d, header)); err == nil {
			return dirs
		}
	}
	t.Skipf("no %s (set %s)", header, env)
	return nil
}

func TestNVENCLayout(t *testing.T) {
	inc := headerDirs(t, "MVC_NVENC_HEADERS", "ffnvcodec/nvEncodeAPI.h", "/usr/include", "/usr/local/include")
	var src strings.Builder
	src.WriteString("#include <stdio.h>\n#include <stddef.h>\n#include <ffnvcodec/nvEncodeAPI.h>\n")
	src.WriteString("static void guid(const GUID *g) { const unsigned char *b = (const void *)g; for (int i = 0; i < 16; i++) printf(\"%02x\", b[i]); printf(\"\\n\"); }\n")
	src.WriteString("int main(void) {\n\tprintf(\"%d.%d\\n\", NVENCAPI_MAJOR_VERSION, NVENCAPI_MINOR_VERSION);\n")
	for _, f := range nvencFacts {
		fmt.Fprintf(&src, "\tprintf(\"%%lld\\n\", (long long)(%s));\n", f.expr)
	}
	for _, g := range nvencGUIDs {
		fmt.Fprintf(&src, "\t{ GUID g = %s; guid(&g); }\n", g.expr)
	}
	src.WriteString("\treturn 0;\n}\n")
	lines := strings.Split(strings.TrimSpace(cRun(t, src.String(), inc)), "\n")
	if lines[0] != "12.0" {
		t.Skipf("the headers are API %s; the layout is for 12.0", lines[0])
	}
	for i, f := range nvencFacts {
		if want := lines[1+i]; fmt.Sprint(f.got) != want {
			t.Errorf("%s = %d, the header says %s (%s)", f.name, f.got, want, f.expr)
		}
	}
	for i, g := range nvencGUIDs {
		if want := lines[1+len(nvencFacts)+i]; fmt.Sprintf("%x", g.got) != want {
			t.Errorf("%s = %x, the header says %s", g.expr, g.got, want)
		}
	}
}

func TestVAAPILayout(t *testing.T) {
	inc := headerDirs(t, "MVC_LIBVA_HEADERS", "va/va.h", "/usr/include", "/usr/local/include")
	src, err := os.ReadFile("testdata/vaoff.c")
	if err != nil {
		t.Fatal(err)
	}
	got, err := format.Source([]byte(cRun(t, string(src), inc)))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("vaapi_layout.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("vaapi_layout.go is not what the headers give; regenerate it:\n%s", got)
	}
}

var nvencFacts = []struct {
	name string
	got  int64
	expr string
}{
	{"nvencAPIVersion", nvencAPIVersion, "NVENCAPI_VERSION"},
	{"nvFunctionListVer", nvFunctionListVer, "NV_ENCODE_API_FUNCTION_LIST_VER"},
	{"nvOpenSessionExParamsVer", nvOpenSessionExParamsVer, "NV_ENC_OPEN_ENCODE_SESSION_EX_PARAMS_VER"},
	{"nvInitializeParamsVer", nvInitializeParamsVer, "NV_ENC_INITIALIZE_PARAMS_VER"},
	{"nvConfigVer", nvConfigVer, "NV_ENC_CONFIG_VER"},
	{"nvPresetConfigVer", nvPresetConfigVer, "NV_ENC_PRESET_CONFIG_VER"},
	{"nvCreateInputBufferVer", nvCreateInputBufferVer, "NV_ENC_CREATE_INPUT_BUFFER_VER"},
	{"nvCreateBitstreamBufVer", nvCreateBitstreamBufVer, "NV_ENC_CREATE_BITSTREAM_BUFFER_VER"},
	{"nvLockInputBufferVer", nvLockInputBufferVer, "NV_ENC_LOCK_INPUT_BUFFER_VER"},
	{"nvLockBitstreamVer", nvLockBitstreamVer, "NV_ENC_LOCK_BITSTREAM_VER"},
	{"nvPicParamsVer", nvPicParamsVer, "NV_ENC_PIC_PARAMS_VER"},
	{"nvRCParamsVer", nvRCParamsVer, "NV_ENC_RC_PARAMS_VER"},
	{"nvSizeFunctionList", nvSizeFunctionList, "sizeof(NV_ENCODE_API_FUNCTION_LIST)"},
	{"nvFnGetPresetConfigEx", nvFnGetPresetConfigEx, "offsetof(NV_ENCODE_API_FUNCTION_LIST, nvEncGetEncodePresetConfigEx)"},
	{"nvFnInitializeEncoder", nvFnInitializeEncoder, "offsetof(NV_ENCODE_API_FUNCTION_LIST, nvEncInitializeEncoder)"},
	{"nvFnCreateInputBuffer", nvFnCreateInputBuffer, "offsetof(NV_ENCODE_API_FUNCTION_LIST, nvEncCreateInputBuffer)"},
	{"nvFnDestroyInputBuffer", nvFnDestroyInputBuffer, "offsetof(NV_ENCODE_API_FUNCTION_LIST, nvEncDestroyInputBuffer)"},
	{"nvFnCreateBitstreamBuffer", nvFnCreateBitstreamBuffer, "offsetof(NV_ENCODE_API_FUNCTION_LIST, nvEncCreateBitstreamBuffer)"},
	{"nvFnDestroyBitstreamBuf", nvFnDestroyBitstreamBuf, "offsetof(NV_ENCODE_API_FUNCTION_LIST, nvEncDestroyBitstreamBuffer)"},
	{"nvFnEncodePicture", nvFnEncodePicture, "offsetof(NV_ENCODE_API_FUNCTION_LIST, nvEncEncodePicture)"},
	{"nvFnLockBitstream", nvFnLockBitstream, "offsetof(NV_ENCODE_API_FUNCTION_LIST, nvEncLockBitstream)"},
	{"nvFnUnlockBitstream", nvFnUnlockBitstream, "offsetof(NV_ENCODE_API_FUNCTION_LIST, nvEncUnlockBitstream)"},
	{"nvFnLockInputBuffer", nvFnLockInputBuffer, "offsetof(NV_ENCODE_API_FUNCTION_LIST, nvEncLockInputBuffer)"},
	{"nvFnUnlockInputBuffer", nvFnUnlockInputBuffer, "offsetof(NV_ENCODE_API_FUNCTION_LIST, nvEncUnlockInputBuffer)"},
	{"nvFnDestroyEncoder", nvFnDestroyEncoder, "offsetof(NV_ENCODE_API_FUNCTION_LIST, nvEncDestroyEncoder)"},
	{"nvFnOpenSessionEx", nvFnOpenSessionEx, "offsetof(NV_ENCODE_API_FUNCTION_LIST, nvEncOpenEncodeSessionEx)"},
	{"nvFnGetLastErrorString", nvFnGetLastErrorString, "offsetof(NV_ENCODE_API_FUNCTION_LIST, nvEncGetLastErrorString)"},
	{"nvSizeOpenSessionExParams", nvSizeOpenSessionExParams, "sizeof(NV_ENC_OPEN_ENCODE_SESSION_EX_PARAMS)"},
	{"nvOSDeviceType", nvOSDeviceType, "offsetof(NV_ENC_OPEN_ENCODE_SESSION_EX_PARAMS, deviceType)"},
	{"nvOSDevice", nvOSDevice, "offsetof(NV_ENC_OPEN_ENCODE_SESSION_EX_PARAMS, device)"},
	{"nvOSAPIVersion", nvOSAPIVersion, "offsetof(NV_ENC_OPEN_ENCODE_SESSION_EX_PARAMS, apiVersion)"},
	{"nvSizePresetConfig", nvSizePresetConfig, "sizeof(NV_ENC_PRESET_CONFIG)"},
	{"nvPCPresetCfg", nvPCPresetCfg, "offsetof(NV_ENC_PRESET_CONFIG, presetCfg)"},
	{"nvSizeConfig", nvSizeConfig, "sizeof(NV_ENC_CONFIG)"},
	{"nvCfgGOPLength", nvCfgGOPLength, "offsetof(NV_ENC_CONFIG, gopLength)"},
	{"nvCfgFrameIntervalP", nvCfgFrameIntervalP, "offsetof(NV_ENC_CONFIG, frameIntervalP)"},
	{"nvCfgRCParams", nvCfgRCParams, "offsetof(NV_ENC_CONFIG, rcParams)"},
	{"nvCfgCodecConfig", nvCfgCodecConfig, "offsetof(NV_ENC_CONFIG, encodeCodecConfig)"},
	{"nvRCRateControlMode", nvRCRateControlMode, "offsetof(NV_ENC_RC_PARAMS, rateControlMode)"},
	{"nvRCConstQP", nvRCConstQP, "offsetof(NV_ENC_RC_PARAMS, constQP)"},
	{"nvH264IDRPeriod", nvH264IDRPeriod, "offsetof(NV_ENC_CONFIG_H264, idrPeriod)"},
	{"nvHEVCIDRPeriod", nvHEVCIDRPeriod, "offsetof(NV_ENC_CONFIG_HEVC, idrPeriod)"},
	{"nvSizeInitializeParams", nvSizeInitializeParams, "sizeof(NV_ENC_INITIALIZE_PARAMS)"},
	{"nvIPEncodeGUID", nvIPEncodeGUID, "offsetof(NV_ENC_INITIALIZE_PARAMS, encodeGUID)"},
	{"nvIPPresetGUID", nvIPPresetGUID, "offsetof(NV_ENC_INITIALIZE_PARAMS, presetGUID)"},
	{"nvIPEncodeWidth", nvIPEncodeWidth, "offsetof(NV_ENC_INITIALIZE_PARAMS, encodeWidth)"},
	{"nvIPEncodeHeight", nvIPEncodeHeight, "offsetof(NV_ENC_INITIALIZE_PARAMS, encodeHeight)"},
	{"nvIPDarWidth", nvIPDarWidth, "offsetof(NV_ENC_INITIALIZE_PARAMS, darWidth)"},
	{"nvIPDarHeight", nvIPDarHeight, "offsetof(NV_ENC_INITIALIZE_PARAMS, darHeight)"},
	{"nvIPFrameRateNum", nvIPFrameRateNum, "offsetof(NV_ENC_INITIALIZE_PARAMS, frameRateNum)"},
	{"nvIPFrameRateDen", nvIPFrameRateDen, "offsetof(NV_ENC_INITIALIZE_PARAMS, frameRateDen)"},
	{"nvIPEnablePTD", nvIPEnablePTD, "offsetof(NV_ENC_INITIALIZE_PARAMS, enablePTD)"},
	{"nvIPEncodeConfig", nvIPEncodeConfig, "offsetof(NV_ENC_INITIALIZE_PARAMS, encodeConfig)"},
	{"nvIPMaxEncodeWidth", nvIPMaxEncodeWidth, "offsetof(NV_ENC_INITIALIZE_PARAMS, maxEncodeWidth)"},
	{"nvIPMaxEncodeHeight", nvIPMaxEncodeHeight, "offsetof(NV_ENC_INITIALIZE_PARAMS, maxEncodeHeight)"},
	{"nvIPTuningInfo", nvIPTuningInfo, "offsetof(NV_ENC_INITIALIZE_PARAMS, tuningInfo)"},
	{"nvSizeCreateInputBuffer", nvSizeCreateInputBuffer, "sizeof(NV_ENC_CREATE_INPUT_BUFFER)"},
	{"nvCIBWidth", nvCIBWidth, "offsetof(NV_ENC_CREATE_INPUT_BUFFER, width)"},
	{"nvCIBHeight", nvCIBHeight, "offsetof(NV_ENC_CREATE_INPUT_BUFFER, height)"},
	{"nvCIBBufferFmt", nvCIBBufferFmt, "offsetof(NV_ENC_CREATE_INPUT_BUFFER, bufferFmt)"},
	{"nvCIBInputBuffer", nvCIBInputBuffer, "offsetof(NV_ENC_CREATE_INPUT_BUFFER, inputBuffer)"},
	{"nvSizeCreateBitstream", nvSizeCreateBitstream, "sizeof(NV_ENC_CREATE_BITSTREAM_BUFFER)"},
	{"nvCBBBitstreamBuffer", nvCBBBitstreamBuffer, "offsetof(NV_ENC_CREATE_BITSTREAM_BUFFER, bitstreamBuffer)"},
	{"nvSizeLockInputBuffer", nvSizeLockInputBuffer, "sizeof(NV_ENC_LOCK_INPUT_BUFFER)"},
	{"nvLIBInputBuffer", nvLIBInputBuffer, "offsetof(NV_ENC_LOCK_INPUT_BUFFER, inputBuffer)"},
	{"nvLIBDataPtr", nvLIBDataPtr, "offsetof(NV_ENC_LOCK_INPUT_BUFFER, bufferDataPtr)"},
	{"nvLIBPitch", nvLIBPitch, "offsetof(NV_ENC_LOCK_INPUT_BUFFER, pitch)"},
	{"nvSizeLockBitstream", nvSizeLockBitstream, "sizeof(NV_ENC_LOCK_BITSTREAM)"},
	{"nvLBOutputBitstream", nvLBOutputBitstream, "offsetof(NV_ENC_LOCK_BITSTREAM, outputBitstream)"},
	{"nvLBSize", nvLBSize, "offsetof(NV_ENC_LOCK_BITSTREAM, bitstreamSizeInBytes)"},
	{"nvLBDataPtr", nvLBDataPtr, "offsetof(NV_ENC_LOCK_BITSTREAM, bitstreamBufferPtr)"},
	{"nvSizePicParams", nvSizePicParams, "sizeof(NV_ENC_PIC_PARAMS)"},
	{"nvPPInputWidth", nvPPInputWidth, "offsetof(NV_ENC_PIC_PARAMS, inputWidth)"},
	{"nvPPInputHeight", nvPPInputHeight, "offsetof(NV_ENC_PIC_PARAMS, inputHeight)"},
	{"nvPPInputPitch", nvPPInputPitch, "offsetof(NV_ENC_PIC_PARAMS, inputPitch)"},
	{"nvPPEncodePicFlags", nvPPEncodePicFlags, "offsetof(NV_ENC_PIC_PARAMS, encodePicFlags)"},
	{"nvPPInputTimeStamp", nvPPInputTimeStamp, "offsetof(NV_ENC_PIC_PARAMS, inputTimeStamp)"},
	{"nvPPInputBuffer", nvPPInputBuffer, "offsetof(NV_ENC_PIC_PARAMS, inputBuffer)"},
	{"nvPPOutputBitstream", nvPPOutputBitstream, "offsetof(NV_ENC_PIC_PARAMS, outputBitstream)"},
	{"nvPPBufferFmt", nvPPBufferFmt, "offsetof(NV_ENC_PIC_PARAMS, bufferFmt)"},
	{"nvPPPictureStruct", nvPPPictureStruct, "offsetof(NV_ENC_PIC_PARAMS, pictureStruct)"},
	{"nvDeviceTypeCUDA", nvDeviceTypeCUDA, "NV_ENC_DEVICE_TYPE_CUDA"},
	{"nvBufferFormatNV12", nvBufferFormatNV12, "NV_ENC_BUFFER_FORMAT_NV12"},
	{"nvRCConstQPMode", nvRCConstQPMode, "NV_ENC_PARAMS_RC_CONSTQP"},
	{"nvPicStructFrame", nvPicStructFrame, "NV_ENC_PIC_STRUCT_FRAME"},
	{"nvPicFlagEOS", nvPicFlagEOS, "NV_ENC_PIC_FLAG_EOS"},
	{"nvTuningHighQuality", nvTuningHighQuality, "NV_ENC_TUNING_INFO_HIGH_QUALITY"},
	{"nvErrNeedMoreInput", nvErrNeedMoreInput, "NV_ENC_ERR_NEED_MORE_INPUT"},
	{"nvSuccess", nvSuccess, "NV_ENC_SUCCESS"},
}

var nvencGUIDs = []struct {
	got  []byte
	expr string
}{
	{nvCodecH264GUID, "NV_ENC_CODEC_H264_GUID"},
	{nvCodecHEVCGUID, "NV_ENC_CODEC_HEVC_GUID"},
	{nvPresetP4GUID, "NV_ENC_PRESET_P4_GUID"},
}
