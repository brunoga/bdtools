package av1

import (
	"math/bits"
	"sync/atomic"
)

// Local warped motion (motion_mode LOCALWARP): an affine model fitted, by
// least squares, to the motion of the block's neighbours that use its
// reference (the find warp samples process, 7.10.4, and the warp
// estimation process, 7.11.3.8), and the block predicted through it in
// 8x8 pieces (the block warp process, 7.11.3.5), each mirroring the spec.

// dbgNoWarp turns warped motion (enable_warped_motion) off;
// dbgForceWarp codes every block it can warped (for tests).
var dbgNoWarp, dbgForceWarp = false, false

// warpedBlocks counts the blocks coded warped (for tests).
var warpedBlocks atomic.Int64

// warpCands is how many of the single reference candidates coded in
// full are also coded warped.
var warpCands = 2

// warpSamples is find_warp_samples' CandList and NumSamples.
type warpSamples struct {
	n, scanned int
	cand       [LEAST_SQUARES_SAMPLES_MAX][4]int
}

// findWarpSamples is the find warp samples process for the block as the
// state has it (its reference and first motion vector).
func (t *tileEnc) findWarpSamples() warpSamples {
	f, b := t.f, &t.b
	var ws warpSamples
	w4, h4 := b.bw4, b.bh4
	doTopLeft, doTopRight := true, true
	if b.availU {
		srcW := int(num4x4BlocksWide[f.miSize[f.mi(b.r-1, b.c)]])
		if w4 <= srcW {
			colOffset := -(b.c & (srcW - 1))
			if colOffset < 0 {
				doTopLeft = false
			}
			if colOffset+srcW > w4 {
				doTopRight = false
			}
			t.addWarpSample(&ws, -1, 0)
		} else {
			for i, step := 0, 0; i < min(w4, f.miCols-b.c); i += step {
				srcW = int(num4x4BlocksWide[f.miSize[f.mi(b.r-1, b.c+i)]])
				step = min(w4, srcW)
				t.addWarpSample(&ws, -1, i)
			}
		}
	}
	if b.availL {
		srcH := int(num4x4BlocksHigh[f.miSize[f.mi(b.r, b.c-1)]])
		if h4 <= srcH {
			if rowOffset := -(b.r & (srcH - 1)); rowOffset < 0 {
				doTopLeft = false
			}
			t.addWarpSample(&ws, 0, -1)
		} else {
			for i, step := 0, 0; i < min(h4, f.miRows-b.r); i += step {
				srcH = int(num4x4BlocksHigh[f.miSize[f.mi(b.r+i, b.c-1)]])
				step = min(h4, srcH)
				t.addWarpSample(&ws, i, -1)
			}
		}
	}
	if doTopLeft {
		t.addWarpSample(&ws, -1, -1)
	}
	if doTopRight && max(w4, h4) <= 16 {
		t.addWarpSample(&ws, -1, w4)
	}
	if ws.n == 0 && ws.scanned > 0 {
		ws.n = 1
	}
	return ws
}

// addWarpSample is the add sample process.
func (t *tileEnc) addWarpSample(ws *warpSamples, deltaRow, deltaCol int) {
	f, b := t.f, &t.b
	if ws.scanned >= LEAST_SQUARES_SAMPLES_MAX {
		return
	}
	mvRow, mvCol := b.r+deltaRow, b.c+deltaCol
	if !t.inside(mvRow, mvCol) || !t.miWritten(mvRow, mvCol) {
		return
	}
	i := f.mi(mvRow, mvCol)
	if int(f.refFrames[0][i]) != b.refFrame || int(f.refFrames[1][i]) != NONE {
		return
	}
	candSz := f.miSize[i]
	candW4, candH4 := int(num4x4BlocksWide[candSz]), int(num4x4BlocksHigh[candSz])
	candRow, candCol := mvRow&^(candH4-1), mvCol&^(candW4-1)
	midY, midX := candRow*4+candH4*2-1, candCol*4+candW4*2-1
	threshold := min(max(int(max(blockWidth[b.size], blockHeight[b.size])), 16), 112)
	m := f.mvs[0][f.mi(candRow, candCol)]
	valid := abs(int(m.row-b.mv.row))+abs(int(m.col-b.mv.col)) <= threshold
	ws.scanned++
	if !valid && ws.scanned > 1 {
		return
	}
	ws.cand[ws.n] = [4]int{midY * 8, midX * 8, midY*8 + int(m.row), midX*8 + int(m.col)}
	if valid {
		ws.n++
	}
}

// warpEstimation is the warp estimation process: LocalWarpParams for the
// block from its samples, and LocalValid.
func (t *tileEnc) warpEstimation(ws *warpSamples) ([6]int32, bool) {
	b := &t.b
	var a [2][2]int64
	var bx, by [2]int64
	midY, midX := b.r*4+b.bh4*2-1, b.c*4+b.bw4*2-1
	suy, sux := midY*8, midX*8
	duy, dux := suy+int(b.mv.row), sux+int(b.mv.col)
	ls := func(p, q int) int64 { return int64((p*q)>>2) + int64(p+q) }
	for i := range ws.n {
		c := ws.cand[i]
		sy, sx, dy, dx := c[0]-suy, c[1]-sux, c[2]-duy, c[3]-dux
		if abs(sx-dx) < LS_MV_MAX && abs(sy-dy) < LS_MV_MAX {
			a[0][0] += ls(sx, sx) + 8
			a[0][1] += ls(sx, sy) + 4
			a[1][1] += ls(sy, sy) + 8
			bx[0] += ls(sx, dx) + 8
			bx[1] += ls(sy, dx) + 4
			by[0] += ls(sx, dy) + 4
			by[1] += ls(sy, dy) + 8
		}
	}
	det := a[0][0]*a[1][1] - a[0][1]*a[0][1]
	if det == 0 {
		return [6]int32{}, false
	}
	divShift, divFactor := resolveDivisor(det)
	divShift -= WARPEDMODEL_PREC_BITS
	if divShift < 0 {
		divFactor <<= uint(-divShift)
		divShift = 0
	}
	div := func(v int64) int64 { return round2Signed128(v, divFactor, divShift) }
	nondiag := func(v int64) int32 {
		return int32(min(max(div(v), -WARPEDMODEL_NONDIAGAFFINE_CLAMP+1), WARPEDMODEL_NONDIAGAFFINE_CLAMP-1)) //nolint:gosec // clipped
	}
	diag := func(v int64) int32 {
		return int32(min(max(div(v), (1<<WARPEDMODEL_PREC_BITS)-WARPEDMODEL_NONDIAGAFFINE_CLAMP+1), (1<<WARPEDMODEL_PREC_BITS)+WARPEDMODEL_NONDIAGAFFINE_CLAMP-1)) //nolint:gosec // clipped
	}
	var p [6]int32
	p[2] = diag(a[1][1]*bx[0] - a[0][1]*bx[1])
	p[3] = nondiag(-a[0][1]*bx[0] + a[0][0]*bx[1])
	p[4] = nondiag(a[1][1]*by[0] - a[0][1]*by[1])
	p[5] = diag(-a[0][1]*by[0] + a[0][0]*by[1])
	vx := int64(b.mv.col)*(1<<(WARPEDMODEL_PREC_BITS-3)) - (int64(midX)*(int64(p[2])-(1<<WARPEDMODEL_PREC_BITS)) + int64(midY)*int64(p[3]))
	vy := int64(b.mv.row)*(1<<(WARPEDMODEL_PREC_BITS-3)) - (int64(midX)*int64(p[4]) + int64(midY)*(int64(p[5])-(1<<WARPEDMODEL_PREC_BITS)))
	p[0] = int32(min(max(vx, -WARPEDMODEL_TRANS_CLAMP), WARPEDMODEL_TRANS_CLAMP-1)) //nolint:gosec // clipped
	p[1] = int32(min(max(vy, -WARPEDMODEL_TRANS_CLAMP), WARPEDMODEL_TRANS_CLAMP-1)) //nolint:gosec // clipped
	return p, true
}

// round2Signed128 is Round2Signed(v*f, n), the product kept exact (it can
// overflow 64 bits).
func round2Signed128(v, f int64, n int) int64 {
	neg := (v < 0) != (f < 0)
	hi, lo := bits.Mul64(uabs64(v), uabs64(f))
	if n > 0 {
		var carry uint64
		lo, carry = bits.Add64(lo, 1<<uint(n-1), 0)
		hi += carry
		lo = lo>>uint(n) | hi<<uint(64-n)
	}
	r := int64(lo) //nolint:gosec // the quotient fits
	if neg {
		return -r
	}
	return r
}

func uabs64(v int64) uint64 {
	if v < 0 {
		return uint64(-v)
	}
	return uint64(v)
}

// resolveDivisor is the resolve divisor process: divShift and divFactor
// with d*divFactor >> divShift about 1.
func resolveDivisor(d int64) (int, int64) {
	ad := uabs64(d)
	n := 63 - bits.LeadingZeros64(ad)
	e := ad - 1<<uint(n)
	var f uint64
	if n > DIV_LUT_BITS {
		f = (e + 1<<uint(n-DIV_LUT_BITS-1)) >> uint(n-DIV_LUT_BITS)
	} else {
		f = e << uint(DIV_LUT_BITS-n)
	}
	factor := int64(divLut[f])
	if d < 0 {
		factor = -factor
	}
	return n + DIV_LUT_PREC_BITS, factor
}

// shear is the setup shear process's outputs.
type shear struct {
	valid                     bool
	alpha, beta, gamma, delta int32
}

// setupShear is the setup shear process.
func setupShear(p *[6]int32) shear {
	clip16 := func(v int64) int64 { return min(max(v, -32768), 32767) }
	alpha0 := clip16(int64(p[2]) - 1<<WARPEDMODEL_PREC_BITS)
	beta0 := clip16(int64(p[3]))
	divShift, divFactor := resolveDivisor(int64(p[2]))
	v := int64(p[4]) << WARPEDMODEL_PREC_BITS
	gamma0 := clip16(round2Signed128(v, divFactor, divShift))
	w := int64(p[3]) * int64(p[4])
	delta0 := clip16(int64(p[5]) - round2Signed128(w, divFactor, divShift) - 1<<WARPEDMODEL_PREC_BITS)
	reduce := func(v int64) int32 {
		return int32(round2Signed64(v, WARP_PARAM_REDUCE_BITS) << WARP_PARAM_REDUCE_BITS) //nolint:gosec // 16 bits
	}
	s := shear{alpha: reduce(alpha0), beta: reduce(beta0), gamma: reduce(gamma0), delta: reduce(delta0)}
	s.valid = 4*abs32(s.alpha)+7*abs32(s.beta) < 1<<WARPEDMODEL_PREC_BITS &&
		4*abs32(s.gamma)+4*abs32(s.delta) < 1<<WARPEDMODEL_PREC_BITS
	return s
}

// blockWarp is the block warp process for the whole w by h block of a
// plane at (x, y) (multiples of 8), from ref through p: the prediction
// into pred (stride w), at InterRound1's precision.
func blockWarp(ref *refFrameState, plane, x, y, w, h int, p *[6]int32, rnd interRounding, pred []int32) {
	sub := uint(min(plane, 1))
	lastX := ((ref.width + int(sub)) >> sub) - 1
	lastY := ((ref.height + int(sub)) >> sub) - 1
	src, stride := ref.rec[plane], ref.stride[plane]
	sh := setupShear(p)
	var mid [15][8]int32
	r0, r1 := uint(rnd.round0), uint(rnd.round1)
	for i8 := 0; i8 < h; i8 += 8 {
		for j8 := 0; j8 < w; j8 += 8 {
			srcX := int64(x+j8+4) << sub
			srcY := int64(y+i8+4) << sub
			dstX := int64(p[2])*srcX + int64(p[3])*srcY + int64(p[0])
			dstY := int64(p[4])*srcX + int64(p[5])*srcY + int64(p[1])
			x4, y4 := dstX>>sub, dstY>>sub
			ix4, sx4 := int(x4>>WARPEDMODEL_PREC_BITS), int32(x4&(1<<WARPEDMODEL_PREC_BITS-1))
			iy4, sy4 := int(y4>>WARPEDMODEL_PREC_BITS), int32(y4&(1<<WARPEDMODEL_PREC_BITS-1))
			for i1 := -7; i1 < 8; i1++ {
				row := src[min(max(iy4+i1, 0), lastY)*stride:]
				for i2 := -4; i2 < 4; i2++ {
					sx := sx4 + sh.alpha*int32(i2) + sh.beta*int32(i1)
					offs := (sx+1<<(WARPEDDIFF_PREC_BITS-1))>>WARPEDDIFF_PREC_BITS + WARPEDPIXEL_PREC_SHIFTS
					fl := &warpedFilters[offs]
					var s int32
					for i3 := range 8 {
						s += int32(fl[i3]) * int32(row[min(max(ix4+i2-3+i3, 0), lastX)])
					}
					mid[i1+7][i2+4] = (s + 1<<(r0-1)) >> r0
				}
			}
			for i1 := -4; i1 < min(4, h-i8-4); i1++ {
				for i2 := -4; i2 < min(4, w-j8-4); i2++ {
					sy := sy4 + sh.gamma*int32(i2) + sh.delta*int32(i1)
					offs := (sy+1<<(WARPEDDIFF_PREC_BITS-1))>>WARPEDDIFF_PREC_BITS + WARPEDPIXEL_PREC_SHIFTS
					fl := &warpedFilters[offs]
					var s int32
					for i3 := range 8 {
						s += int32(fl[i3]) * mid[i1+i3+4][i2+4]
					}
					pred[(i8+i1+4)*w+j8+i2+4] = (s + 1<<(r1-1)) >> r1
				}
			}
		}
	}
}
