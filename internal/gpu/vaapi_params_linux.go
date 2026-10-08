//go:build linux && (amd64 || arm64)

package gpu

import "math"

// The VA parameter buffers for a picture: the sequence on every IDR, the
// picture and its one slice.

type addBuf func(typ uintptr, s cstruct) error

func (e *vaapi) h264Params(p vaPic, l0, l1 []vaPic, add addBuf) error {
	w, h := e.cfg.Width, e.cfg.Height
	mbw, mbh := (w+15)/16, (h+15)/16
	if p.idr {
		s := newStruct(vaSizeH264Seq)
		s.u8(vaH264SeqLevel, uint8(h264Level(mbw, mbh, e.cfg))) //nolint:gosec // a level
		s.u32(vaH264SeqIntraPeriod, uint32(e.cfg.GOP))          //nolint:gosec // small
		s.u32(vaH264SeqIDRPeriod, uint32(e.cfg.GOP))            //nolint:gosec // small
		s.u32(vaH264SeqIPPeriod, uint32(e.bFrames+1))           //nolint:gosec // small
		s.u32(vaH264SeqMaxRefs, uint32(e.maxRefs))              //nolint:gosec // small
		s.u16(vaH264SeqWidthMBs, uint16(mbw))                   //nolint:gosec // frame size
		s.u16(vaH264SeqHeightMBs, uint16(mbh))                  //nolint:gosec // frame size
		s.u32(vaH264SeqFields, 1<<vaH264SeqChromaFormatBit|1<<vaH264SeqFrameMBsOnlyBit|1<<vaH264SeqDirect8x8Bit|
			uint32(e.log2MaxFrameNum()-4)<<vaH264SeqLog2MaxFrameNumBit| //nolint:gosec // 0..12
			uint32(e.log2MaxPOCLsb()-4)<<vaH264SeqLog2MaxPOCLsbBit) //nolint:gosec // 0..12
		if right, bottom := mbw*16-w, mbh*16-h; right != 0 || bottom != 0 {
			s.u8(vaH264SeqCropping, 1)
			s.u32(vaH264SeqCropRight, uint32(right/2))   //nolint:gosec // < 8
			s.u32(vaH264SeqCropBottom, uint32(bottom/2)) //nolint:gosec // < 8
		}
		s.u8(vaH264SeqVUI, 1)
		s.u32(vaH264SeqVUIFields, 1<<vaH264SeqTimingBit|1<<vaH264SeqFixedRateBit)
		s.u32(vaH264SeqUnitsInTick, uint32(e.cfg.FPSDen)) //nolint:gosec // frame rate
		s.u32(vaH264SeqTimeScale, uint32(2*e.cfg.FPSNum)) //nolint:gosec // frame rate
		if err := add(vaEncSequenceParameterBufferType, s); err != nil {
			return err
		}
	}

	pic := func(s cstruct, off int, q vaPic, flags uint32) {
		s.u32(off+vaH264PicID, e.recs[q.rec])
		s.u32(off+vaH264PicFrameIdx, uint32(q.frameNum)) //nolint:gosec // small
		s.u32(off+vaH264PicFlags, flags)
		s.u32(off+vaH264PicTopPOC, uint32(2*q.disp))    //nolint:gosec // small
		s.u32(off+vaH264PicBottomPOC, uint32(2*q.disp)) //nolint:gosec // small
	}
	invalid := func(s cstruct, off, n int) {
		for i := range n {
			s.u32(off+i*vaSizePictureH264+vaH264PicID, vaInvalidID)
			s.u32(off+i*vaSizePictureH264+vaH264PicFlags, vaPictureH264Invalid)
		}
	}
	pp := newStruct(vaSizeH264Pic)
	pic(pp, vaH264PicCurr, p, 0)
	invalid(pp, vaH264PicRefs, 16)
	for i, r := range e.dpb {
		pic(pp, vaH264PicRefs+i*vaSizePictureH264, r, vaPictureH264ShortTermRef)
	}
	pp.u32(vaH264PicCodedBuf, e.codeds[p.coded])
	pp.u16(vaH264PicFrameNum, uint16(p.frameNum)) //nolint:gosec // small
	pp.u8(vaH264PicInitQP, uint8(e.qpP))          //nolint:gosec // 0..51
	fields := uint32(1<<vaH264PicCABACBit | 1<<vaH264PicT8x8Bit | 1<<vaH264PicDeblockCtlBit)
	if p.idr {
		fields |= 1 << vaH264PicIDRBit
	}
	if p.typ != picB {
		fields |= 1 << vaH264PicRefBit
	}
	pp.u32(vaH264PicFields, fields)
	if err := add(vaEncPictureParameterBufferType, pp); err != nil {
		return err
	}
	if p.idr && e.packed {
		if err := e.packedSequence(add); err != nil {
			return err
		}
	}

	sl := newStruct(vaSizeH264Slice)
	sl.u32(vaH264SliceNumMBs, uint32(mbw*mbh)) //nolint:gosec // frame size
	sl.u32(vaH264SliceMBInfo, vaInvalidID)
	sliceType, qp := 2, e.qpI
	switch p.typ {
	case picP:
		sliceType, qp = 0, e.qpP
	case picB:
		sliceType, qp = 1, e.qpB
	}
	sl.u8(vaH264SliceType, uint8(sliceType))         //nolint:gosec // 0..2
	sl.u16(vaH264SliceIDRID, uint16(e.idrID&0xffff)) //nolint:gosec // masked
	sl.u16(vaH264SlicePOCLsb, uint16(2*p.disp))      //nolint:gosec // within the POC range
	sl.u8(vaH264SliceDirectSpatial, 1)
	invalid(sl, vaH264SliceRefList0, 32)
	invalid(sl, vaH264SliceRefList1, 32)
	if len(l0) > 0 {
		pic(sl, vaH264SliceRefList0, l0[0], vaPictureH264ShortTermRef)
	}
	if len(l1) > 0 {
		pic(sl, vaH264SliceRefList1, l1[0], vaPictureH264ShortTermRef)
	}
	sl.u8(vaH264SliceQPDelta, uint8(int8(qp-e.qpP))) //nolint:gosec // a small signed delta
	return add(vaEncSliceParameterBufferType, sl)
}

func (e *vaapi) hevcParams(p vaPic, l0, l1 []vaPic, add addBuf) error {
	c := e.hevc
	w, h := e.cfg.Width, e.cfg.Height
	if p.idr {
		s := newStruct(vaSizeHEVCSeq)
		profile := uint8(1) // Main
		if e.cfg.BitDepth == 10 {
			profile = 2 // Main 10
		}
		s.u8(vaHEVCSeqProfile, profile)
		s.u8(vaHEVCSeqLevel, uint8(hevcLevel(e.cfg)))  //nolint:gosec // a level
		s.u32(vaHEVCSeqIntraPeriod, uint32(e.cfg.GOP)) //nolint:gosec // small
		s.u32(vaHEVCSeqIDRPeriod, uint32(e.cfg.GOP))   //nolint:gosec // small
		s.u32(vaHEVCSeqIPPeriod, uint32(e.bFrames+1))  //nolint:gosec // small
		s.u16(vaHEVCSeqWidth, uint16(w))               //nolint:gosec // frame size
		s.u16(vaHEVCSeqHeight, uint16(h))              //nolint:gosec // frame size
		depth := uint32(e.cfg.BitDepth - 8)            //nolint:gosec // 0 or 2
		s.u32(vaHEVCSeqFields, 1<<vaHEVCSeqChromaFormatBit|depth<<vaHEVCSeqLumaDepthBit|depth<<vaHEVCSeqChromaDepthBit|
			b2u(c.amp)<<vaHEVCSeqAMPBit|
			b2u(c.sao)<<vaHEVCSeqSAOBit|b2u(c.tmvp)<<vaHEVCSeqTMVPBit|
			b2u(c.strongIntra)<<vaHEVCSeqStrongIntraBit|b2u(e.bFrames == 0)<<vaHEVCSeqLowDelayBit)
		s.u8(vaHEVCSeqLog2MinCB, uint8(c.log2MinCB-3))             //nolint:gosec // 0..3
		s.u8(vaHEVCSeqLog2DiffCB, uint8(c.log2MaxCTB-c.log2MinCB)) //nolint:gosec // 0..3
		s.u8(vaHEVCSeqLog2MinTB, uint8(c.log2MinTB-2))             //nolint:gosec // 0..3
		s.u8(vaHEVCSeqLog2DiffTB, uint8(c.log2MaxTB-c.log2MinTB))  //nolint:gosec // 0..3
		s.u8(vaHEVCSeqTHDInter, uint8(c.thdInter))                 //nolint:gosec // 0..3
		s.u8(vaHEVCSeqTHDIntra, uint8(c.thdIntra))                 //nolint:gosec // 0..3
		s.u8(vaHEVCSeqVUI, 1)
		s.u32(vaHEVCSeqVUIFields, 1<<vaHEVCSeqTimingBit)
		s.u32(vaHEVCSeqUnitsInTick, uint32(e.cfg.FPSDen)) //nolint:gosec // frame rate
		s.u32(vaHEVCSeqTimeScale, uint32(e.cfg.FPSNum))   //nolint:gosec // frame rate
		if err := add(vaEncSequenceParameterBufferType, s); err != nil {
			return err
		}
	}

	pic := func(s cstruct, off int, q vaPic, flags uint32) {
		s.u32(off+vaHEVCPicID, e.recs[q.rec])
		s.u32(off+vaHEVCPicPOC, uint32(q.disp)) //nolint:gosec // small
		s.u32(off+vaHEVCPicFlags, flags)
	}
	invalid := func(s cstruct, off int) {
		for i := range 15 {
			s.u32(off+i*vaSizePictureHEVC+vaHEVCPicID, vaInvalidID)
			s.u32(off+i*vaSizePictureHEVC+vaHEVCPicFlags, vaPictureHEVCInvalid)
		}
	}
	side := func(r vaPic) uint32 {
		if r.disp < p.disp {
			return vaPictureHEVCStCurrBefore
		}
		return vaPictureHEVCStCurrAfter
	}
	// The references this picture keeps: what it and the pictures after it
	// in decoding order use. Anything left out is dropped from the DPB.
	var refs []vaPic
	switch p.typ {
	case picP:
		refs = l0
	case picB:
		refs = append(append(refs, l0...), l1...)
	}
	pp := newStruct(vaSizeHEVCPic)
	pic(pp, vaHEVCPicCurr, p, 0)
	invalid(pp, vaHEVCPicRefs)
	for i, r := range refs {
		pic(pp, vaHEVCPicRefs+i*vaSizePictureHEVC, r, side(r))
	}
	pp.u32(vaHEVCPicCodedBuf, e.codeds[p.coded])
	pp.u8(vaHEVCPicCollocated, 0)
	pp.u8(vaHEVCPicInitQP, uint8(e.qpP)) //nolint:gosec // 0..51
	nal, coding := uint8(1), uint32(2)   // TRAIL_R, P
	switch {
	case p.idr:
		nal, coding = 19, 1 // IDR_W_RADL, I
	case p.typ == picB:
		nal, coding = 0, 3 // TRAIL_N, B
	}
	pp.u8(vaHEVCPicNALType, nal)
	fields := coding<<vaHEVCPicCodingTypeBit | b2u(p.idr)<<vaHEVCPicIDRBit | b2u(p.typ != picB)<<vaHEVCPicRefBit |
		b2u(c.signHiding)<<vaHEVCPicSignHidingBit | b2u(c.transformSkip)<<vaHEVCPicTransformSkipBit |
		b2u(c.cuQPDelta)<<vaHEVCPicCUQPDeltaBit
	pp.u32(vaHEVCPicFields, fields)
	if err := add(vaEncPictureParameterBufferType, pp); err != nil {
		return err
	}
	if p.idr && e.packed {
		if err := e.packedSequence(add); err != nil {
			return err
		}
	}

	ctb := 1 << c.log2MaxCTB
	sl := newStruct(vaSizeHEVCSlice)
	sl.u32(vaHEVCSliceNumCTUs, uint32(((w+ctb-1)/ctb)*((h+ctb-1)/ctb))) //nolint:gosec // frame size
	sliceType, qp := 2, e.qpI
	switch {
	case p.typ == picB:
		sliceType, qp = 0, e.qpB
	case p.typ == picP && e.gpb:
		sliceType, qp = 0, e.qpP
	case p.typ == picP:
		sliceType, qp = 1, e.qpP
	}
	sl.u8(vaHEVCSliceType, uint8(sliceType)) //nolint:gosec // 0..2
	invalid(sl, vaHEVCSliceRefList0)
	invalid(sl, vaHEVCSliceRefList1)
	if len(l0) > 0 {
		pic(sl, vaHEVCSliceRefList0, l0[0], side(l0[0]))
	}
	if len(l1) > 0 {
		pic(sl, vaHEVCSliceRefList1, l1[0], side(l1[0]))
	}
	sl.u8(vaHEVCSliceMaxMerge, 5)
	sl.u8(vaHEVCSliceQPDelta, uint8(int8(qp-e.qpP))) //nolint:gosec // a small signed delta
	sl.u32(vaHEVCSliceFields, 1<<vaHEVCSliceLastBit|b2u(c.tmvp && p.typ != picI)<<vaHEVCSliceTMVPBit|
		b2u(c.sao)<<vaHEVCSliceSAOLumaBit|b2u(c.sao)<<vaHEVCSliceSAOChromaBit|1<<vaHEVCSliceColFromL0Bit)
	if e.packedSlices {
		if err := e.packedSlice(add, e.hevcSliceHeader(p, sliceType, qp-e.qpP, refs)); err != nil {
			return err
		}
	}
	return add(vaEncSliceParameterBufferType, sl)
}

// h264Level is the lowest level (Table A-1) whose frame size and macroblock
// rate take the stream, and at least 4.1, where Blu-ray's 1080p sits.
func h264Level(mbw, mbh int, cfg Config) int {
	fs := mbw * mbh
	rate := float64(fs) * float64(cfg.FPSNum) / float64(max(cfg.FPSDen, 1))
	for _, l := range []struct {
		idc, maxFS int
		maxMBPS    float64
	}{{41, 8192, 245760}, {42, 8704, 522240}, {50, 22080, 589824}, {51, 36864, 983040}, {52, 36864, 2073600},
		{60, 139264, 4177920}, {61, 139264, 8355840}} {
		side := int(math.Sqrt(float64(8 * l.maxFS)))
		if fs <= l.maxFS && rate <= l.maxMBPS && mbw <= side && mbh <= side {
			return l.idc
		}
	}
	return 62
}

// hevcLevel is the lowest level (Table A.8) whose picture size and sample
// rate take the stream, and at least 4.1, as general_level_idc.
func hevcLevel(cfg Config) int {
	ps := cfg.Width * cfg.Height
	rate := float64(ps) * float64(cfg.FPSNum) / float64(max(cfg.FPSDen, 1))
	for _, l := range []struct {
		idc, maxPS int
		maxSR      float64
	}{{123, 2228224, 133693440}, {150, 8912896, 267386880}, {153, 8912896, 534773760}, {156, 8912896, 1069547520},
		{180, 35651584, 1069547520}, {183, 35651584, 2139095040}} {
		side := int(math.Sqrt(float64(8 * l.maxPS)))
		if ps <= l.maxPS && rate <= l.maxSR && cfg.Width <= side && cfg.Height <= side {
			return l.idc
		}
	}
	return 186
}
