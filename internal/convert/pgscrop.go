package convert

import (
	"encoding/binary"
	"fmt"

	"github.com/brunoga/bdtools/internal/mkv"
)

// Subtitles for a cropped picture. A PGS track draws on a plane the size
// of the source's picture (1920x1080 on a disc, a UHD disc's included),
// and a player fits that plane to the video it plays: cropped video
// under the source's plane would squeeze the subtitles and put them in
// the wrong place. pgsCrop cuts the plane as the picture is cut (the crop
// taken to the plane's scale), and moves each window, with the objects
// in it, by the cut, as far as it stays inside: a subtitle the disc put
// in a black bar comes up into the picture. A scaled picture needs
// nothing more, as the plane is scaled to it.

// pgsCrop is a PGS source rewritten for a cropped picture.
type pgsCrop struct {
	src            mkv.Source
	crop           Crop // in the video's samples
	videoW, videoH int  // the source picture's size

	width, height int                // the source plane's, from its compositions
	windows       map[byte]pgsWindow // as the source defines them
}

func newPGSCrop(src mkv.Source, crop Crop, videoW, videoH int) *pgsCrop {
	return &pgsCrop{src: src, crop: crop, videoW: videoW, videoH: videoH, width: 1920, height: 1080,
		windows: map[byte]pgsWindow{}}
}

func (p *pgsCrop) Track() mkv.Track { return p.src.Track() }

func (p *pgsCrop) Next() (mkv.Frame, error) {
	f, err := p.src.Next()
	if err != nil {
		return f, err
	}
	data, err := p.rewrite(f.Data)
	if err != nil {
		return f, fmt.Errorf("subtitles at %s: %w", f.PTS, err)
	}
	f.Data = data
	return f, nil
}

// cut is the crop on the plane, and the plane's size after it.
func (p *pgsCrop) cut() (left, top, w, h int) {
	sx := func(v int) int { return (v*p.width + p.videoW/2) / p.videoW }
	sy := func(v int) int { return (v*p.height + p.videoH/2) / p.videoH }
	left, top = sx(p.crop.Left), sy(p.crop.Top)
	return left, top, p.width - left - sx(p.crop.Right), p.height - top - sy(p.crop.Bottom)
}

// moved is a window's position on the cut plane: moved by the cut, then
// into the plane as far as it fits.
func (p *pgsCrop) moved(win pgsWindow) (x, y int) {
	left, top, w, h := p.cut()
	return min(max(win.x-left, 0), max(w-win.w, 0)), min(max(win.y-top, 0), max(h-win.h, 0))
}

// shift is how far window id and its objects move.
func (p *pgsCrop) shift(id byte) (dx, dy int) {
	win, ok := p.windows[id]
	if !ok {
		left, top, _, _ := p.cut()
		return -left, -top
	}
	x, y := p.moved(win)
	return x - win.x, y - win.y
}

// rewrite cuts one display set's plane. A set it cannot read goes as it
// is: a player skips it as it would have.
func (p *pgsCrop) rewrite(ds []byte) ([]byte, error) {
	types, payloads, err := segments(ds)
	if err != nil {
		return ds, nil //nolint:nilerr // passed on unread
	}
	for i, t := range types {
		s := payloads[i]
		switch t {
		case segPCS:
			if len(s) < 11 {
				return ds, nil
			}
			p.width = int(binary.BigEndian.Uint16(s))
			p.height = int(binary.BigEndian.Uint16(s[2:]))
		case segWDS:
			if len(s) < 1 || len(s) < 1+9*int(s[0]) {
				return ds, nil
			}
			for k := range int(s[0]) {
				w := s[1+9*k:]
				p.windows[w[0]] = pgsWindow{int(binary.BigEndian.Uint16(w[1:])), int(binary.BigEndian.Uint16(w[3:])),
					int(binary.BigEndian.Uint16(w[5:])), int(binary.BigEndian.Uint16(w[7:]))}
			}
		}
	}
	if p.width <= 0 || p.height <= 0 || p.videoW <= 0 || p.videoH <= 0 {
		return ds, nil
	}
	_, _, w, h := p.cut()
	put := func(b []byte, v int) { binary.BigEndian.PutUint16(b, uint16(max(v, 0))) } //nolint:gosec // on the plane
	var out []byte
	for i, t := range types {
		s := append([]byte(nil), payloads[i]...)
		switch t {
		case segPCS:
			put(s, w)
			put(s[2:], h)
			n := int(s[10])
			for o, k := s[11:], 0; k < n && len(o) >= 8; k++ {
				size := 8
				if o[3]&0x80 != 0 { // cropped
					size = 16
				}
				if len(o) < size {
					break
				}
				dx, dy := p.shift(o[2])
				put(o[4:], int(binary.BigEndian.Uint16(o[4:]))+dx)
				put(o[6:], int(binary.BigEndian.Uint16(o[6:]))+dy)
				o = o[size:]
			}
		case segWDS:
			for k := range int(s[0]) {
				win := s[1+9*k:]
				x, y := p.moved(p.windows[win[0]])
				put(win[1:], x)
				put(win[3:], y)
			}
		}
		out = appendSegment(out, t, s)
	}
	return out, nil
}
