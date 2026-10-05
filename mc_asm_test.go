//go:build amd64 && !purego

package mvc

import (
	"bytes"
	"math/rand"
	"testing"
)

func randPicture(rng *rand.Rand, mbW, mbH int, extreme bool) *picture {
	p := allocNewPicture(mbW, mbH)
	for c := 0; c < 3; c++ {
		for i := range p.planes[c] {
			if extreme {
				p.planes[c][i] = byte(rng.Intn(2) * 255)
			} else {
				p.planes[c][i] = byte(rng.Intn(256))
			}
		}
	}
	return p
}

func TestSIMDLumaMC(t *testing.T) {
	if !useAVX2Asm {
		t.Skip("no AVX2")
	}
	rng := rand.New(rand.NewSource(1))
	for iter := 0; iter < 4000; iter++ {
		ref := randPicture(rng, 4, 3, iter%2 == 0)
		sizes := [][2]int{{16, 16}, {16, 8}, {8, 16}, {8, 8}, {8, 4}, {4, 8}, {4, 4}}
		sz := sizes[rng.Intn(len(sizes))]
		w, h := sz[0], sz[1]
		qx := rng.Intn((ref.width+80)*4) - 160
		qy := rng.Intn((ref.height+80)*4) - 160
		var m1, m2 mcBuf
		var d1, d2 [256]byte
		lumaMC(&m1, d1[:], 16, ref, qx, qy, w, h)
		lumaMCRef(&m2, d2[:], ref, qx, qy, w, h)
		for y := 0; y < h; y++ {
			if !bytes.Equal(d1[y*16:y*16+w], d2[y*16:y*16+w]) {
				t.Fatalf("luma mismatch w=%d h=%d qx=%d qy=%d frac=%d,%d row %d\n%v\n%v", w, h, qx, qy, qx&3, qy&3, y, d1[y*16:y*16+w], d2[y*16:y*16+w])
			}
		}
		var c1, c2 [64]byte
		cx := rng.Intn((ref.width/2+40)*8) - 80
		cy := rng.Intn((ref.height/2+40)*8) - 80
		chromaMC(c1[:], 8, ref, 1, cx, cy, w/2, h/2)
		chromaMCGeneric(c2[:], 8, ref, 1, cx, cy, w/2, h/2)
		for y := 0; y < h/2; y++ {
			if !bytes.Equal(c1[y*8:y*8+w/2], c2[y*8:y*8+w/2]) {
				t.Fatalf("chroma mismatch w=%d h=%d", w/2, h/2)
			}
		}
		// both planes at once, with full-sample positions often
		if iter%3 == 0 {
			cx &^= 7
		}
		if iter%4 == 0 {
			cy &^= 7
		}
		var b1, b2, r1, r2 [64]byte
		chromaMC2(b1[:], r1[:], 8, ref, cx, cy, w/2, h/2)
		chromaMC2Generic(b2[:], r2[:], 8, ref, cx, cy, w/2, h/2)
		for y := 0; y < h/2; y++ {
			if !bytes.Equal(b1[y*8:y*8+w/2], b2[y*8:y*8+w/2]) || !bytes.Equal(r1[y*8:y*8+w/2], r2[y*8:y*8+w/2]) {
				t.Fatalf("chroma2 mismatch w=%d h=%d frac=%d,%d", w/2, h/2, cx&7, cy&7)
			}
		}
	}
}

// lumaMCRef is lumaMC using only generic kernels.
func lumaMCRef(m *mcBuf, dst []byte, ref *picture, qx, qy, w, h int) {
	ix, iy := qx>>2, qy>>2
	fx, fy := qx&3, qy&3
	if ix < -w-3 {
		ix = -w - 3
	} else if ix > ref.width+2 {
		ix = ref.width + 2
	}
	if iy < -h-3 {
		iy = -h - 3
	} else if iy > ref.height+2 {
		iy = ref.height + 2
	}
	src := ref.planes[0]
	ss := ref.stride[0]
	so := ref.origin[0] + iy*ss + ix
	switch fx | fy<<2 {
	case 0:
		for y := 0; y < h; y++ {
			copy(dst[y*16:y*16+w], src[so+y*ss:so+y*ss+w])
		}
	case 2:
		hpelHGeneric(dst, 16, src, so, ss, w, h)
	case 1, 3:
		hpelHGeneric(m.tA[:], 16, src, so, ss, w, h)
		avgIntoGeneric(dst, 16, m.tA[:], 16, src[so+fx>>1:], ss, w, h)
	case 8:
		hpelVGeneric(dst, 16, src, so, ss, w, h)
	case 4, 12:
		hpelVGeneric(m.tA[:], 16, src, so, ss, w, h)
		avgIntoGeneric(dst, 16, m.tA[:], 16, src[so+(fy>>1)*ss:], ss, w, h)
	case 10:
		hpelJGeneric(dst, 16, m.tJ[:], src, so, ss, w, h)
	case 6, 14:
		hpelJGeneric(m.tA[:], 16, m.tJ[:], src, so, ss, w, h)
		hpelHGeneric(m.tB[:], 16, src, so+(fy>>1)*ss, ss, w, h)
		avgIntoGeneric(dst, 16, m.tA[:], 16, m.tB[:], 16, w, h)
	case 9, 11:
		hpelJGeneric(m.tA[:], 16, m.tJ[:], src, so, ss, w, h)
		hpelVGeneric(m.tB[:], 16, src, so+(fx>>1), ss, w, h)
		avgIntoGeneric(dst, 16, m.tA[:], 16, m.tB[:], 16, w, h)
	default:
		hpelHGeneric(m.tA[:], 16, src, so+(fy>>1)*ss, ss, w, h)
		hpelVGeneric(m.tB[:], 16, src, so+(fx>>1), ss, w, h)
		avgIntoGeneric(dst, 16, m.tA[:], 16, m.tB[:], 16, w, h)
	}
}

func TestSIMDWeighted(t *testing.T) {
	if !useAVX2Asm {
		t.Skip("no AVX2")
	}
	rng := rand.New(rand.NewSource(2))
	for iter := 0; iter < 20000; iter++ {
		var a, b [256]byte
		for i := range a {
			a[i] = byte(rng.Intn(256))
			b[i] = byte(rng.Intn(256))
			if iter%3 == 0 {
				a[i] = byte(rng.Intn(2) * 255)
				b[i] = byte(rng.Intn(2) * 255)
			}
		}
		w := []int{16, 8}[rng.Intn(2)]
		h := []int{16, 8, 4}[rng.Intn(3)]
		logWD := uint(rng.Intn(8))
		w0 := int32(rng.Intn(256) - 128)
		w1 := int32(rng.Intn(256) - 128)
		o0 := int32(rng.Intn(256) - 128)
		o1 := int32(rng.Intn(256) - 128)
		if iter%5 == 0 { // implicit-style weights
			logWD = 5
			w1 = int32(rng.Intn(193) - 64)
			w0 = 64 - w1
			o0, o1 = 0, 0
		}
		var d1, d2 [16 * 20]byte
		as := w // exact-size sources catch over-reads
		sa, sb := a[:as*h], b[:as*h]
		storeWeighted1(d1[:], 20, sa, as, w, h, w0, o0, logWD)
		storeWeighted1Generic(d2[:], 20, sa, as, w, h, w0, o0, logWD)
		if d1 != d2 {
			t.Fatalf("weighted1 mismatch w=%d h=%d wt=%d of=%d log=%d", w, h, w0, o0, logWD)
		}
		storeWeighted2(d1[:], 20, sa, sb, as, w, h, w0, w1, o0, o1, logWD)
		storeWeighted2Generic(d2[:], 20, sa, sb, as, w, h, w0, w1, o0, o1, logWD)
		if d1 != d2 {
			t.Fatalf("weighted2 mismatch w=%d h=%d w=%d,%d o=%d,%d log=%d", w, h, w0, w1, o0, o1, logWD)
		}
		storeAvg(d1[:], 20, sa, sb, as, w, h)
		storeAvgGeneric(d2[:], 20, sa, sb, as, w, h)
		if d1 != d2 {
			t.Fatalf("avg mismatch")
		}
	}
}
