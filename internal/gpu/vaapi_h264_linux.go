//go:build linux && (amd64 || arm64)

package gpu

import "github.com/brunoga/bdtools/mvc"

// VAAPI decoding of H.264, the decoder here (mvc) parsing: progressive
// pictures of the base view (the drivers do not decode MVC; interlaced
// streams go elsewhere, as the parser here does not take fields).

type vaPictureH264 struct {
	PictureID, FrameIdx, Flags uint32
	TopPOC, BottomPOC          int32
	_                          [4]uint32
}

type vaPicParamH264 struct {
	CurrPic                                      vaPictureH264
	Refs                                         [16]vaPictureH264
	WidthMbsMinus1, HeightMbsMinus1              uint16
	BitDepthLumaMinus8, BitDepthChromaMinus8     uint8
	NumRefFrames                                 uint8
	SeqFields                                    uint32
	NumSliceGroupsMinus1, SliceGroupMapType      uint8
	SliceGroupChangeRateMinus1                   uint16
	PicInitQPMinus26, PicInitQSMinus26           int8
	ChromaQPIndexOffset, SecondChromaQPIndexOffs int8
	PicFields                                    uint32
	FrameNum                                     uint16
	_                                            [8]uint32
}

type vaIQMatrixH264 struct {
	Scaling4x4 [6][16]uint8
	Scaling8x8 [2][64]uint8
	_          [4]uint32
}

type vaSliceParamH264 struct {
	DataSize, DataOffset, DataFlag uint32
	BitOffset                      uint16
	FirstMb                        uint16
	SliceType                      uint8
	DirectSpatial                  uint8
	NumRefIdxL0Minus1              uint8
	NumRefIdxL1Minus1              uint8
	CabacInitIdc                   uint8
	QPDelta                        int8
	DisableDeblock                 uint8
	AlphaDiv2, BetaDiv2            int8
	RefPicList                     [2][32]vaPictureH264
	LumaLog2Denom, ChromaLog2Denom uint8
	Weights                        [2]vaWeightsH264
	_                              [4]uint32
}

// vaWeightsH264 is a list's explicit weights in the slice parameters.
type vaWeightsH264 struct {
	LumaFlag                 uint8
	LumaWeight, LumaOffset   [32]int16
	ChromaFlag               uint8
	ChromaWeight, ChromaOffs [32][2]int16
}

// vaH264 is the decoder as the H.264 parser's accelerator.
type vaH264 struct{ d *vaDecoder }

func (a vaH264) NewSurface(width, height int) (int, error) { return a.d.NewSurface(width, height, 8) }

func (a vaH264) vaPic(r mvc.AccelRef) vaPictureH264 {
	p := vaPictureH264{PictureID: a.d.surfaces[r.Surface], FrameIdx: uint32(r.FrameIdx), Flags: vaPictureH264ShortTermRef,
		TopPOC: r.TopPOC, BottomPOC: r.BottomPOC}
	if r.LongTerm {
		p.Flags = vaPictureH264LongTerm
	}
	return p
}

var vaPictureH264None = vaPictureH264{PictureID: vaInvalidID, Flags: vaPictureH264Invalid}

func (a vaH264) DecodePicture(p *mvc.AccelPicture, slices []mvc.AccelSlice) error {
	d := a.d
	pp := &vaPicParamH264{
		CurrPic: vaPictureH264{PictureID: d.surfaces[p.Surface], FrameIdx: uint32(p.FrameNum),
			TopPOC: p.TopPOC, BottomPOC: p.BottomPOC},
		WidthMbsMinus1: uint16(p.WidthMbs - 1), HeightMbsMinus1: uint16(p.HeightMbs - 1),
		NumRefFrames: uint8(p.NumRefFrames),
		SeqFields: uint32(p.ChromaFormat)<<vaH264ChromaFormatBit | bit(p.GapsAllowed, vaH264GapsBit) |
			bit(p.FrameMbsOnly, vaH264FrameMBsOnlyBit) | bit(p.MBAFF, vaH264MBAFFBit) |
			bit(p.Direct8x8Inference, vaH264Direct8x8Bit) | bit(p.MinLumaBiPred8x8, vaH264MinBiPred8x8Bit) |
			uint32(p.Log2MaxFrameNum-4)<<vaH264Log2MaxFrameNumBit | uint32(p.POCType)<<vaH264POCTypeBit |
			uint32(max(p.Log2MaxPOCLsb-4, 0))<<vaH264Log2MaxPOCLsbBit | bit(p.DeltaPicOrderAlwaysZero, vaH264DeltaPOCZeroBit),
		PicInitQPMinus26: int8(p.PicInitQP - 26), PicInitQSMinus26: int8(p.PicInitQS - 26),
		ChromaQPIndexOffset: int8(p.ChromaQPOffset[0]), SecondChromaQPIndexOffs: int8(p.ChromaQPOffset[1]),
		PicFields: bit(p.CABAC, vaH264CABACBit) | bit(p.WeightedPred, vaH264WeightedPredBit) |
			uint32(p.WeightedBipredIdc)<<vaH264WeightedBipredBit | bit(p.Transform8x8, vaH264Transform8x8Bit) |
			bit(p.ConstrainedIntraPred, vaH264ConstrainedIntraBit) | bit(p.BottomFieldPicOrder, vaH264POCPresentBit) |
			bit(p.DeblockingControl, vaH264DeblockControlBit) | bit(p.RedundantPicCntPresent, vaH264RedundantPicCntBit) |
			bit(p.Reference, vaH264ReferenceBit),
		FrameNum: uint16(p.FrameNum),
	}
	if p.Reference {
		pp.CurrPic.Flags = vaPictureH264ShortTermRef
	}
	for i := range pp.Refs {
		pp.Refs[i] = vaPictureH264None
		if i < len(p.Refs) {
			pp.Refs[i] = a.vaPic(p.Refs[i])
		}
	}
	q := &vaIQMatrixH264{Scaling4x4: p.Scaling4x4, Scaling8x8: p.Scaling8x8}
	bufs := []vaBuffer{{vaPictureParameterBufferType, structBytes(pp)}, {vaIQMatrixBufferType, structBytes(q)}}
	for i := range slices {
		s := &slices[i]
		sp := &vaSliceParamH264{DataSize: uint32(len(s.Data)), DataFlag: vaSliceDataFlagAll,
			BitOffset: uint16(s.BitOffset), FirstMb: uint16(s.FirstMb), SliceType: uint8(s.SliceType),
			DirectSpatial: uint8(b2u(s.DirectSpatial)), CabacInitIdc: uint8(s.CabacInitIdc), QPDelta: int8(s.QPDelta),
			DisableDeblock: uint8(s.DisableDeblock), AlphaDiv2: int8(s.AlphaDiv2), BetaDiv2: int8(s.BetaDiv2),
			LumaLog2Denom: uint8(s.LumaLog2Denom), ChromaLog2Denom: uint8(s.ChromaLog2Denom)}
		if n := s.NumRefIdx[0]; n > 0 {
			sp.NumRefIdxL0Minus1 = uint8(n - 1)
		}
		if n := s.NumRefIdx[1]; n > 0 {
			sp.NumRefIdxL1Minus1 = uint8(n - 1)
		}
		for l := range 2 {
			for i := range sp.RefPicList[l] {
				sp.RefPicList[l][i] = vaPictureH264None
				if i < len(s.RefList[l]) {
					sp.RefPicList[l][i] = a.vaPic(s.RefList[l][i])
				}
			}
			w := &sp.Weights[l]
			w.LumaFlag, w.ChromaFlag = uint8(b2u(s.LumaWeighted[l])), uint8(b2u(s.ChromaWeighted[l]))
			w.LumaWeight, w.LumaOffset = s.LumaWeight[l], s.LumaOffset[l]
			w.ChromaWeight, w.ChromaOffs = s.ChromaWeight[l], s.ChromaOffset[l]
		}
		bufs = append(bufs, vaBuffer{vaSliceParameterBufferType, structBytes(sp)},
			vaBuffer{vaSliceDataBufferType, slicesData(s.Data)})
	}
	return d.render(p.Surface, bufs)
}

// outputH264 gives out the frames the parser has ready.
func (d *vaDecoder) outputH264() error {
	for {
		f, ok := d.h264.NextFrame()
		if !ok {
			return nil
		}
		err := d.outputFrame(f.Base)
		f.Release()
		if err != nil {
			return err
		}
	}
}

func (d *vaDecoder) outputFrame(f *mvc.Frame) error {
	if f.Surface < 0 {
		return nil // a picture that had no surface (the decoder reports why)
	}
	w, h := f.Width, f.Height
	ch := (h + 1) / 2
	row := w + w&1
	if len(d.y) != row*h || len(d.uv) != row*ch {
		d.y, d.uv = make([]byte, row*h), make([]byte, row*ch)
	}
	err := d.mapSurface(f.Surface, vaFourccNV12, func(m *surfaceImage) {
		for r := range h {
			copy(d.y[r*row:], cbytes(m.y+uintptr((f.CropTop+r)*m.pitch+f.CropLeft), row))
		}
		for r := range ch {
			copy(d.uv[r*row:], cbytes(m.uv+uintptr((f.CropTop/2+r)*m.uvPitch+f.CropLeft&^1), row))
		}
	})
	if err != nil {
		return err
	}
	num, den := d.h264.FrameRate()
	prim, trc, matrix, full := d.h264.ColorInfo()
	return d.picture(&DecodedPicture{Width: w, Height: h, Depth: 8, Y: d.y, UV: d.uv, Pitch: row, PTS: f.PTS,
		FrameRateNum: num, FrameRateDen: den,
		Color: ColorInfo{Primaries: prim, Transfer: trc, Matrix: matrix, FullRange: full}})
}

// Fill makes the picture in surface one value throughout (the frames a
// gap in frame_num makes up).
func (a vaH264) Fill(surface int, v byte) error {
	d := a.d
	return d.mapSurface(surface, vaFourccNV12, func(m *surfaceImage) {
		h := d.height
		for r := range h {
			row := cbytes(m.y+uintptr(r*m.pitch), d.width)
			for i := range row {
				row[i] = v
			}
		}
		for r := range h / 2 {
			row := cbytes(m.uv+uintptr(r*m.uvPitch), d.width)
			for i := range row {
				row[i] = v
			}
		}
	})
}
