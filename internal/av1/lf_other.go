//go:build !amd64 || purego

package av1

// filterSeg filters the 4 lines of an edge segment at pos (lines across
// apart, the samples across the edge step apart).
func (lf *loopFilter) filterSeg(buf []uint16, pos, step, across, plane, size int, limit, blimit, thresh int32) {
	for i := range 4 {
		lf.sample(buf, pos+i*across, step, plane, size, limit, blimit, thresh)
	}
}
