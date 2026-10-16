package convert

import (
	"encoding/binary"
	"sort"
	"sync"
	"time"

	"github.com/brunoga/bdtools/internal/gpu"
)

// Subtitles burned into the picture (--burn-subs), for an output that
// carries no subtitle track a player shows (an MV-HEVC .mp4: its players
// take text, and a Blu-ray's are pictures). pgsBurner follows a PGS
// track's display sets as the demuxer reads them and draws what shows at
// each picture's time into each eye, apart by the disc's 3D offset at
// that moment: right in the left eye and left in the right one for a
// subtitle in front of the screen, as a 3D player shifts the plane.

// pgsObject is a PGS object: its size and run-length data, expanded to
// palette indices the first time it is drawn.
type pgsObject struct {
	w, h int
	rle  []byte
	pix  []byte
	once sync.Once
}

func (o *pgsObject) pixels() []byte {
	o.once.Do(func() {
		pix, err := decodeRLE(o.rle, o.w, o.h)
		if err != nil {
			pix = make([]byte, o.w*o.h)
		}
		o.pix = pix
	})
	return o.pix
}

// pgsPlaced is an object as a composition shows it: where, and the part
// of it (the whole when not cropped).
type pgsPlaced struct {
	obj            *pgsObject
	x, y           int
	cx, cy, cw, ch int
	cropped        bool
}

// pgsPalette is a palette's entries: Y, Cr, Cb and alpha.
type pgsPalette [256][4]byte

// pgsEvent is a display set's composition from its time on.
type pgsEvent struct {
	pts    int64 // 90 kHz, the output's timeline
	placed []pgsPlaced
	pal    *pgsPalette
	w, h   int // the plane's size
}

type pgsBurner struct {
	mu sync.Mutex
	// What the display sets define, as they arrive.
	palettes map[byte]*pgsPalette
	objects  map[uint16]*pgsObject
	partial  map[uint16]*pgsObject
	pcs      []byte // the set's composition, until its end segment
	pcsPTS   int64
	events   []pgsEvent
	// What shows.
	shown pgsEvent
}

func newPGSBurner() *pgsBurner {
	return &pgsBurner{palettes: map[byte]*pgsPalette{}, objects: map[uint16]*pgsObject{}, partial: map[uint16]*pgsObject{}}
}

// feed takes a segment of the track at its PTS (the demuxer's goroutine).
func (b *pgsBurner) feed(pts int64, seg []byte) {
	if len(seg) < 3 {
		return
	}
	typ, s := seg[0], seg[3:]
	b.mu.Lock()
	defer b.mu.Unlock()
	switch typ {
	case segPCS:
		if len(s) < 11 {
			return
		}
		if s[7]&0x80 != 0 { // epoch start: the buffers begin afresh
			b.palettes = map[byte]*pgsPalette{}
			b.objects = map[uint16]*pgsObject{}
			b.partial = map[uint16]*pgsObject{}
		}
		b.pcs = append(b.pcs[:0], s...)
		b.pcsPTS = pts
	case segPDS:
		if len(s) < 2 {
			return
		}
		p := new(pgsPalette)
		if old := b.palettes[s[0]]; old != nil {
			*p = *old
		}
		for e := s[2:]; len(e) >= 5; e = e[5:] {
			p[e[0]] = [4]byte{e[1], e[2], e[3], e[4]}
		}
		b.palettes[s[0]] = p // a new one: events keep the old
	case segODS:
		if len(s) < 4 {
			return
		}
		id := binary.BigEndian.Uint16(s)
		flags := s[3]
		data := s[4:]
		if flags&0x80 != 0 { // first in sequence: its length and size
			if len(data) < 7 {
				return
			}
			o := &pgsObject{w: int(binary.BigEndian.Uint16(data[3:])), h: int(binary.BigEndian.Uint16(data[5:]))}
			o.rle = append(o.rle, data[7:]...)
			b.partial[id] = o
		} else if o := b.partial[id]; o != nil {
			o.rle = append(o.rle, data...)
		}
		if flags&0x40 != 0 { // last in sequence
			if o := b.partial[id]; o != nil && o.w > 0 && o.h > 0 && o.w*o.h <= 4096*2160 {
				b.objects[id] = o
			}
			delete(b.partial, id)
		}
	case segEND:
		b.endSet()
	}
}

// endSet (with mu held) makes the set's composition an event.
func (b *pgsBurner) endSet() {
	s := b.pcs
	if len(s) < 11 {
		return
	}
	ev := pgsEvent{pts: b.pcsPTS, w: int(binary.BigEndian.Uint16(s)), h: int(binary.BigEndian.Uint16(s[2:])), pal: b.palettes[s[9]]}
	n := int(s[10])
	for c := s[11:]; n > 0 && len(c) >= 8; n-- {
		id := binary.BigEndian.Uint16(c)
		p := pgsPlaced{obj: b.objects[id], x: int(binary.BigEndian.Uint16(c[4:])), y: int(binary.BigEndian.Uint16(c[6:]))}
		size := 8
		if c[3]&0x80 != 0 {
			if len(c) < 16 {
				break
			}
			p.cropped = true
			p.cx, p.cy = int(binary.BigEndian.Uint16(c[8:])), int(binary.BigEndian.Uint16(c[10:]))
			p.cw, p.ch = int(binary.BigEndian.Uint16(c[12:])), int(binary.BigEndian.Uint16(c[14:]))
			size = 16
		}
		if p.obj != nil {
			ev.placed = append(ev.placed, p)
		}
		c = c[size:]
	}
	b.pcs = b.pcs[:0]
	// Sets come in time order; one out of it goes in its place.
	i := sort.Search(len(b.events), func(i int) bool { return b.events[i].pts > ev.pts })
	b.events = append(b.events, pgsEvent{})
	copy(b.events[i+1:], b.events[i:])
	b.events[i] = ev
}

// at advances to what shows at t and returns it.
func (b *pgsBurner) at(t time.Duration) pgsEvent {
	pts := int64(t) * 90000 / int64(time.Second)
	b.mu.Lock()
	defer b.mu.Unlock()
	k := 0
	for k < len(b.events) && b.events[k].pts <= pts {
		b.shown = b.events[k]
		k++
	}
	b.events = b.events[k:]
	return b.shown
}

// draw draws the subtitle showing at t into the eyes, the left shifted
// right by offset (positive toward the viewer) and the right left.
func (b *pgsBurner) draw(left, right *gpu.Picture, w, h int, t time.Duration, offset int) {
	ev := b.at(t)
	if len(ev.placed) == 0 || ev.pal == nil {
		return
	}
	for i, pic := range []*gpu.Picture{left, right} {
		dx := offset
		if i == 1 {
			dx = -offset
		}
		for _, p := range ev.placed {
			drawObject(pic, w, h, ev, p, dx)
		}
	}
}

// drawObject blends an object into a picture (NV12 or P010) of w x h,
// shifted dx, scaling the plane's coordinates to the picture's.
func drawObject(pic *gpu.Picture, w, h int, ev pgsEvent, p pgsPlaced, dx int) {
	o := p.obj
	pix := o.pixels()
	x0, y0, cw, ch := 0, 0, o.w, o.h
	if p.cropped {
		x0, y0 = min(p.cx, o.w), min(p.cy, o.h)
		cw, ch = min(p.cw, o.w-x0), min(p.ch, o.h-y0)
	}
	sx, sy := 1.0, 1.0
	if ev.w > 0 && ev.h > 0 {
		sx, sy = float64(w)/float64(ev.w), float64(h)/float64(ev.h)
	}
	ox := int(float64(p.x+dx)*sx + 0.5)
	oy := int(float64(p.y)*sy + 0.5)
	dw, dh := int(float64(cw)*sx+0.5), int(float64(ch)*sy+0.5)
	wide := pic.Depth > 8
	put := func(plane []byte, off int, v byte, a int) {
		if wide {
			old := int(binary.LittleEndian.Uint16(plane[off:])) >> 6
			nv := (a*(int(v)<<2) + (255-a)*old + 127) / 255
			binary.LittleEndian.PutUint16(plane[off:], uint16(nv<<6)) //nolint:gosec // 10 bits
			return
		}
		plane[off] = byte((a*int(v) + (255-a)*int(plane[off]) + 127) / 255)
	}
	bps := 1
	if wide {
		bps = 2
	}
	for j := range dh {
		y := oy + j
		if y < 0 || y >= h {
			continue
		}
		srcY := y0 + min(int(float64(j)/sy), ch-1)
		for i := range dw {
			x := ox + i
			if x < 0 || x >= w {
				continue
			}
			srcX := x0 + min(int(float64(i)/sx), cw-1)
			e := ev.pal[pix[srcY*o.w+srcX]]
			a := int(e[3])
			if a == 0 {
				continue
			}
			put(pic.Y, y*pic.Pitch+x*bps, e[0], a)
			if x&1 == 0 && y&1 == 0 {
				c := (y/2)*pic.Pitch + x*bps
				put(pic.UV, c, e[2], a)     // Cb
				put(pic.UV, c+bps, e[1], a) // Cr
			}
		}
	}
}
