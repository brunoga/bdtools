package main

import (
	"encoding/binary"
	"os"
	"strings"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
)

// mkSup writes a .sup of subtitles drawn here (Liberation Sans, white,
// outlined), for tests: each cue's lines centred one above the other,
// shown at its time and cleared at its end.
func mkSup(path string, cues []srtCue) error {
	b, err := os.ReadFile("/usr/share/fonts/truetype/liberation/LiberationSans-Regular.ttf")
	if err != nil {
		return err
	}
	f, err := opentype.Parse(b)
	if err != nil {
		return err
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: 46, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		return err
	}
	var out []byte
	seg := func(pts int64, typ byte, payload []byte) {
		out = append(out, 'P', 'G')
		out = binary.BigEndian.AppendUint32(out, uint32(pts)) //nolint:gosec // test times
		out = binary.BigEndian.AppendUint32(out, uint32(pts)) //nolint:gosec // test times
		out = append(out, typ, byte(len(payload)>>8), byte(len(payload)))
		out = append(out, payload...)
	}
	ticks := func(t time.Duration) int64 { return int64(t) * 90000 / int64(time.Second) }
	for n, c := range cues {
		// Draw the lines, each with its outline, stacked.
		var w int
		var parts []*drawn
		for _, l := range strings.Split(c.text, "\n") {
			d := drawLine(face, l)
			parts = append(parts, d)
			w = max(w, d.w)
		}
		h := 0
		for _, d := range parts {
			h += d.h
		}
		ink := make([]float32, w*h)
		op := make([]float32, w*h)
		y := 0
		for _, d := range parts {
			x0 := (w - d.w) / 2
			for yy := range d.h {
				for xx := range d.w {
					ink[(y+yy)*w+x0+xx] = d.ink[yy*d.w+xx]
					op[(y+yy)*w+x0+xx] = d.op[yy*d.w+xx]
				}
			}
			y += d.h
		}
		// The palette: 16 levels of opacity, each white or black.
		pix := make([]byte, w*h)
		for i := range pix {
			a := int(op[i]*15 + 0.5)
			if a == 0 {
				continue
			}
			white := ink[i] > op[i]/2
			pix[i] = byte(1 + (a-1)*2)
			if white {
				pix[i]++
			}
		}
		var pds []byte
		pds = append(pds, 0, 0)
		for a := 1; a <= 15; a++ {
			alpha := byte(a * 17)
			pds = append(pds, byte(1+(a-1)*2), 16, 128, 128, alpha)  // black
			pds = append(pds, byte(2+(a-1)*2), 235, 128, 128, alpha) // white
		}
		x, yPos := (1920-w)/2, 1080-80-h
		pcs := func(state byte, objects bool) []byte {
			p := []byte{0x07, 0x80, 0x04, 0x38, 0x10, byte(n >> 8), byte(n), state, 0, 0}
			if !objects {
				return append(p, 0)
			}
			p = append(p, 1, 0, 0, 0, 0)
			p = binary.BigEndian.AppendUint16(p, uint16(x))    //nolint:gosec // on the plane
			p = binary.BigEndian.AppendUint16(p, uint16(yPos)) //nolint:gosec // on the plane
			return p
		}
		wds := []byte{1, 0}
		wds = binary.BigEndian.AppendUint16(wds, uint16(x))    //nolint:gosec // on the plane
		wds = binary.BigEndian.AppendUint16(wds, uint16(yPos)) //nolint:gosec // on the plane
		wds = binary.BigEndian.AppendUint16(wds, uint16(w))    //nolint:gosec // small
		wds = binary.BigEndian.AppendUint16(wds, uint16(h))    //nolint:gosec // small
		rle := encodeRLE(pix, w, h)
		ods := []byte{0, 0, 0, 0xc0}
		n3 := len(rle) + 4
		ods = append(ods, byte(n3>>16), byte(n3>>8), byte(n3))
		ods = binary.BigEndian.AppendUint16(ods, uint16(w)) //nolint:gosec // small
		ods = binary.BigEndian.AppendUint16(ods, uint16(h)) //nolint:gosec // small
		ods = append(ods, rle...)
		at := ticks(c.start)
		seg(at, 0x16, pcs(0x80, true))
		seg(at, 0x17, wds)
		seg(at, 0x14, pds)
		seg(at, 0x15, ods)
		seg(at, 0x80, nil)
		end := ticks(c.end)
		seg(end, 0x16, pcs(0, false))
		seg(end, 0x17, wds)
		seg(end, 0x80, nil)
	}
	return os.WriteFile(path, out, 0o600)
}

// drawn is a line drawn: its ink and opacity.
type drawn struct {
	w, h    int
	ink, op []float32
}

func drawLine(face font.Face, text string) *drawn {
	b, _ := renderWith(face, text, 3, 1, false)
	return &drawn{w: b.W, h: b.H, ink: b.Ink, op: b.Opacity}
}

// encodeRLE codes palette indices as PGS run-length data, line by line.
func encodeRLE(pix []byte, w, h int) []byte {
	var out []byte
	for y := range h {
		row := pix[y*w : (y+1)*w]
		for x := 0; x < w; {
			c := row[x]
			n := 1
			for x+n < w && row[x+n] == c && n < 16383 {
				n++
			}
			switch {
			case c != 0 && n < 3:
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
