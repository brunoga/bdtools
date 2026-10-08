//go:build (linux || windows) && (amd64 || arm64)

package gpu

import (
	"errors"
	"fmt"
	"io"
	"runtime"
)

// The NVIDIA driver's encoder library and CUDA, by platform.
var (
	nvencLibs = map[string][]string{
		"linux":   {"libnvidia-encode.so.1", "libnvidia-encode.so"},
		"windows": {"nvEncodeAPI64.dll"},
	}
	cudaLibs = map[string][]string{
		"linux":   {"libcuda.so.1", "libcuda.so"},
		"windows": {"nvcuda.dll"},
	}
)

// nvBuffers is the number of input/output buffer pairs: with B-frames the
// encoder holds pictures back until the next reference arrives, each held
// picture keeps its pair, and so does each encoded one not yet read.
const nvBuffers = 12

// nvSpare is how many buffers Encode keeps free: three B-frames held back
// and the reference after them.
const nvSpare = 4

type nvPair struct {
	in, out uintptr
}

type nvenc struct {
	w       io.Writer
	cfg     Config
	fn      cstruct // the API function list
	enc     uintptr // encoder session
	cuda    uintptr // CUDA context
	cuDtor  uintptr
	pairs   []nvPair
	free    []int
	pending []int // submitted, held back by the encoder
	ready   []int // encoded or being encoded, bitstream not yet read, in order
	frame   uint64
	err     error
}

func (e *nvenc) fnp(off int) uintptr { return e.fn.getPtr(off) }

// status turns an NVENCSTATUS into an error with the driver's message.
func (e *nvenc) status(what string, st uintptr) error {
	if st == nvSuccess {
		return nil
	}
	msg := ""
	if e.enc != 0 {
		msg = cstring(call(e.fnp(nvFnGetLastErrorString), e.enc))
	}
	return fmt.Errorf("nvenc: %s: status %d %s", what, st, msg)
}

func openNVENC(cfg Config, w io.Writer) (Encoder, error) {
	encLib, err := openLib(nvencLibs[runtime.GOOS]...)
	if err != nil {
		return nil, err
	}
	cudaLib, err := openLib(cudaLibs[runtime.GOOS]...)
	if err != nil {
		return nil, err
	}
	e := &nvenc{w: w, cfg: cfg}
	if err := e.initCUDA(cudaLib); err != nil {
		return nil, err
	}
	create, err := sym(encLib, "NvEncodeAPICreateInstance")
	if err != nil {
		e.closeCUDA()
		return nil, err
	}
	e.fn = newStruct(nvSizeFunctionList)
	e.fn.u32(0, nvFunctionListVer)
	if st := call(create, e.fn.ptr()); st != nvSuccess {
		e.closeCUDA()
		return nil, fmt.Errorf("%w: NvEncodeAPICreateInstance: status %d (the driver may be too old for API 12.0)", ErrUnavailable, st)
	}
	if err := e.open(); err != nil {
		_ = e.Close()
		return nil, err
	}
	return e, nil
}

func (e *nvenc) initCUDA(lib uintptr) error {
	cuInit, err := sym(lib, "cuInit")
	if err != nil {
		return err
	}
	cuDeviceGet, err := sym(lib, "cuDeviceGet")
	if err != nil {
		return err
	}
	cuCtxCreate, err := sym(lib, "cuCtxCreate_v2")
	if err != nil {
		return err
	}
	if e.cuDtor, err = sym(lib, "cuCtxDestroy_v2"); err != nil {
		return err
	}
	if r := call(cuInit, 0); r != 0 {
		return fmt.Errorf("%w: cuInit: %d (no NVIDIA GPU?)", ErrUnavailable, r)
	}
	dev := newStruct(8)
	if r := call(cuDeviceGet, dev.ptr(), 0); r != 0 {
		return fmt.Errorf("%w: cuDeviceGet: %d", ErrUnavailable, r)
	}
	ctx := newStruct(8)
	if r := call(cuCtxCreate, ctx.ptr(), 0, uintptr(dev.getU32(0))); r != 0 {
		return fmt.Errorf("%w: cuCtxCreate: %d", ErrUnavailable, r)
	}
	e.cuda = ctx.getPtr(0)
	return nil
}

func (e *nvenc) closeCUDA() {
	if e.cuda != 0 {
		call(e.cuDtor, e.cuda)
		e.cuda = 0
	}
}

func (e *nvenc) open() error {
	os := newStruct(nvSizeOpenSessionExParams)
	os.u32(0, nvOpenSessionExParamsVer)
	os.u32(nvOSDeviceType, nvDeviceTypeCUDA)
	os.uptr(nvOSDevice, e.cuda)
	os.u32(nvOSAPIVersion, nvencAPIVersion)
	enc := newStruct(8)
	st := call(e.fnp(nvFnOpenSessionEx), os.ptr(), enc.ptr())
	if st != nvSuccess {
		return fmt.Errorf("%w: opening an encode session: status %d", ErrUnavailable, st)
	}
	e.enc = enc.getPtr(0)

	codec := nvCodecH264GUID
	switch e.cfg.Codec {
	case HEVC:
		codec = nvCodecHEVCGUID
	case AV1:
		codec = nvCodecAV1GUID
	}
	// The P4 preset tuned for quality, then constant QP as ffmpeg's -qp sets
	// it, B-frames, and a keyframe interval.
	pc := newStruct(nvSizePresetConfig)
	pc.u32(0, nvPresetConfigVer)
	pc.u32(nvPCPresetCfg, nvConfigVer)
	args := []uintptr{e.enc}
	args = append(args, guidArg(codec)...)
	args = append(args, guidArg(nvPresetP4GUID)...)
	args = append(args, nvTuningHighQuality, pc.ptr())
	if err := e.status("reading the preset", call(e.fnp(nvFnGetPresetConfigEx), args...)); err != nil {
		return err
	}
	cfg := newStruct(nvSizeConfig)
	copy(cfg, pc[nvPCPresetCfg:nvPCPresetCfg+nvSizeConfig])
	cfg.u32(0, nvConfigVer)
	cfg.u32(nvCfgGOPLength, uint32(e.cfg.GOP)) //nolint:gosec // small
	cfg.u32(nvCfgFrameIntervalP, 4)            // three B-frames
	rc := nvCfgRCParams
	cfg.u32(rc, nvRCParamsVer)
	cfg.u32(rc+nvRCRateControlMode, nvRCConstQPMode)
	i, p, b := qps(e.cfg.QP)
	if e.cfg.Codec == AV1 {
		// AV1's quantiser is a 0-255 index: the same offsets, mapped.
		i, p, b = AV1QIndex(i), AV1QIndex(p), AV1QIndex(b)
	}
	cfg.u32(rc+nvRCConstQP, uint32(p))   //nolint:gosec // 0..255
	cfg.u32(rc+nvRCConstQP+4, uint32(b)) //nolint:gosec // 0..255
	cfg.u32(rc+nvRCConstQP+8, uint32(i)) //nolint:gosec // 0..255
	cc := nvCfgCodecConfig
	switch e.cfg.Codec {
	case H264:
		cfg.u32(cc+nvH264IDRPeriod, uint32(e.cfg.GOP)) //nolint:gosec // small
	case HEVC:
		cfg.u32(cc+nvHEVCIDRPeriod, uint32(e.cfg.GOP)) //nolint:gosec // small
		if c := e.cfg.Color; c != nil {
			vui := cc + nvHEVCVUI
			cfg.u32(vui+nvVUISignalPresent, 1)
			cfg.u32(vui+nvVUIFormat, nvVUIFormatUnspecified)
			cfg.u32(vui+nvVUIFullRange, b2u(c.FullRange))
			cfg.u32(vui+nvVUIColourPresent, 1)
			cfg.u32(vui+nvVUIPrimaries, uint32(c.Primaries)) //nolint:gosec // a code point
			cfg.u32(vui+nvVUITransfer, uint32(c.Transfer))   //nolint:gosec // a code point
			cfg.u32(vui+nvVUIMatrix, uint32(c.Matrix))       //nolint:gosec // a code point
		}
		if e.cfg.BitDepth == 10 {
			cfg.bytes(nvCfgProfileGUID, nvHEVCMain10GUID)
			f := cfg.getU32(cc + nvHEVCFlags)
			f = f&^(7<<nvHEVCPixelBitDepthBit) | 2<<nvHEVCPixelBitDepthBit
			cfg.u32(cc+nvHEVCFlags, f)
		}
	case AV1:
		cfg.u32(cc+nvAV1IDRPeriod, uint32(e.cfg.GOP)) //nolint:gosec // small
		cfg.u32(cc+nvAV1Level, nvLevelAV1Auto)
		cfg.u32(cc+nvAV1Tier, nvTierAV1Main)
		// 4:2:0, 8- or 10-bit, low-overhead OBUs (not Annex B), and the
		// sequence header again on every keyframe so a player can start
		// anywhere.
		f := cfg.getU32(cc + nvAV1Flags)
		f &^= 3<<nvAV1ChromaFormatBit | 7<<nvAV1InputBitDepthBit | 7<<nvAV1PixelBitDepthBit |
			1<<nvAV1AnnexBBit | 1<<nvAV1DisableSeqHdrBit
		f |= 1<<nvAV1ChromaFormatBit | 1<<nvAV1RepeatSeqHdrBit
		d := uint32(e.cfg.BitDepth - 8) //nolint:gosec // 0 or 2
		f |= d<<nvAV1InputBitDepthBit | d<<nvAV1PixelBitDepthBit
		if c := e.cfg.Color; c != nil {
			cfg.u32(cc+nvAV1Primaries, uint32(c.Primaries)) //nolint:gosec // a code point
			cfg.u32(cc+nvAV1Transfer, uint32(c.Transfer))   //nolint:gosec // a code point
			cfg.u32(cc+nvAV1Matrix, uint32(c.Matrix))       //nolint:gosec // a code point
			cfg.u32(cc+nvAV1Range, b2u(c.FullRange))
		}
		cfg.u32(cc+nvAV1Flags, f)
	}

	ip := newStruct(nvSizeInitializeParams)
	ip.u32(0, nvInitializeParamsVer)
	ip.bytes(nvIPEncodeGUID, codec)
	ip.bytes(nvIPPresetGUID, nvPresetP4GUID)
	ip.u32(nvIPEncodeWidth, uint32(e.cfg.Width))   //nolint:gosec // frame size
	ip.u32(nvIPEncodeHeight, uint32(e.cfg.Height)) //nolint:gosec // frame size
	ip.u32(nvIPDarWidth, uint32(e.cfg.Width))      //nolint:gosec // frame size
	ip.u32(nvIPDarHeight, uint32(e.cfg.Height))    //nolint:gosec // frame size
	ip.u32(nvIPFrameRateNum, uint32(e.cfg.FPSNum)) //nolint:gosec // frame rate
	ip.u32(nvIPFrameRateDen, uint32(e.cfg.FPSDen)) //nolint:gosec // frame rate
	ip.u32(nvIPEnablePTD, 1)
	ip.uptr(nvIPEncodeConfig, cfg.ptr())
	ip.u32(nvIPMaxEncodeWidth, uint32(e.cfg.Width))   //nolint:gosec // frame size
	ip.u32(nvIPMaxEncodeHeight, uint32(e.cfg.Height)) //nolint:gosec // frame size
	ip.u32(nvIPTuningInfo, nvTuningHighQuality)
	st = call(e.fnp(nvFnInitializeEncoder), e.enc, ip.ptr())
	runtime.KeepAlive(cfg)
	if err := e.status("initialising", st); err != nil {
		return err
	}

	for i := 0; i < nvBuffers; i++ {
		cib := newStruct(nvSizeCreateInputBuffer)
		cib.u32(0, nvCreateInputBufferVer)
		cib.u32(nvCIBWidth, uint32(e.cfg.Width))   //nolint:gosec // frame size
		cib.u32(nvCIBHeight, uint32(e.cfg.Height)) //nolint:gosec // frame size
		cib.u32(nvCIBBufferFmt, e.format())
		if err := e.status("creating an input buffer", call(e.fnp(nvFnCreateInputBuffer), e.enc, cib.ptr())); err != nil {
			return err
		}
		cbb := newStruct(nvSizeCreateBitstream)
		cbb.u32(0, nvCreateBitstreamBufVer)
		if err := e.status("creating a bitstream buffer", call(e.fnp(nvFnCreateBitstreamBuffer), e.enc, cbb.ptr())); err != nil {
			return err
		}
		e.pairs = append(e.pairs, nvPair{in: cib.getPtr(nvCIBInputBuffer), out: cbb.getPtr(nvCBBBitstreamBuffer)})
		e.free = append(e.free, i)
	}
	return nil
}

// format is the input buffers' format: NV12, or P010 for 10-bit.
func (e *nvenc) format() uint32 {
	if e.cfg.BitDepth == 10 {
		return nvBufferFormatP010
	}
	return nvBufferFormatNV12
}

func (e *nvenc) Encode(fill func(*Picture)) error {
	if e.err != nil {
		return e.err
	}
	if len(e.free) == 0 {
		return errors.New("nvenc: no free buffer")
	}
	idx := e.free[0]
	e.free = e.free[1:]
	pair := e.pairs[idx]

	lib := newStruct(nvSizeLockInputBuffer)
	lib.u32(0, nvLockInputBufferVer)
	lib.uptr(nvLIBInputBuffer, pair.in)
	if err := e.status("locking an input buffer", call(e.fnp(nvFnLockInputBuffer), e.enc, lib.ptr())); err != nil {
		e.err = err
		return err
	}
	pitch := int(lib.getU32(nvLIBPitch))
	base := lib.getPtr(nvLIBDataPtr)
	h := e.cfg.Height
	fill(&Picture{Y: cbytes(base, pitch*h), UV: cbytes(base+uintptr(pitch*h), pitch*h/2), Pitch: pitch, Depth: e.cfg.BitDepth})
	if err := e.status("unlocking an input buffer", call(e.fnp(nvFnUnlockInputBuffer), e.enc, pair.in)); err != nil {
		e.err = err
		return err
	}

	pp := newStruct(nvSizePicParams)
	pp.u32(0, nvPicParamsVer)
	pp.u32(nvPPInputWidth, uint32(e.cfg.Width))   //nolint:gosec // frame size
	pp.u32(nvPPInputHeight, uint32(e.cfg.Height)) //nolint:gosec // frame size
	pp.u32(nvPPInputPitch, uint32(pitch))         //nolint:gosec // frame size
	pp.u64(nvPPInputTimeStamp, e.frame)
	pp.uptr(nvPPInputBuffer, pair.in)
	pp.uptr(nvPPOutputBitstream, pair.out)
	pp.u32(nvPPBufferFmt, e.format())
	pp.u32(nvPPPictureStruct, nvPicStructFrame)
	e.frame++
	e.pending = append(e.pending, idx)
	st := call(e.fnp(nvFnEncodePicture), e.enc, pp.ptr())
	switch st {
	case nvErrNeedMoreInput:
		return nil // held back for a later reference
	case nvSuccess:
		// Everything submitted is now on its way. Its bitstream is read only
		// when the buffers run short: locking it waits for the GPU, and
		// meanwhile the decoder can be drawing the next pictures.
		e.ready = append(e.ready, e.pending...)
		e.pending = e.pending[:0]
		return e.drain(nvSpare)
	}
	e.err = e.status("encoding", st)
	return e.err
}

// drain writes out the oldest encoded pictures' bitstreams, in order, until
// spare buffers are free (all of them with spare = nvBuffers).
func (e *nvenc) drain(spare int) error {
	for len(e.ready) > 0 && len(e.free) < spare {
		idx := e.ready[0]
		e.ready = e.ready[1:]
		lb := newStruct(nvSizeLockBitstream)
		lb.u32(0, nvLockBitstreamVer)
		lb.uptr(nvLBOutputBitstream, e.pairs[idx].out)
		if err := e.status("locking a bitstream", call(e.fnp(nvFnLockBitstream), e.enc, lb.ptr())); err != nil {
			e.err = err
			return err
		}
		data := cbytes(lb.getPtr(nvLBDataPtr), int(lb.getU32(nvLBSize)))
		_, werr := e.w.Write(data)
		if err := e.status("unlocking a bitstream", call(e.fnp(nvFnUnlockBitstream), e.enc, e.pairs[idx].out)); err != nil {
			e.err = err
			return err
		}
		if werr != nil {
			e.err = werr
			return werr
		}
		e.free = append(e.free, idx)
	}
	return nil
}

func (e *nvenc) Close() error {
	var err error
	if e.enc != 0 {
		if e.err == nil {
			// End of stream: the held-back pictures come out.
			pp := newStruct(nvSizePicParams)
			pp.u32(0, nvPicParamsVer)
			pp.u32(nvPPEncodePicFlags, nvPicFlagEOS)
			if err = e.status("flushing", call(e.fnp(nvFnEncodePicture), e.enc, pp.ptr())); err == nil {
				e.ready = append(e.ready, e.pending...)
				e.pending = e.pending[:0]
				err = e.drain(nvBuffers)
			}
		}
		for _, p := range e.pairs {
			call(e.fnp(nvFnDestroyInputBuffer), e.enc, p.in)
			call(e.fnp(nvFnDestroyBitstreamBuf), e.enc, p.out)
		}
		call(e.fnp(nvFnDestroyEncoder), e.enc)
		e.enc = 0
	}
	e.closeCUDA()
	if err == nil {
		err = e.err
	}
	return err
}

// qps returns the I, P and B quantisers for a constant-QP encode as ffmpeg
// derives them from -qp for NVENC, with its default factors.
func qps(qp int) (i, p, b int) {
	return clipQP(float64(qp) * 0.8), qp, clipQP(float64(qp)*1.25 + 1.25)
}

func clipQP(v float64) int { return max(0, min(51, int(v+0.5))) }
