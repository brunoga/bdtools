package convert

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/brunoga/bdtools/internal/mkv"
)

// Flat subtitles for a side-by-side file, as Kodi draws them. Playing a 3D
// file, Kodi takes any subtitle wider (or taller) than half its plane for
// one drawn side by side (or over-under) for both eyes: it keeps the left
// (top) half of the picture and places it on half the plane. A disc's wide
// line — two lines of dialogue are often more than 960 pixels — comes out
// cut in half and pushed off to the right. kodiPGS splits each object wider
// than half the plane into two objects side by side, each narrower than
// that: drawn as one by every player, and each half left alone by Kodi
// (which gets every object as a picture of its own).
//
// A composition holds at most two objects; a display set whose objects
// would be more split goes as it is.

// kodiPGS is a PGS source with its wide objects split in two.
type kodiPGS struct {
	src     mkv.Source
	width   int               // the plane's width, from the compositions
	objects map[uint16][]byte // object data being gathered (head, then data)
	split   map[uint16]split  // the epoch's split objects
	used    map[uint16]bool   // the epoch's object ids
}

// split is an object drawn as two: its left piece keeps its id, the right
// one, right wide, takes id.
type split struct {
	left, right int
	id          uint16
}

func newKodiPGS(src mkv.Source) *kodiPGS {
	return &kodiPGS{src: src, width: 1920, objects: map[uint16][]byte{}, split: map[uint16]split{}, used: map[uint16]bool{}}
}

// KodiPGS wraps a PGS source (display sets, as Matroska carries them) so
// that Kodi draws its wide subtitles whole in a side-by-side 3D film.
func KodiPGS(src mkv.Source) mkv.Source { return newKodiPGS(src) }

func (p *kodiPGS) Track() mkv.Track { return p.src.Track() }

func (p *kodiPGS) Next() (mkv.Frame, error) {
	f, err := p.src.Next()
	if err != nil {
		return f, err
	}
	// A display set it cannot read goes as it is: a subtitle Kodi may
	// draw badly is better than none.
	if data, err := p.rewrite(f.Data); err == nil {
		f.Data = data
	}
	return f, nil
}

// object is an object whose data segments this display set completes.
type object struct {
	id      uint16
	head    []byte // id and version
	w, h    int
	data    []byte
	segs    [][]byte // as they came
	first   int      // the index of its first segment
	pieces  [2][]byte
	isSplit bool
}

// rewrite splits a display set's wide objects.
func (p *kodiPGS) rewrite(ds []byte) ([]byte, error) {
	types, payloads, err := segments(ds)
	if err != nil {
		return nil, err
	}
	// The composition, and the objects the set carries, first: whether an
	// object is split depends on how many the composition shows.
	var pcs []byte
	var objs []*object
	pending := map[uint16]*object{}
	for i, t := range types {
		s := payloads[i]
		switch t {
		case segPCS:
			if len(s) < 11 {
				return nil, errors.New("a short composition")
			}
			pcs = s
			p.width = int(binary.BigEndian.Uint16(s))
			if s[7]&0x80 != 0 { // epoch start: objects begin anew
				clear(p.split)
				clear(p.used)
			}
		case segODS:
			if len(s) < 4 {
				return nil, errors.New("a short object segment")
			}
			id := binary.BigEndian.Uint16(s)
			p.used[id] = true
			o := pending[id]
			if s[3]&0x80 != 0 {
				if len(s) < 11 {
					return nil, errors.New("a short object segment")
				}
				o = &object{id: id, head: s[:3], w: int(binary.BigEndian.Uint16(s[7:])), h: int(binary.BigEndian.Uint16(s[9:])),
					data: append([]byte(nil), s[11:]...), first: i}
				pending[id] = o
			} else if o != nil {
				o.data = append(o.data, s[4:]...)
			}
			if o != nil {
				o.segs = append(o.segs, s)
				if s[3]&0x40 != 0 {
					delete(pending, id)
					objs = append(objs, o)
				}
			}
		}
	}
	half := p.width / 2
	// The objects redefined here: split if wide.
	for _, o := range objs {
		delete(p.split, o.id)
		if o.w <= half || o.w > 2*half || o.h == 0 {
			continue
		}
		pix, err := decodeRLE(o.data, o.w, o.h)
		if err != nil {
			return nil, fmt.Errorf("object %d: %w", o.id, err)
		}
		lw := (o.w + 1) / 2
		rw := o.w - lw
		l, r := make([]byte, lw*o.h), make([]byte, rw*o.h)
		for y := range o.h {
			copy(l[y*lw:(y+1)*lw], pix[y*o.w:y*o.w+lw])
			copy(r[y*rw:(y+1)*rw], pix[y*o.w+lw:(y+1)*o.w])
		}
		id, ok := p.freeID()
		if !ok {
			continue
		}
		p.used[id] = true
		o.isSplit = true
		o.pieces = [2][]byte{encodeRLE(l, lw, o.h), encodeRLE(r, rw, o.h)}
		p.split[o.id] = split{left: lw, right: rw, id: id}
	}
	// The composition, each split object's reference made two; too many
	// for a composition, and the set goes as it was.
	newPCS := pcs
	if pcs != nil {
		var ok bool
		if newPCS, ok = p.composition(pcs); !ok {
			for _, o := range objs {
				if o.isSplit {
					delete(p.split, o.id)
					o.isSplit = false
				}
			}
			newPCS = pcs
		}
	}
	var out []byte
	for i, t := range types {
		s := payloads[i]
		switch t {
		case segPCS:
			out = appendSegment(out, t, newPCS)
		case segODS:
			id := binary.BigEndian.Uint16(s)
			var o *object
			for _, c := range objs {
				if c.id == id && i >= c.first && c.isSplit {
					o = c
				}
			}
			if o == nil {
				out = appendSegment(out, t, s)
				continue
			}
			if i != o.first {
				continue // written whole at its first segment
			}
			sp := p.split[o.id]
			right := []byte{byte(sp.id >> 8), byte(sp.id), o.head[2]}
			for _, seg := range objectSegments(o.head, sp.left, o.h, o.pieces[0]) {
				out = appendSegment(out, segODS, seg)
			}
			for _, seg := range objectSegments(right, sp.right, o.h, o.pieces[1]) {
				out = appendSegment(out, segODS, seg)
			}
		default:
			out = appendSegment(out, t, s)
		}
	}
	return out, nil
}

// freeID is an object id the epoch has not used.
func (p *kodiPGS) freeID() (uint16, bool) {
	for id := range uint16(64) {
		if !p.used[id] {
			return id, true
		}
	}
	return 0, false
}

// composition lists each split object as its two pieces, cropping each to
// what the reference's crop leaves of it; false when that makes more than
// a composition holds.
func (p *kodiPGS) composition(s []byte) ([]byte, bool) {
	n := int(s[10])
	out := append([]byte(nil), s[:11]...)
	count := 0
	for o := s[11:]; n > 0 && len(o) >= 8; n-- {
		size := 8
		if o[3]&0x80 != 0 {
			size = 16
		}
		if len(o) < size {
			break
		}
		c := o[:size]
		o = o[size:]
		id := binary.BigEndian.Uint16(c)
		sp, ok := p.split[id]
		if !ok {
			out = append(out, c...)
			count++
			continue
		}
		x := int(binary.BigEndian.Uint16(c[4:]))
		cx, cw := 0, sp.left+sp.right // the reference's columns of the object
		if size == 16 {
			cx, cw = int(binary.BigEndian.Uint16(c[8:])), int(binary.BigEndian.Uint16(c[12:]))
		}
		for _, piece := range [2]struct {
			id    uint16
			at, w int
		}{{id, 0, sp.left}, {sp.id, sp.left, sp.right}} {
			lo, hi := max(cx, piece.at), min(cx+cw, piece.at+piece.w)
			if lo >= hi {
				continue
			}
			e := append([]byte(nil), c...)
			binary.BigEndian.PutUint16(e, piece.id)
			binary.BigEndian.PutUint16(e[4:], uint16(x+lo-cx)) //nolint:gosec // inside the plane
			if size == 16 {
				binary.BigEndian.PutUint16(e[8:], uint16(lo-piece.at)) //nolint:gosec // inside the object
				binary.BigEndian.PutUint16(e[12:], uint16(hi-lo))      //nolint:gosec // inside the object
			}
			out = append(out, e...)
			count++
		}
	}
	if count > 2 {
		return nil, false
	}
	out[10] = byte(count)
	return out, true
}
