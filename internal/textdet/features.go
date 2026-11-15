package textdet

import "math"

// Grey is a grey picture: w x h bytes, rows stride apart.
type Grey struct {
	Pix          []byte
	W, H, Stride int
}

func (g Grey) at(x, y int) float32 { return float32(g.Pix[y*g.Stride+x]) }

// strokeContrast is how far (in luma) a pixel is from the mean around it
// to count as part of a letter's stroke.
const strokeContrast = 25

// The measures of a region that the classifier reads, in its order.
const (
	fHeight = iota
	fWidth
	fAspect
	fOffCentre
	fRow
	fStrokeShare
	fStrokeLuma
	fStrokeContrast
	fPolarity
	fRingLuma
	fRingSpread
	fBoxSpread
	fProb
	fChangeBefore     // inside the box, against the picture half a second before
	fRingChangeBefore // in the ring, the same
	fChangeAfter      // inside, against half a second after
	fRingChangeAfter
	fChangeLong // inside, against two seconds before
	fRingChangeLong
	numFeatures
)

// Features are a region's measures in picture y (whose map is p, of y's
// size, row stride ps): its size and place, its strokes (the pixels
// strokeContrast or more from the mean around them) and how they stand
// against the box and the ring around it, the map's mean over it, and
// how much the box and the ring change against the pictures before,
// after and long before (NaN for a picture missing).
func Features(y Grey, p []float32, ps int, r Region, before, after, long *Grey) [numFeatures]float32 {
	var f [numFeatures]float32
	H, W := float32(y.H), float32(y.W)
	bw, bh := r.X1-r.X0, r.Y1-r.Y0
	f[fHeight] = float32(r.H) / H
	f[fWidth] = float32(bw) / W
	f[fAspect] = float32(bw) / float32(max(r.H, 1))
	f[fOffCentre] = float32(math.Abs(float64(r.X0+r.X1)/2/float64(W) - 0.5))
	f[fRow] = float32(r.Y0+r.Y1) / 2 / H

	sub := make([]float32, bw*bh)
	for yy := range bh {
		for xx := range bw {
			sub[yy*bw+xx] = y.at(r.X0+xx, r.Y0+yy)
		}
	}
	mean := boxMean(sub, bw, bh, 2*max(3, r.H/2)+1)
	var n, luma, contrast, sign float64
	for i, v := range sub {
		d := float64(v - mean[i])
		if math.Abs(d) > strokeContrast {
			n++
			luma += float64(v)
			contrast += math.Abs(d)
			if d > 0 {
				sign++
			} else {
				sign--
			}
		}
	}
	f[fStrokeShare] = float32(n / float64(len(sub)))
	if n > 0 {
		f[fStrokeLuma], f[fStrokeContrast], f[fPolarity] = float32(luma/n), float32(contrast/n), float32(sign/n)
	}
	rm, rs := ringStats(y, r)
	f[fRingLuma], f[fRingSpread] = rm, rs
	_, f[fBoxSpread] = meanSpread(sub)
	var ps64 float64
	for yy := r.Y0; yy < r.Y1; yy++ {
		for xx := r.X0; xx < r.X1; xx++ {
			ps64 += float64(p[yy*ps+xx])
		}
	}
	f[fProb] = float32(ps64 / float64(bw*bh))
	for i, a := range []*Grey{before, after, long} {
		f[fChangeBefore+2*i], f[fRingChangeBefore+2*i] = change(y, a, r)
	}
	return f
}

// ring is the band around a region, as wide as its text is tall (at
// least 8 pixels), clipped to the picture.
func ring(y Grey, r Region) (x0, y0, x1, y1 int) {
	g := max(8, r.H)
	return max(0, r.X0-g), max(0, r.Y0-g), min(y.W, r.X1+g), min(y.H, r.Y1+g)
}

func inBox(r Region, x, y int) bool { return x >= r.X0 && x < r.X1 && y >= r.Y0 && y < r.Y1 }

// ringStats are the mean and the spread (standard deviation) of the
// ring's luma.
func ringStats(y Grey, r Region) (float32, float32) {
	x0, y0, x1, y1 := ring(y, r)
	var s, s2, n float64
	for yy := y0; yy < y1; yy++ {
		for xx := x0; xx < x1; xx++ {
			if inBox(r, xx, yy) {
				continue
			}
			v := float64(y.at(xx, yy))
			s, s2, n = s+v, s2+v*v, n+1
		}
	}
	if n == 0 {
		return float32(math.NaN()), float32(math.NaN())
	}
	m := s / n
	return float32(m), float32(math.Sqrt(max(s2/n-m*m, 0)))
}

func meanSpread(v []float32) (float32, float32) {
	var s, s2 float64
	for _, x := range v {
		s, s2 = s+float64(x), s2+float64(x)*float64(x)
	}
	n := float64(len(v))
	m := s / n
	return float32(m), float32(math.Sqrt(max(s2/n-m*m, 0)))
}

// change is the mean absolute difference of y and a inside the region
// and in its ring (NaN when there is no a).
func change(y Grey, a *Grey, r Region) (float32, float32) {
	if a == nil {
		return float32(math.NaN()), float32(math.NaN())
	}
	x0, y0, x1, y1 := ring(y, r)
	var in, out, nin, nout float64
	for yy := y0; yy < y1; yy++ {
		for xx := x0; xx < x1; xx++ {
			d := math.Abs(float64(y.at(xx, yy) - a.at(xx, yy)))
			if inBox(r, xx, yy) {
				in, nin = in+d, nin+1
			} else {
				out, nout = out+d, nout+1
			}
		}
	}
	ri := float32(math.NaN())
	if nout > 0 {
		ri = float32(out / nout)
	}
	return float32(in / nin), ri
}

// boxMean is the mean of the size x size square around each element of v
// (w x h), the edges mirrored (d c b a | a b c d | d c b a), as scipy's
// uniform_filter makes it.
func boxMean(v []float32, w, h, size int) []float32 {
	tmp := make([]float32, len(v))
	out := make([]float32, len(v))
	half := size / 2
	mirror := func(i, n int) int {
		for i < 0 || i >= n {
			if i < 0 {
				i = -i - 1
			}
			if i >= n {
				i = 2*n - i - 1
			}
		}
		return i
	}
	for y := range h {
		row := v[y*w : (y+1)*w]
		for x := range w {
			var s float32
			for k := x - half; k <= x+half; k++ {
				s += row[mirror(k, w)]
			}
			tmp[y*w+x] = s / float32(size)
		}
	}
	for x := range w {
		for y := range h {
			var s float32
			for k := y - half; k <= y+half; k++ {
				s += tmp[mirror(k, h)*w+x]
			}
			out[y*w+x] = s / float32(size)
		}
	}
	return out
}
