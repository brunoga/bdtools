package mpeg2

import "math"

// The inverse DCT, to the precision of the standard's reference (IEEE
// 1180): separable, in double precision, rounded once at the end.

var idctCos = func() (c [8][8]float64) {
	for x := range 8 {
		for u := range 8 {
			s := 1.0
			if u == 0 {
				s = 1 / math.Sqrt2
			}
			c[x][u] = s * math.Cos(float64((2*x+1)*u)*math.Pi/16) / 2
		}
	}
	return
}()

// idct turns 8x8 coefficients (raster order) into samples, clamped to
// [-256, 255].
func idct(in *[64]int32, out *[64]int32) {
	var tmp [64]float64
	for v := range 8 { // rows of coefficients: across
		row := in[v*8 : v*8+8]
		nonzero := false
		for _, c := range row {
			if c != 0 {
				nonzero = true
				break
			}
		}
		if !nonzero {
			continue
		}
		for x := range 8 {
			var s float64
			for u := range 8 {
				s += idctCos[x][u] * float64(row[u])
			}
			tmp[v*8+x] = s
		}
	}
	for x := range 8 { // down
		for y := range 8 {
			var s float64
			for v := range 8 {
				s += idctCos[y][v] * tmp[v*8+x]
			}
			r := int32(math.Floor(s + 0.5))
			out[y*8+x] = min(max(r, -256), 255)
		}
	}
}
