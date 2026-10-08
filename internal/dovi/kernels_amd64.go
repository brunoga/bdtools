package dovi

func cpuidAsm(leaf, sub uint32) (eax, ebx, ecx, edx uint32)
func xgetbvAsm() (eax, edx uint32)

// useAVX2 reports whether the AVX2 and FMA kernel can be used: the CPU has
// them and the OS saves the YMM state.
var useAVX2 = func() bool {
	if top, _, _, _ := cpuidAsm(0, 0); top < 7 {
		return false
	}
	_, _, ecx, _ := cpuidAsm(1, 0)
	if ecx&(1<<12) == 0 || ecx&(1<<27) == 0 || ecx&(1<<28) == 0 { // FMA, OSXSAVE, AVX
		return false
	}
	if eax, _ := xgetbvAsm(); eax&6 != 6 {
		return false
	}
	_, ebx, _, _ := cpuidAsm(7, 0)
	return ebx&(1<<5) != 0 // AVX2
}()

// mmrTable is mmrRow laid out for the kernel: every value repeated across
// the eight lanes of a vector.
type mmrTable struct {
	coef     [2][21][8]float32 // Cb's, then Cr's
	constant [2][8]float32
	lo, hi   [2][8]float32
}

// mmrRowAVX2 predicts n (a multiple of 8) chroma samples.
//
//go:noescape
func mmrRowAVX2(t *mmrTable, ob, or, sy, sb, sr *float32, n int)

// row predicts a row of chroma samples from their luma, Cb and Cr.
func (k *mmrRow) row(ob, or, sy, sb, sr []float32) {
	n := len(ob) &^ 7
	if !useAVX2 || n == 0 || k.table == nil {
		k.rowGo(ob, or, sy, sb, sr)
		return
	}
	mmrRowAVX2(k.table, &ob[0], &or[0], &sy[0], &sb[0], &sr[0], n)
	if n < len(ob) {
		k.rowGo(ob[n:], or[n:], sy[n:], sb[n:], sr[n:])
	}
}

// prepare lays the coefficients out for the kernel, before rows are
// shared out.
func (k *mmrRow) prepare() {
	t := &mmrTable{}
	for c := range 2 {
		for i := range 21 {
			for l := range 8 {
				t.coef[c][i][l] = k.coef[c][i]
			}
		}
		for l := range 8 {
			t.constant[c][l], t.lo[c][l], t.hi[c][l] = k.constant[c], k.lo[c], k.hi[c]
		}
	}
	k.table = t
}

//go:noescape
func lumaRowAVX2(out, bl *byte, el *int32, lut, res *float32, hasEL, n int)

//go:noescape
func chromaPrepAVX2(sy, sb, sr *float32, y0, y1, uv *byte, n int)

//go:noescape
func chromaStoreAVX2(out *byte, ob, or *float32, eb, er *int32, resB, resR *float32, hasEL, n int)

//go:noescape
func verticalYAVX2(out *int32, r0, r1, r2, r3 *byte, w *[4]int32, n int)

//go:noescape
func verticalUVAVX2(ob, or *int32, r0, r1, r2, r3 *byte, w *[4]int32, n int)

//go:noescape
func horizontalAVX2(out, in *int32, n int)

// The kernels take whole vectors; the Go versions do what is left.

func lumaRow(out, bl []byte, el []int32, lut, res *[1024]float32) {
	n := len(out) / 2 &^ 15
	if !useAVX2 || n == 0 {
		lumaRowGo(out, bl, el, lut, res)
		return
	}
	_ = bl[2*n-1]
	var e *int32
	hasEL := 0
	if el != nil {
		_ = el[n-1]
		e, hasEL = &el[0], 1
	}
	lumaRowAVX2(&out[0], &bl[0], e, &lut[0], &res[0], hasEL, n)
	if n < len(out)/2 {
		var et []int32
		if el != nil {
			et = el[n:]
		}
		lumaRowGo(out[2*n:], bl[2*n:], et, lut, res)
	}
}

func chromaPrep(sy, sb, sr []float32, y0, y1, uv []byte) {
	// The first sample's luma has no left neighbour: Go does it.
	m := (len(sy) - 1) &^ 7
	if !useAVX2 || m == 0 {
		chromaPrepGo(sy, sb, sr, y0, y1, uv)
		return
	}
	_, _, _ = y0[4*(m+1)-1], y1[4*(m+1)-1], uv[4*(m+1)-1]
	chromaPrepGo(sy[:1], sb[:1], sr[:1], y0, y1, uv)
	chromaPrepAVX2(&sy[1], &sb[1], &sr[1], &y0[4], &y1[4], &uv[4], m)
	if rest := 1 + m; rest < len(sy) {
		tail(sy, sb, sr, y0, y1, uv, rest)
	}
}

// tail does chroma samples from x on in Go, with their left neighbours.
func tail(sy, sb, sr []float32, y0, y1, uv []byte, x int) {
	for ; x < len(sy); x++ {
		ys := sample(y0, 2*x-1) + 2*sample(y0, 2*x) + sample(y0, 2*x+1) +
			sample(y1, 2*x-1) + 2*sample(y1, 2*x) + sample(y1, 2*x+1)
		sy[x] = float32(ys) * (1.0 / (8 * 1023))
		sb[x] = float32(sample(uv, 2*x)) * (1.0 / 1023)
		sr[x] = float32(sample(uv, 2*x+1)) * (1.0 / 1023)
	}
}

func chromaStore(out []byte, ob, or []float32, eb, er []int32, resB, resR *[1024]float32) {
	n := len(ob) &^ 7
	if !useAVX2 || n == 0 {
		chromaStoreGo(out, ob, or, eb, er, resB, resR)
		return
	}
	_, _, _ = out[4*n-1], ob[n-1], or[n-1]
	var b, r *int32
	hasEL := 0
	if eb != nil {
		_, _ = eb[n-1], er[n-1]
		b, r, hasEL = &eb[0], &er[0], 1
	}
	chromaStoreAVX2(&out[0], &ob[0], &or[0], b, r, &resB[0], &resR[0], hasEL, n)
	if n < len(ob) {
		var bt, rt []int32
		if eb != nil {
			bt, rt = eb[n:], er[n:]
		}
		chromaStoreGo(out[4*n:], ob[n:], or[n:], bt, rt, resB, resR)
	}
}

func verticalY(out []int32, r0, r1, r2, r3 []byte, w [4]int32) {
	n := len(out) &^ 7
	if !useAVX2 || n == 0 {
		verticalYGo(out, r0, r1, r2, r3, w)
		return
	}
	_, _, _, _ = r0[2*n-1], r1[2*n-1], r2[2*n-1], r3[2*n-1]
	verticalYAVX2(&out[0], &r0[0], &r1[0], &r2[0], &r3[0], &w, n)
	if n < len(out) {
		verticalYGo(out[n:], r0[2*n:], r1[2*n:], r2[2*n:], r3[2*n:], w)
	}
}

func verticalUV(ob, or []int32, r0, r1, r2, r3 []byte, w [4]int32) {
	n := len(ob) &^ 7
	if !useAVX2 || n == 0 {
		verticalUVGo(ob, or, r0, r1, r2, r3, w)
		return
	}
	_, _, _, _ = r0[4*n-1], r1[4*n-1], r2[4*n-1], r3[4*n-1]
	_ = or[n-1]
	verticalUVAVX2(&ob[0], &or[0], &r0[0], &r1[0], &r2[0], &r3[0], &w, n)
	if n < len(ob) {
		verticalUVGo(ob[n:], or[n:], r0[4*n:], r1[4*n:], r2[4*n:], r3[4*n:], w)
	}
}

func horizontal(out, in []int32) {
	// The kernel needs a neighbour on the left and two past each vector.
	m := (len(in) - 3) &^ 7
	if !useAVX2 || m <= 0 {
		horizontalGo(out, in)
		return
	}
	_ = out[2*len(in)-1]
	out[0] = code(in[0], 128)
	out[1] = code(9*(in[0]+in[1])-in[0]-in[2], 2048)
	horizontalAVX2(&out[2], &in[1], m)
	horizontalTail(out, in, 1+m)
}
