package av1

import (
	"math"
	"sync/atomic"
	"testing"
)

// mePicture is an 8-bit test picture: a broad cone (so a search from far
// away descends towards the shift) with smoothed noise on it, or the
// noise alone with cone 0.
func mePicture(w, h int, cone float64) []uint16 {
	rs := uint32(7)
	n := make([]float64, w*h)
	for i := range n {
		n[i] = float64(xorshift(&rs) & 255)
	}
	p := make([]uint16, w*h)
	for y := range h {
		for x := range w {
			var s float64
			for i := -2; i <= 2; i++ {
				for j := -2; j <= 2; j++ {
					s += n[min(max(y+i, 0), h-1)*w+min(max(x+j, 0), w-1)]
				}
			}
			dx, dy := float64(x-w*7/24), float64(y-h*3/10)
			v := cone*math.Sqrt(dx*dx+dy*dy) + (s/25-128)*0.3 + 40
			p[y*w+x] = uint16(min(max(v, 0), 255))
		}
	}
	return p
}

// TestMotionSearchShift checks the search finds known shifts of a
// picture: a whole sample one, and a subpel one whose source is the
// exact prediction (so its SATD is 0 there).
func TestMotionSearchShift(t *testing.T) {
	const W, H = 192, 160
	ref := &refFrameState{width: W, height: H}
	ref.rec[0], ref.stride[0] = mePicture(W, H, 1.2), W
	filt := [2]int{EIGHTTAP, EIGHTTAP}
	var ms motionSearch
	for _, tc := range []struct {
		want    mv
		allowHP bool
	}{
		{mv{row: -7 * 8, col: 13 * 8}, true},
		{mv{row: -53, col: 27}, true},
		{mv{row: 38, col: -90}, false},
		{mv{row: 2 * 8, col: -21 * 8}, false},
		{mv{row: 0, col: 0}, true},
	} {
		src := make([]uint16, W*H)
		predictInter(ref, 0, 0, 0, 128, 128, tc.want, filt, 8, src, W, &ms.s)
		predictInter(ref, 0, 128, 0, 64, 128, tc.want, filt, 8, src[128:], W, &ms.s)
		b := &meBlock{ref: ref, src: src, srcStride: W, x: 40, y: 32, w: 32, h: 32, bitDepth: 8, filt: filt, allowHP: tc.allowHP,
			cost: func(m mv) float64 { return 0.01 * float64(abs(int(m.row))+abs(int(m.col))) }}
		got, cost := ms.search(b, []mv{{}})
		if got != tc.want {
			t.Errorf("found %+v (cost %.2f), want %+v", got, cost, tc.want)
		}
	}

	// Noise alone, a far shift, found from a start point near it (as a
	// neighbour's vector would be).
	const NW, NH = 320, 240
	noise := &refFrameState{width: NW, height: NH}
	noise.rec[0], noise.stride[0] = mePicture(NW, NH, 0), NW
	want := mv{row: 8*30 + 3, col: -8*95 - 5}
	src := make([]uint16, NW*NH)
	predictInter(noise, 0, 100, 0, 128, 128, want, filt, 8, src[100:], NW, &ms.s)
	b := &meBlock{ref: noise, src: src, srcStride: NW, x: 120, y: 20, w: 64, h: 32, bitDepth: 8, filt: filt, allowHP: true,
		cost: func(m mv) float64 { return 0 }}
	if got, cost := ms.search(b, []mv{{}, {row: want.row - 13, col: want.col + 22}}); got != want {
		t.Errorf("noise: found %+v (cost %.2f), want %+v", got, cost, want)
	}
}

func BenchmarkMotionSearch(b *testing.B) {
	const W, H = 192, 160
	ref := &refFrameState{width: W, height: H}
	ref.rec[0], ref.stride[0] = mePicture(W, H, 1.2), W
	filt := [2]int{EIGHTTAP, EIGHTTAP}
	var ms motionSearch
	src := make([]uint16, W*H)
	predictInter(ref, 0, 0, 0, 128, 128, mv{-53, 27}, filt, 8, src, W, &ms.s)
	blk := &meBlock{ref: ref, src: src, srcStride: W, x: 64, y: 48, w: 16, h: 16, bitDepth: 8, filt: filt, allowHP: true,
		cost: func(m mv) float64 { return 0 }}
	for b.Loop() {
		ms.search(blk, []mv{{}})
	}
}

func BenchmarkPredictInter(b *testing.B) {
	const W, H = 192, 160
	ref := &refFrameState{width: W, height: H}
	ref.rec[0], ref.stride[0] = mePicture(W, H, 1.2), W
	var s interScratch
	dst := make([]uint16, 64*64)
	for b.Loop() {
		predictInter(ref, 0, 40, 40, 64, 64, mv{-53, 27}, [2]int{EIGHTTAP, EIGHTTAP}, 8, dst, 64, &s)
	}
}

// Eighth sample vectors allowed (hpMaxQ above the q): some coded, and
// decoded to the reconstruction.
func TestHighPrecisionMV(t *testing.T) {
	defer func(v int) { hpMaxQ = v }(hpMaxQ)
	hpMaxQ = 256
	const w, h = 96, 64
	o := EncoderOptions{Width: w, Height: h, BitDepth: 8, QIndex: 60, FPSNum: 25, FPSDen: 1, MiniGOP: 4}
	var pics []*Source
	for i := range 5 {
		pics = append(pics, eighthShifted(w, h, float64(3*i)/8))
	}
	var hp, eighth atomic.Int64
	checkDecodesWith(t, o, pics, func(e *Encoder) {
		e.frameHook = func(f *frameEnc) {
			if f.fp.key || !f.fp.highPrecisionMV {
				return
			}
			hp.Add(1)
			for i, v := range f.mvs[0] {
				if f.isInter[i] != 0 && (v.row|v.col)&1 != 0 {
					eighth.Add(1)
				}
			}
		}
	})
	if hp.Load() == 0 || eighth.Load() == 0 {
		t.Fatalf("%d frames allowed eighth sample vectors, %d vectors", hp.Load(), eighth.Load())
	}
}

// eighthShifted is a smooth 8-bit picture moved right by dx samples.
func eighthShifted(w, h int, dx float64) *Source {
	cw, ch := (w+1)/2, (h+1)/2
	s := &Source{Y: make([]uint16, w*h), Cb: make([]uint16, cw*ch), Cr: make([]uint16, cw*ch), StrideY: w, StrideC: cw}
	for y := range h {
		for x := range w {
			xx := float64(x) - dx
			s.Y[y*w+x] = uint16(128 + 90*math.Sin(xx/3.1)*math.Cos(float64(y)/4.3))
		}
	}
	for i := range s.Cb {
		s.Cb[i], s.Cr[i] = 128, 128
	}
	return s
}

// Switchable interpolation filters (switchable on): blocks coded with
// other filters than EIGHTTAP, and decoded to the reconstruction.
func TestSwitchableFilters(t *testing.T) {
	defer func(v, n bool) { switchable, dbgNoTPL = v, n }(switchable, dbgNoTPL)
	switchable = true
	dbgNoTPL = true // the frames' q as planned: the picture is made for the filters
	const w, h = 96, 64
	o := EncoderOptions{Width: w, Height: h, BitDepth: 8, QIndex: 60, FPSNum: 25, FPSDen: 1, MiniGOP: 4}
	var pics []*Source
	for i := range 5 {
		pics = append(pics, eighthShifted(w, h, float64(3*i)/4))
	}
	var other atomic.Int64
	checkDecodesWith(t, o, pics, func(e *Encoder) {
		e.frameHook = func(f *frameEnc) {
			for i, v := range f.interp {
				if f.isInter[i] != 0 && v != EIGHTTAP {
					other.Add(1)
				}
			}
		}
	})
	if other.Load() == 0 {
		t.Fatal("no block with another filter")
	}
}

// subPelStep's costs are subCost's: every block size's shape, filter,
// step and centre phase, near the edges too (where it does not apply).
func TestSubPelStepMatches(t *testing.T) {
	const W, H = 192, 160
	ref := &refFrameState{width: W, height: H}
	ref.rec[0], ref.stride[0] = mePicture(W, H, 1.2), W
	src := mePicture(W, H, 0.7)
	var ms motionSearch
	shared := 0
	for _, sz := range [][2]int{{8, 8}, {16, 8}, {8, 32}, {32, 32}, {64, 16}, {128, 128}} {
		for _, filt := range [][2]int{{EIGHTTAP, EIGHTTAP}, {EIGHTTAP_SMOOTH, EIGHTTAP_SHARP}} {
			for _, c := range []mv{{0, 0}, {4, -4}, {-12, 20}, {6, 2}, {-3, 5}, {-300, 40}} {
				for _, step := range []int32{4, 2, 1} {
					b := &meBlock{ref: ref, src: src, srcStride: W, x: 16, y: 8, w: sz[0], h: sz[1], bitDepth: 8, filt: filt,
						cost: func(m mv) float64 { return 0.5 * float64(abs(int(m.row))+abs(int(m.col))) }}
					if b.x+b.w > W || b.y+b.h > H {
						b.x, b.y = 0, 0
					}
					var costs [8]float64
					if !ms.subPelStep(b, c, step, &costs) {
						continue
					}
					shared++
					for i, d := range subPelDirs {
						m := mv{row: c.row + d[0]*step, col: c.col + d[1]*step}
						if want := ms.subCost(b, m); costs[i] != want {
							t.Fatalf("%dx%d filters %v centre %+v step %d: %+v costs %v, want %v", b.w, b.h, filt, c, step, m, costs[i], want)
						}
					}
				}
			}
		}
	}
	if shared == 0 {
		t.Fatal("no step shared")
	}
}
