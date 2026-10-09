package mvc

import (
	"sort"
	"sync"
)

// Hardware decoding: with an Accel, the decoder parses the stream and keeps
// the reference pictures, their marking and lists, and the output order as
// it always does, and hands each picture's slices to the accelerator
// (VAAPI) in place of decoding them itself. Pictures live in the
// accelerator's surfaces; a Frame given out names its surface. Only the
// base view is decoded (the GPUs' drivers do not decode MVC).

// Accel decodes pictures for the decoder.
type Accel interface {
	// NewSurface gives a surface for pictures of the size (in luma
	// samples: whole macroblocks). A picture keeps its surface for its
	// life; a new size comes with an IDR picture.
	NewSurface(width, height int) (int, error)
	// DecodePicture decodes a picture from its slices, in order.
	DecodePicture(p *AccelPicture, slices []AccelSlice) error
}

// AccelFiller is an Accel that can fill a surface with one value: for the
// picture standing in for a missing reference, mid-grey as the decoder
// here makes it.
type AccelFiller interface {
	Fill(surface int, v byte) error
}

// gapAccelPicture makes a frame a gap in frame_num makes up: it shares the
// surface of the latest reference decoded, as ffmpeg's do (the drivers
// keep their own state of each surface decoded into, which one never
// decoded into lacks), and stays out of the pool.
func (d *Decoder) gapAccelPicture(v *viewState, s *sps) *picture {
	p := &picture{mbW: s.widthMbs, mbH: s.heightMbs(), width: s.widthMbs * 16, height: s.heightMbs() * 16, surface: -1}
	p.cond = sync.NewCond(&p.mu)
	for i := len(v.refs) - 1; i >= 0; i-- {
		if r := v.refs[i]; !r.nonExisting && r.surface >= 0 {
			p.surface = r.surface
			return p
		}
	}
	g := d.greyPicture(p.mbW, p.mbH)
	p.surface = g.surface
	return p
}

// aliased reports whether a frame a gap made up shares p's surface (and so
// p must not be decoded into again yet).
func (d *Decoder) aliased(p *picture) bool {
	if d.accel == nil {
		return false
	}
	for i := range d.views {
		for _, r := range d.views[i].refs {
			if r.nonExisting && r != p && r.surface == p.surface {
				return true
			}
		}
	}
	return false
}

// greyAccel makes the picture's surface mid-grey, when the accelerator can.
func (d *Decoder) greyAccel(p *picture) {
	if f, ok := d.accel.(AccelFiller); ok && p.surface >= 0 {
		if err := f.Fill(p.surface, 128); err != nil && d.accErr == nil {
			d.accErr = err
		}
	}
}

// AccelRef is a reference picture.
type AccelRef struct {
	Surface int
	// FrameIdx is the frame_num of a short-term reference, the
	// LongTermFrameIdx of a long-term one.
	FrameIdx          int
	LongTerm          bool
	TopPOC, BottomPOC int32
}

// AccelPicture is a picture's parameters, the standard's syntax elements.
type AccelPicture struct {
	Surface           int
	TopPOC, BottomPOC int32
	FrameNum          int
	Reference         bool // nal_ref_idc != 0
	// Refs are the reference frames the buffer holds before the picture.
	Refs []AccelRef

	// From the SPS.
	WidthMbs, HeightMbs     int
	NumRefFrames            int
	ChromaFormat            int
	GapsAllowed             bool
	FrameMbsOnly, MBAFF     bool
	Direct8x8Inference      bool
	MinLumaBiPred8x8        bool // level 3.1 and above (A.3.3.2)
	Log2MaxFrameNum         int
	POCType                 int
	Log2MaxPOCLsb           int
	DeltaPicOrderAlwaysZero bool

	// From the PPS.
	PicInitQP, PicInitQS   int
	ChromaQPOffset         [2]int
	CABAC                  bool
	WeightedPred           bool
	WeightedBipredIdc      int
	Transform8x8           bool
	ConstrainedIntraPred   bool
	BottomFieldPicOrder    bool
	DeblockingControl      bool
	RedundantPicCntPresent bool
	// The scaling lists in effect, in raster order (8x8: intra and inter
	// luma).
	Scaling4x4 [6][16]uint8
	Scaling8x8 [2][64]uint8
}

// AccelSlice is a slice: its NAL unit, as in the stream (from its header
// byte, emulation prevention bytes in), and its header.
type AccelSlice struct {
	Data []byte
	// BitOffset is where the slice data starts, in bits from the NAL
	// unit's start, not counting emulation prevention bytes.
	BitOffset      int
	FirstMb        int
	SliceType      int // 0 P, 1 B, 2 I
	DirectSpatial  bool
	NumRefIdx      [2]int
	CabacInitIdc   int
	QPDelta        int
	DisableDeblock int
	AlphaDiv2      int
	BetaDiv2       int
	RefList        [2][]AccelRef

	// Explicit weighted prediction (all zero without).
	LumaLog2Denom, ChromaLog2Denom int
	LumaWeighted, ChromaWeighted   [2]bool // any of the list's weights is explicit
	LumaWeight, LumaOffset         [2][32]int16
	ChromaWeight, ChromaOffset     [2][32][2]int16
}

// SetAccel makes the decoder decode the base view through a. Set before
// decoding.
func (d *Decoder) SetAccel(a Accel) { d.accel = a }

// newAccelPicture makes a picture backed by a surface, without planes.
func (d *Decoder) newAccelPicture(mbW, mbH int) *picture {
	p := &picture{mbW: mbW, mbH: mbH, width: mbW * 16, height: mbH * 16, surface: -1}
	p.cond = sync.NewCond(&p.mu)
	s, err := d.accel.NewSurface(p.width, p.height)
	if err != nil {
		if d.accErr == nil {
			d.accErr = err
		}
		return p
	}
	p.surface = s
	return p
}

func accelRef(p *picture) AccelRef {
	r := AccelRef{Surface: p.surface, FrameIdx: p.frameNum, LongTerm: p.longRef, TopPOC: p.topPOC, BottomPOC: p.botPOC}
	if p.longRef {
		r.FrameIdx = p.longTermFrameIdx
	}
	return r
}

// accelSlice queues a slice of the current picture with its reference
// lists.
func (d *Decoder) accelSlice(h *sliceHeader, lists [2][]*picture, bitPos, hdrLen int) {
	n := len(d.accData)
	d.accData = append(d.accData, d.rawNAL...)
	s := AccelSlice{Data: d.accData[n:], BitOffset: 8*hdrLen + bitPos, FirstMb: h.firstMb, SliceType: h.sliceType,
		DirectSpatial: h.directSpatial, CabacInitIdc: h.cabacInitIdc, QPDelta: h.qpDelta,
		DisableDeblock: h.disableDeblock, AlphaDiv2: h.alphaOffset / 2, BetaDiv2: h.betaOffset / 2}
	for l := range 2 {
		for _, p := range lists[l] {
			s.RefList[l] = append(s.RefList[l], accelRef(p))
		}
		if len(lists[l]) > 0 {
			s.NumRefIdx[l] = h.numRefIdxActive[l]
		}
	}
	if h.hasWeights {
		pw := &h.pw
		s.LumaLog2Denom, s.ChromaLog2Denom = pw.lumaLog2, pw.chromaLog2
		for l := range 2 {
			for i := range min(len(lists[l]), 32) {
				s.LumaWeight[l][i], s.LumaOffset[l][i] = int16(1<<pw.lumaLog2), 0
				if pw.lumaFlag[l][i] {
					s.LumaWeighted[l] = true
					s.LumaWeight[l][i], s.LumaOffset[l][i] = pw.weight[l][i][0], pw.offset[l][i][0]
				}
				for c := range 2 {
					s.ChromaWeight[l][i][c], s.ChromaOffset[l][i][c] = int16(1<<pw.chromaLog2), 0
					if pw.chromaFlag[l][i] {
						s.ChromaWeighted[l] = true
						s.ChromaWeight[l][i][c], s.ChromaOffset[l][i][c] = pw.weight[l][i][1+c], pw.offset[l][i][1+c]
					}
				}
			}
		}
	}
	d.accSlices = append(d.accSlices, s)
}

// accelPicture decodes the current picture's slices queued.
func (d *Decoder) accelPicture(cp *curPic) {
	defer func() {
		d.accSlices = d.accSlices[:0]
		d.accData = d.accData[:0]
		cp.pic.setProgress(cp.pic.mbH)
	}()
	if len(d.accSlices) == 0 || d.accErr != nil {
		return
	}
	// The data may have moved as it grew.
	off := 0
	for i := range d.accSlices {
		s := &d.accSlices[i]
		n := len(s.Data)
		s.Data = d.accData[off : off+n]
		off += n
	}
	h := &cp.hdr
	s, p := h.sps, h.pps
	pic := cp.pic
	ap := &AccelPicture{Surface: pic.surface, TopPOC: pic.topPOC, BottomPOC: pic.botPOC, FrameNum: h.frameNum,
		Reference: h.nal.refIdc != 0,
		WidthMbs:  s.widthMbs, HeightMbs: s.heightMbs(), NumRefFrames: s.maxNumRefFrames, ChromaFormat: s.chromaFormatIdc,
		GapsAllowed: s.gapsAllowed, FrameMbsOnly: s.frameMbsOnly, MBAFF: s.mbaff, Direct8x8Inference: s.direct8x8Inference,
		MinLumaBiPred8x8: s.levelIdc >= 31, Log2MaxFrameNum: int(s.log2MaxFrameNum), POCType: s.pocType,
		Log2MaxPOCLsb: int(s.log2MaxPocLsb), DeltaPicOrderAlwaysZero: s.deltaPicOrderAlwaysZero,
		PicInitQP: p.picInitQP, PicInitQS: p.picInitQS, ChromaQPOffset: p.chromaQPOffset, CABAC: p.cabac,
		WeightedPred: p.weightedPred, WeightedBipredIdc: p.weightedBipredIdc, Transform8x8: p.transform8x8,
		ConstrainedIntraPred: p.constrainedIntraPred, BottomFieldPicOrder: p.bottomFieldPicOrder,
		DeblockingControl: p.deblockingControl, RedundantPicCntPresent: p.redundantPicCntPresent}
	l4, l8 := p.scalingLists(s)
	for i := range l4 {
		for k, v := range l4[i] {
			ap.Scaling4x4[i][zigzag4x4[k]] = v
		}
	}
	for i := range 2 {
		for k, v := range l8[i] {
			ap.Scaling8x8[i][zigzag8x8[k]] = v
		}
	}
	// Short-term references by descending frame_num (wrapped), then
	// long-term by index, as ffmpeg lists them.
	var st, lt []*picture
	for _, r := range d.views[cp.view].refs {
		switch {
		case r.longRef:
			lt = append(lt, r)
		case r.shortRef:
			st = append(st, r)
		}
	}
	maxFrameNum := 1 << s.log2MaxFrameNum
	wrap := func(r *picture) int {
		if r.frameNum > h.frameNum {
			return r.frameNum - maxFrameNum
		}
		return r.frameNum
	}
	sort.SliceStable(st, func(i, j int) bool { return wrap(st[i]) > wrap(st[j]) })
	sort.SliceStable(lt, func(i, j int) bool { return lt[i].longTermFrameIdx < lt[j].longTermFrameIdx })
	for _, r := range append(st, lt...) {
		ap.Refs = append(ap.Refs, accelRef(r))
	}
	if err := d.accel.DecodePicture(ap, d.accSlices); err != nil && d.accErr == nil {
		d.accErr = err
	}
}
