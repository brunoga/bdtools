package av1

// Block differences: the sum of absolute differences, and of the absolute
// 8x8 Hadamard transformed differences (halved twice: about twice the
// SAD, as the 4x4 transforms' halved sum is), with AVX2 kernels on amd64
// (the HEVC encoder's).

func sadBlockGo(a []uint16, as int, b []uint16, bs, w, h int) int {
	sum := 0
	for y := range h {
		ra, rb := a[y*as:y*as+w], b[y*bs:y*bs+w]
		for x := range ra {
			d := int(ra[x]) - int(rb[x])
			if d < 0 {
				d = -d
			}
			sum += d
		}
	}
	return sum
}

func satd8Go(a []uint16, as int, b []uint16, bs int) int {
	var m [64]int32
	for y := range 8 {
		ar, br := a[y*as:y*as+8], b[y*bs:y*bs+8]
		var d [8]int32
		for i := range d {
			d[i] = int32(ar[i]) - int32(br[i])
		}
		s0, s1, s2, s3 := d[0]+d[1], d[0]-d[1], d[2]+d[3], d[2]-d[3]
		s4, s5, s6, s7 := d[4]+d[5], d[4]-d[5], d[6]+d[7], d[6]-d[7]
		t0, t1, t2, t3 := s0+s2, s1+s3, s0-s2, s1-s3
		t4, t5, t6, t7 := s4+s6, s5+s7, s4-s6, s5-s7
		r := m[y*8 : y*8+8]
		r[0], r[1], r[2], r[3] = t0+t4, t1+t5, t2+t6, t3+t7
		r[4], r[5], r[6], r[7] = t0-t4, t1-t5, t2-t6, t3-t7
	}
	sum := int32(0)
	for x := range 8 {
		d0, d1, d2, d3 := m[x], m[8+x], m[16+x], m[24+x]
		d4, d5, d6, d7 := m[32+x], m[40+x], m[48+x], m[56+x]
		s0, s1, s2, s3 := d0+d1, d0-d1, d2+d3, d2-d3
		s4, s5, s6, s7 := d4+d5, d4-d5, d6+d7, d6-d7
		t0, t1, t2, t3 := s0+s2, s1+s3, s0-s2, s1-s3
		t4, t5, t6, t7 := s4+s6, s5+s7, s4-s6, s5-s7
		sum += abs32(t0+t4) + abs32(t1+t5) + abs32(t2+t6) + abs32(t3+t7)
		sum += abs32(t0-t4) + abs32(t1-t5) + abs32(t2-t6) + abs32(t3-t7)
	}
	return int((sum + 2) >> 2)
}

// satd8s is the SATD of a block of whole 8x8s (16x8s where it can).
func satd8s(a []uint16, as int, b []uint16, bs, w, h int) int64 {
	var sum int
	for y := 0; y < h; y += 8 {
		x := 0
		for ; x+16 <= w; x += 16 {
			sum += satd16x8(a[y*as+x:], as, b[y*bs+x:], bs)
		}
		for ; x < w; x += 8 {
			sum += satd8(a[y*as+x:], as, b[y*bs+x:], bs)
		}
	}
	return int64(sum)
}
