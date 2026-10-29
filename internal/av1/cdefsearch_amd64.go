//go:build amd64 && !purego

package av1

// cdefParams is cdefSearch8AVX2's block: its taps' offsets (bytes), the
// strengths, and room for its rows' work and its sums.
type cdefParams struct {
	offs  [20]int32 // the taps' offsets: cdefSearchBlock's pd, sd and s0, in order
	_     [12]int32
	priT  [16]int32
	priSh [16]int32
	tap0  [16]int32
	tap1  [16]int32
	secT  [4]int32
	secSh [4]int32
	d     [20][8]int32
	p     [16][8]int32
	s     [4][8]int32 // [0] stays 0
	s0    [4][8]int32 // [0] stays 0
	acc   [64][8]int32
}

//go:noescape
func cdefSearch8AVX2(win *int32, src *uint16, stride int, p *cdefParams)

// cdefSearch8 is cdefSearchBlock of a whole 8x8 luma block with AVX2 (its
// window 12 wide): false when it is not.
func cdefSearch8(win []int32, w, vw, vh int, src []uint16, stride, dir int,
	priT *[16]int32, priShift *[16]uint, priTaps *[16][2]int32, secT *[4]int32, secShift *[4]uint, e *[64]int64, p *cdefParams) bool {
	if !useAVX2 || w != 8 || vw != 8 || vh != 8 {
		return false
	}
	const ww = 12
	off := cdefOffsets(ww)
	d0, d1 := (dir+2)&7, (dir+6)&7
	for k := range 2 {
		o := [5]int{off[dir][k], off[d0][k], off[d1][k], off[2][k], off[6][k]}
		p.offs[k*2], p.offs[k*2+1] = int32(4*o[0]), int32(-4*o[0])
		p.offs[4+k*4], p.offs[5+k*4], p.offs[6+k*4], p.offs[7+k*4] = int32(4*o[1]), int32(-4*o[1]), int32(4*o[2]), int32(-4*o[2])
		p.offs[12+k*4], p.offs[13+k*4], p.offs[14+k*4], p.offs[15+k*4] = int32(4*o[3]), int32(-4*o[3]), int32(4*o[4]), int32(-4*o[4])
	}
	for pi := 1; pi < 16; pi++ {
		p.priT[pi], p.priSh[pi] = priT[pi], int32(priShift[pi]) //nolint:gosec // small
		p.tap0[pi], p.tap1[pi] = priTaps[pi][0], priTaps[pi][1]
	}
	for si := 1; si < 4; si++ {
		p.secT[si], p.secSh[si] = secT[si], int32(secShift[si]) //nolint:gosec // small
	}
	clear(p.acc[:])
	_ = win[9*ww+9]
	_ = src[7*stride+7]
	cdefSearch8AVX2(&win[2*ww+2], &src[0], stride, p)
	for k := range e {
		var s int64
		for _, v := range p.acc[k] {
			s += int64(v)
		}
		e[k] += s
	}
	return true
}
