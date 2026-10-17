// Package pgs reads Blu-ray subtitles (Presentation Graphic Streams):
// their display sets made into the pictures they show, and when.
package pgs

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
)

// Segment types.
const (
	SegPDS = 0x14 // palette definition
	SegODS = 0x15 // object definition
	SegPCS = 0x16 // presentation composition
	SegWDS = 0x17 // window definition
	SegEND = 0x80
)

// Palette is a palette's entries: Y, Cr, Cb, alpha.
type Palette [256][4]byte

// Object is a picture object, its pixels palette indices.
type Object struct {
	W, H int
	Pix  []byte
}

// Placed is an object as a composition shows it: where on the plane, and
// the part of it shown.
type Placed struct {
	Obj      *Object
	X, Y     int
	CX, CY   int // the part shown (all of it when not cropped)
	CW, CH   int
	ObjectID uint16
	WindowID byte
	Forced   bool
}

// Composition is what a display set shows from its time on: nothing (it
// clears the screen) or objects in a palette.
type Composition struct {
	PTS     int64 // 90 kHz
	W, H    int   // the plane's size
	Objects []Placed
	Palette *Palette
}

// Empty reports whether the composition shows nothing.
func (c *Composition) Empty() bool { return len(c.Objects) == 0 || c.Palette == nil }

// Forced reports whether everything it shows is forced (shown with the
// subtitles off: foreign dialogue, signs).
func (c *Composition) Forced() bool {
	for _, p := range c.Objects {
		if !p.Forced {
			return false
		}
	}
	return len(c.Objects) > 0
}

// Decoder follows a stream's display sets.
type Decoder struct {
	palettes map[byte]*Palette
	objects  map[uint16]*Object
	partial  map[uint16]*partialObject
	pcs      []byte
	pcsPTS   int64
}

type partialObject struct {
	w, h int
	rle  []byte
}

// NewDecoder starts following a stream.
func NewDecoder() *Decoder {
	return &Decoder{palettes: map[byte]*Palette{}, objects: map[uint16]*Object{}, partial: map[uint16]*partialObject{}}
}

// Segment takes a segment (its type, length and payload) at its PTS; at a
// display set's end it gives the set's composition.
func (d *Decoder) Segment(pts int64, seg []byte) (*Composition, bool) {
	if len(seg) < 3 {
		return nil, false
	}
	typ, s := seg[0], seg[3:]
	if n := int(binary.BigEndian.Uint16(seg[1:])); n < len(s) {
		s = s[:n]
	}
	switch typ {
	case SegPCS:
		if len(s) < 11 {
			return nil, false
		}
		if s[7]&0x80 != 0 { // epoch start: the buffers begin afresh
			d.palettes = map[byte]*Palette{}
			d.objects = map[uint16]*Object{}
			d.partial = map[uint16]*partialObject{}
		}
		d.pcs = append(d.pcs[:0], s...)
		d.pcsPTS = pts
	case SegPDS:
		if len(s) < 2 {
			return nil, false
		}
		p := new(Palette)
		if old := d.palettes[s[0]]; old != nil {
			*p = *old
		}
		for e := s[2:]; len(e) >= 5; e = e[5:] {
			p[e[0]] = [4]byte{e[1], e[2], e[3], e[4]}
		}
		d.palettes[s[0]] = p
	case SegODS:
		if len(s) < 4 {
			return nil, false
		}
		id := binary.BigEndian.Uint16(s)
		flags, data := s[3], s[4:]
		if flags&0x80 != 0 { // first in sequence: its length and size
			if len(data) < 7 {
				return nil, false
			}
			d.partial[id] = &partialObject{w: int(binary.BigEndian.Uint16(data[3:])), h: int(binary.BigEndian.Uint16(data[5:])),
				rle: append([]byte(nil), data[7:]...)}
		} else if o := d.partial[id]; o != nil {
			o.rle = append(o.rle, data...)
		}
		if flags&0x40 != 0 { // last in sequence
			if o := d.partial[id]; o != nil && o.w > 0 && o.h > 0 && o.w*o.h <= 4096*2160 {
				if pix, err := DecodeRLE(o.rle, o.w, o.h); err == nil {
					d.objects[id] = &Object{W: o.w, H: o.h, Pix: pix}
				}
			}
			delete(d.partial, id)
		}
	case SegEND:
		return d.endSet()
	}
	return nil, false
}

func (d *Decoder) endSet() (*Composition, bool) {
	s := d.pcs
	if len(s) < 11 {
		return nil, false
	}
	c := &Composition{PTS: d.pcsPTS, W: int(binary.BigEndian.Uint16(s)), H: int(binary.BigEndian.Uint16(s[2:])), Palette: d.palettes[s[9]]}
	n := int(s[10])
	for o := s[11:]; n > 0 && len(o) >= 8; n-- {
		id := binary.BigEndian.Uint16(o)
		p := Placed{ObjectID: id, WindowID: o[2], Forced: o[3]&0x40 != 0, Obj: d.objects[id],
			X: int(binary.BigEndian.Uint16(o[4:])), Y: int(binary.BigEndian.Uint16(o[6:]))}
		size := 8
		if o[3]&0x80 != 0 {
			if len(o) < 16 {
				break
			}
			p.CX, p.CY = int(binary.BigEndian.Uint16(o[8:])), int(binary.BigEndian.Uint16(o[10:]))
			p.CW, p.CH = int(binary.BigEndian.Uint16(o[12:])), int(binary.BigEndian.Uint16(o[14:]))
			size = 16
		} else if p.Obj != nil {
			p.CW, p.CH = p.Obj.W, p.Obj.H
		}
		if p.Obj != nil {
			p.CX, p.CY = min(p.CX, p.Obj.W), min(p.CY, p.Obj.H)
			p.CW, p.CH = min(p.CW, p.Obj.W-p.CX), min(p.CH, p.Obj.H-p.CY)
			c.Objects = append(c.Objects, p)
		}
		o = o[size:]
	}
	d.pcs = d.pcs[:0]
	return c, true
}

// Picture is a composition drawn: its pixels' luma and alpha over the
// part of the plane it covers.
type Picture struct {
	X, Y, W, H int
	Luma, A    []byte
}

// Draw draws a composition's objects into the smallest picture holding
// them.
func (c *Composition) Draw() *Picture {
	if c.Empty() {
		return nil
	}
	x0, y0, x1, y1 := 1<<30, 1<<30, 0, 0
	for _, p := range c.Objects {
		x0, y0 = min(x0, p.X), min(y0, p.Y)
		x1, y1 = max(x1, p.X+p.CW), max(y1, p.Y+p.CH)
	}
	if x1 <= x0 || y1 <= y0 {
		return nil
	}
	pic := &Picture{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
	pic.Luma = make([]byte, pic.W*pic.H)
	pic.A = make([]byte, pic.W*pic.H)
	for _, p := range c.Objects {
		for y := range p.CH {
			row := p.Obj.Pix[(p.CY+y)*p.Obj.W+p.CX:]
			for x := range p.CW {
				e := c.Palette[row[x]]
				i := (p.Y-y0+y)*pic.W + p.X - x0 + x
				if e[3] >= pic.A[i] {
					pic.Luma[i], pic.A[i] = e[0], e[3]
				}
			}
		}
	}
	return pic
}

// ReadSup reads a .sup file (each segment behind "PG", its PTS and DTS),
// calling fn with each segment.
func ReadSup(r io.Reader, fn func(pts int64, seg []byte) error) error {
	br := bufio.NewReader(r)
	var hdr [13]byte
	for {
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if hdr[0] != 'P' || hdr[1] != 'G' {
			return errors.New("pgs: not a .sup file")
		}
		pts := int64(binary.BigEndian.Uint32(hdr[2:]))
		n := int(binary.BigEndian.Uint16(hdr[11:]))
		seg := make([]byte, 3+n)
		copy(seg, hdr[10:13])
		if _, err := io.ReadFull(br, seg[3:]); err != nil {
			return err
		}
		if err := fn(pts, seg); err != nil {
			return err
		}
	}
}

// DecodeRLE expands PGS run-length data to w*h palette indices.
func DecodeRLE(b []byte, w, h int) ([]byte, error) {
	out := make([]byte, 0, w*h)
	row := 0
	for i := 0; i < len(b) && len(out) < w*h; {
		c := b[i]
		i++
		if c != 0 {
			out = append(out, c)
			continue
		}
		if i >= len(b) {
			break
		}
		f := b[i]
		i++
		if f == 0 { // end of line
			row++
			for len(out) < row*w && len(out) < w*h {
				out = append(out, 0)
			}
			continue
		}
		n := int(f & 0x3f)
		if f&0x40 != 0 {
			if i >= len(b) {
				return nil, errors.New("pgs: a truncated run")
			}
			n = n<<8 | int(b[i])
			i++
		}
		colour := byte(0)
		if f&0x80 != 0 {
			if i >= len(b) {
				return nil, errors.New("pgs: a truncated run")
			}
			colour = b[i]
			i++
		}
		for range min(n, w*h-len(out)) {
			out = append(out, colour)
		}
	}
	for len(out) < w*h {
		out = append(out, 0)
	}
	return out, nil
}
