package mvc

import (
	"math"
	"math/bits"
)

// Trellis quantisation: the levels of a transform block chosen for the
// least squared error plus lambda times the CABAC bits, rather than by
// rounding each coefficient with a dead zone. A dynamic program walks the
// coefficients from the highest frequency down, its states the level
// coding's context (how many levels of one, and of more than one, are
// coded so far), plus one for "nothing coded yet"; each coefficient tries
// its rounded level and the one below.

// trellisW4 and trellisW8 are the squared error, in pixels, one level's
// step makes at each (raster) position of a 4x4 and an 8x8 transform, by
// quantiser: the decoder's scaling and inverse transform of a level.
// (initTrellis sets them once the scaling tables are.)
var (
	trellisW4 [52][16]float64
	trellisW8 [52][64]float64
)

func initTrellis() {
	w4, w8 := &trellisW4, &trellisW8
	const l = 1 << 10
	for qp := range 52 {
		for p := range 16 {
			var d [16]int32
			d[p] = encDequant4(l, scale4[qp%6][p], qp)
			encIDCT4(&d)
			s := 0.0
			for _, v := range d {
				s += float64(v) * float64(v)
			}
			w4[qp][p] = s / (64 * 64 * l * l)
		}
		for p := range 64 {
			var d [64]int32
			d[p] = encDequant8(l, scale8[qp%6][p], qp)
			encIDCT8(&d)
			s := 0.0
			for _, v := range d {
				s += float64(v) * float64(v)
			}
			w8[qp][p] = s / (64 * 64 * l * l)
		}
	}
}

// trellisCosts holds a block category's bin costs from the coder's
// context states.
type trellisCosts struct {
	sig, last [64][2]float64
	// lvl0[node][bin] is the first bin of a level's magnitude, lvlN[node]
	// the further ones, by trellis state
	lvl0, lvlN [8][2]float64
	trace      [64][9]trellisStep // trellisBlock's scratch
}

func binBits(s uint8, bin int) float64 {
	return cabacEntropy[s&^1|uint8(bin^int(s&1))]
}

func (t *trellisCosts) load(ctx *[512]uint8, cat, n int) {
	tab := &sigLastCtx[cat]
	for i := range n - 1 {
		for b := range 2 {
			t.sig[i][b] = binBits(ctx[tab[i][0]], b)
			t.last[i][b] = binBits(ctx[tab[i][1]], b)
		}
	}
	absB := absBaseTab[cat]
	gt1Max := 4
	if cat == catChromaDC {
		gt1Max = 3
	}
	for node := range 8 {
		c0, gt1 := absB+min(1+node, 4), 0
		if node >= 4 {
			c0, gt1 = absB, node-3
		}
		cN := absB + 5 + min(gt1, gt1Max)
		for b := range 2 {
			t.lvl0[node][b] = binBits(ctx[c0], b)
			t.lvlN[node][b] = binBits(ctx[cN], b)
		}
	}
}

// levelBits is the bits of a level of magnitude a (sign included) coded
// in state node.
func (t *trellisCosts) levelBits(node int, a int32) float64 {
	if a == 1 {
		return t.lvl0[node][0] + 1
	}
	bits := t.lvl0[node][1] + 1
	am1 := int(a - 1)
	n := min(am1, 14)
	bits += float64(n-1) * t.lvlN[node][1]
	if am1 < 14 {
		bits += t.lvlN[node][0]
	} else {
		// the Exp-Golomb (k=0) suffix of am1-14
		v := am1 - 14 + 1
		k := 0
		for v>>(k+1) != 0 {
			k++
		}
		bits += float64(2*k + 1)
	}
	return bits
}

// trellisNext is the state after coding a level of magnitude a.
func trellisNext(node int, a int32) int {
	if a == 1 {
		if node < 3 {
			return node + 1
		}
		return node
	}
	if node < 4 {
		return 4
	}
	return min(node+1, 7)
}

const trellisNone = 8 // nothing coded yet

// trellisStep is a trellis state's way in: the state before it and the
// level coded.
type trellisStep struct {
	src int8
	l   int32
}

// trellisBlock chooses the n levels (zigzag order, into lv) of the
// transform coefficients coef (raster), zig mapping one to the other,
// quantised with mf and qbits, w the error per level step by position;
// cbf0 and cbf1 are the bits of the block's coded_block_flag (0 when it
// has none). It reports whether a level is left.
func trellisBlock(t *trellisCosts, coef []int32, zig []uint8, mf []int32, qbits uint, w []float64, lambda2, cbf0, cbf1 float64, lv []int32) bool {
	n := len(lv)
	trace := &t.trace
	var xs [64]float64
	var rs [64]int32
	scale := 1 / float64(int64(1)<<qbits)
	top := -1
	for i := range n {
		p := zig[i]
		v := coef[p]
		if v < 0 {
			v = -v
		}
		x := float64(v) * float64(mf[p]) * scale
		xs[i], rs[i] = x, int32(x+0.5)
		if rs[i] != 0 {
			top = i
		}
		lv[i] = 0
	}
	if top < 0 {
		return false
	}
	inf := math.Inf(1)
	var cost [9]float64
	for i := range cost {
		cost[i] = inf
	}
	// the coefficients past the last that can round to a level stay zero
	cost[trellisNone] = 0
	live := uint16(1) << trellisNone
	for i := top; i >= 0; i-- {
		x, r := xs[i], rs[i]
		wp := w[zig[i]]
		var next [9]float64
		for j := range next {
			next[j] = inf
		}
		nlive := uint16(0)
		for l := r; l >= max(r-1, 0); l-- {
			e := x - float64(l)
			d := wp * e * e
			for m := live; m != 0; m &= m - 1 {
				src := bits.TrailingZeros16(m)
				var b float64
				dst := src
				switch {
				case l == 0 && src == trellisNone:
				case l == 0:
					b = t.sig[i][0]
				case src == trellisNone:
					if i < n-1 {
						b = t.sig[i][1] + t.last[i][1]
					}
					b += t.levelBits(0, l)
					dst = trellisNext(0, l)
				default:
					b = t.sig[i][1] + t.last[i][0] + t.levelBits(src, l)
					dst = trellisNext(src, l)
				}
				if c := cost[src] + d + lambda2*b; c < next[dst] {
					next[dst] = c
					trace[i][dst] = trellisStep{int8(src), l}
					nlive |= 1 << dst
				}
			}
			if r == 0 {
				break
			}
		}
		cost, live = next, nlive
	}
	best, bc := trellisNone, inf
	if live>>trellisNone&1 != 0 {
		bc = cost[trellisNone] + lambda2*cbf0
	}
	for node := range 8 {
		if live>>node&1 == 0 {
			continue
		}
		if c := cost[node] + lambda2*cbf1; c < bc {
			best, bc = node, c
		}
	}
	if best == trellisNone {
		return false
	}
	node := best
	for i := 0; i <= top; i++ {
		s := trace[i][node]
		l := s.l
		if coef[zig[i]] < 0 {
			l = -l
		}
		lv[i] = l
		node = int(s.src)
	}
	return true
}

// trellisLuma4 requantises the luma 4x4 blocks of res that have levels
// (bit r of mask) by trellis, giving the new mask and level count.
func (c *depCoder) trellisLuma4(mbX, mbY int, mask uint32, qbits uint, res *residual) (uint32, int) {
	t := &c.tc
	t.load(c.e.contexts(), catLuma4x4, 16)
	mf := &quantMF[c.qp%6]
	w := &trellisW4[c.qp]
	so, ss := mbY*16*c.src.StrideY+mbX*16, c.src.StrideY
	var out uint32
	nz := 0
	for r := range 16 {
		if mask>>r&1 == 0 {
			continue
		}
		bx, by := r&3, r>>2
		var d [16]int32
		for j := range 4 {
			o := so + (by*4+j)*ss + bx*4
			po := (by*4+j)*16 + bx*4
			for i := range 4 {
				d[j*4+i] = int32(c.src.Y[o+i]) - int32(c.pred[po+i])
			}
		}
		fdct4(&d)
		lv := &res.luma[r]
		if trellisBlock(t, d[:], zigzag4x4[:], mf[:], qbits, w[:], c.lambda2, 0, 0, lv[:]) {
			out |= 1 << r
			for _, l := range lv {
				if l != 0 {
					nz++
				}
			}
		}
	}
	return out, nz
}

// trellisIntra scales lambda for intra blocks' trellis: their errors
// propagate (into the blocks predicted from them, and the pictures), so
// it pays to keep more of their levels.
const trellisIntra = 0.5

// trellisChroma requantises the AC levels of a chroma component's blocks
// that have them (bit b of mask; the source at src[so], the prediction
// pred), giving the new mask and level count.
func (c *depCoder) trellisChroma(src []byte, so int, pred *[64]byte, mask uint32, qbits uint, ac [][15]int32) (uint32, int) {
	t := &c.tc
	t.load(c.e.contexts(), catChromaAC, 15)
	mf := &quantMF[c.qpc%6]
	w := &trellisW4[c.qpc]
	ss := c.src.StrideC
	var out uint32
	nz := 0
	for b := range 4 {
		if mask>>b&1 == 0 {
			continue
		}
		bx, by := b&1, b>>1
		var d [16]int32
		for j := range 4 {
			o := so + (by*4+j)*ss + bx*4
			po := (by*4+j)*8 + bx*4
			for i := range 4 {
				d[j*4+i] = int32(src[o+i]) - int32(pred[po+i])
			}
		}
		fdct4(&d)
		if trellisBlock(t, d[:], zigzag4x4[1:], mf[:], qbits, w[:], c.lambda2, 0, 0, ac[b][:]) {
			out |= 1 << b
			for _, l := range ac[b] {
				if l != 0 {
					nz++
				}
			}
		}
	}
	return out, nz
}

// trellising is whether the coder requantises by trellis: it needs the
// coder's context states for the costs.
func (c *depCoder) trellising() bool { return c.trellis && c.e != nil }
