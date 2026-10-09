package mpeg2

import "errors"

// Picture is a decoded frame, in display order: 8-bit 4:2:0 planes, the
// displayed size of a larger buffer.
type Picture struct {
	Width, Height    int
	Y, Cb, Cr        []byte
	StrideY, StrideC int
	PTS              int64
	// Progressive is progressive_frame: the two fields are of one moment.
	// TopFieldFirst says which field comes first when not.
	Progressive, TopFieldFirst bool
	// FrameRateNum and FrameRateDen are the sequence's frame rate.
	FrameRateNum, FrameRateDen int
	// Primaries, Transfer and Matrix are the sequence display extension's
	// colour description (H.273 code points; 2, unspecified, when there is
	// none).
	Primaries, Transfer, Matrix int
}

type frame struct {
	y, cb, cr   []byte
	pts         int64
	progressive bool
	tff         bool
	anchor      bool // an I or P frame
	surface     int  // the accelerator's
}

// Decoder decodes an MPEG-2 video elementary stream, access unit by
// access unit.
type Decoder struct {
	// The sequence.
	seq               bool
	width, height     int
	mbw, mbh          int // macroblocks of the frame
	progressiveSeq    bool
	intraQ, nonIntraQ [64]uint8 // raster order
	rateNum, rateDen  int
	prim, trc, matrix int
	strideY, strideC  int
	lumaH             int

	// The picture being decoded.
	pic       picture
	inPicture bool
	cur       *frame
	second    bool   // the current picture is the second field of cur
	held      *frame // a frame of which one field has been decoded

	// References: fwd the older anchor, bwd the newer.
	fwd, bwd *frame
	pool     []*frame
	errors   int

	// The macroblock being decoded.
	r      bits
	qscale int
	dcPred [3]int
	pmv    [2][2][2]int // [r][s][t]
	last   mbState      // for skipped macroblocks in B pictures
	blk    [64]int32
	resid  [6][64]int32
	predY  [256]uint8
	predC  [2][64]uint8
	out    func(*Picture) error

	accel     Accel
	accSlices []AccelSlice
	accData   []byte
}

// picture is a picture header and its coding extension.
type picture struct {
	codingType       int // 1 I, 2 P, 3 B
	fcode            [2][2]int
	dcPrecision      int
	structure        int // 1 top field, 2 bottom field, 3 frame
	tff              bool
	framePredFrame   bool
	concealment      bool
	qScaleType       bool
	intraVLC         bool
	alternateScan    bool
	progressiveFrame bool
	repeatFirst      bool
	pts              int64
}

// mbState is what a skipped B macroblock repeats.
type mbState struct {
	mbType      int
	motionType  int
	fieldSelect [2][2]int
	vec         [2][2][2]int // the vectors used, [r][s][t]
	dmv         [2]int
}

const (
	topField    = 1
	bottomField = 2
	framePic    = 3
)

var errStream = errors.New("mpeg2: an invalid stream")

// New makes a decoder.
func New() *Decoder { return &Decoder{} }

// Errors is how many slices could not be decoded (and were concealed by
// what was there).
func (d *Decoder) Errors() int { return d.errors }

// Decode decodes an access unit (one or more start code units) given at
// pts, giving out the pictures that are complete in display order.
func (d *Decoder) Decode(au []byte, pts int64, out func(*Picture) error) error {
	d.out = out
	first := true
	for _, u := range units(au) {
		code := u[0]
		body := u[1:]
		switch {
		case code == 0xb3:
			if err := d.sequenceHeader(body); err != nil {
				return err
			}
		case code == 0xb5:
			d.extension(body)
		case code == 0xb8: // group of pictures
		case code == 0x00:
			if err := d.endPicture(); err != nil {
				return err
			}
			p := pts
			if !first {
				p = -1 // the second picture of an access unit has no timestamp of its own
			}
			first = false
			d.pictureHeader(body, p)
		case code >= 0x01 && code <= 0xaf:
			if !d.seq || !d.inPicture {
				continue
			}
			if d.cur == nil {
				if err := d.beginPicture(); err != nil {
					return err
				}
			}
			if d.cur == nil {
				continue
			}
			if d.accel != nil {
				if err := d.accelSlice(int(code), body); err != nil {
					d.errors++
				}
			} else if err := d.slice(int(code), body); err != nil {
				d.errors++
			}
		case code == 0xb7: // sequence end
			if err := d.endPicture(); err != nil {
				return err
			}
		}
	}
	return d.endPicture()
}

// Flush gives out the last picture.
func (d *Decoder) Flush(out func(*Picture) error) error {
	d.out = out
	if err := d.endPicture(); err != nil {
		return err
	}
	if d.bwd != nil {
		f := d.bwd
		d.bwd = nil
		return d.emit(f)
	}
	return nil
}

// units splits a stream into start code units: each the start code's last
// byte and what follows up to the next.
func units(b []byte) [][]byte {
	var out [][]byte
	start := -1
	for i := 0; i+3 < len(b); i++ {
		if b[i] != 0 || b[i+1] != 0 || b[i+2] != 1 {
			continue
		}
		if start >= 0 {
			out = append(out, b[start:i])
		}
		start = i + 3
		i += 2
	}
	if start >= 0 && start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

func (d *Decoder) sequenceHeader(b []byte) error {
	r := bits{b: b}
	w, h := r.u(12), r.u(12)
	r.skip(4) // aspect_ratio_information
	rate := r.u(4)
	r.skip(18 + 1 + 10 + 1) // bit_rate, marker, vbv_buffer_size, constrained_parameters_flag
	if r.flag() {
		for i := range 64 {
			d.intraQ[scanZigzag[i]] = uint8(r.u(8))
		}
	} else {
		d.intraQ = defaultIntraMatrix
	}
	if r.flag() {
		for i := range 64 {
			d.nonIntraQ[scanZigzag[i]] = uint8(r.u(8))
		}
	} else {
		d.nonIntraQ = defaultNonIntraMatrix
	}
	if w == 0 || h == 0 {
		return errStream
	}
	d.rateNum, d.rateDen = frameRates[rate][0], frameRates[rate][1]
	if !d.seq || w != d.width || h != d.height {
		d.width, d.height = w, h
		d.progressiveSeq = true // MPEG-1 until an extension says otherwise
		d.prim, d.trc, d.matrix = 2, 2, 2
		d.layout()
	}
	d.seq = true
	return nil
}

// layout sizes the frame buffers for the sequence.
func (d *Decoder) layout() {
	d.mbw = (d.width + 15) / 16
	if d.progressiveSeq {
		d.mbh = (d.height + 15) / 16
	} else {
		d.mbh = 2 * ((d.height + 31) / 32)
	}
	d.strideY, d.strideC = d.mbw*16, d.mbw*8
	d.lumaH = d.mbh * 16
	d.pool = nil
	if d.accel != nil {
		// The accelerator's surfaces of the old size go with it.
		d.fwd, d.bwd, d.held, d.cur = nil, nil, nil, nil
	}
}

func (d *Decoder) extension(b []byte) {
	r := bits{b: b}
	switch r.u(4) {
	case 1: // sequence extension
		r.skip(8) // profile_and_level_indication
		prog := r.flag()
		r.skip(2) // chroma_format
		wx, hx := r.u(2), r.u(2)
		r.skip(12 + 1 + 8) // bit_rate_extension, marker, vbv_buffer_size_extension
		r.skip(1)          // low_delay
		rn, rd := r.u(2), r.u(5)
		w, h := d.width|wx<<12, d.height|hx<<12
		if prog != d.progressiveSeq || w != d.width || h != d.height {
			d.progressiveSeq, d.width, d.height = prog, w, h
			d.layout()
		}
		if d.rateNum > 0 {
			d.rateNum, d.rateDen = d.rateNum*(rn+1), d.rateDen*(rd+1)
		}
	case 2: // sequence display extension
		r.skip(3) // video_format
		if r.flag() {
			d.prim, d.trc, d.matrix = r.u(8), r.u(8), r.u(8)
		}
	case 3: // quant matrix extension
		if r.flag() {
			for i := range 64 {
				d.intraQ[scanZigzag[i]] = uint8(r.u(8))
			}
		}
		if r.flag() {
			for i := range 64 {
				d.nonIntraQ[scanZigzag[i]] = uint8(r.u(8))
			}
		}
	case 8: // picture coding extension
		p := &d.pic
		p.fcode = [2][2]int{{r.u(4), r.u(4)}, {r.u(4), r.u(4)}}
		p.dcPrecision = r.u(2)
		p.structure = r.u(2)
		p.tff = r.flag()
		p.framePredFrame = r.flag()
		p.concealment = r.flag()
		p.qScaleType = r.flag()
		p.intraVLC = r.flag()
		p.alternateScan = r.flag()
		p.repeatFirst = r.flag()
		r.skip(1) // chroma_420_type
		p.progressiveFrame = r.flag()
	}
}

func (d *Decoder) pictureHeader(b []byte, pts int64) {
	r := bits{b: b}
	r.skip(10) // temporal_reference
	t := r.u(3)
	r.skip(16) // vbv_delay
	d.pic = picture{codingType: t, structure: framePic, framePredFrame: true, progressiveFrame: true, pts: pts,
		fcode: [2][2]int{{15, 15}, {15, 15}}}
	if t == 2 || t == 3 {
		r.skip(1)
		f := r.u(3)
		d.pic.fcode[0] = [2]int{f, f} // MPEG-1; the coding extension overrides
	}
	if t == 3 {
		r.skip(1)
		f := r.u(3)
		d.pic.fcode[1] = [2]int{f, f}
	}
	d.inPicture = t >= 1 && t <= 3
}

// beginPicture sets up the frame the picture decodes into.
func (d *Decoder) beginPicture() error {
	p := &d.pic
	if p.structure != framePic && d.cur == nil && d.secondPending() {
		// The second field of the frame begun by the last picture.
		d.cur, d.second = d.held, true
		d.held = nil
		return nil
	}
	if (p.codingType == 2 && d.bwd == nil) || (p.codingType == 3 && (d.fwd == nil || d.bwd == nil)) {
		// A picture predicted from what has not been decoded (a stream
		// starting on an open GOP's B pictures): skipped.
		d.inPicture = false
		return nil
	}
	f, err := d.frame()
	if err != nil {
		return err
	}
	f.pts, f.progressive, f.tff = p.pts, p.progressiveFrame, p.tff
	f.anchor = p.codingType != 3
	if p.structure != framePic {
		f.tff = p.structure == topField
	}
	if f.anchor {
		// The newer anchor is shown after the B pictures that follow it in
		// decoding order: it goes out now, before the next anchor.
		if d.bwd != nil {
			if err := d.emit(d.bwd); err != nil {
				return err
			}
		}
		old := d.fwd
		d.fwd, d.bwd = d.bwd, f
		if old != nil {
			d.release(old)
		}
	}
	d.cur, d.second = f, false
	return nil
}

// secondPending reports whether a frame waits for its second field.
func (d *Decoder) secondPending() bool { return d.held != nil }

// endPicture finishes the picture decoded so far.
func (d *Decoder) endPicture() error {
	if d.cur == nil {
		d.inPicture = false
		return nil
	}
	if d.accel != nil {
		if err := d.accelPicture(); err != nil {
			d.cur = nil
			return err
		}
	}
	f := d.cur
	d.cur = nil
	d.inPicture = false
	if d.pic.structure != framePic && !d.second {
		d.held = f // wait for its other field
		return nil
	}
	if !f.anchor {
		err := d.emit(f)
		d.release(f)
		return err
	}
	return nil
}

func (d *Decoder) frame() (*frame, error) {
	if d.accel != nil {
		s, err := d.accel.NewSurface(d.strideY, d.lumaH)
		if err != nil {
			return nil, err
		}
		return &frame{surface: s}, nil
	}
	if n := len(d.pool); n > 0 {
		f := d.pool[n-1]
		d.pool = d.pool[:n-1]
		return f, nil
	}
	return &frame{y: make([]byte, d.strideY*d.lumaH), cb: make([]byte, d.strideC*d.lumaH/2), cr: make([]byte, d.strideC*d.lumaH/2)}, nil
}

func (d *Decoder) release(f *frame) {
	if f == d.fwd || f == d.bwd {
		return
	}
	if d.accel != nil {
		d.accel.Release(f.surface)
		return
	}
	if len(f.y) == d.strideY*d.lumaH {
		d.pool = append(d.pool, f)
	}
}

func (d *Decoder) emit(f *frame) error {
	if d.out == nil {
		return nil
	}
	p := &Picture{Width: d.width, Height: d.height, Y: f.y, Cb: f.cb, Cr: f.cr, StrideY: d.strideY, StrideC: d.strideC,
		PTS: f.pts, Progressive: f.progressive, TopFieldFirst: f.tff, FrameRateNum: d.rateNum, FrameRateDen: d.rateDen,
		Primaries: d.prim, Transfer: d.trc, Matrix: d.matrix}
	if d.accel != nil {
		if err := d.accel.Output(f.surface, p); err != nil {
			return err
		}
	}
	return d.out(p)
}
