package stereo

import (
	"testing"

	"github.com/brunoga/bdtools/mvc"
)

// creditPicture is black with rows of white letters' strokes (3 pixels
// wide, 6 apart) every 40 rows.
func creditPicture(w, h int) *mvc.Frame {
	f := newFrame(w, h)
	for i := range f.Y {
		f.Y[i] = 16
	}
	for y := 0; y < h; y++ {
		if y%40 >= 12 {
			continue
		}
		for x := w / 4; x < 3*w/4; x++ {
			if x%9 < 3 {
				f.Y[y*f.StrideY+x] = 235
			}
		}
	}
	return f
}

func TestCredits(t *testing.T) {
	c := Credits(creditPicture(320, 180), 0, 180)
	if !c.IsCredits() {
		t.Errorf("text on black: %+v, not the credits", c)
	}
	// a bright disc in the dark: space, a planet
	f := newFrame(320, 180)
	for y := range 180 {
		for x := range 320 {
			v := byte(16)
			if dx, dy := x-160, y-90; dx*dx+dy*dy < 50*50 {
				v = 180
			}
			f.Y[y*f.StrideY+x] = v
		}
	}
	if c := Credits(f, 0, 180); c.IsCredits() {
		t.Errorf("a planet in the dark: %+v, the credits", c)
	}
	// an ordinary picture
	for i := range f.Y {
		f.Y[i] = byte(60 + i%7*20)
	}
	if c := Credits(f, 0, 180); c.IsCredits() {
		t.Errorf("a picture: %+v, the credits", c)
	}
}

func TestCreditState(t *testing.T) {
	credits, film := CreditFeatures{Dark: 0.9, Thin: 0.95}, CreditFeatures{Dark: 0.3, Thin: 0.1}
	var s creditState
	if k := s.next(credits, true); k != 0 {
		t.Fatalf("a shot of credits: %g, not flat", k)
	}
	// a few pictures saying otherwise change nothing
	for range creditHold - 1 {
		if k := s.next(film, false); k != 0 {
			t.Fatalf("within the hold: %g", k)
		}
	}
	s.next(credits, false)
	// held long enough, the depth eases in
	var k float32
	for range creditHold {
		k = s.next(film, false)
	}
	if k <= 0 || k >= 1 {
		t.Fatalf("easing: %g", k)
	}
	for range creditEase {
		k = s.next(film, false)
	}
	if k != 1 {
		t.Fatalf("eased: %g, not 1", k)
	}
	if k := s.next(credits, true); k != 0 {
		t.Fatalf("a cut to credits: %g, not flat", k)
	}
}
