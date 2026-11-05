package av1

// Scene cuts: a mini-GOP ends on the last picture before one (its anchor
// of the scene it ends, the next one's of the scene that starts), and the
// temporal filter's windows end at one.

var (
	// cutK is how many times the picture before's (and the one before
	// that's) difference a cut's is, at least; cutMin the least
	// difference of a cut (the mean absolute luma difference, 8-bit).
	cutK   = 3.0
	cutMin = 12.0
	// cutStep is the grid the difference is measured on.
	cutStep = 4
)

// sceneDiff is the mean absolute difference of two pictures' luma, on
// cutStep's grid, in 8-bit units.
func sceneDiff(a, b *Source, w, h, bitDepth int) float64 {
	var sum, n int
	for y := 0; y < h; y += cutStep {
		ra, rb := a.Y[y*a.StrideY:], b.Y[y*b.StrideY:]
		for x := 0; x < w; x += cutStep {
			d := int(ra[x]) - int(rb[x])
			sum += max(d, -d)
			n++
		}
	}
	return float64(sum) / float64(n) / float64(int(1)<<uint(bitDepth-8))
}

// isCut is whether the picture disp starts a scene.
func (e *Encoder) isCut(disp int) bool {
	d, ok := e.diffs[disp]
	if !ok || cutK <= 0 || d < cutMin {
		return false
	}
	return d > cutK*e.diffs[disp-1] && d > cutK*e.diffs[disp-2]
}

// nextCut is the first picture after disp, to disp+n at most, that
// starts a scene, or -1.
func (e *Encoder) nextCut(disp, n int) int {
	for d := disp + 1; d <= disp+n; d++ {
		if e.isCut(d) {
			return d
		}
	}
	return -1
}
