package av1

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestDbgIsolate(t *testing.T) {
	if os.Getenv("AV1DBG") == "" {
		t.Skip("AV1DBG unset")
	}
	type sw struct {
		name string
		v    *bool
	}
	all := []sw{{"dct", &dbgDCTOnly}, {"angles", &dbgNoAngles}, {"depth", &dbgNoDepth}, {"square", &dbgSquare}}
	for mask := 0; mask < 16; mask++ {
		name := ""
		for i, s := range all {
			*s.v = mask>>i&1 == 1
			if *s.v {
				name += s.name + "-off "
			}
		}
		t.Run(name, func(t *testing.T) {
			o := EncoderOptions{Width: 200, Height: 136, BitDepth: 8, QIndex: 60, FPSNum: 25, FPSDen: 1}
			checkDecodes(t, o, []*Source{testPicture(200, 136, 8, 1)})
		})
	}
	for _, s := range all {
		*s.v = false
	}
}

func TestDbgFixed(t *testing.T) {
	if os.Getenv("AV1DBG") == "" {
		t.Skip("AV1DBG unset")
	}
	dbgDCTOnly, dbgNoAngles, dbgNoDepth = true, true, true
	defer func() { dbgDCTOnly, dbgNoAngles, dbgNoDepth, dbgFixed = false, false, false, 0 }()
	for _, s := range []int{BLOCK_8X8, BLOCK_16X16, BLOCK_32X32, BLOCK_64X64} {
		dbgFixed = s
		t.Run(string(rune('0'+s)), func(t *testing.T) {
			o := EncoderOptions{Width: 200, Height: 136, BitDepth: 8, QIndex: 60, FPSNum: 25, FPSDen: 1}
			checkDecodes(t, o, []*Source{testPicture(200, 136, 8, 1)})
		})
	}
}

func TestDbgCands(t *testing.T) {
	if os.Getenv("AV1DBG") == "" {
		t.Skip("AV1DBG unset")
	}
	dbgDCTOnly, dbgNoAngles, dbgNoDepth, dbgFixed = true, true, true, BLOCK_16X16
	defer func() {
		dbgDCTOnly, dbgNoAngles, dbgNoDepth, dbgFixed, lumaCands, chromaCands = false, false, false, 0, 3, 2
	}()
	for _, c := range [][2]int{{1, 1}, {3, 1}, {1, 2}} {
		lumaCands, chromaCands = c[0], c[1]
		t.Run(string(rune('0'+c[0]))+string(rune('0'+c[1])), func(t *testing.T) {
			o := EncoderOptions{Width: 200, Height: 136, BitDepth: 8, QIndex: 60, FPSNum: 25, FPSDen: 1}
			checkDecodes(t, o, []*Source{testPicture(200, 136, 8, 1)})
		})
	}
}

func TestDbgCompare(t *testing.T) {
	if os.Getenv("AV1DBG") == "" {
		t.Skip("AV1DBG unset")
	}
	dbgDCTOnly, dbgNoAngles, dbgNoDepth, dbgFixed, dbgCompare = true, true, true, BLOCK_16X16, true
	lumaCands, chromaCands = 1, 1
	defer func() {
		dbgDCTOnly, dbgNoAngles, dbgNoDepth, dbgFixed, lumaCands, chromaCands, dbgCompare = false, false, false, 0, 3, 2, false
	}()
	o := EncoderOptions{Width: 200, Height: 136, BitDepth: 8, QIndex: 60, FPSNum: 25, FPSDen: 1}
	checkDecodes(t, o, []*Source{testPicture(200, 136, 8, 1)})
}

// Every mode, size and availability predicted twice with different
// garbage in the edge buffers: the predictions must not differ.
func TestDbgEdgeReads(t *testing.T) {
	rec := make([]uint16, 256*256)
	for i := range rec {
		rec[i] = uint16(i*7919) & 255
	}
	var e1, e2 intraEdges
	for i := range e1.above {
		e1.above[i], e1.left[i] = 1000, 1000
		e2.above[i], e2.left[i] = 5000, 5000
	}
	p1, p2 := make([]uint16, 64*64), make([]uint16, 64*64)
	for txSz := range TX_SIZES_ALL {
		for mode := DC_PRED; mode <= PAETH_PRED; mode++ {
			for a := -3; a <= 3; a++ {
				if a != 0 && !isDirectional(mode) {
					continue
				}
				for av := range 16 * 6 {
					fi := av/16 - 1
					if fi >= 0 && (mode != DC_PRED || txWidth[txSz] > 32 || txHeight[txSz] > 32) {
						continue
					}
					b := intraBlock{plane: 0, x: 64, y: 64, log2W: int(txWidthLog2[txSz]), log2H: int(txHeightLog2[txSz]),
						haveLeft: av&1 != 0, haveAbove: av&2 != 0, haveAboveRight: av&4 != 0, haveBelow: av&8 != 0,
						maxX: 255, maxY: 255, mode: mode, angleDelta: a, filterIntra: fi, bitDepth: 8}
					e1.predictIntra(&b, rec, 256, p1)
					e2.predictIntra(&b, rec, 256, p2)
					w, h := 1<<b.log2W, 1<<b.log2H
					for i := range w * h {
						if p1[i] != p2[i] {
							t.Fatalf("tx %d mode %d angle %d avail %04b: (%d,%d) reads outside its edges", txSz, mode, a, av, i%w, i/w)
						}
					}
				}
			}
		}
	}
}

// CDEF signalled, every strength zero.
func TestDbgCdefSignal(t *testing.T) {
	o := EncoderOptions{Width: 200, Height: 136, BitDepth: 8, QIndex: 100, FPSNum: 25, FPSDen: 1}
	checkDecodesWith(t, o, []*Source{testPicture(200, 136, 8, 1)}, func(e *Encoder) { e.seq.cdef = true })
}

func TestDbgWriteInter(t *testing.T) {
	if os.Getenv("AV1DBG") == "" {
		t.Skip("AV1DBG unset")
	}
	o := EncoderOptions{Width: 200, Height: 136, BitDepth: 8, QIndex: 100, FPSNum: 25, FPSDen: 1}
	e, _ := NewEncoder(o)
	var units [][]byte
	for _, u := range encodeAll(t, e, []*Source{testPicture(200, 136, 8, 1), testPicture(200, 136, 8, 2)}) {
		units = append(units, u.Data)
	}
	_ = os.WriteFile(os.Getenv("DBGOUT"), ivf(200, 136, 25, 1, units), 0o600)
}

func TestDbgInterModes(t *testing.T) {
	if os.Getenv("AV1DBG") == "" {
		t.Skip("AV1DBG unset")
	}
	defer func() { dbgInterModes, dbgForceInter = 0, false }()
	dbgForceInter = true
	for _, m := range []int{1 << 2, 1 << 0, 1 << 1, 1 << 3, 15} {
		dbgInterModes = m
		t.Run(fmt.Sprint(m), func(t *testing.T) {
			o := EncoderOptions{Width: 200, Height: 136, BitDepth: 8, QIndex: 100, FPSNum: 25, FPSDen: 1}
			p := testPicture(200, 136, 8, 1)
			checkDecodes(t, o, []*Source{p, shifted(p, 3, 2), shifted(p, 5, -1)})
		})
	}
}

func TestDbgInterMixed(t *testing.T) {
	if os.Getenv("AV1DBG") == "" {
		t.Skip("AV1DBG unset")
	}
	defer func() { dbgInterModes = 0 }()
	for _, m := range []int{1 << 2, 1 << 0, 1 << 1, 1 << 3} {
		dbgInterModes = m
		t.Run(fmt.Sprint(m), func(t *testing.T) {
			o := EncoderOptions{Width: 200, Height: 136, BitDepth: 8, QIndex: 100, FPSNum: 25, FPSDen: 1}
			checkDecodes(t, o, []*Source{testPicture(200, 136, 8, 1), testPicture(200, 136, 8, 2)})
		})
	}
}

func TestDbgCDFCarry(t *testing.T) {
	if os.Getenv("AV1DBG") == "" {
		t.Skip("AV1DBG unset")
	}
	defer func() { dbgNoCDFCarry = false }()
	for _, off := range []bool{true, false} {
		dbgNoCDFCarry = off
		o := EncoderOptions{Width: 200, Height: 136, BitDepth: 8, QIndex: 100, FPSNum: 25, FPSDen: 1}
		p := testPicture(200, 136, 8, 1)
		var pics []*Source
		for i := range 8 {
			pics = append(pics, shifted(p, i, i/2))
		}
		n, psnr := checkDecodesWith(t, o, pics, nil)
		t.Logf("carry off %v: %d bytes, %.2f dB", off, n, psnr)
	}
}

func TestDbgCompound(t *testing.T) {
	if os.Getenv("AV1DBG") == "" {
		t.Skip("AV1DBG unset")
	}
	defer func() { dbgForceCompound, dbgForceInter, dbgInterModes = false, false, 0 }()
	dbgForceCompound, dbgForceInter = true, true
	// NEAREST_NEAREST, NEAR_NEAR, GLOBAL_GLOBAL, NEW_NEW, all
	for _, m := range []int{1 << 0, 1 << 1, 1 << 2, 1 << 3, 1 << 4, 1 << 5, 1 << 6, 1 << 7, 0} {
		dbgInterModes = m
		t.Run(fmt.Sprint(m), func(t *testing.T) {
			o := EncoderOptions{Width: 96, Height: 64, BitDepth: 8, QIndex: 120, FPSNum: 25, FPSDen: 1, MiniGOP: 4}
			p := testPicture(96, 64, 8, 3)
			var pics []*Source
			for i := range 9 {
				pics = append(pics, shifted(p, i, i/3))
			}
			checkDecodes(t, o, pics)
		})
	}
}

func TestDbgSkipMode(t *testing.T) {
	if os.Getenv("AV1DBG") == "" {
		t.Skip("AV1DBG unset")
	}
	defer func() { dbgForceSkipMode, dbgForceInter = false, false }()
	for _, force := range []bool{true, false} {
		dbgForceSkipMode, dbgForceInter = force, force
		o := EncoderOptions{Width: 96, Height: 64, BitDepth: 8, QIndex: 120, FPSNum: 25, FPSDen: 1, MiniGOP: 4}
		p := testPicture(96, 64, 8, 3)
		var pics []*Source
		for i := range 9 {
			pics = append(pics, shifted(p, i, i/3))
		}
		checkDecodes(t, o, pics)
	}
}

// A q index a superblock (delta_q), the targets random.
func TestDeltaQ(t *testing.T) {
	t.Parallel()
	o := EncoderOptions{Width: 200, Height: 136, BitDepth: 8, QIndex: 120, FPSNum: 25, FPSDen: 1, MiniGOP: 4}
	p := testPicture(200, 136, 8, 1)
	var pics []*Source
	for i := range 6 {
		pics = append(pics, shifted(p, i, i/2))
	}
	// Frames are coded at once, so the hook has its own generator a
	// call, seeded by a counter.
	var calls atomic.Uint64
	checkDecodesWith(t, o, pics, func(e *Encoder) {
		e.sbQHook = func(n int) []int {
			rng := rand.New(rand.NewPCG(5, calls.Add(1)))
			q := make([]int, n)
			for i := range q {
				q[i] = 40 + rng.IntN(200)
			}
			return q
		}
	})
}

// bands is a picture whose bands of rows move differently: (dx, dy)
// scaled by -1, 0 and 1 in turn, 20 rows a band.
func bands(p *Source, dx, dy int) *Source {
	w, h := p.StrideY, len(p.Y)/p.StrideY
	cw := p.StrideC
	q := &Source{Y: make([]uint16, len(p.Y)), Cb: make([]uint16, len(p.Cb)), Cr: make([]uint16, len(p.Cr)), StrideY: w, StrideC: cw}
	for k := -1; k <= 1; k++ {
		m := shifted(p, k*dx, k*dy)
		for y := range h {
			if (y/20)%3 != k+1 {
				continue
			}
			copy(q.Y[y*w:(y+1)*w], m.Y[y*w:(y+1)*w])
			if y%2 == 0 {
				c := y / 2
				copy(q.Cb[c*cw:(c+1)*cw], m.Cb[c*cw:(c+1)*cw])
				copy(q.Cr[c*cw:(c+1)*cw], m.Cr[c*cw:(c+1)*cw])
			}
		}
	}
	return q
}

// seqPSNR is the luma PSNR of every picture of a sequence coded as o
// says, with the bytes.
func seqPSNR(t *testing.T, o EncoderOptions, pics []*Source) (int, float64) {
	t.Helper()
	e, err := NewEncoder(o)
	if err != nil {
		t.Fatal(err)
	}
	e.keepRecon = true
	n, se := 0, 0.0
	for i, u := range encodeAll(t, e, pics) {
		n += len(u.Data)
		src, rec := pics[i], u.recon[0]
		for y := range o.Height {
			for x := range o.Width {
				d := float64(src.Y[y*src.StrideY+x]) - float64(rec[y*o.Width+x])
				se += d * d
			}
		}
	}
	peak := float64(int(1)<<uint(o.BitDepth) - 1)
	return n, 10 * math.Log10(peak*peak/(se/float64(o.Width*o.Height*len(pics))))
}

// OBMC wherever it may be: moving pictures (bands of rows moving apart),
// compound blocks around the OBMC ones (mini-GOPs), tiles, frame edges
// inside the last blocks, 8 and 10 bits; then OBMC off and chosen, the
// bytes and PSNR logged.
func TestDbgObmc(t *testing.T) {
	if os.Getenv("AV1DBG") == "" {
		t.Skip("AV1DBG unset")
	}
	defer func() { dbgForceObmc, dbgForceInter, dbgNoObmc = false, false, false }()
	for _, c := range []struct{ w, h, depth, mini, cl int }{
		{96, 64, 8, 4, 0}, {200, 136, 8, 1, 0}, {200, 136, 10, 4, 1}, {96, 64, 10, 1, 0}, {196, 130, 8, 4, 1},
	} {
		p := testPicture(c.w, c.h, c.depth, 3)
		var pics []*Source
		for i := range 6 {
			pics = append(pics, bands(p, i*3/2, -i/2))
		}
		o := EncoderOptions{Width: c.w, Height: c.h, BitDepth: c.depth, QIndex: 120, FPSNum: 25, FPSDen: 1, MiniGOP: c.mini, TileColsLog2: c.cl}
		for _, force := range []bool{true, false} {
			dbgForceObmc, dbgForceInter = force, force
			t.Run(fmt.Sprintf("%dx%d-%d-%v", c.w, c.h, c.depth, force), func(t *testing.T) { checkDecodes(t, o, pics) })
		}
		dbgForceObmc, dbgForceInter = false, false
		for _, off := range []bool{true, false} {
			dbgNoObmc = off
			start := time.Now()
			n, psnr := seqPSNR(t, o, pics)
			t.Logf("%dx%d %d bits mini-GOP %d, OBMC off %v: %d bytes, %.3f dB, %v", c.w, c.h, c.depth, c.mini, off, n, psnr, time.Since(start).Round(time.Millisecond))
		}
		dbgNoObmc = false
	}
}
