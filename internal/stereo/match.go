package stereo

import (
	"math"
	"math/bits"
	"sync"

	"github.com/brunoga/bdtools/mvc"
)

// Disparity is a disparity map measured between a stereo pair's eyes, at
// half their resolution: D[y*W+x], in the eyes' (full resolution) pixels,
// positive nearer than the screen (the left eye's view of a point that
// far right of the right eye's), where Valid says the match is sure.
type Disparity struct {
	W, H  int
	D     []float32
	Valid []bool
}

// Matching's parameters: the search (half resolution pixels each way),
// the census window's radius, semi-global matching's penalties for a step
// of one and for a larger one, and the uniqueness the best match needs
// (its cost at most this share of the next best's, away from it).
const (
	matchRange      = 64
	censusRadius    = 2
	censusMax       = (2*censusRadius+1)*(2*censusRadius+1) - 1
	matchP1         = 4
	matchP2         = 36
	matchUniqueness = 0.95
	// matchTexture is the least deviation of the luma (half resolution,
	// over the census window) a match is trusted on: below it, as in
	// night skies and space, the match follows the grain, which two
	// cameras do not share.
	matchTexture = 2.5
)

// Match measures the disparity between a stereo pair's eyes (8-bit
// 4:2:0, the same size): their luma halved, a census transform's costs
// aggregated along eight paths (semi-global matching), the best match to
// a fraction of a pixel; a match is valid when it is unique and the right
// eye's match of the pixel it lands on comes back to it.
func Match(left, right *mvc.Frame) *Disparity {
	l, _ := MatchBoth(left, right)
	return l
}

// MatchBoth is Match and the right eye's disparity too, from the same
// matching: its pixel x matching the left eye's x+D (the sign Match's
// has), valid on the same terms.
func MatchBoth(left, right *mvc.Frame) (l, r *Disparity) {
	w, h := left.Width/2, left.Height/2
	lh, rh := half(left), half(right)
	lc := census(lh, w, h)
	rc := census(rh, w, h)
	const nd = 2*matchRange + 1
	cost := make([]uint8, w*h*nd)
	rows(h, func(y int) {
		for x := range w {
			c := cost[(y*w+x)*nd:][:nd]
			l := lc[y*w+x]
			for i := range nd {
				xr := x - (i - matchRange)
				if xr < 0 || xr >= w {
					c[i] = censusMax
					continue
				}
				c[i] = uint8(bits.OnesCount32(l ^ rc[y*w+xr])) //nolint:gosec // at most 24
			}
		}
	})
	sum := aggregate(cost, w, h, nd)

	// each eye's best match: the left eye's pixel x at disparity i is the
	// sums' (x, i); the right eye's x the sums' (x+d, i), d its disparity
	pick := func(at func(x, y, i int) (uint16, bool)) (d []float32, best []int, unique []bool) {
		d, best, unique = make([]float32, w*h), make([]int, w*h), make([]bool, w*h)
		rows(h, func(y int) {
			var s [nd]uint16
			for x := range w {
				for i := range nd {
					v, ok := at(x, y, i)
					if !ok {
						v = math.MaxUint16
					}
					s[i] = v
				}
				b := 0
				for i := 1; i < nd; i++ {
					if s[i] < s[b] {
						b = i
					}
				}
				second := uint16(math.MaxUint16)
				for i := range nd {
					if (i < b-1 || i > b+1) && s[i] < second {
						second = s[i]
					}
				}
				sub := float64(b)
				if b > 0 && b < nd-1 && s[b-1] != math.MaxUint16 && s[b+1] != math.MaxUint16 { //nolint:gosec // b within 1..nd-2 here
					a, m, c := float64(s[b-1]), float64(s[b]), float64(s[b+1]) //nolint:gosec // as above
					if den := a - 2*m + c; den > 0 {
						sub += (a - c) / (2 * den)
					}
				}
				best[y*w+x] = b - matchRange
				d[y*w+x] = float32(2 * (sub - matchRange))
				unique[y*w+x] = s[b] != math.MaxUint16 && float64(s[b]) <= matchUniqueness*float64(second)
			}
		})
		return d, best, unique
	}
	dl, il, ul := pick(func(x, y, i int) (uint16, bool) { return sum[(y*w+x)*nd+i], true })
	dr, ir, ur := pick(func(x, y, i int) (uint16, bool) {
		xl := x + (i - matchRange)
		if xl < 0 || xl >= w {
			return 0, false
		}
		return sum[(y*w+xl)*nd+i], true
	})
	// valid: unique, textured, and the other eye's match of the pixel it
	// lands on coming back to it
	valid := func(best []int, unique []bool, other []int, p []byte, dir int) []bool {
		v := make([]bool, w*h)
		for y := censusRadius; y < h-censusRadius; y++ {
			for x := censusRadius; x < w-censusRadius; x++ {
				i := y*w + x
				xo := x + dir*best[i]
				if !unique[i] || xo < 0 || xo >= w || !textured(p, w, x, y) {
					continue
				}
				if d := best[i] - other[y*w+xo]; d >= -1 && d <= 1 {
					v[i] = true
				}
			}
		}
		return v
	}
	return &Disparity{W: w, H: h, D: dl, Valid: valid(il, ul, ir, lh, -1)},
		&Disparity{W: w, H: h, D: dr, Valid: valid(ir, ur, il, rh, 1)}
}

// textured reports whether the luma about (x, y), over the census
// window, deviates by matchTexture or more.
func textured(p []byte, w, x, y int) bool {
	var s, ss, n float64
	for dy := -censusRadius; dy <= censusRadius; dy++ {
		for dx := -censusRadius; dx <= censusRadius; dx++ {
			v := float64(p[(y+dy)*w+x+dx])
			s += v
			ss += v * v
			n++
		}
	}
	return ss/n-(s/n)*(s/n) >= matchTexture*matchTexture
}

// half is a frame's luma at half its size (each 2x2 averaged).
func half(f *mvc.Frame) []byte {
	w, h := f.Width/2, f.Height/2
	out := make([]byte, w*h)
	rows(h, func(y int) {
		a := f.Y[2*y*f.StrideY:]
		b := f.Y[(2*y+1)*f.StrideY:]
		for x := range w {
			out[y*w+x] = byte((int(a[2*x]) + int(a[2*x+1]) + int(b[2*x]) + int(b[2*x+1]) + 2) / 4)
		}
	})
	return out
}

// census is each pixel's census: a bit for each other pixel of the window
// about it, set when that one is darker (the window clamped at the edges).
func census(p []byte, w, h int) []uint32 {
	out := make([]uint32, w*h)
	rows(h, func(y int) {
		for x := range w {
			c := p[y*w+x]
			var v uint32
			for dy := -censusRadius; dy <= censusRadius; dy++ {
				yy := min(max(y+dy, 0), h-1)
				for dx := -censusRadius; dx <= censusRadius; dx++ {
					if dx == 0 && dy == 0 {
						continue
					}
					xx := min(max(x+dx, 0), w-1)
					v <<= 1
					if p[yy*w+xx] < c {
						v |= 1
					}
				}
			}
			out[y*w+x] = v
		}
	})
	return out
}

// aggregate sums the costs along eight paths (semi-global matching): along
// each, a pixel's cost at a disparity plus the least of the path's cost
// before it at the same disparity, at one away plus P1, at any plus P2.
func aggregate(cost []uint8, w, h, nd int) []uint16 {
	sum := make([]uint16, len(cost))
	// along the rows, both ways
	rows(h, func(y int) {
		prev := make([]uint16, nd)
		cur := make([]uint16, nd)
		for _, dir := range [2]int{1, -1} {
			x0, x1 := 0, w
			if dir < 0 {
				x0, x1 = w-1, -1
			}
			first := true
			for x := x0; x != x1; x += dir {
				i := (y*w + x) * nd
				step(cost[i:i+nd], prev, cur, first)
				first = false
				for d, v := range cur {
					sum[i+d] += v
				}
				prev, cur = cur, prev
			}
		}
	})
	// down and up the picture: straight, and the two diagonals, row after
	// row with the row's pixels in parallel
	for _, dir := range [2]int{1, -1} {
		y0, y1 := 0, h
		if dir < 0 {
			y0, y1 = h-1, -1
		}
		var prev, cur [3][]uint16
		for k := range 3 {
			prev[k], cur[k] = make([]uint16, w*nd), make([]uint16, w*nd)
		}
		for y := y0; y != y1; y += dir {
			first := y == y0
			chunks(w, func(xa, xb int) {
				for x := xa; x < xb; x++ {
					i := (y*w + x) * nd
					c := cost[i : i+nd]
					for k, dx := range [3]int{-1, 0, 1} {
						px := x + dx
						out := cur[k][x*nd : (x+1)*nd]
						if first || px < 0 || px >= w {
							step(c, nil, out, true)
						} else {
							step(c, prev[k][px*nd:(px+1)*nd], out, false)
						}
						for d, v := range out {
							sum[i+d] += v
						}
					}
				}
			})
			prev, cur = cur, prev
		}
	}
	return sum
}

// step is one pixel of a path: out from its costs c and the path's
// previous pixel's prev (none at the path's start).
func step(c []uint8, prev, out []uint16, first bool) {
	if first {
		for d, v := range c {
			out[d] = uint16(v)
		}
		return
	}
	m := prev[0]
	for _, v := range prev[1:] {
		m = min(m, v)
	}
	nd := len(c)
	for d := range nd {
		b := min(prev[d], m+matchP2)
		if d > 0 {
			b = min(b, prev[d-1]+matchP1)
		}
		if d < nd-1 {
			b = min(b, prev[d+1]+matchP1)
		}
		out[d] = uint16(c[d]) + b - m
	}
}

// chunks runs f over [0, n) split into sixteen ranges, in parallel.
func chunks(n int, f func(a, b int)) {
	const parts = 16
	var wg sync.WaitGroup
	for k := range parts {
		a, b := k*n/parts, (k+1)*n/parts
		if a < b {
			wg.Go(func() { f(a, b) })
		}
	}
	wg.Wait()
}
