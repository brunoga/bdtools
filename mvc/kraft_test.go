package mvc

import "testing"

// TestVLCKraft checks the CAVLC tables form complete prefix codes.
func TestVLCKraft(t *testing.T) {
	check := func(name string, lens []uint8, codes []uint8, complete bool) {
		sum := 0.0
		for i, l := range lens {
			if l == 0 {
				continue
			}
			sum += 1.0 / float64(uint(1)<<l)
			for j, l2 := range lens {
				if j == i || l2 == 0 || l2 < l {
					continue
				}
				if uint32(codes[j])>>(l2-l) == uint32(codes[i]) && (l2 != l || j >= i) {
					t.Errorf("%s: code %d is a prefix of %d", name, i, j)
				}
			}
		}
		if complete && sum != 1 {
			t.Errorf("%s: kraft sum %v", name, sum)
		}
	}
	for i := 0; i < 3; i++ {
		check("coeff_token", coeffTokenLen[i][:], coeffTokenBits[i][:], false)
	}
	check("chromaDC", chromaDCTokenLen[:], chromaDCTokenBits[:], false)
	for i := 0; i < 15; i++ {
		check("total_zeros", totalZerosLen[i][:], totalZerosBits[i][:], i != 0)
	}
	for i := 0; i < 3; i++ {
		check("cdc total_zeros", chromaDCTotalZerosLen[i][:], chromaDCTotalZerosBits[i][:], true)
	}
	for i := 0; i < 7; i++ {
		check("run_before", runBeforeLen[i][:], runBeforeBits[i][:], i != 6)
	}
}
