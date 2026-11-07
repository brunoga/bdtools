package av1

// A partition depth prior: before a superblock of an inter frame is
// searched, each of its 8x8 regions takes the depth (64x64 to 4x4, by
// the block's larger side) of the block there in the nearest reference
// in display order, and the search is allowed that depth and a step
// either side, two steps when the reference is more than depthNear
// pictures away; none past depthFar, where the prior is too weak.
// (SVT-AV1's preset 4 instead pre-decides the depths with a light pass
// over every square; tried, with the temporal model's vectors, a square
// against its quarters: a window of one 3.7% larger by PSNR-YUV for 21%
// less CPU, no better than the speeds' own curve. On the six 240-picture
// clips at speed 2 the prior is 0.2% smaller on every metric for 4% less
// CPU; the prior from LAST_FRAME on every frame 1.0% larger for 12%,
// the anchors included with a window of two 0.55% for 5.6%, a window of
// 0 for a reference a picture away 3.0% for 12%.)

// depthNear and depthFar are the reference distances (pictures) past
// which the prior's window widens and past which it is not used.
const depthNear, depthFar = 4, 16

// squareDepth is the depth of the square a block size's larger side
// makes.
func squareDepth(bSize int) uint8 {
	switch max(blockWidth[bSize], blockHeight[bSize]) {
	case 64:
		return 0
	case 32:
		return 1
	case 16:
		return 2
	case 8:
		return 3
	}
	return 4
}

// planDepths decides the depths the superblock at mi (r, c) is searched
// at (t.allowed, by 8x8 region), from the nearest reference's blocks.
func (t *tileEnc) planDepths(r, c int) {
	f := t.f
	var near *refFrameState
	dist := 0
	for ref := LAST_FRAME; ref < TOTAL_REFS_PER_FRAME; ref++ {
		s := f.refs[ref]
		if s == nil || s.depths == nil || s.miCols != f.miCols {
			continue
		}
		if d := abs(s.disp - f.fp.orderHint); near == nil || d < dist {
			near, dist = s, d
		}
	}
	if near == nil || dist > depthFar {
		return
	}
	window := 1
	if dist > depthNear {
		window = 2
	}
	w8 := f.miCols >> 1
	for i := range 64 {
		y8, x8 := (r>>1)+i/8, (c>>1)+i%8
		if 2*y8 >= f.miRows || 2*x8 >= f.miCols {
			continue
		}
		d := int(near.depths[y8*w8+x8])
		lo, hi := max(d-window, 0), min(d+window, 4)
		t.allowed[i] = uint8(1<<uint(hi+1)) - uint8(1<<uint(lo)) //nolint:gosec // bits 0..4
	}
	t.useDepths = true
}

// depthsAllow reports whether the search of the square at mi (r, c) of
// size bSize (depth d) may code it as a block (here) and may split it
// (deeper).
func (t *tileEnc) depthsAllow(r, c, bSize, d int) (here, deeper bool) {
	f := t.f
	step := int(num4x4BlocksWide[bSize])
	sr, sc := r&^15, c&^15
	for y := 0; y < step; y += 2 {
		for x := 0; x < step; x += 2 {
			if r+y >= f.miRows || c+x >= f.miCols {
				continue
			}
			a := t.allowed[((r+y-sr)>>1)*8+(c+x-sc)>>1]
			here = here || a>>uint(d)&1 != 0
			deeper = deeper || a>>uint(d+1) != 0
		}
	}
	return here, deeper
}
