package mpeg2

// Hardware decoding: with an Accel, the decoder parses the stream and
// keeps the frame order and references as it always does, and hands each
// picture's slices to the accelerator (VAAPI) in place of decoding them.
// Frames live in the accelerator's surfaces.

// Accel decodes pictures for the decoder.
type Accel interface {
	// NewSurface gives a surface for frames of the size (the coded size:
	// whole macroblocks). The surfaces of another size have all been
	// released before.
	NewSurface(width, height int) (int, error)
	// Release gives a surface back.
	Release(surface int)
	// DecodePicture decodes a picture (a frame, or one field of one) from
	// its slices, in order.
	DecodePicture(p *AccelPicture, slices []AccelSlice) error
	// Output fills the planes of p, a frame given out in display order,
	// from the surface.
	Output(surface int, p *Picture) error
}

// AccelPicture is a picture's parameters, the standard's syntax elements.
type AccelPicture struct {
	Surface int
	// Forward and Backward are the reference frames' surfaces (-1: none).
	Forward, Backward int
	Width, Height     int // the sequence's
	CodingType        int // 1 I, 2 P, 3 B
	FCode             [2][2]int
	DCPrecision       int
	Structure         int // 1 top field, 2 bottom field, 3 frame
	TopFieldFirst     bool
	FramePredFrameDCT bool
	Concealment       bool
	QScaleType        bool
	IntraVLC          bool
	AlternateScan     bool
	RepeatFirstField  bool
	ProgressiveFrame  bool
	// FirstField is set for a frame picture, and the first field of a
	// frame.
	FirstField bool
	// The quantiser matrices in zigzag scan order.
	IntraQ, NonIntraQ [64]uint8
}

// AccelSlice is a slice: its data, from its start code, and its header.
type AccelSlice struct {
	Data []byte
	// MacroblockOffset is where the first macroblock starts, in bits from
	// the start code.
	MacroblockOffset   int
	MBX, MBY           int // the first macroblock's (MBY in the picture: a field's rows)
	QuantiserScaleCode int
	IntraSlice         bool
}

// SetAccel makes the decoder decode through a, from the next sequence.
func (d *Decoder) SetAccel(a Accel) { d.accel = a }

// accelSlice queues a slice of the current picture.
func (d *Decoder) accelSlice(code int, b []byte) error {
	r := bits{b: b}
	row := code - 1
	if d.height > 2800 {
		row += r.u(3) << 7
	}
	q := r.u(5)
	intra := r.peek(1) == 1
	if intra {
		r.skip(9)
	}
	for r.u(1) == 1 {
		r.skip(8)
	}
	offset := r.pos
	mbx := -1
	for {
		v, ok := mbIncrement.read(&r)
		if !ok {
			return errStream
		}
		if v == -1 {
			mbx += 33
			continue
		}
		if v == -2 {
			continue
		}
		mbx += v
		break
	}
	n := len(d.accData)
	d.accData = append(d.accData, 0, 0, 1, byte(code))
	d.accData = append(d.accData, b...)
	d.accSlices = append(d.accSlices, AccelSlice{MacroblockOffset: 32 + offset, MBX: mbx, MBY: row,
		QuantiserScaleCode: q, IntraSlice: intra, Data: d.accData[n:]})
	return nil
}

// accelPicture decodes the picture's slices queued.
func (d *Decoder) accelPicture() error {
	if len(d.accSlices) == 0 {
		return nil
	}
	// The data may have moved as it grew.
	off := 0
	for i := range d.accSlices {
		s := &d.accSlices[i]
		n := len(s.Data)
		s.Data = d.accData[off : off+n]
		off += n
	}
	p := &d.pic
	ap := &AccelPicture{Surface: d.cur.surface, Forward: -1, Backward: -1,
		Width: d.width, Height: d.height, CodingType: p.codingType, FCode: p.fcode,
		DCPrecision: p.dcPrecision, Structure: p.structure, TopFieldFirst: p.tff,
		FramePredFrameDCT: p.framePredFrame, Concealment: p.concealment, QScaleType: p.qScaleType,
		IntraVLC: p.intraVLC, AlternateScan: p.alternateScan, RepeatFirstField: p.repeatFirst,
		ProgressiveFrame: p.progressiveFrame, FirstField: p.structure == framePic || !d.second}
	switch p.codingType {
	case 2:
		if d.fwd != nil {
			ap.Forward = d.fwd.surface
		}
	case 3:
		ap.Forward, ap.Backward = d.fwd.surface, d.bwd.surface
	}
	if p.codingType == 2 && d.fwd == nil {
		// A P picture after a sequence start that predicts from the only
		// anchor there is: itself (only its second field can).
		ap.Forward = d.cur.surface
	}
	for i := range 64 {
		ap.IntraQ[i] = d.intraQ[scanZigzag[i]]
		ap.NonIntraQ[i] = d.nonIntraQ[scanZigzag[i]]
	}
	err := d.accel.DecodePicture(ap, d.accSlices)
	d.accSlices = d.accSlices[:0]
	d.accData = d.accData[:0]
	return err
}
