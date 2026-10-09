//go:build linux && (amd64 || arm64)

package gpu

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"github.com/brunoga/bdtools/internal/hevc"
	"github.com/brunoga/bdtools/internal/mpeg2"
)

// VAAPI decoding of HEVC, MPEG-2 and H.264: the parsers here (internal/hevc,
// internal/mpeg2, mvc) read the stream and keep the reference pictures and
// their order; the GPU decodes each picture's slices into a surface (VAAPI
// has no parser of its own).

// The parameter buffers, as the libva headers lay them out (amd64 and
// arm64 align these fields as C does; TestVAAPIDecodeLayout checks them).

type vaPictureHEVC struct {
	PictureID uint32
	POC       int32
	Flags     uint32
	_         [4]uint32
}

type vaPicParamHEVC struct {
	CurrPic                                                  vaPictureHEVC
	Refs                                                     [15]vaPictureHEVC
	Width, Height                                            uint16
	PicFields                                                uint32
	MaxDecPicBufferingMinus1                                 uint8
	BitDepthLumaMinus8, BitDepthChromaMinus8                 uint8
	PCMBitDepthLumaMinus1, PCMBitDepthChromaMinus1           uint8
	Log2MinCbMinus3, Log2DiffMaxMinCb                        uint8
	Log2MinTbMinus2, Log2DiffMaxMinTb                        uint8
	Log2MinPCMMinus3, Log2DiffMaxMinPCM                      uint8
	MaxTrDepthIntra, MaxTrDepthInter                         uint8
	InitQPMinus26                                            int8
	DiffCuQPDeltaDepth                                       uint8
	CbQPOffset, CrQPOffset                                   int8
	Log2ParMrgLevelMinus2                                    uint8
	NumTileColumnsMinus1, NumTileRowsMinus1                  uint8
	ColumnWidthMinus1                                        [19]uint16
	RowHeightMinus1                                          [21]uint16
	SliceParsingFields                                       uint32
	Log2MaxPOCLsbMinus4, NumShortTermRPS, NumLongTermRefsSPS uint8
	NumRefIdxL0DefaultMinus1, NumRefIdxL1DefaultMinus1       uint8
	BetaOffsetDiv2, TcOffsetDiv2                             int8
	NumExtraSliceHeaderBits                                  uint8
	StRPSBits                                                uint32
	_                                                        [8]uint32
}

type vaSliceParamHEVC struct {
	DataSize, DataOffset, DataFlag, DataByteOffset, SegmentAddr uint32
	RefPicList                                                  [2][15]uint8
	LongSliceFlags                                              uint32
	CollocatedRefIdx                                            uint8
	NumRefIdxL0Minus1, NumRefIdxL1Minus1                        uint8
	QPDelta, CbQPOffset, CrQPOffset                             int8
	BetaOffsetDiv2, TcOffsetDiv2                                int8
	LumaLog2WeightDenom                                         uint8
	DeltaChromaLog2WeightDenom                                  int8
	Weights                                                     [2]vaWeightsHEVC
	FiveMinusMaxNumMergeCand                                    uint8
	NumEntryPointOffsets                                        uint16
	EntryOffsetToSubsetArray                                    uint16
	SliceDataNumEmuPrevnBytes                                   uint16
	_                                                           [2]uint32
}

// vaWeightsHEVC is a list's explicit weights in the slice parameters.
type vaWeightsHEVC struct {
	DeltaLumaWeight, LumaOffset     [15]int8
	DeltaChromaWeight, ChromaOffset [15][2]int8
}

type vaIQMatrixHEVC struct {
	L4      [6][16]uint8
	L8, L16 [6][64]uint8
	L32     [2][64]uint8
	DC16    [6]uint8
	DC32    [2]uint8
	_       [4]uint32
}

// vaDecSurfaces is how many surfaces a decoder makes: the 16 pictures
// HEVC may keep, the one being decoded, and room for the output.
const vaDecSurfaces = 20

type vaDecoder struct {
	f       *vaFuncs
	fd      *os.File
	dpy     uintptr
	picture func(*DecodedPicture) error
	codec   VideoCodec
	hevc    *hevc.Decoder
	mpeg2   *mpeg2.Decoder
	di      *Deinterlacing // MPEG-2's pictures, deinterlaced

	conf, ctx     uint32
	surfaces      []uint32
	free          []bool
	width, height int
	depth         int

	y, uv  []byte // the picture given out
	cb, cr []byte // MPEG-2's chroma planes
	err    error
}

// openVAAPIDecoder opens the first render node whose driver decodes the
// codec.
func openVAAPIDecoder(cfg DecodeConfig, picture func(*DecodedPicture) error) (Decoder, error) {
	switch cfg.Codec {
	case DecodeHEVC, DecodeMPEG2:
	default:
		return nil, fmt.Errorf("%w: VAAPI decoding is of HEVC and MPEG-2", ErrDecodeUnavailable)
	}
	f, err := loadVA()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecodeUnavailable, err)
	}
	devices, _ := filepath.Glob("/dev/dri/renderD*")
	firstErr := fmt.Errorf("%w: no render node", ErrDecodeUnavailable)
	for i, dev := range devices {
		d := &vaDecoder{f: f, picture: picture, codec: cfg.Codec}
		err := d.open(dev)
		if err == nil {
			switch cfg.Codec {
			case DecodeHEVC:
				d.hevc = hevc.New()
				d.hevc.SetAccel(d)
			case DecodeMPEG2:
				d.mpeg2 = mpeg2.New()
				d.mpeg2.SetAccel(vaMPEG2{d})
				d.di = NewDeinterlacing(picture)
			}
			return d, nil
		}
		d.release()
		if i == 0 {
			firstErr = err
		}
	}
	return nil, firstErr
}

func (d *vaDecoder) check(what string, st uintptr) error {
	if st == vaStatusSuccess {
		return nil
	}
	return fmt.Errorf("vaapi: %s: %s (%d)", what, cstring(call(d.f.errorStr, st)), st)
}

func (d *vaDecoder) open(dev string) error {
	fd, err := os.OpenFile(dev, os.O_RDWR, 0) //nolint:gosec // a render node
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDecodeUnavailable, err)
	}
	d.fd = fd
	d.dpy = call(d.f.getDisplayDRM, fd.Fd())
	if d.dpy == 0 {
		return fmt.Errorf("%w: %s: no VA display", ErrDecodeUnavailable, dev)
	}
	call(d.f.setInfoCallback, d.dpy, 0, 0)
	ver := newStruct(8)
	if st := call(d.f.initialize, d.dpy, ver.ptr(), ver.ptr()+4); st != vaStatusSuccess {
		d.dpy = 0
		return fmt.Errorf("%w: %s: %v", ErrDecodeUnavailable, dev, d.check("initialising", st))
	}
	switch d.codec {
	case DecodeHEVC:
		// Main 10 decoding covers Ultra HD Blu-ray; Main comes with it.
		if !d.vld(vaProfileHEVCMain10) || !d.vld(vaProfileHEVCMain) {
			return fmt.Errorf("%w: %s: the driver does not decode HEVC Main 10", ErrDecodeUnavailable, dev)
		}
	case DecodeMPEG2:
		if !d.vld(vaProfileMPEG2Main) {
			return fmt.Errorf("%w: %s: the driver does not decode MPEG-2", ErrDecodeUnavailable, dev)
		}
	}
	return nil
}

// vld reports whether the driver decodes the profile.
func (d *vaDecoder) vld(profile uint32) bool {
	n := int(call(d.f.maxNumEntrypoints, d.dpy))
	list := newStruct(4 * max(n, 1))
	num := newStruct(4)
	if call(d.f.queryConfigEntrypoints, d.dpy, uintptr(profile), list.ptr(), num.ptr()) != vaStatusSuccess {
		return false
	}
	for i := 0; i < int(num.getU32(0)) && i < n; i++ {
		if list.getU32(4*i) == vaEntrypointVLD {
			return true
		}
	}
	return false
}

func (d *vaDecoder) Decode(au []byte, pts int64) error {
	if d.err != nil {
		return d.err
	}
	var err error
	switch d.codec {
	case DecodeHEVC:
		err = d.hevc.Decode(au, pts, nil)
	case DecodeMPEG2:
		err = d.mpeg2.Decode(au, pts, d.takeMPEG2)
	}
	d.err = err
	return err
}

func (d *vaDecoder) Flush() error {
	if d.err != nil {
		return d.err
	}
	var err error
	switch d.codec {
	case DecodeHEVC:
		err = d.hevc.Flush(nil)
	case DecodeMPEG2:
		if err = d.mpeg2.Flush(d.takeMPEG2); err == nil {
			err = d.di.Flush()
		}
	}
	d.err = err
	return err
}

func (d *vaDecoder) Close() error {
	d.release()
	return nil
}

func (d *vaDecoder) release() {
	if d.dpy != 0 {
		d.destroyContext()
		call(d.f.terminate, d.dpy)
		d.dpy = 0
	}
	if d.fd != nil {
		_ = d.fd.Close()
		d.fd = nil
	}
	runtime.KeepAlive(d)
}

func (d *vaDecoder) destroyContext() {
	if d.ctx != 0 {
		call(d.f.destroyContext, d.dpy, uintptr(d.ctx))
		d.ctx = 0
	}
	if len(d.surfaces) > 0 {
		ids := newStruct(4 * len(d.surfaces))
		for i, id := range d.surfaces {
			ids.u32(4*i, id)
		}
		call(d.f.destroySurfaces, d.dpy, ids.ptr(), uintptr(len(d.surfaces)))
		d.surfaces, d.free = nil, nil
	}
	if d.conf != 0 {
		call(d.f.destroyConfig, d.dpy, uintptr(d.conf))
		d.conf = 0
	}
}

// NewSurface gives a free surface, making the context for the size and
// depth first if they are new.
func (d *vaDecoder) NewSurface(width, height, depth int) (int, error) {
	if width != d.width || height != d.height || depth != d.depth || d.ctx == 0 {
		if err := d.createContext(width, height, depth); err != nil {
			return 0, err
		}
	}
	for i, f := range d.free {
		if f {
			d.free[i] = false
			return i, nil
		}
	}
	return 0, errors.New("vaapi: no free surface")
}

func (d *vaDecoder) Release(surface int) {
	if surface >= 0 && surface < len(d.free) {
		d.free[surface] = true
	}
}

func (d *vaDecoder) createContext(width, height, depth int) error {
	d.destroyContext()
	profile, rt := uint32(vaProfileHEVCMain), uint32(vaRTFormatYUV420)
	switch {
	case d.codec == DecodeMPEG2:
		profile = vaProfileMPEG2Main
	case depth > 8:
		profile, rt = vaProfileHEVCMain10, vaRTFormatYUV420_10
	}
	attr := newStruct(vaSizeConfigAttrib)
	attr.u32(0, vaConfigAttribRTFormat)
	attr.u32(4, rt)
	id := newStruct(4)
	if err := d.check("creating the configuration", call(d.f.createConfig, d.dpy, uintptr(profile), vaEntrypointVLD, attr.ptr(), 1, id.ptr())); err != nil {
		return err
	}
	d.conf = id.getU32(0)
	s := newStruct(4 * vaDecSurfaces)
	if err := d.check("creating surfaces", call(d.f.createSurfaces, d.dpy, uintptr(rt), uintptr(width), uintptr(height), s.ptr(), vaDecSurfaces, 0, 0)); err != nil {
		return err
	}
	d.surfaces = make([]uint32, vaDecSurfaces)
	d.free = make([]bool, vaDecSurfaces)
	for i := range d.surfaces {
		d.surfaces[i] = s.getU32(4 * i)
		d.free[i] = true
	}
	if err := d.check("creating the context", call(d.f.createContext, d.dpy, uintptr(d.conf), uintptr(width), uintptr(height), vaProgressive,
		s.ptr(), vaDecSurfaces, id.ptr())); err != nil {
		return err
	}
	d.ctx = id.getU32(0)
	d.width, d.height, d.depth = width, height, depth
	return nil
}

// structBytes copies a Go mirror of a C struct into C-reachable memory.
func structBytes[T any](v *T) cstruct {
	n := int(unsafe.Sizeof(*v))
	s := newStruct(n)
	copy(s, unsafe.Slice((*byte)(unsafe.Pointer(v)), n))
	return s
}

func bit(b bool, pos int) uint32 { return b2u(b) << pos }

// vaBuffer is a parameter or data buffer of a picture.
type vaBuffer struct {
	typ  uintptr
	data cstruct
}

// slicesData copies slice data into C-reachable memory.
func slicesData(b []byte) cstruct {
	data := newStruct(len(b))
	copy(data, b)
	return data
}

// DecodePicture sends a picture's parameters and slices to the GPU.
func (d *vaDecoder) DecodePicture(p *hevc.AccelPicture, slices []hevc.AccelSlice) error {
	bufs := []vaBuffer{{vaPictureParameterBufferType, structBytes(d.picParams(p))}}
	if p.Scaling != nil {
		q := &vaIQMatrixHEVC{L4: p.Scaling.L4, L8: p.Scaling.L8, L16: p.Scaling.L16, L32: p.Scaling.L32,
			DC16: p.Scaling.DC16, DC32: p.Scaling.DC32}
		bufs = append(bufs, vaBuffer{vaIQMatrixBufferType, structBytes(q)})
	}
	for i := range slices {
		bufs = append(bufs, vaBuffer{vaSliceParameterBufferType, structBytes(sliceParams(&slices[i], i == len(slices)-1))},
			vaBuffer{vaSliceDataBufferType, slicesData(slices[i].Data)})
	}
	return d.render(p.Surface, bufs)
}

// render decodes a picture into surface from its buffers.
func (d *vaDecoder) render(surf int, in []vaBuffer) error {
	var bufs []uint32
	defer func() {
		for _, b := range bufs {
			call(d.f.destroyBuffer, d.dpy, uintptr(b))
		}
	}()
	for _, b := range in {
		id := newStruct(4)
		if err := d.check("creating a buffer", call(d.f.createBuffer, d.dpy, uintptr(d.ctx), b.typ, uintptr(len(b.data)), 1, b.data.ptr(), id.ptr())); err != nil {
			return err
		}
		bufs = append(bufs, id.getU32(0))
	}
	surface := uintptr(d.surfaces[surf])
	if err := d.check("starting a picture", call(d.f.beginPicture, d.dpy, uintptr(d.ctx), surface)); err != nil {
		return err
	}
	list := newStruct(4 * len(bufs))
	for i, b := range bufs {
		list.u32(4*i, b)
	}
	if err := d.check("sending a picture", call(d.f.renderPicture, d.dpy, uintptr(d.ctx), list.ptr(), uintptr(len(bufs)))); err != nil {
		return err
	}
	return d.check("decoding a picture", call(d.f.endPicture, d.dpy, uintptr(d.ctx)))
}

func (d *vaDecoder) picParams(p *hevc.AccelPicture) *vaPicParamHEVC {
	pp := &vaPicParamHEVC{
		CurrPic: vaPictureHEVC{PictureID: d.surfaces[p.Surface], POC: int32(p.POC)},
		Width:   uint16(p.Width), Height: uint16(p.Height),
		PicFields: uint32(p.ChromaFormat)<<vaHEVCDecChromaFormatBit | bit(p.SeparateColourPlane, vaHEVCDecSepColourBit) |
			bit(p.PCM, vaHEVCDecPCMBit) | bit(p.Scaling != nil, vaHEVCDecScalingBit) |
			bit(p.TransformSkip, vaHEVCDecTransformSkipBit) | bit(p.AMP, vaHEVCDecAMPBit) |
			bit(p.StrongIntraSmoothing, vaHEVCDecStrongIntraBit) | bit(p.SignDataHiding, vaHEVCDecSignHidingBit) |
			bit(p.ConstrainedIntraPred, vaHEVCDecConstrainedIntraBit) | bit(p.CuQPDelta, vaHEVCDecCUQPDeltaBit) |
			bit(p.WeightedPred, vaHEVCDecWeightedPredBit) | bit(p.WeightedBipred, vaHEVCDecWeightedBipredBit) |
			bit(p.TransquantBypass, vaHEVCDecBypassBit) | bit(p.Tiles, vaHEVCDecTilesBit) |
			bit(p.EntropySync, vaHEVCDecEntropySyncBit) | bit(p.LoopFilterAcrossSlices, vaHEVCDecLFAcrossSlicesBit) |
			bit(p.LoopFilterAcrossTiles, vaHEVCDecLFAcrossTilesBit) | bit(p.PCMLoopFilterDisabled, vaHEVCDecPCMLFDisabledBit),
		MaxDecPicBufferingMinus1: uint8(p.MaxDecPicBuffering - 1),
		BitDepthLumaMinus8:       uint8(p.BitDepth - 8), BitDepthChromaMinus8: uint8(p.BitDepthC - 8),
		Log2MinCbMinus3: uint8(p.Log2MinCb - 3), Log2DiffMaxMinCb: uint8(p.Log2Ctb - p.Log2MinCb),
		Log2MinTbMinus2: uint8(p.Log2MinTb - 2), Log2DiffMaxMinTb: uint8(p.Log2MaxTb - p.Log2MinTb),
		MaxTrDepthIntra: uint8(p.MaxTrDepthIntra), MaxTrDepthInter: uint8(p.MaxTrDepthInter),
		InitQPMinus26: int8(p.InitQP - 26), DiffCuQPDeltaDepth: uint8(p.DiffCuQPDeltaDepth),
		CbQPOffset: int8(p.CbQPOffset), CrQPOffset: int8(p.CrQPOffset), Log2ParMrgLevelMinus2: uint8(p.Log2ParMrgLevel - 2),
		SliceParsingFields: bit(p.ListsModification, vaHEVCDecListsModBit) | bit(p.LongTermRefsPresent, vaHEVCDecLTRefsBit) |
			bit(p.TemporalMVP, vaHEVCDecTMVPBit) | bit(p.CabacInitPresent, vaHEVCDecCabacInitBit) |
			bit(p.OutputFlagPresent, vaHEVCDecOutputFlagBit) | bit(p.DependentSlices, vaHEVCDecDependentBit) |
			bit(p.SliceChromaQPOffsets, vaHEVCDecSliceChromaQPBit) | bit(p.SAO, vaHEVCDecSAOBit) |
			bit(p.DeblockingOverride, vaHEVCDecDeblockOverrideBit) | bit(p.DeblockingDisabled, vaHEVCDecDeblockDisabledBit) |
			bit(p.SliceHeaderExtension, vaHEVCDecHeaderExtBit) | bit(p.IRAP, vaHEVCDecRapBit) |
			bit(p.IDR, vaHEVCDecIdrBit) | bit(p.IRAP, vaHEVCDecIntraBit),
		Log2MaxPOCLsbMinus4: uint8(p.Log2MaxPOCLsb - 4), NumShortTermRPS: uint8(p.NumShortTermRPS),
		NumLongTermRefsSPS:       uint8(p.NumLongTermRefsSPS),
		NumRefIdxL0DefaultMinus1: uint8(p.NumRefIdxDefault[0] - 1), NumRefIdxL1DefaultMinus1: uint8(p.NumRefIdxDefault[1] - 1),
		BetaOffsetDiv2: int8(p.BetaOffsetDiv2), TcOffsetDiv2: int8(p.TcOffsetDiv2),
		NumExtraSliceHeaderBits: uint8(p.NumExtraSliceHeaderBits), StRPSBits: uint32(p.StRPSBits),
	}
	if p.PCM {
		pp.PCMBitDepthLumaMinus1, pp.PCMBitDepthChromaMinus1 = uint8(p.PCMBits-1), uint8(p.PCMBitsC-1)
		pp.Log2MinPCMMinus3, pp.Log2DiffMaxMinPCM = uint8(p.Log2MinPCM-3), uint8(p.Log2MaxPCM-p.Log2MinPCM)
	}
	if p.Tiles {
		pp.NumTileColumnsMinus1, pp.NumTileRowsMinus1 = uint8(len(p.ColumnWidths)-1), uint8(len(p.RowHeights)-1)
		for i, w := range p.ColumnWidths {
			pp.ColumnWidthMinus1[i] = uint16(w - 1)
		}
		for i, h := range p.RowHeights {
			pp.RowHeightMinus1[i] = uint16(h - 1)
		}
	}
	for i := range pp.Refs {
		pp.Refs[i] = vaPictureHEVC{PictureID: vaInvalidID, Flags: vaPictureHEVCInvalid}
		if i >= len(p.Refs) {
			continue
		}
		r := &p.Refs[i]
		pp.Refs[i] = vaPictureHEVC{PictureID: d.surfaces[r.Surface], POC: int32(r.POC)}
		switch r.RPS {
		case hevc.RPSStCurrBefore:
			pp.Refs[i].Flags = vaPictureHEVCStCurrBefore
		case hevc.RPSStCurrAfter:
			pp.Refs[i].Flags = vaPictureHEVCStCurrAfter
		case hevc.RPSLtCurr:
			pp.Refs[i].Flags = vaPictureHEVCLtCurr
		}
		if r.LongTerm {
			pp.Refs[i].Flags |= vaPictureHEVCLongTerm
		}
	}
	return pp
}

func sliceParams(s *hevc.AccelSlice, last bool) *vaSliceParamHEVC {
	sp := &vaSliceParamHEVC{
		DataSize: uint32(len(s.Data)), DataFlag: vaSliceDataFlagAll, DataByteOffset: uint32(s.DataOffset),
		SegmentAddr: uint32(s.SegmentAddr),
		LongSliceFlags: bit(last, vaHEVCDecSliceLastBit) | bit(s.Dependent, vaHEVCDecSliceDependentBit) |
			uint32(s.Type)<<vaHEVCDecSliceTypeBit | uint32(s.ColourPlane)<<vaHEVCDecSliceColourPlaneBit |
			bit(s.SAOLuma, vaHEVCDecSliceSAOLumaBit) | bit(s.SAOChroma, vaHEVCDecSliceSAOChromaBit) |
			bit(s.MvdL1Zero, vaHEVCDecSliceMvdL1ZeroBit) | bit(s.CabacInit, vaHEVCDecSliceCabacInitBit) |
			bit(s.TemporalMVP, vaHEVCDecSliceTMVPBit) | bit(s.DeblockingDisabled, vaHEVCDecSliceDeblockOffBit) |
			bit(s.ColFromL0, vaHEVCDecSliceColFromL0Bit) | bit(s.LoopFilterAcross, vaHEVCDecSliceLFAcrossBit),
		CollocatedRefIdx: 0xff,
		QPDelta:          int8(s.QPDelta), CbQPOffset: int8(s.CbQPOffset), CrQPOffset: int8(s.CrQPOffset),
		BetaOffsetDiv2: int8(s.BetaOffsetDiv2), TcOffsetDiv2: int8(s.TcOffsetDiv2),
	}
	if s.TemporalMVP {
		sp.CollocatedRefIdx = uint8(s.ColRefIdx)
	}
	if s.Type != 2 {
		sp.FiveMinusMaxNumMergeCand = uint8(5 - s.MaxMergeCand)
	}
	for l := range 2 {
		for i := range sp.RefPicList[l] {
			sp.RefPicList[l][i] = 0xff
		}
		for i, r := range s.RefList[l] {
			if i < 15 {
				sp.RefPicList[l][i] = uint8(r)
			}
		}
	}
	if n := s.NumRefIdx[0]; n > 0 {
		sp.NumRefIdxL0Minus1 = uint8(n - 1)
	}
	if n := s.NumRefIdx[1]; n > 0 {
		sp.NumRefIdxL1Minus1 = uint8(n - 1)
	}
	if s.Weights[0] != nil {
		sp.LumaLog2WeightDenom = uint8(s.Log2WeightDenom)
		sp.DeltaChromaLog2WeightDenom = int8(s.Log2WeightDenomC - s.Log2WeightDenom)
		for l := range 2 {
			for i, w := range s.Weights[l] {
				if i >= 15 {
					break
				}
				lw := &sp.Weights[l]
				lw.DeltaLumaWeight[i] = int8(w.LumaWeight - 1<<s.Log2WeightDenom)
				lw.LumaOffset[i] = int8(w.LumaOffset)
				for c := range 2 {
					lw.DeltaChromaWeight[i][c] = int8(w.ChromaWeight[c] - 1<<s.Log2WeightDenomC)
					lw.ChromaOffset[i][c] = int8(w.ChromaOffset[c])
				}
			}
		}
	}
	return sp
}

// surfaceImage is a decoded surface mapped: its planes' addresses and
// pitches.
type surfaceImage struct {
	y, uv          uintptr
	pitch, uvPitch int
}

// mapSurface waits for the picture in surface and maps it (NV12 or P010,
// as want says) for fn to read.
func (d *vaDecoder) mapSurface(surface int, want uint32, fn func(*surfaceImage)) error {
	sid := uintptr(d.surfaces[surface])
	if err := d.check("waiting for a picture", call(d.f.syncSurface, d.dpy, sid)); err != nil {
		return err
	}
	img := newStruct(vaSizeImage)
	if err := d.check("mapping a surface", call(d.f.deriveImage, d.dpy, sid, img.ptr())); err != nil {
		return err
	}
	defer call(d.f.destroyImage, d.dpy, uintptr(img.getU32(vaImageID)))
	if img.getU32(vaImageFourcc) != want {
		return errors.New("vaapi: the decoded surface is in another format")
	}
	buf := uintptr(img.getU32(vaImageBuf))
	m := newStruct(8)
	if err := d.check("mapping an image", call(d.f.mapBuffer, d.dpy, buf, m.ptr())); err != nil {
		return err
	}
	base := m.getPtr(0)
	fn(&surfaceImage{y: base + uintptr(img.getU32(vaImageOffsets)), uv: base + uintptr(img.getU32(vaImageOffsets+4)),
		pitch: int(img.getU32(vaImagePitches)), uvPitch: int(img.getU32(vaImagePitches + 4))})
	return d.check("unmapping an image", call(d.f.unmapBuffer, d.dpy, buf))
}

// Output waits for the picture in surface and gives it out, its window
// copied out of the surface as NV12 or P010.
func (d *vaDecoder) Output(surface int, p *hevc.Picture) error {
	bps, depth, want := 1, 8, uint32(vaFourccNV12)
	if p.BitDepth > 8 {
		bps, depth, want = 2, 10, vaFourccP010
	}
	w, h := p.Width, p.Height
	ch := (h + 1) / 2
	row := (w + w&1) * bps
	if len(d.y) != row*h || len(d.uv) != row*ch {
		d.y, d.uv = make([]byte, row*h), make([]byte, row*ch)
	}
	x0 := p.Left * bps
	err := d.mapSurface(surface, want, func(m *surfaceImage) {
		for r := range h {
			copy(d.y[r*row:], cbytes(m.y+uintptr((p.Top+r)*m.pitch+x0), row))
		}
		for r := range ch {
			copy(d.uv[r*row:], cbytes(m.uv+uintptr((p.Top/2+r)*m.uvPitch+(p.Left/2)*2*bps), row))
		}
	})
	if err != nil {
		return err
	}
	return d.picture(&DecodedPicture{Width: w, Height: h, Depth: depth, Y: d.y, UV: d.uv, Pitch: row, PTS: p.PTS,
		FrameRateNum: p.FrameRateNum, FrameRateDen: p.FrameRateDen,
		Color: ColorInfo{Primaries: p.Primaries, Transfer: p.Transfer, Matrix: p.Matrix, FullRange: p.FullRange}})
}
