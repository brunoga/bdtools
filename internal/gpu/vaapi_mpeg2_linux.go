//go:build linux && (amd64 || arm64)

package gpu

import "github.com/brunoga/bdtools/internal/mpeg2"

// VAAPI decoding of MPEG-2, the parser here (internal/mpeg2) driving it;
// interlaced frames are deinterlaced here, as with the decoder in Go.

type vaPicParamMPEG2 struct {
	Width, Height     uint16
	Forward, Backward uint32
	CodingType        int32
	FCode             int32
	Ext               uint32
	_                 [4]uint32
}

type vaIQMatrixMPEG2 struct {
	LoadIntra, LoadNonIntra, LoadChromaIntra, LoadChromaNonIntra int32
	Intra, NonIntra, ChromaIntra, ChromaNonIntra                 [64]uint8
	_                                                            [4]uint32
}

type vaSliceParamMPEG2 struct {
	DataSize, DataOffset, DataFlag uint32
	MBOffset                       uint32
	HPos, VPos                     uint32
	QScaleCode, IntraSlice         int32
	_                              [4]uint32
}

// vaMPEG2 is the decoder as the MPEG-2 parser's accelerator.
type vaMPEG2 struct{ d *vaDecoder }

func (a vaMPEG2) NewSurface(width, height int) (int, error) { return a.d.NewSurface(width, height, 8) }

func (a vaMPEG2) Release(surface int) { a.d.Release(surface) }

func (a vaMPEG2) DecodePicture(p *mpeg2.AccelPicture, slices []mpeg2.AccelSlice) error {
	d := a.d
	ref := func(s int) uint32 {
		if s < 0 {
			return vaInvalidID
		}
		return d.surfaces[s]
	}
	pp := &vaPicParamMPEG2{Width: uint16(p.Width), Height: uint16(p.Height),
		Forward: ref(p.Forward), Backward: ref(p.Backward), CodingType: int32(p.CodingType),
		FCode: int32(p.FCode[0][0]<<12 | p.FCode[0][1]<<8 | p.FCode[1][0]<<4 | p.FCode[1][1]),
		Ext: uint32(p.DCPrecision)<<vaMPEG2DCPrecisionBit | uint32(p.Structure)<<vaMPEG2StructureBit |
			bit(p.TopFieldFirst, vaMPEG2TFFBit) | bit(p.FramePredFrameDCT, vaMPEG2FramePredBit) |
			bit(p.Concealment, vaMPEG2ConcealmentBit) | bit(p.QScaleType, vaMPEG2QScaleTypeBit) |
			bit(p.IntraVLC, vaMPEG2IntraVLCBit) | bit(p.AlternateScan, vaMPEG2AltScanBit) |
			bit(p.RepeatFirstField, vaMPEG2RepeatFirstBit) | bit(p.ProgressiveFrame, vaMPEG2ProgressiveBit) |
			bit(p.FirstField, vaMPEG2FirstFieldBit)}
	q := &vaIQMatrixMPEG2{LoadIntra: 1, LoadNonIntra: 1, Intra: p.IntraQ, NonIntra: p.NonIntraQ}
	bufs := []vaBuffer{{vaPictureParameterBufferType, structBytes(pp)}, {vaIQMatrixBufferType, structBytes(q)}}
	for i := range slices {
		s := &slices[i]
		sp := &vaSliceParamMPEG2{DataSize: uint32(len(s.Data)), DataFlag: vaSliceDataFlagAll,
			MBOffset: uint32(s.MacroblockOffset), HPos: uint32(s.MBX), VPos: uint32(s.MBY),
			QScaleCode: int32(s.QuantiserScaleCode), IntraSlice: int32(b2u(s.IntraSlice))}
		bufs = append(bufs, vaBuffer{vaSliceParameterBufferType, structBytes(sp)},
			vaBuffer{vaSliceDataBufferType, slicesData(s.Data)})
	}
	return d.render(p.Surface, bufs)
}

// Output copies the frame in surface out as planes.
func (a vaMPEG2) Output(surface int, p *mpeg2.Picture) error {
	d := a.d
	w, h := p.Width, p.Height
	cw, ch := (w+1)/2, (h+1)/2
	if len(d.y) != w*h || len(d.cb) != cw*ch {
		d.y, d.cb, d.cr = make([]byte, w*h), make([]byte, cw*ch), make([]byte, cw*ch)
	}
	err := d.mapSurface(surface, vaFourccNV12, func(m *surfaceImage) {
		for r := range h {
			copy(d.y[r*w:r*w+w], cbytes(m.y+uintptr(r*m.pitch), w))
		}
		for r := range ch {
			uv := cbytes(m.uv+uintptr(r*m.uvPitch), 2*cw)
			cb, cr := d.cb[r*cw:r*cw+cw], d.cr[r*cw:r*cw+cw]
			for x := range cw {
				cb[x], cr[x] = uv[2*x], uv[2*x+1]
			}
		}
	})
	p.Y, p.Cb, p.Cr, p.StrideY, p.StrideC = d.y, d.cb, d.cr, w, cw
	return err
}

func (d *vaDecoder) takeMPEG2(p *mpeg2.Picture) error { return d.di.Take(MPEG2Picture(p)) }
