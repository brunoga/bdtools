package textdet

import "math"

// Region is a line of text the map shows: the box around its letters
// [Y0, Y1) x [X0, X1), grown to hold the whole letters (the map marks
// only their middles), and H, the height of the marked band. [MX0,
// MX1) is how far its letters reach across: the box's own, and the
// pieces of the map beside it that are too short to be lines of their
// own (a swash capital), which the letters' mask takes in (Mask) and the
// classifier does not.
type Region struct {
	Y0, Y1, X0, X1, H int
	MX0, MX1          int
}

const (
	mapThreshold = 0.3 // a pixel of the map at least this is text
	minPixels    = 40  // a band of fewer pixels is not a line of text
)

// Regions are the lines of text of a probability map w x h (row stride
// stride): each band of the map over mapThreshold (pixels joined across
// their sides) of at least minPixels and at least twice as wide as tall,
// grown by 0.9 of its height up and down and 0.45 left and right.
func Regions(p []float32, w, h, stride int) []Region {
	lab := make([]int32, w*h)
	var out []Region
	type piece struct{ y0, y1, x0, x1 int }
	var bands, short []piece
	var stack []int
	next := int32(0)
	for i0 := range w * h {
		if lab[i0] != 0 || p[i0/w*stride+i0%w] <= mapThreshold {
			continue
		}
		next++
		lab[i0] = next
		stack = append(stack[:0], i0)
		y0, y1, x0, x1, n := h, 0, w, 0, 0
		for len(stack) > 0 {
			i := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			y, x := i/w, i%w
			n++
			y0, y1, x0, x1 = min(y0, y), max(y1, y+1), min(x0, x), max(x1, x+1)
			for _, j := range [4]int{i - w, i + w, i - 1, i + 1} {
				switch {
				case j < 0 || j >= w*h,
					j == i-1 && x == 0,
					j == i+1 && x == w-1,
					lab[j] != 0,
					p[j/w*stride+j%w] <= mapThreshold:
					continue
				}
				lab[j] = next
				stack = append(stack, j)
			}
		}
		bh := max(y1-y0, 1)
		if n < minPixels || x1-x0 < 2*bh {
			if n >= minPixels {
				short = append(short, piece{y0, y1, x0, x1})
			}
			continue
		}
		e := int(math.RoundToEven(0.9 * float64(bh)))
		x0g, x1g := max(0, x0-e/2), min(w, x1+e/2)
		out = append(out, Region{max(0, y0-e), min(h, y1+e), x0g, x1g, bh, x0g, x1g})
		bands = append(bands, piece{y0, y1, x0, x1})
	}
	// the short pieces on a line's band, at most its height from its ends
	for i := range out {
		b, r := bands[i], &out[i]
		for grew := true; grew; {
			grew = false
			for _, p := range short {
				over := min(p.y1, b.y1) - max(p.y0, b.y0)
				near := p.x1 >= b.x0-r.H && p.x0 <= b.x1+r.H
				if over*2 < p.y1-p.y0 || !near || p.x0 >= b.x0 && p.x1 <= b.x1 {
					continue
				}
				b.x0, b.x1 = min(b.x0, p.x0), max(b.x1, p.x1)
				grew = true
			}
		}
		// a swash capital reaches out past where the map marks it (the
		// Disney wordmark's W, a whole letter outside the band)
		r.MX0, r.MX1 = min(r.MX0, max(0, b.x0-2*r.H)), max(r.MX1, min(w, b.x1+2*r.H))
	}
	return out
}
