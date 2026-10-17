package vmaf

// Visual information fidelity (VIF), at four scales, as libvmaf's integer
// extractor computes it (integer_vif.c, Copyright Netflix, BSD+Patent:
// see LICENSE.libvmaf).
//
// At each scale the reference and the distorted picture are filtered by a
// Gaussian (17, 9, 5 and 3 taps); from the local means, variances and
// covariance comes how much of the reference's information the distorted
// picture keeps, against a noise floor, as sums of base-2 logarithms (in
// fixed point, by a table). The next scale is the filtered picture taken
// every other sample both ways. libvmaf's 8-bit path is its 16-bit one
// with 8 bits, so one path serves every depth.

import (
	"math"
	"math/bits"
)

var vifFilter = [4][]uint16{
	{489, 935, 1640, 2640, 3896, 5274, 6547, 7455, 7784, 7455, 6547, 5274, 3896, 2640, 1640, 935, 489},
	{1244, 3663, 7925, 12590, 14692, 12590, 7925, 3663, 1244},
	{3571, 16004, 26386, 16004, 3571},
	{10904, 43728, 10904},
}

// vifEnhnGainLimit is libvmaf's default enhancement gain limit.
const vifEnhnGainLimit = 100.0

// log2Table holds round(log2(i) * 2048) for i in [32767, 65535].
var log2Table = func() []uint16 {
	t := make([]uint16, 65537)
	for i := 32767; i < 65536; i++ {
		t[i] = uint16(math.Round(float64(float32(math.Log2(float64(float32(i)))) * 2048)))
	}
	return t
}()

func log2_32(v uint32) int32 {
	k := 16 - bits.LeadingZeros32(v)
	return int32(log2Table[v>>uint(k)]) + 2048*int32(k) //nolint:gosec // small
}

func log2_64(v uint64) int64 {
	k := 48 - bits.LeadingZeros64(v)
	return int64(log2Table[v>>uint(k)]) + 2048*int64(k) //nolint:gosec // small
}

// mirror reflects an index into [0, n) about its ends, the end sample not
// repeated.
func mirror(i, n int) int {
	if i < 0 {
		return -i
	}
	if i >= n {
		return 2*(n-1) - i
	}
	return i
}

// vifScores is each scale's VIF: its numerator over its denominator.
func vifScores(ref, dis Frame) [4]float64 {
	w, h, bpc := ref.W, ref.H, ref.BPC
	r, d := ref.Y, dis.Y
	var out [4]float64
	for scale := range 4 {
		if scale > 0 {
			r, d = vifSubsample(r, d, w, h, scale-1, bpc)
			w, h = w/2, h/2
		}
		num, den := vifStatistic(r, d, w, h, scale, bpc)
		out[scale] = float64(num / den)
	}
	return out
}

// vifSubsample filters the pictures for the next scale (by scale+1's
// filter) and takes every other sample.
func vifSubsample(ref, dis []uint16, w, h, scale, bpc int) ([]uint16, []uint16) {
	filt := vifFilter[scale+1]
	fw := len(filt)
	roundV, shiftV := uint32(32768), uint(16)
	if scale == 0 {
		roundV, shiftV = 1<<(bpc-1), uint(bpc) //nolint:gosec // a depth
	}
	cr, cd := make([]uint32, w), make([]uint32, w)
	mu1, mu2 := make([]uint16, w*h), make([]uint16, w*h)
	for i := range h {
		for j := range w {
			var ar, ad uint32
			for k, c := range filt {
				row := mirror(i-fw/2+k, h) * w
				ar += uint32(c) * uint32(ref[row+j])
				ad += uint32(c) * uint32(dis[row+j])
			}
			cr[j] = uint32(uint16((ar + roundV) >> shiftV))
			cd[j] = uint32(uint16((ad + roundV) >> shiftV))
		}
		for j := range w {
			var ar, ad uint32
			for k, c := range filt {
				col := mirror(j-fw/2+k, w)
				ar += uint32(c) * cr[col]
				ad += uint32(c) * cd[col]
			}
			mu1[i*w+j] = uint16((ar + 32768) >> 16)
			mu2[i*w+j] = uint16((ad + 32768) >> 16)
		}
	}
	nw, nh := w/2, h/2
	nr, nd := make([]uint16, nw*nh), make([]uint16, nw*nh)
	for i := range nh {
		for j := range nw {
			nr[i*nw+j] = mu1[2*i*w+2*j]
			nd[i*nw+j] = mu2[2*i*w+2*j]
		}
	}
	return nr, nd
}

// vifStatistic is a scale's numerator and denominator.
func vifStatistic(ref, dis []uint16, w, h, scale, bpc int) (float32, float32) {
	filt := vifFilter[scale]
	fw := len(filt)
	const sigmaNsq = 65536 << 1
	roundV, shiftV := uint32(32768), uint(16)
	roundVsq, shiftVsq := uint64(32768), uint(16)
	if scale == 0 {
		roundV, shiftV = 1<<(bpc-1), uint(bpc) //nolint:gosec // a depth
		shiftVsq = uint((bpc - 8) * 2)         //nolint:gosec // a depth
		roundVsq = 0
		if bpc > 8 {
			roundVsq = 1 << (shiftVsq - 1)
		}
	}
	tmu1, tmu2 := make([]uint32, w), make([]uint32, w)
	tref, tdis, tref2 := make([]uint32, w), make([]uint32, w), make([]uint32, w)
	var numLog, denLog, numNonLog, denNonLog int64
	for i := range h {
		for j := range w {
			var mu1, mu2 uint32
			var xx, yy, xy uint64
			for k, c := range filt {
				row := mirror(i-fw/2+k, h) * w
				a, b := ref[row+j], dis[row+j]
				ca, cb := uint32(c)*uint32(a), uint32(c)*uint32(b)
				mu1 += ca
				mu2 += cb
				xx += uint64(ca) * uint64(a)
				yy += uint64(cb) * uint64(b)
				xy += uint64(ca) * uint64(b)
			}
			tmu1[j] = uint32(uint16((mu1 + roundV) >> shiftV))
			tmu2[j] = uint32(uint16((mu2 + roundV) >> shiftV))
			tref[j] = uint32((xx + roundVsq) >> shiftVsq)
			tdis[j] = uint32((yy + roundVsq) >> shiftVsq)
			tref2[j] = uint32((xy + roundVsq) >> shiftVsq)
		}
		for j := range w {
			var mu1, mu2 uint32
			var xx, yy, xy uint64
			for k, c := range filt {
				col := mirror(j-fw/2+k, w)
				mu1 += uint32(c) * tmu1[col]
				mu2 += uint32(c) * tmu2[col]
				xx += uint64(c) * uint64(tref[col])
				yy += uint64(c) * uint64(tdis[col])
				xy += uint64(c) * uint64(tref2[col])
			}
			mu1Sq := uint32((uint64(mu1)*uint64(mu1) + 2147483648) >> 32)
			mu2Sq := uint32((uint64(mu2)*uint64(mu2) + 2147483648) >> 32)
			mu12 := uint32((uint64(mu1)*uint64(mu2) + 2147483648) >> 32)
			sigma1Sq := int32(uint32((xx+32768)>>16) - mu1Sq) //nolint:gosec // wraps as libvmaf's
			sigma2Sq := int32(uint32((yy+32768)>>16) - mu2Sq) //nolint:gosec // wraps as libvmaf's
			sigma12 := int32(uint32((xy+32768)>>16) - mu12)   //nolint:gosec // wraps as libvmaf's
			sigma2Sq = max(sigma2Sq, 0)
			if sigma1Sq >= sigmaNsq {
				denLog += int64(log2_32(uint32(sigmaNsq+sigma1Sq)) - 2048*17) //nolint:gosec // positive
				if sigma12 > 0 && sigma2Sq > 0 {
					const eps = 65536 * 1.0e-10
					g := float64(sigma12) / (float64(sigma1Sq) + eps)
					svSq := int32(float64(sigma2Sq) - float64(g*float64(sigma12))) // no fused multiply-add: libvmaf rounds the product
					svSq = max(svSq, 0)
					g = min(g, vifEnhnGainLimit)
					numer1 := uint32(svSq + sigmaNsq)                         //nolint:gosec // positive
					numer1Tmp := int64(g*g*float64(sigma1Sq)) + int64(numer1) //nolint:gosec // positive
					numLog += log2_64(uint64(numer1Tmp)) - log2_64(uint64(numer1))
				}
			} else {
				numNonLog += int64(sigma2Sq)
				denNonLog++
			}
		}
	}
	num := float32(float64(numLog)/2048.0 + (float64(denNonLog) - (float64(numNonLog)/16384.0)/65025.0))
	den := float32(float64(denLog)/2048.0 + float64(denNonLog))
	return num, den
}
