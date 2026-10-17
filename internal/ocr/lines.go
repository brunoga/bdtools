package ocr

import (
	"math"
	"sort"
)

// A subtitle's picture to lines the network reads.
//
// A subtitle is drawn as a fill (white, yellow) with a dark outline on a
// transparent plane. What the network sees is the ink: each pixel's
// brightness as much as it is opaque, so the fill and not the outline.
// The picture is cut into lines where no pixel is opaque across a whole
// row, each line cut to its width, and scaled so that the track's line
// height (from the tops of its capitals to the bottoms of its descenders)
// is lineFill of the network's height: one scale for the track, so that
// a line of small letters stays small.

// lineFill is how much of Height a track's line takes.
const lineFill = 26

// Bitmap is a subtitle picture: per pixel, its brightness and how opaque
// it is (0 to 1).
type Bitmap struct {
	W, H         int
	Ink, Opacity []float32
}

// Line is a line of a subtitle, cut from its picture.
type Line struct {
	// Top, Bottom, Left and Right are its bounds in the picture.
	Top, Bottom, Left, Right int
}

// Height of a line, in the picture's pixels.
func (l Line) Height() int { return l.Bottom - l.Top }

// opaque is how opaque a pixel must be to count as drawn.
const opaque = 0.25

// Lines finds a picture's lines. With the track's line height (0 when
// it is not known yet), a band much taller than a line is two lines
// touching, cut apart.
func (b *Bitmap) Lines(lineHeight float64) []Line {
	var lines []Line
	inLine, top := false, 0
	rowDrawn := func(y int) bool {
		for _, a := range b.Opacity[y*b.W : (y+1)*b.W] {
			if a > opaque {
				return true
			}
		}
		return false
	}
	type band struct{ top, bottom int }
	var bands []band
	for y := 0; y <= b.H; y++ {
		drawn := y < b.H && rowDrawn(y)
		switch {
		case drawn && !inLine:
			inLine, top = true, y
		case !drawn && inLine:
			inLine = false
			bands = append(bands, band{top, y})
		}
	}
	// A sliver (accents over capitals, apart by a blank row) goes with
	// the band below it, or above it when it is last.
	tallest := 0
	for _, bd := range bands {
		tallest = max(tallest, bd.bottom-bd.top)
	}
	for i := 0; i < len(bands); i++ {
		if bands[i].bottom-bands[i].top >= tallest/3 || len(bands) == 1 {
			continue
		}
		if i+1 < len(bands) {
			bands[i+1].top = bands[i].top
		} else {
			bands[i-1].bottom = bands[i].bottom
		}
		bands = append(bands[:i], bands[i+1:]...)
		i--
	}
	for _, bd := range bands {
		for _, part := range b.split(bd.top, bd.bottom, lineHeight) {
			if l, ok := b.bounds(part[0], part[1]); ok {
				lines = append(lines, l)
			}
		}
	}
	return lines
}

// split cuts a band that holds two lines touching (a descender on the
// next line's capitals) at its emptiest row near the middle.
func (b *Bitmap) split(top, bottom int, lineHeight float64) [][2]int {
	h := bottom - top
	if lineHeight <= 0 || float64(h) < 1.6*lineHeight {
		return [][2]int{{top, bottom}}
	}
	best, at := math.MaxFloat64, -1
	for y := top + h/3; y < bottom-h/3; y++ {
		var s float64
		for _, a := range b.Opacity[y*b.W : (y+1)*b.W] {
			s += float64(a)
		}
		if s < best {
			best, at = s, y
		}
	}
	if at < 0 {
		return [][2]int{{top, bottom}}
	}
	return [][2]int{{top, at}, {at + 1, bottom}}
}

// bounds is the line in rows [top, bottom): its columns' extent.
func (b *Bitmap) bounds(top, bottom int) (Line, bool) {
	left, right := b.W, 0
	for y := top; y < bottom; y++ {
		for x, a := range b.Opacity[y*b.W : (y+1)*b.W] {
			if a > opaque {
				left, right = min(left, x), max(right, x+1)
			}
		}
	}
	if right <= left || bottom-top < 3 {
		return Line{}, false
	}
	return Line{Top: top, Bottom: bottom, Left: left, Right: right}, true
}

// LineHeight is a track's line height: the most usual of its lines'
// heights (a line of only small letters, or a dash, is shorter).
func LineHeight(heights []int) float64 {
	if len(heights) == 0 {
		return 0
	}
	h := append([]int(nil), heights...)
	sort.Ints(h)
	// The upper middle: most lines have a capital or a tall letter, and
	// some a descender too.
	return float64(h[len(h)*2/3])
}

// Image is a line ready for the network: its ink scaled so that the
// track's line height is lineFill, centred, a margin each side, the width
// a multiple of 4.
func (b *Bitmap) Image(l Line, lineHeight float64) ([]float32, int) {
	if lineHeight <= 0 {
		lineHeight = float64(l.Height())
	}
	s := lineFill / lineHeight
	srcW, srcH := l.Right-l.Left, l.Height()
	// A line taller than the track's (capitals with accents above, and
	// descenders) is shrunk to fit rather than cut.
	s = min(s, float64(Height-2)/float64(srcH))
	dw := max(1, int(math.Round(float64(srcW)*s)))
	w := (dw + 8 + 3) &^ 3
	img := make([]float32, Height*w)
	// The line's middle on the network's.
	dh := float64(srcH) * s
	y0 := (Height - dh) / 2
	x0 := 4.0
	// Each target pixel the mean of the source pixels it covers (box
	// filtering, for the usual shrinking), bilinear when enlarging.
	for ty := range Height {
		sy0 := (float64(ty) - y0) / s
		sy1 := (float64(ty) + 1 - y0) / s
		if sy1 <= 0 || sy0 >= float64(srcH) {
			continue
		}
		for tx := range w {
			sx0 := (float64(tx) - x0) / s
			sx1 := (float64(tx) + 1 - x0) / s
			if sx1 <= 0 || sx0 >= float64(srcW) {
				continue
			}
			img[ty*w+tx] = b.area(l, sx0, sy0, sx1, sy1)
		}
	}
	// The brightest ink as 1.
	var peak float32
	for _, v := range img {
		peak = max(peak, v)
	}
	if peak > 0 {
		for i := range img {
			img[i] /= peak
		}
	}
	return img, w
}

// area is the mean ink over a rectangle of the line's pixels (fractional
// bounds, clipped to the line).
func (b *Bitmap) area(l Line, x0, y0, x1, y1 float64) float32 {
	srcW, srcH := float64(l.Right-l.Left), float64(l.Height())
	x0, y0 = max(x0, 0), max(y0, 0)
	x1, y1 = min(x1, srcW), min(y1, srcH)
	if x1-x0 < 1 && y1-y0 < 1 && x1 > x0 && y1 > y0 {
		// Smaller than a pixel: bilinear at its centre.
		return b.bilinear(l, (x0+x1)/2-0.5, (y0+y1)/2-0.5)
	}
	var sum, wsum float64
	for y := int(y0); y < int(math.Ceil(y1)); y++ {
		hy := min(float64(y+1), y1) - max(float64(y), y0)
		for x := int(x0); x < int(math.Ceil(x1)); x++ {
			wx := min(float64(x+1), x1) - max(float64(x), x0)
			v := b.Ink[(l.Top+y)*b.W+l.Left+x]
			sum += float64(v) * wx * hy
			wsum += wx * hy
		}
	}
	if wsum == 0 {
		return 0
	}
	return float32(sum / wsum)
}

func (b *Bitmap) bilinear(l Line, x, y float64) float32 {
	srcW, srcH := l.Right-l.Left, l.Height()
	at := func(x, y int) float64 {
		x, y = min(max(x, 0), srcW-1), min(max(y, 0), srcH-1)
		return float64(b.Ink[(l.Top+y)*b.W+l.Left+x])
	}
	ix, iy := int(math.Floor(x)), int(math.Floor(y))
	fx, fy := x-float64(ix), y-float64(iy)
	v := at(ix, iy)*(1-fx)*(1-fy) + at(ix+1, iy)*fx*(1-fy) + at(ix, iy+1)*(1-fx)*fy + at(ix+1, iy+1)*fx*fy
	return float32(v)
}

// NewBitmap is a subtitle picture from its pixels' luma (limited range,
// as PGS palettes give it) and alpha: the ink each pixel's brightness as
// much as it is opaque.
func NewBitmap(w, h int, luma, alpha []byte) *Bitmap {
	b := &Bitmap{W: w, H: h, Ink: make([]float32, w*h), Opacity: make([]float32, w*h)}
	for i, a := range alpha[:w*h] {
		op := float32(a) / 255
		y := min(max((float32(luma[i])-16)/219, 0), 1)
		b.Ink[i], b.Opacity[i] = y*op, op
	}
	return b
}

// Slant is how far a line leans, as the shear (columns per row, up to
// the right) that makes its strokes most upright: near 0 for upright
// faces, about 0.2 for italics.
func (b *Bitmap) Slant(l Line) float64 {
	best, at := -1.0, 0.0
	mid := float64(l.Top+l.Bottom) / 2
	w := l.Right - l.Left
	cols := make([]float64, w+l.Height())
	for k := -0.1; k <= 0.4; k += 0.025 {
		clear(cols)
		for y := l.Top; y < l.Bottom; y++ {
			off := k * (mid - float64(y)) // a leaning stroke stands up
			for x := l.Left; x < l.Right; x++ {
				v := b.Ink[y*b.W+x]
				if v == 0 {
					continue
				}
				c := int(float64(x-l.Left) - off + float64(l.Height())/2)
				if c >= 0 && c < len(cols) {
					cols[c] += float64(v)
				}
			}
		}
		// Upright strokes gather the ink in fewer columns.
		var s float64
		for _, c := range cols {
			s += c * c
		}
		if s > best {
			best, at = s, k
		}
	}
	return at
}

// Italic reports whether a picture's lines lean as italics do.
func (b *Bitmap) Italic(lines []Line) bool {
	var s float64
	n := 0
	for _, l := range lines {
		s += b.Slant(l) * float64(l.Right-l.Left)
		n += l.Right - l.Left
	}
	return n > 0 && s/float64(n) > italicSlant
}

// italicSlant is the slant past which a line is italic.
const italicSlant = 0.1
