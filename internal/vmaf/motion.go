package vmaf

// Motion: how much the reference changes from one picture to the next,
// as libvmaf's integer extractor measures it (integer_motion.c, Copyright
// Netflix, BSD+Patent: see LICENSE.libvmaf): the mean absolute difference
// of the two pictures blurred by a 5-tap Gaussian, both ways. VMAF's
// motion2 of a picture is the smaller of its own and the next one's.

var motionFilter = [5]int32{3571, 16004, 26386, 16004, 3571}

// motionMax is libvmaf's default largest motion.
const motionMax = 10000.0

// motionSAD is the motion of cur from prev.
func motionSAD(prev, cur Frame) float64 {
	w, h, bpc := cur.W, cur.H, cur.BPC
	yRound := int64(1) << (bpc - 1)
	row := make([]int32, w)
	var sad uint64
	for i := range h {
		nonzero := int32(0)
		for j := range w {
			var acc int64
			for k, c := range motionFilter {
				r := mirror(i-2+k, h) * w
				acc += int64(c) * int64(int32(prev.Y[r+j])-int32(cur.Y[r+j]))
			}
			row[j] = int32((acc + yRound) >> uint(bpc)) //nolint:gosec // a depth
			nonzero |= row[j]
		}
		if nonzero == 0 {
			continue
		}
		var rowSAD uint32
		for j := range w {
			var acc int64
			for k, c := range motionFilter {
				acc += int64(c) * int64(row[mirror(j-2+k, w)])
			}
			v := int32((acc + 32768) >> 16)
			if v < 0 {
				v = -v
			}
			rowSAD += uint32(v) //nolint:gosec // positive
		}
		sad += uint64(rowSAD)
	}
	return min(float64(sad)/256./float64(w*h), motionMax)
}
