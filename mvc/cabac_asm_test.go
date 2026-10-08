//go:build amd64 && !purego

package mvc

import (
	"math/rand"
	"testing"
)

func TestCabacResidAsm(t *testing.T) {
	if !useCabacAsm {
		t.Skip("CPU lacks BMI2/LZCNT")
	}
	rng := rand.New(rand.NewSource(7))
	cats := []struct{ cat, max int }{{catLumaDC, 16}, {catLumaAC, 15}, {catLuma4x4, 16}, {catChromaDC, 4}, {catChromaAC, 15}, {catLuma8x8, 64}}
	for iter := 0; iter < 300; iter++ {
		data := make([]byte, 4096+cabacPad)
		bias := rng.Intn(4)
		for i := 0; i < 4096; i++ {
			b := byte(rng.Intn(256))
			if bias == 1 {
				b &= byte(rng.Intn(256)) // skewed data
			} else if bias == 2 {
				b |= byte(rng.Intn(256))
			}
			data[i] = b
		}
		var a, g sliceDec
		initContexts(&a.ctx, rng.Intn(4), rng.Intn(52))
		g.ctx = a.ctx
		a.cab.init(data, 0)
		g.cab.init(data, 0)
		for blk := 0; blk < 200 && a.cab.pos < 4000; blk++ {
			c := cats[rng.Intn(len(cats))]
			cbf := -1
			if c.cat != catLuma8x8 && rng.Intn(2) == 0 {
				cbf = cbfCtxIdx(c.cat, uint32(rng.Intn(2)), uint32(rng.Intn(2)))
			}
			var ca, cg coeffBuf
			var ra, rg bool
			var da, dg [256]int16
			dq := c.cat != catLumaDC && c.cat != catChromaDC && rng.Intn(3) != 0
			if dq {
				// dequantizing mode: random scale table and shifts
				var scale [64]int32
				var pos [64]uint8
				perm := rng.Perm(256)
				for i := range scale {
					scale[i] = int32(rng.Intn(600) + 1)
					pos[i] = uint8(perm[i]) // distinct destinations
				}
				start := 0
				if c.max == 15 {
					start = 1
				}
				sh := dqShifts(rng.Intn(52), c.max == 64)
				ra = a.cabacBlockAsm(cbf, c.cat, c.max, &ca, &da[0], &scale[start], &pos[start], sh)
				rg = g.cabacBlockGo(cbf, c.cat, c.max, &cg)
				if rg {
					l := uint(sh & 255)
					r := uint(sh >> 8 & 255)
					round := int32(sh >> 16)
					for k := 0; k < cg.n; k++ {
						i := int(cg.idx[k]) + start
						dg[pos[i]] = int16((cg.level[k]*scale[i]<<l + round) >> r)
					}
				}
				if da != dg {
					t.Fatalf("iter %d blk %d cat %d: dequantized output mismatch", iter, blk, c.cat)
				}
			} else {
				ra = a.cabacBlockAsm(cbf, c.cat, c.max, &ca, nil, nil, nil, 0)
				rg = g.cabacBlockGo(cbf, c.cat, c.max, &cg)
			}
			if ra != rg || a.cab.rng != g.cab.rng || a.cab.off != g.cab.off || a.cab.pos != g.cab.pos || a.ctx != g.ctx {
				t.Fatalf("iter %d blk %d cat %d: coded %v/%v state mismatch", iter, blk, c.cat, ra, rg)
			}
			if ra && !dq {
				if ca.n != cg.n || string(ca.idx[:ca.n]) != string(cg.idx[:cg.n]) {
					t.Fatalf("iter %d blk %d cat %d: idx mismatch n=%d/%d", iter, blk, c.cat, ca.n, cg.n)
				}
				for k := 0; k < ca.n; k++ {
					if ca.level[k] != cg.level[k] {
						t.Fatalf("iter %d blk %d cat %d: level[%d] %d vs %d", iter, blk, c.cat, k, ca.level[k], cg.level[k])
					}
				}
			}
		}
	}
}

func blockBench(b *testing.B, useAsm bool) {
	rng := rand.New(rand.NewSource(9))
	data := make([]byte, 1<<20+cabacPad)
	for i := 0; i < 1<<20; i++ {
		a, b := rng.Intn(256), rng.Intn(256)
		data[i] = byte(a & b) // skewed
	}
	var s sliceDec
	var cb coeffBuf
	blocks := 0
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		initContexts(&s.ctx, 1, 30)
		s.cab.init(data, 0)
		for s.cab.pos < 1<<20-256 {
			if useAsm {
				s.cabacBlockAsm(cbfCtxIdx(catLuma4x4, 1, 1), catLuma4x4, 16, &cb, nil, nil, nil, 0)
			} else {
				s.cabacBlockGo(cbfCtxIdx(catLuma4x4, 1, 1), catLuma4x4, 16, &cb)
			}
			blocks++
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(blocks), "ns/block")
}

func BenchmarkBlockGo(b *testing.B)  { blockBench(b, false) }
func BenchmarkBlockAsm(b *testing.B) { blockBench(b, true) }

// twinDecoders returns two slice decoders with identical engine and
// context state over the same random data.
func twinDecoders(rng *rand.Rand, bias int) (a, g *sliceDec) {
	data := make([]byte, 4096+cabacPad)
	for i := 0; i < 4096; i++ {
		b := byte(rng.Intn(256))
		if bias == 1 {
			b &= byte(rng.Intn(256))
		} else if bias == 2 {
			b |= byte(rng.Intn(256))
		}
		data[i] = b
	}
	a, g = new(sliceDec), new(sliceDec)
	initContexts(&a.ctx, rng.Intn(4), rng.Intn(52))
	g.ctx = a.ctx
	a.cab.init(data, 0)
	g.cab.init(data, 0)
	return
}

func sameEngine(a, g *sliceDec) bool {
	return a.cab.rng == g.cab.rng && a.cab.off == g.cab.off && a.cab.pos == g.cab.pos && a.ctx == g.ctx
}

func TestCabacMvdAsm(t *testing.T) {
	if !useCabacAsm {
		t.Skip("CPU lacks BMI2/LZCNT")
	}
	rng := rand.New(rand.NewSource(11))
	const st = 12 // grid stride in cells
	for iter := 0; iter < 300; iter++ {
		a, g := twinDecoders(rng, iter%3)
		var ga, gg [st * 8][2]uint8
		for i := range ga {
			ga[i] = [2]uint8{uint8(rng.Intn(70)), uint8(rng.Intn(70))}
		}
		gg = ga
		for n := 0; n < 300 && a.cab.pos < 3900; n++ {
			w, h := 1+rng.Intn(4)&^0, 1+rng.Intn(4)
			if w == 3 {
				w = 4
			}
			if h == 3 {
				h = 4
			}
			x, y := 1+rng.Intn(4), 1+rng.Intn(3) // leave a margin for neighbours
			avail := rng.Intn(4)
			base := y*st + x
			ax, ay := cabacMvdAsm(&a.cab, &a.ctx[0], &ga[base], st, avail, w, h)
			// Go reference
			var sx, sy int
			if avail&1 != 0 {
				sx += int(gg[base-1][0])
				sy += int(gg[base-1][1])
			}
			if avail&2 != 0 {
				sx += int(gg[base-st][0])
				sy += int(gg[base-st][1])
			}
			inc := func(sum int) int {
				if sum > 32 {
					return 2
				} else if sum >= 3 {
					return 1
				}
				return 0
			}
			gx := g.cabacMvdCompGo(0, inc(sx))
			gy := g.cabacMvdCompGo(1, inc(sy))
			v := [2]uint8{uint8(min(abs(gx), 64)), uint8(min(abs(gy), 64))}
			for j := 0; j < h; j++ {
				for i := 0; i < w; i++ {
					gg[base+j*st+i] = v
				}
			}
			if ax != gx || ay != gy || !sameEngine(a, g) || ga != gg {
				t.Fatalf("iter %d n %d: mvd (%d,%d) vs (%d,%d) grid-equal %v", iter, n, ax, ay, gx, gy, ga == gg)
			}
		}
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// TestMvPredFillAsm compares the assembly MV predictor against mvPred and
// fillMotion on random grids.
func TestMvPredFillAsm(t *testing.T) {
	if !useCabacAsm {
		t.Skip("CPU lacks BMI2/LZCNT")
	}
	rng := rand.New(rand.NewSource(21))
	shapes := [][2]int{{4, 4}, {4, 2}, {2, 4}, {2, 2}, {2, 1}, {1, 2}, {1, 1}}
	for iter := 0; iter < 20000; iter++ {
		// 3x2 macroblocks, current one at (1,1)
		pic := allocNewPicture(3, 2)
		fc := newFrameCtx(3, 2)
		for l := 0; l < 2; l++ {
			for i := range pic.refs[l] {
				pic.refs[l][i] = int8(rng.Intn(4) - 1)
				pic.mvs[l][i] = mv{int16(rng.Intn(64) - 32), int16(rng.Intn(64) - 32)}
			}
		}
		var sa, sg sliceDec
		for _, s := range []*sliceDec{&sa, &sg} {
			s.fc, s.pic = fc, pic
			s.mbX, s.mbY = 1, 1
			s.mbAddr = 4
			s.availA, s.availB, s.availC, s.availD = rng.Intn(2) == 0, rng.Intn(2) == 0, rng.Intn(2) == 0, rng.Intn(2) == 0
		}
		sg.availA, sg.availB, sg.availC, sg.availD = sa.availA, sa.availB, sa.availC, sa.availD
		pg := allocNewPicture(3, 2)
		for l := 0; l < 2; l++ {
			copy(pg.refs[l], pic.refs[l])
			copy(pg.mvs[l], pic.mvs[l])
		}
		sg.pic = pg
		for n := 0; n < 6; n++ {
			sh := shapes[rng.Intn(len(shapes))]
			w, h := sh[0], sh[1]
			x, y := rng.Intn(4-w+1), rng.Intn(4-h+1)
			if w == 4 && h == 2 || w == 2 && h == 4 {
				x, y = x&^1, y&^1
			}
			l := rng.Intn(2)
			ref := int8(rng.Intn(3))
			mvdx, mvdy := int16(rng.Intn(40)-20), int16(rng.Intn(40)-20)
			sa.setRefRegion(l, x, y, w, h, ref)
			sg.setRefRegion(l, x, y, w, h, ref)
			sa.predFill(l, ref, x, y, w, h, mvdx, mvdy)
			sg.predFillGo(l, ref, x, y, w, h, mvdx, mvdy)
			for i := range pic.refs[l] {
				if pic.refs[l][i] != pg.refs[l][i] || pic.mvs[l][i] != pg.mvs[l][i] {
					t.Fatalf("iter %d n %d: shape %dx%d at (%d,%d) ref %d avail %v: grid differs at %d: %v/%v vs %v/%v", iter, n, w, h, x, y, ref, sa.availBits(), i, pic.refs[l][i], pic.mvs[l][i], pg.refs[l][i], pg.mvs[l][i])
				}
			}
		}
	}
}

// TestMvPredModesAsm checks the P_Skip and spatial direct modes of the
// assembly predictor against the Go derivations.
func TestMvPredModesAsm(t *testing.T) {
	if !useCabacAsm {
		t.Skip("CPU lacks BMI2/LZCNT")
	}
	rng := rand.New(rand.NewSource(23))
	for iter := 0; iter < 20000; iter++ {
		pic := allocNewPicture(3, 2)
		fc := newFrameCtx(3, 2)
		zeroish := iter%2 == 0
		for l := 0; l < 2; l++ {
			for i := range pic.refs[l] {
				pic.refs[l][i] = int8(rng.Intn(4) - 1)
				if zeroish {
					pic.refs[l][i] = int8(rng.Intn(2) - 1 + rng.Intn(2))
					pic.mvs[l][i] = mv{int16(rng.Intn(3) - 1), int16(rng.Intn(3) - 1)}
				} else {
					pic.mvs[l][i] = mv{int16(rng.Intn(64) - 32), int16(rng.Intn(64) - 32)}
				}
			}
		}
		var s sliceDec
		s.fc, s.pic = fc, pic
		s.mbX, s.mbY = 1, 1
		s.mbAddr = 4
		s.availA, s.availB, s.availC, s.availD = rng.Intn(2) == 0, rng.Intn(2) == 0, rng.Intn(2) == 0, rng.Intn(2) == 0
		for l := 0; l < 2; l++ {
			rA, _, _ := s.nb(l, -1, 0)
			rB, _, _ := s.nb(l, 0, -1)
			rC, _, _ := s.nbC(l, 0, 0, 4)
			ref := minPositive(rA, minPositive(rB, rC))
			pm := 0
			if ref >= 0 {
				pm = packMV(s.mvPred(l, ref, 0, 0, 4, 4))
			}
			r, p := s.spatialPred(l)
			if r != ref || p != pm {
				t.Fatalf("iter %d list %d: spatial got %d/%x want %d/%x", iter, l, r, p, ref, pm)
			}
		}
		want := s.pskipMV()
		pg := allocNewPicture(3, 2)
		copy(pg.refs[0], pic.refs[0])
		copy(pg.mvs[0], pic.mvs[0])
		s.pskipFill()
		sg := s
		sg.pic = pg
		sg.fillMotion(0, 0, 0, 4, 4, 0, want)
		for i := range pic.refs[0] {
			if pic.refs[0][i] != pg.refs[0][i] || pic.mvs[0][i] != pg.mvs[0][i] {
				t.Fatalf("iter %d: P_Skip grid differs at %d: %v/%v vs %v/%v (avail %v)", iter, i, pic.refs[0][i], pic.mvs[0][i], pg.refs[0][i], pg.mvs[0][i], s.availBits())
			}
		}
	}
}

func TestCabacCBPAsm(t *testing.T) {
	if !useCabacAsm {
		t.Skip("CPU lacks BMI2/LZCNT")
	}
	rng := rand.New(rand.NewSource(12))
	for iter := 0; iter < 300; iter++ {
		a, g := twinDecoders(rng, iter%3)
		for n := 0; n < 400 && a.cab.pos < 3900; n++ {
			la, lb := rng.Intn(16), rng.Intn(16)
			ca, cb := rng.Intn(3), rng.Intn(3)
			chroma := rng.Intn(4) != 0
			ci := 0
			if chroma {
				ci = 1
			}
			ra := uint8(cabacCBPAsm(&a.cab, &a.ctx[0], la, lb, ca, cb, ci))
			rg := g.cabacCBPGo(la, lb, ca, cb, chroma)
			if ra != rg || !sameEngine(a, g) {
				t.Fatalf("iter %d n %d: cbp %#x vs %#x", iter, n, ra, rg)
			}
		}
	}
}

func TestCabacBlocksAsm(t *testing.T) {
	if !useCabacAsm {
		t.Skip("CPU lacks BMI2/LZCNT")
	}
	rng := rand.New(rand.NewSource(13))
	for iter := 0; iter < 400; iter++ {
		a, g := twinDecoders(rng, iter%3)
		var da, dg [256]int16
		var scale [2][64]int32
		for c := range scale {
			for i := range scale[c] {
				scale[c][i] = int32(rng.Intn(600) + 1)
			}
		}
		for n := 0; n < 40 && a.cab.pos < 3500; n++ {
			var args blockArgs
			args.cbfA, args.cbfB = uint32(rng.Uint32()), uint32(rng.Uint32())
			if rng.Intn(4) == 0 {
				args.cbfA = ^uint32(0)
			}
			cbfCur := uint32(rng.Intn(1 << 27))
			switch rng.Intn(4) {
			case 0:
				args.cat, args.desc, args.cbp = catLumaAC, lumaBlkDesc[:], 15
				args.scale, args.pos = [2][]int32{scale[0][1:]}, zzPos16[1:]
				args.shifts = [2]int{dqShifts(rng.Intn(52), false)}
			case 1:
				args.cat, args.desc, args.cbp = catLuma4x4, lumaBlkDesc[:], rng.Intn(16)
				args.scale, args.pos = [2][]int32{scale[0][:]}, zzPos16[:]
				args.shifts = [2]int{dqShifts(rng.Intn(52), false)}
			case 2:
				args.cat, args.desc, args.cbp = catLuma8x8, luma8x8Desc[:], rng.Intn(16)
				args.scale, args.pos = [2][]int32{scale[0][:]}, zz8Pos16[:]
				args.shifts = [2]int{dqShifts(rng.Intn(52), true)}
			default:
				args.cat, args.desc, args.cbp = catChromaAC, chromaACDesc[:], 0xff
				args.scale, args.pos = [2][]int32{scale[0][1:], scale[1][1:]}, zzPos8[1:]
				args.shifts = [2]int{dqShifts(rng.Intn(52), false), dqShifts(rng.Intn(52), false)}
			}
			args.dst = da[:]
			oa, na, aa := a.cabacBlocksAsm(&args, cbfCur)
			args.dst = dg[:]
			og, ng, ag := g.cabacBlocksGo(&args, cbfCur)
			if oa != og || na != ng || aa != ag || da != dg || !sameEngine(a, g) {
				t.Fatalf("iter %d n %d cat %d: cbf %#x/%#x nz %#x/%#x ac %#x/%#x coef-equal %v", iter, n, args.cat, oa, og, na, ng, aa, ag, da == dg)
			}
			da, dg = [256]int16{}, [256]int16{}
		}
	}
}

func TestCabacChromaDCAsm(t *testing.T) {
	if !useCabacAsm {
		t.Skip("CPU lacks BMI2/LZCNT")
	}
	rng := rand.New(rand.NewSource(41))
	for iter := 0; iter < 400; iter++ {
		a, g := twinDecoders(rng, iter%3)
		for n := 0; n < 100 && a.cab.pos < 3800; n++ {
			cbfA, cbfB := uint32(rng.Uint32()), uint32(rng.Uint32())
			cbfCur := uint32(rng.Intn(1 << 27))
			scale := [2]int32{int32(rng.Intn(50) + 10), int32(rng.Intn(50) + 10)}
			qp := [2]int{rng.Intn(52), rng.Intn(52)}
			var ca, cg [2][64]int16
			oa, nza := cabacChromaDCAsm(&a.cab, &a.ctx[0], &a.cb, int(cbfA), int(cbfB), int(cbfCur), &ca[0][0],
				int(scale[0]), int(scale[1]), qp[0]/6, qp[1]/6)
			// Go reference
			og, nzg := cbfCur, uint32(0)
			for c := 0; c < 2; c++ {
				bit := uint32(cbfCbDC) << c
				ctxa := (cbfA >> (25 + c)) & 1
				ctxb := (cbfB >> (25 + c)) & 1
				if !g.cabacBlockGo(cbfCtxIdx(catChromaDC, ctxa, ctxb), catChromaDC, 4, &g.cb) {
					continue
				}
				og |= bit
				var dc [4]int32
				for k := 0; k < g.cb.n; k++ {
					dc[g.cb.idx[k]] = g.cb.level[k]
				}
				chromaDCDequant(&dc, qp[c], scale[c])
				for b := 0; b < 4; b++ {
					if dc[b] != 0 {
						cg[c][b>>1*32+b&1*4] = int16(dc[b])
						nzg |= 1 << (c*4 + b)
					}
				}
			}
			if uint32(oa) != og || uint32(nza) != nzg || ca != cg || !sameEngine(a, g) {
				t.Fatalf("iter %d n %d: cbf %#x/%#x nz %#x/%#x coef-equal %v", iter, n, oa, og, nza, nzg, ca == cg)
			}
		}
	}
}

func TestCabacIntraModesAsm(t *testing.T) {
	if !useCabacAsm {
		t.Skip("CPU lacks BMI2/LZCNT")
	}
	rng := rand.New(rand.NewSource(61))
	for iter := 0; iter < 300; iter++ {
		a, g := twinDecoders(rng, iter%3)
		for n := 0; n < 100 && a.cab.pos < 3800; n++ {
			cnt := 4
			if rng.Intn(2) == 0 {
				cnt = 16
			}
			a.cabacIntraModes(cnt)
			g.cabacIntraModesGo(cnt)
			for i := 0; i < cnt; i++ {
				if a.mb.prevFlag[i] != g.mb.prevFlag[i] || !a.mb.prevFlag[i] && a.mb.remMode[i] != g.mb.remMode[i] {
					t.Fatalf("iter %d n %d: mode %d differs", iter, n, i)
				}
			}
			if !sameEngine(a, g) {
				t.Fatalf("iter %d n %d: engine differs", iter, n)
			}
		}
	}
}

// TestDirectFillAsm compares the assembly spatial direct fill with the Go
// derivation on random grids.
func TestDirectFillAsm(t *testing.T) {
	if !useCabacAsm {
		t.Skip("CPU lacks BMI2/LZCNT")
	}
	rng := rand.New(rand.NewSource(71))
	for iter := 0; iter < 20000; iter++ {
		pic := allocNewPicture(3, 2)
		col := allocNewPicture(3, 2)
		for l := 0; l < 2; l++ {
			for i := range pic.refs[l] {
				pic.refs[l][i] = int8(rng.Intn(4) - 1)
				pic.mvs[l][i] = mv{int16(rng.Intn(64) - 32), int16(rng.Intn(64) - 32)}
				col.refs[l][i] = int8(rng.Intn(3) - 1)
				col.mvs[l][i] = mv{int16(rng.Intn(5) - 2), int16(rng.Intn(5) - 2)}
			}
		}
		pg := allocNewPicture(3, 2)
		for l := 0; l < 2; l++ {
			copy(pg.refs[l], pic.refs[l])
			copy(pg.mvs[l], pic.mvs[l])
		}
		fc := newFrameCtx(3, 2)
		h := &sliceHeader{sps: &sps{direct8x8Inference: rng.Intn(2) == 0}, directSpatial: true}
		var info sliceRefInfo
		info.lt[1][0] = rng.Intn(4) == 0
		mk := func(p *picture) *sliceDec {
			s := &sliceDec{fc: fc, pic: p, colPic: col, h: h, refInfo: &info, mbX: 1, mbY: 1, mbAddr: 4}
			s.refList[1] = []*picture{col}
			return s
		}
		sa, sg := mk(pic), mk(pg)
		av := [4]bool{rng.Intn(2) == 0, rng.Intn(2) == 0, rng.Intn(2) == 0, rng.Intn(2) == 0}
		for _, s := range []*sliceDec{sa, sg} {
			s.availA, s.availB, s.availC, s.availD = av[0], av[1], av[2], av[3]
		}
		mask := uint8(rng.Intn(16))
		if rng.Intn(2) == 0 {
			mask = 15
		}
		sa.directSpatial(mask)
		// Go reference: force the Go path
		saved := useCabacAsm
		useCabacAsm = false
		sg.directSpatial(mask)
		useCabacAsm = saved
		for l := 0; l < 2; l++ {
			for i := range pic.refs[l] {
				if pic.refs[l][i] != pg.refs[l][i] || pic.mvs[l][i] != pg.mvs[l][i] {
					t.Fatalf("iter %d: mask %d infer %v: grid differs at list %d block %d: %v/%v vs %v/%v", iter, mask, h.sps.direct8x8Inference, l, i, pic.refs[l][i], pic.mvs[l][i], pg.refs[l][i], pg.mvs[l][i])
				}
			}
		}
		// uniformity bits against uniformMotion
		u := sa.directUniform
		if mask == 15 && (u&1 != 0) != sg.uniformMotion(0, 0, 4) {
			t.Fatalf("iter %d: whole-MB uniformity %v vs %v", iter, u&1 != 0, sg.uniformMotion(0, 0, 4))
		}
		for b8 := 0; b8 < 4; b8++ {
			if mask>>b8&1 != 0 && (u>>(1+b8)&1 != 0) != sg.uniformMotion((b8&1)*2, (b8>>1)*2, 2) {
				t.Fatalf("iter %d: 8x8 %d uniformity differs", iter, b8)
			}
		}
	}
}
