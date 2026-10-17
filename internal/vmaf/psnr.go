package vmaf

import "math"

// PSNR is the luma's peak signal-to-noise ratio, in dB, as libvmaf's psnr
// feature gives it (psnr_y): the peak the depth's largest value, at most
// 6 dB a bit plus 12 (what identical pictures get).
func PSNR(ref, dis Frame) float64 {
	var sse uint64
	for i, v := range ref.Y {
		e := int64(v) - int64(dis.Y[i])
		sse += uint64(e * e) //nolint:gosec // a square
	}
	peak := float64(int(1)<<ref.BPC - 1)
	mse := float64(sse) / float64(ref.W*ref.H)
	return min(10*math.Log10(peak*peak/max(mse, 1e-16)), float64(6*ref.BPC+12))
}
