package stereo

import (
	"math"
	"slices"

	"github.com/brunoga/bdtools/internal/nn"
	"github.com/brunoga/bdtools/mvc"
)

// Redepth makes a stereo pair's eyes (8-bit 4:2:0, the same size) again
// with its depth scaled by k and moved by shift (pixels of disparity,
// positive nearer). The disparity is measured between the eyes, each way
// (Match), and each new eye is rendered from its own camera at (1-k)/2 of
// the way to the other, what it then sees that its camera did not (behind
// a nearer thing's edge) taken from the other camera's view, which saw it,
// rendered there too: so with k below 1, the views between the cameras,
// little is invented. Above 1 the views are outside the cameras', and
// what neither saw is filled from the farther side, as Pair fills. The
// shift moves the eyes apart (each half of it), black coming in at the
// edges. With k 1 the eyes are only shifted.
//
// The matching is on g when it is not nil (MatchBothGPU), else on the CPU.
func Redepth(g *nn.GPU, l, r *mvc.Frame, k, shift float32) (nl, nr *mvc.Frame) {
	if k != 1 {
		w, h := l.Width, l.Height
		var ml, mr *Disparity
		var err error
		if g != nil {
			ml, mr, err = MatchBothGPU(g, l, r)
		}
		if g == nil || err != nil {
			ml, mr = MatchBoth(l, r)
		}
		dl, dr := Dense(ml, w, h), Dense(mr, w, h)
		a := (1 - k) / 2
		nl = view(l, dl, -a, r, dr, 1-a)
		nr = view(r, dr, a, l, dl, -(1 - a))
		l, r = nl, nr
	}
	if shift == 0 && k != 1 {
		return nl, nr
	}
	return translate(l, shift/2), translate(r, -shift/2)
}

// view renders f moved by sign times its disparity d, its holes taken
// from o (the other eye, its disparity od) moved by osign to the same
// view where o saw them.
func view(f *mvc.Frame, d []float32, sign float32, o *mvc.Frame, od []float32, osign float32) *mvc.Frame {
	eye, holes := Render(f, d, sign)
	other, oholes := Render(o, od, osign)
	w, h := eye.Width, eye.Height
	for y := range h {
		for x := range w {
			if i := y*w + x; holes[i] && !oholes[i] {
				eye.Y[i] = other.Y[i]
			}
		}
	}
	cw, ch := (w+1)/2, (h+1)/2
	for y := range ch {
		for x := range cw {
			// a chroma sample is the other's where its luma's first is
			if i := 2*y*w + 2*x; holes[i] && !oholes[i] {
				eye.Cb[y*cw+x], eye.Cr[y*cw+x] = other.Cb[y*cw+x], other.Cr[y*cw+x]
			}
		}
	}
	return eye
}

// Dense is a measured disparity (Match's, half resolution) at the
// picture's size (w x h, one value a luma pixel): each row's unsure runs
// filled from their farther side (behind an edge, what is unsure is
// mostly what one eye does not see, which lies behind), then
// interpolated up.
func Dense(dm *Disparity, w, h int) []float32 {
	half := slices.Clone(dm.D)
	for y := range dm.H {
		row, ok := half[y*dm.W:(y+1)*dm.W], dm.Valid[y*dm.W:(y+1)*dm.W]
		for x := 0; x < dm.W; {
			if ok[x] {
				x++
				continue
			}
			a := x
			for x < dm.W && !ok[x] {
				x++
			}
			var v float32
			switch {
			case a == 0 && x == dm.W:
				v = 0
			case a == 0:
				v = row[x]
			case x == dm.W:
				v = row[a-1]
			default:
				v = min(row[a-1], row[x])
			}
			for i := a; i < x; i++ {
				row[i] = v
			}
		}
	}
	out := make([]float32, w*h)
	rows(h, func(y int) {
		fy := max((float32(y)-0.5)/2, 0)
		y0 := min(int(fy), dm.H-1)
		y1 := min(y0+1, dm.H-1)
		ty := fy - float32(y0)
		for x := range w {
			fx := max((float32(x)-0.5)/2, 0)
			x0 := min(int(fx), dm.W-1)
			x1 := min(x0+1, dm.W-1)
			tx := fx - float32(x0)
			a := half[y0*dm.W+x0]*(1-tx) + half[y0*dm.W+x1]*tx
			b := half[y1*dm.W+x0]*(1-tx) + half[y1*dm.W+x1]*tx
			out[y*w+x] = a*(1-ty) + b*ty
		}
	})
	return out
}

// translate is f moved right by dx pixels (a fraction too), black where
// nothing comes in.
func translate(f *mvc.Frame, dx float32) *mvc.Frame {
	w, h := f.Width, f.Height
	cw, ch := (w+1)/2, (h+1)/2
	out := newFrame(w, h)
	out.PTS = f.PTS
	shiftRow := func(src, dst []byte, n int, dx float32, black byte) {
		i0 := int(math.Floor(float64(dx)))
		t := dx - float32(i0)
		for x := range n {
			// out[x] = src[x - dx]: between src[x-i0-1] and src[x-i0]
			a, b := x-i0-1, x-i0
			switch {
			case b < 0 || b >= n || t > 0 && a < 0:
				dst[x] = black
			case t == 0:
				dst[x] = src[b]
			default:
				dst[x] = byte(float32(src[a])*t + float32(src[b])*(1-t) + 0.5)
			}
		}
	}
	rows(h, func(y int) { shiftRow(f.Y[y*f.StrideY:], out.Y[y*w:(y+1)*w], w, dx, 16) })
	rows(ch, func(y int) {
		shiftRow(f.Cb[y*f.StrideC:], out.Cb[y*cw:(y+1)*cw], cw, dx/2, 128)
		shiftRow(f.Cr[y*f.StrideC:], out.Cr[y*cw:(y+1)*cw], cw, dx/2, 128)
	})
	return out
}
