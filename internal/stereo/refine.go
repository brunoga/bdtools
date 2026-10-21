package stereo

// The depth's edges. The network sees the picture at about 518 lines and
// its depth is soft at objects' edges; upsampled, those edges fall a few
// pixels off the picture's, and the eyes show a halo of background
// dragged along with the nearer thing (or the reverse). A guided filter
// (He, Sun and Tang) with the picture's luma as the guide makes the depth,
// in each window, a linear function of the luma: its edges move to the
// picture's where the picture has one, it stays smooth where the picture
// is flat.

// Guide is the guided filter's settings.
type Guide struct {
	// Radius is the window's radius in pixels of the picture; zero turns
	// the filter off.
	Radius int
	// Eps is the regularisation, in luma units squared (the picture's luma
	// spread over 0..1, its 1st to 99th percentile): the larger, the less
	// the depth follows the picture's finer variations.
	Eps float64
}

// DefaultGuide is the filter the conversion uses: none for now, since
// tools/stereobench measures no gain from it yet (radius 8, eps 1e-3:
// the made right eye within 0.1 dB either way); kept to tune.
var DefaultGuide = Guide{}

// guided filters p (w x h) guided by the luma rows y (stride), in place.
func (g Guide) guided(p []float32, y []byte, stride, w, h int) {
	if g.Radius <= 0 {
		return
	}
	n := w * h
	// the luma's range, so a dark picture's edges count as a bright one's
	var hist [256]int
	for r := 0; r < h; r += 2 {
		for x := 0; x < w; x += 2 {
			hist[y[r*stride+x]]++
		}
	}
	total := 0
	for _, c := range hist {
		total += c
	}
	lo, hi, acc := 0, 255, 0
	for v, c := range hist {
		if acc += c; acc*100 <= total {
			lo = v
		}
	}
	acc = 0
	for v := 255; v >= 0; v-- {
		if acc += hist[v]; acc*100 <= total {
			hi = v
		}
	}
	span := float64(max(hi-lo, 16))
	I := make([]float64, n)
	P := make([]float64, n)
	rows(h, func(r int) {
		for x := range w {
			I[r*w+x] = min(max((float64(y[r*stride+x])-float64(lo))/span, 0), 1)
			P[r*w+x] = float64(p[r*w+x])
		}
	})
	ip := make([]float64, n)
	ii := make([]float64, n)
	for i := range n {
		ip[i] = I[i] * P[i]
		ii[i] = I[i] * I[i]
	}
	mI, mP := box(I, w, h, g.Radius), box(P, w, h, g.Radius)
	mIP, mII := box(ip, w, h, g.Radius), box(ii, w, h, g.Radius)
	a, b := ip, ii // reused
	for i := range n {
		cov := mIP[i] - mI[i]*mP[i]
		v := mII[i] - mI[i]*mI[i]
		a[i] = cov / (v + g.Eps)
		b[i] = mP[i] - a[i]*mI[i]
	}
	ma, mb := box(a, w, h, g.Radius), box(b, w, h, g.Radius)
	rows(h, func(r int) {
		for x := range w {
			i := r*w + x
			p[i] = float32(ma[i]*I[i] + mb[i])
		}
	})
}

// box is the mean of each (2r+1)-square window, clipped at the edges.
func box(v []float64, w, h, r int) []float64 {
	tmp := make([]float64, len(v))
	rows(h, func(y int) {
		row, out := v[y*w:(y+1)*w], tmp[y*w:(y+1)*w]
		var s float64
		for x := range min(r, w) {
			s += row[x]
		}
		for x := range w {
			if x+r < w {
				s += row[x+r]
			}
			if x-r-1 >= 0 {
				s -= row[x-r-1]
			}
			out[x] = s / float64(min(x+r, w-1)-max(x-r, 0)+1)
		}
	})
	out := make([]float64, len(v))
	chunks(w, func(xa, xb int) {
		s := make([]float64, xb-xa)
		for y := range min(r, h) {
			for x := xa; x < xb; x++ {
				s[x-xa] += tmp[y*w+x]
			}
		}
		for y := range h {
			n := float64(min(y+r, h-1) - max(y-r, 0) + 1)
			for x := xa; x < xb; x++ {
				if y+r < h {
					s[x-xa] += tmp[(y+r)*w+x]
				}
				if y-r-1 >= 0 {
					s[x-xa] -= tmp[(y-r-1)*w+x]
				}
				out[y*w+x] = s[x-xa] / n
			}
		}
	})
	return out
}
