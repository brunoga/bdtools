package convert

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/brunoga/mvc/internal/mkv"
)

// Subtitles for a side-by-side frame. A Blu-ray's PGS subtitles are drawn
// once, on a 1920x1080 plane, and a 3D player shifts that plane apart in
// the two eyes by the offset the disc gives (see depth.go). A side-by-side
// file has no such player: the subtitle has to be in the frame's two
// halves already, at their offsets. pgs3D rewrites each display set so:
//
//   - the composition is as wide as the stacked frame, and lists every
//     object twice, once in each eye's half, shifted by the offset at the
//     display set's start (right in the left eye and left in the right one
//     for an offset toward the viewer), and kept inside its half;
//   - every window is defined twice, the right eye's with its id plus
//     rightWindow, shifted with its objects;
//   - for half side by side, each object is squeezed to half its width
//     (each pair of pixels becomes the more opaque one, so thin strokes
//     survive), and positions and offsets are halved.
//
// Palettes and the end segment pass through. The offset is the one at the
// first frame a display set shows; a disc that moves a subtitle's depth
// while it is up is followed from the next display set on.

const (
	segPDS = 0x14
	segODS = 0x15
	segPCS = 0x16
	segWDS = 0x17
	segEND = 0x80

	// rightWindow is added to a window's id for its right eye copy.
	rightWindow = 0x80
)

// pgs3D is a PGS source rewritten for a side-by-side frame.
type pgs3D struct {
	src    mkv.Source
	track  mkv.Track
	half   bool
	offset func(time.Duration) int // pixels at the source's width, positive toward the viewer

	width    int                // the source plane's width, from its compositions
	windows  map[byte]pgsWindow // as the source defines them
	palettes map[byte]*[256]byte
	palette  byte              // the composition's palette
	objects  map[uint16][]byte // object data being gathered, for squeezing
}

type pgsWindow struct{ x, y, w, h int }

func newPGS3D(src mkv.Source, half bool, offset func(time.Duration) int) *pgs3D {
	t := src.Track()
	if t.Name == "" {
		t.Name = "3D"
	} else {
		t.Name += " (3D)"
	}
	return &pgs3D{src: src, track: t, half: half, offset: offset, width: 1920,
		windows: map[byte]pgsWindow{}, palettes: map[byte]*[256]byte{}, objects: map[uint16][]byte{}}
}

func (p *pgs3D) Track() mkv.Track { return p.track }

func (p *pgs3D) Next() (mkv.Frame, error) {
	f, err := p.src.Next()
	if err != nil {
		return f, err
	}
	data, err := p.rewrite(f.Data, p.offset(f.PTS))
	if err != nil {
		return f, fmt.Errorf("3D subtitles at %s: %w", f.PTS, err)
	}
	f.Data = data
	return f, nil
}

// segments splits a display set into its segments' types and payloads.
func segments(ds []byte) ([]byte, [][]byte, error) {
	var types []byte
	var payloads [][]byte
	for len(ds) > 0 {
		if len(ds) < 3 {
			return nil, nil, errors.New("a truncated segment")
		}
		n := int(binary.BigEndian.Uint16(ds[1:]))
		if 3+n > len(ds) {
			return nil, nil, errors.New("a truncated segment")
		}
		types = append(types, ds[0])
		payloads = append(payloads, ds[3:3+n])
		ds = ds[3+n:]
	}
	return types, payloads, nil
}

func appendSegment(out []byte, typ byte, payload []byte) []byte {
	out = append(out, typ, byte(len(payload)>>8), byte(len(payload)))
	return append(out, payload...)
}

// rewrite turns one display set into its side-by-side form.
func (p *pgs3D) rewrite(ds []byte, offset int) ([]byte, error) {
	types, payloads, err := segments(ds)
	if err != nil {
		return nil, err
	}
	// What the set defines first: the plane's width, its windows, its
	// palettes. The composition refers to them.
	for i, t := range types {
		s := payloads[i]
		switch t {
		case segPCS:
			if len(s) < 11 {
				return nil, errors.New("a short composition")
			}
			p.width = int(binary.BigEndian.Uint16(s))
			p.palette = s[9]
		case segWDS:
			if len(s) < 1 || len(s) < 1+9*int(s[0]) {
				return nil, errors.New("a short window definition")
			}
			for k := range int(s[0]) {
				w := s[1+9*k:]
				p.windows[w[0]] = pgsWindow{int(binary.BigEndian.Uint16(w[1:])), int(binary.BigEndian.Uint16(w[3:])),
					int(binary.BigEndian.Uint16(w[5:])), int(binary.BigEndian.Uint16(w[7:]))}
			}
		case segPDS:
			p.readPalette(s)
		}
	}
	eye, scale := p.width, 1
	if p.half {
		eye, scale = p.width/2, 2
	}
	off := offset / scale
	// shifts is how far a window and its objects move in each eye: the
	// offset, as far as the window stays in its half.
	shifts := func(id byte) (left, right int) {
		w, ok := p.windows[id]
		if !ok {
			return off, -off
		}
		x, ww := w.x/scale, (w.w+scale-1)/scale
		clamp := func(s int) int { return min(max(s, -x), eye-(x+ww)) }
		return clamp(off), clamp(-off)
	}

	var out []byte
	for i, t := range types {
		s := payloads[i]
		switch t {
		case segPCS:
			out = appendSegment(out, segPCS, p.composition(s, eye, scale, shifts))
		case segWDS:
			out = appendSegment(out, segWDS, p.windowDefinition(s, eye, scale, shifts))
		case segODS:
			if !p.half {
				out = appendSegment(out, t, s)
				continue
			}
			squeezed, err := p.squeezeObject(s)
			if err != nil {
				return nil, err
			}
			for _, seg := range squeezed {
				out = appendSegment(out, segODS, seg)
			}
		default:
			out = appendSegment(out, t, s)
		}
	}
	return out, nil
}

// composition rewrites a presentation composition segment.
func (p *pgs3D) composition(s []byte, eye, scale int, shifts func(byte) (int, int)) []byte {
	out := append([]byte(nil), s[:11]...)
	binary.BigEndian.PutUint16(out, uint16(2*eye)) //nolint:gosec // a frame width
	n := int(s[10])
	var objs [][]byte
	for o := s[11:]; len(objs) < n && len(o) >= 8; {
		size := 8
		if o[3]&0x80 != 0 { // cropped
			size = 16
		}
		if len(o) < size {
			break
		}
		objs = append(objs, o[:size])
		o = o[size:]
	}
	out[10] = byte(2 * len(objs)) //nolint:gosec // at most 2 x 255, as a decoder takes it
	for side := range 2 {
		for _, o := range objs {
			c := append([]byte(nil), o...)
			left, right := shifts(o[2])
			x := int(binary.BigEndian.Uint16(o[4:])) / scale
			if side == 0 {
				x += left
			} else {
				x += eye + right
				c[2] += rightWindow
			}
			binary.BigEndian.PutUint16(c[4:], uint16(max(x, 0))) //nolint:gosec // inside the frame
			if scale > 1 && len(c) == 16 {
				// The crop is in the object's own pixels, which are halved.
				binary.BigEndian.PutUint16(c[8:], binary.BigEndian.Uint16(c[8:])/2)
				binary.BigEndian.PutUint16(c[12:], (binary.BigEndian.Uint16(c[12:])+1)/2)
			}
			out = append(out, c...)
		}
	}
	return out
}

// windowDefinition rewrites a window definition segment: each window once
// per eye.
func (p *pgs3D) windowDefinition(s []byte, eye, scale int, shifts func(byte) (int, int)) []byte {
	n := int(s[0])
	out := []byte{byte(2 * n)} //nolint:gosec // at most 2 x 255
	for side := range 2 {
		for k := range n {
			w := s[1+9*k : 1+9*(k+1)]
			c := append([]byte(nil), w...)
			left, right := shifts(w[0])
			x := int(binary.BigEndian.Uint16(w[1:])) / scale
			if side == 0 {
				x += left
			} else {
				x += eye + right
				c[0] += rightWindow
			}
			binary.BigEndian.PutUint16(c[1:], uint16(max(x, 0)))                                           //nolint:gosec // inside the frame
			binary.BigEndian.PutUint16(c[5:], uint16((int(binary.BigEndian.Uint16(w[5:]))+scale-1)/scale)) //nolint:gosec // a width
			out = append(out, c...)
		}
	}
	return out
}

// readPalette records a palette's alpha values, which squeezing needs.
func (p *pgs3D) readPalette(s []byte) {
	if len(s) < 2 {
		return
	}
	a := p.palettes[s[0]]
	if a == nil {
		a = new([256]byte)
		p.palettes[s[0]] = a
	}
	for e := s[2:]; len(e) >= 5; e = e[5:] {
		a[e[0]] = e[4]
	}
}

// squeezeObject gathers an object's data segments and, at its last,
// returns it squeezed to half width as new segments; nothing before that.
func (p *pgs3D) squeezeObject(s []byte) ([][]byte, error) {
	if len(s) < 4 {
		return nil, errors.New("a short object segment")
	}
	id := binary.BigEndian.Uint16(s)
	first, last := s[3]&0x80 != 0, s[3]&0x40 != 0
	if first {
		if len(s) < 11 {
			return nil, errors.New("a short object segment")
		}
		// Keep the id, version, width, height and data; the length and the
		// flags are written anew.
		p.objects[id] = append(append([]byte(nil), s[:3]...), s[7:]...)
	} else if p.objects[id] == nil {
		return nil, errors.New("object data before its first segment")
	} else {
		p.objects[id] = append(p.objects[id], s[4:]...)
	}
	if !last {
		return nil, nil
	}
	obj := p.objects[id]
	delete(p.objects, id)
	w, h := int(binary.BigEndian.Uint16(obj[3:])), int(binary.BigEndian.Uint16(obj[5:]))
	if w == 0 || h == 0 || w > 4096 || h > 4096 { // the largest object PGS allows
		return nil, fmt.Errorf("object %d is %dx%d", id, w, h)
	}
	pix, err := decodeRLE(obj[7:], w, h)
	if err != nil {
		return nil, fmt.Errorf("object %d: %w", id, err)
	}
	alpha := p.palettes[p.palette]
	if alpha == nil {
		alpha = new([256]byte)
	}
	nw := (w + 1) / 2
	half := make([]byte, nw*h)
	for y := range h {
		for x := range nw {
			a := pix[y*w+2*x]
			if 2*x+1 < w {
				if b := pix[y*w+2*x+1]; alpha[b] > alpha[a] {
					a = b
				}
			}
			half[y*nw+x] = a
		}
	}
	return objectSegments(obj[:3], nw, h, encodeRLE(half, nw, h)), nil
}

// objectSegments splits an object's data into segments: the first carries
// the data length and the size, and none is larger than a segment can be.
func objectSegments(head []byte, w, h int, rle []byte) [][]byte {
	const maxSegment = 0xffff
	total := len(rle) + 4
	first := append(append([]byte(nil), head...), 0x80, byte(total>>16), byte(total>>8), byte(total),
		byte(w>>8), byte(w), byte(h>>8), byte(h))
	n := min(len(rle), maxSegment-len(first))
	segs := [][]byte{append(first, rle[:n]...)}
	rle = rle[n:]
	for len(rle) > 0 {
		c := append(append([]byte(nil), head...), 0)
		n := min(len(rle), maxSegment-len(c))
		segs = append(segs, append(c, rle[:n]...))
		rle = rle[n:]
	}
	segs[len(segs)-1][3] |= 0x40 // last in sequence
	return segs
}

// decodeRLE expands PGS run-length data to w*h palette indices.
func decodeRLE(b []byte, w, h int) ([]byte, error) {
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
				return nil, errors.New("truncated run")
			}
			n = n<<8 | int(b[i])
			i++
		}
		colour := byte(0)
		if f&0x80 != 0 {
			if i >= len(b) {
				return nil, errors.New("truncated run")
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

// encodeRLE is decodeRLE's inverse, line by line.
func encodeRLE(pix []byte, w, h int) []byte {
	var out []byte
	for y := range h {
		row := pix[y*w : (y+1)*w]
		for x := 0; x < w; {
			c := row[x]
			n := 1
			for x+n < w && row[x+n] == c && n < 0x3fff {
				n++
			}
			switch {
			case c != 0 && n <= 2:
				for range n {
					out = append(out, c)
				}
			case c == 0 && n < 64:
				out = append(out, 0, byte(n))
			case c == 0:
				out = append(out, 0, 0x40|byte(n>>8), byte(n))
			case n < 64:
				out = append(out, 0, 0x80|byte(n), c)
			default:
				out = append(out, 0, 0xc0|byte(n>>8), byte(n), c)
			}
			x += n
		}
		out = append(out, 0, 0)
	}
	return out
}

// subtitles3D wraps a subtitle track for the side-by-side frame, at the
// depth its offset sequence gives.
func (r *Runner) subtitles3D(src mkv.Source, t Track) mkv.Source {
	seq := -1
	if r.offsetSequence != nil {
		seq = r.offsetSequence(t)
	}
	switch {
	case r.depth == nil || len(r.depth.gops) == 0:
		r.Report.Report("3D subtitles: track %d: the source carries no offset metadata, so they sit at the screen", t.ID)
	case seq == frontMost:
		r.Report.Report("3D subtitles: track %d: the source does not say which offset sequence it follows; "+
			"using the one nearest the viewer at each moment", t.ID)
	case seq < 0:
		r.Report.Report("3D subtitles: track %d follows no offset sequence, so it sits at the screen", t.ID)
	default:
		r.Report.Report("3D subtitles: track %d follows offset sequence %d", t.ID, seq)
	}
	return newPGS3D(src, r.Opts.Layout == LayoutHalfSBS, func(at time.Duration) int { return r.depth.offset(seq, at) })
}
