package vmaf

// SSIM as libvmaf's float_ssim feature computes it (ssim.c and its IQA
// library, Copyright Netflix and Tom Distler, BSD+Patent and BSD: see
// LICENSE.libvmaf): the luma as floats on the 8-bit scale, shrunk by a
// whole factor near the smaller side over 256 (a box filter, every
// factor-th sample), then the local means, variances and covariance under
// an 11-tap Gaussian (where it fits in the picture), the product of the
// luminance, contrast and structure terms averaged. The float and double
// steps are libvmaf's, in its order.

import "math"

var ssimGaussian = [11]float32{0.001028, 0.007599, 0.036001, 0.109361, 0.213006, 0.266012, 0.213006, 0.109361, 0.036001, 0.007599, 0.001028}

// iqaRound is IQA's rounding.
func iqaRound(a float32) int {
	sign := -1
	if a > 0 {
		sign = 1
	}
	if a-float32(int(a)) >= 0.5 {
		return int(a) + sign
	}
	return int(a)
}

// SSIM is the SSIM index of a distorted picture against its reference.
func SSIM(ref, dis Frame) float64 {
	w, h := ref.W, ref.H
	div := float32(int(1) << max(ref.BPC-8, 0))
	r, d := make([]float32, w*h), make([]float32, w*h)
	for i := range r {
		r[i], d[i] = float32(ref.Y[i])/div, float32(dis.Y[i])/div
	}
	scale := max(1, iqaRound(float32(min(w, h))/256.0))
	if scale > 1 {
		r, _, _ = iqaDecimate(r, w, h, scale)
		d, w, h = iqaDecimate(d, w, h, scale)
	}
	return float64(iqaSSIM(r, d, w, h))
}

// iqaDecimate low-passes a picture by a factor x factor box (its edges
// mirrored) and keeps every factor-th sample.
func iqaDecimate(img []float32, w, h, factor int) ([]float32, int, int) {
	sw, sh := w/factor+(w&1), h/factor+(h&1)
	k := float32(1) / float32(factor*factor)
	uc, vc := factor/2, factor/2
	even := 1 - factor&1
	at := func(x, y int) float32 {
		if x < 0 {
			x = -1 - x
		} else if x >= w {
			x = w - (x - w) - 1
		}
		if y < 0 {
			y = -1 - y
		} else if y >= h {
			y = h - (y - h) - 1
		}
		return img[y*w+x]
	}
	out := make([]float32, sw*sh)
	for y := range sh {
		for x := range sw {
			cx, cy := x*factor, y*factor
			edge := cx < uc || cy < vc || cx >= w-uc || cy >= h-vc
			var sum float64
			for v := -vc; v <= vc-even; v++ {
				for u := -uc; u <= uc-even; u++ {
					var p float32
					if edge {
						p = at(cx+u, cy+v)
					} else {
						p = img[(cy+v)*w+cx+u]
					}
					sum += float64(float32(p * k))
				}
			}
			out[y*sw+x] = float32(sum * 1.0)
		}
	}
	return out, sw, sh
}

// iqaConvolve applies the Gaussian both ways where it fits, giving a
// (w-10) x (h-10) picture.
func iqaConvolve(img []float32, w, h int) []float32 {
	const n = len(ssimGaussian)
	c := n / 2
	dw, dh := w-n+1, h-n+1
	cache := make([]float32, w*h)
	for y := -c; y < dh+c; y++ {
		for x := range dw {
			var sum float64
			o := (y+c)*w + x + c
			for k := range n {
				sum += float64(float32(img[o+k-c] * ssimGaussian[k]))
			}
			cache[o] = float32(sum * 1.0)
		}
	}
	out := make([]float32, dw*dh)
	for x := range dw {
		for y := range dh {
			var sum float64
			o := (y+c)*w + x + c
			for k := range n {
				sum += float64(float32(cache[o+(k-c)*w] * ssimGaussian[k]))
			}
			out[y*dw+x] = float32(sum * 1.0)
		}
	}
	return out
}

func iqaSSIM(ref, cmp []float32, w, h int) float32 {
	const k1, k2 = float32(0.01), float32(0.03)
	c1 := (k1 * 255) * (k1 * 255)
	c2 := (k2 * 255) * (k2 * 255)
	c3 := c2 / 2.0
	refMu, cmpMu := iqaConvolve(ref, w, h), iqaConvolve(cmp, w, h)
	rr, cc, rc := make([]float32, w*h), make([]float32, w*h), make([]float32, w*h)
	for i := range ref {
		rr[i], cc[i], rc[i] = ref[i]*ref[i], cmp[i]*cmp[i], ref[i]*cmp[i]
	}
	refSq, cmpSq, both := iqaConvolve(rr, w, h), iqaConvolve(cc, w, h), iqaConvolve(rc, w, h)
	n := len(refMu)
	for i := range n {
		refSq[i] -= float32(refMu[i] * refMu[i]) // the products rounded, as libvmaf (no fused multiply-adds)
		cmpSq[i] -= float32(cmpMu[i] * cmpMu[i])
		refSq[i] = float32(math.Max(0, float64(refSq[i])))
		cmpSq[i] = float32(math.Max(0, float64(cmpSq[i])))
		both[i] -= float32(refMu[i] * cmpMu[i])
	}
	var sum float64
	for i := range n {
		sigma := float32(math.Sqrt(float64(refSq[i] * cmpSq[i])))
		l := (float64(2.0*float64(refMu[i])*float64(cmpMu[i])) + float64(c1)) / float64(float32(refMu[i]*refMu[i])+float32(cmpMu[i]*cmpMu[i])+c1)
		c := (float64(2.0*float64(sigma)) + float64(c2)) / float64(refSq[i]+cmpSq[i]+c2)
		b := both[i]
		if b < 0 && sigma <= 0 {
			b = 0
		}
		s := float64((b + c3) / (sigma + c3))
		sum += float64(l * c * s)
	}
	return float32(sum / float64(n))
}
