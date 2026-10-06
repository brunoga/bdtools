package mkv

import (
	"errors"
	"fmt"
)

// paramSets collects the parameter sets of a stream, raw (as NAL units) and
// as much parsed as the timing and the codec private data need.
type paramSets struct {
	fpsNum, fpsDen int
	width, height  int

	// H.264
	sps     map[uint32]*h264SPS
	pps     map[uint32]*h264PPS
	rawSPS  map[uint32][]byte
	rawPPS  map[uint32][]byte
	spsList []uint32
	ppsList []uint32

	// HEVC
	hvps    map[uint32][]byte
	hsps    map[uint32]*hevcSPS
	hpps    map[uint32]*hevcPPS
	hrawSPS map[uint32][]byte
	hrawPPS map[uint32][]byte
	hvpsIDs []uint32
	hspsIDs []uint32
	hppsIDs []uint32
}

func (p *paramSets) complete(c Codec) bool {
	if c == HEVC {
		return len(p.hvps) > 0 && len(p.hsps) > 0 && len(p.hpps) > 0
	}
	return len(p.sps) > 0 && len(p.pps) > 0
}

func remember(ids *[]uint32, id uint32) {
	for _, x := range *ids {
		if x == id {
			return
		}
	}
	*ids = append(*ids, id)
}

// --- H.264 -----------------------------------------------------------------------

type h264SPS struct {
	profile, constraints, level byte
	chromaFormat                uint32
	bitDepthLuma, bitDepthChrom uint32
	log2MaxFrameNum             int
	pocType                     uint32
	log2MaxPOCLsb               int
	deltaAlwaysZero             bool
	offsetNonRef, offsetTopBot  int32
	refOffsets                  []int32
	frameMbsOnly                bool
	separateColour              bool
}

type h264PPS struct {
	sps            uint32
	bottomFieldPOC bool
}

func (p *paramSets) addH264SPS(nal []byte) {
	r := &bitReader{b: rbsp(nal[1:])}
	s := &h264SPS{chromaFormat: 1}
	s.profile = byte(r.u(8))
	s.constraints = byte(r.u(8))
	s.level = byte(r.u(8))
	id := r.ue()
	switch s.profile {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		s.chromaFormat = r.ue()
		if s.chromaFormat == 3 {
			s.separateColour = r.flag()
		}
		s.bitDepthLuma = r.ue()
		s.bitDepthChrom = r.ue()
		r.u(1) // qpprime_y_zero_transform_bypass
		if r.flag() {
			n := 8
			if s.chromaFormat == 3 {
				n = 12
			}
			for i := 0; i < n; i++ {
				if r.flag() {
					size := 16
					if i >= 6 {
						size = 64
					}
					last, next := int32(8), int32(8)
					for j := 0; j < size; j++ {
						if next != 0 {
							next = (last + r.se() + 256) % 256
						}
						if next != 0 {
							last = next
						}
					}
				}
			}
		}
	}
	s.log2MaxFrameNum = int(r.ue()) + 4
	s.pocType = r.ue()
	switch s.pocType {
	case 0:
		s.log2MaxPOCLsb = int(r.ue()) + 4
	case 1:
		s.deltaAlwaysZero = r.flag()
		s.offsetNonRef = r.se()
		s.offsetTopBot = r.se()
		n := r.ue()
		for i := uint32(0); i < n && i < 256; i++ {
			s.refOffsets = append(s.refOffsets, r.se())
		}
	}
	r.ue() // max_num_ref_frames
	r.u(1) // gaps
	w := r.ue() + 1
	h := r.ue() + 1
	s.frameMbsOnly = r.flag()
	if !s.frameMbsOnly {
		r.u(1)
	}
	r.u(1) // direct_8x8_inference
	cl, cr, ct, cb := uint32(0), uint32(0), uint32(0), uint32(0)
	if r.flag() {
		cl, cr, ct, cb = r.ue(), r.ue(), r.ue(), r.ue()
	}
	if r.bad {
		return
	}
	if p.sps == nil {
		p.sps, p.rawSPS = map[uint32]*h264SPS{}, map[uint32][]byte{}
	}
	p.sps[id] = s
	p.rawSPS[id] = append([]byte(nil), nal...)
	remember(&p.spsList, id)
	mul := uint32(2)
	if s.frameMbsOnly {
		mul = 1
	}
	cropX, cropY := uint32(2), 2*mul
	if s.chromaFormat == 0 || s.separateColour {
		cropX, cropY = 1, mul
	}
	p.width = int(w*16 - cropX*(cl+cr))
	p.height = int(h*16*mul - cropY*(ct+cb))
}

func (p *paramSets) addH264PPS(nal []byte) {
	r := &bitReader{b: rbsp(nal[1:])}
	id := r.ue()
	pp := &h264PPS{sps: r.ue()}
	r.u(1) // entropy_coding_mode
	pp.bottomFieldPOC = r.flag()
	if r.bad {
		return
	}
	if p.pps == nil {
		p.pps, p.rawPPS = map[uint32]*h264PPS{}, map[uint32][]byte{}
	}
	p.pps[id] = pp
	p.rawPPS[id] = append([]byte(nil), nal...)
	remember(&p.ppsList, id)
}

// pocState is the H.264 picture order count derivation (8.2.1), for frames.
type pocState struct {
	prevMsb, prevLsb   int32
	prevFrameNum       uint32
	prevFrameNumOffset int32
	havePrev           bool
}

// h264 returns a picture's POC and whether it is an IDR picture.
func (st *pocState) h264(nal []byte, p *paramSets) (int32, bool, error) {
	refIdc := nal[0] >> 5 & 3
	idr := nal[0]&0x1f == 5
	r := &bitReader{b: rbsp(nal[1:])}
	r.ue() // first_mb_in_slice
	r.ue() // slice_type
	pp := p.pps[r.ue()]
	if pp == nil {
		return 0, idr, errors.New("mkv: slice refers to an unknown PPS")
	}
	s := p.sps[pp.sps]
	if s == nil {
		return 0, idr, errors.New("mkv: PPS refers to an unknown SPS")
	}
	if s.separateColour {
		r.u(2)
	}
	frameNum := r.u(s.log2MaxFrameNum)
	field := false
	if !s.frameMbsOnly {
		field = r.flag()
		if field {
			r.u(1)
		}
	}
	if idr {
		r.ue() // idr_pic_id
	}
	var poc int32
	switch s.pocType {
	case 0:
		lsb := int32(r.u(s.log2MaxPOCLsb))
		var deltaBottom int32
		if pp.bottomFieldPOC && !field {
			deltaBottom = r.se()
		}
		if idr {
			st.prevMsb, st.prevLsb = 0, 0
		}
		max := int32(1) << s.log2MaxPOCLsb
		msb := st.prevMsb
		switch {
		case lsb < st.prevLsb && st.prevLsb-lsb >= max/2:
			msb += max
		case lsb > st.prevLsb && lsb-st.prevLsb > max/2:
			msb -= max
		}
		top := msb + lsb
		poc = top
		if deltaBottom < 0 {
			poc = top + deltaBottom
		}
		if refIdc != 0 {
			st.prevMsb, st.prevLsb = msb, lsb
		}
	case 1, 2:
		var d0, d1 int32
		if s.pocType == 1 && !s.deltaAlwaysZero {
			d0 = r.se()
			if pp.bottomFieldPOC && !field {
				d1 = r.se()
			}
		}
		maxFN := int32(1) << s.log2MaxFrameNum
		offset := int32(0)
		if !idr {
			offset = st.prevFrameNumOffset
			if st.havePrev && frameNum < st.prevFrameNum {
				offset += maxFN
			}
		}
		absFN := offset + int32(frameNum)
		if s.pocType == 2 {
			switch {
			case idr:
				poc = 0
			case refIdc == 0:
				poc = 2*absFN - 1
			default:
				poc = 2 * absFN
			}
		} else {
			n := int32(len(s.refOffsets))
			var expected int32
			if n > 0 {
				if refIdc == 0 && absFN > 0 {
					absFN--
				}
				if absFN > 0 {
					var delta int32
					for _, o := range s.refOffsets {
						delta += o
					}
					cycles := (absFN - 1) / n
					inCycle := (absFN - 1) % n
					expected = cycles * delta
					for i := int32(0); i <= inCycle; i++ {
						expected += s.refOffsets[i]
					}
				}
			}
			if refIdc == 0 {
				expected += s.offsetNonRef
			}
			top := expected + d0
			bottom := top + s.offsetTopBot + d1
			poc = min(top, bottom)
		}
		st.prevFrameNumOffset = offset
		st.prevFrameNum = frameNum
		st.havePrev = true
	default:
		return 0, idr, fmt.Errorf("mkv: unknown POC type %d", s.pocType)
	}
	if r.bad {
		return 0, idr, errors.New("mkv: truncated slice header")
	}
	return poc, idr, nil
}

// avcC is the H.264 codec private data (ISO/IEC 14496-15).
func (p *paramSets) avcC() ([]byte, error) {
	if len(p.spsList) == 0 || len(p.ppsList) == 0 {
		return nil, errors.New("mkv: no SPS/PPS")
	}
	first := p.sps[p.spsList[0]]
	b := []byte{1, first.profile, first.constraints, first.level, 0xff, 0xe0 | byte(len(p.spsList))}
	for _, id := range p.spsList {
		raw := p.rawSPS[id]
		b = append(b, byte(len(raw)>>8), byte(len(raw)))
		b = append(b, raw...)
	}
	b = append(b, byte(len(p.ppsList)))
	for _, id := range p.ppsList {
		raw := p.rawPPS[id]
		b = append(b, byte(len(raw)>>8), byte(len(raw)))
		b = append(b, raw...)
	}
	switch first.profile {
	case 100, 110, 122, 144:
		b = append(b, 0xfc|byte(first.chromaFormat&3), 0xf8|byte(first.bitDepthLuma&7), 0xf8|byte(first.bitDepthChrom&7), 0)
	}
	return b, nil
}

// raw returns the parameter sets as NAL units, in the order a decoder needs.
func (p *paramSets) raw(c Codec) [][]byte {
	var out [][]byte
	if c == HEVC {
		for _, id := range p.hvpsIDs {
			out = append(out, p.hvps[id])
		}
		for _, id := range p.hspsIDs {
			out = append(out, p.hrawSPS[id])
		}
		for _, id := range p.hppsIDs {
			out = append(out, p.hrawPPS[id])
		}
		return out
	}
	for _, id := range p.spsList {
		out = append(out, p.rawSPS[id])
	}
	for _, id := range p.ppsList {
		out = append(out, p.rawPPS[id])
	}
	return out
}

func (p *paramSets) codecPrivate(c Codec) ([]byte, error) {
	if c == HEVC {
		return p.hvcC()
	}
	return p.avcC()
}
