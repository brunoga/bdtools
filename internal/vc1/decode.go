package vc1

// Picture is a decoded frame, in display order: 8-bit 4:2:0 planes, the
// coded size of a larger buffer.
type Picture struct {
	Width, Height    int
	Y, Cb, Cr        []byte
	StrideY, StrideC int
	PTS              int64
	// Interlaced frames' fields are of two moments, TopFieldFirst says
	// which comes first.
	Interlaced, TopFieldFirst bool
	// FrameRateNum and FrameRateDen are the sequence's frame rate (0 when
	// it does not say).
	FrameRateNum, FrameRateDen int
	// Primaries, Transfer and Matrix are the sequence's colour description
	// (H.273 code points; 2, unspecified, when there is none).
	Primaries, Transfer, Matrix int
}

// mv is a motion vector, in quarter samples.
type mv struct{ x, y int16 }

type frame struct {
	y, cb, cr  []byte
	pts        int64
	interlaced bool // coded as an interlaced frame or as fields
	tff        bool
	fields     bool // coded as two fields
	grey       bool // made up for a P picture without a reference
	// directMV is the vector of each block (a P picture's, for B
	// pictures' direct mode), on the block grid; intraMB the intra
	// macroblocks of field pictures (each field's on the macroblock grid).
	directMV []mv
	intraMB  []bool
}

// Decoder decodes a VC-1 Advanced Profile elementary stream, access unit by
// access unit (each a frame: its start codes and their units).
type Decoder struct {
	seq           seqHeader
	ep            entryPoint
	haveSeq       bool
	haveEP        bool
	width, height int // coded
	mbw, mbh      int // macroblocks of the frame
	mbhA          int // mbh rounded up to even, for field pictures
	strideY       int
	strideC       int

	ph             picHeader
	secondField    bool
	curFieldBottom bool
	refFieldBottom [2]bool

	// Bitplanes, a byte a macroblock.
	fieldTX, acPred, overFlags     []uint8
	mvTypePlane, skipPlane         []uint8
	directPlane, forwardPlane      []uint8
	fieldTXRaw, acPredRaw          bool
	overFlagsRaw, mvTypeRaw        bool
	skipRaw, directRaw, forwardRaw bool

	// Intensity compensation tables: for the reference before (last) and
	// after (next), and a B picture's own (aux).
	lastLUTY, lastLUTUV [2][256]uint8
	nextLUTY, nextLUTUV [2][256]uint8
	auxLUTY, auxLUTUV   [2][256]uint8
	lastUseIC           bool
	nextUseIC           bool
	auxUseIC            bool
	currAux             bool // curr is aux rather than next

	// Frames: cur the one being decoded, last and next the references
	// (next the newer).
	cur, last, next *frame
	// mvfNext is the last field reference's opposite field flags (see
	// mbContext.mvfY), for direct prediction.
	mvfNext  [2][]bool
	mvfNextC [2][]bool
	pool     []*frame
	errors   int

	mb  mbContext
	out func(*Picture) error
}

// New makes a decoder.
func New() *Decoder {
	initVLCs()
	return &Decoder{}
}

// Errors is how many pictures or slices could not be decoded (and were
// concealed by what was there).
func (d *Decoder) Errors() int { return d.errors }

// Start codes' last bytes.
const (
	scEndOfSeq   = 0x0a
	scSlice      = 0x0b
	scField      = 0x0c
	scFrame      = 0x0d
	scEntryPoint = 0x0e
	scSequence   = 0x0f
)

// unit is a start code unit: its code, and its payload with emulation
// prevention removed.
type unit struct {
	code byte
	data []byte
}

// units splits an access unit at its start codes, unescaping each.
func units(au []byte) []unit {
	var us []unit
	start := -1
	for i := 0; i+3 <= len(au); {
		if au[i] == 0 && au[i+1] == 0 && au[i+2] == 1 && i+3 < len(au) {
			if start >= 0 {
				us = append(us, unit{au[start], unescape(au[start+1 : i])})
			}
			start = i + 3
			i += 4
			continue
		}
		i++
	}
	if start >= 0 && start < len(au) {
		us = append(us, unit{au[start], unescape(au[start+1:])})
	}
	return us
}

// unescape removes emulation prevention bytes: a 3 after two zeros, before
// a byte below 4.
func unescape(b []byte) []byte {
	var out []byte
	zeros := 0
	for i := 0; i < len(b); i++ {
		c := b[i]
		if zeros >= 2 && c == 3 && i+1 < len(b) && b[i+1] < 4 {
			if out == nil {
				out = append(make([]byte, 0, len(b)), b[:i]...)
			}
			zeros = 0
			continue
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		if out != nil {
			out = append(out, c)
		}
	}
	if out == nil {
		return b
	}
	return out
}

// Decode decodes an access unit given at pts, giving out the pictures that
// are complete, in display order.
func (d *Decoder) Decode(au []byte, pts int64, out func(*Picture) error) error {
	d.out = out
	us := units(au)
	var frameUnit *unit
	var slices []unit
	var field *unit
	for i := range us {
		u := &us[i]
		switch u.code {
		case scSequence:
			if err := d.sequenceHeader(&bits{b: u.data}); err != nil {
				return err
			}
		case scEntryPoint:
			if err := d.entryPointHeader(&bits{b: u.data}); err != nil {
				return err
			}
		case scFrame:
			frameUnit = u
		case scField:
			if frameUnit != nil {
				field = u
			}
		case scSlice:
			if frameUnit != nil {
				if field != nil {
					// Slices of the second field come after its unit.
					slices = append(slices, unit{code: scSlice | 0x80, data: u.data})
				} else {
					slices = append(slices, *u)
				}
			}
		}
	}
	if frameUnit == nil || !d.haveSeq || !d.haveEP {
		return nil
	}
	if err := d.setSize(d.ep.width, d.ep.height); err != nil {
		return err
	}
	return d.decodeFrame(frameUnit, field, slices, pts)
}

func (d *Decoder) setSize(w, h int) error {
	if w == d.width && h == d.height {
		return nil
	}
	if w <= 0 || h <= 0 || w > 8192 || h > 8192 {
		return errStream
	}
	d.width, d.height = w, h
	d.mbw, d.mbh = (w+15)>>4, (h+15)>>4
	d.mbhA = (d.mbh + 1) &^ 1
	d.strideY = d.mbw * 16
	d.strideC = d.mbw * 8
	n := d.mbw * d.mbhA
	d.fieldTX = make([]uint8, n)
	d.acPred = make([]uint8, n)
	d.overFlags = make([]uint8, n)
	d.mvTypePlane = make([]uint8, n)
	d.skipPlane = make([]uint8, n)
	d.directPlane = make([]uint8, n)
	d.forwardPlane = make([]uint8, n)
	d.pool = nil
	d.cur, d.last, d.next = nil, nil, nil
	d.mb.init(d)
	for dir := range 2 {
		d.mvfNext[dir] = make([]bool, d.mb.blkLen)
		d.mvfNextC[dir] = make([]bool, d.mb.mbLen)
	}
	return nil
}

// fieldMBH is the macroblock rows of a field.
func (d *Decoder) fieldMBH() int { return d.mbhA >> 1 }

func (d *Decoder) newFrame() *frame {
	if n := len(d.pool); n > 0 {
		f := d.pool[n-1]
		d.pool = d.pool[:n-1]
		return f
	}
	ys := d.strideY * d.mbhA * 16
	cs := d.strideC * d.mbhA * 8
	buf := make([]byte, ys+2*cs)
	return &frame{
		y: buf[:ys], cb: buf[ys : ys+cs], cr: buf[ys+cs:],
		directMV: make([]mv, d.mb.blkLen),
		intraMB:  make([]bool, d.mb.mbLen),
	}
}

func (d *Decoder) release(f *frame) {
	if f != nil && f != d.last && f != d.next && f != d.cur {
		d.pool = append(d.pool, f)
	}
}

// emit gives a frame out.
func (d *Decoder) emit(f *frame) error {
	p := &Picture{
		Width: d.width, Height: d.height,
		Y: f.y, Cb: f.cb, Cr: f.cr,
		StrideY: d.strideY, StrideC: d.strideC,
		PTS:           f.pts,
		Interlaced:    f.interlaced,
		TopFieldFirst: f.tff,
		FrameRateNum:  d.seq.rateNum, FrameRateDen: d.seq.rateDen,
		Primaries: d.seq.prim, Transfer: d.seq.trc, Matrix: d.seq.matrix,
	}
	return d.out(p)
}

// Flush gives out the last reference frame.
func (d *Decoder) Flush(out func(*Picture) error) error {
	d.out = out
	if d.next == nil {
		return nil
	}
	f := d.next
	d.next = nil
	err := d.emit(f)
	d.release(f)
	if d.last != nil {
		l := d.last
		d.last = nil
		d.release(l)
	}
	return err
}

func (d *Decoder) decodeFrame(fu *unit, field *unit, slices []unit, pts int64) error {
	d.secondField = false
	r := &bits{b: fu.data}
	if err := d.pictureHeader(r, true); err != nil {
		d.errors++
		return nil
	}
	p := &d.ph
	if (p.typ == picB || p.typ == picBI) && d.last == nil {
		return nil // a B picture without both its references (an open GOP's start)
	}
	f := d.newFrame()
	f.pts = pts
	f.interlaced = p.fcm != progressive
	f.tff = p.tff
	f.fields = p.fieldMode
	f.grey = false
	anchor := p.typ != picB && p.typ != picBI
	if anchor {
		if p.typ == picP && d.next == nil {
			// A P picture without a reference: predict from grey.
			g := d.newFrame()
			for i := range g.y {
				g.y[i] = 128
			}
			for i := range g.cb {
				g.cb[i], g.cr[i] = 128, 128
			}
			clear(g.directMV)
			g.grey = true
			d.next = g
		}
		old := d.last
		d.last, d.next = d.next, f
		d.release(old)
	}
	d.cur = f
	if err := d.decodePicture(r, field, slices); err != nil {
		d.errors++
	}
	d.cur = nil
	if anchor {
		if d.last != nil && !d.last.grey {
			return d.emit(d.last)
		}
		return nil
	}
	err := d.emit(f)
	d.release(f)
	return err
}
