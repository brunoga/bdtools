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
	first   int // the picture the detector first saw it in (Detector.n's)
	sureAt  int // the picture it was first sure of in, -1 not yet
}

// A Sighting is a line as one picture showed it: the line, its box
// there, its lift then, and whether the detector found it there (not
// only followed it a few pictures on). Observe gives them, so that a
// picture made after the pictures that follow it were looked at can lift
// a line the detector became sure of only later (Later).
type Sighting struct {
	Line  *Overlay
	Box   Region
	Lift  float64
	Found bool
	at    int // the picture's number (Detector.n's)
}

// Ahead is how many pictures before a picture is made the detector
// looks at it, when it is given them ahead: two seconds of film. The
// classifier is sure of a title within half a second of finding it when
// it appears whole, but Toy Story 2's wordmark, written in by a sparkle,
// is found two and a half seconds before it is sure, and with one second
// it rose a second after it was whole.
const Ahead = 48

// Later is how far a sighting's line is lifted in its picture, given
// what the detector has seen since: its lift then, or, for a line found
// there that the detector became sure of later, rising from the picture
// it first appeared in (but no earlier than Ahead pictures before it was
// sure, the pictures before those being made already), so that a title
// comes forward as it appears and not half a second after.
func (s Sighting) Later() float64 {
	l := s.Line
	if !s.Found || l.sureAt < 0 || s.at >= l.sureAt+int(1/liftStep) {
		return s.Lift
	}
	from := max(l.first, l.sureAt-Ahead+2)
	return math.Max(s.Lift, math.Min(1, float64(s.at-from+1)*liftStep))
}

// Sure reports whether the detector is sure of the sighting's line now,
// and found it in the sighting's picture: a still picture lifts it fully.
func (s Sighting) Sure() bool { return s.Found && s.Line.sureAt >= 0 }

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
	n     int    // the pictures observed
}

type past struct {
	y   Grey
	pts time.Duration
}

// Observe takes a picture y (half size, grey) at time pts, with its map p
// (the net's output, row stride ps): the lines found in it, matched with
// those followed, and each one's probability and lift brought up to date.
// On is false when nothing is to be lifted now (the grading turned it
// off): the lines still followed, their lift falling. It gives the
// lines followed as the picture shows them.
func (d *Detector) Observe(y Grey, p []float32, ps int, pts time.Duration, on bool) []Sighting {
	at := d.n
	d.n++
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
			d.lines = append(d.lines, &Overlay{Region: r, P: prob, first: at, sureAt: -1})
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
		if l.sure >= surePictures && l.sureAt < 0 {
			l.sureAt = at
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
	out := make([]Sighting, len(kept))
	for i, l := range kept {
		out[i] = Sighting{Line: l, Box: l.Region, Lift: l.Lift, Found: l.lost == 0, at: at}
	}
	return out
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
	boxes, lifts := make([]Region, len(d.lines)), make([]float64, len(d.lines))
	for i, l := range d.lines {
		boxes[i], lifts[i] = l.Region, l.Lift
		if sure {
			lifts[i] = 0
			if l.lost == 0 && l.sure >= surePictures {
				lifts[i] = 1
			}
		}
	}
	return MaskLines(y, m, ids, boxes, lifts)
}

// MaskLines is Mask for lines given by their boxes and lifts (0 to 1):
// those lifted at all are in m and ids, and given.
func MaskLines(y Grey, m, ids []byte, boxes []Region, lifts []float64) []Region {
	clear(m)
	clear(ids)
	var lines []Region
	for i, r := range boxes {
		lift := lifts[i]
		if lift <= 0 || len(lines) == 255 {
			continue
		}
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
