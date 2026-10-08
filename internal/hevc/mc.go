package hevc

// The motion compensation kernels, on whole blocks: the interpolation
// filters (8.5.3.3.3) and the weighted sample prediction (8.5.3.3.4). Each
// has its Go version here, which the assembly ones (amd64) are tested
// against and fall back to.

// tapPairs are a filter's coefficients in pairs, each packed as two int16
// in a uint32 (the first in the low half), as the kernels multiply them.
type tapPairs [4]uint32

var lumaPairs, chromaPairs = func() (l [4]tapPairs, c [8]tapPairs) {
	pack := func(a, b int) uint32 { return uint32(uint16(int16(a))) | uint32(uint16(int16(b)))<<16 }
	for f := range lumaFilter {
		for k := range 4 {
			l[f][k] = pack(lumaFilter[f][2*k], lumaFilter[f][2*k+1])
		}
	}
	for f := range chromaFilter {
		for k := range 2 {
			c[f][k] = pack(chromaFilter[f][2*k], chromaFilter[f][2*k+1])
		}
	}
	return l, c
}()

// tap is coefficient i of the pairs.
func (t *tapPairs) tap(i int) int {
	return int(int16(t[i/2] >> (16 * (i % 2))))
}

// hFilterGo filters the w x h block at src (rows sstride apart)
// horizontally with the taps (2*pairs of them) into dst (rows dstride
// apart): dst[x] = sum c[k] * src[x+k] >> shift.
func hFilterGo(dst []int16, dstride, w, h int, src []uint16, sstride int, c *tapPairs, pairs int, shift uint) {
	taps := 2 * pairs
	for y := range h {
		row := src[y*sstride : y*sstride+w+taps-1]
		d := dst[y*dstride : y*dstride+w]
		for x := range d {
			var sum int
			for k := range taps {
				sum += c.tap(k) * int(row[x+k])
			}
			d[x] = int16(sum >> shift)
		}
	}
}

// vFilterGo filters vertically: dst[x] = sum c[k] * src[x+k*sstride] >>
// shift, from samples (uint16) or from a first pass (int16).
func vFilterGo[T uint16 | int16](dst []int16, dstride, w, h int, src []T, sstride int, c *tapPairs, pairs int, shift uint) {
	taps := 2 * pairs
	for y := range h {
		d := dst[y*dstride : y*dstride+w]
		for x := range d {
			var sum int
			for k := range taps {
				sum += c.tap(k) * int(src[(y+k)*sstride+x])
			}
			d[x] = int16(sum >> shift)
		}
	}
}

// copyBlockGo takes a block at a whole-sample position: src << shift.
func copyBlockGo(dst []int16, dstride, w, h int, src []uint16, sstride int, shift uint) {
	for y := range h {
		row := src[y*sstride : y*sstride+w]
		d := dst[y*dstride : y*dstride+w]
		for x, v := range row {
			d[x] = int16(v) << shift
		}
	}
}

// putBlockGo writes the prediction of a w x h block from its predictions
// a and b (rows abstride apart):
// clip(((a*w0 + b*w1 + add) >> shift) + o, 0, maxV). It is every case
// of 8.5.3.3.4: the default and the weighted, of one list and of two.
func putBlockGo(dst []uint16, dstride int, a, b []int16, abstride, w, h int, w0, w1, add int, shift uint, o, maxV int) {
	for y := range h {
		row := dst[y*dstride : y*dstride+w]
		ra, rb := a[y*abstride:y*abstride+w], b[y*abstride:y*abstride+w]
		for x := range row {
			v := ((int(ra[x])*w0+int(rb[x])*w1+add)>>shift + o)
			row[x] = uint16(min(max(v, 0), maxV))
		}
	}
}
