package main

import (
	"image"
	"math"
	"math/rand/v2"
	"os"
	"sync"

	"github.com/brunoga/bdtools/internal/ocr"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Subtitle lines drawn as a disc draws them: a font's glyphs filled
// (white mostly, yellow, grey), outlined in black, anti-aliased, on
// nothing; then what the PGS palette and its coding do to them.

type fontFile struct {
	path   string
	italic bool
	f      *opentype.Font
}

// fonts are the faces drawn with: the sans-serif families discs use and
// their look-alikes, a serif or two, regular, bold and italic.
var fontPaths = []string{
	"/usr/share/fonts/truetype/liberation/LiberationSans-Regular.ttf",
	"/usr/share/fonts/truetype/liberation/LiberationSans-Bold.ttf",
	"/usr/share/fonts/truetype/liberation/LiberationSans-Italic.ttf",
	"/usr/share/fonts/truetype/liberation/LiberationSans-BoldItalic.ttf",
	"/usr/share/fonts/truetype/liberation/LiberationSansNarrow-Regular.ttf",
	"/usr/share/fonts/truetype/liberation/LiberationSansNarrow-Italic.ttf",
	"/usr/share/fonts/truetype/liberation/LiberationSerif-Regular.ttf",
	"/usr/share/fonts/truetype/liberation/LiberationSerif-Italic.ttf",
	"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
	"/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
	"/usr/share/fonts/truetype/dejavu/DejaVuSans-Oblique.ttf",
	"/usr/share/fonts/truetype/dejavu/DejaVuSerif.ttf",
	"/usr/share/fonts/opentype/urw-base35/NimbusSans-Regular.otf",
	"/usr/share/fonts/opentype/urw-base35/NimbusSans-Bold.otf",
	"/usr/share/fonts/opentype/urw-base35/NimbusSans-Italic.otf",
	"/usr/share/fonts/opentype/urw-base35/NimbusSans-BoldItalic.otf",
	"/usr/share/fonts/opentype/urw-base35/NimbusSansNarrow-Regular.otf",
	"/usr/share/fonts/opentype/urw-base35/NimbusRoman-Regular.otf",
	"/usr/share/fonts/opentype/urw-base35/NimbusRoman-Italic.otf",
	"/usr/share/fonts/truetype/freefont/FreeSans.ttf",
	"/usr/share/fonts/truetype/freefont/FreeSansBold.ttf",
	"/usr/share/fonts/truetype/freefont/FreeSansOblique.ttf",
	"/usr/share/fonts/truetype/noto/NotoSans-Regular.ttf",
	"/usr/share/fonts/truetype/noto/NotoSans-Bold.ttf",
	"/usr/share/fonts/truetype/noto/NotoSans-Italic.ttf",
	"/usr/share/fonts/truetype/lato/Lato-Regular.ttf",
	"/usr/share/fonts/truetype/lato/Lato-Medium.ttf",
	"/usr/share/fonts/truetype/lato/Lato-Bold.ttf",
	"/usr/share/fonts/truetype/lato/Lato-Italic.ttf",
}

func loadFonts() ([]*fontFile, error) {
	var out []*fontFile
	for _, p := range fontPaths {
		b, err := os.ReadFile(p) //nolint:gosec // the fonts listed
		if err != nil {
			continue
		}
		f, err := opentype.Parse(b)
		if err != nil {
			continue
		}
		out = append(out, &fontFile{path: p, italic: containsAny(p, "Italic", "Oblique"), f: f})
	}
	if len(out) == 0 {
		return nil, os.ErrNotExist
	}
	return out, nil
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		for i := 0; i+len(x) <= len(s); i++ {
			if s[i:i+len(x)] == x {
				return true
			}
		}
	}
	return false
}

// faces caches faces by font and size: making one is slow, and a face
// is not safe for concurrent use, so each goroutine has its own cache.
type faces map[[2]int]font.Face

func (fc faces) get(fonts []*fontFile, i, size int) font.Face {
	if f, ok := fc[[2]int{i, size}]; ok {
		return f
	}
	f, err := opentype.NewFace(fonts[i].f, &opentype.FaceOptions{Size: float64(size), DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		return nil
	}
	fc[[2]int{i, size}] = f
	return f
}

// lineHeights caches each face's line height as a track's lines show it:
// from the top of a capital to the bottom of a descender.
var lineHeights sync.Map

func lineHeight(face font.Face, key [2]int) float64 {
	if v, ok := lineHeights.Load(key); ok {
		return v.(float64)
	}
	b, _ := font.BoundString(face, "Hg")
	h := float64(b.Max.Y-b.Min.Y) / 64
	lineHeights.Store(key, h)
	return h
}

// render draws a line of text, giving its picture and the track's line
// height, or false when the face cannot draw it.
func render(rng *rand.Rand, fonts []*fontFile, fc faces, text string) (*ocr.Bitmap, float64, bool) {
	fi := rng.IntN(len(fonts))
	size := 30 + rng.IntN(36) // 1080p subtitles: about 36 to 64 pixels a line
	face := fc.get(fonts, fi, size)
	if face == nil {
		return nil, 0, false
	}
	for _, r := range text {
		if r != ' ' {
			if _, ok := face.GlyphAdvance(r); !ok {
				return nil, 0, false
			}
		}
	}
	outline := 1 + rng.IntN(4)
	fillY := float32(1)
	switch rng.IntN(10) {
	case 0:
		fillY = 0.85 // yellow's luma
	case 1:
		fillY = 0.6 + 0.3*rng.Float32()
	}
	shearIt := !fonts[fi].italic && rng.IntN(8) == 0
	b, _ := renderWith(face, text, outline, fillY, shearIt)
	w, h := b.W, b.H
	// The PGS palette: few levels of the edges' anti-aliasing.
	if rng.IntN(2) == 0 {
		levels := float32(4 + rng.IntN(12))
		for i := range b.Ink {
			b.Ink[i] = float32(math.Round(float64(b.Ink[i]*levels))) / levels
			b.Opacity[i] = float32(math.Round(float64(b.Opacity[i]*levels))) / levels
		}
	}
	if rng.IntN(4) == 0 {
		b.Ink = blur(b.Ink, w, h)
	}
	if rng.IntN(3) == 0 {
		amp := 0.05 * rng.Float32()
		for i := range b.Ink {
			b.Ink[i] = min(max(b.Ink[i]+amp*float32(rng.NormFloat64()), 0), 1)
		}
	}
	lh := lineHeight(face, [2]int{fi, size}) * (0.93 + 0.14*rng.Float64())
	return b, lh, true
}

// renderWith draws a line in a face: filled at fillY, outlined in black
// outline pixels wide, leant like italics if shear.
func renderWith(face font.Face, text string, outline int, fillY float32, lean bool) (*ocr.Bitmap, bool) {
	pad := outline + 6
	size := face.Metrics().Height.Ceil()
	adv := font.MeasureString(face, text)
	m := face.Metrics()
	w := adv.Ceil() + 2*pad + size/4 // italics lean past their advance
	h := (m.Ascent + m.Descent).Ceil() + 2*pad
	mask := image.NewAlpha(image.Rect(0, 0, w, h))
	d := font.Drawer{Dst: mask, Src: image.Opaque, Face: face, Dot: fixed.P(pad, pad+m.Ascent.Ceil())}
	d.DrawString(text)
	fill := make([]float32, w*h)
	for i, a := range mask.Pix {
		fill[i] = float32(a) / 255
	}
	if lean {
		fill = shear(fill, w, h, 0.2)
	}
	// The outline: the fill grown by its width (the most of the fill
	// within it, a disc), its edge soft.
	edge := dilate(fill, w, h, float64(outline))
	b := &ocr.Bitmap{W: w, H: h, Ink: make([]float32, w*h), Opacity: make([]float32, w*h)}
	for i := range fill {
		b.Opacity[i] = max(fill[i], edge[i])
		b.Ink[i] = fill[i] * fillY
	}
	return b, true
}

func dilate(src []float32, w, h int, r float64) []float32 {
	out := make([]float32, len(src))
	ir := int(math.Ceil(r))
	for y := range h {
		for x := range w {
			var best float32
			for dy := -ir; dy <= ir; dy++ {
				yy := y + dy
				if yy < 0 || yy >= h {
					continue
				}
				for dx := -ir; dx <= ir; dx++ {
					xx := x + dx
					if xx < 0 || xx >= w {
						continue
					}
					dist := math.Hypot(float64(dx), float64(dy))
					if dist > r+0.5 {
						continue
					}
					v := src[yy*w+xx] * float32(min(1, r+0.5-dist))
					best = max(best, v)
				}
			}
			out[y*w+x] = best
		}
	}
	return out
}

func blur(src []float32, w, h int) []float32 {
	out := make([]float32, len(src))
	for y := range h {
		for x := range w {
			var s float32
			n := 0
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					if yy, xx := y+dy, x+dx; yy >= 0 && yy < h && xx >= 0 && xx < w {
						s += src[yy*w+xx]
						n++
					}
				}
			}
			out[y*w+x] = 0.5*src[y*w+x] + 0.5*s/float32(n)
		}
	}
	return out
}

// shear leans a picture right by k (as italics do), about its middle.
func shear(src []float32, w, h int, k float64) []float32 {
	out := make([]float32, len(src))
	for y := range h {
		off := k * float64(h/2-y)
		for x := range w {
			sx := float64(x) - off
			ix := int(math.Floor(sx))
			f := float32(sx - float64(ix))
			var a, b float32
			if ix >= 0 && ix < w {
				a = src[y*w+ix]
			}
			if ix+1 >= 0 && ix+1 < w {
				b = src[y*w+ix+1]
			}
			out[y*w+x] = a*(1-f) + b*f
		}
	}
	return out
}

