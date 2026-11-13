package convert

// The grading's readout drawn into the stream: a line of text at the
// bottom of both eyes, in the same place in each (so it sits at the
// screen), white on black, so that the player at the 3D screen and a
// headset show the time and the knobs while a hand turns them.

import (
	"image"
	"image/color"
	"image/draw"

	"github.com/brunoga/bdtools/mvc"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

const (
	hudScale  = 2  // the 7x13 font, doubled
	hudPad    = 8  // around the text, in font pixels
	hudMargin = 40 // from the picture's left and bottom, in pixels
)

// drawHUD draws a line of text into the bottom left of both eyes (8-bit
// 4:2:0 of the same size), the box behind it black, the chroma neutral.
func drawHUD(eyes []*mvc.Frame, text string) {
	if len(eyes) == 0 || text == "" {
		return
	}
	face := basicfont.Face7x13
	tw := font.MeasureString(face, text).Ceil()
	w, h := tw+2*hudPad, face.Height+2*hudPad
	img := image.NewGray(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.Gray{16}), image.Point{}, draw.Src)
	d := &font.Drawer{Dst: img, Src: image.NewUniform(color.Gray{235}), Face: face,
		Dot: fixed.P(hudPad, hudPad+face.Ascent)}
	d.DrawString(text)
	for _, f := range eyes {
		if f == nil {
			continue
		}
		bw, bh := w*hudScale, h*hudScale
		x0, y0 := hudMargin, f.Height-hudMargin-bh
		if x0+bw > f.Width {
			bw = f.Width - x0
		}
		if y0 < 0 {
			y0 = 0
		}
		for y := range bh {
			row := f.Y[(y0+y)*f.StrideY+x0:]
			src := img.Pix[(y/hudScale)*img.Stride:]
			for x := range bw {
				row[x] = src[x/hudScale]
			}
		}
		for y := y0 / 2; y < (y0+bh+1)/2 && y < (f.Height+1)/2; y++ {
			for x := x0 / 2; x < (x0+bw+1)/2 && x < (f.Width+1)/2; x++ {
				f.Cb[y*f.StrideC+x], f.Cr[y*f.StrideC+x] = 128, 128
			}
		}
	}
}

// copyFrame is a frame of its own, for drawing on.
func copyFrame(f *mvc.Frame) *mvc.Frame {
	c := *f
	c.Y, c.Cb, c.Cr = append([]byte(nil), f.Y...), append([]byte(nil), f.Cb...), append([]byte(nil), f.Cr...)
	return &c
}
