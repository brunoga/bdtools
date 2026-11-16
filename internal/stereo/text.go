package stereo

import (
	"math"
	"time"

	"github.com/brunoga/bdtools/internal/nn"
	"github.com/brunoga/bdtools/internal/textdet"
	"github.com/brunoga/bdtools/mvc"
)

// The text over the picture: titles, credits over a scene, the studios'
// logos. The 3D discs put it in front of the scene, at the picture's near
// end (the ones that lift it: the letters' disparity a median 0.7 pixels
// short of the picture's 95th percentile, 15 in front of what is around
// them). The depth network gives it the depth of what it is written over,
// so the Disney castle's name sinks into the lake below it, whatever the
// grading. A TextLift finds it (textdet) and brings its letters to the
// near end.

// The Shape's Text: where the text over the picture is lifted.
const (
	TextAuto = iota // in the film's opening, where the titles are (TextLift.Opening)
	TextOn          // wherever the detector finds it
	TextOff         // nowhere
)

// TextLift is a converter's text detector and how it applies.
type TextLift struct {
	Det *textdet.Detector
	// Opening is how long a film's opening is: TextAuto lifts the text
	// only before this time of the film (Clock's), as the detector takes
	// for overlays a share of the text written on things in the film.
	Opening time.Duration
	// Clock is a picture's time in the film, by its PTS (nil: the PTS as
	// 90 kHz ticks).
	Clock func(pts int64) time.Duration
	// Lift is how far in front of what is around it a line of text goes,
	// a fraction of the picture's width (DefaultTextLift), and never short
	// of the picture's near end.
	Lift float64

	near    *nn.Tensor // the GPU's near end (NearState)
	nearOn  nn.Backend
	cpuNear float32 // the CPU's, and whether it has one for the shot
	cpuOK   bool
}

// DefaultOpening is TextLift.Opening's usual value: the opening titles
// of the films surveyed run within it, and none of their scenes' text
// ten minutes in is lifted.
const DefaultOpening = 5 * time.Minute

// DefaultTextLift is TextLift.Lift's usual value: the discs that lift the
// text put it a median 15 pixels of 1920 in front of what is around it.
const DefaultTextLift = 0.008

// text is an Estimate's part of the text lift: the picture made small,
// whether its text is to be lifted, and the letters to lift now (nil:
// none), from the lines followed up to the picture before.
type text struct {
	y         textdet.Grey
	k         int // y is the picture at 1/k of its size
	on        bool
	mask, ids []byte           // Detector.Mask's (nil: no line lifted)
	lines     []textdet.Region // the lines lifted, by their number in ids
	pts       time.Duration
	// The picture given ahead (Converter.Ahead): it, and once the
	// detector has looked at it, the lines it showed.
	f        *mvc.Frame
	observed bool
	seen     []textdet.Sighting
}

// lift fills in the lines to lift (sure: those the detector is sure of,
// all fully): for a picture given ahead, the lines it showed as the
// detector knows them now (Sighting.Later), else the detector's own.
func (x *text) lift(d *textdet.Detector, sure bool) {
	m, ids := make([]byte, x.y.W*x.y.H), make([]byte, x.y.W*x.y.H)
	if x.f == nil {
		x.mask, x.ids, x.lines = nil, nil, d.Mask(x.y, m, ids, sure)
	} else {
		boxes, lifts := make([]textdet.Region, len(x.seen)), make([]float64, len(x.seen))
		for i, s := range x.seen {
			boxes[i] = s.Box
			switch {
			case !x.on:
			case sure && s.Sure():
				lifts[i] = 1
			case !sure:
				lifts[i] = s.Later()
			}
		}
		x.mask, x.ids, x.lines = nil, nil, textdet.MaskLines(x.y, m, ids, boxes, lifts)
	}
	if len(x.lines) > 0 {
		x.mask, x.ids = m, ids
	}
}

// rings are the lines' rings and boxes at full size, as
// nn.LiftLines takes them.
func (x *text) rings() [][8]int32 {
	r := make([][8]int32, len(x.lines))
	for i, l := range x.lines {
		x0, y0, x1, y1 := textdet.Ring(x.y, l)
		k := int32(x.k)
		r[i] = [8]int32{k * int32(x0), k * int32(y0), k * int32(x1), k * int32(y1),
			k * int32(l.X0), k * int32(l.Y0), k * int32(l.X1), k * int32(l.Y1)}
	}
	return r
}

func (t *TextLift) clock(pts int64) time.Duration {
	if t.Clock != nil {
		return t.Clock(pts)
	}
	return time.Duration(pts) * time.Second / 90000
}

// start is a picture's text part, its shape's Text setting mode.
func (t *TextLift) start(f *mvc.Frame, mode int) *text {
	at := t.clock(f.PTS)
	k := textScale(f.Width)
	x := &text{y: smallGrey(f, k), k: k, pts: at}
	x.on = t.on(mode, at)
	x.lift(t.Det, false)
	return x
}

// ahead is a picture's text part when it is given ahead, its shape's
// Text setting mode: the detector has not looked at it yet.
func (t *TextLift) ahead(f *mvc.Frame, mode int) *text {
	at := t.clock(f.PTS)
	k := textScale(f.Width)
	return &text{y: smallGrey(f, k), k: k, pts: at, on: t.on(mode, at), f: f}
}

// on reports whether a shape's Text setting mode lifts the text at a
// time of the film.
func (t *TextLift) on(mode int, at time.Duration) bool {
	return mode == TextOn || mode == TextAuto && at < t.Opening
}

// still is a picture's text part for a still grading, shaped by a mode
// that may not be the one it was made with: the lines the detector is
// sure of lifted fully when the mode lifts them, none otherwise.
func (t *TextLift) still(x *text, mode int) *text {
	s := *x
	s.on, s.mask, s.ids, s.lines = t.on(mode, x.pts), nil, nil, nil
	if s.on {
		s.lift(t.Det, true)
	}
	return &s
}

// observe takes the detector's map of a picture (H x W, W its row
// stride) into the lines it follows.
func (t *TextLift) observe(x *text, m *nn.Tensor) {
	seen := t.Det.Observe(x.y, m.Data, m.Dim(2), x.pts, x.on)
	if x.f != nil {
		x.seen, x.observed = seen, true
	}
}

// input is the detector's input for a picture's text part, on the CPU.
func (x *text) input() *nn.Tensor { return textdet.Input(x.y.Pix, x.y.W, x.y.H, x.y.Stride) }

// textScale is the factor the detector's pictures are made smaller by,
// for a picture w wide: about 960 wide, the size it was trained at (half
// of 1920), whatever the source.
func textScale(w int) int { return max(1, (w+480)/960) }

// smallGrey is a picture's luma at 1/k of its size (k x k means,
// rounded), as the detector takes it.
func smallGrey(f *mvc.Frame, k int) textdet.Grey {
	w, h := f.Width/k, f.Height/k
	out := make([]byte, w*h)
	rows(h, func(y int) {
		for x := range w {
			s := 0
			for j := range k {
				row := f.Y[(k*y+j)*f.StrideY+k*x:]
				for i := range k {
					s += int(row[i])
				}
			}
			out[y*w+x] = byte((s + k*k/2) / (k * k))
		}
	})
	return textdet.Grey{Pix: out, W: w, H: h, Stride: w}
}

// nearState is the GPU's near end state, made once.
func (t *TextLift) nearState(g *nn.GPU) *nn.Tensor {
	if t.nearOn != g {
		t.near, t.nearOn = g.NearState(), g
	}
	return t.near
}

// textBins and textQuantile are the near end's histogram, as the GPU's.
const (
	textBins     = 512
	textQuantile = 0.95
	textAlpha    = 0.25
)

// textRange is how far either way of the screen the near end's histogram
// reaches, in pixels, for a picture w wide.
func textRange(w int) float32 { return float32(0.05 * float64(w)) }

// liftCPU is the GPU's NearEnd and LiftText on the CPU: d is the
// disparity of the active rows [top, bottom) of a w x h picture (d's
// first row is top's), fresh a new shot.
func (t *TextLift) liftCPU(x *text, d []float32, w, top, bottom int, fresh bool) {
	if fresh {
		t.cpuOK = false
	}
	lim := textRange(w)
	lo, bw := -lim, 2*lim/textBins
	var hist [textBins]int
	n := 0
	for y := 0; y < bottom-top; y += 2 {
		for xx := 0; xx < w; xx += 2 {
			v := d[y*w+xx]
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				continue
			}
			b := min(max(int(math.Floor(float64((v-lo)/bw))), 0), textBins-1)
			hist[b]++
			n++
		}
	}
	if n > 0 {
		want, c, b := int(textQuantile*float64(n)), 0, 0
		for ; b < textBins-1; b++ {
			c += hist[b]
			if c > want {
				break
			}
		}
		v := lo + (float32(b)+0.5)*bw
		if t.cpuOK {
			t.cpuNear += textAlpha * (v - t.cpuNear)
		} else {
			t.cpuNear, t.cpuOK = v, true
		}
	}
	if x.mask == nil {
		return
	}
	targets := make([]float32, len(x.lines))
	for i, r := range x.rings() {
		var hist [textBins]int
		n := 0
		for y := max(int(r[1]), top); y < min(int(r[3]), bottom); y += 2 {
			for xx := int(r[0]); xx < int(r[2]); xx += 2 {
				if xx >= int(r[4]) && xx < int(r[6]) && y >= int(r[5]) && y < int(r[7]) {
					continue
				}
				v := d[(y-top)*w+xx]
				if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
					continue
				}
				hist[min(max(int(math.Floor(float64((v-lo)/bw))), 0), textBins-1)]++
				n++
			}
		}
		targets[i] = float32(math.Inf(-1))
		if t.cpuOK {
			targets[i] = t.cpuNear
		}
		if n > 0 {
			c, b := 0, 0
			for ; b < textBins-1; b++ {
				c += hist[b]
				if 2*c > n {
					break
				}
			}
			targets[i] = max(targets[i], lo+(float32(b)+0.5)*bw+t.liftPixels(w))
		}
	}
	mw, mh := x.y.W, x.y.H
	rows(bottom-top, func(r int) {
		y := top + r
		kf := float32(x.k)
		fy := max((float32(y)+0.5)/kf-0.5, 0)
		y0 := min(int(fy), mh-1)
		y1 := min(y0+1, mh-1)
		ly := fy - float32(y0)
		for xx := range w {
			fx := max((float32(xx)+0.5)/kf-0.5, 0)
			x0 := min(int(fx), mw-1)
			x1 := min(x0+1, mw-1)
			lx := fx - float32(x0)
			m := x.mask
			a := float32(m[y0*mw+x0])*(1-lx) + float32(m[y0*mw+x1])*lx
			b := float32(m[y1*mw+x0])*(1-lx) + float32(m[y1*mw+x1])*lx
			k := (a*(1-ly) + b*ly) / 255
			if k <= 0 {
				continue
			}
			id := x.ids[min(y/x.k, mh-1)*mw+min(xx/x.k, mw-1)]
			if id == 0 {
				id = max(x.ids[y0*mw+x0], x.ids[y0*mw+x1], x.ids[y1*mw+x0], x.ids[y1*mw+x1])
			}
			if id == 0 {
				continue
			}
			if j, tg := r*w+xx, targets[id-1]; tg > d[j] {
				d[j] += k * (tg - d[j])
			}
		}
	})
}

// liftPixels is Lift in pixels of a picture w wide.
func (t *TextLift) liftPixels(w int) float32 {
	l := t.Lift
	if l == 0 {
		l = DefaultTextLift
	}
	return float32(l * float64(w))
}
