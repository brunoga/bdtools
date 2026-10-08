//go:build darwin && (amd64 || arm64)

package gpu

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"
)

// VideoToolbox through its C API: a compression session takes NV12 pixel
// buffers from its own pool and hands each coded frame to a callback, on one
// of its threads, as length-prefixed NAL units with the parameter sets kept
// aside in the format description. The callback turns them into Annex B
// and queues them; Encode and Close write the queue out.

const (
	vtFrameworks = "/System/Library/Frameworks/"

	cfNumberSInt32  = 3
	cfNumberFloat64 = 6

	cvPixelFormat420v = 0x34323076 // '420v': NV12, video range
	cvPixelFormatx420 = 0x78343230 // 'x420': P010, video range
	cmCodecH264       = 0x61766331 // 'avc1'
	cmCodecHEVC       = 0x68766331 // 'hvc1'
	cmTimeValid       = 1
	vtErrHardware     = -12915 // kVTCouldNotFindVideoEncoderErr
)

// cmTime is CMTime.
type cmTime struct {
	Value     int64
	Timescale int32
	Flags     uint32
	Epoch     int64
}

// vtAPI is the part of CoreFoundation, CoreVideo, CoreMedia and
// VideoToolbox the encoder uses.
type vtAPI struct {
	dictCreate   func(alloc uintptr, capacity int, keyCB, valueCB uintptr) uintptr
	dictSet      func(d, k, v uintptr)
	dictGet      func(d, k uintptr) uintptr
	numberCreate func(alloc uintptr, typ int, value unsafe.Pointer) uintptr
	release      func(uintptr)
	arrayCount   func(uintptr) int
	arrayAt      func(uintptr, int) uintptr
	boolValue    func(uintptr) bool

	sessionCreate func(alloc uintptr, w, h int32, codec uint32, spec, srcAttrs, dataAlloc, cb, refcon uintptr, out *uintptr) int32
	setProperty   func(session, key, value uintptr) int32
	prepare       func(session uintptr) int32
	encodeFrame   func(session, image uintptr, pts, duration cmTime, props, refcon uintptr, infoOut *uint32) int32
	complete      func(session uintptr, until cmTime) int32
	invalidate    func(session uintptr)
	pool          func(session uintptr) uintptr

	poolCreate   func(alloc, pool uintptr, out *uintptr) int32
	lock         func(pb uintptr, flags uint64) int32
	unlock       func(pb uintptr, flags uint64) int32
	planeBase    func(pb uintptr, plane int) uintptr
	planeStride  func(pb uintptr, plane int) int
	sampleData   func(sbuf uintptr) uintptr
	sampleFormat func(sbuf uintptr) uintptr
	sampleAttach func(sbuf uintptr, create bool) uintptr
	blockLength  func(bb uintptr) int
	blockCopy    func(bb uintptr, offset, length int, dst unsafe.Pointer) int32
	h264ParamSet func(desc uintptr, index int, ptr *uintptr, size, count *int, nalLen *int32) int32
	hevcParamSet func(desc uintptr, index int, ptr *uintptr, size, count *int, nalLen *int32) int32

	keyCB, valueCB  uintptr // &kCFTypeDictionaryKeyCallBacks, &...ValueCallBacks
	cfTrue, cfFalse uintptr
	keys            map[string]uintptr // CFString constants
}

var (
	vtOnce     sync.Once
	vtLoaded   *vtAPI
	vtLoadErr  error
	vtCallback uintptr
	vtSessions sync.Map // refcon -> *vtEnc

	// vtRequireHardware refuses Apple's software encoder; tests on machines
	// without a media engine (CI's virtual Macs) turn it off.
	vtRequireHardware = true
)

func loadVT() (*vtAPI, error) {
	vtOnce.Do(func() {
		vtLoaded, vtLoadErr = loadVTLibs()
		if vtLoadErr == nil {
			vtCallback = purego.NewCallback(vtOutput)
		}
	})
	return vtLoaded, vtLoadErr
}

func loadVTLibs() (*vtAPI, error) {
	libs := map[string]uintptr{}
	for _, n := range []string{"CoreFoundation", "CoreVideo", "CoreMedia", "VideoToolbox"} {
		h, err := openLib(vtFrameworks + n + ".framework/" + n)
		if err != nil {
			return nil, err
		}
		libs[n] = h
	}
	a := &vtAPI{keys: map[string]uintptr{}}
	var err error
	fn := func(fptr any, lib, name string) {
		if err != nil {
			return
		}
		var p uintptr
		if p, err = sym(libs[lib], name); err == nil {
			purego.RegisterFunc(fptr, p)
		}
	}
	// data returns the address of a C variable.
	data := func(lib, name string) uintptr {
		if err != nil {
			return 0
		}
		var p uintptr
		p, err = sym(libs[lib], name)
		return p
	}
	// constant reads a C pointer variable (a CFStringRef, a CFBooleanRef).
	constant := func(lib, name string) uintptr {
		p := data(lib, name)
		if p == 0 {
			return 0
		}
		return *(*uintptr)(cptr(p))
	}
	fn(&a.dictCreate, "CoreFoundation", "CFDictionaryCreateMutable")
	fn(&a.dictSet, "CoreFoundation", "CFDictionarySetValue")
	fn(&a.dictGet, "CoreFoundation", "CFDictionaryGetValue")
	fn(&a.numberCreate, "CoreFoundation", "CFNumberCreate")
	fn(&a.release, "CoreFoundation", "CFRelease")
	fn(&a.arrayCount, "CoreFoundation", "CFArrayGetCount")
	fn(&a.arrayAt, "CoreFoundation", "CFArrayGetValueAtIndex")
	fn(&a.boolValue, "CoreFoundation", "CFBooleanGetValue")
	fn(&a.sessionCreate, "VideoToolbox", "VTCompressionSessionCreate")
	fn(&a.setProperty, "VideoToolbox", "VTSessionSetProperty")
	fn(&a.prepare, "VideoToolbox", "VTCompressionSessionPrepareToEncodeFrames")
	fn(&a.encodeFrame, "VideoToolbox", "VTCompressionSessionEncodeFrame")
	fn(&a.complete, "VideoToolbox", "VTCompressionSessionCompleteFrames")
	fn(&a.invalidate, "VideoToolbox", "VTCompressionSessionInvalidate")
	fn(&a.pool, "VideoToolbox", "VTCompressionSessionGetPixelBufferPool")
	fn(&a.poolCreate, "CoreVideo", "CVPixelBufferPoolCreatePixelBuffer")
	fn(&a.lock, "CoreVideo", "CVPixelBufferLockBaseAddress")
	fn(&a.unlock, "CoreVideo", "CVPixelBufferUnlockBaseAddress")
	fn(&a.planeBase, "CoreVideo", "CVPixelBufferGetBaseAddressOfPlane")
	fn(&a.planeStride, "CoreVideo", "CVPixelBufferGetBytesPerRowOfPlane")
	fn(&a.sampleData, "CoreMedia", "CMSampleBufferGetDataBuffer")
	fn(&a.sampleFormat, "CoreMedia", "CMSampleBufferGetFormatDescription")
	fn(&a.sampleAttach, "CoreMedia", "CMSampleBufferGetSampleAttachmentsArray")
	fn(&a.blockLength, "CoreMedia", "CMBlockBufferGetDataLength")
	fn(&a.blockCopy, "CoreMedia", "CMBlockBufferCopyDataBytes")
	fn(&a.h264ParamSet, "CoreMedia", "CMVideoFormatDescriptionGetH264ParameterSetAtIndex")
	fn(&a.hevcParamSet, "CoreMedia", "CMVideoFormatDescriptionGetHEVCParameterSetAtIndex")
	a.keyCB = data("CoreFoundation", "kCFTypeDictionaryKeyCallBacks")
	a.valueCB = data("CoreFoundation", "kCFTypeDictionaryValueCallBacks")
	a.cfTrue = constant("CoreFoundation", "kCFBooleanTrue")
	a.cfFalse = constant("CoreFoundation", "kCFBooleanFalse")
	for _, k := range []struct{ lib, name string }{
		{"CoreVideo", "kCVPixelBufferPixelFormatTypeKey"},
		{"CoreVideo", "kCVPixelBufferWidthKey"},
		{"CoreVideo", "kCVPixelBufferHeightKey"},
		{"CoreMedia", "kCMSampleAttachmentKey_NotSync"},
		{"VideoToolbox", "kVTVideoEncoderSpecification_EnableHardwareAcceleratedVideoEncoder"},
		{"VideoToolbox", "kVTVideoEncoderSpecification_RequireHardwareAcceleratedVideoEncoder"},
		{"VideoToolbox", "kVTCompressionPropertyKey_RealTime"},
		{"VideoToolbox", "kVTCompressionPropertyKey_ProfileLevel"},
		{"VideoToolbox", "kVTCompressionPropertyKey_AllowFrameReordering"},
		{"VideoToolbox", "kVTCompressionPropertyKey_MaxKeyFrameInterval"},
		{"VideoToolbox", "kVTCompressionPropertyKey_ExpectedFrameRate"},
		{"VideoToolbox", "kVTCompressionPropertyKey_Quality"},
		{"VideoToolbox", "kVTProfileLevel_H264_High_AutoLevel"},
		{"VideoToolbox", "kVTProfileLevel_HEVC_Main_AutoLevel"},
		{"VideoToolbox", "kVTProfileLevel_HEVC_Main10_AutoLevel"},
		{"VideoToolbox", "kVTCompressionPropertyKey_ColorPrimaries"},
		{"VideoToolbox", "kVTCompressionPropertyKey_TransferFunction"},
		{"VideoToolbox", "kVTCompressionPropertyKey_YCbCrMatrix"},
		{"CoreVideo", "kCVImageBufferColorPrimaries_ITU_R_709_2"},
		{"CoreVideo", "kCVImageBufferColorPrimaries_ITU_R_2020"},
		{"CoreVideo", "kCVImageBufferColorPrimaries_P3_D65"},
		{"CoreVideo", "kCVImageBufferTransferFunction_ITU_R_709_2"},
		{"CoreVideo", "kCVImageBufferTransferFunction_SMPTE_ST_2084_PQ"},
		{"CoreVideo", "kCVImageBufferTransferFunction_ITU_R_2100_HLG"},
		{"CoreVideo", "kCVImageBufferYCbCrMatrix_ITU_R_709_2"},
		{"CoreVideo", "kCVImageBufferYCbCrMatrix_ITU_R_2020"},
	} {
		a.keys[k.name] = constant(k.lib, k.name)
	}
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (a *vtAPI) number32(v int32) uintptr {
	return a.numberCreate(0, cfNumberSInt32, unsafe.Pointer(&v))
}

func (a *vtAPI) float(v float64) uintptr {
	return a.numberCreate(0, cfNumberFloat64, unsafe.Pointer(&v))
}

func (a *vtAPI) dict() uintptr { return a.dictCreate(0, 0, a.keyCB, a.valueCB) }

type vtEnc struct {
	a       *vtAPI
	w       io.Writer
	cfg     Config
	session uintptr
	refcon  uintptr
	n       int64

	mu  sync.Mutex
	out [][]byte // coded frames from the callback, in decoding order
	err error    // the first error the callback met
}

var vtNextRefcon atomic.Uintptr

func openVideoToolbox(cfg Config, w io.Writer) (Encoder, error) {
	if cfg.Codec == AV1 {
		return nil, fmt.Errorf("%w: VideoToolbox has no AV1 encoder", ErrUnavailable)
	}
	a, err := loadVT()
	if err != nil {
		return nil, err
	}
	e := &vtEnc{a: a, w: w, cfg: cfg}
	e.refcon = vtNextRefcon.Add(1)
	vtSessions.Store(e.refcon, e)

	spec := a.dict()
	defer a.release(spec)
	hw := a.keys["kVTVideoEncoderSpecification_EnableHardwareAcceleratedVideoEncoder"]
	if vtRequireHardware {
		hw = a.keys["kVTVideoEncoderSpecification_RequireHardwareAcceleratedVideoEncoder"]
	}
	a.dictSet(spec, hw, a.cfTrue)

	src := a.dict()
	defer a.release(src)
	format := int32(cvPixelFormat420v)
	if cfg.BitDepth == 10 {
		format = cvPixelFormatx420
	}
	for k, v := range map[string]int32{
		"kCVPixelBufferPixelFormatTypeKey": format,
		"kCVPixelBufferWidthKey":           int32(cfg.Width),  //nolint:gosec // frame size
		"kCVPixelBufferHeightKey":          int32(cfg.Height), //nolint:gosec // frame size
	} {
		n := a.number32(v)
		a.dictSet(src, a.keys[k], n)
		a.release(n)
	}

	codec, profile := uint32(cmCodecH264), a.keys["kVTProfileLevel_H264_High_AutoLevel"]
	if cfg.Codec == HEVC {
		codec, profile = cmCodecHEVC, a.keys["kVTProfileLevel_HEVC_Main_AutoLevel"]
		if cfg.BitDepth == 10 {
			profile = a.keys["kVTProfileLevel_HEVC_Main10_AutoLevel"]
		}
	}
	var session uintptr
	st := a.sessionCreate(0, int32(cfg.Width), int32(cfg.Height), codec, spec, src, 0, vtCallback, e.refcon, &session) //nolint:gosec // frame size
	if st != 0 {
		vtSessions.Delete(e.refcon)
		if st == vtErrHardware {
			return nil, fmt.Errorf("%w: no hardware encoder for this codec", ErrUnavailable)
		}
		return nil, fmt.Errorf("%w: VTCompressionSessionCreate: %d", ErrUnavailable, st)
	}
	e.session = session

	gop := a.number32(int32(cfg.GOP)) //nolint:gosec // small
	rate := a.float(float64(cfg.FPSNum) / float64(cfg.FPSDen))
	quality := a.float(VTQuality(cfg.QP))
	defer func() {
		a.release(gop)
		a.release(rate)
		a.release(quality)
	}()
	for _, p := range []struct {
		key   string
		value uintptr
	}{
		{"kVTCompressionPropertyKey_RealTime", a.cfFalse},
		{"kVTCompressionPropertyKey_ProfileLevel", profile},
		{"kVTCompressionPropertyKey_AllowFrameReordering", a.cfTrue},
		{"kVTCompressionPropertyKey_MaxKeyFrameInterval", gop},
		{"kVTCompressionPropertyKey_ExpectedFrameRate", rate},
		{"kVTCompressionPropertyKey_Quality", quality},
	} {
		if st := a.setProperty(session, a.keys[p.key], p.value); st != 0 {
			_ = e.Close()
			return nil, fmt.Errorf("videotoolbox: setting %s: %d", p.key, st)
		}
	}
	// The colour signalling, where VideoToolbox has a name for it.
	if c := cfg.Color; c != nil {
		for _, p := range []struct{ key, value string }{
			{"kVTCompressionPropertyKey_ColorPrimaries", map[int]string{1: "kCVImageBufferColorPrimaries_ITU_R_709_2",
				9: "kCVImageBufferColorPrimaries_ITU_R_2020", 12: "kCVImageBufferColorPrimaries_P3_D65"}[c.Primaries]},
			{"kVTCompressionPropertyKey_TransferFunction", map[int]string{1: "kCVImageBufferTransferFunction_ITU_R_709_2",
				16: "kCVImageBufferTransferFunction_SMPTE_ST_2084_PQ", 18: "kCVImageBufferTransferFunction_ITU_R_2100_HLG"}[c.Transfer]},
			{"kVTCompressionPropertyKey_YCbCrMatrix", map[int]string{1: "kCVImageBufferYCbCrMatrix_ITU_R_709_2",
				9: "kCVImageBufferYCbCrMatrix_ITU_R_2020"}[c.Matrix]},
		} {
			if p.value == "" {
				continue
			}
			if st := a.setProperty(session, a.keys[p.key], a.keys[p.value]); st != 0 {
				_ = e.Close()
				return nil, fmt.Errorf("videotoolbox: setting %s: %d", p.key, st)
			}
		}
	}
	if st := a.prepare(session); st != 0 {
		_ = e.Close()
		return nil, fmt.Errorf("videotoolbox: preparing: %d", st)
	}
	return e, nil
}

func (e *vtEnc) Encode(fill func(*Picture)) error {
	if err := e.flush(); err != nil {
		return err
	}
	a := e.a
	pool := a.pool(e.session)
	if pool == 0 {
		return errors.New("videotoolbox: the session has no pixel buffer pool")
	}
	var pb uintptr
	if st := a.poolCreate(0, pool, &pb); st != 0 {
		return fmt.Errorf("videotoolbox: creating a pixel buffer: %d", st)
	}
	defer a.release(pb)
	if st := a.lock(pb, 0); st != 0 {
		return fmt.Errorf("videotoolbox: locking a pixel buffer: %d", st)
	}
	h, depth := e.cfg.Height, e.cfg.BitDepth
	row := e.cfg.Width
	if depth == 10 {
		row *= 2 // P010: two bytes a sample
	}
	yp, uvp := a.planeStride(pb, 0), a.planeStride(pb, 1)
	y, uv := cbytes(a.planeBase(pb, 0), yp*h), cbytes(a.planeBase(pb, 1), uvp*h/2)
	if yp == uvp {
		fill(&Picture{Y: y, UV: uv, Pitch: yp, Depth: depth})
	} else {
		// One pitch for both planes is what fill takes: draw, then copy.
		p := &Picture{Y: make([]byte, row*h), UV: make([]byte, row*h/2), Pitch: row, Depth: depth}
		fill(p)
		for r := range h {
			copy(y[r*yp:r*yp+row], p.Y[r*row:])
		}
		for r := range h / 2 {
			copy(uv[r*uvp:r*uvp+row], p.UV[r*row:])
		}
	}
	a.unlock(pb, 0)

	den, num := int64(e.cfg.FPSDen), int32(e.cfg.FPSNum) //nolint:gosec // frame rate
	pts := cmTime{Value: e.n * den, Timescale: num, Flags: cmTimeValid}
	dur := cmTime{Value: den, Timescale: num, Flags: cmTimeValid}
	e.n++
	if st := a.encodeFrame(e.session, pb, pts, dur, 0, 0, nil); st != 0 {
		return fmt.Errorf("videotoolbox: encoding: %d", st)
	}
	return e.flush()
}

// flush writes what the callback has queued.
func (e *vtEnc) flush() error {
	e.mu.Lock()
	out, err := e.out, e.err
	e.out = nil
	e.mu.Unlock()
	if err != nil {
		return err
	}
	for _, b := range out {
		if _, err := e.w.Write(b); err != nil {
			e.mu.Lock()
			e.err = err
			e.mu.Unlock()
			return err
		}
	}
	return nil
}

func (e *vtEnc) Close() error {
	var err error
	if e.session != 0 {
		// Every held-back frame comes out before this returns.
		if st := e.a.complete(e.session, cmTime{}); st != 0 {
			err = fmt.Errorf("videotoolbox: finishing: %d", st)
		}
		e.a.invalidate(e.session)
		e.a.release(e.session)
		e.session = 0
	}
	vtSessions.Delete(e.refcon)
	if ferr := e.flush(); err == nil {
		err = ferr
	}
	return err
}

// vtOutput is the session's output callback, on a VideoToolbox thread. Every
// argument is a register's worth: status is an OSStatus.
func vtOutput(refcon, _, status, _, sbuf uintptr) {
	v, ok := vtSessions.Load(refcon)
	if !ok {
		return
	}
	e := v.(*vtEnc)                                 //nolint:forcetypeassert // only *vtEnc is stored
	b, err := e.annexB(int32(uint32(status)), sbuf) //nolint:gosec // the low 32 bits are the OSStatus
	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil && e.err == nil {
		e.err = err
	}
	if b != nil {
		e.out = append(e.out, b)
	}
}

// annexB turns a coded frame into Annex B, with the parameter sets in front
// of a keyframe.
func (e *vtEnc) annexB(status int32, sbuf uintptr) ([]byte, error) {
	a := e.a
	if status != 0 {
		return nil, fmt.Errorf("videotoolbox: a frame failed: %d", status)
	}
	if sbuf == 0 {
		return nil, nil // dropped
	}
	var out []byte
	nalLen := int32(4)
	key := true
	if att := a.sampleAttach(sbuf, false); att != 0 && a.arrayCount(att) > 0 {
		if ns := a.dictGet(a.arrayAt(att, 0), a.keys["kCMSampleAttachmentKey_NotSync"]); ns != 0 && a.boolValue(ns) {
			key = false
		}
	}
	if key {
		get := a.h264ParamSet
		if e.cfg.Codec == HEVC {
			get = a.hevcParamSet
		}
		desc := a.sampleFormat(sbuf)
		var count int
		if st := get(desc, 0, nil, nil, &count, &nalLen); st != 0 {
			return nil, fmt.Errorf("videotoolbox: reading the parameter sets: %d", st)
		}
		for i := range count {
			var p uintptr
			var size int
			if st := get(desc, i, &p, &size, nil, nil); st != 0 {
				return nil, fmt.Errorf("videotoolbox: reading a parameter set: %d", st)
			}
			out = append(out, 0, 0, 0, 1)
			out = append(out, cbytes(p, size)...)
		}
	}
	bb := a.sampleData(sbuf)
	n := a.blockLength(bb)
	if n == 0 {
		return out, nil
	}
	data := make([]byte, n)
	if st := a.blockCopy(bb, 0, n, unsafe.Pointer(&data[0])); st != 0 {
		return nil, fmt.Errorf("videotoolbox: copying a frame: %d", st)
	}
	l := int(nalLen)
	if l < 1 || l > 4 {
		return nil, fmt.Errorf("videotoolbox: %d-byte NAL lengths", l)
	}
	for len(data) >= l {
		var size int
		for _, c := range data[:l] {
			size = size<<8 | int(c)
		}
		data = data[l:]
		if size > len(data) {
			return nil, errors.New("videotoolbox: a NAL unit runs past its frame")
		}
		out = append(out, 0, 0, 0, 1)
		out = append(out, data[:size]...)
		data = data[size:]
	}
	return out, nil
}
