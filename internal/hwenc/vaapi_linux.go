//go:build linux && (amd64 || arm64)

package hwenc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// VAAPI encoding through libva: the driver (Intel's iHD, Mesa) builds the
// parameter sets and slice headers from the parameter buffers, and this
// side decides the GOP — an IDR every GOP frames, two non-reference
// B-frames between references, as ffmpeg's VAAPI encoders do by default —
// and the reference lists.

type vaFuncs struct {
	getDisplayDRM, initialize, terminate, setInfoCallback, errorStr       uintptr
	maxNumEntrypoints, queryConfigEntrypoints, getConfigAttributes        uintptr
	createConfig, destroyConfig, createSurfaces, destroySurfaces          uintptr
	createContext, destroyContext, createBuffer, destroyBuffer, mapBuffer uintptr
	unmapBuffer, deriveImage, destroyImage, beginPicture, renderPicture   uintptr
	endPicture, syncSurface                                               uintptr
}

func loadVA() (*vaFuncs, error) {
	va, err := openLib("libva.so.2")
	if err != nil {
		return nil, err
	}
	drm, err := openLib("libva-drm.so.2")
	if err != nil {
		return nil, err
	}
	f := &vaFuncs{}
	for _, s := range []struct {
		lib  uintptr
		name string
		p    *uintptr
	}{
		{drm, "vaGetDisplayDRM", &f.getDisplayDRM},
		{va, "vaInitialize", &f.initialize},
		{va, "vaTerminate", &f.terminate},
		{va, "vaSetInfoCallback", &f.setInfoCallback},
		{va, "vaErrorStr", &f.errorStr},
		{va, "vaMaxNumEntrypoints", &f.maxNumEntrypoints},
		{va, "vaQueryConfigEntrypoints", &f.queryConfigEntrypoints},
		{va, "vaGetConfigAttributes", &f.getConfigAttributes},
		{va, "vaCreateConfig", &f.createConfig},
		{va, "vaDestroyConfig", &f.destroyConfig},
		{va, "vaCreateSurfaces", &f.createSurfaces},
		{va, "vaDestroySurfaces", &f.destroySurfaces},
		{va, "vaCreateContext", &f.createContext},
		{va, "vaDestroyContext", &f.destroyContext},
		{va, "vaCreateBuffer", &f.createBuffer},
		{va, "vaDestroyBuffer", &f.destroyBuffer},
		{va, "vaMapBuffer", &f.mapBuffer},
		{va, "vaUnmapBuffer", &f.unmapBuffer},
		{va, "vaDeriveImage", &f.deriveImage},
		{va, "vaDestroyImage", &f.destroyImage},
		{va, "vaBeginPicture", &f.beginPicture},
		{va, "vaRenderPicture", &f.renderPicture},
		{va, "vaEndPicture", &f.endPicture},
		{va, "vaSyncSurface", &f.syncSurface},
	} {
		if *s.p, err = sym(s.lib, s.name); err != nil {
			return nil, err
		}
	}
	return f, nil
}

const (
	vaInFlight = 4 // pictures submitted before the oldest is waited for
	vaBFrames  = 2 // B-frames between references, as ffmpeg's -bf default
)

type picType int

const (
	picI picType = iota
	picP
	picB
)

// vaPic is a picture on its way through the encoder.
type vaPic struct {
	in, rec, coded int // pool indexes
	disp           int // display index since the IDR
	typ            picType
	idr            bool
	frameNum       int
}

type vaapi struct {
	f                 *vaFuncs
	w                 io.Writer
	cfg               Config
	fd                *os.File
	dpy               uintptr
	conf              uint32
	ctx               uint32
	ins               []uint32 // input surfaces
	recs              []uint32 // reconstructed (reference) surfaces
	codeds            []uint32
	freeIn, freeCoded []int
	recUse            []int // per reconstructed surface: references held (DPB, in flight)

	profile       uint32
	bFrames       int
	maxRefs       int
	packed        bool // the driver takes packed parameter sets
	packedSlices  bool // and slice headers (HEVC: Intel's driver writes none)
	gpb           bool // P pictures must be sent as B slices with list 1 = list 0 (HEVC low power)
	hevc          hevcCaps
	qpI, qpP, qpB int

	n            int // pictures received
	inGOP        int // their position in the GOP
	idrDisp      int // display index of the last IDR
	idrID        int
	nextFrameNum int
	held         []vaPic // B candidates waiting for the next reference
	dpb          []vaPic // references, oldest first
	inflight     []vaPic
	err          error
}

// hevcCaps are the driver's HEVC block sizes and coding tools.
type hevcCaps struct {
	log2MinCB, log2MaxCTB, log2MinTB, log2MaxTB int
	thdInter, thdIntra                          int
	amp, sao, tmvp, strongIntra, signHiding     bool
	transformSkip, cuQPDelta                    bool
}

func (e *vaapi) check(what string, st uintptr) error {
	if st == vaStatusSuccess {
		return nil
	}
	return fmt.Errorf("vaapi: %s: %s (%d)", what, cstring(call(e.f.errorStr, st)), st)
}

func openVAAPI(cfg Config, w io.Writer) (Encoder, error) {
	f, err := loadVA()
	if err != nil {
		return nil, err
	}
	devices := []string{cfg.Device}
	if cfg.Device == "" {
		devices, _ = filepath.Glob("/dev/dri/renderD*")
	}
	var firstErr error
	for _, dev := range devices {
		e := &vaapi{f: f, w: w, cfg: cfg}
		err := e.open(dev)
		if err == nil {
			return e, nil
		}
		e.release()
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		firstErr = fmt.Errorf("%w: no render node", ErrUnavailable)
	}
	return nil, firstErr
}

func (e *vaapi) open(dev string) error {
	fd, err := os.OpenFile(dev, os.O_RDWR, 0) //nolint:gosec // a device node the user chose
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	e.fd = fd
	e.dpy = call(e.f.getDisplayDRM, fd.Fd())
	if e.dpy == 0 {
		return fmt.Errorf("%w: %s: no VA display", ErrUnavailable, dev)
	}
	call(e.f.setInfoCallback, e.dpy, 0, 0) // libva's info lines on stderr
	ver := newStruct(8)
	if st := call(e.f.initialize, e.dpy, ver.ptr(), ver.ptr()+4); st != vaStatusSuccess {
		e.dpy = 0 // nothing to terminate
		return fmt.Errorf("%w: %s: %v", ErrUnavailable, dev, e.check("initialising", st))
	}

	e.profile = vaProfileH264High
	switch e.cfg.Codec {
	case HEVC:
		e.profile = vaProfileHEVCMain
	case AV1:
		e.profile = vaProfileAV1Profile0
	}
	entry, err := e.entrypoint()
	if err != nil {
		return fmt.Errorf("%s: %w", dev, err)
	}

	attrs := newStruct(6 * vaSizeConfigAttrib)
	for i, t := range []uint32{vaConfigAttribRTFormat, vaConfigAttribRateControl, vaConfigAttribEncMaxRefFrames,
		vaConfigAttribPredictionDirection, vaConfigAttribEncHEVCFeatures, vaConfigAttribEncPackedHeaders} {
		attrs.u32(i*vaSizeConfigAttrib, t)
	}
	if err := e.check("querying attributes", call(e.f.getConfigAttributes, e.dpy, uintptr(e.profile), entry, attrs.ptr(), 6)); err != nil {
		return err
	}
	attr := func(i int) uint32 { return attrs.getU32(i*vaSizeConfigAttrib + 4) }
	if v := attr(0); v == vaAttribNotSupported || v&vaRTFormatYUV420 == 0 {
		return fmt.Errorf("%w: %s: no 4:2:0 encoding", ErrUnavailable, dev)
	}
	if v := attr(1); v == vaAttribNotSupported || v&vaRCCQP == 0 {
		return fmt.Errorf("%w: %s: no constant-QP encoding", ErrUnavailable, dev)
	}
	e.maxRefs, e.bFrames = 1, 0
	if v := attr(2); v != vaAttribNotSupported && v>>16&0xffff > 0 && v&0xffff > 0 {
		e.maxRefs, e.bFrames = 2, vaBFrames
	}
	if v := attr(3); v != vaAttribNotSupported && v&vaPredictionBiNotEmpty != 0 {
		e.gpb = true
	}
	if e.cfg.Codec == AV1 {
		e.maxRefs, e.bFrames, e.gpb = 1, 0, false // I and P pictures only; see vaapi_av1_linux.go
	}
	if e.cfg.Codec == HEVC {
		if err := e.hevcCapabilities(entry, attr(4)); err != nil {
			return err
		}
	}

	// Drivers that take packed headers want the parameter sets that way;
	// the slice headers they write themselves.
	nattr := 2
	packedFlags := uint32(vaPackedSeqFlag)
	if v := attr(5); v != vaAttribNotSupported && v&vaPackedSeqFlag != 0 {
		e.packed = true
		nattr = 3
		if e.cfg.Codec == HEVC && v&vaPackedSliceFlag != 0 {
			e.packedSlices = true
			packedFlags |= vaPackedSliceFlag
		}
	}
	if e.cfg.Codec == AV1 {
		// AV1 is only possible with both headers packed: the driver writes
		// neither.
		if v := attr(5); v == vaAttribNotSupported || v&(vaPackedSeqFlag|vaPackedPicFlag) != vaPackedSeqFlag|vaPackedPicFlag {
			return fmt.Errorf("%w: %s: the driver takes no packed AV1 headers", ErrUnavailable, dev)
		}
		packedFlags = vaPackedSeqFlag | vaPackedPicFlag
	}
	create := newStruct(3 * vaSizeConfigAttrib)
	create.u32(0, vaConfigAttribRTFormat)
	create.u32(4, vaRTFormatYUV420)
	create.u32(vaSizeConfigAttrib, vaConfigAttribRateControl)
	create.u32(vaSizeConfigAttrib+4, vaRCCQP)
	create.u32(2*vaSizeConfigAttrib, vaConfigAttribEncPackedHeaders)
	create.u32(2*vaSizeConfigAttrib+4, packedFlags)
	id := newStruct(4)
	if err := e.check("creating the configuration", call(e.f.createConfig, e.dpy, uintptr(e.profile), entry, create.ptr(), uintptr(nattr), id.ptr())); err != nil {
		return err
	}
	e.conf = id.getU32(0)

	w, h := uint32(e.cfg.Width), uint32(e.cfg.Height) //nolint:gosec // frame size
	surfaces := func(n int, w, h uint32) ([]uint32, error) {
		s := newStruct(4 * n)
		if err := e.check("creating surfaces", call(e.f.createSurfaces, e.dpy, vaRTFormatYUV420, uintptr(w), uintptr(h), s.ptr(), uintptr(n), 0, 0)); err != nil {
			return nil, err
		}
		ids := make([]uint32, n)
		for i := range ids {
			ids[i] = s.getU32(4 * i)
		}
		return ids, nil
	}
	if e.ins, err = surfaces(e.bFrames+vaInFlight+1, w, h); err != nil {
		return err
	}
	// AV1's reconstructed pictures cover whole superblocks, as ffmpeg
	// allocates them for Intel's driver.
	rw, rh := w, h
	if e.cfg.Codec == AV1 {
		rw, rh = (w+av1SB-1)/av1SB*av1SB, (h+av1SB-1)/av1SB*av1SB
	}
	if e.recs, err = surfaces(e.maxRefs+vaInFlight+1, rw, rh); err != nil {
		return err
	}
	all := append(append([]uint32(nil), e.ins...), e.recs...)
	targets := newStruct(4 * len(all))
	for i, s := range all {
		targets.u32(4*i, s)
	}
	if err := e.check("creating the context", call(e.f.createContext, e.dpy, uintptr(e.conf), uintptr(w), uintptr(h), vaProgressive,
		targets.ptr(), uintptr(len(all)), id.ptr())); err != nil {
		return err
	}
	e.ctx = id.getU32(0)
	for range vaInFlight + 1 {
		size := uintptr(w*h*3/2 + 1<<16)
		if err := e.check("creating a coded buffer", call(e.f.createBuffer, e.dpy, uintptr(e.ctx), vaEncCodedBufferType, size, 1, 0, id.ptr())); err != nil {
			return err
		}
		e.codeds = append(e.codeds, id.getU32(0))
	}
	for i := range e.ins {
		e.freeIn = append(e.freeIn, i)
	}
	for i := range e.codeds {
		e.freeCoded = append(e.freeCoded, i)
	}
	e.recUse = make([]int, len(e.recs))
	e.qpI, e.qpP, e.qpB = vaapiQPS(e.cfg.QP)
	return nil
}

// entrypoint picks the encoding entrypoint: the full one when the driver has
// it, else low power (the only one on recent Intel GPUs).
func (e *vaapi) entrypoint() (uintptr, error) {
	n := int(call(e.f.maxNumEntrypoints, e.dpy))
	list := newStruct(4 * max(n, 1))
	num := newStruct(4)
	if err := e.check("listing entrypoints", call(e.f.queryConfigEntrypoints, e.dpy, uintptr(e.profile), list.ptr(), num.ptr())); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	found := map[uint32]bool{}
	for i := 0; i < int(num.getU32(0)) && i < n; i++ {
		found[list.getU32(4*i)] = true
	}
	switch {
	case found[vaEntrypointEncSlice]:
		return vaEntrypointEncSlice, nil
	case found[vaEntrypointEncSliceLP]:
		return vaEntrypointEncSliceLP, nil
	}
	codec := "H.264"
	if e.cfg.Codec == HEVC {
		codec = "HEVC"
	}
	return 0, fmt.Errorf("%w: the driver cannot encode %s", ErrUnavailable, codec)
}

// hevcCapabilities reads the block sizes and coding tools the driver takes,
// with ffmpeg's defaults for drivers that do not say.
func (e *vaapi) hevcCapabilities(entry uintptr, features uint32) error {
	c := hevcCaps{log2MinCB: 4, log2MaxCTB: 5, log2MinTB: 2, log2MaxTB: 5, thdInter: 3, thdIntra: 3,
		amp: true, sao: true, tmvp: true, strongIntra: true, signHiding: true}
	a := newStruct(vaSizeConfigAttrib)
	a.u32(0, vaConfigAttribEncHEVCBlockSizes)
	if err := e.check("querying HEVC block sizes", call(e.f.getConfigAttributes, e.dpy, uintptr(e.profile), entry, a.ptr(), 1)); err != nil {
		return err
	}
	if v := a.getU32(4); v != vaAttribNotSupported {
		bits := func(b int) int { return int(v >> b & 3) }
		c.log2MaxCTB = 3 + bits(vaHEVCBlkMaxCTBBit)
		c.log2MinCB = 3 + bits(vaHEVCBlkMinCBBit)
		c.log2MaxTB = 2 + bits(vaHEVCBlkMaxTBBit)
		c.log2MinTB = 2 + bits(vaHEVCBlkMinTBBit)
		c.thdInter = bits(vaHEVCBlkMaxTHDInterBit)
		c.thdIntra = bits(vaHEVCBlkMaxTHDIntraBit)
	}
	if features != vaAttribNotSupported {
		// Use a tool the driver supports, and one it requires.
		on := func(b int) bool { return features>>b&3 != vaFeatureNotSupported }
		c.amp, c.sao, c.tmvp = on(vaHEVCFeatAMPBit), on(vaHEVCFeatSAOBit), on(vaHEVCFeatTMVPBit)
		c.strongIntra, c.signHiding = on(vaHEVCFeatStrongIntraBit), on(vaHEVCFeatSignHidingBit)
		c.transformSkip = on(vaHEVCFeatTransformSkipBit)
		c.cuQPDelta = false
	}
	if e.cfg.Width%(1<<c.log2MinCB) != 0 || e.cfg.Height%(1<<c.log2MinCB) != 0 {
		return fmt.Errorf("%w: %dx%d is not a multiple of the driver's %d-pixel HEVC coding blocks",
			ErrUnavailable, e.cfg.Width, e.cfg.Height, 1<<c.log2MinCB)
	}
	e.hevc = c
	return nil
}

func (e *vaapi) Encode(fill func(*Picture)) error {
	if e.err != nil {
		return e.err
	}
	for len(e.freeIn) == 0 && len(e.inflight) > 0 {
		if err := e.finish(); err != nil {
			return err
		}
	}
	if len(e.freeIn) == 0 {
		return errors.New("vaapi: no free surface")
	}
	in := e.freeIn[0]
	e.freeIn = e.freeIn[1:]
	if err := e.upload(e.ins[in], fill); err != nil {
		e.err = err
		return err
	}
	if e.inGOP == e.cfg.GOP {
		e.inGOP = 0
	}
	p := vaPic{in: in, disp: e.n}
	e.n++
	e.inGOP++
	switch {
	case e.inGOP == 1:
		p.typ, p.idr = picI, true
		e.err = e.encode(p)
	case len(e.held) == e.bFrames || e.inGOP == e.cfg.GOP:
		// A reference: it goes first, then the B-frames before it.
		p.typ = picP
		e.err = e.flushHeld(p)
	default:
		e.held = append(e.held, p)
	}
	return e.err
}

// flushHeld encodes the reference p, then the held B-frames.
func (e *vaapi) flushHeld(p vaPic) error {
	if err := e.encode(p); err != nil {
		return err
	}
	for _, b := range e.held {
		b.typ = picB
		if err := e.encode(b); err != nil {
			return err
		}
	}
	e.held = e.held[:0]
	return nil
}

// upload copies a picture into a surface through a mapping of it.
func (e *vaapi) upload(surface uint32, fill func(*Picture)) error {
	img := newStruct(vaSizeImage)
	if err := e.check("mapping a surface", call(e.f.deriveImage, e.dpy, uintptr(surface), img.ptr())); err != nil {
		return err
	}
	defer call(e.f.destroyImage, e.dpy, uintptr(img.getU32(vaImageID)))
	if img.getU32(vaImageFourcc) != vaFourccNV12 {
		return errors.New("vaapi: the surface is not NV12")
	}
	pitch := int(img.getU32(vaImagePitches))
	if int(img.getU32(vaImagePitches+4)) != pitch {
		return errors.New("vaapi: the surface's planes have different pitches")
	}
	buf := uintptr(img.getU32(vaImageBuf))
	p := newStruct(8)
	if err := e.check("mapping an image", call(e.f.mapBuffer, e.dpy, buf, p.ptr())); err != nil {
		return err
	}
	base := p.getPtr(0)
	h := e.cfg.Height
	fill(&Picture{
		Y:     cbytes(base+uintptr(img.getU32(vaImageOffsets)), pitch*h),
		UV:    cbytes(base+uintptr(img.getU32(vaImageOffsets+4)), pitch*h/2),
		Pitch: pitch,
	})
	return e.check("unmapping an image", call(e.f.unmapBuffer, e.dpy, buf))
}

// encode submits one picture, in decoding order.
func (e *vaapi) encode(p vaPic) error {
	for (len(e.freeCoded) == 0 || e.freeRec() < 0) && len(e.inflight) > 0 {
		if err := e.finish(); err != nil {
			return err
		}
	}
	p.rec = e.freeRec()
	if p.rec < 0 || len(e.freeCoded) == 0 {
		return errors.New("vaapi: no free surface")
	}
	p.coded = e.freeCoded[0]
	e.freeCoded = e.freeCoded[1:]
	e.recUse[p.rec]++

	if p.idr {
		for _, r := range e.dpb {
			e.recUse[r.rec]--
		}
		e.dpb = e.dpb[:0]
		e.idrDisp = p.disp
		e.nextFrameNum = 0
		e.idrID++
	}
	p.disp -= e.idrDisp
	p.frameNum = e.nextFrameNum
	ref := p.typ != picB
	if ref {
		e.nextFrameNum = (p.frameNum + 1) % (1 << e.log2MaxFrameNum())
	}
	// P refers to the newest reference; a B-frame sits between the two.
	var l0, l1 []vaPic
	switch p.typ {
	case picP:
		l0 = e.dpb[len(e.dpb)-1:]
		if e.gpb {
			l1 = l0
		}
	case picB:
		l0, l1 = e.dpb[len(e.dpb)-2:len(e.dpb)-1], e.dpb[len(e.dpb)-1:]
	}

	var bufs []uint32
	add := func(typ uintptr, s cstruct) error {
		id := newStruct(4)
		err := e.check("creating a parameter buffer", call(e.f.createBuffer, e.dpy, uintptr(e.ctx), typ, uintptr(len(s)), 1, s.ptr(), id.ptr()))
		if err == nil {
			bufs = append(bufs, id.getU32(0))
		}
		return err
	}
	var err error
	switch e.cfg.Codec {
	case HEVC:
		err = e.hevcParams(p, l0, l1, add)
	case AV1:
		err = e.av1Params(p, l0, add)
	default:
		err = e.h264Params(p, l0, l1, add)
	}
	if err == nil {
		err = e.submit(p, bufs)
	}
	for _, b := range bufs {
		call(e.f.destroyBuffer, e.dpy, uintptr(b))
	}
	if err != nil {
		return err
	}

	if ref {
		e.dpb = append(e.dpb, p)
		e.recUse[p.rec]++
		if len(e.dpb) > e.maxRefs {
			e.recUse[e.dpb[0].rec]--
			e.dpb = append(e.dpb[:0], e.dpb[1:]...)
		}
	}
	e.inflight = append(e.inflight, p)
	if len(e.inflight) > vaInFlight {
		return e.finish()
	}
	return nil
}

func (e *vaapi) freeRec() int {
	for i, n := range e.recUse {
		if n == 0 {
			return i
		}
	}
	return -1
}

func (e *vaapi) submit(p vaPic, bufs []uint32) error {
	if err := e.check("starting a picture", call(e.f.beginPicture, e.dpy, uintptr(e.ctx), uintptr(e.ins[p.in]))); err != nil {
		return err
	}
	list := newStruct(4 * len(bufs))
	for i, b := range bufs {
		list.u32(4*i, b)
	}
	if err := e.check("sending parameters", call(e.f.renderPicture, e.dpy, uintptr(e.ctx), list.ptr(), uintptr(len(bufs)))); err != nil {
		return err
	}
	return e.check("encoding", call(e.f.endPicture, e.dpy, uintptr(e.ctx)))
}

// finish waits for the oldest submitted picture and writes its bitstream.
func (e *vaapi) finish() error {
	p := e.inflight[0]
	e.inflight = e.inflight[1:]
	if err := e.check("waiting for a picture", call(e.f.syncSurface, e.dpy, uintptr(e.ins[p.in]))); err != nil {
		e.err = err
		return err
	}
	coded := uintptr(e.codeds[p.coded])
	seg := newStruct(8)
	if err := e.check("mapping the bitstream", call(e.f.mapBuffer, e.dpy, coded, seg.ptr())); err != nil {
		e.err = err
		return err
	}
	var werr error
	if e.cfg.Codec == AV1 {
		// Each picture is a temporal unit, which starts with a delimiter.
		_, werr = e.w.Write(av1TD)
	}
	for s := seg.getPtr(0); s != 0; {
		h := cbytes(s, 32)
		hdr := cstruct(h)
		if hdr.getU32(vaSegStatus)&vaCodedSliceOverflow != 0 && werr == nil {
			werr = errors.New("vaapi: a picture overflowed its bitstream buffer")
		}
		if werr == nil {
			_, werr = e.w.Write(cbytes(hdr.getPtr(vaSegBuf), int(hdr.getU32(vaSegSize))))
		}
		s = hdr.getPtr(vaSegNext)
	}
	if err := e.check("unmapping the bitstream", call(e.f.unmapBuffer, e.dpy, coded)); err != nil && werr == nil {
		werr = err
	}
	e.freeIn = append(e.freeIn, p.in)
	e.freeCoded = append(e.freeCoded, p.coded)
	e.recUse[p.rec]--
	if werr != nil {
		e.err = werr
	}
	return werr
}

func (e *vaapi) Close() error {
	err := e.err
	if err == nil && len(e.held) > 0 {
		// The stream ends: the last held picture becomes the reference.
		last := e.held[len(e.held)-1]
		e.held = e.held[:len(e.held)-1]
		last.typ = picP
		err = e.flushHeld(last)
	}
	for err == nil && len(e.inflight) > 0 {
		err = e.finish()
	}
	e.release()
	return err
}

func (e *vaapi) release() {
	if e.dpy != 0 {
		for _, b := range e.codeds {
			call(e.f.destroyBuffer, e.dpy, uintptr(b))
		}
		if e.ctx != 0 {
			call(e.f.destroyContext, e.dpy, uintptr(e.ctx))
		}
		for _, s := range [][]uint32{e.ins, e.recs} {
			if len(s) > 0 {
				ids := newStruct(4 * len(s))
				for i, id := range s {
					ids.u32(4*i, id)
				}
				call(e.f.destroySurfaces, e.dpy, ids.ptr(), uintptr(len(s)))
			}
		}
		if e.conf != 0 {
			call(e.f.destroyConfig, e.dpy, uintptr(e.conf))
		}
		call(e.f.terminate, e.dpy)
		e.dpy = 0
	}
	if e.fd != nil {
		_ = e.fd.Close()
		e.fd = nil
	}
	e.codeds, e.ins, e.recs, e.ctx, e.conf = nil, nil, nil, 0, 0
	runtime.KeepAlive(e)
}

// log2MaxFrameNum is the frame_num range: a GOP's references must fit.
func (e *vaapi) log2MaxFrameNum() int {
	n := 4
	for 1<<n <= e.cfg.GOP && n < 16 {
		n++
	}
	return n
}

// log2MaxPOCLsb is the picture order count range: twice the frame count of
// a GOP (H.264 counts fields).
func (e *vaapi) log2MaxPOCLsb() int {
	n := 4
	for 1<<n <= 2*e.cfg.GOP && n < 16 {
		n++
	}
	return n
}

func b2u(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

// vaapiQPS is qps for ffmpeg's VAAPI encoders, whose defaults differ: I
// pictures at the P quantiser, B at 6/5 of it.
func vaapiQPS(qp int) (i, p, b int) {
	return qp, qp, clipQP(float64(qp) * 6 / 5)
}

func (s cstruct) u8(off int, v uint8) { s[off] = v }
