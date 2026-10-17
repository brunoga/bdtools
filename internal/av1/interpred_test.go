package av1

import (
	"fmt"
	"testing"
)

// interCase is a random inter prediction: a reference plane, a block and
// a motion vector, drawn as the libaom harness
// (~/mvc-scratch/av1/interharness) draws them.
type interCase struct {
	ref        refFrameState
	plane      int
	x, y, w, h int
	m          mv
	filt       [2]int
}

func xorshift(s *uint32) uint32 {
	*s ^= *s << 13
	*s ^= *s >> 17
	*s ^= *s << 5
	return *s
}

func nextInterCase(rs *uint32, bd int, buf []uint16) interCase {
	rnd := func() uint32 { return xorshift(rs) }
	var tc interCase
	refW, refH := 8+int(rnd()%193), 8+int(rnd()%193)
	sub := int(rnd() & 1)
	pw, ph := (refW+sub)>>sub, (refH+sub)>>sub
	mode := rnd() % 3
	maxV := 1<<uint(bd) - 1
	for i := range ph {
		for j := range pw {
			r := rnd()
			var v int
			switch mode {
			case 0:
				v = int(r) & maxV
			case 1:
				v = maxV * int(r&1)
			default:
				v = min(max(((i*3+j*5)<<uint(bd-8))%(maxV+1)+int(r%9)-4, 0), maxV)
			}
			buf[i*pw+j] = uint16(v) //nolint:gosec // a sample
		}
	}
	sizes := [7]int{2, 4, 8, 16, 32, 64, 128}
	tc.w, tc.h = sizes[rnd()%7], sizes[rnd()%7]
	tc.x, tc.y = int(rnd()%uint32(pw)), int(rnd()%uint32(ph)) //nolint:gosec // small
	if rnd()%3 == 0 {
		lim := 8 * (refH + 300)
		tc.m.row = int32(int(rnd()%uint32(2*lim)) - lim) //nolint:gosec // small
		lim = 8 * (refW + 300)
		tc.m.col = int32(int(rnd()%uint32(2*lim)) - lim) //nolint:gosec // small
	} else {
		tc.m.row = int32(rnd()%1025) - 512 //nolint:gosec // small
		tc.m.col = int32(rnd()%1025) - 512 //nolint:gosec // small
	}
	tc.filt[1], tc.filt[0] = int(rnd()&3), int(rnd()&3)
	tc.plane = sub
	tc.ref.width, tc.ref.height = refW, refH
	tc.ref.rec[sub], tc.ref.stride[sub] = buf, pw
	return tc
}

func fnvAdd(h *uint32, v uint32) {
	for i := range 4 {
		*h ^= v >> uint(8*i) & 255
		*h *= 16777619
	}
}

// TestInterPredLibaom checks the block inter prediction against libaom's
// C convolutions (single prediction clipped, compound preds[] raw) over
// random blocks of 2 to 128, motion vectors far outside the picture
// included, every filter pair and 8, 10 and 12 bits.
func TestInterPredLibaom(t *testing.T) {
	want := map[[2]int]uint32{
		{8, 0}: 0x7df66e65, {8, 1}: 0xea1221b4,
		{10, 0}: 0x68ed7328, {10, 1}: 0x19cc9dc2,
		{12, 0}: 0x8690f2a3, {12, 1}: 0x05c28337,
	}
	n := 3000
	if testing.Short() {
		n = 0
	}
	var s interScratch
	buf := make([]uint16, 400*400)
	d16 := make([]uint16, 128*128)
	d32 := make([]int32, 128*128)
	for _, bd := range []int{8, 10, 12} {
		for comp := range 2 {
			rs := uint32(0x12345678 + bd*7 + comp) //nolint:gosec // small
			h := uint32(2166136261)
			for range n {
				tc := nextInterCase(&rs, bd, buf)
				if comp == 0 {
					predictInter(&tc.ref, tc.plane, tc.x, tc.y, tc.w, tc.h, tc.m, tc.filt, bd, d16, tc.w, &s)
					for _, v := range d16[:tc.w*tc.h] {
						fnvAdd(&h, uint32(v))
					}
					continue
				}
				sx, sy, _, _ := scaleMv(tc.plane, tc.x, tc.y, tc.m, tc.ref.width, tc.ref.height, tc.ref.width, tc.ref.height)
				s.blockInterPred(&tc.ref, tc.plane, sx, sy, tc.w, tc.h, tc.filt, interRoundingFor(bd, true), bd, nil, d32, tc.w)
				for _, v := range d32[:tc.w*tc.h] {
					fnvAdd(&h, uint32(v)) //nolint:gosec // hashed bits
				}
			}
			if n == 3000 && h != want[[2]int{bd, comp}] {
				t.Errorf("bit depth %d compound %d: hash %#08x, want %#08x", bd, comp, h, want[[2]int{bd, comp}])
			}
		}
	}
}

// specInterPred is the block inter prediction process transcribed
// literally (any steps), the yardstick for the fast paths.
func specInterPred(ref *refFrameState, plane, x, y, xStep, yStep, w, h int, filt [2]int, rnd interRounding, bd int) []int32 {
	sub := min(plane, 1)
	lastX := ((ref.width + sub) >> sub) - 1
	lastY := ((ref.height + sub) >> sub) - 1
	ih := (((h-1)*yStep + (1 << SCALE_SUBPEL_BITS) - 1) >> SCALE_SUBPEL_BITS) + 8
	inter := make([]int32, ih*w)
	at := func(r, c int) int32 {
		return int32(ref.rec[plane][min(max(r, 0), lastY)*ref.stride[plane]+min(max(c, 0), lastX)])
	}
	fx := filterFor(filt[1], w)
	for r := range ih {
		for c := range w {
			var s int32
			p := x + xStep*c
			for t := range 8 {
				s += int32(subpelFilters[fx][(p>>6)&SUBPEL_MASK][t]) * at((y>>10)+r-3, (p>>10)+t-3)
			}
			inter[r*w+c] = round2(s, uint(rnd.round0))
		}
	}
	fy := filterFor(filt[0], h)
	pred := make([]int32, w*h)
	for r := range h {
		for c := range w {
			var s int32
			p := (y & 1023) + yStep*r
			for t := range 8 {
				s += int32(subpelFilters[fy][(p>>6)&SUBPEL_MASK][t]) * inter[((p>>10)+t)*w+c]
			}
			pred[r*w+c] = round2(s, uint(rnd.round1))
		}
	}
	return pred
}

// TestInterPredSpec checks the fast block process against the literal
// one, single (clipped) and compound.
func TestInterPredSpec(t *testing.T) {
	var s interScratch
	buf := make([]uint16, 400*400)
	d16 := make([]uint16, 128*128)
	d32 := make([]int32, 128*128)
	rs := uint32(99)
	for i := range 600 {
		bd := []int{8, 10, 12}[i%3]
		tc := nextInterCase(&rs, bd, buf)
		comp := i&1 == 1
		rnd := interRoundingFor(bd, comp)
		sx, sy, stx, sty := scaleMv(tc.plane, tc.x, tc.y, tc.m, tc.ref.width, tc.ref.height, tc.ref.width, tc.ref.height)
		if stx != 1024 || sty != 1024 {
			t.Fatalf("steps %d %d", stx, sty)
		}
		want := specInterPred(&tc.ref, tc.plane, sx, sy, stx, sty, tc.w, tc.h, tc.filt, rnd, bd)
		maxV := int32(1)<<uint(bd) - 1
		if comp {
			s.blockInterPred(&tc.ref, tc.plane, sx, sy, tc.w, tc.h, tc.filt, rnd, bd, nil, d32, tc.w)
		} else {
			predictInter(&tc.ref, tc.plane, tc.x, tc.y, tc.w, tc.h, tc.m, tc.filt, bd, d16, tc.w, &s)
		}
		for j, v := range want {
			got := d32[j]
			if !comp {
				v, got = clip(v, maxV), int32(d16[j])
			}
			if got != v {
				t.Fatalf("case %d (%+v): sample %d is %d, want %d", i, tc, j, got, v)
			}
		}
	}
}

// TestPredictInterBlockChroma checks the chroma of 4x4 luma blocks: with
// all four inter, four 2x2 predictions each by its block's motion; with
// one intra, one 4x4 by the bottom right block's.
func TestPredictInterBlockChroma(t *testing.T) {
	const W, H = 64, 64
	f := &frameEnc{bitDepth: 8, miCols: W / 4, miRows: H / 4, width: W, height: H, fp: &frameParams{interpFilter: EIGHTTAP}}
	n := f.miCols * f.miRows
	for k := range 2 {
		f.refFrames[k] = make([]int8, n)
		f.mvs[k] = make([]mv, n)
	}
	ref := &refFrameState{width: W, height: H}
	rs := uint32(5)
	for p := range 3 {
		w := W >> uint(min(p, 1))
		ref.rec[p], ref.stride[p] = make([]uint16, w*w), w
		for i := range ref.rec[p] {
			ref.rec[p][i] = uint16(xorshift(&rs) & 255) //nolint:gosec // a sample
		}
	}
	var refs [TOTAL_REFS_PER_FRAME]*refFrameState
	refs[LAST_FRAME] = ref
	mvs := [4]mv{{3, -5}, {-17, 9}, {40, 1}, {-2, -66}}
	for i, m := range mvs {
		r, c := 6+i/2, 8+i%2
		f.refFrames[0][f.mi(r, c)], f.refFrames[1][f.mi(r, c)] = LAST_FRAME, NONE
		f.mvs[0][f.mi(r, c)] = m
	}
	var s interScratch
	filt := [2]int{EIGHTTAP, EIGHTTAP}
	got := make([]uint16, 16)
	b := &blockState{r: 7, c: 9, size: BLOCK_4X4, bw4: 1, bh4: 1, hasChroma: true}
	f.predictInterBlock(&refs, b, 1, filt, got, 4, &s)
	want := make([]uint16, 16)
	for i, m := range mvs {
		y, x := i/2*2, i%2*2
		predictInter(ref, 1, 16+x, 12+y, 2, 2, m, filt, 8, want[y*4+x:], 4, &s)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("all inter: %v, want %v", got, want)
	}
	f.refFrames[0][f.mi(6, 8)] = INTRA_FRAME
	f.predictInterBlock(&refs, b, 1, filt, got, 4, &s)
	predictInter(ref, 1, 16, 12, 4, 4, mvs[3], filt, 8, want, 4, &s)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("one intra: %v, want %v", got, want)
	}
}
