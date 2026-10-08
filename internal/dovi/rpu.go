package dovi

import (
	"errors"
	"fmt"
)

// RPU is a Dolby Vision reference processing unit: the per-picture
// metadata that maps the base layer (and, in profile 7, the enhancement
// layer) to the Dolby Vision signal, and the display management metadata
// that tone maps it to a display. Its syntax is ETSI GS CCM 001's, as
// dovi_tool and ffmpeg read it. Everything is kept, so writing back an RPU
// that was not changed gives the same bytes.
type RPU struct {
	Type   int // rpu_type: 2 is the only one defined
	Format int // rpu_format

	VDRProfile, VDRLevel int // vdr_rpu_profile, vdr_rpu_level

	// The sequence information. A stream's RPUs all carry it in practice;
	// one without it is refused.
	ChromaResamplingExplicitFilter bool
	CoefDataType                   int    // 0: fixed point, 1: float32
	CoefLog2Denom                  uint64 // with fixed point
	NormalizedIdc                  int
	BLFullRange                    bool
	// The rest is present when Format&0x700 == 0.
	BLBitDepthMinus8, ELBitDepthMinus8, VDRBitDepthMinus8 uint64
	SpatialResamplingFilter                               bool
	Reserved3                                             int // 1: compressed display management data
	ELSpatialResamplingFilter                             bool
	DisableResidual                                       bool

	UsePrev bool   // use_prev_vdr_rpu_flag: no mapping of its own
	PrevID  uint64 // prev_vdr_rpu_id, with UsePrev

	Mapping *Mapping // nil with UsePrev
	DM      *DM      // nil when absent

	// Remaining is whatever follows the display management data before the
	// CRC, kept as it is.
	Remaining []byte
}

// Mapping is the RPU's prediction: per component, a piecewise curve from
// the base layer to the Dolby Vision signal, and for profile 7 the
// enhancement layer's non-linear quantisation.
type Mapping struct {
	VDRRPUID, ColorSpace, ChromaFormat uint64
	Curves                             [3]Curve
	XPartitionsMinus1                  uint64
	YPartitionsMinus1                  uint64
	NLQ                                *NLQ // with an enhancement layer
}

// Curve is one component's mapping: the pivots (as written, in the base
// layer's bit depth) and a piece between each pair.
type Curve struct {
	Pivots []uint64
	Pieces []Piece
}

// Piece is one segment of a curve: a polynomial, or a multivariate
// multiple regression (MMR) over all three components.
type Piece struct {
	MappingIdc uint64 // 0: polynomial, 1: MMR

	PolyOrderMinus1 uint64
	LinearInterp    bool
	PolyCoefInt     []int64
	PolyCoef        []uint64

	MMROrderMinus1 int
	MMRConstInt    int64
	MMRConst       uint64
	MMRCoefInt     [][7]int64
	MMRCoef        [][7]uint64
}

// NLQ is the enhancement layer's non-linear quantisation (linear dead zone).
type NLQ struct {
	Method     int
	PredPivots [2]uint64
	Offset     [3]uint64
	InMaxInt   [3]uint64
	InMax      [3]uint64
	SlopeInt   [3]uint64
	Slope      [3]uint64
	ThreshInt  [3]uint64
	Thresh     [3]uint64
}

// MEL reports whether the quantisation is the minimal enhancement layer's:
// a residual that adds nothing.
func (n *NLQ) MEL() bool {
	for c := range 3 {
		if n.Offset[c] != 0 || n.InMaxInt[c] != 1 || n.InMax[c] != 0 || n.SlopeInt[c] != 0 ||
			n.Slope[c] != 0 || n.ThreshInt[c] != 0 || n.Thresh[c] != 0 {
			return false
		}
	}
	return true
}

// DM is the display management metadata.
type DM struct {
	AffectedID, CurrentID, SceneRefresh uint64
	// Compressed data (Reserved3 == 1) stops at the identifiers.
	Compressed bool

	YCCToRGB       [9]int16
	YCCToRGBOffset [3]uint32
	RGBToLMS       [9]int16
	EOTF           uint16
	EOTFParam0     uint16
	EOTFParam1     uint16
	EOTFParam2     uint32
	SignalBitDepth int
	ColorSpace     int
	ChromaFormat   int
	FullRange      int
	SourceMinPQ    int
	SourceMaxPQ    int
	SourceDiagonal int

	CMv29 []ExtBlock
	CMv40 []ExtBlock // nil when the RPU has no CM v4.0 data
	HasV4 bool
}

// ExtBlock is an extension metadata block (L1 to L11, L254, L255), kept as
// its payload.
type ExtBlock struct {
	Level   byte
	Payload []byte // ext_block_length bytes
}

const rpuPrefix = 0x19

// crcMPEG2 is CRC-32/MPEG-2: the IEEE polynomial, not reflected.
func crcMPEG2(b []byte) uint32 {
	c := uint32(0xffffffff)
	for _, x := range b {
		c = c<<8 ^ mpeg2Table[byte(c>>24)^x]
	}
	return c
}

var mpeg2Table = func() (t [256]uint32) {
	for i := range t {
		c := uint32(i) << 24 //nolint:gosec // a byte
		for range 8 {
			if c&0x80000000 != 0 {
				c = c<<1 ^ 0x04c11db7
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return t
}()

// ParseNAL reads an RPU from its HEVC NAL unit (type 62, with its header,
// without start code).
func ParseNAL(nal []byte) (*RPU, error) {
	if len(nal) < 3 || nal[0]>>1&0x3f != NALRPU {
		return nil, errors.New("dovi: not an RPU NAL unit")
	}
	return Parse(unescape(nal[2:]))
}

// Parse reads an RPU from its payload (from the 0x19 prefix, emulation
// prevention removed).
func Parse(b []byte) (*RPU, error) {
	b = trimZeros(b)
	if len(b) < 7 || b[0] != rpuPrefix || b[len(b)-1] != 0x80 {
		return nil, errors.New("dovi: not an RPU")
	}
	crcAt := len(b) - 5
	if got, want := crcMPEG2(b[1:crcAt]), uint32(b[crcAt])<<24|uint32(b[crcAt+1])<<16|uint32(b[crcAt+2])<<8|uint32(b[crcAt+3]); got != want {
		return nil, fmt.Errorf("dovi: RPU CRC %08x, the data's is %08x", want, got)
	}
	r := &bitReader{b: b, pos: 8}
	u := &RPU{}
	if err := u.parse(r); err != nil {
		return nil, err
	}
	return u, nil
}

const crcBits = 40 // the CRC and the final 0x80

func (u *RPU) parse(r *bitReader) error {
	u.Type = int(r.u(6))
	u.Format = int(r.u(11))
	if u.Type != 2 {
		return fmt.Errorf("dovi: RPU type %d", u.Type)
	}
	u.VDRProfile, u.VDRLevel = int(r.u(4)), int(r.u(4))
	if !r.flag() {
		return errors.New("dovi: an RPU without sequence information")
	}
	u.ChromaResamplingExplicitFilter = r.flag()
	u.CoefDataType = int(r.u(2))
	switch u.CoefDataType {
	case 0:
		u.CoefLog2Denom = r.ue()
	case 1:
	default:
		return fmt.Errorf("dovi: coefficient data type %d", u.CoefDataType)
	}
	u.NormalizedIdc = int(r.u(2))
	u.BLFullRange = r.flag()
	if u.Format&0x700 == 0 {
		u.BLBitDepthMinus8, u.ELBitDepthMinus8, u.VDRBitDepthMinus8 = r.ue(), r.ue(), r.ue()
		u.SpatialResamplingFilter = r.flag()
		u.Reserved3 = int(r.u(3))
		u.ELSpatialResamplingFilter = r.flag()
		u.DisableResidual = r.flag()
	}
	dm := r.flag()
	u.UsePrev = r.flag()
	if u.UsePrev {
		u.PrevID = r.ue()
	} else {
		m, err := u.parseMapping(r)
		if err != nil {
			return err
		}
		u.Mapping = m
	}
	if dm {
		u.DM = u.parseDM(r)
	}
	r.align()
	if r.err != nil {
		return r.err
	}
	if n := r.left() - crcBits; n > 0 {
		u.Remaining = append([]byte(nil), r.b[r.pos/8:r.pos/8+n/8]...)
	} else if n < 0 {
		return errShort
	}
	return nil
}

func (u *RPU) coefBits() int {
	if u.CoefDataType == 0 {
		return int(u.CoefLog2Denom) //nolint:gosec // small
	}
	return 32
}

func (u *RPU) blDepth() int { return int(u.BLBitDepthMinus8) + 8 } //nolint:gosec // small

// residual reports whether the RPU carries the enhancement layer's
// quantisation.
func (u *RPU) residual() bool { return u.Format&0x700 == 0 && !u.DisableResidual }

func (u *RPU) parseMapping(r *bitReader) (*Mapping, error) {
	m := &Mapping{VDRRPUID: r.ue(), ColorSpace: r.ue(), ChromaFormat: r.ue()}
	depth, cb := u.blDepth(), u.coefBits()
	for c := range m.Curves {
		n := r.ue() + 2
		if n > 9 || r.err != nil {
			return nil, fmt.Errorf("dovi: %d pivots", n)
		}
		for range n {
			m.Curves[c].Pivots = append(m.Curves[c].Pivots, r.u(depth))
		}
	}
	if u.residual() {
		m.NLQ = &NLQ{Method: int(r.u(3))}
		if m.NLQ.Method != 0 {
			return nil, fmt.Errorf("dovi: NLQ method %d", m.NLQ.Method)
		}
		m.NLQ.PredPivots = [2]uint64{r.u(depth), r.u(depth)}
	}
	m.XPartitionsMinus1, m.YPartitionsMinus1 = r.ue(), r.ue()
	for c := range m.Curves {
		cv := &m.Curves[c]
		for range len(cv.Pivots) - 1 {
			p := Piece{MappingIdc: r.ue()}
			switch p.MappingIdc {
			case 0:
				p.PolyOrderMinus1 = r.ue()
				if p.PolyOrderMinus1 > 1 {
					return nil, fmt.Errorf("dovi: polynomial order %d", p.PolyOrderMinus1+1)
				}
				if p.PolyOrderMinus1 == 0 {
					p.LinearInterp = r.flag()
				}
				if p.LinearInterp {
					return nil, errors.New("dovi: linear interpolation is not supported")
				}
				for range p.PolyOrderMinus1 + 2 {
					var i int64
					if u.CoefDataType == 0 {
						i = r.se()
					}
					p.PolyCoefInt = append(p.PolyCoefInt, i)
					p.PolyCoef = append(p.PolyCoef, r.u(cb))
				}
			case 1:
				p.MMROrderMinus1 = int(r.u(2))
				if u.CoefDataType == 0 {
					p.MMRConstInt = r.se()
				}
				p.MMRConst = r.u(cb)
				for range p.MMROrderMinus1 + 1 {
					var ci [7]int64
					var cf [7]uint64
					for k := range 7 {
						if u.CoefDataType == 0 {
							ci[k] = r.se()
						}
						cf[k] = r.u(cb)
					}
					p.MMRCoefInt, p.MMRCoef = append(p.MMRCoefInt, ci), append(p.MMRCoef, cf)
				}
			default:
				return nil, fmt.Errorf("dovi: mapping method %d", p.MappingIdc)
			}
			cv.Pieces = append(cv.Pieces, p)
		}
	}
	if q := m.NLQ; q != nil {
		el := int(u.ELBitDepthMinus8&0xff) + 8 //nolint:gosec // masked
		for c := range 3 {
			q.Offset[c] = r.u(el)
			if u.CoefDataType == 0 {
				q.InMaxInt[c] = r.ue()
			}
			q.InMax[c] = r.u(cb)
			if u.CoefDataType == 0 {
				q.SlopeInt[c] = r.ue()
			}
			q.Slope[c] = r.u(cb)
			if u.CoefDataType == 0 {
				q.ThreshInt[c] = r.ue()
			}
			q.Thresh[c] = r.u(cb)
		}
	}
	return m, r.err
}

func (u *RPU) parseDM(r *bitReader) *DM {
	d := &DM{AffectedID: r.ue(), CurrentID: r.ue(), SceneRefresh: r.ue(), Compressed: u.Reserved3 == 1}
	if !d.Compressed {
		for i := range d.YCCToRGB {
			d.YCCToRGB[i] = int16(r.u(16)) //nolint:gosec // 16 bits, two's complement
		}
		for i := range d.YCCToRGBOffset {
			d.YCCToRGBOffset[i] = uint32(r.u(32)) //nolint:gosec // 32 bits
		}
		for i := range d.RGBToLMS {
			d.RGBToLMS[i] = int16(r.u(16)) //nolint:gosec // 16 bits, two's complement
		}
		d.EOTF, d.EOTFParam0, d.EOTFParam1 = uint16(r.u(16)), uint16(r.u(16)), uint16(r.u(16)) //nolint:gosec // 16 bits
		d.EOTFParam2 = uint32(r.u(32))                                                         //nolint:gosec // 32 bits
		d.SignalBitDepth, d.ColorSpace, d.ChromaFormat, d.FullRange = int(r.u(5)), int(r.u(2)), int(r.u(2)), int(r.u(2))
		d.SourceMinPQ, d.SourceMaxPQ, d.SourceDiagonal = int(r.u(12)), int(r.u(12)), int(r.u(10))
	}
	d.CMv29 = parseExtBlocks(r)
	if r.left() >= 56 {
		d.HasV4 = true
		d.CMv40 = parseExtBlocks(r)
	}
	return d
}

func parseExtBlocks(r *bitReader) []ExtBlock {
	n := r.ue()
	r.align()
	var out []ExtBlock
	for i := uint64(0); i < n && r.err == nil; i++ {
		l := r.ue()
		if l > 1024 {
			r.err = fmt.Errorf("dovi: extension block of %d bytes", l)
			return nil
		}
		b := ExtBlock{Level: byte(r.u(8))}
		for range l {
			b.Payload = append(b.Payload, byte(r.u(8)))
		}
		out = append(out, b)
	}
	return out
}

// Bytes is the RPU's payload, from the 0x19 prefix to the final 0x80, with
// its CRC computed.
func (u *RPU) Bytes() []byte {
	w := &bitWriter{}
	w.u(8, rpuPrefix)
	w.u(6, uint64(u.Type))    //nolint:gosec // small
	w.u(11, uint64(u.Format)) //nolint:gosec // small
	w.u(4, uint64(u.VDRProfile))
	w.u(4, uint64(u.VDRLevel))
	w.flag(true) // vdr_seq_info_present_flag
	w.flag(u.ChromaResamplingExplicitFilter)
	w.u(2, uint64(u.CoefDataType)) //nolint:gosec // small
	if u.CoefDataType == 0 {
		w.ue(u.CoefLog2Denom)
	}
	w.u(2, uint64(u.NormalizedIdc)) //nolint:gosec // small
	w.flag(u.BLFullRange)
	if u.Format&0x700 == 0 {
		w.ue(u.BLBitDepthMinus8)
		w.ue(u.ELBitDepthMinus8)
		w.ue(u.VDRBitDepthMinus8)
		w.flag(u.SpatialResamplingFilter)
		w.u(3, uint64(u.Reserved3)) //nolint:gosec // small
		w.flag(u.ELSpatialResamplingFilter)
		w.flag(u.DisableResidual)
	}
	w.flag(u.DM != nil)
	w.flag(u.UsePrev)
	if u.UsePrev {
		w.ue(u.PrevID)
	} else if u.Mapping != nil {
		u.writeMapping(w)
	}
	if u.DM != nil {
		u.writeDM(w)
	}
	w.align()
	for _, c := range u.Remaining {
		w.u(8, uint64(c))
	}
	c := crcMPEG2(w.b[1:])
	w.u(32, uint64(c))
	w.u(8, 0x80)
	return w.b
}

// NAL is the RPU as an HEVC NAL unit (type 62), without start code.
func (u *RPU) NAL() []byte {
	return append([]byte{NALRPU << 1, 1}, escape(u.Bytes())...)
}

func (u *RPU) writeMapping(w *bitWriter) {
	m := u.Mapping
	depth, cb := u.blDepth(), u.coefBits()
	w.ue(m.VDRRPUID)
	w.ue(m.ColorSpace)
	w.ue(m.ChromaFormat)
	for _, cv := range m.Curves {
		w.ue(uint64(len(cv.Pivots) - 2)) //nolint:gosec // at least 2
		for _, p := range cv.Pivots {
			w.u(depth, p)
		}
	}
	if u.residual() && m.NLQ != nil {
		w.u(3, uint64(m.NLQ.Method)) //nolint:gosec // small
		w.u(depth, m.NLQ.PredPivots[0])
		w.u(depth, m.NLQ.PredPivots[1])
	}
	w.ue(m.XPartitionsMinus1)
	w.ue(m.YPartitionsMinus1)
	for _, cv := range m.Curves {
		for _, p := range cv.Pieces {
			w.ue(p.MappingIdc)
			switch p.MappingIdc {
			case 0:
				w.ue(p.PolyOrderMinus1)
				if p.PolyOrderMinus1 == 0 {
					w.flag(p.LinearInterp)
				}
				for i := range p.PolyCoef {
					if u.CoefDataType == 0 {
						w.se(p.PolyCoefInt[i])
					}
					w.u(cb, p.PolyCoef[i])
				}
			case 1:
				w.u(2, uint64(p.MMROrderMinus1)) //nolint:gosec // small
				if u.CoefDataType == 0 {
					w.se(p.MMRConstInt)
				}
				w.u(cb, p.MMRConst)
				for j := range p.MMRCoef {
					for k := range 7 {
						if u.CoefDataType == 0 {
							w.se(p.MMRCoefInt[j][k])
						}
						w.u(cb, p.MMRCoef[j][k])
					}
				}
			}
		}
	}
	if q := m.NLQ; q != nil && u.residual() {
		el := int(u.ELBitDepthMinus8&0xff) + 8 //nolint:gosec // masked
		for c := range 3 {
			w.u(el, q.Offset[c])
			if u.CoefDataType == 0 {
				w.ue(q.InMaxInt[c])
			}
			w.u(cb, q.InMax[c])
			if u.CoefDataType == 0 {
				w.ue(q.SlopeInt[c])
			}
			w.u(cb, q.Slope[c])
			if u.CoefDataType == 0 {
				w.ue(q.ThreshInt[c])
			}
			w.u(cb, q.Thresh[c])
		}
	}
}

func (u *RPU) writeDM(w *bitWriter) {
	d := u.DM
	w.ue(d.AffectedID)
	w.ue(d.CurrentID)
	w.ue(d.SceneRefresh)
	if !d.Compressed {
		for _, v := range d.YCCToRGB {
			w.u(16, uint64(uint16(v)))
		}
		for _, v := range d.YCCToRGBOffset {
			w.u(32, uint64(v))
		}
		for _, v := range d.RGBToLMS {
			w.u(16, uint64(uint16(v)))
		}
		w.u(16, uint64(d.EOTF))
		w.u(16, uint64(d.EOTFParam0))
		w.u(16, uint64(d.EOTFParam1))
		w.u(32, uint64(d.EOTFParam2))
		w.u(5, uint64(d.SignalBitDepth))  //nolint:gosec // small
		w.u(2, uint64(d.ColorSpace))      //nolint:gosec // small
		w.u(2, uint64(d.ChromaFormat))    //nolint:gosec // small
		w.u(2, uint64(d.FullRange))       //nolint:gosec // small
		w.u(12, uint64(d.SourceMinPQ))    //nolint:gosec // small
		w.u(12, uint64(d.SourceMaxPQ))    //nolint:gosec // small
		w.u(10, uint64(d.SourceDiagonal)) //nolint:gosec // small
	}
	writeExtBlocks(w, d.CMv29)
	if d.HasV4 {
		writeExtBlocks(w, d.CMv40)
	}
}

func writeExtBlocks(w *bitWriter, blocks []ExtBlock) {
	w.ue(uint64(len(blocks)))
	w.align()
	for _, b := range blocks {
		w.ue(uint64(len(b.Payload)))
		w.u(8, uint64(b.Level))
		for _, c := range b.Payload {
			w.u(8, uint64(c))
		}
	}
}

// Profile is the Dolby Vision profile the RPU describes: 5, 7 or 8 (4 for
// the old 8-bit-enhancement profile), 0 when it cannot tell.
func (u *RPU) Profile() int {
	switch u.VDRProfile {
	case 0:
		if u.BLFullRange {
			return 5
		}
	case 1:
		if u.ELSpatialResamplingFilter && !u.DisableResidual {
			if u.VDRBitDepthMinus8 == 4 {
				return 7
			}
			return 4
		}
		return 8
	}
	return 0
}

// FEL reports whether the RPU goes with a full enhancement layer: one whose
// residual changes the picture.
func (u *RPU) FEL() bool {
	return u.Mapping != nil && u.Mapping.NLQ != nil && !u.Mapping.NLQ.MEL()
}

// ToProfile81 makes a profile 7 (or 8) RPU one for profile 8.1: the base
// layer alone, HDR10 compatible. The enhancement layer's quantisation goes;
// with a full enhancement layer the mapping becomes the identity, as the
// base layer alone is the picture (with a minimal one the mapping is kept:
// it was made for the base layer). The display management data's colour
// matrices become profile 8.1's, BT.2020. This is dovi_tool's mode 2.
func (u *RPU) ToProfile81() error {
	switch p := u.Profile(); p {
	case 7, 8:
	default:
		return fmt.Errorf("dovi: cannot make profile %d into 8.1", p)
	}
	fel := u.FEL()
	u.ELSpatialResamplingFilter, u.DisableResidual = false, true
	if m := u.Mapping; m != nil {
		m.NLQ = nil
		m.XPartitionsMinus1, m.YPartitionsMinus1 = 0, 0
		if fel {
			top := uint64(1)<<u.blDepth() - 1
			for c := range m.Curves {
				m.Curves[c] = Curve{Pivots: []uint64{0, top},
					Pieces: []Piece{{PolyCoefInt: []int64{0, 1}, PolyCoef: []uint64{0, 0}}}}
			}
		}
	}
	if d := u.DM; d != nil {
		d.YCCToRGB = [9]int16{9574, 0, 13802, 9574, -1540, -5348, 9574, 17610, 0}
		d.YCCToRGBOffset = [3]uint32{16777216, 134217728, 134217728}
		d.RGBToLMS = [9]int16{7222, 8771, 390, 2654, 12430, 1300, 0, 422, 15962}
		d.ColorSpace = 0
	}
	return nil
}

// unescape removes emulation prevention bytes.
func unescape(b []byte) []byte {
	out := make([]byte, 0, len(b))
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return out
}

// escape inserts emulation prevention bytes.
func escape(b []byte) []byte {
	out := make([]byte, 0, len(b)+len(b)/64)
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return out
}
