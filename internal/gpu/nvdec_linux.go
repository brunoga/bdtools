//go:build linux && (amd64 || arm64)

package gpu

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/ebitengine/purego"
)

// NVDEC through CUVID's own parser: it splits the stream into pictures,
// reorders them, and calls back to decode each (cuvidDecodePicture) and to
// show each (when its picture is mapped and copied out). The callbacks come
// from within cuvidParseVideoData, on the calling thread, which holds the
// decoder's CUDA context for the duration.

var nvdecLibs = map[string][]string{"linux": {"libnvcuvid.so.1", "libnvcuvid.so"}}

type nvdecAPI struct {
	createParser, parse, destroyParser   uintptr
	createDecoder, decodePic, destroyDec uintptr
	mapFrame, unmapFrame                 uintptr
	cuInit, cuDeviceGet, cuCtxCreate     uintptr
	cuCtxDestroy, cuCtxPush, cuCtxPop    uintptr
	cuMemAllocHost, cuMemFreeHost        uintptr
	cuMemcpy2D                           uintptr
}

var (
	nvdecOnce sync.Once
	nvdecLib  *nvdecAPI
	nvdecErr  error
	// The three callbacks, made once: purego can make only so many.
	nvdecSeqCB, nvdecDecodeCB, nvdecDisplayCB uintptr
	nvdecSessions                             sync.Map // id -> *nvdec
	nvdecNextID                               atomic.Uintptr
)

func loadNVDEC() (*nvdecAPI, error) {
	nvdecOnce.Do(func() {
		cuvid, err := openLib(nvdecLibs[runtime.GOOS]...)
		if err != nil {
			nvdecErr = err
			return
		}
		cuda, err := openLib(cudaLibs[runtime.GOOS]...)
		if err != nil {
			nvdecErr = err
			return
		}
		a := &nvdecAPI{}
		for _, f := range []struct {
			p    *uintptr
			lib  uintptr
			name string
		}{
			{&a.createParser, cuvid, "cuvidCreateVideoParser"}, {&a.parse, cuvid, "cuvidParseVideoData"},
			{&a.destroyParser, cuvid, "cuvidDestroyVideoParser"}, {&a.createDecoder, cuvid, "cuvidCreateDecoder"},
			{&a.decodePic, cuvid, "cuvidDecodePicture"}, {&a.destroyDec, cuvid, "cuvidDestroyDecoder"},
			{&a.mapFrame, cuvid, "cuvidMapVideoFrame64"}, {&a.unmapFrame, cuvid, "cuvidUnmapVideoFrame64"},
			{&a.cuInit, cuda, "cuInit"}, {&a.cuDeviceGet, cuda, "cuDeviceGet"},
			{&a.cuCtxCreate, cuda, "cuCtxCreate_v2"}, {&a.cuCtxDestroy, cuda, "cuCtxDestroy_v2"},
			{&a.cuCtxPush, cuda, "cuCtxPushCurrent_v2"}, {&a.cuCtxPop, cuda, "cuCtxPopCurrent_v2"},
			{&a.cuMemAllocHost, cuda, "cuMemAllocHost_v2"}, {&a.cuMemFreeHost, cuda, "cuMemFreeHost"},
			{&a.cuMemcpy2D, cuda, "cuMemcpy2D_v2"},
		} {
			if *f.p, err = sym(f.lib, f.name); err != nil {
				nvdecErr = fmt.Errorf("%w (%v)", ErrDecodeUnavailable, err)
				return
			}
		}
		if r := call(a.cuInit, 0); r != 0 {
			nvdecErr = fmt.Errorf("%w: cuInit: %d (no NVIDIA GPU?)", ErrDecodeUnavailable, r)
			return
		}
		nvdecSeqCB = purego.NewCallback(func(user, format uintptr) uintptr { return nvdecSession(user).sequence(format) })
		nvdecDecodeCB = purego.NewCallback(func(user, params uintptr) uintptr { return nvdecSession(user).decode(params) })
		nvdecDisplayCB = purego.NewCallback(func(user, info uintptr) uintptr { return nvdecSession(user).display(info) })
		nvdecLib = a
	})
	return nvdecLib, nvdecErr
}

func nvdecSession(id uintptr) *nvdec {
	d, _ := nvdecSessions.Load(id)
	return d.(*nvdec)
}

// nvdecHost is a page-locked host buffer a picture is copied into.
type nvdecHost struct {
	p    uintptr
	size int
}

type nvdec struct {
	a       *nvdecAPI
	cfg     DecodeConfig
	id      uintptr
	ctx     uintptr
	parser  uintptr
	dec     uintptr
	picture func(*DecodedPicture) error

	// From the sequence header.
	width, height  int // the displayed picture
	surfaceHeight  int // the decoded surface's, whose chroma follows its luma
	depth          int
	color          ColorInfo
	rateNum, rateD int

	// feeding is the timestamp of the access unit being parsed, and pts
	// each decode surface's picture's, set as it is decoded. The parser's
	// own display timestamps are the pending ones handed out in ascending
	// order, not each picture's: when it drops leading pictures (RASL after
	// a stream's first CRA), theirs stay pending and the pictures shown
	// take them.
	feeding int64
	pts     map[uint32]int64

	ready []*DecodedPicture // copied out, waiting to be handed on
	free  []nvdecHost
	used  []nvdecHost
	err   error // the first error a callback met
}

func openNVDEC(cfg DecodeConfig, picture func(*DecodedPicture) error) (Decoder, error) {
	a, err := loadNVDEC()
	if err != nil {
		return nil, err
	}
	codec, ok := map[VideoCodec]uint32{DecodeH264: cuvCodecH264, DecodeHEVC: cuvCodecHEVC, DecodeVC1: cuvCodecVC1,
		DecodeMPEG2: cuvCodecMPEG2, DecodeAV1: cuvCodecAV1}[cfg.Codec]
	if !ok {
		return nil, fmt.Errorf("%w: codec %d", ErrDecodeUnavailable, cfg.Codec)
	}
	d := &nvdec{a: a, cfg: cfg, picture: picture}
	dev := newStruct(8)
	if r := call(a.cuDeviceGet, dev.ptr(), 0); r != 0 {
		return nil, fmt.Errorf("%w: cuDeviceGet: %d", ErrDecodeUnavailable, r)
	}
	ctx := newStruct(8)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if r := call(a.cuCtxCreate, ctx.ptr(), 0, uintptr(dev.getU32(0))); r != 0 {
		return nil, fmt.Errorf("%w: cuCtxCreate: %d", ErrDecodeUnavailable, r)
	}
	d.ctx = ctx.getPtr(0)
	// cuCtxCreate made it current here; it is pushed again around each use.
	call(a.cuCtxPop, newStruct(8).ptr())

	d.id = nvdecNextID.Add(1)
	nvdecSessions.Store(d.id, d)
	pp := newStruct(cuvSizeParserParams)
	pp.u32(cuvPPCodec, codec)
	pp.u32(cuvPPMaxSurfaces, 1) // set from the sequence header, by the sequence callback's answer
	pp.u32(cuvPPClockRate, 90000)
	pp.u32(cuvPPErrorThreshold, 100) // decode damaged pictures as best it can, as the H.264 decoder conceals
	pp.u32(cuvPPDisplayDelay, 4)
	pp.uptr(cuvPPUserData, d.id)
	pp.uptr(cuvPPSequence, nvdecSeqCB)
	pp.uptr(cuvPPDecode, nvdecDecodeCB)
	pp.uptr(cuvPPDisplay, nvdecDisplayCB)
	parser := newStruct(8)
	if r := call(a.createParser, parser.ptr(), pp.ptr()); r != 0 {
		_ = d.Close()
		return nil, fmt.Errorf("%w: cuvidCreateVideoParser: %d", ErrDecodeUnavailable, r)
	}
	d.parser = parser.getPtr(0)
	return d, nil
}

// withContext runs f on a locked thread with the decoder's context current.
func (d *nvdec) withContext(f func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if r := call(d.a.cuCtxPush, d.ctx); r != 0 {
		return fmt.Errorf("nvdec: cuCtxPushCurrent: %d", r)
	}
	defer call(d.a.cuCtxPop, newStruct(8).ptr())
	return f()
}

func (d *nvdec) Decode(au []byte, pts int64) error {
	return d.feed(au, pts, cuvPktHasTimestamp|cuvPktEndOfPicture)
}

func (d *nvdec) Flush() error { return d.feed(nil, 0, cuvPktEOS) }

func (d *nvdec) feed(au []byte, pts int64, flags uint32) error {
	if d.err != nil {
		return d.err
	}
	err := d.withContext(func() error {
		pkt := newStruct(cuvSizePacket)
		pkt.u64(cuvPktFlags, uint64(flags))
		pkt.u64(cuvPktSize, uint64(len(au)))
		var data cstruct
		if len(au) > 0 {
			data = newStruct(len(au)) // C must not hold a pointer into a Go slice it did not get as one
			copy(data, au)
			pkt.uptr(cuvPktPayload, data.ptr())
		}
		pkt.u64(cuvPktTimestamp, uint64(pts)) //nolint:gosec // a timestamp, passed through
		d.feeding = pts
		r := call(d.a.parse, d.parser, pkt.ptr())
		runtime.KeepAlive(data)
		if d.err != nil {
			return d.err
		}
		if r != 0 {
			return fmt.Errorf("nvdec: cuvidParseVideoData: %d", r)
		}
		return nil
	})
	if err != nil {
		d.err = err
		return err
	}
	// Hand on what came out, outside the parser.
	for len(d.ready) > 0 {
		p := d.ready[0]
		d.ready = d.ready[1:]
		if err := d.picture(p); err != nil {
			d.err = err
			return err
		}
	}
	d.free, d.used = append(d.free, d.used...), d.used[:0]
	return nil
}

// sequence is called with the sequence header's format: the decoder is
// made then. It answers the number of decode surfaces the parser should
// cycle through (0 is an error).
func (d *nvdec) sequence(format uintptr) uintptr {
	f := cstruct(cbytes(format, cuvFmtMatrix+1))
	if d.dec != 0 {
		// The same stream again (a new sequence header): nothing to do. A
		// change of size would need a new decoder.
		if int(f.getU32(cuvFmtCodedWidth)) != 0 && d.width != 0 &&
			int(int32(f.getU32(cuvFmtRight))-int32(f.getU32(cuvFmtLeft))) != d.width { //nolint:gosec // picture sizes
			d.err = errors.New("nvdec: the picture size changed mid-stream")
			return 0
		}
		return uintptr(f[cuvFmtMinSurfaces])
	}
	surfaces := max(int(f[cuvFmtMinSurfaces]), 1) + 4
	codedW, codedH := f.getU32(cuvFmtCodedWidth), f.getU32(cuvFmtCodedHeight)
	left, top := int32(f.getU32(cuvFmtLeft)), int32(f.getU32(cuvFmtTop))         //nolint:gosec // display area
	right, bottom := int32(f.getU32(cuvFmtRight)), int32(f.getU32(cuvFmtBottom)) //nolint:gosec // display area
	d.width, d.height = int(right-left), int(bottom-top)
	d.surfaceHeight = d.height
	d.depth = 8 + int(f[cuvFmtDepthLuma])
	d.rateNum, d.rateD = int(f.getU32(cuvFmtRateNum)), int(f.getU32(cuvFmtRateDen))
	d.color = ColorInfo{Primaries: int(f[cuvFmtPrimaries]), Transfer: int(f[cuvFmtTransfer]), Matrix: int(f[cuvFmtMatrix]),
		FullRange: f[cuvFmtSignalFlags]&cuvFmtFullRangeMask != 0}

	ci := newStruct(cuvSizeCreateInfo)
	ci.u64(cuvCIWidth, uint64(codedW))
	ci.u64(cuvCIHeight, uint64(codedH))
	ci.u64(cuvCISurfaces, uint64(surfaces)) //nolint:gosec // small
	ci.u32(cuvCICodec, f.getU32(cuvFmtCodec))
	ci.u32(cuvCIChroma, uint32(f[cuvFmtChroma]))
	ci.u64(cuvCIFlags, cuvCreatePreferCUVID)
	ci.u64(cuvCIDepth, uint64(f[cuvFmtDepthLuma]))
	ci.u64(cuvCIMaxWidth, uint64(codedW))
	ci.u64(cuvCIMaxHeight, uint64(codedH))
	ci.u16(cuvCIDisplayArea, uint16(left))     //nolint:gosec // display area
	ci.u16(cuvCIDisplayArea+2, uint16(top))    //nolint:gosec // display area
	ci.u16(cuvCIDisplayArea+4, uint16(right))  //nolint:gosec // display area
	ci.u16(cuvCIDisplayArea+6, uint16(bottom)) //nolint:gosec // display area
	out := uint32(cuvSurfaceNV12)
	if d.depth > 8 {
		out = cuvSurfaceP016
	}
	ci.u32(cuvCIOutputFormat, out)
	deint := uint32(cuvDeinterlaceWeave)
	if f[cuvFmtProgressive] == 0 {
		deint = cuvDeinterlaceAdaptive
	}
	ci.u32(cuvCIDeinterlace, deint)
	ci.u64(cuvCITargetWidth, uint64(d.width))   //nolint:gosec // a picture size
	ci.u64(cuvCITargetHeight, uint64(d.height)) //nolint:gosec // a picture size
	ci.u64(cuvCIOutputSurfaces, 2)
	dec := newStruct(8)
	if r := call(d.a.createDecoder, dec.ptr(), ci.ptr()); r != 0 {
		d.err = fmt.Errorf("%w: cuvidCreateDecoder (%s %dx%d, %d-bit): %d", ErrDecodeUnavailable, d.cfg.Codec, codedW, codedH, d.depth, r)
		return 0
	}
	d.dec = dec.getPtr(0)
	return uintptr(surfaces)
}

func (d *nvdec) decode(params uintptr) uintptr {
	if d.dec == 0 {
		return 0
	}
	if d.pts == nil {
		d.pts = map[uint32]int64{}
	}
	pp := cstruct(cbytes(params, cuvPicSecondField+4))
	if pp.getU32(cuvPicSecondField) == 0 { // a field pair is its first field's
		d.pts[pp.getU32(cuvPicCurrIdx)] = d.feeding
	}
	if r := call(d.a.decodePic, d.dec, params); r != 0 {
		d.err = fmt.Errorf("nvdec: cuvidDecodePicture: %d", r)
		return 0
	}
	return 1
}

// display copies a picture ready for display out of the GPU.
func (d *nvdec) display(info uintptr) uintptr {
	if info == 0 { // the end of the stream
		return 1
	}
	di := cstruct(cbytes(info, cuvSizeDispInfo))
	pp := newStruct(cuvSizeProcParams)
	pp.u32(cuvPPProgressive, di.getU32(cuvDIProgressive))
	pp.u32(cuvPPTopFirst, di.getU32(cuvDITopFirst))
	dev, pitch := newStruct(8), newStruct(4)
	if r := call(d.a.mapFrame, d.dec, uintptr(di.getU32(cuvDIIndex)), dev.ptr(), pitch.ptr(), pp.ptr()); r != 0 {
		d.err = fmt.Errorf("nvdec: cuvidMapVideoFrame: %d", r)
		return 0
	}
	defer call(d.a.unmapFrame, d.dec, dev.getPtr(0))
	bps := 1
	if d.depth > 8 {
		bps = 2
	}
	row := d.width * bps
	size := row * d.height * 3 / 2
	host, err := d.hostBuffer(size)
	if err != nil {
		d.err = err
		return 0
	}
	src, srcPitch := dev.getPtr(0), uintptr(pitch.getU32(0))
	copyPlane := func(from uintptr, to uintptr, rows int) error {
		m := newStruct(cuSizeMemcpy2D)
		m.u32(cuMCSrcType, cuMemoryDevice)
		m.uptr(cuMCSrcDevice, from)
		m.u64(cuMCSrcPitch, uint64(srcPitch))
		m.u32(cuMCDstType, cuMemoryHost)
		m.uptr(cuMCDstHost, to)
		m.u64(cuMCDstPitch, uint64(row)) //nolint:gosec // a row size
		m.u64(cuMCWidth, uint64(row))    //nolint:gosec // a row size
		m.u64(cuMCHeight, uint64(rows))  //nolint:gosec // a picture height
		if r := call(d.a.cuMemcpy2D, m.ptr()); r != 0 {
			return fmt.Errorf("nvdec: cuMemcpy2D: %d", r)
		}
		return nil
	}
	if err := copyPlane(src, host.p, d.height); err != nil {
		d.err = err
		return 0
	}
	chroma := src + srcPitch*uintptr((d.surfaceHeight+1)&^1)
	if err := copyPlane(chroma, host.p+uintptr(row*d.height), d.height/2); err != nil {
		d.err = err
		return 0
	}
	b := cbytes(host.p, size)
	pts, ok := d.pts[di.getU32(cuvDIIndex)]
	if !ok {
		pts = int64(di.getPtr(cuvDITimestamp)) //nolint:gosec // passed through
	}
	d.ready = append(d.ready, &DecodedPicture{Width: d.width, Height: d.height, Depth: d.depth,
		Y: b[:row*d.height], UV: b[row*d.height:], Pitch: row, PTS: pts,
		Color: d.color, FrameRateNum: d.rateNum, FrameRateDen: d.rateD})
	return 1
}

// hostBuffer gives a page-locked buffer of at least size bytes, free until
// the pictures of this feed have been handed on.
func (d *nvdec) hostBuffer(size int) (nvdecHost, error) {
	for i, h := range d.free {
		if h.size >= size {
			d.free = append(d.free[:i], d.free[i+1:]...)
			d.used = append(d.used, h)
			return h, nil
		}
	}
	p := newStruct(8)
	if r := call(d.a.cuMemAllocHost, p.ptr(), uintptr(size)); r != 0 {
		return nvdecHost{}, fmt.Errorf("nvdec: cuMemAllocHost: %d", r)
	}
	h := nvdecHost{p: p.getPtr(0), size: size}
	d.used = append(d.used, h)
	return h, nil
}

func (d *nvdec) Close() error {
	if d.ctx == 0 {
		return nil
	}
	_ = d.withContext(func() error {
		if d.parser != 0 {
			call(d.a.destroyParser, d.parser)
		}
		if d.dec != 0 {
			call(d.a.destroyDec, d.dec)
		}
		for _, h := range append(d.free, d.used...) {
			call(d.a.cuMemFreeHost, h.p)
		}
		return nil
	})
	call(d.a.cuCtxDestroy, d.ctx)
	d.ctx = 0
	nvdecSessions.Delete(d.id)
	return nil
}
