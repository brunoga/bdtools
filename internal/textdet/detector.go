package textdet

import (
	"math"
	"time"
)

// Overlay is a line of text the detector follows from picture to
// picture: its box now, how sure the classifier is that it lies over the
// picture (P, smoothed over the pictures), and how far it is lifted (Lift,
// 0 not at all to 1 fully, rising and falling over a few pictures so that
// the text never jumps).
type Overlay struct {
	Region
	P, Lift float64
	lost    int // pictures since the detector last saw it
	sure    int // pictures in a row P has been over Threshold
}

const (
	// Threshold is the smoothed probability over which a line is lifted,
	// once it has been over it for surePictures in a row; it stays lifted
	// until P falls under Release.
	Threshold    = 0.55
	Release      = 0.4
	surePictures = 12  // half a second: the detector's mistakes on clouds and sparks come and go faster
	smoothing    = 0.3 // of P, the new picture's share
	liftStep     = 1.0 / 8
	keepLost     = 6 // pictures a line is followed after the detector loses it
	minIoU       = 0.3
	before       = 500 * time.Millisecond // the pictures the change measures compare with
	longAgo      = 2 * time.Second
)

// Detector follows the text of a film's pictures (half size, grey) and
// says which lines lie over the picture: the titles, credits and logos.
type Detector struct {
	Net   *Net
	Trees *Trees
	lines []*Overlay
	hist  []past // the pictures of the last longAgo, oldest first
}

type past struct {
	y   Grey
	pts time.Duration
}

// Observe takes a picture y (half size, grey) at time pts, with its map p
// (the net's output, row stride ps): the lines found in it, matched with
// those followed, and each one's probability and lift brought up to date.
// On is false when nothing is to be lifted now (the grading turned it
// off): the lines still followed, their lift falling.
func (d *Detector) Observe(y Grey, p []float32, ps int, pts time.Duration, on bool) {
	found := Regions(p, y.W, y.H, ps)
	bef, long := d.at(pts-before), d.at(pts-longAgo)
	seen := make([]bool, len(d.lines))
	for _, r := range found {
		f := Features(y, p, ps, r, bef, nil, long)
		prob := d.Trees.Prob(f)
		best, bi := 0.0, -1
		for i, l := range d.lines {
			if v := iou(l.Region, r); v > best && !seen[i] {
				best, bi = v, i
			}
		}
		if best < minIoU {
			d.lines = append(d.lines, &Overlay{Region: r, P: prob})
			seen = append(seen, true)
			continue
		}
		l := d.lines[bi]
		seen[bi] = true
		l.Region, l.lost = r, 0
		l.P += smoothing * (prob - l.P)
	}
	kept := d.lines[:0]
	for i, l := range d.lines {
		if !seen[i] {
			l.lost++
		}
		switch {
		case l.P > Threshold:
			l.sure++
		case l.P < Release && l.Lift < 1:
			l.sure = 0
		}
		if l.Lift >= 1 && l.lost == 0 {
			l.sure = max(l.sure, surePictures) // confirmed: a title stays one while it is followed
		}
		if on && l.lost == 0 && l.sure >= surePictures {
			l.Lift = math.Min(1, l.Lift+liftStep)
		} else {
			l.Lift = math.Max(0, l.Lift-liftStep)
		}
		if l.lost <= keepLost || l.Lift > 0 {
			kept = append(kept, l)
		}
	}
	d.lines = kept
	d.remember(y, pts)
}

// Lines are the lines followed now.
func (d *Detector) Lines() []*Overlay { return d.lines }

// Reset forgets the lines and the pictures (a seek).
func (d *Detector) Reset() {
	d.lines = nil
	d.hist = d.hist[:0]
}

// remember keeps y (a copy) as the picture at pts, dropping those older
// than longAgo and a bit.
func (d *Detector) remember(y Grey, pts time.Duration) {
	if n := len(d.hist); n > 0 && pts <= d.hist[n-1].pts {
		d.hist = d.hist[:0] // time went back: a seek
	}
	var buf []byte
	drop := 0
	for drop < len(d.hist) && pts-d.hist[drop].pts > longAgo+250*time.Millisecond {
		drop++
	}
	if drop > 0 {
		buf = d.hist[0].y.Pix
		d.hist = append(d.hist[:0], d.hist[drop:]...)
	}
	if cap(buf) < y.W*y.H {
		buf = make([]byte, y.W*y.H)
	}
	buf = buf[:y.W*y.H]
	for r := range y.H {
		copy(buf[r*y.W:(r+1)*y.W], y.Pix[r*y.Stride:])
	}
	d.hist = append(d.hist, past{Grey{buf, y.W, y.H, y.W}, pts})
}

// at is the picture remembered nearest to time t, within a quarter of a
// second, or nil.
func (d *Detector) at(t time.Duration) *Grey {
	var best *Grey
	bd := 250 * time.Millisecond
	for i := range d.hist {
		dt := d.hist[i].pts - t
		if dt < 0 {
			dt = -dt
		}
		if dt <= bd {
			best, bd = &d.hist[i].y, dt
		}
	}
	return best
}

func iou(a, b Region) float64 {
	y0, y1, x0, x1 := max(a.Y0, b.Y0), min(a.Y1, b.Y1), max(a.X0, b.X0), min(a.X1, b.X1)
	if y1 <= y0 || x1 <= x0 {
		return 0
	}
	i := float64((y1 - y0) * (x1 - x0))
	return i / (float64((a.Y1-a.Y0)*(a.X1-a.X0)+(b.Y1-b.Y0)*(b.X1-b.X0)) - i)
}

// Mask is the letters of the lines being lifted in picture y (half size),
// as a weight a pixel in m (0 to 255, the line's lift) and the line's
// number in ids (1 on, in the order of the lines it gives; 0 none): the
// pixels strokeContrast or more from the mean around them, grown by a
// pixel so that the letters' edges go with them. With sure, the lines
// are those the classifier is sure of now, all fully lifted (a still
// picture, its grading changed). It gives the lines lifted, at most 255.
func (d *Detector) Mask(y Grey, m, ids []byte, sure bool) []Region {
	clear(m)
	clear(ids)
	var lines []Region
	for _, l := range d.lines {
		lift := l.Lift
		if sure {
			lift = 0
			if l.lost == 0 && l.sure >= surePictures {
				lift = 1
			}
		}
		if lift <= 0 || len(lines) == 255 {
			continue
		}
		r := l.Region
		lines = append(lines, r)
		id := byte(len(lines))
		r.X0, r.X1 = r.MX0, r.MX1 // the letters' reach
		bw, bh := r.X1-r.X0, r.Y1-r.Y0
		sub := make([]float32, bw*bh)
		for yy := range bh {
			for xx := range bw {
				sub[yy*bw+xx] = y.at(r.X0+xx, r.Y0+yy)
			}
		}
		mean := boxMean(sub, bw, bh, 2*max(3, r.H/2)+1)
		sign := polarity(sub, mean)
		v := byte(math.Round(255 * lift))
		for yy := range bh {
			for xx := range bw {
				i := yy*bw + xx
				if sign*(sub[i]-mean[i]) <= strokeContrast {
					continue
				}
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						X, Y := r.X0+xx+dx, r.Y0+yy+dy
						if X >= r.X0 && X < r.X1 && Y >= r.Y0 && Y < r.Y1 && v >= m[Y*y.W+X] {
							m[Y*y.W+X], ids[Y*y.W+X] = v, id
						}
					}
				}
			}
		}
	}
	return lines
}

// polarity is 1 when a box's letters are lighter than what is around
// them and -1 when darker: the side of the mean whose pixels stand out
// from it more, as letters do from the gaps between them.
func polarity(sub, mean []float32) float32 {
	var lt, dk, nl, nd float64
	for i, v := range sub {
		switch d := float64(v - mean[i]); {
		case d > strokeContrast:
			lt, nl = lt+d, nl+1
		case d < -strokeContrast:
			dk, nd = dk-d, nd+1
		}
	}
	if nd > 0 && (nl == 0 || dk/nd > lt/nl) {
		return -1
	}
	return 1
}

// Ring is the band around a region that its text stands in front of, in
// picture y (half size): x0, y0, x1, y1 of the band's outside, the region
// its inside.
func Ring(y Grey, r Region) (x0, y0, x1, y1 int) { return ring(y, r) }
