//go:build darwin && (amd64 || arm64)

package gpu

import (
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Decoding with VideoToolbox: a decompression session, made from the
// stream's parameter sets, takes each access unit as a sample buffer of
// length-prefixed NAL units and hands each picture to a callback (here,
// within the call that decodes it, or the one that ends the stream). Each
// access unit's timestamp goes with it as the frame's refcon, and comes
// back with its picture. H.264 and HEVC; VideoToolbox decodes neither VC-1
// nor (on every Mac) MPEG-2, which ffmpeg does instead.

const (
	vtDecodeTemporalProcessing = 1 << 3 // kVTDecodeFrame_EnableTemporalProcessing: display order
	vtDecodeInfoFrameDropped   = 1 << 1
	cmBlockBufferAssureMemory  = 1 << 0
)

// vtDecAPI is the decompression side of VideoToolbox and CoreMedia.
type vtDecAPI struct {
	*vtAPI
	h264Desc   func(alloc uintptr, count int, ptrs, sizes unsafe.Pointer, nalLen int32, out *uintptr) int32
	hevcDesc   func(alloc uintptr, count int, ptrs, sizes unsafe.Pointer, nalLen int32, ext uintptr, out *uintptr) int32
	create     func(alloc, desc, spec, attrs uintptr, cb unsafe.Pointer, out *uintptr) int32
	decode     func(session, sbuf uintptr, flags uint32, refcon uintptr, info *uint32) int32
	finish     func(session uintptr) int32
	wait       func(session uintptr) int32
	invalidate func(session uintptr)
	canAccept  func(session, desc uintptr) bool
	blockNew   func(alloc, mem uintptr, length int, blockAlloc, custom uintptr, offset, dataLen int, flags uint32, out *uintptr) int32
	blockFill  func(src unsafe.Pointer, dst uintptr, offset, length int) int32
	sampleNew  func(alloc, data, desc uintptr, n, nTiming int, timing uintptr, nSizes int, sizes unsafe.Pointer, out *uintptr) int32
	width      func(pb uintptr) int
	height     func(pb uintptr) int
	attachment func(buf, key uintptr, mode unsafe.Pointer) uintptr
	attachKeys map[string]uintptr // the colour attachments' keys
}

var (
	vtDecOnce     sync.Once
	vtDecLoaded   *vtDecAPI
	vtDecErr      error
	vtDecCallback uintptr
	vtDecoders    sync.Map // refcon -> *vtDec
	vtDecNext     atomic.Uintptr
)

func loadVTDec() (*vtDecAPI, error) {
	vtDecOnce.Do(func() {
		base, err := loadVT()
		if err != nil {
			vtDecErr = err
			return
		}
		a := &vtDecAPI{vtAPI: base, attachKeys: map[string]uintptr{}}
		libs := map[string]uintptr{}
		for _, n := range []string{"CoreVideo", "CoreMedia", "VideoToolbox"} {
			h, err := openLib(vtFrameworks + n + ".framework/" + n)
			if err != nil {
				vtDecErr = err
				return
			}
			libs[n] = h
		}
		fn := func(fptr any, lib, name string) {
			if err != nil {
				return
			}
			var p uintptr
			if p, err = sym(libs[lib], name); err == nil {
				purego.RegisterFunc(fptr, p)
			}
		}
		fn(&a.h264Desc, "CoreMedia", "CMVideoFormatDescriptionCreateFromH264ParameterSets")
		fn(&a.hevcDesc, "CoreMedia", "CMVideoFormatDescriptionCreateFromHEVCParameterSets")
		fn(&a.create, "VideoToolbox", "VTDecompressionSessionCreate")
		fn(&a.decode, "VideoToolbox", "VTDecompressionSessionDecodeFrame")
		fn(&a.finish, "VideoToolbox", "VTDecompressionSessionFinishDelayedFrames")
		fn(&a.wait, "VideoToolbox", "VTDecompressionSessionWaitForAsynchronousFrames")
		fn(&a.invalidate, "VideoToolbox", "VTDecompressionSessionInvalidate")
		fn(&a.canAccept, "VideoToolbox", "VTDecompressionSessionCanAcceptFormatDescription")
		fn(&a.blockNew, "CoreMedia", "CMBlockBufferCreateWithMemoryBlock")
		fn(&a.blockFill, "CoreMedia", "CMBlockBufferReplaceDataBytes")
		fn(&a.sampleNew, "CoreMedia", "CMSampleBufferCreateReady")
		fn(&a.width, "CoreVideo", "CVPixelBufferGetWidth")
		fn(&a.height, "CoreVideo", "CVPixelBufferGetHeight")
		fn(&a.attachment, "CoreVideo", "CVBufferGetAttachment")
		if err != nil {
			vtDecErr = fmt.Errorf("%w (%v)", ErrDecodeUnavailable, err)
			return
		}
		for _, k := range []string{"kCVImageBufferColorPrimariesKey", "kCVImageBufferTransferFunctionKey", "kCVImageBufferYCbCrMatrixKey"} {
			p, err := sym(libs["CoreVideo"], k)
			if err != nil {
				vtDecErr = fmt.Errorf("%w (%v)", ErrDecodeUnavailable, err)
				return
			}
			a.attachKeys[k] = *(*uintptr)(cptr(p))
		}
		vtDecLoaded = a
		vtDecCallback = purego.NewCallback(vtDecOutput)
	})
	return vtDecLoaded, vtDecErr
}

// vtDec decodes one stream.
type vtDec struct {
	a       *vtDecAPI
	cfg     DecodeConfig
	picture func(*DecodedPicture) error
	refcon  uintptr

	session, desc uintptr
	params        map[int][]byte // the parameter sets by NAL unit type
	info          spsInfo

	mu    sync.Mutex
	ready []*DecodedPicture // copied out by the callback, in display order
	err   error             // the first error the callback met
}

func openVTDecoder(cfg DecodeConfig, picture func(*DecodedPicture) error) (Decoder, error) {
	if cfg.Codec != DecodeH264 && cfg.Codec != DecodeHEVC {
		return nil, fmt.Errorf("%w: VideoToolbox does not decode %s", ErrDecodeUnavailable, cfg.Codec)
	}
	a, err := loadVTDec()
	if err != nil {
		return nil, err
	}
	d := &vtDec{a: a, cfg: cfg, picture: picture, params: map[int][]byte{}, refcon: vtDecNext.Add(1)}
	vtDecoders.Store(d.refcon, d)
	return d, nil
}

func (d *vtDec) hevc() bool { return d.cfg.Codec == DecodeHEVC }

// nalType is a NAL unit's type in the stream's codec.
func (d *vtDec) nalType(n []byte) int {
	if d.hevc() {
		return int(n[0] >> 1 & 0x3f)
	}
	return int(n[0] & 0x1f)
}

func (d *vtDec) Decode(au []byte, pts int64) error {
	if d.err != nil {
		return d.err
	}
	var sample []byte
	changed := false
	for _, n := range annexBNALs(au) {
		for len(n) > 0 && n[len(n)-1] == 0 {
			n = n[:len(n)-1]
		}
		if len(n) < 2 {
			continue
		}
		switch t := d.nalType(n); {
		case d.hevc() && (t == 32 || t == 33 || t == 34), !d.hevc() && (t == 7 || t == 8):
			if !slices.Equal(d.params[t], n) {
				d.params[t] = append([]byte(nil), n...)
				changed = true
			}
		case d.hevc() && t == 35, !d.hevc() && t == 9: // access unit delimiters
		case !d.hevc() && (t == 14 || t == 15 || t == 20):
			// MVC's prefix, subset SPS and dependent view slices (a 3D
			// disc's base view carries prefixes): VideoToolbox refuses them.
		default:
			sample = append(sample, byte(len(n)>>24), byte(len(n)>>16), byte(len(n)>>8), byte(len(n)))
			sample = append(sample, n...)
		}
	}
	if changed {
		if err := d.newFormat(); err != nil {
			d.err = err
			return err
		}
	}
	if d.session == 0 || len(sample) == 0 {
		return nil // nothing to decode before the first parameter sets
	}
	var block, sbuf uintptr
	if st := d.a.blockNew(0, 0, len(sample), 0, 0, 0, len(sample), cmBlockBufferAssureMemory, &block); st != 0 {
		return d.fail("CMBlockBufferCreateWithMemoryBlock", st)
	}
	defer d.a.release(block)
	if st := d.a.blockFill(unsafe.Pointer(&sample[0]), block, 0, len(sample)); st != 0 {
		return d.fail("CMBlockBufferReplaceDataBytes", st)
	}
	size := len(sample)
	if st := d.a.sampleNew(0, block, d.desc, 1, 0, 0, 1, unsafe.Pointer(&size), &sbuf); st != 0 {
		return d.fail("CMSampleBufferCreateReady", st)
	}
	defer d.a.release(sbuf)
	var info uint32
	st := d.a.decode(d.session, sbuf, vtDecodeTemporalProcessing, uintptr(pts), &info) //nolint:gosec // a timestamp, passed through
	if st != 0 {
		return d.fail("VTDecompressionSessionDecodeFrame", st)
	}
	return d.hand()
}

func (d *vtDec) fail(what string, st int32) error {
	d.err = fmt.Errorf("videotoolbox: %s: %d", what, st)
	return d.err
}

// newFormat makes the format description from the latest parameter sets,
// and a session for it unless the one there takes it.
func (d *vtDec) newFormat() error {
	types := []int{7, 8}
	sps := 7
	if d.hevc() {
		types, sps = []int{32, 33, 34}, 33
	}
	var ptrs []unsafe.Pointer
	var sizes []int
	for _, t := range types {
		p := d.params[t]
		if p == nil {
			return nil // wait for the rest
		}
		ptrs, sizes = append(ptrs, unsafe.Pointer(&p[0])), append(sizes, len(p))
	}
	var err error
	if d.hevc() {
		d.info, err = hevcSPS(d.params[sps])
	} else {
		d.info, err = h264SPS(d.params[sps])
	}
	if err != nil {
		return err
	}
	var desc uintptr
	var st int32
	if d.hevc() {
		st = d.a.hevcDesc(0, len(ptrs), unsafe.Pointer(&ptrs[0]), unsafe.Pointer(&sizes[0]), 4, 0, &desc)
	} else {
		st = d.a.h264Desc(0, len(ptrs), unsafe.Pointer(&ptrs[0]), unsafe.Pointer(&sizes[0]), 4, &desc)
	}
	if st != 0 {
		return fmt.Errorf("%w: the format description: %d", ErrDecodeUnavailable, st)
	}
	if d.session != 0 && d.a.canAccept(d.session, desc) {
		d.a.release(d.desc)
		d.desc = desc
		return nil
	}
	if d.session != 0 {
		_ = d.a.finish(d.session)
		_ = d.a.wait(d.session)
		d.a.invalidate(d.session)
		d.a.release(d.session)
		d.session = 0
	}
	if d.desc != 0 {
		d.a.release(d.desc)
	}
	d.desc = desc
	format := int32(cvPixelFormat420v)
	if d.info.depth > 8 {
		format = cvPixelFormatx420
	}
	attrs := d.a.dict()
	defer d.a.release(attrs)
	n := d.a.number32(format)
	d.a.dictSet(attrs, d.a.keys["kCVPixelBufferPixelFormatTypeKey"], n)
	d.a.release(n)
	cb := [2]uintptr{vtDecCallback, d.refcon}
	var session uintptr
	if st := d.a.create(0, desc, 0, attrs, unsafe.Pointer(&cb), &session); st != 0 {
		return fmt.Errorf("%w: VTDecompressionSessionCreate: %d", ErrDecodeUnavailable, st)
	}
	d.session = session
	return nil
}

// vtDecOutput is VideoToolbox's callback with a decoded picture: it copies
// the picture out.
func vtDecOutput(refcon, frameRefcon, status, infoFlags, image uintptr) {
	v, ok := vtDecoders.Load(refcon)
	if !ok {
		return
	}
	d := v.(*vtDec) //nolint:forcetypeassert // only vtDecs are stored
	d.mu.Lock()
	defer d.mu.Unlock()
	if int32(status) != 0 { //nolint:gosec // an OSStatus in a register
		if d.err == nil {
			d.err = fmt.Errorf("videotoolbox: decoding: %d", int32(status)) //nolint:gosec // an OSStatus
		}
		return
	}
	if image == 0 || uint32(infoFlags)&vtDecodeInfoFrameDropped != 0 { //nolint:gosec // flags
		return
	}
	p, err := d.copyOut(image, int64(frameRefcon)) //nolint:gosec // the timestamp given
	if err != nil {
		if d.err == nil {
			d.err = err
		}
		return
	}
	d.ready = append(d.ready, p)
}

func (d *vtDec) copyOut(pb uintptr, pts int64) (*DecodedPicture, error) {
	a := d.a
	if st := a.lock(pb, 1); st != 0 { // read only
		return nil, fmt.Errorf("videotoolbox: CVPixelBufferLockBaseAddress: %d", st)
	}
	defer a.unlock(pb, 1)
	w, h := d.info.width(), d.info.height()
	x0, y0 := 0, 0
	if a.width(pb) != w || a.height(pb) != h {
		// The coded picture: the displayed part is cropped out here.
		x0, y0 = d.info.cropL, d.info.cropT
		if a.width(pb) < x0+w || a.height(pb) < y0+h {
			return nil, fmt.Errorf("videotoolbox: a %dx%d picture for a %dx%d stream", a.width(pb), a.height(pb), w, h)
		}
	}
	bps := 1
	if d.info.depth > 8 {
		bps = 2
	}
	row := w * bps
	buf := make([]byte, row*h*3/2)
	for plane, rows := range []int{h, h / 2} {
		base, stride := a.planeBase(pb, plane), a.planeStride(pb, plane)
		ys, xs := y0, x0*bps
		if plane == 1 {
			ys, xs = y0/2, x0/2*2*bps
		}
		dst := buf
		if plane == 1 {
			dst = buf[row*h:]
		}
		for y := range rows {
			src := cbytes(base+uintptr((ys+y)*stride+xs), row) //nolint:gosec // within the plane
			copy(dst[y*row:], src)
		}
	}
	return &DecodedPicture{Width: w, Height: h, Depth: d.info.depth, Y: buf[:row*h], UV: buf[row*h:], Pitch: row,
		PTS: pts, Color: d.color(pb)}, nil
}

// color is the stream's colour: the SPS's, else the picture's colour
// attachments' (Apple's software decoders attach none).
func (d *vtDec) color(pb uintptr) ColorInfo {
	if c := d.info.color; c.Primaries != 2 || c.Transfer != 2 || c.Matrix != 2 {
		return c
	}
	a := d.a
	c := ColorInfo{Primaries: 2, Transfer: 2, Matrix: 2, FullRange: d.info.color.FullRange}
	get := func(key string) uintptr { return a.attachment(pb, a.attachKeys[key], nil) }
	k := a.keys
	switch get("kCVImageBufferColorPrimariesKey") {
	case k["kCVImageBufferColorPrimaries_ITU_R_709_2"]:
		c.Primaries = 1
	case k["kCVImageBufferColorPrimaries_ITU_R_2020"]:
		c.Primaries = 9
	case k["kCVImageBufferColorPrimaries_P3_D65"]:
		c.Primaries = 12
	}
	switch get("kCVImageBufferTransferFunctionKey") {
	case k["kCVImageBufferTransferFunction_ITU_R_709_2"]:
		c.Transfer = 1
	case k["kCVImageBufferTransferFunction_SMPTE_ST_2084_PQ"]:
		c.Transfer = 16
	case k["kCVImageBufferTransferFunction_ITU_R_2100_HLG"]:
		c.Transfer = 18
	}
	switch get("kCVImageBufferYCbCrMatrixKey") {
	case k["kCVImageBufferYCbCrMatrix_ITU_R_709_2"]:
		c.Matrix = 1
	case k["kCVImageBufferYCbCrMatrix_ITU_R_2020"]:
		c.Matrix = 9
	}
	return c
}

// hand gives on the pictures the callback has copied out, in the order
// VideoToolbox gave them: display order, with temporal processing.
func (d *vtDec) hand() error {
	d.mu.Lock()
	ready, err := d.ready, d.err
	d.ready = nil
	d.mu.Unlock()
	if err != nil {
		return err
	}
	for _, p := range ready {
		if err := d.picture(p); err != nil {
			d.err = err
			return err
		}
	}
	return nil
}

func (d *vtDec) Flush() error {
	if d.err != nil {
		return d.err
	}
	if d.session != 0 {
		if st := d.a.finish(d.session); st != 0 {
			return d.fail("VTDecompressionSessionFinishDelayedFrames", st)
		}
		_ = d.a.wait(d.session)
	}
	return d.hand()
}

func (d *vtDec) Close() error {
	if d.session != 0 {
		d.a.invalidate(d.session)
		d.a.release(d.session)
		d.session = 0
	}
	if d.desc != 0 {
		d.a.release(d.desc)
		d.desc = 0
	}
	vtDecoders.Delete(d.refcon)
	return nil
}
