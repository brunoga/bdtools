//go:build amd64 && !purego

package av1

//go:generate go run ../../tools/av1lfasm -dir .

// lf_amd64.s is av1lfasm's (its kernel's arithmetic a lane a line, the
// wide filters' sums written out): TestLoopFilterKernel checks it against
// sample.

//go:noescape
func lfSeg4AVX2(buf *uint16, step, across, flen, size int, limit, blimit, thresh, bd int)

// filterSeg filters the 4 lines of an edge segment at pos (lines across
// apart, the samples across the edge step apart).
func (lf *loopFilter) filterSeg(buf []uint16, pos, step, across, plane, size int, limit, blimit, thresh int32) {
	if !useAVX2 {
		for i := range 4 {
			lf.sample(buf, pos+i*across, step, plane, size, limit, blimit, thresh)
		}
		return
	}
	flen, half := 16, 7 // the samples read each side
	switch {
	case size == 4:
		flen, half = 4, 2
	case plane != 0:
		flen, half = 6, 3
	case size == 8:
		flen, half = 8, 4
	}
	_, _ = buf[pos-half*step], buf[pos+3*across+(half-1)*step]
	lfSeg4AVX2(&buf[pos], step, across, flen, size, int(limit), int(blimit), int(thresh), lf.f.bitDepth-8)
}
