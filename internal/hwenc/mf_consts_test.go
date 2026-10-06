package hwenc

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The Media Foundation encoder's vtable slots, GUIDs and constants were
// copied from the Windows SDK headers. This test re-reads them from the
// headers (mingw-w64's mfapi.h, codecapi.h, mferror.h, mfobjects.idl,
// mftransform.idl, icodecapi.idl) when MVC_MF_HEADERS lists directories
// holding them, and compares with mf_windows.go — parsed as source, so it
// runs on any platform.

func mfHeaders(t *testing.T) string {
	t.Helper()
	dirs := filepath.SplitList(os.Getenv("MVC_MF_HEADERS"))
	if len(dirs) == 0 {
		t.Skip("MVC_MF_HEADERS not set")
	}
	var all strings.Builder
	for _, f := range []string{"mfapi.h", "codecapi.h", "mferror.h", "mfobjects.idl", "mftransform.idl", "icodecapi.idl"} {
		found := false
		for _, dir := range dirs {
			if b, err := os.ReadFile(filepath.Join(dir, f)); err == nil { //nolint:gosec // test input
				all.Write(b)
				all.WriteByte('\n')
				found = true
				break
			}
		}
		if !found {
			t.Skipf("no %s in %v", f, dirs)
		}
	}
	return all.String()
}

// mfSource reads mf_windows.go's constants and guid(...) values.
func mfSource(t *testing.T) (map[string]int64, map[string]string) {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "mf_windows.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	consts, guids := map[string]int64{}, map[string]string{}
	var eval func(ast.Expr) int64
	eval = func(e ast.Expr) int64 {
		switch x := e.(type) {
		case *ast.BasicLit:
			v, err := strconv.ParseInt(strings.ReplaceAll(x.Value, "_", ""), 0, 64)
			if err != nil {
				t.Fatalf("%s: %v", x.Value, err)
			}
			return v
		case *ast.ParenExpr:
			return eval(x.X)
		case *ast.Ident:
			v, ok := consts[x.Name]
			if !ok {
				t.Fatalf("constant %s used before it is known", x.Name)
			}
			return v
		case *ast.BinaryExpr:
			a, b := eval(x.X), eval(x.Y)
			switch x.Op {
			case token.OR:
				return a | b
			case token.SHL:
				return a << b
			case token.ADD:
				return a + b
			}
		}
		t.Fatalf("cannot evaluate %T", e)
		return 0
	}
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, s := range g.Specs {
			v, ok := s.(*ast.ValueSpec)
			if !ok || len(v.Values) != len(v.Names) {
				continue
			}
			for i, name := range v.Names {
				switch g.Tok {
				case token.CONST:
					consts[name.Name] = eval(v.Values[i])
				case token.VAR:
					c, ok := v.Values[i].(*ast.CallExpr)
					if !ok {
						continue
					}
					var parts []string
					for _, a := range c.Args {
						switch y := a.(type) {
						case *ast.BasicLit:
							parts = append(parts, y.Value)
						case *ast.CompositeLit:
							for _, e := range y.Elts {
								parts = append(parts, e.(*ast.BasicLit).Value) //nolint:forcetypeassert // byte literals
							}
						}
					}
					guids[name.Name] = strings.Join(parts, ",")
				}
			}
		}
	}
	return consts, guids
}

// canonGUID turns a header's GUID (comma list or dashed) into one form.
func canonGUID(s string) string {
	s = strings.TrimSpace(s)
	if strings.Count(s, "-") == 4 && !strings.Contains(s, ",") {
		h := strings.ReplaceAll(s, "-", "")
		var parts []string
		parts = append(parts, "0x"+h[0:8], "0x"+h[8:12], "0x"+h[12:16])
		for i := 16; i < 32; i += 2 {
			parts = append(parts, "0x"+h[i:i+2])
		}
		s = strings.Join(parts, ",")
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		v, err := strconv.ParseUint(strings.TrimSpace(p), 0, 64)
		if err != nil {
			return "bad:" + s
		}
		out = append(out, fmt.Sprint(v))
	}
	return strings.Join(out, ",")
}

func TestMFConstants(t *testing.T) {
	h := mfHeaders(t)
	consts, guids := mfSource(t)

	headerGUID := func(name string) string {
		for _, re := range []string{
			`(?:DEFINE_GUID|EXTERN_GUID)\s*\(\s*` + name + `\s*,([^)]*)\)`,
			`#define\s+STATIC_` + name + `\s+([^\n]*)`,
		} {
			if m := regexp.MustCompile(re).FindStringSubmatch(h); m != nil {
				return canonGUID(m[1])
			}
		}
		if m := regexp.MustCompile(`uuid\s*\(\s*([0-9a-fA-F-]+)\s*\)[^\]]*\]\s*interface\s+` + strings.TrimPrefix(name, "IID_") + `\b`).FindStringSubmatch(h); m != nil {
			return canonGUID(m[1])
		}
		return "missing"
	}
	for goName, hName := range map[string]string{
		"mftCategoryVideoEncoder": "MFT_CATEGORY_VIDEO_ENCODER", "mfMediaTypeVideo": "MFMediaType_Video",
		"mfMTMajorType": "MF_MT_MAJOR_TYPE", "mfMTSubtype": "MF_MT_SUBTYPE", "mfMTFrameSize": "MF_MT_FRAME_SIZE",
		"mfMTFrameRate": "MF_MT_FRAME_RATE", "mfMTPixelAspect": "MF_MT_PIXEL_ASPECT_RATIO",
		"mfMTInterlaceMode": "MF_MT_INTERLACE_MODE", "mfMTAvgBitrate": "MF_MT_AVG_BITRATE",
		"mfMTMpeg2Profile": "MF_MT_MPEG2_PROFILE", "mfMTSequenceHeader": "MF_MT_MPEG_SEQUENCE_HEADER",
		"mfTransformAsync": "MF_TRANSFORM_ASYNC", "mfTransformAsyncUnl": "MF_TRANSFORM_ASYNC_UNLOCK",
		"codecAPIRateControlMode": "CODECAPI_AVEncCommonRateControlMode", "codecAPIQuality": "CODECAPI_AVEncCommonQuality",
		"codecAPIVideoEncodeQP": "CODECAPI_AVEncVideoEncodeQP", "codecAPIBPictureCount": "CODECAPI_AVEncMPVDefaultBPictureCount",
		"codecAPIGOPSize": "CODECAPI_AVEncMPVGOPSize", "iidICodecAPI": "IID_ICodecAPI", "iidIMFTransform": "IID_IMFTransform",
		"iidIMFMediaEventGenerator": "IID_IMFMediaEventGenerator",
	} {
		if got, want := canonGUID(guids[goName]), headerGUID(hName); got != want {
			t.Errorf("%s = %s, %s is %s", goName, got, hName, want)
		}
	}
	for goName, fcc := range map[string]string{"mfVideoFormatNV12": "NV12", "mfVideoFormatH264": "H264", "mfVideoFormatHEVC": "HEVC"} {
		hName := "MFVideoFormat_" + strings.TrimPrefix(goName, "mfVideoFormat")
		if !regexp.MustCompile(`DEFINE_MEDIATYPE_GUID\s*\(\s*` + hName + `\s*,\s*FCC\s*\(\s*'` + fcc + `'\s*\)`).MatchString(h) {
			t.Errorf("%s is not FCC('%s') in the headers", hName, fcc)
		}
		if !strings.Contains(guids[goName], `"`+fcc+`"`) {
			t.Errorf("%s is not fourCC(%q)", goName, fcc)
		}
	}

	headerInt := func(name string) (int64, bool) {
		m := regexp.MustCompile(`\b` + name + `\b\s*=\s*(?:_HRESULT_TYPEDEF_\()?\s*(0x[0-9a-fA-F]+|\d+)`).FindStringSubmatch(h)
		if m == nil {
			m = regexp.MustCompile(`#define\s+` + name + `\s+(?:_HRESULT_TYPEDEF_\()?\s*(0x[0-9a-fA-F]+|\d+)`).FindStringSubmatch(h)
		}
		if m == nil {
			return 0, false
		}
		v, err := strconv.ParseInt(m[1], 0, 64)
		return v, err == nil
	}
	for goName, hName := range map[string]string{
		"mftEnumSync": "MFT_ENUM_FLAG_SYNCMFT", "mftEnumHardware": "MFT_ENUM_FLAG_HARDWARE",
		"mftEnumSortAndFilter": "MFT_ENUM_FLAG_SORTANDFILTER", "mftMsgCommandDrain": "MFT_MESSAGE_COMMAND_DRAIN",
		"mftMsgNotifyBeginStream": "MFT_MESSAGE_NOTIFY_BEGIN_STREAMING", "mftMsgNotifyEndStreaming": "MFT_MESSAGE_NOTIFY_END_STREAMING",
		"mftMsgNotifyEndOfStream": "MFT_MESSAGE_NOTIFY_END_OF_STREAM", "mftMsgNotifyStartOfStrm": "MFT_MESSAGE_NOTIFY_START_OF_STREAM",
		"mftOutputProvidesSamples": "MFT_OUTPUT_STREAM_PROVIDES_SAMPLES", "mftOutputCanProvideSamples": "MFT_OUTPUT_STREAM_CAN_PROVIDE_SAMPLES",
		"mfInterlaceProgressive": "MFVideoInterlace_Progressive", "mfRateControlQuality": "eAVEncCommonRateControlMode_Quality",
		"mfH264ProfileHigh": "eAVEncH264VProfile_High", "mfHEVCProfileMain": "eAVEncH265VProfile_Main_420_8",
		"mfENeedMoreInput": "MF_E_TRANSFORM_NEED_MORE_INPUT", "mfENotAccepting": "MF_E_NOTACCEPTING",
		"mfEStreamChange": "MF_E_TRANSFORM_STREAM_CHANGE", "mfEventTransformNeedInput": "METransformNeedInput",
		"mfEventTransformHaveOutput": "METransformHaveOutput", "mfEventTransformDrainComplt": "METransformDrainComplete",
	} {
		want, ok := headerInt(hName)
		if !ok && strings.HasPrefix(hName, "METransform") {
			// An enum continuing from METransformUnknown = 600.
			base, _ := headerInt("METransformUnknown")
			want = base + map[string]int64{"METransformNeedInput": 1, "METransformHaveOutput": 2, "METransformDrainComplete": 3}[hName]
			ok = base != 0
		}
		if !ok {
			t.Errorf("%s not found in the headers", hName)
			continue
		}
		if consts[goName] != want {
			t.Errorf("%s = %#x, %s is %#x", goName, consts[goName], hName, want)
		}
	}
	if api, _ := headerInt("MF_API_VERSION"); consts["mfVersion"] != 2<<16|api {
		t.Errorf("mfVersion = %#x, want MF_SDK_VERSION 2 and MF_API_VERSION %#x", consts["mfVersion"], api)
	}

	// Vtable slots: an interface's methods in declaration order after its
	// base's (IUnknown has three), a [call_as] remote twin taking no slot.
	methods := func(iface string) (base string, names []string) {
		m := regexp.MustCompile(`(?ms)^interface\s+` + iface + `\s*:\s*(\w+)\s*\{(.*?)^\}`).FindStringSubmatch(h)
		if m == nil {
			t.Fatalf("interface %s not in the headers", iface)
		}
		body := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(m[2], "")
		for _, line := range strings.Split(body, ";") {
			if strings.Contains(line, "call_as") {
				continue
			}
			if mm := regexp.MustCompile(`(?:HRESULT|ULONG|void|DWORD)\s+(\w+)\s*\(`).FindStringSubmatch(line); mm != nil {
				names = append(names, mm[1])
			}
		}
		return m[1], names
	}
	// size is how many slots an interface's vtable has; slot finds a method.
	var size func(iface string) int
	size = func(iface string) int {
		if iface == "IUnknown" {
			return 3
		}
		base, names := methods(iface)
		return size(base) + len(names)
	}
	var slot func(iface, method string) int
	slot = func(iface, method string) int {
		if iface == "IUnknown" {
			return map[string]int{"QueryInterface": 0, "AddRef": 1, "Release": 2}[method]
		}
		base, names := methods(iface)
		for i, n := range names {
			if n == method {
				return size(base) + i
			}
		}
		return slot(base, method)
	}
	for goName, im := range map[string][2]string{
		"slotQueryInterface": {"IUnknown", "QueryInterface"}, "slotRelease": {"IUnknown", "Release"},
		"slotGetUINT32": {"IMFAttributes", "GetUINT32"}, "slotGetBlob": {"IMFAttributes", "GetBlob"},
		"slotGetBlobLen": {"IMFAttributes", "GetBlobSize"}, "slotSetUINT32": {"IMFAttributes", "SetUINT32"},
		"slotSetUINT64": {"IMFAttributes", "SetUINT64"}, "slotSetGUID": {"IMFAttributes", "SetGUID"},
		"slotActivateObject": {"IMFActivate", "ActivateObject"}, "slotShutdownObject": {"IMFActivate", "ShutdownObject"},
		"slotSetSampleTime": {"IMFSample", "SetSampleTime"}, "slotSetSampleDuration": {"IMFSample", "SetSampleDuration"},
		"slotConvertContiguous": {"IMFSample", "ConvertToContiguousBuffer"}, "slotAddBuffer": {"IMFSample", "AddBuffer"},
		"slotBufLock": {"IMFMediaBuffer", "Lock"}, "slotBufUnlock": {"IMFMediaBuffer", "Unlock"},
		"slotBufSetCurLen": {"IMFMediaBuffer", "SetCurrentLength"}, "slotGetEvent": {"IMFMediaEventGenerator", "GetEvent"},
		"slotEventGetType": {"IMFMediaEvent", "GetType"}, "slotEventGetStatus": {"IMFMediaEvent", "GetStatus"},
		"slotGetOutputStreamInfo": {"IMFTransform", "GetOutputStreamInfo"}, "slotGetAttributes": {"IMFTransform", "GetAttributes"},
		"slotGetOutputAvailableType": {"IMFTransform", "GetOutputAvailableType"}, "slotSetInputType": {"IMFTransform", "SetInputType"},
		"slotSetOutputType": {"IMFTransform", "SetOutputType"}, "slotGetOutputCurrentType": {"IMFTransform", "GetOutputCurrentType"},
		"slotProcessMessage": {"IMFTransform", "ProcessMessage"}, "slotProcessInput": {"IMFTransform", "ProcessInput"},
		"slotProcessOutput": {"IMFTransform", "ProcessOutput"}, "slotCodecIsSupported": {"ICodecAPI", "IsSupported"},
		"slotCodecSetValue": {"ICodecAPI", "SetValue"},
	} {
		if want := int64(slot(im[0], im[1])); consts[goName] != want {
			t.Errorf("%s = %d, %s::%s is slot %d", goName, consts[goName], im[0], im[1], want)
		}
	}
}
