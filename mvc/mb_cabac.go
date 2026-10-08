package mvc

import "math/bits"

// CABAC parsing of macroblock layer syntax elements (9.3).

func (s *sliceDec) leftMB() *mbInfo {
	if s.availA {
		return &s.fc.mbs[s.mbAddr-1]
	}
	return nil
}

func (s *sliceDec) topMB() *mbInfo {
	if s.availB {
		return &s.fc.mbs[s.mbAddr-s.fc.mbW]
	}
	return nil
}

func (s *sliceDec) cabacSkipFlag() bool {
	inc := 0
	if a := s.leftMB(); a != nil && a.flags&mbfSkip == 0 {
		inc++
	}
	if b := s.topMB(); b != nil && b.flags&mbfSkip == 0 {
		inc++
	}
	base := 11
	if s.h.sliceType == sliceB {
		base = 24
	}
	return s.cab.decision(&s.ctx[base+inc]) != 0
}

// cabacMBTypeI decodes an I macroblock type (or the suffix in P/B slices).
// Returns 0 (I_NxN), 1..24 (I_16x16), 25 (I_PCM).
func (s *sliceDec) cabacMBTypeI(prefixCtx int, inI bool) int {
	c := &s.cab
	if inI {
		inc := 0
		if a := s.leftMB(); a != nil && a.flags&mbfINxN == 0 {
			inc++
		}
		if b := s.topMB(); b != nil && b.flags&mbfINxN == 0 {
			inc++
		}
		if c.decision(&s.ctx[3+inc]) == 0 {
			return 0
		}
	} else if c.decision(&s.ctx[prefixCtx]) == 0 {
		return 0
	}
	if c.terminate() != 0 {
		return 25
	}
	var b2, b3, i4, i5, i6 int
	if inI {
		b2, b3, i4, i5, i6 = 3+3, 3+4, 3+5, 3+6, 3+7
	} else {
		b2, b3, i4, i5, i6 = prefixCtx+1, prefixCtx+2, prefixCtx+2, prefixCtx+3, prefixCtx+3
	}
	t := 1
	if c.decision(&s.ctx[b2]) != 0 {
		t += 12
	}
	if c.decision(&s.ctx[b3]) != 0 {
		t += 4
		if c.decision(&s.ctx[i4]) != 0 {
			t += 4
		}
		t += int(c.decision(&s.ctx[i5])) << 1
		t += int(c.decision(&s.ctx[i6]))
	} else {
		t += int(c.decision(&s.ctx[i5])) << 1
		t += int(c.decision(&s.ctx[i6]))
	}
	return t
}

// cabacMBTypeP returns 0..3 for inter types or 5+ for intra.
func (s *sliceDec) cabacMBTypeP() int {
	c := &s.cab
	if c.decision(&s.ctx[14]) == 0 {
		if c.decision(&s.ctx[15]) == 0 {
			return 3 * int(c.decision(&s.ctx[16])) // 0: 16x16, 3: 8x8
		}
		if c.decision(&s.ctx[17]) == 0 {
			return 2 // 8x16
		}
		return 1 // 16x8
	}
	return 5 + s.cabacMBTypeI(17, false)
}

func (s *sliceDec) cabacMBTypeB() int {
	c := &s.cab
	inc := 0
	if a := s.leftMB(); a != nil && a.flags&mbfDirect16 == 0 {
		inc++
	}
	if b := s.topMB(); b != nil && b.flags&mbfDirect16 == 0 {
		inc++
	}
	if c.decision(&s.ctx[27+inc]) == 0 {
		return 0
	}
	if c.decision(&s.ctx[27+3]) == 0 {
		return 1 + int(c.decision(&s.ctx[27+5]))
	}
	bits := int(c.decision(&s.ctx[27+4])) << 3
	bits |= int(c.decision(&s.ctx[27+5])) << 2
	bits |= int(c.decision(&s.ctx[27+5])) << 1
	bits |= int(c.decision(&s.ctx[27+5]))
	switch {
	case bits < 8:
		return bits + 3
	case bits == 13:
		return 23 + s.cabacMBTypeI(32, false)
	case bits == 14:
		return 11
	case bits == 15:
		return 22
	}
	bits = bits<<1 | int(c.decision(&s.ctx[27+5]))
	return bits - 4
}

func (s *sliceDec) cabacSubMBTypeP() int {
	c := &s.cab
	if c.decision(&s.ctx[21]) != 0 {
		return 0
	}
	if c.decision(&s.ctx[22]) == 0 {
		return 1
	}
	if c.decision(&s.ctx[23]) != 0 {
		return 2
	}
	return 3
}

func (s *sliceDec) cabacSubMBTypeB() int {
	c := &s.cab
	if c.decision(&s.ctx[36]) == 0 {
		return 0
	}
	if c.decision(&s.ctx[37]) == 0 {
		return 1 + int(c.decision(&s.ctx[39]))
	}
	t := 3
	if c.decision(&s.ctx[38]) != 0 {
		if c.decision(&s.ctx[39]) != 0 {
			return 11 + int(c.decision(&s.ctx[39]))
		}
		t += 4
	}
	t += 2 * int(c.decision(&s.ctx[39]))
	t += int(c.decision(&s.ctx[39]))
	return t
}

func (s *sliceDec) cabacTransform8x8() bool {
	inc := 0
	if a := s.leftMB(); a != nil && a.flags&mbfT8x8 != 0 {
		inc++
	}
	if b := s.topMB(); b != nil && b.flags&mbfT8x8 != 0 {
		inc++
	}
	return s.cab.decision(&s.ctx[399+inc]) != 0
}

func (s *sliceDec) cabacChromaPredMode() uint8 {
	c := &s.cab
	inc := 0
	if a := s.leftMB(); a != nil && a.chromaPred != 0 {
		inc++
	}
	if b := s.topMB(); b != nil && b.chromaPred != 0 {
		inc++
	}
	if c.decision(&s.ctx[64+inc]) == 0 {
		return 0
	}
	if c.decision(&s.ctx[67]) == 0 {
		return 1
	}
	if c.decision(&s.ctx[67]) == 0 {
		return 2
	}
	return 3
}

func (s *sliceDec) cabacIntraModesGo(n int) {
	c := &s.cab
	mb := &s.mb
	for i := 0; i < n; i++ {
		mb.prevFlag[i] = c.decision(&s.ctx[68]) != 0
		if !mb.prevFlag[i] {
			v := c.decision(&s.ctx[69])
			v |= c.decision(&s.ctx[69]) << 1
			v |= c.decision(&s.ctx[69]) << 2
			mb.remMode[i] = int8(v)
		}
	}
}

// cabacCBP decodes coded_block_pattern.
func (s *sliceDec) cabacCBP() uint8 {
	lumaA, lumaB := 0xf, 0xf // unavailable: all bits set
	chromaA, chromaB := 0, 0
	if a := s.leftMB(); a != nil {
		lumaA, chromaA = int(a.cbp&15), int(a.cbp>>4)
	}
	if b := s.topMB(); b != nil {
		lumaB, chromaB = int(b.cbp&15), int(b.cbp>>4)
	}
	return s.cabacCBPWith(lumaA, lumaB, chromaA, chromaB, s.h.sps.chromaFormatIdc != 0)
}

func (s *sliceDec) cabacCBPGo(lumaA, lumaB, chromaA, chromaB int, chroma bool) uint8 {
	c := &s.cab
	var cbp int
	for b8 := 0; b8 < 4; b8++ {
		var la, lb int
		if b8&1 == 0 {
			la = lumaA >> (b8 + 1) & 1
		} else {
			la = cbp >> (b8 - 1) & 1
		}
		if b8 < 2 {
			lb = lumaB >> (b8 + 2) & 1
		} else {
			lb = cbp >> (b8 - 2) & 1
		}
		inc := (la ^ 1) + 2*(lb^1)
		cbp |= int(c.decision(&s.ctx[73+inc])) << b8
	}
	if !chroma {
		return uint8(cbp)
	}
	inc := 0
	if chromaA != 0 {
		inc++
	}
	if chromaB != 0 {
		inc += 2
	}
	if c.decision(&s.ctx[77+inc]) == 0 {
		return uint8(cbp)
	}
	inc = 4
	if chromaA == 2 {
		inc++
	}
	if chromaB == 2 {
		inc += 2
	}
	return uint8(cbp | (1+int(c.decision(&s.ctx[77+inc])))<<4)
}

func (s *sliceDec) cabacQPDelta() int {
	c := &s.cab
	inc := 0
	if s.lastDQ != 0 {
		inc = 1
	}
	if c.decision(&s.ctx[60+inc]) == 0 {
		return 0
	}
	k := 1
	ctx := 62
	for c.decision(&s.ctx[ctx]) != 0 {
		k++
		ctx = 63
		if k > 200 {
			break
		}
	}
	if k&1 != 0 {
		return (k + 1) >> 1
	}
	return -(k >> 1)
}

// refIdxCond returns the ref_idx context condition for the neighbouring 4x4
// block at (x,y) relative to the current macroblock (x,y may be -1).
func (s *sliceDec) refIdxCond(l, x, y int) int {
	var m *mbInfo
	if x < 0 {
		if !s.availA {
			return 0
		}
		m = &s.fc.mbs[s.mbAddr-1]
	} else if y < 0 {
		if !s.availB {
			return 0
		}
		m = &s.fc.mbs[s.mbAddr-s.fc.mbW]
	} else {
		m = &s.fc.mbs[s.mbAddr]
	}
	if m.flags&(mbfSkip|mbfIntra) != 0 {
		return 0
	}
	bx, by := (x+4)&3, (y+4)&3
	if m.directSub>>((by>>1)*2+(bx>>1))&1 != 0 {
		return 0
	}
	if s.pic.refs[l][s.idx4(x, y)] > 0 {
		return 1
	}
	return 0
}

func (s *sliceDec) cabacRefIdx(l, x, y int) int {
	c := &s.cab
	inc := s.refIdxCond(l, x-1, y) + 2*s.refIdxCond(l, x, y-1)
	if c.decision(&s.ctx[54+inc]) == 0 {
		return 0
	}
	if c.decision(&s.ctx[58]) == 0 {
		return 1
	}
	v := 2
	for c.decision(&s.ctx[59]) != 0 {
		v++
		if v > 32 {
			break
		}
	}
	return v
}

// absMvdN returns the stored absolute mvd component of the neighbour block.
func (s *sliceDec) absMvdN(l, comp, x, y int) int {
	if x < 0 && !s.availA || y < 0 && !s.availB {
		return 0
	}
	return int(s.fc.mvd[l][s.idx4(x, y)][comp])
}

var mvdCtxInc = [9]uint8{0, 3, 4, 5, 6, 6, 6, 6, 6}

// mvdInc returns the first-bin context increment of an mvd component.
func (s *sliceDec) mvdInc(l, comp, x, y int) int {
	sum := s.absMvdN(l, comp, x-1, y) + s.absMvdN(l, comp, x, y-1)
	if sum > 32 {
		return 2
	} else if sum >= 3 {
		return 1
	}
	return 0
}

// cabacMvdCompGo decodes one mvd component with first-bin increment inc.
func (s *sliceDec) cabacMvdCompGo(comp, inc int) int {
	c := &s.cab
	base := 40 + comp*7
	// prefix: TU with cMax 9, one inline decision site, engine state in
	// registers
	rng, off := c.rng, c.off
	ctx := &s.ctx
	ci := uint(base+inc) & 511
	v := 0
	for {
		st := ctx[ci]
		shift := uint(bits.LeadingZeros64(rng)) & 63
		lps := uint64(uint8(cabacLPS4[uint(st>>1)&63]>>((rng<<shift>>58)&24))) << ((55 - shift) & 63)
		rng -= lps
		noff := off - rng
		var hit uint
		if off >= rng {
			hit = 1
		}
		if hit != 0 {
			off = noff
		}
		if hit != 0 {
			rng = lps
		}
		ctx[ci] = cabacTrans[(hit<<7|uint(st))&255]
		if rng < 256 {
			c.pos, off, rng = cabacRefill(c.buf, c.pos, rng, off)
		}
		if (uint(st)^hit)&1 == 0 {
			break
		}
		v++
		if v == 9 {
			break
		}
		ci = uint(base+int(mvdCtxInc[v])) & 511
	}
	if v == 0 {
		c.rng, c.off = rng, off
		return 0
	}
	if v >= 9 {
		// Exp-Golomb k=3 suffix
		c.rng, c.off = rng, off
		k := uint(3)
		for c.bypass() != 0 {
			v += 1 << k
			k++
			if k > 24 {
				break
			}
		}
		v += int(c.bypassBits(int(k)))
		rng, off = c.rng, c.off
	}
	// sign (bypass)
	if rng < 512 {
		c.pos, off, rng = cabacRefill(c.buf, c.pos, rng, off)
	}
	rng >>= 1
	neg := off >= rng
	if neg {
		off -= rng
	}
	c.rng, c.off = rng, off
	if neg {
		return -v
	}
	return v
}

// cabacMvd decodes an mvd pair for the partition with top-left 4x4 block
// (x,y) and size (w,h) in 4x4 units, storing absolute values for context
// derivation.
func (s *sliceDec) cabacMvdGo(l, x, y, w, h int) (int16, int16) {
	mx := s.cabacMvdCompGo(0, s.mvdInc(l, 0, x, y))
	my := s.cabacMvdCompGo(1, s.mvdInc(l, 1, x, y))
	ax, ay := mx, my
	if ax < 0 {
		ax = -ax
	}
	if ay < 0 {
		ay = -ay
	}
	if ax > 64 {
		ax = 64
	}
	if ay > 64 {
		ay = 64
	}
	v := [2]uint8{uint8(ax), uint8(ay)}
	g := s.fc.mvd[l]
	st := s.fc.mbW * 4
	base := s.idx4(x, y)
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			g[base+j*st+i] = v
		}
	}
	return int16(mx), int16(my)
}

// Residual block categories.
const (
	catLumaDC   = 0
	catLumaAC   = 1
	catLuma4x4  = 2
	catChromaDC = 3
	catChromaAC = 4
	catLuma8x8  = 5
)

var cbfCatOffset = [5]int{0, 4, 8, 12, 16}

var sig8x8Inc = [63]uint8{
	0, 1, 2, 3, 4, 5, 5, 4, 4, 3, 3, 4, 4, 4, 5, 5,
	4, 4, 4, 4, 3, 3, 6, 7, 7, 7, 8, 9, 10, 9, 8, 7,
	7, 6, 11, 12, 13, 11, 6, 7, 8, 9, 14, 10, 9, 8, 6, 11,
	12, 13, 11, 6, 9, 14, 10, 9, 11, 12, 13, 11, 14, 10, 12,
}
var last8x8Inc = [63]uint8{
	0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
	2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2,
	3, 3, 3, 3, 3, 3, 3, 3, 4, 4, 4, 4, 4, 4, 4, 4,
	5, 5, 5, 5, 6, 6, 6, 6, 7, 7, 7, 7, 8, 8, 8,
}

// coeffBuf receives decoded coefficient levels in scan order.
type coeffBuf struct {
	n     int
	idx   [64]uint8
	level [64]int32
}

// cbfCtxIdx returns the coded_block_flag context index for the condition
// terms of the neighbouring blocks.
func cbfCtxIdx(cat int, condA, condB uint32) int {
	return 85 + cbfCatOffset[cat] + int(condA) + 2*int(condB)
}

// Per-category context increment tables for significant_coeff_flag and
// last_significant_coeff_flag, indexed by levelListIdx.
var (
	sigIncTab   [6][64]uint8
	lastIncTab  [6][64]uint8
	sigBaseTab  = [6]int{105 + 0, 105 + 15, 105 + 29, 105 + 44, 105 + 47, 402}
	lastBaseTab = [6]int{166 + 0, 166 + 15, 166 + 29, 166 + 44, 166 + 47, 417}
	absBaseTab  = [6]int{227 + 0, 227 + 10, 227 + 20, 227 + 30, 227 + 39, 426}
)

// sigLastCtx[cat][i] holds the absolute context indices of
// significant_coeff_flag and last_significant_coeff_flag for levelListIdx i.
var sigLastCtx [8][64][2]uint16

func init() {
	defer func() {
		for c := 0; c < 6; c++ {
			for i := 0; i < 64; i++ {
				sigLastCtx[c][i] = [2]uint16{uint16(sigBaseTab[c] + int(sigIncTab[c][i])), uint16(lastBaseTab[c] + int(lastIncTab[c][i]))}
			}
		}
	}()
	for c := 0; c < 6; c++ {
		for i := 0; i < 64; i++ {
			switch c {
			case catLuma8x8:
				if i < 63 {
					sigIncTab[c][i] = sig8x8Inc[i]
					lastIncTab[c][i] = last8x8Inc[i]
				}
			case catChromaDC:
				sigIncTab[c][i] = uint8(min(i, 2))
				lastIncTab[c][i] = uint8(min(i, 2))
			default:
				if i < 16 {
					sigIncTab[c][i] = uint8(i)
					lastIncTab[c][i] = uint8(i)
				}
			}
		}
	}
}

// cabacBlock decodes coded_block_flag (unless cbfCtx < 0) and, if set,
// the significance map and levels for a block of maxNum coefficients into
// cb. It reports whether the block is coded. The engine state is kept in registers and
// the decision process is expanded inline (one site per loop).
func (s *sliceDec) cabacBlock(cbfCtx int, cat int, maxNum int, cb *coeffBuf) bool {
	if useCabacAsm {
		return s.cabacBlockAsm(cbfCtx, cat, maxNum, cb, nil, nil, nil, 0)
	}
	return s.cabacBlockGo(cbfCtx, cat, maxNum, cb)
}

// dqShifts packs the dequantization shift parameters: the scaled level is
// ((level*scale << l) + round) >> r with l = bits 0-7, r = bits 8-15 and
// round = bits 16+. eight selects the 8x8 transform scaling.
func dqShifts(qp int, eight bool) int {
	qd := qp / 6
	base := 4
	if eight {
		base = 6
	}
	if qd >= base {
		return qd - base
	}
	return (base-qd)<<8 | 1<<(base-1-qd)<<16
}

// cabacBlockDQ decodes a block like cabacBlock and writes the scaled
// coefficients to dst: coefficient with scan index i goes to dst[pos[i]]
// scaled by scale[i] (both tables already offset by the block's first scan
// index). shifts comes from dqShifts.
func (s *sliceDec) cabacBlockDQ(cbfCtx, cat, maxNum int, cb *coeffBuf, dst []int16, scale []int32, pos []uint8, shifts int) bool {
	if useCabacAsm {
		return s.cabacBlockAsm(cbfCtx, cat, maxNum, cb, &dst[0], &scale[0], &pos[0], shifts)
	}
	if !s.cabacBlockGo(cbfCtx, cat, maxNum, cb) {
		return false
	}
	l := uint(shifts & 255)
	r := uint(shifts >> 8 & 255)
	round := int32(shifts >> 16)
	for k := 0; k < cb.n; k++ {
		i := cb.idx[k]
		v := (cb.level[k]*scale[i]<<l + round) >> r
		dst[pos[i]] = int16(v)
	}
	return true
}

func (s *sliceDec) cabacBlockGo(cbfCtx int, cat int, maxNum int, cb *coeffBuf) bool {
	c := &s.cab
	rng, off := c.rng, c.off
	ctx := &s.ctx
	if cbfCtx >= 0 {
		// coded_block_flag, decoded with the same register-held state
		ci := uint(cbfCtx) & 511
		st := ctx[ci]
		shift := uint(bits.LeadingZeros64(rng)) & 63
		lps := uint64(uint8(cabacLPS4[uint(st>>1)&63]>>((rng<<shift>>58)&24))) << ((55 - shift) & 63)
		rng -= lps
		noff := off - rng
		var hit uint
		if off >= rng {
			hit = 1
		}
		if hit != 0 {
			off = noff
		}
		if hit != 0 {
			rng = lps
		}
		ctx[ci] = cabacTrans[(hit<<7|uint(st))&255]
		if rng < 256 {
			c.pos, off, rng = cabacRefill(c.buf, c.pos, rng, off)
		}
		if (uint(st)^hit)&1 == 0 {
			c.rng, c.off = rng, off
			return false
		}
	}
	tab := &sigLastCtx[cat&7]
	n := 0
	i := 0
	end := maxNum - 1
	expectLast := false
	if end > 0 {
		for {
			ci := uint(tab[i&63][0]) & 511
			if expectLast {
				ci = uint(tab[i&63][1]) & 511
			}
			// DecodeDecision
			st := ctx[ci]
			shift := uint(bits.LeadingZeros64(rng)) & 63
			lps := uint64(uint8(cabacLPS4[uint(st>>1)&63]>>((rng<<shift>>58)&24))) << ((55 - shift) & 63)
			rng -= lps
			noff := off - rng
			var hit uint
			if off >= rng {
				hit = 1
			}
			if hit != 0 {
				off = noff
			}
			if hit != 0 {
				rng = lps
			}
			ctx[ci] = cabacTrans[(hit<<7|uint(st))&255]
			if rng < 256 {
				c.pos, off, rng = cabacRefill(c.buf, c.pos, rng, off)
			}
			bin := (uint(st) ^ hit) & 1
			if expectLast {
				if bin != 0 {
					goto levels
				}
				expectLast = false
				i++
			} else if bin != 0 {
				cb.idx[n&63] = uint8(i)
				n++
				expectLast = true
				continue
			} else {
				i++
			}
			if i >= end {
				break
			}
		}
	}
	cb.idx[n&63] = uint8(i)
	n++
levels:
	cb.n = n
	absB := uint(absBaseTab[cat])
	gt1Max := 4
	if cat == catChromaDC {
		gt1Max = 3
	}
	numGt1, numEq1 := 0, 0
	for k := n - 1; k >= 0; k-- {
		var ci uint
		if numGt1 == 0 {
			ci = (absB + uint(min(1+numEq1, 4))) & 511
		} else {
			ci = absB & 511
		}
		cN := (absB + 5 + uint(min(numGt1, gt1Max))) & 511
		p := int32(0)
		for {
			// DecodeDecision
			st := ctx[ci]
			shift := uint(bits.LeadingZeros64(rng)) & 63
			lps := uint64(uint8(cabacLPS4[uint(st>>1)&63]>>((rng<<shift>>58)&24))) << ((55 - shift) & 63)
			rng -= lps
			noff := off - rng
			var hit uint
			if off >= rng {
				hit = 1
			}
			if hit != 0 {
				off = noff
			}
			if hit != 0 {
				rng = lps
			}
			ctx[ci] = cabacTrans[(hit<<7|uint(st))&255]
			if rng < 256 {
				c.pos, off, rng = cabacRefill(c.buf, c.pos, rng, off)
			}
			if (uint(st)^hit)&1 == 0 {
				break
			}
			p++
			if p == 14 {
				break
			}
			ci = cN
		}
		var lv int32
		if p == 0 {
			lv = 1
			numEq1++
		} else {
			if p == 14 {
				// Exp-Golomb k=0 suffix
				c.rng, c.off = rng, off
				kk := uint(0)
				for c.bypass() != 0 {
					p += 1 << kk
					kk++
					if kk > 30 {
						break
					}
				}
				if kk > 0 {
					p += int32(c.bypassBits(int(kk)))
				}
				rng, off = c.rng, c.off
			}
			lv = p + 1
			numGt1++
		}
		// sign (bypass)
		if rng < 512 {
			c.pos, off, rng = cabacRefill(c.buf, c.pos, rng, off)
		}
		rng >>= 1
		if off >= rng {
			off -= rng
			lv = -lv
		}
		cb.level[k&63] = lv
	}
	c.rng, c.off = rng, off
	return true
}
