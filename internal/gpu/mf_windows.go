//go:build windows && (amd64 || arm64)

package gpu

import (
	"fmt"
	"io"
	"sync"
	"unsafe"
)

// Media Foundation: the encoder MFTs (Media Foundation transforms) Windows
// GPU drivers install — Intel Quick Sync, AMD AMF, NVIDIA's too — driven
// through COM, in process, with no cgo. A hardware MFT is asynchronous: it
// asks for input and announces output through events. Microsoft's own
// software encoders are synchronous; tests on machines without a GPU use
// them, through the same code.
//
// COM is called through vtables: every method is a slot in the object's
// function table, in the order the interface declares them (IUnknown's
// three first). The slots, GUIDs and constants below are taken from the
// Windows SDK headers (mingw-w64's mfobjects.idl, mftransform.idl,
// icodecapi.idl, mfapi.h, codecapi.h); TestMFConstants re-reads them when
// MVC_MF_HEADERS points at those headers.

// mfAllowSoftware lets the software MFTs stand in when no hardware encoder
// is registered; tests on GPU-less machines turn it on.
var mfAllowSoftware = false

// Vtable slots.
const (
	slotQueryInterface = 0
	slotRelease        = 2

	// IMFAttributes (on every IMFAttributes-derived interface).
	slotGetUINT32  = 7
	slotGetBlob    = 15
	slotGetBlobLen = 14
	slotSetUINT32  = 21
	slotSetUINT64  = 22
	slotSetGUID    = 24

	// IMFActivate.
	slotActivateObject = 33
	slotShutdownObject = 34

	// IMFSample.
	slotSetSampleTime     = 36
	slotSetSampleDuration = 38
	slotConvertContiguous = 41
	slotAddBuffer         = 42

	// IMFMediaBuffer.
	slotBufLock      = 3
	slotBufUnlock    = 4
	slotBufSetCurLen = 6

	// IMFMediaEventGenerator, IMFMediaEvent.
	slotGetEvent       = 3
	slotEventGetType   = 33
	slotEventGetStatus = 35

	// IMFTransform.
	slotGetOutputStreamInfo    = 7
	slotGetAttributes          = 8
	slotGetOutputAvailableType = 14
	slotSetInputType           = 15
	slotSetOutputType          = 16
	slotGetOutputCurrentType   = 18
	slotProcessMessage         = 23
	slotProcessInput           = 24
	slotProcessOutput          = 25

	// ICodecAPI.
	slotCodecIsSupported = 3
	slotCodecSetValue    = 9
)

// Constants.
const (
	mfVersion = 0x2<<16 | 0x0070 // MF_SDK_VERSION << 16 | MF_API_VERSION

	mftEnumSync          = 0x01
	mftEnumHardware      = 0x04
	mftEnumSortAndFilter = 0x40

	mftMsgCommandDrain       = 0x00000001
	mftMsgNotifyBeginStream  = 0x10000000
	mftMsgNotifyEndStreaming = 0x10000001
	mftMsgNotifyEndOfStream  = 0x10000002
	mftMsgNotifyStartOfStrm  = 0x10000003

	mftOutputProvidesSamples    = 0x100
	mftOutputCanProvideSamples  = 0x200
	mfInterlaceProgressive      = 2
	mfRateControlQuality        = 3 // eAVEncCommonRateControlMode_Quality
	mfH264ProfileHigh           = 100
	mfHEVCProfileMain           = 1
	mfEventTransformNeedInput   = 601
	mfEventTransformHaveOutput  = 602
	mfEventTransformDrainComplt = 603

	mfENeedMoreInput = 0xc00d6d72
	mfENotAccepting  = 0xc00d36b5
	mfEStreamChange  = 0xc00d6d61

	vtUI4 = 19
	vtUI8 = 21

	// Struct sizes on 64-bit Windows.
	sizeRegisterTypeInfo = 32 // two GUIDs
	sizeOutputDataBuffer = 32 // DWORD, pad, IMFSample*, DWORD, pad, IMFCollection*
	sizeOutputStreamInfo = 12 // three DWORDs
	sizeVariant          = 24 // VARTYPE, three reserved WORDs, an 8-byte value (and padding)
)

// fourCC makes a Media Foundation subtype GUID from a FOURCC.
func fourCC(s string) []byte {
	d1 := uint32(s[0]) | uint32(s[1])<<8 | uint32(s[2])<<16 | uint32(s[3])<<24
	return guid(d1, 0x0000, 0x0010, [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71})
}

var (
	mftCategoryVideoEncoder = guid(0xf79eac7d, 0xe545, 0x4387, [8]byte{0xbd, 0xee, 0xd6, 0x47, 0xd7, 0xbd, 0xe4, 0x2a})
	mfMediaTypeVideo        = guid(0x73646976, 0x0000, 0x0010, [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71})
	mfVideoFormatNV12       = fourCC("NV12")
	mfVideoFormatH264       = fourCC("H264")
	mfVideoFormatHEVC       = fourCC("HEVC")

	mfMTMajorType       = guid(0x48eba18e, 0xf8c9, 0x4687, [8]byte{0xbf, 0x11, 0x0a, 0x74, 0xc9, 0xf9, 0x6a, 0x8f})
	mfMTSubtype         = guid(0xf7e34c9a, 0x42e8, 0x4714, [8]byte{0xb7, 0x4b, 0xcb, 0x29, 0xd7, 0x2c, 0x35, 0xe5})
	mfMTFrameSize       = guid(0x1652c33d, 0xd6b2, 0x4012, [8]byte{0xb8, 0x34, 0x72, 0x03, 0x08, 0x49, 0xa3, 0x7d})
	mfMTFrameRate       = guid(0xc459a2e8, 0x3d2c, 0x4e44, [8]byte{0xb1, 0x32, 0xfe, 0xe5, 0x15, 0x6c, 0x7b, 0xb0})
	mfMTPixelAspect     = guid(0xc6376a1e, 0x8d0a, 0x4027, [8]byte{0xbe, 0x45, 0x6d, 0x9a, 0x0a, 0xd3, 0x9b, 0xb6})
	mfMTInterlaceMode   = guid(0xe2724bb8, 0xe676, 0x4806, [8]byte{0xb4, 0xb2, 0xa8, 0xd6, 0xef, 0xb4, 0x4c, 0xcd})
	mfMTAvgBitrate      = guid(0x20332624, 0xfb0d, 0x4d9e, [8]byte{0xbd, 0x0d, 0xcb, 0xf6, 0x78, 0x6c, 0x10, 0x2e})
	mfMTMpeg2Profile    = guid(0xad76a80b, 0x2d5c, 0x4e0b, [8]byte{0xb3, 0x75, 0x64, 0xe5, 0x20, 0x13, 0x70, 0x36})
	mfMTSequenceHeader  = guid(0x3c036de7, 0x3ad0, 0x4c9e, [8]byte{0x92, 0x16, 0xee, 0x6d, 0x6a, 0xc2, 0x1c, 0xb3})
	mfTransformAsync    = guid(0xf81a699a, 0x649a, 0x497d, [8]byte{0x8c, 0x73, 0x29, 0xf8, 0xfe, 0xd6, 0xad, 0x7a})
	mfTransformAsyncUnl = guid(0xe5666d6b, 0x3422, 0x4eb6, [8]byte{0xa4, 0x21, 0xda, 0x7d, 0xb1, 0xf8, 0xe2, 0x07})

	codecAPIRateControlMode = guid(0x1c0608e9, 0x370c, 0x4710, [8]byte{0x8a, 0x58, 0xcb, 0x61, 0x81, 0xc4, 0x24, 0x23})
	codecAPIQuality         = guid(0xfcbf57a3, 0x7ea5, 0x4b0c, [8]byte{0x96, 0x44, 0x69, 0xb4, 0x0c, 0x39, 0xc3, 0x91})
	codecAPIVideoEncodeQP   = guid(0x2cb5696b, 0x23fb, 0x4ce1, [8]byte{0xa0, 0xf9, 0xef, 0x5b, 0x90, 0xfd, 0x55, 0xca})
	codecAPIBPictureCount   = guid(0x8d390aac, 0xdc5c, 0x4200, [8]byte{0xb5, 0x7f, 0x81, 0x4d, 0x04, 0xba, 0xba, 0xb2})
	codecAPIGOPSize         = guid(0x95f31b26, 0x95a4, 0x41aa, [8]byte{0x93, 0x03, 0x24, 0x6a, 0x7f, 0xc6, 0xee, 0xf1})

	iidICodecAPI              = guid(0x901db4c7, 0x31ce, 0x41a2, [8]byte{0x85, 0xdc, 0x8f, 0xa0, 0xbf, 0x41, 0xb8, 0xda})
	iidIMFTransform           = guid(0xbf94c121, 0x5b05, 0x4e6f, [8]byte{0x80, 0x00, 0xba, 0x59, 0x89, 0x61, 0x41, 0x4d})
	iidIMFMediaEventGenerator = guid(0x2cd0bd52, 0xbcd5, 0x4b89, [8]byte{0xb6, 0x2c, 0xea, 0xdc, 0x0c, 0x03, 0x1e, 0x7d})
)

// mfAPI is what Media Foundation and COM export that the encoder calls.
type mfAPI struct {
	startup, shutdown, enumEx, createMediaType, createSample, createMemoryBuffer uintptr
	coInitializeEx, coTaskMemFree                                                uintptr
}

var (
	mfOnce    sync.Once
	mfLoaded  *mfAPI
	mfLoadErr error
)

func loadMF() (*mfAPI, error) {
	mfOnce.Do(func() {
		plat, err := openLib("mfplat.dll")
		if err != nil {
			mfLoadErr = err
			return
		}
		ole, err := openLib("ole32.dll")
		if err != nil {
			mfLoadErr = err
			return
		}
		a := &mfAPI{}
		for _, s := range []struct {
			lib  uintptr
			name string
			p    *uintptr
		}{
			{plat, "MFStartup", &a.startup},
			{plat, "MFShutdown", &a.shutdown},
			{plat, "MFTEnumEx", &a.enumEx},
			{plat, "MFCreateMediaType", &a.createMediaType},
			{plat, "MFCreateSample", &a.createSample},
			{plat, "MFCreateMemoryBuffer", &a.createMemoryBuffer},
			{ole, "CoInitializeEx", &a.coInitializeEx},
			{ole, "CoTaskMemFree", &a.coTaskMemFree},
		} {
			if *s.p, err = sym(s.lib, s.name); err != nil {
				mfLoadErr = err
				return
			}
		}
		// The multithreaded apartment, for the whole process: a goroutine
		// moves between OS threads, and threads that never initialised COM
		// join the MTA implicitly once one has.
		call(a.coInitializeEx, 0, 0) // COINIT_MULTITHREADED
		mfLoaded = a
	})
	return mfLoaded, mfLoadErr
}

// vcall calls slot of a COM object's vtable.
func vcall(obj uintptr, slot int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(cptr(obj))
	fn := *(*uintptr)(cptr(vtbl + uintptr(slot)*unsafe.Sizeof(uintptr(0))))
	return call(fn, append([]uintptr{obj}, args...)...)
}

func comRelease(obj uintptr) {
	if obj != 0 {
		vcall(obj, slotRelease)
	}
}

// failed reports an HRESULT that is an error.
func failed(hr uintptr) bool { return int32(uint32(hr)) < 0 } //nolint:gosec // an HRESULT is 32 bits

func hrErr(what string, hr uintptr) error {
	if !failed(hr) {
		return nil
	}
	return fmt.Errorf("mediafoundation: %s: HRESULT 0x%08x", what, uint32(hr)) //nolint:gosec // as above
}

type mfEnc struct {
	a        *mfAPI
	w        io.Writer
	cfg      Config
	activate uintptr
	mft      uintptr
	events   uintptr // IMFMediaEventGenerator, for an asynchronous MFT
	async    bool
	provides bool // the MFT allocates its output samples
	outSize  int
	credits  int // input the asynchronous MFT has asked for
	drained  bool
	started  bool // MFStartup succeeded
	wroteHdr bool
	n        int64
	err      error
}

func openMediaFoundation(cfg Config, w io.Writer) (Encoder, error) {
	if cfg.Codec == AV1 {
		// Hardware AV1 MFTs exist, but this encoder drives H.264 and HEVC.
		return nil, fmt.Errorf("%w: Media Foundation here encodes H.264 and HEVC", ErrUnavailable)
	}
	a, err := loadMF()
	if err != nil {
		return nil, err
	}
	e := &mfEnc{a: a, w: w, cfg: cfg}
	if err := hrErr("MFStartup", call(a.startup, mfVersion, 0)); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	e.started = true
	acts, err := e.enumerate(mftEnumHardware | mftEnumSortAndFilter)
	if err == nil && len(acts) == 0 && mfAllowSoftware {
		acts, err = e.enumerate(mftEnumSync | mftEnumSortAndFilter)
	}
	if err != nil {
		_ = e.Close()
		return nil, err
	}
	if len(acts) == 0 {
		_ = e.Close()
		return nil, fmt.Errorf("%w: no Media Foundation encoder for this codec", ErrUnavailable)
	}
	// The first that takes the configuration; the rest are let go.
	var firstErr error
	for _, act := range acts {
		if e.mft == 0 {
			if err := e.open(act); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				e.releaseMFT()
				vcall(act, slotShutdownObject)
				comRelease(act)
				continue
			}
			e.activate = act
			continue
		}
		comRelease(act)
	}
	if e.mft == 0 {
		_ = e.Close()
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, firstErr)
	}
	return e, nil
}

// enumerate lists the encoder MFTs that take NV12 and produce the codec.
func (e *mfEnc) enumerate(flags uintptr) ([]uintptr, error) {
	sub := mfVideoFormatH264
	if e.cfg.Codec == HEVC {
		sub = mfVideoFormatHEVC
	}
	in, out := newStruct(sizeRegisterTypeInfo), newStruct(sizeRegisterTypeInfo)
	in.bytes(0, mfMediaTypeVideo)
	in.bytes(16, mfVideoFormatNV12)
	out.bytes(0, mfMediaTypeVideo)
	out.bytes(16, sub)
	arr, count := newStruct(8), newStruct(4)
	args := append(guidArg(mftCategoryVideoEncoder), flags, in.ptr(), out.ptr(), arr.ptr(), count.ptr())
	if err := hrErr("MFTEnumEx", call(e.a.enumEx, args...)); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	p, n := arr.getPtr(0), int(count.getU32(0))
	if p == 0 {
		return nil, nil
	}
	defer call(e.a.coTaskMemFree, p)
	acts := make([]uintptr, n)
	for i := range acts {
		acts[i] = *(*uintptr)(cptr(p + uintptr(i)*unsafe.Sizeof(uintptr(0))))
	}
	return acts, nil
}

// open activates an MFT and configures it for the stream.
func (e *mfEnc) open(act uintptr) error {
	out := newStruct(8)
	if err := hrErr("activating the encoder", vcall(act, slotActivateObject, gp(iidIMFTransform), out.ptr())); err != nil {
		return err
	}
	e.mft = out.getPtr(0)

	// A hardware MFT is asynchronous and locked until the caller says it
	// knows: MF_TRANSFORM_ASYNC_UNLOCK.
	if hrv := vcall(e.mft, slotGetAttributes, out.ptr()); !failed(hrv) {
		attrs := out.getPtr(0)
		v := newStruct(4)
		if !failed(vcall(attrs, slotGetUINT32, gp(mfTransformAsync), v.ptr())) && v.getU32(0) != 0 {
			e.async = true
			vcall(attrs, slotSetUINT32, gp(mfTransformAsyncUnl), 1)
		}
		comRelease(attrs)
	}

	// Rate control before the media types: some encoders fix it then.
	e.codecSettings()

	w, h := uint64(e.cfg.Width), uint64(e.cfg.Height)      //nolint:gosec // frame size
	num, den := uint64(e.cfg.FPSNum), uint64(e.cfg.FPSDen) //nolint:gosec // frame rate
	mediaType := func(sub []byte) (uintptr, error) {
		t := newStruct(8)
		if err := hrErr("MFCreateMediaType", call(e.a.createMediaType, t.ptr())); err != nil {
			return 0, err
		}
		mt := t.getPtr(0)
		vcall(mt, slotSetGUID, gp(mfMTMajorType), gp(mfMediaTypeVideo))
		vcall(mt, slotSetGUID, gp(mfMTSubtype), gp(sub))
		vcall(mt, slotSetUINT64, gp(mfMTFrameSize), uintptr(w<<32|h))
		vcall(mt, slotSetUINT64, gp(mfMTFrameRate), uintptr(num<<32|den))
		vcall(mt, slotSetUINT64, gp(mfMTPixelAspect), uintptr(uint64(1)<<32|1))
		vcall(mt, slotSetUINT32, gp(mfMTInterlaceMode), mfInterlaceProgressive)
		return mt, nil
	}
	// The output type first: an encoder offers input types for the output
	// it has been given.
	sub, profile := mfVideoFormatH264, uintptr(mfH264ProfileHigh)
	if e.cfg.Codec == HEVC {
		sub, profile = mfVideoFormatHEVC, mfHEVCProfileMain
	}
	ot, err := mediaType(sub)
	if err != nil {
		return err
	}
	// Required even when the rate control ignores it.
	vcall(ot, slotSetUINT32, gp(mfMTAvgBitrate), uintptr(min(w*h*num/max(den, 1)/4, 1<<31-1)))
	vcall(ot, slotSetUINT32, gp(mfMTMpeg2Profile), profile)
	hrv := vcall(e.mft, slotSetOutputType, 0, ot, 0)
	comRelease(ot)
	if err := hrErr("setting the output type", hrv); err != nil {
		return err
	}
	it, err := mediaType(mfVideoFormatNV12)
	if err != nil {
		return err
	}
	hrv = vcall(e.mft, slotSetInputType, 0, it, 0)
	comRelease(it)
	if err := hrErr("setting the input type (NV12)", hrv); err != nil {
		return err
	}
	e.codecSettings() // again: some take them only once the types are set
	if err := e.streamInfo(); err != nil {
		return err
	}
	if e.async {
		if err := hrErr("getting the event generator", vcall(e.mft, slotQueryInterface, gp(iidIMFMediaEventGenerator), out.ptr())); err != nil {
			return err
		}
		e.events = out.getPtr(0)
	}
	if err := hrErr("starting", vcall(e.mft, slotProcessMessage, mftMsgNotifyBeginStream, 0)); err != nil {
		return err
	}
	return hrErr("starting", vcall(e.mft, slotProcessMessage, mftMsgNotifyStartOfStrm, 0))
}

// codecSettings asks for constant quality at the configured QP, B-frames and
// the keyframe interval. An encoder that does not take a setting keeps its
// own; nothing here is fatal.
func (e *mfEnc) codecSettings() {
	out := newStruct(8)
	if failed(vcall(e.mft, slotQueryInterface, gp(iidICodecAPI), out.ptr())) {
		return
	}
	api := out.getPtr(0)
	defer comRelease(api)
	set := func(key []byte, vt uint16, v uint64) bool {
		if failed(vcall(api, slotCodecIsSupported, gp(key))) {
			return false
		}
		val := newStruct(sizeVariant)
		val.u16(0, vt)
		val.u64(8, v)
		return !failed(vcall(api, slotCodecSetValue, gp(key), val.ptr()))
	}
	set(codecAPIRateControlMode, vtUI4, mfRateControlQuality)
	// A QP where the encoder takes one (Intel's and AMD's do), else the
	// 0–100 quality the mode is defined by, mapped like VideoToolbox's.
	if !set(codecAPIVideoEncodeQP, vtUI8, uint64(max(e.cfg.QP, 0))) { //nolint:gosec // 0..51
		set(codecAPIQuality, vtUI4, uint64(VTQuality(e.cfg.QP)*100+0.5))
	}
	set(codecAPIBPictureCount, vtUI4, 2)
	if e.cfg.GOP > 0 {
		set(codecAPIGOPSize, vtUI4, uint64(e.cfg.GOP)) //nolint:gosec // small
	}
}

// streamInfo reads who allocates output samples, and how large.
func (e *mfEnc) streamInfo() error {
	info := newStruct(sizeOutputStreamInfo)
	if err := hrErr("reading the output stream", vcall(e.mft, slotGetOutputStreamInfo, 0, info.ptr())); err != nil {
		return err
	}
	flags := info.getU32(0)
	e.provides = flags&(mftOutputProvidesSamples|mftOutputCanProvideSamples) != 0
	e.outSize = int(info.getU32(4))
	if e.outSize == 0 {
		e.outSize = e.cfg.Width*e.cfg.Height*3/2 + 1<<16
	}
	return nil
}

func (e *mfEnc) Encode(fill func(*Picture)) error {
	if e.err != nil {
		return e.err
	}
	s, err := e.sample(fill)
	if err != nil {
		e.err = err
		return err
	}
	defer comRelease(s)
	if e.async {
		for e.credits == 0 {
			if err := e.event(); err != nil {
				e.err = err
				return err
			}
		}
		e.credits--
		e.err = hrErr("encoding", vcall(e.mft, slotProcessInput, 0, s, 0))
		return e.err
	}
	for {
		hrv := vcall(e.mft, slotProcessInput, 0, s, 0)
		if uint32(hrv) == mfENotAccepting { //nolint:gosec // an HRESULT
			// Full: take what is ready, then offer the picture again.
			if err := e.drainSync(); err != nil {
				e.err = err
				return err
			}
			continue
		}
		if err := hrErr("encoding", hrv); err != nil {
			e.err = err
			return err
		}
		break
	}
	e.err = e.drainSync()
	return e.err
}

// sample draws the next picture into a new sample.
func (e *mfEnc) sample(fill func(*Picture)) (uintptr, error) {
	w, h := e.cfg.Width, e.cfg.Height
	size := w * h * 3 / 2
	out := newStruct(8)
	if err := hrErr("MFCreateMemoryBuffer", call(e.a.createMemoryBuffer, uintptr(size), out.ptr())); err != nil {
		return 0, err
	}
	buf := out.getPtr(0)
	defer comRelease(buf)
	p := newStruct(8)
	if err := hrErr("locking a buffer", vcall(buf, slotBufLock, p.ptr(), 0, 0)); err != nil {
		return 0, err
	}
	base := p.getPtr(0)
	fill(&Picture{Y: cbytes(base, w*h), UV: cbytes(base+uintptr(w*h), w*h/2), Pitch: w})
	vcall(buf, slotBufUnlock)
	vcall(buf, slotBufSetCurLen, uintptr(size))
	if err := hrErr("MFCreateSample", call(e.a.createSample, out.ptr())); err != nil {
		return 0, err
	}
	s := out.getPtr(0)
	if err := hrErr("adding a buffer", vcall(s, slotAddBuffer, buf)); err != nil {
		comRelease(s)
		return 0, err
	}
	// 100 ns units.
	dur := int64(10_000_000) * int64(e.cfg.FPSDen) / int64(max(e.cfg.FPSNum, 1))
	vcall(s, slotSetSampleTime, uintptr(e.n*dur))
	vcall(s, slotSetSampleDuration, uintptr(dur))
	e.n++
	return s, nil
}

// event handles one event of an asynchronous MFT, waiting for it.
func (e *mfEnc) event() error {
	out := newStruct(8)
	if err := hrErr("waiting for the encoder", vcall(e.events, slotGetEvent, 0, out.ptr())); err != nil {
		return err
	}
	ev := out.getPtr(0)
	defer comRelease(ev)
	typ, status := newStruct(4), newStruct(4)
	vcall(ev, slotEventGetType, typ.ptr())
	vcall(ev, slotEventGetStatus, status.ptr())
	if err := hrErr("encoder event", uintptr(status.getU32(0))); err != nil {
		return err
	}
	switch typ.getU32(0) {
	case mfEventTransformNeedInput:
		e.credits++
	case mfEventTransformHaveOutput:
		_, err := e.output()
		return err
	case mfEventTransformDrainComplt:
		e.drained = true
	}
	return nil
}

// drainSync takes every output a synchronous MFT has ready.
func (e *mfEnc) drainSync() error {
	for {
		got, err := e.output()
		if err != nil || !got {
			return err
		}
	}
}

// output takes one output sample and writes it; false when the MFT has none.
func (e *mfEnc) output() (bool, error) {
	var own uintptr
	if !e.provides {
		out := newStruct(8)
		if err := hrErr("MFCreateMemoryBuffer", call(e.a.createMemoryBuffer, uintptr(e.outSize), out.ptr())); err != nil {
			return false, err
		}
		buf := out.getPtr(0)
		if err := hrErr("MFCreateSample", call(e.a.createSample, out.ptr())); err != nil {
			comRelease(buf)
			return false, err
		}
		own = out.getPtr(0)
		vcall(own, slotAddBuffer, buf)
		comRelease(buf)
	}
	db := newStruct(sizeOutputDataBuffer)
	db.uptr(8, own)
	status := newStruct(4)
	hrv := vcall(e.mft, slotProcessOutput, 0, 1, db.ptr(), status.ptr())
	if evs := db.getPtr(24); evs != 0 {
		comRelease(evs)
	}
	got := db.getPtr(8)
	if got != own {
		comRelease(own)
	}
	switch uint32(hrv) { //nolint:gosec // an HRESULT
	case mfENeedMoreInput:
		comRelease(got)
		return false, nil
	case mfEStreamChange:
		// The encoder changed its output format (typically to add the
		// parameter sets): take the type it now offers.
		comRelease(got)
		t := newStruct(8)
		if err := hrErr("reading the new output type", vcall(e.mft, slotGetOutputAvailableType, 0, 0, t.ptr())); err != nil {
			return false, err
		}
		hrv := vcall(e.mft, slotSetOutputType, 0, t.getPtr(0), 0)
		comRelease(t.getPtr(0))
		if err := hrErr("setting the new output type", hrv); err != nil {
			return false, err
		}
		return true, e.streamInfo()
	}
	if err := hrErr("taking output", hrv); err != nil {
		comRelease(got)
		return false, err
	}
	defer comRelease(got)
	return true, e.write(got)
}

// write writes a sample's bitstream, the parameter sets first if the
// encoder keeps them in the media type rather than in the stream.
func (e *mfEnc) write(s uintptr) error {
	out := newStruct(8)
	if err := hrErr("reading output", vcall(s, slotConvertContiguous, out.ptr())); err != nil {
		return err
	}
	buf := out.getPtr(0)
	defer comRelease(buf)
	p, n := newStruct(8), newStruct(4)
	if err := hrErr("locking output", vcall(buf, slotBufLock, p.ptr(), 0, n.ptr())); err != nil {
		return err
	}
	data := append([]byte(nil), cbytes(p.getPtr(0), int(n.getU32(0)))...)
	vcall(buf, slotBufUnlock)
	if !e.wroteHdr {
		e.wroteHdr = true
		if !hasParamSets(data, e.cfg.Codec) {
			if hdr := e.sequenceHeader(); len(hdr) > 0 {
				data = append(hdr, data...)
			}
		}
	}
	if len(data) == 0 {
		return nil
	}
	_, err := e.w.Write(data)
	return err
}

// sequenceHeader is MF_MT_MPEG_SEQUENCE_HEADER of the output type: the
// parameter sets, Annex B.
func (e *mfEnc) sequenceHeader() []byte {
	t := newStruct(8)
	if failed(vcall(e.mft, slotGetOutputCurrentType, 0, t.ptr())) {
		return nil
	}
	mt := t.getPtr(0)
	defer comRelease(mt)
	n := newStruct(4)
	if failed(vcall(mt, slotGetBlobLen, gp(mfMTSequenceHeader), n.ptr())) || n.getU32(0) == 0 {
		return nil
	}
	b := newStruct(int(n.getU32(0)))
	if failed(vcall(mt, slotGetBlob, gp(mfMTSequenceHeader), b.ptr(), uintptr(len(b)), 0)) {
		return nil
	}
	return append([]byte(nil), b...)
}

// hasParamSets reports whether Annex B data holds an SPS.
func hasParamSets(b []byte, c Codec) bool {
	for i := 0; i+3 < len(b); i++ {
		if b[i] != 0 || b[i+1] != 0 || b[i+2] != 1 {
			continue
		}
		h := b[i+3]
		if c == HEVC && h>>1&0x3f == 33 || c == H264 && h&0x1f == 7 {
			return true
		}
	}
	return false
}

func (e *mfEnc) Close() error {
	err := e.err
	if e.mft != 0 && err == nil {
		vcall(e.mft, slotProcessMessage, mftMsgNotifyEndOfStream, 0)
		err = hrErr("finishing", vcall(e.mft, slotProcessMessage, mftMsgCommandDrain, 0))
		switch {
		case err != nil:
		case e.async:
			for !e.drained && err == nil {
				err = e.event()
			}
		default:
			err = e.drainSync()
		}
		vcall(e.mft, slotProcessMessage, mftMsgNotifyEndStreaming, 0)
	}
	e.releaseMFT()
	if e.activate != 0 {
		vcall(e.activate, slotShutdownObject)
		comRelease(e.activate)
		e.activate = 0
	}
	if e.started {
		call(e.a.shutdown)
		e.started = false
	}
	if err == nil {
		err = e.err
	}
	return err
}

func (e *mfEnc) releaseMFT() {
	comRelease(e.events)
	comRelease(e.mft)
	e.events, e.mft = 0, 0
	// What the next candidate MFT is like is its own.
	e.async, e.provides, e.credits, e.drained = false, false, 0, false
}
