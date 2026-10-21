package nn

import "math"

// expf is e^x in float32: x = n ln2 + r (|r| <= ln2/2), e^r by its Taylor
// series to the 7th power (a relative error under 2e-7), times 2^n.
func expf(x float32) float32 {
	switch {
	case x > 88.7:
		return float32(math.Inf(1))
	case x < -87.3:
		return 0
	}
	const ln2hi, ln2lo = 0.693145751953125, 1.428606765330187e-06
	n := float32(math.Floor(float64(x*1.4426950408889634 + 0.5)))
	r := x - n*ln2hi - n*ln2lo
	p := 1 + r*(1+r*(1.0/2+r*(1.0/6+r*(1.0/24+r*(1.0/120+r*(1.0/720+r*(1.0/5040)))))))
	return p * math.Float32frombits(uint32(int32(n)+127)<<23) //nolint:gosec // n within the exponent's range
}

// erff is erf(x) in float32: Abramowitz and Stegun's 7.1.26 (an absolute
// error under 1.5e-7).
func erff(x float32) float32 {
	sign := float32(1)
	if x < 0 {
		sign, x = -1, -x
	}
	t := 1 / (1 + 0.3275911*x)
	y := 1 - (((((1.061405429*t-1.453152027)*t)+1.421413741)*t-0.284496736)*t+0.254829592)*t*expf(-x*x)
	return sign * y
}
