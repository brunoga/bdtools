package vc1

// Frame coding modes.
const (
	progressive = 0
	ilaceFrame  = 1
	ilaceField  = 2
)

// Picture types.
const (
	picI = iota
	picP
	picB
	picBI
)

// MV modes.
const (
	mvHpelBilin = iota // 1MV half-pel bilinear
	mv1MV              // 1MV quarter-pel bicubic
	mvHpel             // 1MV half-pel bicubic
	mvMixed            // mixed 1MV and 4MV
	mvIntensity        // intensity compensation, then the mode in mvMode2
)

// Quantizer modes.
const (
	quantImplicit = iota
	quantExplicit
	quantNonUniform
	quantUniform
)

// DQUANT profiles.
const (
	dqFourEdges = iota
	dqDoubleEdges
	dqSingleEdge
	dqAllMBs
)

// Transform types.
const (
	tt8x8 = iota
	tt8x4Bottom
	tt8x4Top
	tt8x4
	tt4x8Right
	tt4x8Left
	tt4x8
	tt4x4
)

// Conditional overlap.
const (
	condOverNone = iota
	condOverAll
	condOverSelect
)

// seqHeader is an Advanced Profile sequence header.
type seqHeader struct {
	level                int
	postprocFlag         bool
	maxWidth, maxHeight  int
	broadcast, interlace bool
	tfcntrFlag, finterp  bool
	psf                  bool
	rateNum, rateDen     int
	prim, trc, matrix    int
	hrdBuckets           int
}

// entryPoint is an entry point header.
type entryPoint struct {
	brokenLink, closedEntry bool
	panScan, refDistFlag    bool
	loopFilter, fastUVMC    bool
	extendedMV              bool
	dquant                  int
	vsTransform, overlap    bool
	quantizerMode           int
	width, height           int
	extendedDMV             bool
	rangeMapY, rangeMapUV   int // -1: none
}

func (d *Decoder) sequenceHeader(r *bits) error {
	if r.u(2) != 3 {
		return errStream // only Advanced Profile carries sequence headers in-band
	}
	s := seqHeader{rateNum: 0, rateDen: 0, prim: 2, trc: 2, matrix: 2}
	s.level = r.u(3)
	if r.u(2) != 1 {
		return errUnsupported("a chroma format other than 4:2:0")
	}
	r.skip(3 + 5) // FRMRTQ_POSTPROC, BITRTQ_POSTPROC
	s.postprocFlag = r.flag()
	s.maxWidth = (r.u(12) + 1) << 1
	s.maxHeight = (r.u(12) + 1) << 1
	s.broadcast = r.flag()
	s.interlace = r.flag()
	s.tfcntrFlag = r.flag()
	s.finterp = r.flag()
	r.skip(1)
	s.psf = r.flag()
	if r.flag() { // display extension
		r.skip(14 + 14)
		if r.flag() {
			if r.u(4) == 15 {
				r.skip(16)
			}
		}
		if r.flag() { // frame rate
			if r.flag() {
				s.rateNum, s.rateDen = r.u(16)+1, 32
			} else {
				nr, dr := r.u(8), r.u(4)
				if nr > 0 && nr < 8 && dr > 0 && dr < 3 {
					s.rateNum = [7]int{24, 25, 30, 50, 60, 48, 72}[nr-1] * 1000
					s.rateDen = [2]int{1000, 1001}[dr-1]
				}
			}
		}
		if r.flag() { // colour format
			s.prim, s.trc, s.matrix = r.u(8), r.u(8), r.u(8)
		}
	}
	if r.flag() { // HRD parameters
		s.hrdBuckets = r.u(5)
		r.skip(8)
		r.skip(32 * s.hrdBuckets)
	}
	if r.left() < 0 {
		return errShort
	}
	d.seq, d.haveSeq = s, true
	return nil
}

func (d *Decoder) entryPointHeader(r *bits) error {
	if !d.haveSeq {
		return nil
	}
	e := entryPoint{rangeMapY: -1, rangeMapUV: -1}
	e.brokenLink = r.flag()
	e.closedEntry = r.flag()
	e.panScan = r.flag()
	e.refDistFlag = r.flag()
	e.loopFilter = r.flag()
	e.fastUVMC = r.flag()
	e.extendedMV = r.flag()
	e.dquant = r.u(2)
	e.vsTransform = r.flag()
	e.overlap = r.flag()
	e.quantizerMode = r.u(2)
	r.skip(8 * d.seq.hrdBuckets)
	if r.flag() {
		e.width = (r.u(12) + 1) << 1
		e.height = (r.u(12) + 1) << 1
	} else {
		e.width, e.height = d.seq.maxWidth, d.seq.maxHeight
	}
	if e.extendedMV {
		e.extendedDMV = r.flag()
	}
	if r.flag() {
		e.rangeMapY = r.u(3)
	}
	if r.flag() {
		e.rangeMapUV = r.u(3)
	}
	if r.left() < 0 {
		return errShort
	}
	d.ep, d.haveEP = e, true
	return nil
}

// picHeader is a picture (or field) header.
type picHeader struct {
	fcm           int
	fieldMode     bool
	fptype        int
	typ           int // picI, picP, picB, picBI
	skipped       bool
	tff, rff      bool
	rptfrm        int
	rnd           bool
	uvsamp        bool
	refdist       int
	bfraction     int
	frfd, brfd    int
	interpfrm     bool
	pqindex, pq   int
	halfpq        bool
	pquantizer    bool
	postproc      int
	condover      int
	numref        bool
	reffield      int
	mvrange       int
	dmvrange      int
	kx, ky        int
	rangeX        int
	rangeY        int
	fourMVSwitch  bool
	intcomp       bool
	lumscale      int
	lumshift      int
	lumscale2     int
	lumshift2     int
	intcompField  int
	mvMode        int
	mvMode2       int
	quarter       bool // quarter-pel vectors
	mspel         bool // bicubic interpolation
	mvTable       int
	cbpTable      int
	mbModeTable   int
	imvTable      int
	icbpTable     int
	twoMVBPTable  int
	fourMVBPTable int
	ttIndex       int
	dquantFrm     bool
	dqProfile     int
	dqSBEdge      int
	dqBilevel     bool
	altpq         int
	ttmbf         bool
	ttfrm         int
	cACTable      int
	yACTable      int
	dcTable       int
}

// bfractions are BFRACTION's values, of 256.
var bfractions = [23]int{
	128, 85, 170, 64, 192, 51, 102, 153, 204, 43, 215,
	37, 74, 111, 148, 185, 222, 32, 96, 160, 224, -1, 0,
}

func readBFraction(r *bits) (int, error) {
	i := r.u(3)
	if i == 7 {
		i = 7 + r.u(4)
	}
	if i == 21 {
		return 0, errStream
	}
	return bfractions[i], nil
}

// pictureHeader reads a picture or field header into d.ph: first says it
// is the first of a frame's (rather than a second field's or a slice's
// repeated one).
func (d *Decoder) pictureHeader(r *bits, first bool) error {
	p := &d.ph
	p.numref = false
	p.skipped = false
	if d.secondField {
		if p.fptype&4 != 0 {
			p.typ = picB
			if p.fptype&1 != 0 {
				p.typ = picBI
			}
		} else {
			p.typ = picI
			if p.fptype&1 != 0 {
				p.typ = picP
			}
		}
		return d.commonHeader(r, first)
	}
	fieldMode := false
	fcm := progressive
	if d.seq.interlace {
		switch r.v012() {
		case 1:
			fcm = ilaceFrame
		case 2:
			fcm, fieldMode = ilaceField, true
		}
	}
	if !first && p.fieldMode != fieldMode {
		return errStream
	}
	p.fieldMode, p.fcm = fieldMode, fcm
	if fieldMode {
		p.fptype = r.u(3)
		if p.fptype&4 != 0 {
			p.typ = picB
			if p.fptype&2 != 0 {
				p.typ = picBI
			}
		} else {
			p.typ = picI
			if p.fptype&2 != 0 {
				p.typ = picP
			}
		}
	} else {
		switch r.unary(0, 4) {
		case 0:
			p.typ = picP
		case 1:
			p.typ = picB
		case 2:
			p.typ = picI
		case 3:
			p.typ = picBI
		case 4:
			p.typ, p.skipped = picP, true
		}
	}
	if d.seq.tfcntrFlag {
		r.skip(8)
	}
	p.rptfrm, p.rff = 0, false
	p.tff = true
	if d.seq.broadcast {
		if !d.seq.interlace || d.seq.psf {
			p.rptfrm = r.u(2)
		} else {
			p.tff = r.flag()
			p.rff = r.flag()
		}
	}
	if d.ep.panScan {
		return errUnsupported("pan-scan windows")
	}
	if p.skipped {
		return nil
	}
	p.rnd = r.flag()
	if d.seq.interlace {
		p.uvsamp = r.flag()
	}
	if fieldMode {
		if !d.ep.refDistFlag {
			p.refdist = 0
		} else if p.typ != picB && p.typ != picBI {
			p.refdist = r.u(2)
			if p.refdist == 3 {
				p.refdist += r.unary(0, 14)
			}
			if p.refdist > 16 {
				return errStream
			}
		}
		if p.typ == picB || p.typ == picBI {
			bf, err := readBFraction(r)
			if err != nil {
				return err
			}
			p.bfraction = bf
			p.frfd = (p.bfraction * p.refdist) >> 8
			p.brfd = max(p.refdist-p.frfd-1, 0)
		}
		return d.commonHeader(r, first)
	}
	if fcm == progressive {
		if d.seq.finterp {
			p.interpfrm = r.flag()
		}
		if p.typ == picB {
			bf, err := readBFraction(r)
			if err != nil {
				return err
			}
			p.bfraction = bf
			if bf == 0 {
				p.typ = picBI
			}
		}
	}
	return d.commonHeader(r, first)
}

// commonHeader reads what follows the frame coding mode and type.
func (d *Decoder) commonHeader(r *bits, first bool) error {
	p := &d.ph
	if p.fieldMode {
		d.curFieldBottom = p.tff == d.secondField
	}
	p.pqindex = r.u(5)
	if p.pqindex == 0 {
		return errStream
	}
	if d.ep.quantizerMode == quantImplicit {
		p.pq = int(pquantTable[0][p.pqindex])
	} else {
		p.pq = int(pquantTable[1][p.pqindex])
	}
	p.halfpq = false
	if p.pqindex < 9 {
		p.halfpq = r.flag()
	}
	switch d.ep.quantizerMode {
	case quantImplicit:
		p.pquantizer = p.pqindex < 9
	case quantNonUniform:
		p.pquantizer = false
	case quantExplicit:
		p.pquantizer = r.flag()
	default:
		p.pquantizer = true
	}
	p.dquantFrm = false
	if d.seq.postprocFlag {
		p.postproc = r.u(2)
	}
	if first {
		d.rotateLUTs()
	}
	var err error
	switch p.typ {
	case picI, picBI:
		if p.fcm == ilaceFrame {
			if d.fieldTXRaw, err = d.bitplane(r, d.fieldTX); err != nil {
				return err
			}
		} else {
			d.fieldTXRaw = false
		}
		if d.acPredRaw, err = d.bitplane(r, d.acPred); err != nil {
			return err
		}
		p.condover = condOverNone
		if d.ep.overlap && p.pq <= 8 {
			p.condover = r.v012()
			if p.condover == condOverSelect {
				if d.overFlagsRaw, err = d.bitplane(r, d.overFlags); err != nil {
					return err
				}
			}
		}
	case picP:
		if err := d.pHeader(r); err != nil {
			return err
		}
	case picB:
		if err := d.bHeader(r); err != nil {
			return err
		}
	}
	p.cACTable = r.v012()
	if p.typ == picI || p.typ == picBI {
		p.yACTable = r.v012()
	} else if p.fcm != progressive && !p.quarter {
		p.rangeX <<= 1
		p.rangeY <<= 1
	}
	p.dcTable = r.u(1)
	if (p.typ == picI || p.typ == picBI) && d.ep.dquant != 0 {
		d.vopDquant(r)
	}
	if r.left() < 0 {
		return errShort
	}
	return nil
}

func (d *Decoder) setMVRange(r *bits) {
	p := &d.ph
	p.mvrange = 0
	if d.ep.extendedMV {
		p.mvrange = r.unary(0, 3)
	}
	p.kx = p.mvrange + 9 + p.mvrange>>1
	p.ky = p.mvrange + 8
	p.rangeX = 1 << (p.kx - 1)
	p.rangeY = 1 << (p.ky - 1)
}

func (d *Decoder) pHeader(r *bits) error {
	p := &d.ph
	var err error
	if p.fieldMode {
		p.numref = r.flag()
		if !p.numref {
			p.reffield = r.u(1)
			d.refFieldBottom[0] = (p.reffield != 0) != !d.curFieldBottom
		}
	}
	d.setMVRange(r)
	if d.seq.interlace {
		p.dmvrange = 0
		if d.ep.extendedDMV {
			p.dmvrange = r.unary(0, 3)
		}
		if p.fcm == ilaceFrame {
			p.fourMVSwitch = r.flag()
			p.intcomp = r.flag()
			if p.intcomp {
				p.lumscale, p.lumshift = r.u(6), r.u(6)
				initLUT(p.lumscale, p.lumshift, &d.lastLUTY[0], &d.lastLUTUV[0], true)
				initLUT(p.lumscale, p.lumshift, &d.lastLUTY[1], &d.lastLUTUV[1], true)
				d.lastUseIC = true
			}
			if d.skipRaw, err = d.bitplane(r, d.skipPlane); err != nil {
				return err
			}
			p.mbModeTable = r.u(2)
			p.imvTable = r.u(2)
			p.icbpTable = r.u(3)
			p.twoMVBPTable = r.u(2)
			if p.fourMVSwitch {
				p.fourMVBPTable = r.u(2)
			}
		}
	}
	p.ttIndex = 0
	if p.pq > 4 {
		p.ttIndex++
	}
	if p.pq > 12 {
		p.ttIndex++
	}
	if p.fcm != ilaceFrame {
		lowquant := 1
		if p.pq > 12 {
			lowquant = 0
		}
		p.mvMode = int(mvPModeTable[lowquant][r.unary(1, 4)])
		if p.mvMode == mvIntensity {
			p.mvMode2 = int(mvPModeTable2[lowquant][r.unary(1, 3)])
			if p.fieldMode {
				p.intcompField = r.v210() ^ 3
			} else {
				p.intcompField = 3
			}
			p.lumscale, p.lumshift = 32, 0
			p.lumscale2, p.lumshift2 = 32, 0
			if p.intcompField&1 != 0 {
				p.lumscale, p.lumshift = r.u(6), r.u(6)
			}
			if p.intcompField&2 != 0 && p.fieldMode {
				p.lumscale2, p.lumshift2 = r.u(6), r.u(6)
			} else if !p.fieldMode {
				p.lumscale2, p.lumshift2 = p.lumscale, p.lumshift
			}
			if p.fieldMode && d.secondField {
				cur := 0
				if d.curFieldBottom {
					cur = 1
				}
				if cur == 1 {
					initLUT(p.lumscale, p.lumshift, &d.currLUTY()[cur^1], &d.currLUTUV()[cur^1], false)
					initLUT(p.lumscale2, p.lumshift2, &d.lastLUTY[cur], &d.lastLUTUV[cur], true)
				} else {
					initLUT(p.lumscale2, p.lumshift2, &d.currLUTY()[cur^1], &d.currLUTUV()[cur^1], false)
					initLUT(p.lumscale, p.lumshift, &d.lastLUTY[cur], &d.lastLUTUV[cur], true)
				}
				d.nextUseIC = true
				*d.currUseIC() = true
			} else {
				initLUT(p.lumscale, p.lumshift, &d.lastLUTY[0], &d.lastLUTUV[0], true)
				initLUT(p.lumscale2, p.lumshift2, &d.lastLUTY[1], &d.lastLUTUV[1], true)
			}
			d.lastUseIC = true
		}
		m := p.mvMode
		if m == mvIntensity {
			m = p.mvMode2
		}
		p.quarter = m != mvHpel && m != mvHpelBilin
		p.mspel = m != mvHpelBilin
	}
	switch p.fcm {
	case progressive:
		if p.mixedMV() {
			if d.mvTypeRaw, err = d.bitplane(r, d.mvTypePlane); err != nil {
				return err
			}
		} else {
			d.mvTypeRaw = false
			clear(d.mvTypePlane)
		}
		if d.skipRaw, err = d.bitplane(r, d.skipPlane); err != nil {
			return err
		}
		p.mvTable = r.u(2)
		p.cbpTable = r.u(2)
	case ilaceFrame:
		p.quarter, p.mspel = true, true
	default:
		p.mbModeTable = r.u(3)
		if p.numref {
			p.imvTable = r.u(3)
		} else {
			p.imvTable = r.u(2)
		}
		p.icbpTable = r.u(3)
		if p.mixedMV() {
			p.fourMVBPTable = r.u(2)
		}
	}
	if d.ep.dquant != 0 {
		d.vopDquant(r)
	}
	d.transformTypes(r)
	return nil
}

func (p *picHeader) mixedMV() bool {
	return p.mvMode == mvMixed || (p.mvMode == mvIntensity && p.mvMode2 == mvMixed)
}

func (d *Decoder) bHeader(r *bits) error {
	p := &d.ph
	var err error
	if p.fcm == ilaceFrame {
		bf, err := readBFraction(r)
		if err != nil {
			return err
		}
		if bf == 0 {
			return errStream
		}
		p.bfraction = bf
	}
	d.setMVRange(r)
	p.ttIndex = 0
	if p.pq > 4 {
		p.ttIndex++
	}
	if p.pq > 12 {
		p.ttIndex++
	}
	switch p.fcm {
	case ilaceField:
		if d.ep.extendedDMV {
			p.dmvrange = r.unary(0, 3)
		}
		lowquant := 1
		if p.pq > 12 {
			lowquant = 0
		}
		p.mvMode = int(mvPModeTable2[lowquant][r.unary(1, 3)])
		p.quarter = p.mvMode == mv1MV || p.mvMode == mvMixed
		p.mspel = p.mvMode != mvHpelBilin
		if d.forwardRaw, err = d.bitplane(r, d.forwardPlane); err != nil {
			return err
		}
		p.mbModeTable = r.u(3)
		p.imvTable = r.u(3)
		p.icbpTable = r.u(3)
		if p.mvMode == mvMixed {
			p.fourMVBPTable = r.u(2)
		}
		p.numref = true
	case ilaceFrame:
		if d.ep.extendedDMV {
			p.dmvrange = r.unary(0, 3)
		}
		r.skip(1) // INTCOMP, always 0
		p.intcomp = false
		p.mvMode = mv1MV
		p.fourMVSwitch = false
		p.quarter, p.mspel = true, true
		if d.directRaw, err = d.bitplane(r, d.directPlane); err != nil {
			return err
		}
		if d.skipRaw, err = d.bitplane(r, d.skipPlane); err != nil {
			return err
		}
		p.mbModeTable = r.u(2)
		p.imvTable = r.u(2)
		p.icbpTable = r.u(3)
		p.twoMVBPTable = r.u(2)
		p.fourMVBPTable = r.u(2)
	default:
		if r.flag() {
			p.mvMode = mv1MV
		} else {
			p.mvMode = mvHpelBilin
		}
		p.quarter = p.mvMode == mv1MV
		p.mspel = p.quarter
		if d.directRaw, err = d.bitplane(r, d.directPlane); err != nil {
			return err
		}
		if d.skipRaw, err = d.bitplane(r, d.skipPlane); err != nil {
			return err
		}
		p.mvTable = r.u(2)
		p.cbpTable = r.u(2)
	}
	if d.ep.dquant != 0 {
		d.vopDquant(r)
	}
	d.transformTypes(r)
	return nil
}

func (d *Decoder) transformTypes(r *bits) {
	p := &d.ph
	if d.ep.vsTransform {
		p.ttmbf = r.flag()
		p.ttfrm = 0
		if p.ttmbf {
			p.ttfrm = int(ttfrmToTT[r.u(2)])
		}
	} else {
		p.ttmbf = true
		p.ttfrm = tt8x8
	}
}

// v210 reads the code 1, 01, 00 as 0, 1, 2.
func (r *bits) v210() int {
	if r.u(1) == 1 {
		return 0
	}
	return 2 - r.u(1)
}

// vopDquant reads VOPDQUANT.
func (d *Decoder) vopDquant(r *bits) {
	p := &d.ph
	if d.ep.dquant != 2 {
		p.dquantFrm = r.flag()
		if !p.dquantFrm {
			return
		}
		p.dqProfile = r.u(2)
		switch p.dqProfile {
		case dqSingleEdge, dqDoubleEdges:
			p.dqSBEdge = r.u(2)
		case dqAllMBs:
			p.dqBilevel = r.flag()
			if !p.dqBilevel {
				p.halfpq = false
				return
			}
		}
	}
	pqdiff := r.u(3)
	if pqdiff == 7 {
		p.altpq = r.u(5)
	} else {
		p.altpq = p.pq + pqdiff + 1
	}
}

// Bitplane coding modes.
const (
	imodeRaw = iota
	imodeNorm2
	imodeDiff2
	imodeNorm6
	imodeDiff6
	imodeRowSkip
	imodeColSkip
)

// bitplane decodes a bitplane into plane (one byte a macroblock, rows of
// mbw), giving true when it is raw: coded in the macroblock layer.
func (d *Decoder) bitplane(r *bits, plane []uint8) (bool, error) {
	w := d.mbw
	h := d.mbh
	if d.ph.fieldMode {
		h = d.fieldMBH()
	}
	invert := r.u(1)
	imode, ok := imodeVLC.read(r)
	if !ok {
		return false, errStream
	}
	plane = plane[:w*h]
	switch imode {
	case imodeRaw:
		return true, nil
	case imodeNorm2, imodeDiff2:
		i := 0
		if w*h&1 != 0 {
			plane[0] = uint8(r.u(1))
			i = 1
		}
		for ; i < w*h; i += 2 {
			code, ok := norm2VLC.read(r)
			if !ok {
				return false, errStream
			}
			plane[i] = uint8(code & 1)
			plane[i+1] = uint8(code >> 1)
		}
	case imodeNorm6, imodeDiff6:
		if h%3 == 0 && w%3 != 0 { // 2x3 tiles
			for y := 0; y < h; y += 3 {
				for x := w & 1; x < w; x += 2 {
					code, ok := norm6VLC.read(r)
					if !ok {
						return false, errStream
					}
					for k := range 6 {
						plane[(y+k>>1)*w+x+k&1] = uint8(code >> k & 1)
					}
				}
			}
			if w&1 != 0 {
				colSkip(r, plane, 0, 1, w, h)
			}
		} else { // 3x2 tiles
			for y := h & 1; y < h; y += 2 {
				for x := w % 3; x < w; x += 3 {
					code, ok := norm6VLC.read(r)
					if !ok {
						return false, errStream
					}
					for k := range 6 {
						plane[(y+k/3)*w+x+k%3] = uint8(code >> k & 1)
					}
				}
			}
			x := w % 3
			if x != 0 {
				colSkip(r, plane, 0, x, w, h)
			}
			if h&1 != 0 {
				rowSkip(r, plane[x:], w-x, 1, w)
			}
		}
	case imodeRowSkip:
		rowSkip(r, plane, w, h, w)
	case imodeColSkip:
		colSkip(r, plane, 0, w, w, h)
	}
	if imode == imodeDiff2 || imode == imodeDiff6 {
		inv := uint8(invert)
		plane[0] ^= inv
		for x := 1; x < w; x++ {
			plane[x] ^= plane[x-1]
		}
		for y := 1; y < h; y++ {
			row := plane[y*w:]
			up := plane[(y-1)*w:]
			row[0] ^= up[0]
			for x := 1; x < w; x++ {
				if row[x-1] != up[x] {
					row[x] ^= inv
				} else {
					row[x] ^= row[x-1]
				}
			}
		}
	} else if invert != 0 {
		for i := range plane {
			plane[i] ^= 1
		}
	}
	if r.left() < 0 {
		return false, errShort
	}
	return false, nil
}

func rowSkip(r *bits, plane []uint8, w, h, stride int) {
	for y := range h {
		row := plane[y*stride : y*stride+w]
		if r.u(1) == 0 {
			clear(row)
			continue
		}
		for x := range row {
			row[x] = uint8(r.u(1))
		}
	}
}

func colSkip(r *bits, plane []uint8, x0, n, stride, h int) {
	for x := x0; x < x0+n; x++ {
		if r.u(1) == 0 {
			for y := range h {
				plane[y*stride+x] = 0
			}
			continue
		}
		for y := range h {
			plane[y*stride+x] = uint8(r.u(1))
		}
	}
}

type unsupported string

func (u unsupported) Error() string { return "vc1: " + string(u) + " is not supported" }

func errUnsupported(what string) error { return unsupported(what) }
