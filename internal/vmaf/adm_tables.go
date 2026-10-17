// Constant tables for the ADM port (adm.go). The CSF weights come from
// libvmaf's integer_adm.c and integer_adm.h (Copyright Netflix,
// BSD+Patent, see LICENSE.libvmaf); the powf tables from the GNU C
// Library (see powf below).

package vmaf

import "math"

// admCSF holds the contrast sensitivity weights of one wavelet scale
// under libvmaf's default options (Watson's 1997 noise visibility model,
// viewing distance 3 picture heights, 1080-line reference display). A
// weight is the reciprocal of the quantisation step at which wavelet
// noise becomes visible; the horizontal and vertical bands share one,
// the diagonal band has its own.
//
// libvmaf derives them at run time with log10/pow; the values here are
// what that code yields on glibc, so the port does not depend on Go's
// math library rounding the same way:
//   - cubeHV, cubeD: pow(weight, 3), the weight cubed in double, used to
//     scale the reference's energy (the denominator);
//   - fixHV, fixD: the weight times 2^32 truncated to uint32, used to
//     weight the bands of scales 1-3 in fixed point. Scale 0 uses its own
//     16-bit constants (admCSF0Fix).
type admCSF struct {
	cubeHV, cubeD float64
	fixHV, fixD   uint32
}

var admCSFs = [4]admCSF{
	{0x1.60682e40ca298p-18, 0x1.b6f67955c18e8p-23, 74653136, 25300308},
	{0x1.127cb41f44aabp-15, 0x1.88675620541cbp-19, 137373792, 61414028},
	{0x1.5638de3f6e459p-14, 0x1.e740af5557bdcp-17, 186284160, 104783920},
	{0x1.8f9f6fa0b6d35p-14, 0x1.018b80fefe987p-15, 196165808, 134487184},
}

// admCSF0Fix is the scale 0 weight in fixed point for the h, v and d
// bands: Q21 for h and v, Q23 for d. libvmaf hard-codes these for its
// default viewing conditions.
var admCSF0Fix = [3]int32{36453, 36453, 49417}

// admCos1DegSq is cos(1 degree)^2 rounded to float, the threshold of the
// decoupling's angle test.
const admCos1DegSq float32 = 0x1.ffd814p-1

// admDivLookup[32768+i] is 2^30/i for i in [-32768, 32768] (i != 0):
// the decoupling divides by a reference coefficient by multiplying with
// this reciprocal.
var admDivLookup = func() *[65537]int32 {
	var t [65537]int32
	for i := 1; i <= 32768; i++ {
		recip := int32(1073741824 / i)
		t[32768+i] = recip
		t[32768-i] = -recip
	}
	return &t
}()

// powf tables, from glibc's sysdeps/ieee754/flt-32/e_powf_log2_data.c and
// e_exp2f_data.c (Copyright Free Software Foundation, LGPL 2.1+; written
// by Szabolcs Nagy, ARM Ltd., the same code as ARM's optimized-routines
// under the MIT license).
var powfLog2Tab = [16]struct{ invc, logc float64 }{
	{0x1.661ec79f8f3bep+0, -0x1.efec65b963019p-2},
	{0x1.571ed4aaf883dp+0, -0x1.b0b6832d4fca4p-2},
	{0x1.49539f0f010bp+0, -0x1.7418b0a1fb77bp-2},
	{0x1.3c995b0b80385p+0, -0x1.39de91a6dcf7bp-2},
	{0x1.30d190c8864a5p+0, -0x1.01d9bf3f2b631p-2},
	{0x1.25e227b0b8eap+0, -0x1.97c1d1b3b7afp-3},
	{0x1.1bb4a4a1a343fp+0, -0x1.2f9e393af3c9fp-3},
	{0x1.12358f08ae5bap+0, -0x1.960cbbf788d5cp-4},
	{0x1.0953f419900a7p+0, -0x1.a6f9db6475fcep-5},
	{0x1p+0, 0x0p+0},
	{0x1.e608cfd9a47acp-1, 0x1.338ca9f24f53dp-4},
	{0x1.ca4b31f026aap-1, 0x1.476a9543891bap-3},
	{0x1.b2036576afce6p-1, 0x1.e840b4ac4e4d2p-3},
	{0x1.9c2d163a1aa2dp-1, 0x1.40645f0c6651cp-2},
	{0x1.886e6037841edp-1, 0x1.88e9c2c1b9ff8p-2},
	{0x1.767dcf5534862p-1, 0x1.ce0a44eb17bccp-2},
}

var powfLog2Poly = [5]float64{
	0x1.27616c9496e0bp-2, -0x1.71969a075c67ap-2, 0x1.ec70a6ca7baddp-2,
	-0x1.7154748bef6c8p-1, 0x1.71547652ab82bp0,
}

// powfExp2Tab[i] is the bits of 2^(i/32) minus i<<47.
var powfExp2Tab = [32]uint64{
	0x3ff0000000000000, 0x3fefd9b0d3158574, 0x3fefb5586cf9890f, 0x3fef9301d0125b51,
	0x3fef72b83c7d517b, 0x3fef54873168b9aa, 0x3fef387a6e756238, 0x3fef1e9df51fdee1,
	0x3fef06fe0a31b715, 0x3feef1a7373aa9cb, 0x3feedea64c123422, 0x3feece086061892d,
	0x3feebfdad5362a27, 0x3feeb42b569d4f82, 0x3feeab07dd485429, 0x3feea47eb03a5585,
	0x3feea09e667f3bcd, 0x3fee9f75e8ec5f74, 0x3feea11473eb0187, 0x3feea589994cce13,
	0x3feeace5422aa0db, 0x3feeb737b0cdc5e5, 0x3feec49182a3f090, 0x3feed503b23e255d,
	0x3feee89f995ad3ad, 0x3feeff76f2fb5e47, 0x3fef199bdd85529c, 0x3fef3720dcef9069,
	0x3fef5818dcfba487, 0x3fef7c97337b9b5f, 0x3fefa4afa2a490da, 0x3fefd0765b6e4540,
}

var powfExp2Poly = [3]float64{0x1.c6af84b912394p-5, 0x1.ebfce50fac4f3p-3, 0x1.62e42ff0c52d6p-1}

// powf is glibc's powf for a finite, non-negative x and a positive y
// (ADM only ever takes cube roots of energies). libvmaf rounds through
// powf at every scale, and glibc's result is not always the correctly
// rounded one that float32(math.Pow(x, y)) gives (about 6 in 10000 cube
// roots differ in the last bit), so the port follows glibc's algorithm:
// log2(x) from a 16-entry table and a degree-5 polynomial, times y, then
// 2^t from a 32-entry table and a cubic, all in double.
func powf(x, y float32) float32 {
	if x == 0 || math.IsInf(float64(x), 1) || x != x {
		return x
	}
	ix := math.Float32bits(x)
	if ix < 0x00800000 {
		// Subnormal: normalise so the exponent goes negative.
		ix = math.Float32bits(x*0x1p23) - 23<<23
	}

	// log2(x) = k + log2(c) + log1p(z/c-1)/ln2, with x = 2^k z.
	tmp := ix - 0x3f330000
	e := powfLog2Tab[(tmp>>19)%16]
	top := tmp & 0xff800000
	z := float64(math.Float32frombits(ix - top))
	k := int32(top) >> 23
	r := float64(z*e.invc) - 1
	y0 := e.logc + float64(k)
	r2 := r * r
	a := &powfLog2Poly
	q := float64(a[4]*r) + y0
	p := float64(a[2]*r) + a[3]
	q = float64(p*r2) + q
	l := float64(a[0]*r) + a[1]
	l = float64(l*float64(r2*r2)) + q

	// 2^t = 2^(n/32) * 2^rr, with t = y*log2(x) = n/32 + rr.
	t := float64(y) * l
	const shift = 0x1.8p+52 / 32
	kd := t + shift
	ki := math.Float64bits(kd)
	kd -= shift
	rr := t - kd
	s := math.Float64frombits(powfExp2Tab[ki%32] + ki<<47)
	c := &powfExp2Poly
	zz := float64(c[0]*rr) + c[1]
	v := float64(c[2]*rr) + 1
	v = float64(zz*float64(rr*rr)) + v
	return float32(v * s)
}
