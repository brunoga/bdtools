package main

import (
	"encoding/csv"
	"fmt"
	"math"
	"math/rand/v2"
	"runtime"
	"slices"
	"strconv"
	"sync"

	"github.com/brunoga/bdtools/internal/stereo"
)

// The parallax probe (-parallax K,...): does the motion between a
// picture's left eye and the one K pictures before carry depth the
// network's does not? Each measured picture's dense optical flow (half
// resolution, pyramidal Lucas-Kanade, kept where forward and backward
// agree) has the camera's rotation and the dominant plane's motion taken
// out (a homography, by RANSAC); what is left is parallax, along lines
// through the epipole, its size against the distance from it the inverse
// depth's difference from the plane's (up to a scale). Fitting the disc's
// disparity by the network's depth and by both the network's depth and
// that parallax says how much the parallax adds.

var (
	parallaxKs  []int
	parallaxCSV *csv.Writer
)

func parseKs(s string) ([]int, error) {
	var ks []int
	for f := range splitComma(s) {
		k, err := strconv.Atoi(f)
		if err != nil || k < 1 {
			return nil, fmt.Errorf("-parallax: bad step %q", f)
		}
		ks = append(ks, k)
	}
	return ks, nil
}

func splitComma(s string) func(func(string) bool) {
	return func(yield func(string) bool) {
		a := 0
		for i := 0; i <= len(s); i++ {
			if i == len(s) || s[i] == ',' {
				if i > a && !yield(s[a:i]) {
					return
				}
				a = i + 1
			}
		}
	}
}

// parallaxStats is a clip's sums over the pictures the camera moved in.
type parallaxStats struct {
	frames, moving   int
	errNet, errBoth  float64 // after fitting, moving pictures
	corrNet, corrPar float64
}

// parallaxFrame measures one picture against the picture k before (both
// half resolution luma, w x h), with the disc's disparity dm and the
// network's depth hr (both half resolution), its active rows top..bottom
// (half resolution).
func parallaxFrame(clip string, frame, k int, cur, prev []byte, w, h int, dm *stereo.Disparity, hr []float32, top, bottom int, st *parallaxStats) {
	a, b := toFloat(cur), toFloat(prev)
	fu, fv, ok := flowLK(a, b, w, h)
	bu, bv, _ := flowLK(b, a, w, h)
	// forward-backward agreement
	valid := make([]bool, w*h)
	nValid := 0
	for y := top; y < bottom; y++ {
		for x := range w {
			i := y*w + x
			if !ok[i] {
				continue
			}
			x2, y2 := float32(x)+fu[i], float32(y)+fv[i]
			if x2 < 0 || y2 < 0 || x2 > float32(w-1) || y2 > float32(h-1) {
				continue
			}
			ru, rv := sample(bu, w, h, x2, y2), sample(bv, w, h, x2, y2)
			if du, dv := fu[i]+ru, fv[i]+rv; du*du+dv*dv < 1 {
				valid[i] = true
				nValid++
			}
		}
	}
	st.frames++
	rows := bottom - top
	if rows <= 0 || float64(nValid) < 0.2*float64(rows*w) {
		return
	}
	// coordinates normalised about the centre, the width's half a unit
	cx, cy, s := float64(w)/2, float64(h)/2, 2/float64(w)
	var pts [][4]float64 // x, y, x', y' (normalised)
	for y := top; y < bottom; y += 4 {
		for x := 0; x < w; x += 4 {
			if i := y*w + x; valid[i] {
				pts = append(pts, [4]float64{(float64(x) - cx) * s, (float64(y) - cy) * s,
					(float64(x) + float64(fu[i]) - cx) * s, (float64(y) + float64(fv[i]) - cy) * s})
			}
		}
	}
	H, inl := ransacHomography(pts, 1.0*s)
	if H == nil {
		return
	}
	// residuals, the epipole, the parallax
	n := w * h
	ru, rv := make([]float64, n), make([]float64, n)
	var mag []float64
	var L [3][3]float64
	for y := top; y < bottom; y++ {
		for x := range w {
			i := y*w + x
			if !valid[i] {
				continue
			}
			X, Y := (float64(x)-cx)*s, (float64(y)-cy)*s
			px, py := applyH(H, X, Y)
			ru[i] = (float64(x)+float64(fu[i])-cx)*s - px
			rv[i] = (float64(y)+float64(fv[i])-cy)*s - py
			m := math.Hypot(ru[i], rv[i])
			mag = append(mag, m/s)
			if m < 0.3*s {
				continue
			}
			// the line through (X, Y) along the residual
			l := [3]float64{-rv[i], ru[i], rv[i]*X - ru[i]*Y}
			nl := math.Sqrt(l[0]*l[0] + l[1]*l[1] + l[2]*l[2])
			for r := range 3 {
				for c := range 3 {
					L[r][c] += m * l[r] * l[c] / (nl * nl)
				}
			}
		}
	}
	slices.Sort(mag)
	strength := mag[len(mag)/2] // half resolution pixels
	strong := mag[len(mag)*9/10]
	e := smallestEigen(L)
	par := make([]float64, n)
	parOK := make([]bool, n)
	for y := top; y < bottom; y++ {
		for x := range w {
			i := y*w + x
			if !valid[i] {
				continue
			}
			X, Y := (float64(x)-cx)*s, (float64(y)-cy)*s
			vx, vy := e[2]*X-e[0], e[2]*Y-e[1]
			v2 := vx*vx + vy*vy
			if v2 < 0.01 {
				continue
			}
			par[i] = (ru[i]*vx + rv[i]*vy) / v2
			parOK[i] = true
		}
	}
	// fits on the pixels all three have
	var xs, ps, ds []float64
	for y := top; y < bottom; y++ {
		for x := range w {
			i := y*w + x
			if parOK[i] && dm.Valid[i] && !math.IsNaN(float64(hr[i])) {
				xs = append(xs, float64(hr[i]))
				ps = append(ps, par[i])
				ds = append(ds, float64(dm.D[i]))
			}
		}
	}
	if len(ds) < 1000 {
		return
	}
	eNet := fitErr(ds, xs)
	eBoth := fitErr(ds, xs, ps)
	ePar := fitErr(ds, ps)
	cNet, cPar := math.Abs(corr(xs, ds)), math.Abs(corr(ps, ds))
	moving := strong > 1.0 // the 10% most moved a pixel or more after the homography
	if moving {
		st.moving++
		st.errNet += eNet
		st.errBoth += eBoth
		st.corrNet += cNet
		st.corrPar += cPar
	}
	if parallaxCSV != nil {
		_ = parallaxCSV.Write([]string{clip, strconv.Itoa(frame), strconv.Itoa(k),
			f3(strength), f3(strong), f3(float64(inl) / float64(len(pts))), f3(float64(len(ds)) / float64(rows*w)),
			f3(cNet), f3(cPar), f3(eNet), f3(ePar), f3(eBoth)})
	}
}

func f3(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }

func toFloat(p []byte) []float32 {
	out := make([]float32, len(p))
	for i, v := range p {
		out[i] = float32(v)
	}
	return out
}

func corr(a, b []float64) float64 {
	var sa, sb, saa, sbb, sab float64
	for i := range a {
		sa += a[i]
		sb += b[i]
		saa += a[i] * a[i]
		sbb += b[i] * b[i]
		sab += a[i] * b[i]
	}
	n := float64(len(a))
	va, vb := saa-sa*sa/n, sbb-sb*sb/n
	if va <= 0 || vb <= 0 {
		return 0
	}
	return (sab - sa*sb/n) / math.Sqrt(va*vb)
}

// fitErr is the mean absolute error of the least-squares fit of d by the
// features and a constant.
func fitErr(d []float64, feats ...[]float64) float64 {
	m := len(feats) + 1
	A := make([][]float64, m)
	for r := range A {
		A[r] = make([]float64, m+1)
	}
	f := make([]float64, m)
	for i := range d {
		for j, ft := range feats {
			f[j] = ft[i]
		}
		f[m-1] = 1
		for r := range m {
			for c := range m {
				A[r][c] += f[r] * f[c]
			}
			A[r][m] += f[r] * d[i]
		}
	}
	coef := solve(A)
	if coef == nil {
		return math.NaN()
	}
	var e float64
	for i := range d {
		p := coef[m-1]
		for j, ft := range feats {
			p += coef[j] * ft[i]
		}
		e += math.Abs(p - d[i])
	}
	return e / float64(len(d))
}

// solve solves the augmented system A (m x m+1) by Gaussian elimination
// with partial pivoting.
func solve(A [][]float64) []float64 {
	m := len(A)
	for c := range m {
		p := c
		for r := c + 1; r < m; r++ {
			if math.Abs(A[r][c]) > math.Abs(A[p][c]) {
				p = r
			}
		}
		if math.Abs(A[p][c]) < 1e-12 {
			return nil
		}
		A[c], A[p] = A[p], A[c]
		for r := c + 1; r < m; r++ {
			f := A[r][c] / A[c][c]
			for k := c; k <= m; k++ {
				A[r][k] -= f * A[c][k]
			}
		}
	}
	x := make([]float64, m)
	for r := m - 1; r >= 0; r-- {
		v := A[r][m]
		for k := r + 1; k < m; k++ {
			v -= A[r][k] * x[k]
		}
		x[r] = v / A[r][r]
	}
	return x
}

func applyH(H []float64, x, y float64) (float64, float64) {
	w := H[6]*x + H[7]*y + 1
	return (H[0]*x + H[1]*y + H[2]) / w, (H[3]*x + H[4]*y + H[5]) / w
}

// fitH is the least-squares homography (h33 1) of the correspondences.
func fitH(pts [][4]float64) []float64 {
	A := make([][]float64, 8)
	for r := range A {
		A[r] = make([]float64, 9)
	}
	add := func(row [8]float64, rhs float64) {
		for r := range 8 {
			for c := range 8 {
				A[r][c] += row[r] * row[c]
			}
			A[r][8] += row[r] * rhs
		}
	}
	for _, p := range pts {
		x, y, u, v := p[0], p[1], p[2], p[3]
		add([8]float64{x, y, 1, 0, 0, 0, -u * x, -u * y}, u)
		add([8]float64{0, 0, 0, x, y, 1, -v * x, -v * y}, v)
	}
	return solve(A)
}

func ransacHomography(pts [][4]float64, thresh float64) ([]float64, int) {
	if len(pts) < 50 {
		return nil, 0
	}
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // sampling, repeatably
	count := func(H []float64, keep bool) (int, [][4]float64) {
		var in [][4]float64
		c := 0
		for _, p := range pts {
			u, v := applyH(H, p[0], p[1])
			if math.Hypot(u-p[2], v-p[3]) < thresh {
				c++
				if keep {
					in = append(in, p)
				}
			}
		}
		return c, in
	}
	var best []float64
	bestN := 0
	for range 300 {
		var four [][4]float64
		for range 4 {
			four = append(four, pts[r.IntN(len(pts))])
		}
		H := fitH(four)
		if H == nil {
			continue
		}
		if n, _ := count(H, false); n > bestN {
			best, bestN = H, n
		}
	}
	if best == nil {
		return nil, 0
	}
	_, in := count(best, true)
	if H := fitH(in); H != nil {
		best = H
	}
	n, _ := count(best, false)
	return best, n
}

// smallestEigen is the unit eigenvector of a symmetric 3x3 matrix's
// smallest eigenvalue (Jacobi rotations).
func smallestEigen(A [3][3]float64) [3]float64 {
	V := [3][3]float64{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}
	for range 50 {
		p, q := 0, 1
		for i := range 3 {
			for j := i + 1; j < 3; j++ {
				if math.Abs(A[i][j]) > math.Abs(A[p][q]) {
					p, q = i, j
				}
			}
		}
		if math.Abs(A[p][q]) < 1e-15 {
			break
		}
		th := 0.5 * math.Atan2(2*A[p][q], A[q][q]-A[p][p])
		c, s := math.Cos(th), math.Sin(th)
		for k := range 3 {
			akp, akq := A[k][p], A[k][q]
			A[k][p], A[k][q] = c*akp-s*akq, s*akp+c*akq
		}
		for k := range 3 {
			apk, aqk := A[p][k], A[q][k]
			A[p][k], A[q][k] = c*apk-s*aqk, s*apk+c*aqk
		}
		for k := range 3 {
			vkp, vkq := V[k][p], V[k][q]
			V[k][p], V[k][q] = c*vkp-s*vkq, s*vkp+c*vkq
		}
	}
	m := 0
	for i := 1; i < 3; i++ {
		if A[i][i] < A[m][m] {
			m = i
		}
	}
	return [3]float64{V[0][m], V[1][m], V[2][m]}
}

// flowLK is the dense flow from a to b (b(x + flow) = a(x)), pyramidal
// Lucas-Kanade, and where the structure is enough to trust it.
func flowLK(a, b []float32, w, h int) (u, v []float32, ok []bool) {
	type level struct {
		a, b []float32
		w, h int
	}
	levels := []level{{a, b, w, h}}
	for levels[len(levels)-1].w > 80 {
		l := levels[len(levels)-1]
		nw, nh := l.w/2, l.h/2
		levels = append(levels, level{down(l.a, l.w, nw, nh), down(l.b, l.w, nw, nh), nw, nh})
	}
	const radius = 4
	var fu, fv []float32
	var good []bool
	for li := len(levels) - 1; li >= 0; li-- {
		l := levels[li]
		n := l.w * l.h
		nu, nv := make([]float32, n), make([]float32, n)
		if fu != nil {
			pw, ph := levels[li+1].w, levels[li+1].h
			for y := range l.h {
				for x := range l.w {
					sx, sy := float32(x)/2, float32(y)/2
					nu[y*l.w+x] = 2 * sample(fu, pw, ph, min(sx, float32(pw-1)), min(sy, float32(ph-1)))
					nv[y*l.w+x] = 2 * sample(fv, pw, ph, min(sx, float32(pw-1)), min(sy, float32(ph-1)))
				}
			}
		}
		fu, fv = nu, nv
		ix, iy := grad(l.a, l.w, l.h)
		sxx, sxy, syy := make([]float32, n), make([]float32, n), make([]float32, n)
		for i := range n {
			sxx[i], sxy[i], syy[i] = ix[i]*ix[i], ix[i]*iy[i], iy[i]*iy[i]
		}
		sxx, sxy, syy = boxF(sxx, l.w, l.h, radius), boxF(sxy, l.w, l.h, radius), boxF(syy, l.w, l.h, radius)
		// each pixel's confidence: its window's structure tensor's smaller
		// eigenvalue, a pixel's
		area := float32((2*radius + 1) * (2*radius + 1))
		conf := make([]float32, n)
		for i := range n {
			tr, det := sxx[i]+syy[i], sxx[i]*syy[i]-sxy[i]*sxy[i]
			conf[i] = (tr/2 - float32(math.Sqrt(math.Max(float64(tr*tr/4-det), 0)))) / area
		}
		for range 8 {
			// the error of b warped by the flow against a
			ex, ey := make([]float32, n), make([]float32, n)
			parRows(l.h, func(y int) {
				for x := range l.w {
					i := y*l.w + x
					wx := min(max(float32(x)+fu[i], 0), float32(l.w-1))
					wy := min(max(float32(y)+fv[i], 0), float32(l.h-1))
					it := sample(l.b, l.w, l.h, wx, wy) - l.a[i]
					ex[i], ey[i] = ix[i]*it, iy[i]*it
				}
			})
			ex, ey = boxF(ex, l.w, l.h, radius), boxF(ey, l.w, l.h, radius)
			// only where the window is sure; each step at most a pixel
			for i := range n {
				if conf[i] < 0.5 {
					continue
				}
				det := sxx[i]*syy[i] - sxy[i]*sxy[i]
				du := -(syy[i]*ex[i] - sxy[i]*ey[i]) / det
				dv := -(sxx[i]*ey[i] - sxy[i]*ex[i]) / det
				fu[i] += min(max(du, -1), 1)
				fv[i] += min(max(dv, -1), 1)
			}
		}
		if li > 0 {
			// up to the next level smoothed, weighted by confidence, so
			// the unsure windows take their sure neighbours' flow
			wu, wv := make([]float32, n), make([]float32, n)
			for i := range n {
				c := min(conf[i], 50)
				wu[i], wv[i] = c*fu[i], c*fv[i]
			}
			cw := boxF(func() []float32 {
				c := make([]float32, n)
				for i := range n {
					c[i] = min(conf[i], 50)
				}
				return c
			}(), l.w, l.h, 2*radius)
			wu, wv = boxF(wu, l.w, l.h, 2*radius), boxF(wv, l.w, l.h, 2*radius)
			for i := range n {
				if cw[i] > 1e-3 {
					fu[i], fv[i] = wu[i]/cw[i], wv[i]/cw[i]
				}
			}
			continue
		}
		good = make([]bool, n)
		for i := range n {
			good[i] = conf[i] > 1
		}
	}
	return fu, fv, good
}

func down(p []float32, w, nw, nh int) []float32 {
	out := make([]float32, nw*nh)
	for y := range nh {
		for x := range nw {
			i := 2*y*w + 2*x
			out[y*nw+x] = (p[i] + p[i+1] + p[i+w] + p[i+w+1]) / 4
		}
	}
	return out
}

func grad(p []float32, w, h int) (gx, gy []float32) {
	gx, gy = make([]float32, w*h), make([]float32, w*h)
	for y := range h {
		for x := range w {
			xl, xr := max(x-1, 0), min(x+1, w-1)
			yu, yd := max(y-1, 0), min(y+1, h-1)
			gx[y*w+x] = (p[y*w+xr] - p[y*w+xl]) / 2
			gy[y*w+x] = (p[yd*w+x] - p[yu*w+x]) / 2
		}
	}
	return gx, gy
}

// boxF is the sum over a (2r+1)^2 window, clamped at the edges.
func boxF(p []float32, w, h, r int) []float32 {
	tmp := make([]float32, w*h)
	parRows(h, func(y int) {
		var s float32
		for x := -r; x <= r; x++ {
			s += p[y*w+min(max(x, 0), w-1)]
		}
		for x := range w {
			tmp[y*w+x] = s
			s += p[y*w+min(x+r+1, w-1)] - p[y*w+max(x-r, 0)]
		}
	})
	out := make([]float32, w*h)
	parRows(w, func(x int) {
		var s float32
		for y := -r; y <= r; y++ {
			s += tmp[min(max(y, 0), h-1)*w+x]
		}
		for y := range h {
			out[y*w+x] = s
			s += tmp[min(y+r+1, h-1)*w+x] - tmp[max(y-r, 0)*w+x]
		}
	})
	return out
}

func sample(p []float32, w, h int, x, y float32) float32 {
	x0, y0 := int(x), int(y)
	x1, y1 := min(x0+1, w-1), min(y0+1, h-1)
	fx, fy := x-float32(x0), y-float32(y0)
	a := p[y0*w+x0]*(1-fx) + p[y0*w+x1]*fx
	b := p[y1*w+x0]*(1-fx) + p[y1*w+x1]*fx
	return a*(1-fy) + b*fy
}

func parRows(n int, f func(int)) {
	var wg sync.WaitGroup
	k := runtime.NumCPU()
	for t := range k {
		wg.Go(func() {
			for i := t; i < n; i += k {
				f(i)
			}
		})
	}
	wg.Wait()
}
