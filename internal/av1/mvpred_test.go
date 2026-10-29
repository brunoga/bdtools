package av1

import "testing"

// mvGrid is a synthetic frame for motion vector prediction: every mi an
// intra 4x4 block, coded (BlockDecoded) where a test says so.
func mvGrid(miRows, miCols int) *tileEnc {
	n := miRows * miCols
	f := &frameEnc{miRows: miRows, miCols: miCols, mis: make([]modeInfo, n)}
	for i := range n {
		f.mis[i].refFrames[0], f.mis[i].refFrames[1] = INTRA_FRAME, NONE
	}
	f.inter = newInterFrameInfo()
	f.inter.allowHighPrecisionMv = true
	return &tileEnc{f: f, rowEnd: miRows, colEnd: miCols}
}

// place puts an inter block (ref1 NONE for single prediction) on the grid.
func (t *tileEnc) place(r, c, size, mode, ref0, ref1 int, mv0, mv1 mv) {
	f := t.f
	for y := r; y < min(r+int(num4x4BlocksHigh[size]), f.miRows); y++ {
		for x := c; x < min(c+int(num4x4BlocksWide[size]), f.miCols); x++ {
			i := f.mi(y, x)
			f.mis[i].miSize, f.mis[i].yMode, f.mis[i].isInter = uint8(size), uint8(mode), 1
			f.mis[i].refFrames[0], f.mis[i].refFrames[1] = int8(ref0), int8(ref1)
			f.mis[i].mvs[0], f.mis[i].mvs[1] = mv0, mv1
		}
	}
}

// at sets up the block at (r, c), its superblock's decoded flags as a
// decoder has them with everything above and left of the block coded
// (rows above it, and the columns left of it down to its bottom).
func (t *tileEnc) at(r, c, size int) {
	t.setupBlock(r, c, size)
	sbR, sbC := r&^15, c&^15
	t.clearDecoded(sbR, sbC)
	for y := sbR; y < r+int(num4x4BlocksHigh[size]); y++ {
		for x := sbC; x < sbC+16; x++ {
			if y < r || x < c {
				t.decoded[0][y-sbR+1][x-sbC+1] = true
			}
		}
	}
}

func checkStack(t *testing.T, s *mvStack, n int, mvs [][2]mv, weights []int) {
	t.Helper()
	if s.numMvFound != n {
		t.Fatalf("NumMvFound %d, want %d (stack %v, weights %v)", s.numMvFound, n, s.refStackMv, s.weightStack)
	}
	for i, m := range mvs {
		if s.refStackMv[i][0] != m[0] || (s.isCompound && s.refStackMv[i][1] != m[1]) {
			t.Errorf("RefStackMv[%d] %v, want %v", i, s.refStackMv[i], m)
		}
	}
	for i, w := range weights {
		if s.weightStack[i] != w {
			t.Errorf("WeightStack[%d] %d, want %d", i, s.weightStack[i], w)
		}
	}
}

func checkCtx(t *testing.T, s *mvStack, newCtx, refCtx, zeroCtx int, drl []int) {
	t.Helper()
	if s.newMvContext != newCtx || s.refMvContext != refCtx || s.zeroMvContext != zeroCtx {
		t.Errorf("contexts new %d ref %d zero %d, want %d %d %d", s.newMvContext, s.refMvContext, s.zeroMvContext, newCtx, refCtx, zeroCtx)
	}
	for i, z := range drl {
		if s.drlCtxStack[i] != z {
			t.Errorf("DrlCtxStack[%d] %d, want %d", i, s.drlCtxStack[i], z)
		}
	}
}

// One 16x16 NEWMV block above: found by the nearest row scan and again by
// the row 3 above (deltaCol 1, weight 8), the stack padded with the
// global motion vector.
func TestMvStackAboveOnly(t *testing.T) {
	g := mvGrid(32, 32)
	g.place(4, 8, BLOCK_16X16, NEWMV, LAST_FRAME, NONE, mv{4, 6}, mv{})
	g.at(8, 8, BLOCK_16X16)
	var s mvStack
	g.findMvStack(&s, false, [2]int{LAST_FRAME, NONE})
	checkStack(t, &s, 1, [][2]mv{{{4, 6}}, {{0, 0}}}, []int{8 + REF_CAT_LEVEL + 8})
	if s.newMvCount != 2 {
		t.Errorf("NewMvCount %d, want 2", s.newMvCount)
	}
	checkCtx(t, &s, 2, 3, 0, []int{0})
	// NEARMV with one candidate takes the padding, NEWMV the first.
	if m := s.predMv(NEARMV, 1, 0); m != (mv{}) {
		t.Errorf("NEARMV PredMv %v", m)
	}
	if m := s.predMv(NEWMV, 0, 0); m != (mv{4, 6}) {
		t.Errorf("NEWMV PredMv %v", m)
	}
	if lo, hi := s.refMvIdxRange(NEWMV); lo != 0 || hi != 0 {
		t.Errorf("NEWMV RefMvIdx range %d..%d", lo, hi)
	}
	if _, _, n := s.drlModes(NEWMV, 0); n != 0 {
		t.Errorf("%d drl_modes with one candidate", n)
	}
	// Another reference: the extra search takes the LAST motion vector
	// (same sign bias), then pads.
	g.findMvStack(&s, false, [2]int{GOLDEN_FRAME, NONE})
	checkStack(t, &s, 1, [][2]mv{{{4, 6}}, {{0, 0}}}, []int{2})
	checkCtx(t, &s, 0, 0, 0, []int{0})
}

// Compound blocks to the left (two 8x8 with the same pair merged), the
// extra search adding the same pair again from RefIdMvs (it compares
// with RefStackMv[0] and takes combinedMvs[1], equal here).
func TestMvStackLeftCompound(t *testing.T) {
	g := mvGrid(32, 32)
	g.f.inter.refFrameSignBias[ALTREF_FRAME] = 1
	p := [2]mv{{2, 2}, {-2, -2}}
	g.place(8, 6, BLOCK_8X8, NEAREST_NEARESTMV, LAST_FRAME, ALTREF_FRAME, p[0], p[1])
	g.place(10, 6, BLOCK_8X8, NEAREST_NEARESTMV, LAST_FRAME, ALTREF_FRAME, p[0], p[1])
	g.place(12, 6, BLOCK_8X8, NEWMV, LAST_FRAME, NONE, mv{8, 8}, mv{})
	g.at(8, 8, BLOCK_16X16)
	var s mvStack
	g.findMvStack(&s, true, [2]int{LAST_FRAME, ALTREF_FRAME})
	checkStack(t, &s, 2, [][2]mv{p, p}, []int{8 + REF_CAT_LEVEL, 2})
	checkCtx(t, &s, 3, 3, 0, []int{1, 0})
	if got, want := s.compoundModeCtx(), int(compoundModeCtxMap[1][3]); got != want {
		t.Errorf("compound_mode ctx %d, want %d", got, want)
	}
	// The single LAST stack takes list 0 of the pair; the NEWMV block at
	// row 12 is past the column scans (bh4 rows) and the extra search
	// (num4x4 rows), and the ALTREF vectors, negated, repeat (2, 2): the
	// global motion vector pads.
	g.findMvStack(&s, false, [2]int{LAST_FRAME, NONE})
	checkStack(t, &s, 1, [][2]mv{{{2, 2}}, {{0, 0}}}, []int{8 + REF_CAT_LEVEL})
	checkCtx(t, &s, 3, 3, 0, []int{0})
}

// Above, left, above-right (when coded) and above-left, duplicates
// merged, the outer candidates sorted by weight.
func TestMvStackBoth(t *testing.T) {
	g := mvGrid(32, 32)
	g.place(6, 8, BLOCK_8X8, NEARESTMV, LAST_FRAME, NONE, mv{10, 0}, mv{})
	g.place(8, 6, BLOCK_8X8, NEWMV, LAST_FRAME, NONE, mv{10, 0}, mv{})
	g.place(6, 10, BLOCK_8X8, GLOBALMV, LAST_FRAME, NONE, mv{20, 4}, mv{})
	g.place(6, 6, BLOCK_8X8, NEARMV, LAST_FRAME, NONE, mv{20, 4}, mv{})
	g.place(4, 8, BLOCK_16X8, NEARMV, LAST_FRAME, NONE, mv{-8, 16}, mv{})
	g.place(8, 4, BLOCK_8X8, NEARMV, LAST_FRAME, NONE, mv{-8, 16}, mv{})
	var s mvStack

	// Above-right coded.
	g.at(8, 8, BLOCK_8X8)
	g.findMvStack(&s, false, [2]int{LAST_FRAME, NONE})
	checkStack(t, &s, 3, [][2]mv{{{10, 0}}, {{20, 4}}, {{-8, 16}}}, []int{648, 648, 8})
	checkCtx(t, &s, 4, 5, 0, []int{0, 1, 0})
	if s.newMvCount != 1 {
		t.Errorf("NewMvCount %d, want 1", s.newMvCount)
	}

	// Above-right not coded yet (as for the bottom-left block of a
	// split): the above-left candidate lands in the outer part, which
	// the sort reorders.
	g.decoded[0][7+1][10+1] = false
	g.findMvStack(&s, false, [2]int{LAST_FRAME, NONE})
	checkStack(t, &s, 3, [][2]mv{{{10, 0}}, {{-8, 16}}, {{20, 4}}}, []int{648, 8, 4})
	checkCtx(t, &s, 4, 5, 0, []int{1, 2, 0})

	// The drl_mode symbols and the predicted motion vectors.
	if lo, hi := s.refMvIdxRange(NEWMV); lo != 0 || hi != 2 {
		t.Errorf("NEWMV RefMvIdx range %d..%d", lo, hi)
	}
	if lo, hi := s.refMvIdxRange(NEARMV); lo != 1 || hi != 2 {
		t.Errorf("NEARMV RefMvIdx range %d..%d", lo, hi)
	}
	if ctx, val, n := s.drlModes(NEWMV, 2); n != 2 || ctx != [2]int{1, 2} || val != [2]int{1, 1} {
		t.Errorf("NEWMV idx 2 drl_modes %v %v %d", ctx, val, n)
	}
	if ctx, val, n := s.drlModes(NEWMV, 0); n != 1 || ctx[0] != 1 || val[0] != 0 {
		t.Errorf("NEWMV idx 0 drl_modes %v %v %d", ctx, val, n)
	}
	if ctx, val, n := s.drlModes(NEARMV, 1); n != 1 || ctx[0] != 2 || val[0] != 0 {
		t.Errorf("NEARMV idx 1 drl_modes %v %v %d", ctx, val, n)
	}
	if ctx, val, n := s.drlModes(NEARMV, 2); n != 1 || ctx[0] != 2 || val[0] != 1 {
		t.Errorf("NEARMV idx 2 drl_modes %v %v %d", ctx, val, n)
	}
	for _, c := range []struct {
		mode, idx int
		want      mv
	}{{NEARESTMV, 0, mv{10, 0}}, {NEARMV, 1, mv{-8, 16}}, {NEARMV, 2, mv{20, 4}}, {NEWMV, 2, mv{20, 4}}, {GLOBALMV, 0, mv{}}} {
		if m := s.predMv(c.mode, c.idx, 0); m != c.want {
			t.Errorf("mode %d idx %d PredMv %v, want %v", c.mode, c.idx, m, c.want)
		}
	}
}

// Candidates beyond the frame's edges clamped (to MV_BORDER plus the
// block's size past them); the tile's edge stops the above-right scan.
func TestMvStackClamp(t *testing.T) {
	g := mvGrid(16, 16)
	g.place(12, 14, BLOCK_8X8, NEWMV, LAST_FRAME, NONE, mv{1000, 1000}, mv{})
	g.place(14, 12, BLOCK_8X8, NEWMV, LAST_FRAME, NONE, mv{-5000, -5000}, mv{})
	g.at(14, 14, BLOCK_8X8)
	var s mvStack
	g.findMvStack(&s, false, [2]int{LAST_FRAME, NONE})
	// Bottom and right edges 0 away, top and left 14*32 = 448 away; the
	// border 128 + 8*8.
	checkStack(t, &s, 2, [][2]mv{{{192, 192}}, {{-640, -640}}}, []int{644, 644})
	checkCtx(t, &s, 4, 5, 0, []int{0, 0})
}

// The extra search: no neighbour with the reference, so the row above
// and the column left give their motion vectors (negated where the sign
// bias differs, not lowered in precision).
func TestMvStackExtraSearch(t *testing.T) {
	g := mvGrid(32, 32)
	g.f.inter.allowHighPrecisionMv = false
	g.f.inter.refFrameSignBias[ALTREF_FRAME] = 1
	g.f.inter.refFrameSignBias[GOLDEN_FRAME] = 1
	g.place(6, 8, BLOCK_8X8, NEWMV, LAST_FRAME, NONE, mv{6, -2}, mv{})
	g.place(8, 6, BLOCK_8X8, NEWMV, GOLDEN_FRAME, NONE, mv{3, 3}, mv{})
	g.at(8, 8, BLOCK_8X8)
	var s mvStack
	g.findMvStack(&s, false, [2]int{ALTREF_FRAME, NONE})
	checkStack(t, &s, 2, [][2]mv{{{-6, 2}}, {{3, 3}}}, []int{2, 2})
	checkCtx(t, &s, 0, 0, 0, []int{2, 0})
	if s.newMvCount != 0 {
		t.Errorf("NewMvCount %d", s.newMvCount)
	}
}

// A compound neighbour serves a single reference from its second list.
func TestMvStackSecondList(t *testing.T) {
	g := mvGrid(32, 32)
	g.place(4, 8, BLOCK_16X16, NEW_NEWMV, LAST_FRAME, ALTREF_FRAME, mv{2, 4}, mv{-6, 8})
	g.at(8, 8, BLOCK_16X16)
	var s mvStack
	g.findMvStack(&s, false, [2]int{ALTREF_FRAME, NONE})
	// The extra search adds list 0's vector (same sign bias).
	checkStack(t, &s, 2, [][2]mv{{{-6, 8}}, {{2, 4}}}, []int{656, 2})
	if s.newMvCount != 2 {
		t.Errorf("NewMvCount %d, want 2", s.newMvCount)
	}
}

func TestLowerMvPrecision(t *testing.T) {
	fi := newInterFrameInfo()
	for _, c := range []struct {
		hp, integer bool
		in, want    mv
	}{
		{true, false, mv{5, -7}, mv{5, -7}},
		{false, false, mv{5, -7}, mv{4, -6}},
		{false, true, mv{5, -12}, mv{8, -8}},
		{false, true, mv{-3, 4}, mv{0, 0}},
		{false, true, mv{-13, 20}, mv{-16, 16}},
	} {
		fi.allowHighPrecisionMv, fi.forceIntegerMv = c.hp, c.integer
		m := c.in
		fi.lowerMvPrecision(&m)
		if m != c.want {
			t.Errorf("hp %v int %v: %v gives %v, want %v", c.hp, c.integer, c.in, m, c.want)
		}
	}
}

// A ROTZOOM global motion projects the block's centre (39, 39).
func TestSetupGlobalMv(t *testing.T) {
	g := mvGrid(32, 32)
	fi := g.f.inter
	fi.gmType[LAST_FRAME] = ROTZOOM
	fi.gmParams[LAST_FRAME] = [6]int32{1 << 16, 0, 1<<16 + 655, -100, 100, 1<<16 + 655}
	fi.gmType[GOLDEN_FRAME] = TRANSLATION
	fi.gmParams[GOLDEN_FRAME] = [6]int32{3 << 13, -5 << 13, 1 << 16, 0, 0, 1 << 16}
	g.at(8, 8, BLOCK_16X16)
	s := mvStack{refFrame: [2]int{LAST_FRAME, GOLDEN_FRAME}}
	// xc = 655*39 - 100*39 + 65536 = 87181, yc = 100*39 + 655*39 = 29445
	if m := g.setupGlobalMv(&s, 0); m != (mv{4, 11}) {
		t.Errorf("ROTZOOM %v", m)
	}
	if m := g.setupGlobalMv(&s, 1); m != (mv{3, -5}) {
		t.Errorf("TRANSLATION %v", m)
	}
	fi.allowHighPrecisionMv = false
	if m := g.setupGlobalMv(&s, 0); m != (mv{4, 10}) {
		t.Errorf("ROTZOOM low precision %v", m)
	}
	if m := g.setupGlobalMv(&s, 1); m != (mv{2, -4}) {
		t.Errorf("TRANSLATION low precision %v", m)
	}
}

func TestHasOverlappableCandidates(t *testing.T) {
	g := mvGrid(32, 32)
	g.at(8, 8, BLOCK_16X16)
	if g.hasOverlappableCandidates() {
		t.Error("intra neighbours overlappable")
	}
	g.place(6, 10, BLOCK_8X8, NEWMV, LAST_FRAME, NONE, mv{}, mv{})
	if !g.hasOverlappableCandidates() {
		t.Error("inter above not overlappable")
	}
	g = mvGrid(32, 32)
	g.place(10, 7, BLOCK_4X4, NEWMV, LAST_FRAME, NONE, mv{}, mv{})
	g.at(8, 8, BLOCK_16X16)
	if g.hasOverlappableCandidates() {
		t.Error("left at an even row looked at")
	}
	g.place(11, 7, BLOCK_4X4, NEWMV, LAST_FRAME, NONE, mv{}, mv{})
	if !g.hasOverlappableCandidates() {
		t.Error("left at row 11 not found")
	}
}

func TestRefFrameSignBias(t *testing.T) {
	fi := newInterFrameInfo()
	fi.enableOrderHint, fi.orderHintBits, fi.orderHint = true, 7, 2
	fi.orderHints = [TOTAL_REFS_PER_FRAME]int{0, 1, 0, 127, 0, 3, 4, 66}
	fi.setRefFrameSignBias()
	want := [TOTAL_REFS_PER_FRAME]int{0, 0, 0, 0, 0, 1, 1, 0}
	if fi.refFrameSignBias != want {
		t.Errorf("RefFrameSignBias %v, want %v", fi.refFrameSignBias, want)
	}
}

// The compound extra search with nothing found: RefDiffMvs for both
// lists (negated where the sign bias differs), two entries.
func TestMvStackCompoundExtra(t *testing.T) {
	g := mvGrid(32, 32)
	g.f.inter.refFrameSignBias[ALTREF_FRAME] = 1
	g.f.inter.refFrameSignBias[BWDREF_FRAME] = 1
	g.place(6, 8, BLOCK_8X8, NEWMV, GOLDEN_FRAME, NONE, mv{4, 4}, mv{})
	g.place(8, 6, BLOCK_8X8, NEWMV, BWDREF_FRAME, NONE, mv{2, -2}, mv{})
	g.at(8, 8, BLOCK_8X8)
	var s mvStack
	g.findMvStack(&s, true, [2]int{LAST_FRAME, ALTREF_FRAME})
	checkStack(t, &s, 2, [][2]mv{{{4, 4}, {-4, -4}}, {{-2, 2}, {2, -2}}}, []int{2, 2})
	checkCtx(t, &s, 0, 0, 0, []int{2, 0})
}
