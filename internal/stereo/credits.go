package stereo

import "github.com/brunoga/bdtools/mvc"

// The credits: text on black (a film's end crawl, its cards), which the
// 3D discs put on the screen, flat (tools/stereobench, the ends of ten
// discs: 0 disparity, within a pixel; one at a constant -8). The depth
// network gives it neither: letters at their own depths, the black around
// them something else again; a picture the credits frame too far off
// both ways (the made right eye 18.5 dB against the discs', 40.3 flat).

// CreditFeatures is a picture's measures of the credits (its active rows
// [top, bottom), every other pixel): Dark, the share that is black; Thin,
// the share of the rest in runs (across a row) of at most creditStroke
// pixels with black either side, as letters' strokes are.
type CreditFeatures struct {
	Dark, Thin float64
}

const (
	creditBlack  = 40 // luma at most this is black
	creditStroke = 12 // pixels (of the half-size rows): a stroke at most
)

// Credits measures a picture's credit features.
func Credits(f *mvc.Frame, top, bottom int) CreditFeatures {
	w := f.Width
	var dark, lit, thin float64
	for y := top; y < bottom; y += 2 {
		row := f.Y[y*f.StrideY : y*f.StrideY+w]
		for x := 0; x < w; {
			if row[x] <= creditBlack {
				dark++
				x += 2
				continue
			}
			a := x
			for x < w && row[x] > creditBlack {
				x += 2
			}
			n := float64((x - a + 1) / 2)
			lit += n
			if (x-a)/2 <= creditStroke && a > 0 && x < w {
				thin += n
			}
		}
	}
	var c CreditFeatures
	if all := dark + lit; all > 0 {
		c.Dark = dark / all
	}
	if lit > 0 {
		c.Thin = thin / lit
	}
	return c
}

// A picture is the credits when this much of it is black and this much of
// the rest thin: on the discs' credits 0.85 to 0.97 black, 0.69 to 1 thin
// (most over 0.94); on the films' pictures (the benchmark's and the glass
// clips, Gravity's space among them) at most 0.77 black, 0.32 thin.
const (
	creditDark = 0.8
	creditThin = 0.6
)

// IsCredits reports whether a picture's features are the credits'.
func (c CreditFeatures) IsCredits() bool { return c.Dark >= creditDark && c.Thin >= creditThin }

// creditState is how flat the pictures are made, from 1 (their depth) to
// 0 (flat, on the screen): at a new shot what the picture is, within one
// a change once creditHold pictures in a row say so, eased over
// creditEase pictures.
type creditState struct {
	flat   bool
	run    int // pictures in a row saying otherwise
	factor float32
}

const (
	creditHold = 24
	creditEase = 12
)

// next is the factor of a picture's disparity, its features c, fresh at
// a new shot.
func (s *creditState) next(c CreditFeatures, fresh bool) float32 {
	is := c.IsCredits()
	switch {
	case fresh:
		s.flat, s.run = is, 0
		s.factor = 1
		if is {
			s.factor = 0
		}
		return s.factor
	case is != s.flat:
		if s.run++; s.run >= creditHold {
			s.flat, s.run = is, 0
		}
	default:
		s.run = 0
	}
	want := float32(1)
	if s.flat {
		want = 0
	}
	const step = 1.0 / creditEase
	switch {
	case s.factor < want:
		s.factor = min(s.factor+step, want)
	case s.factor > want:
		s.factor = max(s.factor-step, want)
	}
	return s.factor
}
